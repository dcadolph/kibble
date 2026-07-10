package main

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestClassifyLine checks that install commands are recognized and classified.
func TestClassifyLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In       string
		WantKind string
		WantMod  string
		WantRun  bool
		WantOK   bool
	}{{ // Test 0: go install with subdir and version.
		In:       "go install github.com/dcadolph/cipher/cmd/cipher@latest",
		WantKind: "go-install", WantMod: "github.com/dcadolph/cipher/cmd/cipher@latest",
		WantRun: true, WantOK: true,
	}, { // Test 1: go install with a trailing comment.
		In:       "go install github.com/dcadolph/slop-chop@latest   # lands in $(go env GOPATH)/bin",
		WantKind: "go-install", WantMod: "github.com/dcadolph/slop-chop@latest",
		WantRun: true, WantOK: true,
	}, { // Test 2: brew install is recognized but not executed by v1.
		In:       "brew install dcadolph/tap/vamoose",
		WantKind: "brew", WantMod: "dcadolph/tap/vamoose", WantRun: false, WantOK: true,
	}, { // Test 3: git clone is recognized but not executed by v1.
		In:       "git clone https://github.com/dcadolph/cipher && cd cipher && make install",
		WantKind: "git-clone", WantMod: "https://github.com/dcadolph/cipher",
		WantRun: false, WantOK: true,
	}, { // Test 4: plain prose is not an install command.
		In: "Run the doctor command to check your setup.", WantOK: false,
	}, { // Test 5: go install without a version is not matched.
		In: "go install ./...", WantOK: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got, _, ok := classifyLine("repo", test.In)
			if ok != test.WantOK {
				t.Fatalf("ok mismatch: want %v got %v", test.WantOK, ok)
			}
			if !ok {
				return
			}
			if diff := cmp.Diff(test.WantKind, got.Kind); diff != "" {
				t.Errorf("kind mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantMod, got.Module); diff != "" {
				t.Errorf("module mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantRun, got.Run); diff != "" {
				t.Errorf("run mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestExtract checks extraction across fenced, inline, and indented code, and dedup.
func TestExtract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In        string
		WantCount int
		WantBins  []string
	}{{ // Test 0: fenced, inline, and indented installs are all found.
		In: "Install it.\n\n```sh\ngo install github.com/x/y@latest\n```\n\n" +
			"Or `go install github.com/x/z/cmd/z@v1.2.3` inline.\n\n    brew install tap/thing\n",
		WantCount: 3, WantBins: []string{"y", "z"},
	}, { // Test 1: the same go install written twice is deduplicated.
		In: "```sh\ngo install github.com/x/y@latest\n```\n\nlater: " +
			"`go install github.com/x/y@latest`\n",
		WantCount: 1, WantBins: []string{"y"},
	}, { // Test 2: a README with no install commands yields nothing.
		In: "# Title\n\nSome prose with no commands.\n", WantCount: 0,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			steps := DefaultExtractor().Extract("repo", test.In)
			if diff := cmp.Diff(test.WantCount, len(steps)); diff != "" {
				t.Fatalf("count mismatch (-want +got):\n%s\nsteps: %+v", diff, steps)
			}
			var bins []string
			for _, s := range steps {
				if s.Kind == "go-install" {
					bins = append(bins, s.Binary)
				}
			}
			if diff := cmp.Diff(test.WantBins, bins); diff != "" {
				t.Errorf("binaries mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
