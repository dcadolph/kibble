package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestCollectOwnedInstalls checks that the install kibble runs is the
// repository's own tool, not a package an example installs. The fixtures are
// the shapes of the uv, dotenvx, and pipx documents that made kibble test ruff,
// codex, and pycowsay in their place.
func TestCollectOwnedInstalls(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name            string
		Files           map[string]string
		WantInstalls    []string
		WantSessionCmds []string
		WantSessionBins []string
	}{{ // Test 0: uv installs ruff in an example, and its own pip route is the tool.
		Name: "uv",
		Files: map[string]string{
			"pyproject.toml": "[project]\nname = \"uv\"\n",
			"README.md": "# uv\n\n## Installation\n\n```bash\n# With pip.\npip install uv\n```\n\n" +
				"```bash\n# Or pipx.\npipx install uv\n```\n\n## Tools\n\n" +
				"Install a tool with `uv tool install`:\n\n```console\n$ uv tool install ruff\n" +
				"$ ruff --version\n```\n",
		},
		WantInstalls:    []string{"pip-install uv", "pipx-install uv"},
		WantSessionCmds: []string{"pip install uv"},
		WantSessionBins: []string{"uv"},
	}, { // Test 1: dotenvx installs codex in an example, and its scoped package is the tool.
		Name: "dotenvx",
		Files: map[string]string{
			"package.json": `{"name": "@dotenvx/dotenvx", "bin": {"dotenvx": "./src/cli/dotenvx.js"}}`,
			"README.md": "# dotenvx\n\n```sh\nnpm i -g @dotenvx/dotenvx\n```\n\n```sh\ndotenvx encrypt\n```\n\n## Agents\n\n" +
				"```sh\n$ npm install -g @openai/codex\n$ dotenvx run -- codex\n```\n",
		},
		WantInstalls:    []string{"npm-install @dotenvx/dotenvx"},
		WantSessionCmds: []string{"npm i -g @dotenvx/dotenvx"},
		WantSessionBins: []string{"dotenvx"},
	}, { // Test 2: pipx installs pycowsay in an example, and its pip route is the tool.
		Name: "pipx",
		Files: map[string]string{
			"pyproject.toml": "[project]\nname = \"pipx\"\n",
			"README.md": "# pipx\n\nInstall an application for your user:\n\n" +
				"```\npipx install pycowsay\npycowsay mooo\n```\n",
			"docs/how-to/install-pipx.rst": "Other distributions, using ``pip``:\n\n" +
				".. code-block:: console\n\n    $ python3 -m pip install --user pipx\n" +
				"    $ python3 -m pipx ensurepath\n",
		},
		WantInstalls:    []string{"pip-install pipx"},
		WantSessionCmds: []string{"python3 -m pip install --user pipx"},
		WantSessionBins: []string{"pipx"},
	}, { // Test 3: a package named apart from the repository is owned through its manifest.
		Name: "fd",
		Files: map[string]string{
			"Cargo.toml": "[package]\nname = \"fd-find\"\n\n[[bin]]\nname = \"fd\"\n",
			"README.md":  "# fd\n\n```sh\ncargo install fd-find\n```\n\n```sh\nfd --hidden x\n```\n",
		},
		WantInstalls:    []string{"cargo-install fd-find"},
		WantSessionCmds: []string{"cargo install fd-find"},
		WantSessionBins: []string{"fd-find", "fd"},
	}, { // Test 4: a workspace member's package name is owned.
		Name: "watchexec",
		Files: map[string]string{
			"Cargo.toml":            "[workspace]\nmembers = [\"crates/*\"]\n",
			"crates/cli/Cargo.toml": "[package]\nname = \"watchexec-cli\"\n",
			"README.md": "# watchexec\n\n```sh\ncargo install --locked watchexec-cli\n```\n\n" +
				"```sh\nwatchexec --version\n```\n",
		},
		WantInstalls:    []string{"cargo-install watchexec-cli"},
		WantSessionCmds: []string{"cargo install --locked watchexec-cli"},
		WantSessionBins: []string{"watchexec-cli", "watchexec"},
	}, { // Test 5: a Go repository installing its own cmd path owns it, and a linter it installs does not.
		Name: "tool",
		Files: map[string]string{
			"go.mod": "module example.com/org/tool/v2\n\ngo 1.22\n",
			"README.md": "# tool\n\n```sh\ngo install example.com/org/tool/v2/cmd/tool@latest\n```\n\n" +
				"```sh\ngo install honnef.co/go/tools/cmd/staticcheck@latest\ntool --version\n```\n",
		},
		WantInstalls:    []string{"go-install example.com/org/tool/v2/cmd/tool@latest"},
		WantSessionCmds: []string{"go install example.com/org/tool/v2/cmd/tool@latest"},
		WantSessionBins: []string{"tool"},
	}, { // Test 6: with no manifest, ownership is unknown and every install is kept.
		Name: "dotfiles",
		Files: map[string]string{
			"README.md": "# dotfiles\n\n```sh\ncargo install ripgrep\n```\n\n```sh\nrg --version\n```\n",
		},
		WantInstalls:    []string{"cargo-install ripgrep"},
		WantSessionCmds: []string{"cargo install ripgrep"},
		WantSessionBins: []string{"ripgrep", "dotfiles"},
	}, { // Test 7: ripgrep's only cargo route misspelled is kept for the registry to answer,
		// and never becomes the tool under test.
		Name: "ripgrep",
		Files: map[string]string{
			"Cargo.toml": "[package]\nname = \"ripgrep\"\n",
			"README.md": "# ripgrep\n\n```sh\nbrew install ripgrep\n```\n\n```sh\ncargo install ripgreppp\n```\n\n" +
				"```sh\nrg --version\n```\n",
		},
		WantInstalls: []string{"cargo-install ripgreppp"},
	}, { // Test 8: fd's owned npm route is the session install, and a misspelled cargo
		// route beside it is kept as an install of its own.
		Name: "fd",
		Files: map[string]string{
			"Cargo.toml": "[package]\nname = \"fd-find\"\n\n[[bin]]\nname = \"fd\"\n",
			"README.md": "# fd\n\n```sh\nnpm install -g fd-find\n```\n\n" +
				"```sh\ncargo install fd-findd\n```\n\n```sh\nfd --hidden x\n```\n",
		},
		WantInstalls:    []string{"npm-install fd-find", "cargo-install fd-findd"},
		WantSessionCmds: []string{"npm install -g fd-find"},
		WantSessionBins: []string{"fd-find", "fd"},
	}, { // Test 9: an unowned pip install beside the owned one is a prerequisite and dropped.
		Name: "mytool",
		Files: map[string]string{
			"pyproject.toml": "[project]\nname = \"mytool\"\n",
			"README.md": "# mytool\n\n```sh\npip install dead-prereq\npip install mytool\n```\n\n" +
				"```sh\nmytool --version\n```\n",
		},
		WantInstalls:    []string{"pip-install mytool"},
		WantSessionCmds: []string{"pip install mytool"},
		WantSessionBins: []string{"mytool"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), test.Name)
			for name, body := range test.Files {
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
					t.Fatalf("mkdir for %s: %v", name, err)
				}
				writeRepoFile(t, filepath.Dir(p), filepath.Base(p), body)
			}
			steps, plans, problems := collect([]string{dir}, true)
			if len(problems) != 0 {
				t.Fatalf("unexpected problems: %v", problems)
			}
			var installs []string
			for _, s := range steps {
				if s.Run && (s.Kind == "go-install" || pkgKinds[s.Kind].Ecosystem != "") {
					installs = append(installs, s.Kind+" "+s.Module)
				}
			}
			if diff := cmp.Diff(test.WantInstalls, installs, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("install steps mismatch (-want +got):\n%s", diff)
			}
			if len(plans) != 1 {
				t.Fatalf("got %d plans, want 1", len(plans))
			}
			var cmds []string
			for _, in := range plans[0].Installs {
				cmds = append(cmds, in.Cmd)
			}
			if diff := cmp.Diff(test.WantSessionCmds, cmds, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("session installs mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantSessionBins, plans[0].Binaries, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("session binaries mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRepoNamesKeep checks which unrecognized installs are dropped as foreign
// tools and which are kept for the registry to answer.
func TestRepoNamesKeep(t *testing.T) {
	t.Parallel()
	rust := repoNames{
		names:      map[string]bool{"ripgrep": true},
		ecosystems: map[string]bool{"rust": true},
		declared:   true,
	}
	tests := []struct {
		Names      repoNames
		Steps      []InstallStep
		WantResult []string
	}{{ // Test 0: an unowned cargo install with rust declared and no owned cargo route is kept.
		Names:      rust,
		Steps:      []InstallStep{{Kind: "cargo-install", Module: "ripgreppp", Run: true}},
		WantResult: []string{"cargo-install ripgreppp"},
	}, { // Test 1: an unowned cargo install beside an owned runnable one is dropped.
		Names: rust,
		Steps: []InstallStep{
			{Kind: "cargo-install", Module: "ripgrep", Run: true},
			{Kind: "cargo-install", Module: "cargo-nextest", Run: true},
		},
		WantResult: []string{"cargo-install ripgrep"},
	}, { // Test 2: an unowned pip install with no python manifest is dropped.
		Names:      rust,
		Steps:      []InstallStep{{Kind: "pip-install", Module: "black", Run: true}},
		WantResult: nil,
	}, { // Test 3: a repository that declares nothing keeps everything.
		Names:      repoNames{names: map[string]bool{"dotfiles": true}, ecosystems: map[string]bool{}},
		Steps:      []InstallStep{{Kind: "pip-install", Module: "black", Run: true}},
		WantResult: []string{"pip-install black"},
	}, { // Test 4: an owned placeholder that does not run is not the own install in hand.
		Names: rust,
		Steps: []InstallStep{
			{Kind: "cargo-install", Module: "ripgrep", Run: false},
			{Kind: "cargo-install", Module: "ripgreppp", Run: true},
		},
		WantResult: []string{"cargo-install ripgrep", "cargo-install ripgreppp"},
	}, { // Test 5: a brew step is never dropped.
		Names:      rust,
		Steps:      []InstallStep{{Kind: "brew", Module: "something-else", Run: true}},
		WantResult: []string{"brew something-else"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, s := range test.Names.keep(test.Steps) {
				got = append(got, s.Kind+" "+s.Module)
			}
			if diff := cmp.Diff(test.WantResult, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("kept steps mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
