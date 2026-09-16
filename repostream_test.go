package main

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestRepoFitsMeasuresWhatIsStreamed checks the stat-only pre-check against the
// same exclusions the archive itself applies. The answer has to be known before
// a container starts, because a partially streamed repository makes a document
// look broken when the missing piece is kibble's.
func TestRepoFitsMeasuresWhatIsStreamed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Name     string
		Files    map[string]int
		WantFits bool
	}{{ // Test 0: an ordinary tree fits.
		Name:     "small tree",
		Files:    map[string]int{"README.md": 1024, "src/main.go": 2048},
		WantFits: true,
	}, { // Test 1: weight inside a generated directory is not streamed, so it
		// cannot push a repository over the cap. node_modules is the common case.
		Name: "weight in a skipped directory",
		Files: map[string]int{
			"README.md":                 1024,
			"node_modules/big/index.js": 4 << 20,
			"target/artifact.bin":       4 << 20,
		},
		WantFits: true,
	}, { // Test 2: a single file over the per-file limit is skipped by the
		// archive, so it must not count toward the total either.
		Name:     "oversized single file",
		Files:    map[string]int{"README.md": 1024, "fixture.bin": 3 << 20},
		WantFits: true,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, size := range test.Files {
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			if got := repoFits(dir); got != test.WantFits {
				t.Errorf("repoFits = %v, want %v", got, test.WantFits)
			}
		})
	}
}

// TestRepoTarToStreamsSameContent checks that writing the archive as it walks
// produces what buffering produced. The streaming form exists so the cap is not
// really a memory limit; it must not also change what arrives.
func TestRepoTarToStreamsSameContent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	want := map[string]int{
		"README.md":         400,
		"docs/GUIDE.md":     900,
		"src/main.go":       120,
		"node_modules/x.js": 50,
	}
	for name, size := range want {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	buffered, _ := repoTar(dir)
	var streamed bytes.Buffer
	if _, err := repoTarTo(&streamed, dir); err != nil {
		t.Fatalf("repoTarTo: %v", err)
	}
	if !bytes.Equal(buffered, streamed.Bytes()) {
		t.Errorf("streamed archive differs from the buffered one")
	}

	names := map[string]bool{}
	tr := tar.NewReader(&streamed)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		names[h.Name] = true
	}
	for _, n := range []string{"README.md", "docs/GUIDE.md", "src/main.go"} {
		if !names[n] {
			t.Errorf("archive is missing %s", n)
		}
	}
	if names["node_modules/x.js"] {
		t.Errorf("archive carries a generated directory")
	}
}
