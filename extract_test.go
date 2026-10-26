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
		In:       "go install example.com/tool/cmd/mytool@latest",
		WantKind: "go-install", WantMod: "example.com/tool/cmd/mytool@latest",
		WantRun: true, WantOK: true,
	}, { // Test 1: go install with a trailing comment.
		In:       "go install example.com/tool@latest   # lands in $(go env GOPATH)/bin",
		WantKind: "go-install", WantMod: "example.com/tool@latest",
		WantRun: true, WantOK: true,
	}, { // Test 2: brew install is recognized and verified.
		In:       "brew install example/tap/mytool",
		WantKind: "brew", WantMod: "example/tap/mytool", WantRun: true, WantOK: true,
	}, { // Test 3: git clone is recognized and its recipe is executed.
		In:       "git clone https://example.com/tool && cd tool && make install",
		WantKind: "git-clone", WantMod: "https://example.com/tool",
		WantRun: true, WantOK: true,
	}, { // Test 4: plain prose is not an install command.
		In: "Run the doctor command to check your setup.", WantOK: false,
	}, { // Test 5: go install without a version is not matched.
		In: "go install ./...", WantOK: false,
	}, { // Test 6: leading flags before the module are skipped.
		In:       "go install -v github.com/x/y@latest",
		WantKind: "go-install", WantMod: "github.com/x/y@latest", WantRun: true, WantOK: true,
	}, { // Test 7: a piped shell installer is recorded but not run.
		In:       "curl -sSfL https://example.com/install.sh | sh",
		WantKind: "script-install", WantMod: "https://example.com/install.sh",
		WantRun: false, WantOK: true,
	}, { // Test 8: sh -c "$(curl ...)" is also a piped installer.
		In:       `sh -c "$(curl -fsSL https://example.com/i.sh)"`,
		WantKind: "script-install", WantMod: "https://example.com/i.sh",
		WantRun: false, WantOK: true,
	}, { // Test 9: a system package install is recorded but not run.
		In:       "sudo apt install ripgrep",
		WantKind: "os-package", WantMod: "apt:ripgrep", WantRun: false, WantOK: true,
	}, { // Test 10: a package manager flag before the name is skipped.
		In:       "sudo dnf install -y fd-find",
		WantKind: "os-package", WantMod: "dnf:fd-find", WantRun: false, WantOK: true,
	}, { // Test 11: A clone of a placeholder URL is recorded but not run.
		In:       "git clone <some-url>/my-dotfiles",
		WantKind: "git-clone", WantMod: "<some-url>/my-dotfiles", WantRun: false, WantOK: true,
	}, { // Test 12: A clone of a real URL still runs.
		In:       "git clone https://github.com/x/y.git",
		WantKind: "git-clone", WantMod: "https://github.com/x/y.git", WantRun: true, WantOK: true,
	}, { // Test 13: A clone under a stand-in owner is a placeholder.
		In:       "git clone https://github.com/example/dotfiles",
		WantKind: "git-clone", WantMod: "https://github.com/example/dotfiles",
		WantRun: false, WantOK: true,
	}, { // Test 14: A go install of a placeholder module is not run.
		In:       "go install <module>@latest",
		WantKind: "go-install", WantMod: "<module>@latest", WantRun: false, WantOK: true,
	}, { // Test 15: A pip install of a placeholder package is not run.
		In:       "pip install <package>",
		WantKind: "pip-install", WantMod: "<package>", WantRun: false, WantOK: true,
	}, { // Test 16: Python extras are not a placeholder, so the install runs.
		In:       "pip install 'httpie[socks]'",
		WantKind: "pip-install", WantMod: "httpie", WantRun: true, WantOK: true,
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

// TestInstallRecipeCapture checks that a git-clone step carries the rest of
// its code block as the install recipe.
func TestInstallRecipeCapture(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In        string
		WantBlock []string
	}{{ // Test 0: clone plus following build lines in one fenced block.
		In:        "```sh\ngit clone git@github.com:x/y.git\ncd y\nmake install   # build it\n```\n",
		WantBlock: []string{"git clone git@github.com:x/y.git", "cd y", "make install"},
	}, { // Test 1: a one-line clone with && chain keeps the whole line.
		In:        "```sh\ngit clone https://github.com/x/y && cd y && make install\n```\n",
		WantBlock: []string{"git clone https://github.com/x/y && cd y && make install"},
	}, { // Test 2: an inline clone span is its own one-line recipe.
		In:        "Or `git clone https://github.com/x/y.git` to start.\n",
		WantBlock: []string{"git clone https://github.com/x/y.git"},
	}, { // Test 3: lines before the clone are not part of the recipe.
		In:        "```sh\nbrew update\ngit clone https://github.com/x/y\ncd y\n```\n",
		WantBlock: []string{"git clone https://github.com/x/y", "cd y"},
	}, { // Test 4: capture stops before a documented teardown line.
		In:        "```sh\ngit clone https://github.com/x/y\ncd y\nmake install\nmake uninstall\n```\n",
		WantBlock: []string{"git clone https://github.com/x/y", "cd y", "make install"},
	}, { // Test 5: in a prompted block, printed output is not a command.
		In: "```\n$ git clone https://github.com/x/y\n$ cd y\n$ cargo build --release\n" +
			"$ ./target/release/y --version\n0.1.3\n```\n",
		WantBlock: []string{
			"git clone https://github.com/x/y", "cd y", "cargo build --release",
			"./target/release/y --version",
		},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			steps := DefaultExtractor().Extract("repo", test.In)
			var clone *InstallStep
			for i := range steps {
				if steps[i].Kind == "git-clone" {
					clone = &steps[i]
					break
				}
			}
			if clone == nil {
				t.Fatalf("no git-clone step extracted from %q", test.In)
			}
			if diff := cmp.Diff(test.WantBlock, clone.Block); diff != "" {
				t.Errorf("block mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestBinaryName checks the binary name go install produces, including the
// major-version suffix case that a plain path.Base would get wrong.
func TestBinaryName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want string
	}{{ // Test 0: command under a cmd directory.
		In: "example.com/tool/cmd/mytool@latest", Want: "mytool",
	}, { // Test 1: main package at the module root.
		In: "example.com/tool@latest", Want: "tool",
	}, { // Test 2: a v2 module root uses the element before the version suffix.
		In: "github.com/foo/bar/v2@latest", Want: "bar",
	}, { // Test 3: a command under a versioned module keeps its own name.
		In: "github.com/foo/bar/v3/cmd/baz@latest", Want: "baz",
	}, { // Test 4: a path with no version segment.
		In: "example.com/tool", Want: "tool",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, binaryName(test.In)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestClassifyPackage checks that documented package installs are recognized
// with the right kind and target, and that a local build is left to the clone
// recipe rather than treated as a package install.
func TestClassifyPackage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In       string
		WantKind string
		WantPkg  string
		WantOK   bool
	}{{ // Test 0: a crate install.
		In: "cargo install ripgrep", WantOK: true,
		WantKind: "cargo-install", WantPkg: "ripgrep",
	}, { // Test 1: flags before the crate do not hide it.
		In: "cargo install --locked ripgrep", WantOK: true,
		WantKind: "cargo-install", WantPkg: "ripgrep",
	}, { // Test 2: a local build belongs to the clone recipe.
		In: "cargo install --path .", WantOK: false,
	}, { // Test 3: a global node install.
		In: "npm install -g prettier", WantOK: true,
		WantKind: "npm-install", WantPkg: "prettier",
	}, { // Test 4: the short form counts too.
		In: "npm i -g prettier", WantOK: true,
		WantKind: "npm-install", WantPkg: "prettier",
	}, { // Test 5: a project dependency install is not a tool install.
		In: "npm install prettier", WantOK: false,
	}, { // Test 6: yarn spells global differently.
		In: "yarn global add prettier", WantOK: true,
		WantKind: "npm-install", WantPkg: "prettier",
	}, { // Test 7: a pipx install.
		In: "pipx install black", WantOK: true,
		WantKind: "pipx-install", WantPkg: "black",
	}, { // Test 8: a uv tool install.
		In: "uv tool install ruff", WantOK: true,
		WantKind: "uv-install", WantPkg: "ruff",
	}, { // Test 9: sudo does not hide the install.
		In: "sudo npm install -g prettier", WantOK: true,
		WantKind: "npm-install", WantPkg: "prettier",
	}, { // Test 10: an unrelated line matches nothing.
		In: "go build ./...", WantOK: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			kind, pkg, ok := classifyPackage("", test.In)
			if diff := cmp.Diff(test.WantOK, ok); diff != "" {
				t.Errorf("ok mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantKind, kind); diff != "" {
				t.Errorf("kind mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantPkg, pkg); diff != "" {
				t.Errorf("package mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestPackageBinary checks the fallback binary name guessed for a package,
// used only when the install adds nothing detectable to the bin directory.
func TestPackageBinary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In       string
		WantName string
	}{{ // Test 0: a plain package keeps its name.
		In: "prettier", WantName: "prettier",
	}, { // Test 1: a scope is not part of the binary name.
		In: "@biomejs/biome", WantName: "biome",
	}, { // Test 2: a pinned version is not part of the name.
		In: "prettier@3.3.3", WantName: "prettier",
	}, { // Test 3: a scoped and pinned request reduces to the name.
		In: "@biomejs/biome@1.8.0", WantName: "biome",
	}, { // Test 4: a git URL with a ref names its repository, not name.git.
		In: "git+https://github.com/contributor/pipx.git@fix-something", WantName: "pipx",
	}, { // Test 5: an ssh git URL names its repository.
		In: "git+ssh://git@github.com/psf/black", WantName: "black",
	}, { // Test 6: an archive URL names its repository, not the archive file.
		In: "https://github.com/psf/black/archive/18.9b0.zip", WantName: "black",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.WantName, packageBinary(test.In)); diff != "" {
				t.Errorf("binary mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestCloneTarget checks the repository is read past any flags. Taking the
// first token after the subcommand reported --depth as the repository for
// fzf's documented clone, and the recipe that followed passed without ever
// cloning anything.
func TestCloneTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want string
	}{{ // Test 0: the plain form.
		In: "git clone https://github.com/example/tool.git", Want: "https://github.com/example/tool.git",
	}, { // Test 1: a flag with a value sits before the target.
		In:   "git clone --depth 1 https://github.com/example/tool.git ~/.tool",
		Want: "https://github.com/example/tool.git",
	}, { // Test 2: several flags, one of them attached to its value.
		In:   "git clone -b main --depth=1 --recursive https://github.com/example/tool.git",
		Want: "https://github.com/example/tool.git",
	}, { // Test 3: an ssh remote is a remote.
		In: "git clone git@github.com:example/tool.git", Want: "git@github.com:example/tool.git",
	}, { // Test 4: a branch name is not mistaken for the target.
		In: "git clone -b v2 https://example.com/t.git", Want: "https://example.com/t.git",
	}, { // Test 5: a bare local path is used when nothing remote-shaped is there.
		In: "git clone --quiet mirror", Want: "mirror",
	}, { // Test 6: a prompt marker and quotes do not become part of the target.
		In: `git clone "https://example.com/t.git"`, Want: "https://example.com/t.git",
	}, { // Test 7: a branch value holding a slash is not the target.
		In:   "git clone -b release/v2 https://github.com/org/repo.git",
		Want: "https://github.com/org/repo.git",
	}, { // Test 8: a --branch value with a slash, separate token form.
		In:   "git clone --branch feature/x https://github.com/org/repo.git",
		Want: "https://github.com/org/repo.git",
	}, { // Test 9: a --reference path is a flag value, not the target.
		In:   "git clone --reference /srv/mirror/repo.git https://github.com/org/repo.git",
		Want: "https://github.com/org/repo.git",
	}, { // Test 10: a config value holding a slash is skipped.
		In:   "git clone -c core.hooksPath=/dev/null https://github.com/org/repo.git",
		Want: "https://github.com/org/repo.git",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := cloneTarget(test.In); got != test.Want {
				t.Errorf("cloneTarget = %q, want %q", got, test.Want)
			}
		})
	}
}

// TestPlaceholderInstallNotRun checks that an install line holding a
// placeholder is reported as one and never becomes the session's install.
// dotdrop documents `git clone <some-url>/my-dotfiles`, which ran and failed.
func TestPlaceholderInstallNotRun(t *testing.T) {
	t.Parallel()

	step, _, ok := classifyLine("my-dotfiles", "git clone <some-url>/my-dotfiles")
	if !ok {
		t.Fatal("placeholder clone not recognized")
	}
	res := unrunResult(step)
	if res.Status != StatusSkipped || res.Reason != ReasonPlaceholder {
		t.Errorf("verdict = %s %q, want %s %q", res.Status, res.Reason, StatusSkipped, ReasonPlaceholder)
	}
	if diff := cmp.Diff("docs use a placeholder the reader must fill in", res.Detail); diff != "" {
		t.Errorf("detail mismatch (-want +got):\n%s", diff)
	}

	step.Block = []string{"git clone <some-url>/my-dotfiles", "cd my-dotfiles", "make install"}
	tests := []struct {
		WantCount int
		Run       bool
	}{{ // Test 0: The placeholder clone is not the session's install.
		Run: false, WantCount: 0,
	}, { // Test 1: The same recipe marked runnable is, which shows the check can fail.
		Run: true, WantCount: 1,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			s := step
			s.Run = test.Run
			_, got := sessionInstalls(t.TempDir(), []InstallStep{s})
			if len(got) != test.WantCount {
				t.Errorf("installs = %d, want %d (%v)", len(got), test.WantCount, got)
			}
		})
	}
}

// nvmGitInstall is nvm's README lines 319 to 327 verbatim at the pinned commit.
const nvmGitInstall = "### Git Install\n" +
	"\n" +
	"If you have `git` installed (requires git v1.7.10+):\n" +
	"\n" +
	"1. clone this repo in the root of your user profile\n" +
	"    - `cd ~/` from anywhere then `git clone https://github.com/nvm-sh/nvm.git .nvm`\n" +
	"1. `cd ~/.nvm` and check out the latest version with `git checkout v0.40.8`\n" +
	"1. activate `nvm` by sourcing it from your shell: `. ./nvm.sh`\n" +
	"\n"

// TestExtractListRecipe checks that a clone written as a span in a list takes
// the recipe from the spans of its outermost list, starting at its own item,
// and keeps the clone alone when a span is not a recipe step.
func TestExtractListRecipe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In        string
		WantBlock []string
	}{{ // Test 0: nvm's git install, with the nvm prose span dropped.
		In: nvmGitInstall,
		WantBlock: []string{
			"cd ~/", "git clone https://github.com/nvm-sh/nvm.git .nvm", "cd ~/.nvm",
			"git checkout v0.40.8", ". ./nvm.sh",
		},
	}, { // Test 1: a span that is neither a recipe step nor one word keeps the clone alone.
		In:        "1. `git clone https://github.com/o/tool.git`\n1. `now run the tool`\n",
		WantBlock: []string{"git clone https://github.com/o/tool.git"},
	}, { // Test 2: a code block inside the list keeps the clone alone.
		In:        "1. `git clone https://github.com/o/tool.git`\n1. build it:\n\n   ```sh\n   make\n   ```\n",
		WantBlock: []string{"git clone https://github.com/o/tool.git"},
	}, { // Test 3: a clone outside a list is read from its own block as before.
		In:        "```sh\ngit clone https://github.com/o/tool.git\ncd tool\nmake\n```\n",
		WantBlock: []string{"git clone https://github.com/o/tool.git", "cd tool", "make"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, s := range DefaultExtractor().Extract("tool", test.In) {
				if s.Kind == "git-clone" {
					got = s.Block
					break
				}
			}
			if diff := cmp.Diff(test.WantBlock, got); diff != "" {
				t.Errorf("recipe mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
