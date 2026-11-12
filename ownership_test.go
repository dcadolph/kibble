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
	}, { // Test 10: A local install of the repository's own package is its route,
		// and the session expects the binary the manifest declares.
		Name: "zx",
		Files: map[string]string{
			"package.json": `{"name": "zx", "bin": {"zx": "build/cli.js"}}`,
			"README.md": "# zx\n\n```bash\nnpm install zx # add -g to install globally\n```\n\n" +
				"```bash\nzx --version\n```\n",
		},
		WantInstalls:    []string{"npm-install zx"},
		WantSessionCmds: []string{"npm install zx"},
		WantSessionBins: []string{"zx"},
	}, { // Test 11: A single-path bin names its binary after the scoped package.
		Name: "tool-repo",
		Files: map[string]string{
			"package.json": `{"name": "@scope/tool", "bin": "cli.js"}`,
			"README.md":    "# tool\n\n```sh\nnpm i @scope/tool\n```\n\n```sh\ntool --version\n```\n",
		},
		WantInstalls:    []string{"npm-install @scope/tool"},
		WantSessionCmds: []string{"npm i @scope/tool"},
		WantSessionBins: []string{"tool", "tool-repo"},
	}, { // Test 12: A workspace member's package is declared, and the private root is not it.
		Name: "mono",
		Files: map[string]string{
			"package.json":               `{"name": "@org/monorepo", "private": true}`,
			"packages/tool/package.json": `{"name": "tool", "bin": {"tool": "bin/tool.js"}}`,
			"README.md": "# tool\n\n```sh\nnpm install tool\n```\n\n" +
				"```sh\ntool --help\n```\n",
		},
		WantInstalls:    []string{"npm-install tool"},
		WantSessionCmds: []string{"npm install tool"},
		WantSessionBins: []string{"tool", "mono"},
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

// TestCollectLocalNpmLibrary checks the routes a node library's README gives.
// A local install of the declared package is verified as a step and opens no
// session, since a package with no binary puts no tool on PATH. A local
// install of anything else is a dependency install and no route at all.
func TestCollectLocalNpmLibrary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name         string
		Manifest     string
		Readme       string
		WantInstalls []string
		WantSession  []string
		WantNoRoute  bool
	}{{ // Test 0: Chalk's README installs chalk, which declares no binary.
		Name: "chalk", Manifest: `{"name": "chalk"}`,
		Readme:       "# chalk\n\n```sh\nnpm install chalk\n```\n\n```sh\nnode example.js\n```\n",
		WantInstalls: []string{"npm-install chalk"},
	}, { // Test 1: A second package on the line rides along with the declared one.
		Name: "ink", Manifest: `{"name": "ink"}`,
		Readme:       "# ink\n\n```sh\nnpm install ink react\n```\n",
		WantInstalls: []string{"npm-install ink"},
	}, { // Test 2: A dependency install in another package's README is no route.
		Name: "widget", Manifest: `{"name": "widget"}`,
		Readme:      "# widget\n\n```sh\nnpm install prettier\n```\n",
		WantNoRoute: true,
	}, { // Test 3: The folder name is not a declaration when the manifest names another package.
		Name: "chalk", Manifest: `{"name": "@fork/chalk-next"}`,
		Readme:      "# chalk\n\n```sh\nnpm install chalk\n```\n",
		WantNoRoute: true,
	}, { // Test 4: A library keeps the clone session and the tool install beside it.
		Name: "express", Manifest: `{"name": "express"}`,
		Readme: "# express\n\n```sh\nnpm install express\n```\n\n" +
			"```sh\nnpm install -g express-generator@4\n```\n\n" +
			"```sh\ngit clone https://github.com/expressjs/express.git\ncd express\nnpm install\n```\n\n" +
			"```sh\nexpress --version\n```\n",
		WantInstalls: []string{"npm-install express", "npm-install express-generator@4"},
		WantSession: []string{
			"git clone https://github.com/expressjs/express.git\ncd express\nnpm install",
		},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), test.Name)
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			writeRepoFile(t, dir, "package.json", test.Manifest)
			writeRepoFile(t, dir, "README.md", test.Readme)
			steps, plans, problems := collect([]string{dir}, true)
			var installs []string
			for _, s := range steps {
				if s.Run && pkgKinds[s.Kind].Ecosystem != "" {
					installs = append(installs, s.Kind+" "+s.Module)
				}
			}
			if diff := cmp.Diff(test.WantInstalls, installs, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("install steps mismatch (-want +got):\n%s", diff)
			}
			var session []string
			for _, p := range plans {
				for _, in := range p.Installs {
					session = append(session, in.Cmd)
				}
			}
			if diff := cmp.Diff(test.WantSession, session, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("session installs mismatch (-want +got):\n%s", diff)
			}
			noRoute := len(problems) == 1 && problems[0].Reason == ReasonNoInstallRoute
			if noRoute != test.WantNoRoute || len(problems) > 1 {
				t.Errorf("no-install-route row = %v, want %v: %+v", noRoute, test.WantNoRoute, problems)
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

// TestCollectLocalNpmTool checks the routes a node tool's documents give when
// a local install names the package the repository publishes.
func TestCollectLocalNpmTool(t *testing.T) {
	t.Parallel()
	const zx = `{"name": "zx", "bin": {"zx": "build/cli.js"}}`
	const use = "```sh\nzx --version\n```\n"
	tests := []struct {
		Manifest     string
		Readme       string
		Install      string
		WantInstalls []string
		WantSession  []string
		WantNoRoute  bool
	}{{ // Test 0: A scoped name with a version is still the repository's own package.
		Manifest:     `{"name": "@scope/tool", "bin": {"cli": "cli.js"}}`,
		Readme:       "# tool\n\n```sh\nnpm install @scope/tool@2\n```\n\n```sh\ncli --version\n```\n",
		WantInstalls: []string{"npm install @scope/tool@2"},
		WantSession:  []string{"npm install @scope/tool@2"},
	}, { // Test 1: A local and a global install in one document are both verified.
		Manifest:     zx,
		Readme:       "# zx\n\n```sh\nnpm install zx\n```\n\n```sh\nnpm install -g zx\n```\n\n" + use,
		WantInstalls: []string{"npm install zx", "npm install -g zx"},
		WantSession:  []string{"npm install zx"},
	}, { // Test 2: The same pair split across two documents is both verified too.
		Manifest:     zx,
		Readme:       "# zx\n\n```sh\nnpm install zx\n```\n\n" + use,
		Install:      "# Install\n\n```sh\nnpm install -g zx\n```\n",
		WantInstalls: []string{"npm install zx", "npm install -g zx"},
		WantSession:  []string{"npm install zx"},
	}, { // Test 3: A private package is never published, so its name is no route.
		Manifest:    `{"name": "demo", "private": true, "bin": {"demo": "cli.js"}}`,
		Readme:      "# demo\n\n```sh\nnpm install demo\n```\n",
		WantNoRoute: true,
	}, { // Test 4: A workspace install of a declared name is no route.
		Manifest:    zx,
		Readme:      "# zx\n\n```sh\nnpm install -w zx lodash\n```\n",
		WantNoRoute: true,
	}, { // Test 5: Two routes to one package open one session install, whatever its binary is named.
		Manifest: `{"name": "@scope/tool", "bin": {"cli": "cli.js"}}`,
		Readme: "# tool\n\n```sh\nnpm install @scope/tool\nnpm install -g @scope/tool\n```\n\n" +
			"```sh\ncli --version\n```\n",
		WantInstalls: []string{"npm install @scope/tool", "npm install -g @scope/tool"},
		WantSession:  []string{"npm install @scope/tool"},
	}, { // Test 6: Any truthy private value keeps a package off the registry.
		Manifest:    `{"name": "demo", "private": "true", "bin": {"demo": "cli.js"}}`,
		Readme:      "# demo\n\n```sh\nnpm install demo\n```\n",
		WantNoRoute: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "checkout")
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			writeRepoFile(t, dir, "package.json", test.Manifest)
			writeRepoFile(t, dir, "README.md", test.Readme)
			if test.Install != "" {
				writeRepoFile(t, dir, "INSTALL.md", test.Install)
			}
			steps, plans, problems := collect([]string{dir}, true)
			var installs []string
			for _, s := range steps {
				if s.Run && pkgKinds[s.Kind].Ecosystem != "" {
					installs = append(installs, s.Raw)
				}
			}
			if diff := cmp.Diff(test.WantInstalls, installs, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("install steps mismatch (-want +got):\n%s", diff)
			}
			var session []string
			for _, p := range plans {
				for _, in := range p.Installs {
					session = append(session, in.Cmd)
				}
			}
			if diff := cmp.Diff(test.WantSession, session, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("session installs mismatch (-want +got):\n%s", diff)
			}
			noRoute := len(problems) == 1 && problems[0].Reason == ReasonNoInstallRoute
			if noRoute != test.WantNoRoute || len(problems) > 1 {
				t.Errorf("no-install-route row = %v, want %v: %+v", noRoute, test.WantNoRoute, problems)
			}
		})
	}
}

