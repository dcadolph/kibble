package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dcadolph/kibble/internal/config"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestReplayDocs checks which documents are chosen for replay. Selection is
// conservative: a document run by mistake spends a container and reports a
// failure about instructions nobody was following.
func TestReplayDocs(t *testing.T) {
	t.Parallel()
	shell := "# T\n```sh\ntool run\n```\n"
	prose := "# T\n\nNo commands here.\n"
	tests := []struct {
		Files map[string]string
		Cfg   *config.ExamplesConfig
		Want  []string
	}{{ // Test 0: a docs tree is replayed beside the README.
		Files: map[string]string{"README.md": shell, "docs/quickstart.md": shell},
		Want:  []string{"README.md", "docs/quickstart.md"},
	}, { // Test 1: a contributing guide tells maintainers to run tests, which
		// is not what a reader is being asked to do.
		Files: map[string]string{"README.md": shell, "CONTRIBUTING.md": shell},
		Want:  []string{"README.md"},
	}, { // Test 2: a top-level guide is replayed by name.
		Files: map[string]string{"README.md": shell, "GETTING_STARTED.md": shell},
		Want:  []string{"README.md", "GETTING_STARTED.md"},
	}, { // Test 2a: a usage guide beside the README is where the commands a
		// reader copies usually live, whatever the project calls it.
		Files: map[string]string{"README.md": shell, "GUIDE.md": shell, "FAQ.md": shell},
		Want:  []string{"README.md", "FAQ.md", "GUIDE.md"},
	}, { // Test 3: a document with nothing to run costs a container and
		// proves nothing.
		Files: map[string]string{"README.md": shell, "docs/design.md": prose},
		Want:  []string{"README.md"},
	}, { // Test 4: generated and vendored trees are left alone.
		Files: map[string]string{"README.md": shell, "node_modules/pkg/docs/a.md": shell,
			"site/docs/b.md": shell},
		Want: []string{"README.md"},
	}, { // Test 5: a repository adds what the convention misses.
		Files: map[string]string{"README.md": shell, "walkthrough.md": shell},
		Cfg:   &config.ExamplesConfig{Docs: []string{"walkthrough.md"}},
		Want:  []string{"README.md", "walkthrough.md"},
	}, { // Test 6: a repository drops what the convention picks up, but the
		// README is never dropped.
		Files: map[string]string{"README.md": shell, "docs/wip.md": shell},
		Cfg:   &config.ExamplesConfig{SkipDocs: []string{"docs/wip.md", "README.md"}},
		Want:  []string{"README.md"},
	}, { // Test 7: a versioned directory beside unversioned docs is old.
		Files: map[string]string{"README.md": shell, "docs/@v1/a.md": shell, "docs/b.md": shell},
		Want:  []string{"README.md", "docs/b.md"},
	}, { // Test 8: with only versioned siblings the highest is current.
		Files: map[string]string{"README.md": shell, "docs/v1/a.md": shell, "docs/v2/a.md": shell},
		Want:  []string{"README.md", "docs/v2/a.md"},
	}, { // Test 9: a 1.x directory beside a named guide directory is old.
		Files: map[string]string{"README.md": shell, "docs/1.x/a.md": shell,
			"docs/guide/a.md": shell},
		Want: []string{"README.md", "docs/guide/a.md"},
	}, { // Test 10: a versioned_docs tree is never selected.
		Files: map[string]string{"README.md": shell, "versioned_docs/version-1.2/a.md": shell,
			"docs/versioned_docs/version-1.2/a.md": shell},
		Want: []string{"README.md"},
	}, { // Test 11: a snippets directory with no markdown is not the current
		// docs, so the highest version still wins.
		Files: map[string]string{"README.md": shell, "docs/@v1/a.md": shell,
			"docs/@v2/a.md": shell, "docs/_code-snippets/rules.yaml": "a: 1\n"},
		Want: []string{"README.md", "docs/@v2/a.md"},
	}, { // Test 12: directories named for years are not versions.
		Files: map[string]string{"README.md": shell, "docs/2023/a.md": shell,
			"docs/2024/a.md": shell},
		Want: []string{"README.md", "docs/2023/a.md", "docs/2024/a.md"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, body := range test.Files {
				full := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}
			got := replayDocs(dir, "README.md", test.Cfg)
			if diff := cmp.Diff(test.Want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestInstallDocs checks that install-step extraction reaches install
// documents beyond the README while staying narrower than example replay, so a
// tutorial or FAQ that shows a command in passing is not read as an install.
func TestInstallDocs(t *testing.T) {
	t.Parallel()
	body := "# T\n```sh\ngo install example.com/tool@latest\n```\n"
	tests := []struct {
		Files map[string]string
		Want  []string
	}{{ // Test 0: an installation guide under docs is read.
		Files: map[string]string{"README.md": body, "docs/installation.md": body},
		Want:  []string{"README.md", "docs/installation.md"},
	}, { // Test 1: a top-level INSTALL file is read.
		Files: map[string]string{"README.md": body, "INSTALL.md": body},
		Want:  []string{"README.md", "INSTALL.md"},
	}, { // Test 2: a getting-started guide counts as install instructions.
		Files: map[string]string{"README.md": body, "docs/getting-started.md": body},
		Want:  []string{"README.md", "docs/getting-started.md"},
	}, { // Test 3: an FAQ or tutorial is not an install document, unlike replay.
		Files: map[string]string{"README.md": body, "FAQ.md": body, "docs/tutorial.md": body},
		Want:  []string{"README.md"},
	}, { // Test 4: a contributing guide is never read.
		Files: map[string]string{"README.md": body, "INSTALLING-CONTRIBUTORS.md": body,
			"CONTRIBUTING.md": body},
		Want: []string{"README.md", "INSTALLING-CONTRIBUTORS.md"},
	}, { // Test 5: generated and vendored trees are left alone.
		Files: map[string]string{"README.md": body, "node_modules/pkg/INSTALL.md": body},
		Want:  []string{"README.md"},
	}, { // Test 6: an index under a guide or install-named directory is read.
		Files: map[string]string{"README.md": body, "docs/guide/index.md": body,
			"docs/getting-started/index.md": body},
		Want: []string{"README.md", "docs/getting-started/index.md", "docs/guide/index.md"},
	}, { // Test 7: an index under docs or another directory is a landing page.
		Files: map[string]string{"README.md": body, "docs/index.md": body,
			"docs/config/index.md": body, "index.md": body},
		Want: []string{"README.md"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, b := range test.Files {
				full := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(full, []byte(b), 0o600); err != nil {
					t.Fatalf("write %s: %v", name, err)
				}
			}
			got := installDocs(dir, "README.md")
			if diff := cmp.Diff(test.Want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
