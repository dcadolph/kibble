package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// TestClassify checks that container output maps to the right status. A build
// timeout must not be reported as a failure.
func TestClassify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In         string
		WantStatus Status
		WantSmoke  string
		WantDetail string
	}{{ // Test 0: built and smoke test passed.
		In:         "BUILDCODE=0\nSMOKECODE=0\nSMOKELINE=tool version 1.0\n",
		WantStatus: StatusVerified, WantSmoke: "tool version 1.0",
	}, { // Test 1: built but the binary did not respond cleanly.
		In:         "BUILDCODE=0\nSMOKECODE=2\nSMOKELINE=Usage of tool:\n",
		WantStatus: StatusBuilt, WantSmoke: "Usage of tool:",
	}, { // Test 2: build exceeded the timeout, so the result is unknown.
		In:         "BUILDCODE=124\ngo: downloading github.com/pkg/errors v0.9.1\n",
		WantStatus: StatusTimeout,
	}, { // Test 3: build failed with a compile error.
		In:         "BUILDCODE=1\npkg/foo/bar.go:10: undefined: Baz\n",
		WantStatus: StatusFail, WantDetail: "undefined: Baz",
	}, { // Test 4: no marker at all is kibble's own error, not a doc failure.
		In:         "docker: Error response from daemon: pull access denied\n",
		WantStatus: StatusError, WantDetail: "kibble could not run the step",
	}, { // Test 5: a recipe that runs but produces no binary is unverified.
		In:         "BUILDCODE=0\nNOBIN=1\n",
		WantStatus: StatusRan, WantDetail: "no binary to smoke-test",
	}, { // Test 6: a smoke test that fails on a cross-architecture binary.
		In:         "BUILDCODE=0\nSMOKECODE=1\nSMOKELINE=qemu-aarch64: Could not open '/lib/ld'\n",
		WantStatus: StatusCrossArch,
	}, { // Test 7: an arch string in the build log does not mask a real crash.
		In:         "cannot execute binary file elsewhere\nBUILDCODE=0\nSMOKECODE=139\nSMOKELINE=segfault\n",
		WantStatus: StatusBuilt, WantSmoke: "segfault", WantDetail: "smoke exit=139",
	}}
	step := InstallStep{Repo: "repo", Kind: "go-install", Binary: "tool"}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := classify(step, test.In, 5*time.Second)
			if diff := cmp.Diff(test.WantStatus, got.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantSmoke, got.SmokeLine); diff != "" {
				t.Errorf("smoke mismatch (-want +got):\n%s", diff)
			}
			if test.WantDetail != "" && !strings.Contains(got.Detail, test.WantDetail) {
				t.Errorf("detail %q does not contain %q", got.Detail, test.WantDetail)
			}
		})
	}
}

// TestClassifySubcommandCodes checks that each cited subcommand's help probe
// reports its own exit code, and that the markers do not leak into the help
// text the flag check reads.
func TestClassifySubcommandCodes(t *testing.T) {
	t.Parallel()
	out := "BUILDCODE=0\nSMOKECODE=0\nSMOKELINE=tool 1.0\n" +
		"KIBBLE-HELP-START\nUsage: tool [flags]\n  --json  emit json\n" +
		"KIBBLE-SUB scan CODE=0\n" +
		"KIBBLE-SUB walk rotate CODE=1\n" +
		"KIBBLE-HELP-END\n"
	got := classify(InstallStep{Repo: "repo", Kind: "go-install", Binary: "tool"}, out, 0)
	want := map[string]int{"scan": 0, "walk rotate": 1}
	if diff := cmp.Diff(want, got.subCodes); diff != "" {
		t.Errorf("subCodes mismatch (-want +got):\n%s", diff)
	}
	if strings.Contains(got.helpText, "KIBBLE-SUB") {
		t.Errorf("help text carries a probe marker: %q", got.helpText)
	}
	if !strings.Contains(got.helpText, "--json") {
		t.Errorf("help text lost its flags: %q", got.helpText)
	}
}

