package main

import (
	"archive/tar"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// examplePlan builds a small plan for executor tests: one step of runnable
// lines around one planned skip.
func examplePlan() *Plan {
	return &Plan{
		Repo: "repo", Binaries: []string{"tool"},
		Installs: []PlanInstall{{Cmd: "go install example.com/tool@latest", Ecosystem: "go"}},
		Steps: []PlanStep{{
			ID: "b1",
			Lines: []PlanLine{
				{Cmd: "tool init"},
				{Cmd: "tool ask"},
				{Cmd: "tool check", NonzeroOK: true},
			},
		}},
	}
}

// sessionOut fabricates container output for a plan's markers.
func sessionOut(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

func TestClassifyExample(t *testing.T) {
	t.Parallel()
	step := InstallStep{Repo: "repo", Kind: "example"}
	tests := []struct {
		Out        string
		Wrapped    map[string]bool
		Plan       *Plan
		WantStatus Status
		WantLines  []Status
	}{{ // Test 0: all lines pass.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"KIBBLE-LINE b1:1 CODE=0",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusPass,
		WantLines:  []Status{StatusPass, StatusPass, StatusPass},
	}, { // Test 1: a plain failure names the line and fails the repo.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"boom: file corrupted",
			"KIBBLE-LINE b1:1 CODE=2",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusFail,
		WantLines:  []Status{StatusPass, StatusFail, StatusPass},
	}, { // Test 2: a documented nonzero exit passes.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"KIBBLE-LINE b1:1 CODE=0",
			"KIBBLE-LINE b1:2 CODE=1",
			"KIBBLE-DONE"),
		WantStatus: StatusPass,
		WantLines:  []Status{StatusPass, StatusPass, StatusPass},
	}, { // Test 2a: a setting the document never names is the document's gap.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"vamoose: VAMOOSE_CLIENT_ID not set: register an Entra app",
			"KIBBLE-LINE b1:1 CODE=1",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusGap,
		WantLines:  []Status{StatusPass, StatusGap, StatusPass},
	}, { // Test 2b: a credential-shaped name without missing wording still
		// fails, so a real error naming a token is not swallowed.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"vamoose: MY_TOKEN was rejected by the parser",
			"KIBBLE-LINE b1:1 CODE=1",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusFail,
		WantLines:  []Status{StatusPass, StatusFail, StatusPass},
	}, { // Test 2c: when the document names the setting, supplying it is the
		// reader's job, so the same failure is a skip and not a gap.
		Plan: &Plan{
			Repo: "repo", Binaries: []string{"tool"},
			Steps: []PlanStep{{ID: "b1", Lines: []PlanLine{
				{Cmd: "export VAMOOSE_CLIENT_ID=<application-client-id>", Skip: "placeholder"},
				{Cmd: "tool ask"},
				{Cmd: "tool check", NonzeroOK: true},
			}}},
		},
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"vamoose: VAMOOSE_CLIENT_ID not set: register an Entra app",
			"KIBBLE-LINE b1:1 CODE=1",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusPass,
		WantLines:  []Status{StatusSkipped, StatusSkipped, StatusPass},
	}, { // Test 3: a named setting the document omits is a gap, so this
		// fixture's key reports rather than disappearing into a skip.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"error: ANTHROPIC_API_KEY is not set",
			"KIBBLE-LINE b1:1 CODE=2",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusGap,
		WantLines:  []Status{StatusPass, StatusGap, StatusPass},
	}, { // Test 4: a wrapped 124 is a hang, and it wins over pass lines.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=124",
			"KIBBLE-LINE b1:1 CODE=0",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		Wrapped:    map[string]bool{"b1:0": true},
		WantStatus: StatusTimeout,
		WantLines:  []Status{StatusTimeout, StatusPass, StatusPass},
	}, { // Test 5: a killed session times out the running line, rest not run.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0"),
		WantStatus: StatusTimeout,
		WantLines:  []Status{StatusPass, StatusTimeout, StatusSkipped},
	}, { // Test 6: an aborted install skips the examples.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=1",
			"build error tail",
			"KIBBLE-ABORT"),
		WantStatus: StatusSkipped,
	}, { // Test 7: a query with no data in the fresh session skips.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"no entries on 2026-07-11",
			"KIBBLE-LINE b1:1 CODE=4",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusPass,
		WantLines:  []Status{StatusPass, StatusSkipped, StatusPass},
	}, { // Test 8: a missing helper command skips.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"exec: \"dbus-launch\": executable file not found in $PATH",
			"KIBBLE-LINE b1:1 CODE=3",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusPass,
		WantLines:  []Status{StatusPass, StatusSkipped, StatusPass},
	}, { // Test 8a: a tool that asks for an interactive terminal skips.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"tool: connect needs an interactive terminal; use 'tool index' for scripts",
			"KIBBLE-LINE b1:1 CODE=1",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusPass,
		WantLines:  []Status{StatusPass, StatusSkipped, StatusPass},
	}, { // Test 8b: a tool that aborts an interactive approval changed
		// nothing, so the document is not broken.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"Aborted. Nothing was changed.",
			"KIBBLE-LINE b1:1 CODE=8",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusPass,
		WantLines:  []Status{StatusPass, StatusSkipped, StatusPass},
	}, { // Test 8c: a Makefile reaching for a tool the image lacks skips.
		// dash reports it without the word command, and make exits 2 rather
		// than 127, so neither of the older signals fires.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"sh: 1: zip: not found",
			"make: *** [Makefile:70: extension-package] Error 127",
			"KIBBLE-LINE b1:1 CODE=2",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusPass,
		WantLines:  []Status{StatusPass, StatusSkipped, StatusPass},
	}, { // Test 9: a missing system dependency skips, not fails.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"ffmpeg is not installed. Install it from https://ffmpeg.org",
			"KIBBLE-LINE b1:1 CODE=1",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusPass,
		WantLines:  []Status{StatusPass, StatusSkipped, StatusPass},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := test.Plan
			if plan == nil {
				plan = examplePlan()
			}
			res := classifyExample(step, plan, test.Out, test.Wrapped, 0)
			if res.Status != test.WantStatus {
				t.Errorf("status = %s, want %s (detail %q)", res.Status, test.WantStatus, res.Detail)
			}
			if test.WantLines == nil {
				return
			}
			var got []Status
			for _, s := range res.example.Steps {
				for _, l := range s.Lines {
					got = append(got, l.Status)
				}
			}
			if diff := cmp.Diff(test.WantLines, got); diff != "" {
				t.Errorf("line statuses mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestClassifyExampleMarkerRecovery checks that a documented line whose
// output ends without a trailing newline still reports its own result. The
// session prints each marker on a fresh line, and the parser recovers one
// that arrives glued to earlier output anyway, so a real failure is never
// filed as a line that did not run.
func TestClassifyExampleMarkerRecovery(t *testing.T) {
	t.Parallel()
	step := InstallStep{Repo: "repo", Kind: "example"}
	tests := []struct {
		Out        string
		WantStatus Status
		WantLines  []Status
	}{{ // Test 0: a passing line whose output has no trailing newline.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"1.2.3KIBBLE-LINE b1:0 CODE=0",
			"KIBBLE-LINE b1:1 CODE=0",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusPass,
		WantLines:  []Status{StatusPass, StatusPass, StatusPass},
	}, { // Test 1: a failing line whose output has no trailing newline.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"boom-and-failKIBBLE-LINE b1:1 CODE=3",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusFail,
		WantLines:  []Status{StatusPass, StatusFail, StatusPass},
	}, { // Test 2: the newline the session prints keeps the marker on its own line.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"1.2.3",
			"KIBBLE-LINE b1:0 CODE=0",
			"KIBBLE-LINE b1:1 CODE=0",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusPass,
		WantLines:  []Status{StatusPass, StatusPass, StatusPass},
	}, { // Test 3: output glued to the marker is still that line's output.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"ffmpeg is not installedKIBBLE-LINE b1:1 CODE=1",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusPass,
		WantLines:  []Status{StatusPass, StatusSkipped, StatusPass},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			res := classifyExample(step, examplePlan(), test.Out, nil, 0)
			if res.Status != test.WantStatus {
				t.Errorf("status = %s, want %s (detail %q)", res.Status, test.WantStatus, res.Detail)
			}
			var got []Status
			for _, s := range res.example.Steps {
				for _, l := range s.Lines {
					got = append(got, l.Status)
				}
			}
			if diff := cmp.Diff(test.WantLines, got); diff != "" {
				t.Errorf("line statuses mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestResolveDependentFailures(t *testing.T) {
	t.Parallel()
	plan := &Plan{Binaries: []string{"tool"}}
	tests := []struct {
		Steps []exampleStep
		Want  []Status
	}{{ // Test 0: a failure citing a skipped command becomes a skip.
		Steps: []exampleStep{{ID: "b1", Lines: []lineResult{
			{Cmd: "tool recall x", Status: StatusFail,
				output: "no index found: run `tool reindex` first"},
			{Cmd: "tool reindex", Status: StatusSkipped},
		}}},
		Want: []Status{StatusSkipped, StatusSkipped},
	}, { // Test 1: a failure after a skip in the same family becomes a skip.
		Steps: []exampleStep{{ID: "b1", Lines: []lineResult{
			{Cmd: "tool encrypt enable", Status: StatusSkipped},
			{Cmd: "tool encrypt disable", Status: StatusFail, output: "vault is not encrypted"},
		}}},
		Want: []Status{StatusSkipped, StatusSkipped},
	}, { // Test 2: an unrelated failure stays a failure.
		Steps: []exampleStep{{ID: "b1", Lines: []lineResult{
			{Cmd: "tool stats", Status: StatusFail, output: "panic: bad state"},
		}}},
		Want: []Status{StatusFail},
	}, { // Test 2a: a failure whose output tells the reader to run a sibling
		// command that never ran is describing session state, not a hole in
		// the document.
		Steps: []exampleStep{{ID: "b1", Lines: []lineResult{
			{Cmd: "tool login", Status: StatusSkipped},
			{Cmd: "tool promote", Status: StatusFail,
				output: "tool: no hold id: pass --id or run tool request first"},
		}}},
		Want: []Status{StatusSkipped, StatusSkipped},
	}, { // Test 2b: the same failure stands when the named command did run
		// and passed, since then the document's sequence really is broken.
		Steps: []exampleStep{{ID: "b1", Lines: []lineResult{
			{Cmd: "tool request", Status: StatusPass},
			{Cmd: "tool promote", Status: StatusFail,
				output: "tool: no hold id: pass --id or run tool request first"},
		}}},
		Want: []Status{StatusPass, StatusFail},
	}, { // Test 3: a failure citing a line the document's own gap stopped is
		// a skip, so the gap is reported once instead of once per dependent
		// command. The gap itself stays a gap.
		Steps: []exampleStep{{ID: "b1", Lines: []lineResult{
			{Cmd: "tool recall x", Status: StatusFail,
				output: "no index found: run `tool reindex` first"},
			{Cmd: "tool reindex", Status: StatusGap},
		}}},
		Want: []Status{StatusSkipped, StatusGap},
	}, { // Test 4: a failure after a gap in the same family is a skip too.
		Steps: []exampleStep{{ID: "b1", Lines: []lineResult{
			{Cmd: "tool index build", Status: StatusGap},
			{Cmd: "tool index query", Status: StatusFail, output: "no index"},
		}}},
		Want: []Status{StatusGap, StatusSkipped},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			run := &exampleRun{Steps: test.Steps}
			resolveDependentFailures(run, plan)
			var got []Status
			for _, s := range run.Steps {
				for _, l := range s.Lines {
					got = append(got, l.Status)
				}
			}
			if diff := cmp.Diff(test.Want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSessionScript(t *testing.T) {
	t.Parallel()
	plan := examplePlan()
	plan.Packages = []string{"age"}
	plan.Env = map[string]string{"B": "2", "A": "1"}
	plan.Fixtures = []Fixture{{Path: "docs/notes.md", Contents: "hello\n"}}
	plan.Steps[0].Lines[1].Skip = "needs an interactive sign-in"
	script, wrapped := sessionScript(plan, 240)

	for _, want := range []string{
		"bash -ec 'go install example.com/tool@latest'",
		"apt-get install -y -qq --no-install-recommends age",
		"mkdir -p 'docs'",
		"export A='1'",
		"export B='2'",
		"KIBBLE-STEP b1 START",
		"timeout 90 tool init",
		`printf '\nKIBBLE-LINE b1:1 SKIP\n'`,
		"KIBBLE-DONE",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
		}
	}
	// Every marker starts its own line, so output that ends without a newline
	// cannot swallow the marker printed after it.
	for _, marker := range []string{"KIBBLE-STEP", "KIBBLE-LINE", "KIBBLE-DONE", "KIBBLE-BUILD"} {
		for _, line := range strings.Split(script, "\n") {
			if strings.Contains(line, marker) && !strings.Contains(line, `printf '\n`) {
				t.Errorf("marker %s printed without a leading newline: %q", marker, line)
			}
		}
	}
	if !wrapped["b1:0"] || !wrapped["b1:2"] {
		t.Errorf("wrapped = %v, want b1:0 and b1:2 wrapped", wrapped)
	}
	if idx := strings.Index(script, "export A='1'"); idx > strings.Index(script, "export B='2'") {
		t.Error("env exports are not in sorted order")
	}
}

func TestIsSimpleCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want bool
	}{{ // Test 0: a plain invocation wraps.
		In: "tool run notes.md", Want: true,
	}, { // Test 1: pipes do not wrap.
		In: "echo x | tool run", Want: false,
	}, { // Test 2: redirects do not wrap.
		In: "echo x > f.yaml", Want: false,
	}, { // Test 3: builtins do not wrap.
		In: "export K=v", Want: false,
	}, { // Test 4: assignments do not wrap.
		In: "PUB=$(tool key)", Want: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := isSimpleCommand(test.In); got != test.Want {
				t.Errorf("isSimpleCommand(%q) = %v, want %v", test.In, got, test.Want)
			}
		})
	}
}

// TestRedirectDirs checks that redirect targets get their parent directories
// created, so docs redirecting into standard user paths do not fail.
func TestRedirectDirs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In       string
		WantDirs []string
	}{{ // Test 0: a home-relative completion path.
		In:       "tool --gen bash > ~/.local/share/bash-completion/completions/tool",
		WantDirs: []string{`"$HOME"/.local/share/bash-completion/completions`},
	}, { // Test 1: a bare filename needs no directory.
		In: "tool list > out.txt",
	}, { // Test 2: a relative nested path.
		In:       "tool export > exports/data.json",
		WantDirs: []string{"'exports'"},
	}, { // Test 3: a target with an expansion is left alone.
		In: "tool export > $OUT/data.json",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := redirectDirs(test.In)
			if diff := cmp.Diff(test.WantDirs, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("dirs mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestResolveMissingBinaries checks that a failing line invoking a documented
// binary the session lacks becomes a skip, while other failures stay real.
func TestResolveMissingBinaries(t *testing.T) {
	t.Parallel()
	plan := &Plan{Binaries: []string{"tool", "conda"}}
	tests := []struct {
		Have map[string]bool
		In   []lineResult
		Want []Status
	}{{ // Test 0: the absent binary's failure becomes a skip.
		Have: map[string]bool{"tool": true},
		In: []lineResult{
			{Cmd: "conda install -c conda-forge tool", Status: StatusFail},
			{Cmd: "tool run", Status: StatusPass},
		},
		Want: []Status{StatusSkipped, StatusPass},
	}, { // Test 1: a failure on a present binary stays a failure.
		Have: map[string]bool{"tool": true},
		In:   []lineResult{{Cmd: "tool run", Status: StatusFail}},
		Want: []Status{StatusFail},
	}, { // Test 2: no recorded binaries leaves everything alone.
		Have: nil,
		In:   []lineResult{{Cmd: "conda install tool", Status: StatusFail}},
		Want: []Status{StatusFail},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			run := &exampleRun{Steps: []exampleStep{{ID: "b1", Lines: test.In}}}
			resolveMissingBinaries(run, plan, test.Have)
			var got []Status
			for _, l := range run.Steps[0].Lines {
				got = append(got, l.Status)
			}
			if diff := cmp.Diff(test.Want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRepoTar checks that generated directories are left out of the session
// stream. Streaming them wastes the budget on output nobody documents and can
// push the source a document needs past the cap.
func TestRepoTar(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"wasm/main.go":                   "package main",
		"README.md":                      "# T",
		"node_modules/pkg/index.js":      "x",
		"jetbrains/build/reports/a.html": "<html>",
		".git/config":                    "[core]",
		"target/debug/app":               "bin",
	}
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	data, truncated := repoTar(dir)
	if truncated {
		t.Error("small tree reported as truncated")
	}
	var got []string
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		got = append(got, h.Name)
	}
	sort.Strings(got)
	want := []string{"README.md", "wasm/main.go"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("streamed files mismatch (-want +got):\n%s", diff)
	}
}