// TestOwnedNamesNpm checks which package.json files name a node package the
// repository publishes, and which binaries each one is recorded with.
func TestOwnedNamesNpm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Files      map[string]string
		WantResult map[string][]string
	}{{ // Test 0: The root manifest and its binary.
		Files:      map[string]string{"package.json": `{"name": "zx", "bin": {"zx": "cli.js"}}`},
		WantResult: map[string][]string{"zx": {"zx"}},
	}, { // Test 1: Dependencies, tests, and fixtures declare nothing the repository publishes.
		Files: map[string]string{
			"package.json":                  `{"name": "zx"}`,
			"node_modules/dep/package.json": `{"name": "dep", "bin": {"dep": "d.js"}}`,
			"test/package.json":             `{"name": "sample"}`,
			"fixtures/package.json":         `{"name": "fixture"}`,
		},
		WantResult: map[string][]string{"zx": nil},
	}, { // Test 2: A manifest that does not parse declares nothing.
		Files:      map[string]string{"package.json": `{"name": "zx", `},
		WantResult: nil,
	}, { // Test 3: Two manifests of one name pool their binaries in name order.
		Files: map[string]string{
			"package.json":               `{"name": "tool", "bin": {"b": "b.js"}}`,
			"packages/tool/package.json": `{"name": "tool", "bin": {"a": "a.js"}}`,
		},
		WantResult: map[string][]string{"tool": {"a", "b"}},
	}, { // Test 4: A private package is never published, so it is left out.
		Files: map[string]string{
			"package.json":      `{"name": "tool-monorepo", "private": true}`,
			"demo/package.json": `{"name": "demo", "private": true, "bin": {"demo": "d.js"}}`,
			"cli/package.json":  `{"name": "tool", "bin": "cli.js"}`,
		},
		WantResult: map[string][]string{"tool": {"tool"}},
	}, { // Test 5: A workspace member two folders down is read, and one three down is not.
		Files: map[string]string{
			"packages/cli/package.json":       `{"name": "@scope/cli", "bin": "cli.js"}`,
			"packages/group/lib/package.json": `{"name": "@scope/lib"}`,
		},
		WantResult: map[string][]string{"@scope/cli": {"cli"}},
	}, { // Test 6: A false private flag leaves the package published.
		Files:      map[string]string{"package.json": `{"name": "zx", "private": false}`},
		WantResult: map[string][]string{"zx": nil},
	}, { // Test 7: A null private flag does too.
		Files:      map[string]string{"package.json": `{"name": "zx", "private": null}`},
		WantResult: map[string][]string{"zx": nil},
	}, { // Test 8: A truthy number is private, as npm reads it.
		Files:      map[string]string{"package.json": `{"name": "zx", "private": 1, "bin": "c.js"}`},
		WantResult: nil,
	}, { // Test 9: A truthy string is private, the word false included.
		Files:      map[string]string{"package.json": `{"name": "zx", "private": "false"}`},
		WantResult: nil,
	}, { // Test 10: A binary name that is not a plain word is not a binary.
		Files: map[string]string{"package.json": `{"name": "zx", "bin": {"zx": "c.js",
			"x'; touch /tmp/owned; '": "e.js", "a\u0000b": "f.js", "-V": "g.js", "a b": "h.js"}}`},
		WantResult: map[string][]string{"zx": {"zx"}},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "checkout")
			for name, body := range test.Files {
				if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o750); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				writeRepoFile(t, dir, name, body)
			}
			got := ownedNames(dir, "checkout").npm
			if diff := cmp.Diff(test.WantResult, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("npm packages mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRepoNamesOwnsNpmVersion checks that a version on a node package request
// does not hide the repository's own package, scoped or not.
func TestRepoNamesOwnsNpmVersion(t *testing.T) {
	t.Parallel()
	rn := repoNames{names: map[string]bool{}, ecosystems: map[string]bool{"node": true}}
	rn.addPackageJSON([]byte(`{"name": "@scope/tool"}`))
	rn.addPackageJSON([]byte(`{"name": "zx", "bin": {"zx": "cli.js"}}`))
	tests := []struct {
		In         string
		WantResult bool
	}{{ // Test 0: A scoped name.
		In: "@scope/tool", WantResult: true,
	}, { // Test 1: A scoped name with a version.
		In: "@scope/tool@2", WantResult: true,
	}, { // Test 2: A scoped name with a tag.
		In: "@scope/tool@latest", WantResult: true,
	}, { // Test 3: A plain name with a version.
		In: "zx@8.8.5", WantResult: true,
	}, { // Test 4: Another package with a version.
		In: "left-pad@1.3.0", WantResult: false,
	}, { // Test 5: Another scope's package of another name.
		In: "@other/thing@2", WantResult: false,
	}, { // Test 6: An alias installs another package under the repository's name.
		In: "@scope/tool@npm:evil", WantResult: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := rn.owns(InstallStep{Kind: "npm-install", Module: test.In, Run: true})
			if diff := cmp.Diff(test.WantResult, got); diff != "" {
				t.Errorf("owns mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