// TestInstallScriptFindsBinary checks that the Go install script falls back to
// whatever landed in GOBIN and prints NOBIN when nothing did, so a wrong guess
// at the binary name is never reported as the documented install failing.
func TestInstallScriptFindsBinary(t *testing.T) {
	t.Parallel()
	script := fmt.Sprintf(installScript, 60, "example.com/x/cmd/...@latest", "...")
	for _, want := range []string{
		`bin="$GOBIN/..."`,
		`b=$(ls "$GOBIN" 2>/dev/null | head -n1)`,
		"printf 'NOBIN=1\\n'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
		}
	}
	res := classify(InstallStep{Kind: "go-install", Binary: "..."}, "BUILDCODE=0\nNOBIN=1\n", 0)
	if res.Status != StatusRan {
		t.Errorf("status = %s, want %s", res.Status, StatusRan)
	}
}

// TestDockerExampleSession replays a plan in a real container and checks that
// every documented line reports its own result, including one whose output
// ends without a newline. It is the end-to-end guard on the marker protocol,
// which no amount of parsing tests can cover on its own.
func TestDockerExampleSession(t *testing.T) {
	if testing.Short() {
		t.Skip("container test skipped in short mode")
	}
	if os.Getenv("KIBBLE_INTEGRATION") == "" {
		t.Skip("set KIBBLE_INTEGRATION=1 to run container tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := DockerAvailable(ctx); err != nil {
		t.Skipf("docker is not reachable: %v", err)
	}
	plan := &Plan{
		Repo:     "repo",
		Installs: []PlanInstall{{Cmd: "true"}},
		Steps: []PlanStep{{ID: "b1", Lines: []PlanLine{
			{Cmd: "printf no-trailing-newline"},
			{Cmd: "echo second"},
			{Cmd: "sh -c 'printf glued; exit 3'"},
			{Cmd: "echo fourth"},
			{Cmd: "echo fifth", Skip: "planned skip"},
		}}},
	}
	runner := &DockerRunner{Image: "debian:stable-slim", Timeout: 60 * time.Second}
	step := InstallStep{Repo: "repo", Kind: "example", Run: true, plan: plan, dir: t.TempDir()}
	res := runner.runExample(ctx, step)
	if res.example == nil {
		t.Fatalf("no per-line outcomes, status %s: %s", res.Status, res.Detail)
	}
	var got []Status
	for _, l := range res.example.Steps[0].Lines {
		got = append(got, l.Status)
	}
	want := []Status{StatusVerified, StatusVerified, StatusFail, StatusVerified, StatusSkipped}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("line statuses mismatch (-want +got):\n%s\ndetail: %s", diff, res.Detail)
	}
	if res.Status != StatusFail {
		t.Errorf("status = %s, want %s (detail %q)", res.Status, StatusFail, res.Detail)
	}
}

