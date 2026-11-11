package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	kplan "github.com/dcadolph/kibble/internal/plan"
)

// TestCommandWords checks that each simple command on a line yields its
// command name, so the tool a recipe needs is visible whatever shell
// punctuation surrounds it.
func TestCommandWords(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In       string
		WantCmds []string
	}{{ // Test 0: a plain command.
		In: "cargo build --release", WantCmds: []string{"cargo"},
	}, { // Test 1: sudo does not name the tool.
		In: "sudo make install", WantCmds: []string{"make"},
	}, { // Test 2: both sides of a conjunction count.
		In: "npm ci && npm run build", WantCmds: []string{"npm", "npm"},
	}, { // Test 3: a pipeline names every stage.
		In: "cat x | python3 -", WantCmds: []string{"cat", "python3"},
	}, { // Test 4: an environment assignment does not name the tool.
		In: "CGO_ENABLED=0 go build ./...", WantCmds: []string{"go"},
	}, { // Test 5: a prompt marker is not the tool.
		In: "$ cargo install --path .", WantCmds: []string{"cargo"},
	}, { // Test 6: a path yields the base name.
		In: "./scripts/build.sh", WantCmds: []string{"build.sh"},
	}, { // Test 7: a trailing comment is not a command.
		In: "make # build everything", WantCmds: []string{"make"},
	}, { // Test 8: a comment line yields nothing.
		In: "# just a note", WantCmds: nil,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := commandWords(test.In)
			if diff := cmp.Diff(test.WantCmds, got); diff != "" {
				t.Errorf("commands mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestDetectToolchain checks that a recipe resolves to the image providing the
// tools it assumes, and that an unrecognized recipe resolves to nothing so the
// caller keeps its default.
func TestDetectToolchain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Manifests []string
		Lines     []string
		WantImage string
		WantOK    bool
	}{{ // Test 0: a cargo recipe needs the rust image.
		Lines:  []string{"git clone https://github.com/o/r", "cd r", "cargo build --release"},
		WantOK: true, WantImage: "rust:1",
	}, { // Test 1: an npm recipe needs the node image.
		Lines:  []string{"cd r", "npm ci", "npm run build"},
		WantOK: true, WantImage: "node:22",
	}, { // Test 2: a pip recipe needs the python image.
		Lines:  []string{"pip install -r requirements.txt"},
		WantOK: true, WantImage: "python:3",
	}, { // Test 3: go resolves to the configured default, not a fixed image.
		Lines:  []string{"go build ./..."},
		WantOK: true, WantImage: "",
	}, { // Test 4: neutral commands alone identify nothing.
		Lines:  []string{"cd r", "make install"},
		WantOK: false,
	}, { // Test 5: a manifest settles what neutral commands cannot.
		Lines: []string{"cd r", "make install"}, Manifests: []string{"Cargo.toml"},
		WantOK: true, WantImage: "rust:1",
	}, { // Test 6: the recipe's own commands outrank the manifest.
		Lines: []string{"npm ci"}, Manifests: []string{"Cargo.toml"},
		WantOK: true, WantImage: "node:22",
	}, { // Test 7: the ecosystem named most often wins.
		Lines:  []string{"go build ./...", "npm ci", "npm run build"},
		WantOK: true, WantImage: "node:22",
	}, { // Test 8: manifests naming different ecosystems settle nothing.
		Lines: []string{"cd r", "make install"}, Manifests: []string{"go.mod", "package.json"},
		WantOK: false,
	}, { // Test 9: manifests naming one ecosystem still settle it.
		Lines:     []string{"cd r", "make install"},
		Manifests: []string{"pyproject.toml", "requirements.txt"},
		WantOK:    true, WantImage: "python:3",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, m := range test.Manifests {
				if err := os.WriteFile(filepath.Join(dir, m), []byte("x"), 0o600); err != nil {
					t.Fatalf("write manifest: %v", err)
				}
			}
			got, ok := detectToolchain(test.Lines, dir)
			if diff := cmp.Diff(test.WantOK, ok); diff != "" {
				t.Errorf("ok mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantImage, got.Image); diff != "" {
				t.Errorf("image mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestMissingCommand checks that a container reporting an absent tool is
// recognized, so kibble reports its own gap instead of blaming the document.
func TestMissingCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In       string
		WantName string
		WantOK   bool
	}{{ // Test 0: bash names the line before the command.
		In:     "bash: line 3: cargo: command not found",
		WantOK: true, WantName: "cargo",
	}, { // Test 1: the short form is recognized too.
		In:     "/bin/sh: 1: npm: not found",
		WantOK: true, WantName: "npm",
	}, { // Test 2: a real compile error is not a missing command.
		In:     "error[E0425]: cannot find value `x` in this scope",
		WantOK: false,
	}, { // Test 3: the marker is found among surrounding output.
		In:     "building\nbash: line 9: pnpm: command not found\nexit 127",
		WantOK: true, WantName: "pnpm",
	}, { // Test 4: empty output names nothing.
		In: "", WantOK: false,
	}, { // Test 5: the Go toolchain's exec-not-found is a missing build tool.
		In:     `lib/results.go:272: running "easyjson": exec: "easyjson": executable file not found in $PATH`,
		WantOK: true, WantName: "easyjson",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got, ok := missingCommand(test.In)
			if diff := cmp.Diff(test.WantOK, ok); diff != "" {
				t.Errorf("ok mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantName, got); diff != "" {
				t.Errorf("name mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestEscapeAnnotation checks that workflow command messages survive the
// characters GitHub reserves.
func TestEscapeAnnotation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want string
	}{{ // Test 0: percent escapes first so later escapes are not doubled.
		In: "50% done\nnext", Want: "50%25 done%0Anext",
	}, { // Test 1: plain text is unchanged.
		In: "plain", Want: "plain",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, escapeAnnotation(test.In)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestPythonImage checks that a stated upper bound on Python picks the newest
// image it allows, that an unbounded or absent range keeps the default, and
// that a range no Python 3 image satisfies is blocked rather than run.
func TestPythonImage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Files       map[string]string
		WantImage   string
		WantBlocked bool
	}{{ // Test 0: aider's pyproject bound picks python:3.14.
		Files:     map[string]string{"pyproject.toml": "[project]\nrequires-python = \">=3.10,<3.15\"\n"},
		WantImage: "python:3.14",
	}, { // Test 1: an inclusive bound picks that release.
		Files:     map[string]string{"pyproject.toml": "requires-python = '<=3.14'\n"},
		WantImage: "python:3.14",
	}, { // Test 2: a compatible release on a minor bounds nothing.
		Files:     map[string]string{"pyproject.toml": "requires-python = \"~=3.12\"\n"},
		WantImage: "python:3",
	}, { // Test 3: a compatible release on a patch stops at its minor.
		Files:     map[string]string{"pyproject.toml": "requires-python = \"~=3.12.0\"\n"},
		WantImage: "python:3.12",
	}, { // Test 4: a lower bound alone keeps the default.
		Files:     map[string]string{"pyproject.toml": "requires-python = \">=3.10\"\n"},
		WantImage: "python:3",
	}, { // Test 5: no stated range keeps the default.
		Files:     map[string]string{"README.md": "# Tool\n"},
		WantImage: "python:3",
	}, { // Test 6: setup.cfg is read when there is no pyproject.toml.
		Files:     map[string]string{"setup.cfg": "[options]\npython_requires = >=3.9, <3.13\n"},
		WantImage: "python:3.12",
	}, { // Test 7: markitdown's README range is used when metadata says nothing.
		Files:     map[string]string{"README.md": "Requires Python 3.10 through 3.14.\n"},
		WantImage: "python:3.14",
	}, { // Test 8: pyproject wins over the README.
		Files: map[string]string{"pyproject.toml": "requires-python = \"<3.13\"\n",
			"README.md": "Python 3.10 through 3.14\n"},
		WantImage: "python:3.12",
	}, { // Test 9: a bound below Python 3 is blocked.
		Files:       map[string]string{"pyproject.toml": "requires-python = \"<3\"\n"},
		WantBlocked: true,
	}, { // Test 10: a patch above zero still allows its minor.
		Files:     map[string]string{"pyproject.toml": "requires-python = \">=3.8,<3.13.2\"\n"},
		WantImage: "python:3.13",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, body := range test.Files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			image, blocked := pythonImage(dir)
			if diff := cmp.Diff(test.WantImage, image); diff != "" {
				t.Errorf("image mismatch (-want +got):\n%s", diff)
			}
			if got := blocked != ""; got != test.WantBlocked {
				t.Errorf("blocked = %q, want blocked %v", blocked, test.WantBlocked)
			}
		})
	}
}

// TestPythonStepImage checks that a pip install step and a Python example
// session both run on the image the checkout's range picks.
func TestPythonStepImage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"),
		[]byte("requires-python = \">=3.10,<3.15\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &DockerRunner{Image: "golang:1"}
	if image, _ := d.imageFor(InstallStep{Kind: "pip-install", dir: dir}); image != "python:3.14" {
		t.Errorf("install image = %q, want python:3.14", image)
	}
	plan := &kplan.Plan{Installs: []kplan.PlanInstall{{Ecosystem: "python"}}}
	if image, ok, _ := d.exampleImage(plan, dir); !ok || image != "python:3.14" {
		t.Errorf("session image = %q ok=%v, want python:3.14", image, ok)
	}
}
