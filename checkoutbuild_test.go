package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	kplan "github.com/dcadolph/kibble/internal/plan"
)

// writeTree writes files under dir, creating their directories.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCheckoutBuild checks which checkouts kibble builds its own tool from,
// and that it builds only a binary it can name without guessing.
func TestCheckoutBuild(t *testing.T) {
	t.Parallel()
	const goMain = "package main\n\nfunc main() {}\n"
	tests := []struct {
		Repo       string
		Files      map[string]string
		WantCmd    string
		WantBinary string
		Want       bool
	}{{ // Test 0: a root main package installs from the root under the module's name.
		Repo:    "glance",
		Files:   map[string]string{"go.mod": "module github.com/glanceapp/glance\n", "main.go": goMain},
		WantCmd: "go install .", WantBinary: "glance", Want: true,
	}, { // Test 1: a major-version suffix is not the binary's name.
		Repo:    "tool",
		Files:   map[string]string{"go.mod": "module example.com/tool/v2\n", "main.go": goMain},
		WantCmd: "go install .", WantBinary: "tool", Want: true,
	}, { // Test 2: the cmd directory named for the repository wins.
		Repo: "headscale",
		Files: map[string]string{"go.mod": "module github.com/juanfont/headscale\n",
			"cmd/headscale/main.go": goMain, "cmd/hi/main.go": goMain},
		WantCmd: "go install ./cmd/headscale", WantBinary: "headscale", Want: true,
	}, { // Test 3: a single cmd directory is the tool whatever its name.
		Repo: "opentofu",
		Files: map[string]string{"go.mod": "module github.com/opentofu/opentofu\n",
			"cmd/tofu/main.go": goMain, "cmd/tofu/main_test.go": "package main\n"},
		WantCmd: "go install ./cmd/tofu", WantBinary: "tofu", Want: true,
	}, { // Test 4: two cmd directories, neither named for the repository, build nothing.
		Repo: "repo",
		Files: map[string]string{"go.mod": "module example.com/repo\n",
			"cmd/a/main.go": goMain, "cmd/b/main.go": goMain},
	}, { // Test 5: a library builds nothing.
		Repo:  "lib",
		Files: map[string]string{"go.mod": "module example.com/lib\n", "lib.go": "package lib\n"},
	}, { // Test 6: a test file alone does not make a main package.
		Repo:  "lib",
		Files: map[string]string{"go.mod": "module example.com/lib\n", "main_test.go": goMain},
	}, { // Test 7: a Rust package with src/main.rs installs locked when it has a lock file.
		Repo: "navi",
		Files: map[string]string{"Cargo.toml": "[package]\nname = \"navi\"\nversion = \"2.0.0\"\n",
			"src/main.rs": "fn main() {}\n", "Cargo.lock": ""},
		WantCmd: "cargo install --path . --bin navi --locked", WantBinary: "navi", Want: true,
	}, { // Test 8: a single declared binary is the tool, unlocked without a lock file.
		Repo:    "nushell",
		Files:   map[string]string{"Cargo.toml": "[package]\nname = \"nu\"\n\n[[bin]]\nname = \"nu\"\npath = \"src/main.rs\"\n"},
		WantCmd: "cargo install --path . --bin nu", WantBinary: "nu", Want: true,
	}, { // Test 9: a workspace root with no package of its own builds nothing.
		Repo:  "ws",
		Files: map[string]string{"Cargo.toml": "[workspace]\nmembers = [\"crates/*\"]\n"},
	}, { // Test 10: a Rust library builds nothing.
		Repo:  "lib",
		Files: map[string]string{"Cargo.toml": "[package]\nname = \"lib\"\n", "src/lib.rs": ""},
	}, { // Test 11: a Python project with one console script installs with pip.
		Repo: "fastapi",
		Files: map[string]string{"pyproject.toml": "[project]\nname = \"fastapi\"\n\n" +
			"[project.scripts]\nfastapi = \"fastapi.cli:main\"\n"},
		WantCmd: "python3 -m venv /opt/kibble-tool && /opt/kibble-tool/bin/pip install --quiet . && " +
			"ln -sf /opt/kibble-tool/bin/fastapi /usr/local/bin/fastapi",
		WantBinary: "fastapi", Want: true,
	}, { // Test 12: a Python project with no script builds nothing.
		Repo:  "lib",
		Files: map[string]string{"pyproject.toml": "[project]\nname = \"lib\"\n"},
	}, { // Test 13: of two ecosystems, the tool named for the repository wins.
		Repo: "tool",
		Files: map[string]string{"go.mod": "module example.com/helper\n", "main.go": goMain,
			"pyproject.toml": "[project]\nname = \"tool\"\n\n[project.scripts]\ntool = \"tool:main\"\n"},
		WantCmd: "python3 -m venv /opt/kibble-tool && /opt/kibble-tool/bin/pip install --quiet . && " +
			"ln -sf /opt/kibble-tool/bin/tool /usr/local/bin/tool",
		WantBinary: "tool", Want: true,
	}, { // Test 14: of two ecosystems with neither named for the repository, nothing builds.
		Repo: "repo",
		Files: map[string]string{"go.mod": "module example.com/helper\n", "main.go": goMain,
			"pyproject.toml": "[project]\nname = \"other\"\n\n[project.scripts]\nother = \"o:main\"\n"},
	}, { // Test 15: a script name that cannot sit in a shell command is not built.
		Repo:  "repo",
		Files: map[string]string{"pyproject.toml": "[project.scripts]\n\"bad;name\" = \"x:main\"\n"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeTree(t, dir, test.Files)
			got, ok := checkoutBuild(dir, test.Repo)
			if ok != test.Want {
				t.Fatalf("ok = %v, want %v (install %+v)", ok, test.Want, got)
			}
			if !ok {
				return
			}
			if diff := cmp.Diff(test.WantCmd, got.Cmd); diff != "" {
				t.Errorf("cmd mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantBinary, got.Binary); diff != "" {
				t.Errorf("binary mismatch (-want +got):\n%s", diff)
			}
			if !got.FromCheckout {
				t.Error("install is not marked as built from the checkout")
			}
		})
	}
}

// TestCheckoutBuildSession checks that examples run against a checkout build
// say so, and that a failed checkout build blocks rather than skips.
func TestCheckoutBuildSession(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Out         string
		Checkout    bool
		Release     bool
		WantStatus  Status
		WantDetail  string
		WantLabel   bool
		WantRelease bool
	}{{ // Test 0: passing examples on a checkout build are labeled.
		Out: sessionOut("KIBBLE-BUILD CODE=0", "KIBBLE-STEP b1 START", "KIBBLE-LINE b1:0 CODE=0",
			"KIBBLE-LINE b1:1 CODE=0", "KIBBLE-LINE b1:2 CODE=0", "KIBBLE-DONE"),
		Checkout: true, WantStatus: StatusVerified, WantLabel: true,
	}, { // Test 1: a failing example on a checkout build is labeled too.
		Out: sessionOut("KIBBLE-BUILD CODE=0", "KIBBLE-STEP b1 START", "KIBBLE-LINE b1:0 CODE=0",
			"error: unknown command", "KIBBLE-LINE b1:1 CODE=2", "KIBBLE-LINE b1:2 CODE=0", "KIBBLE-DONE"),
		Checkout: true, WantStatus: StatusFail, WantLabel: true,
	}, { // Test 2: a failed checkout build blocks.
		Out:      sessionOut("KIBBLE-BUILD CODE=1", "go: go.mod requires go >= 1.27", "KIBBLE-ABORT"),
		Checkout: true, WantStatus: StatusBlocked, WantLabel: true, WantDetail: "requires go >= 1.27",
	}, { // Test 2a: a timed-out checkout build says so.
		Out:      sessionOut("KIBBLE-BUILD CODE=124", "KIBBLE-ABORT"),
		Checkout: true, WantStatus: StatusBlocked, WantLabel: true, WantDetail: "timed out",
	}, { // Test 3: a failed documented install still skips, unlabeled.
		Out:        sessionOut("KIBBLE-BUILD CODE=1", "build error tail", "KIBBLE-ABORT"),
		WantStatus: StatusSkipped,
	}, { // Test 4: passing examples on a documented install carry no label.
		Out: sessionOut("KIBBLE-BUILD CODE=0", "KIBBLE-STEP b1 START", "KIBBLE-LINE b1:0 CODE=0",
			"KIBBLE-LINE b1:1 CODE=0", "KIBBLE-LINE b1:2 CODE=0", "KIBBLE-DONE"),
		WantStatus: StatusVerified,
	}, { // Test 5: passing examples on a documented release download name the asset.
		Out: sessionOut("KIBBLE-BUILD CODE=0", "KIBBLE-STEP b1 START", "KIBBLE-LINE b1:0 CODE=0",
			"KIBBLE-LINE b1:1 CODE=0", "KIBBLE-LINE b1:2 CODE=0", "KIBBLE-DONE"),
		Release: true, WantStatus: StatusVerified, WantRelease: true,
	}, { // Test 6: a failing example on a release download is labeled too.
		Out: sessionOut("KIBBLE-BUILD CODE=0", "KIBBLE-STEP b1 START", "KIBBLE-LINE b1:0 CODE=0",
			"error: unknown command", "KIBBLE-LINE b1:1 CODE=2", "KIBBLE-LINE b1:2 CODE=0", "KIBBLE-DONE"),
		Release: true, WantStatus: StatusFail, WantRelease: true,
	}, { // Test 7: a failed release download skips, says it was the download, and names the asset.
		Out:     sessionOut("KIBBLE-BUILD CODE=8", "wget: server returned error: HTTP/1.1 404", "KIBBLE-ABORT"),
		Release: true, WantStatus: StatusSkipped, WantRelease: true, WantDetail: "release download failed",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := examplePlan()
			if test.Checkout {
				plan.Installs = []kplan.PlanInstall{{Cmd: "go install .", Ecosystem: "go", Binary: "tool",
					FromCheckout: true}}
			}
			if test.Release {
				plan.Installs = []kplan.PlanInstall{{Cmd: "wget x", Dir: "/work", Binary: "tool",
					Release: "https://github.com/acme/tool/releases/download/v1/tool_linux_arm64.tar.gz"}}
			}
			step := InstallStep{Repo: "repo", Kind: "example", plan: plan}
			res := classifyExample(step, plan, test.Out, nil, 0, lineTimeout, sessionEnd{})
			if diff := cmp.Diff(test.WantStatus, res.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s\ndetail: %s", diff, res.Detail)
			}
			if !strings.Contains(res.Detail, test.WantDetail) {
				t.Errorf("detail %q does not contain %q", res.Detail, test.WantDetail)
			}
			if got := strings.Contains(res.Detail, checkoutNote); got != test.WantLabel {
				t.Errorf("labeled = %v, want %v (detail %q)", got, test.WantLabel, res.Detail)
			}
			note := releaseNote("tool_linux_arm64.tar.gz")
			if got := strings.Contains(res.Detail, note); got != test.WantRelease {
				t.Errorf("release labeled = %v, want %v (detail %q)", got, test.WantRelease, res.Detail)
			}
		})
	}
}