// TestRewriteSSH checks that GitHub SSH remotes become HTTPS for keyless clones.
func TestRewriteSSH(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want string
	}{{ // Test 0: an ssh remote with .git suffix.
		In:   "git clone git@github.com:example/mytool.git",
		Want: "git clone https://github.com/example/mytool.git",
	}, { // Test 1: an ssh remote without the suffix.
		In:   "git clone git@github.com:example/other",
		Want: "git clone https://github.com/example/other.git",
	}, { // Test 2: an https remote is untouched.
		In:   "git clone https://github.com/example/mytool && cd mytool",
		Want: "git clone https://github.com/example/mytool && cd mytool",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, rewriteSSH(test.In)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestTruncate checks the ellipsis helper used by the table report.
func TestTruncate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		N    int
		Want string
	}{{ // Test 0: shorter than the limit is unchanged.
		In: "short", N: 10, Want: "short",
	}, { // Test 1: longer than the limit is cut with an ellipsis.
		In: "abcdefghij", N: 5, Want: "abcd…",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, truncate(test.In, test.N)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestFailureLine checks that a build wrapper's own summary is skipped in
// favor of the line that says what actually broke.
func TestFailureLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Lines []string
		Want  string
	}{{ // Test 0: make prints its summary after the real error.
		Lines: []string{"go: cannot find module for ./wasm",
			"make: *** [Makefile:56: wasm] Error 1"},
		Want: "go: cannot find module for ./wasm",
	}, { // Test 1: nested make adds more summary lines, all skipped.
		Lines: []string{"cc: fatal error: no input files",
			"make[1]: *** [all] Error 1", "make: *** [build] Error 2"},
		Want: "cc: fatal error: no input files",
	}, { // Test 2: a plain failure keeps its last line.
		Lines: []string{"setting up", "boom: file corrupted"},
		Want:  "boom: file corrupted",
	}, { // Test 3: nothing but wrapper summaries falls back to the last line.
		Lines: []string{"make: *** [all] Error 1"},
		Want:  "make: *** [all] Error 1",
	}, { // Test 4: a tool that answers a bad flag with its whole usage screen
		// is reported by what it refused, not by the last flag it lists. The
		// tail here is a flag description, which says nothing about the error.
		Lines: []string{
			"flag provided but not defined: -nosuchflag",
			"Usage of kibble:",
			"  -workers int",
			"    max concurrent installs (default 3)",
		},
		Want: "flag provided but not defined: -nosuchflag",
	}, { // Test 5: an error with no usage screen after it keeps the normal
		// tail, since a later line is usually the more specific cause.
		Lines: []string{"error: build started", "ld: symbol not found"},
		Want:  "ld: symbol not found",
	}, { // Test 6: a parser naming an unknown command above its screen.
		Lines: []string{`unknown command "migrate" for "tool"`, "Usage:", "  tool [flags]"},
		Want:  `unknown command "migrate" for "tool"`,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := failureLine(test.Lines); got != test.Want {
				t.Errorf("failureLine = %q, want %q", got, test.Want)
			}
		})
	}
}

// TestClassifyProbeSegments checks that the root screen, subcommand screens,
// and flag probes each land in their own segment. A tool with no subcommands
// once had its whole root screen mistaken for the first probe's output and
// trimmed out of the corpus.
func TestClassifyProbeSegments(t *testing.T) {
	t.Parallel()
	out := strings.Join([]string{
		"BUILDCODE=0",
		"SMOKECODE=0",
		"SMOKELINE=tool 1.0",
		"KIBBLE-HELP-START",
		"Usage of tool:",
		"  -json emit JSON",
		"KIBBLE-ROOT-END",
		"unknown flag: --nope",
		"KIBBLE-FLAG --nope CODE=1",
		"Usage of tool:",
		"KIBBLE-FLAG --json CODE=0",
		"KIBBLE-HELP-END",
	}, "\n")
	res := classify(InstallStep{Repo: "r", Kind: "go-install"}, out, 0)
	if !strings.Contains(res.helpRoot, "-json emit JSON") {
		t.Errorf("helpRoot lost the root screen: %q", res.helpRoot)
	}
	if strings.Contains(res.helpText, "--nope") {
		t.Errorf("a probe's rejection leaked into the corpus: %q", res.helpText)
	}
	if !strings.Contains(res.helpByFlag["nope"], "unknown flag") {
		t.Errorf("probe screen not captured: %q", res.helpByFlag["nope"])
	}
	if res.helpByFlag["json"] == "" || !strings.Contains(res.helpByFlag["json"], "Usage") {
		t.Errorf("accepted probe screen not captured: %q", res.helpByFlag["json"])
	}
}

// TestBrewInstallGuards checks the two documented lines a real install refuses
// to run. Both refusals are skips, because a step kibble declined to run has
// no verdict to report.
func TestBrewInstallGuards(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Module     string
		WantDetail string
	}{{ // Test 0: a cask installs a macOS application, which a Linux
		// container cannot judge either way.
		Module:     "cask:alacritty",
		WantDetail: "installs on macOS",
	}, { // Test 1: a name carrying shell characters is never handed to a
		// shell, since the formula field comes out of a document.
		Module:     "wget; rm -rf /",
		WantDetail: "shell characters",
	}}
	d := &DockerRunner{BrewInstall: true, Timeout: time.Minute}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := d.runBrewInstall(t.Context(), InstallStep{Kind: "brew", Module: test.Module})
			if got.Status != StatusSkipped {
				t.Errorf("status = %s, want %s", got.Status, StatusSkipped)
			}
			if !strings.Contains(got.Detail, test.WantDetail) {
				t.Errorf("detail = %q, want it to mention %q", got.Detail, test.WantDetail)
			}
		})
	}
}

