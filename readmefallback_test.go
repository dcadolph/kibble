package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestReadREADMEFallsBackToDocsTree checks that a repository keeping its front
// document in a documentation tree is read rather than abandoned. pipx has no
// README at its root and keeps docs/README.md, and kibble reported nothing at
// all for it, which is the worst answer available: not a verdict, not a gap,
// just silence about a repository whose instructions it could have read.
func TestReadREADMEFallsBackToDocsTree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Name     string
		Files    map[string]string
		WantName string
		WantErr  bool
	}{{ // Test 0: a root README still wins, and nothing else is consulted.
		Name:     "root wins",
		Files:    map[string]string{"README.md": "root", "docs/README.md": "docs"},
		WantName: "README.md",
	}, { // Test 1: no root README, so the docs tree supplies the front document.
		Name:     "docs fallback",
		Files:    map[string]string{"docs/README.md": "docs"},
		WantName: "docs/README.md",
	}, { // Test 2: the singular directory name is honored too.
		Name:     "doc fallback",
		Files:    map[string]string{"doc/README.md": "doc"},
		WantName: "doc/README.md",
	}, { // Test 3: docs is preferred over doc when a repository has both, so
		// the choice does not depend on directory iteration order.
		Name:     "docs before doc",
		Files:    map[string]string{"doc/README.md": "doc", "docs/README.md": "docs"},
		WantName: "docs/README.md",
	}, { // Test 4: nothing anywhere is still an honest error, not an empty pass.
		Name:    "nothing to read",
		Files:   map[string]string{"docs/index.rst": "not markdown"},
		WantErr: true,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, body := range test.Files {
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			body, name, err := readREADME(dir)
			if test.WantErr {
				if err == nil {
					t.Errorf("got %q with no error, want an error", name)
				}
				return
			}
			if err != nil {
				t.Fatalf("readREADME: %v", err)
			}
			if diff := cmp.Diff(test.WantName, name); diff != "" {
				t.Errorf("name mismatch (-want +got):\n%s", diff)
			}
			if body == "" {
				t.Errorf("read %s but its contents came back empty", name)
			}
		})
	}
}