// TestBrewInstallScriptShape checks the install script asks brew which binary
// the formula installed rather than guessing. `brew install rich` resolves an
// alias to rich-cli and installs a binary named rich, and the formula's
// dependencies install binaries of their own, so neither the documented name
// nor the first new file in the bin directory is reliably the tool.
func TestBrewInstallScriptShape(t *testing.T) {
	t.Parallel()
	script := fmt.Sprintf(brewInstallScript, 300, "rich", "rich")
	for _, want := range []string{
		`timeout 300 brew install rich`,
		`brew list --verbose rich`,
		`before=$(ls "$(brew --prefix)/bin"`,
		`comm -13`,
		"printf 'NOBIN=1\\n'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
		}
	}
}

// TestPkgScriptPrefersNamedBinary checks the package script smoke-tests the
// package's own binary when the install added it, not the alphabetically
// first new file. A pip install lands its dependencies' entry points in the
// same directory, and cmark sorting before rich is how a dependency got
// smoke-tested in the tool's name.
func TestPkgScriptPrefersNamedBinary(t *testing.T) {
	t.Parallel()
	script := pkgScriptFor(InstallStep{Kind: "pipx-install", Raw: "pipx install rich-cli", Binary: "rich"}, 300)
	for _, want := range []string{
		`grep -Fx 'rich'`,
		`comm -13`,
		`grep -v '^$'`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
		}
	}
	// pip mixes dependency entry points into the shared bin directory and the
	// documented package need not name its binary, rich-cli installs rich, so
	// pip alone asks the manager which binaries the package owns.
	pip := pkgScriptFor(InstallStep{
		Kind: "pip-install", Raw: "pip install rich-cli", Module: "rich-cli", Binary: "rich-cli"}, 300)
	if !strings.Contains(pip, `pip show -f 'rich-cli'`) {
		t.Error("pip script missing the owned-bins lookup")
	}
	evil := pkgScriptFor(InstallStep{
		Kind: "pip-install", Raw: "pip install x", Module: "x; rm -rf /", Binary: "x"}, 300)
	if strings.Contains(evil, "rm -rf") {
		t.Error("shell syntax from the docs reached the owned-bins lookup")
	}
	if strings.Contains(script, "pip show") {
		t.Error("pipx script gained a lookup only pip needs")
	}
}

// TestStripANSI checks that terminal control sequences are removed from
// captured output. A tool that colors its own errors would otherwise put raw
// escapes into a JSON report, a CI annotation, and the table's alignment.
func TestStripANSI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want string
	}{{ // Test 0: plain text is returned untouched.
		In: "no such file", Want: "no such file",
	}, { // Test 1: color codes go, the message stays.
		In: "\x1b[31merror:\x1b[0m not found", Want: "error: not found",
	}, { // Test 2: a carriage return from a progress bar goes too.
		In: "downloading\r100%", Want: "downloading100%",
	}, { // Test 3: cursor movement is a control sequence like any other.
		In: "\x1b[2K\x1b[1Gbuilding", Want: "building",
	}, { // Test 4: an operating system command, as used to set a title.
		In: "\x1b]0;title\x07done", Want: "done",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := stripANSI(test.In); got != test.Want {
				t.Errorf("stripANSI = %q, want %q", got, test.Want)
			}
		})
	}
}

// TestClassifyStripsANSI checks the stripping happens where output is turned
// into a verdict, so the detail a report carries is already clean.
func TestClassifyStripsANSI(t *testing.T) {
	t.Parallel()
	out := "BUILDCODE=1\n\x1b[1;31mfatal:\x1b[0m repository not found\n"
	res := classify(InstallStep{Kind: "git-clone"}, out, 0)
	if res.Status != StatusFail {
		t.Fatalf("status = %s, want %s", res.Status, StatusFail)
	}
	if strings.Contains(res.Detail, "\x1b") {
		t.Errorf("detail carries escapes: %q", res.Detail)
	}
	if !strings.Contains(res.Detail, "repository not found") {
		t.Errorf("detail lost the message: %q", res.Detail)
	}
}
