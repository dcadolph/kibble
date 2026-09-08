package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestClassifyFalseNegatives feeds classifyLineResult the output a broken
// documented line really produces when that output happens to read like one
// of the conditions kibble excuses. Every case is a line the reader would
// find broken, worded so a pattern match alone would call it fine. The suite
// exists because the mutation corpus only proves kibble catches damage that
// looks like damage; this proves it does not launder damage that looks like
// an excuse. A verdict of SKIP here is the one failure the tool cannot
// afford, since it tells a reader the documentation was never in question.
func TestClassifyFalseNegatives(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Cmd        string
		Output     string
		Documented map[string]bool
		WantStatus Status
		Code       int
	}{{ // Test 0: a migration that died silently. Exiting 1 with nothing to
		// say is how a search reports no match, so kibble excused it; it is
		// also how a command dies. Nothing here settles which, and BLOCKED is
		// the only honest reading of silence.
		Cmd: "mytool migrate", Code: 1, Output: "",
		WantStatus: StatusBlocked,
	}, { // Test 1: the tool's own lookup failure worded like a missing shell
		// command. The shell says "not found" with exit 127; this is the tool
		// saying it about a record the line itself names, at exit 2, so the
		// container lacks nothing and only the name says so.
		Cmd: "mytool get widget", Code: 2, Output: "widget: not found",
		WantStatus: StatusFail,
	}, { // Test 2: a 403 earned by a documented argument that is wrong. The
		// status code reads as missing credentials and may be, or may be the
		// bucket name in the document. Kibble cannot tell the two apart.
		Cmd:  "mytool push --bucket typo",
		Code: 1, Output: `error: 403 Forbidden: bucket "typo" does not exist`,
		WantStatus: StatusBlocked,
	}, { // Test 3: the tool naming its own plugin as not installed. That is a
		// step the document owes the reader, not a system package the
		// container is missing, so it must not read as the container's fault.
		Cmd: "mytool build", Code: 1, Output: `error: plugin "zig" is not installed`,
		WantStatus: StatusBlocked,
	}, { // Test 4: a crash that also printed an empty-result line. The panic
		// convicts regardless of what preceded it, so the no-data excuse must
		// not reach it.
		Cmd: "mytool report", Code: 2,
		Output:     "no records\npanic: assignment to entry in nil map",
		WantStatus: StatusFail,
	}, { // Test 5: a documented endpoint pointing at a port nothing serves.
		// The refusal is real; whether the document forgot to start the
		// service or named the wrong port is not established by it.
		Cmd:  "mytool sync --endpoint http://localhost:9999",
		Code: 1, Output: "dial tcp 127.0.0.1:9999: connection refused",
		WantStatus: StatusBlocked,
	}, { // Test 6: exit 127 from the shell keeps its skip. The evidence is the
		// shell's own exit code, not a phrase in the output, so this is the
		// case the loosened rules must not cost.
		Cmd: "mytool render", Code: 127, Output: "bash: pandoc: command not found",
		WantStatus: StatusSkipped,
	}, { // Test 7: a clean exit stays verified. The suite must not earn its
		// other rows by convicting everything.
		Cmd: "mytool walk", Code: 0, Output: "walked 3 paths",
		WantStatus: StatusVerified,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			lr := lineResult{Cmd: test.Cmd, Code: -1}
			o := lineOutcome{code: test.Code, output: test.Output}
			got := classifyLineResult(lr, PlanLine{Cmd: test.Cmd}, o, false, test.Documented)
			if diff := cmp.Diff(test.WantStatus, got.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s\ndetail: %s", diff, got.Detail)
			}
		})
	}
}

// TestSyntheticInputIsNotABarePass checks that a line which only ran because
// kibble invented a file says so. The document referenced the file and never
// created it, so the exit proves the command accepts kibble's input, not that
// the documented example works. A bare VERIFIED would claim the second.
func TestSyntheticInputIsNotABarePass(t *testing.T) {
	t.Parallel()

	lr := lineResult{Cmd: "tool render notes.md", Code: -1, Synthetic: []string{"notes.md"}}
	got := classifyLineResult(lr, PlanLine{}, lineOutcome{code: 0}, false, nil)
	if got.Status != StatusVerified {
		t.Errorf("status = %s, want %s", got.Status, StatusVerified)
	}
	if !strings.Contains(got.Detail, "notes.md") || !strings.Contains(got.Detail, "fabricated") {
		t.Errorf("detail = %q, want it to name notes.md as fabricated", got.Detail)
	}

	// The count travels to the summary, since the summary is the line a
	// reader believes before they open the JSON.
	run := &exampleRun{Steps: []exampleStep{{ID: "b1", Lines: []lineResult{
		got,
		{Cmd: "tool version", Status: StatusVerified},
	}}}}
	_, detail := summarize(run)
	if !strings.Contains(detail, "1 against fabricated files") {
		t.Errorf("summary detail = %q, want the fabricated count", detail)
	}
}

// TestPlanMarksSyntheticInput checks the planner end of the same claim: a
// documented line reading a file no documented step creates is planned with
// the fabricated fixture recorded on it, rather than silently fixed up.
func TestPlanMarksSyntheticInput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	readme := "# tool\n\n## Install\n\n```sh\ngo install example.com/tool@latest\n```\n\n" +
		"## Usage\n\n```sh\ntool render notes.md\n```\n"
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o644); err != nil {
		t.Fatal(err)
	}
	steps, _, _ := collect([]string{dir}, true)
	var found []string
	for _, s := range steps {
		if s.plan == nil {
			continue
		}
		for _, ps := range s.plan.Steps {
			for _, l := range ps.Lines {
				if strings.Contains(l.Cmd, "notes.md") {
					found = l.Synthetic
				}
			}
		}
	}
	if diff := cmp.Diff([]string{"notes.md"}, found); diff != "" {
		t.Errorf("synthetic mismatch (-want +got):\n%s", diff)
	}
}

// TestDependentFailureFalseNegatives checks the causal downgrades against
// failures that only look dependent. Kibble excuses a failure when its output
// names a command that did not run, and when an earlier line of the same
// binary and subcommand was skipped. Neither observation establishes cause: a
// tool prints its own name in hints, and a second invocation of a subcommand
// is usually independent of the first. The downgrade may still suppress a
// cascade, but it may not conclude the documentation is fine.
func TestDependentFailureFalseNegatives(t *testing.T) {
	t.Parallel()

	plan := &Plan{Binaries: []string{"mytool"}}

	tests := []struct {
		Name       string
		Lines      []lineResult
		WantStatus Status
	}{{ // Test 0: a config parse error whose hint happens to name a skipped
		// command. The failure is on line 3 of a config file and has nothing
		// to do with sync; the word "sync" in a docs pointer is not cause.
		Name: "hint names a skipped command",
		Lines: []lineResult{
			{Cmd: "mytool sync --token TOKEN", Status: StatusSkipped},
			{
				Cmd: "mytool walk .", Status: StatusFail,
				output: "error: config parse failed at line 3\nhint: see docs for mytool sync",
			},
		},
		WantStatus: StatusBlocked,
	}, { // Test 1: an independent second invocation of a skipped subcommand.
		// The first was skipped for a placeholder; the second names a real
		// path and failed on its own merits.
		Name: "same subcommand family, independent failure",
		Lines: []lineResult{
			{Cmd: "mytool walk --depth $DEPTH", Status: StatusSkipped},
			{Cmd: "mytool walk .", Status: StatusFail, output: "error: permission denied"},
		},
		WantStatus: StatusBlocked,
	}, { // Test 2: a failure naming nothing that was skipped stays a failure.
		Name: "unrelated failure keeps its verdict",
		Lines: []lineResult{
			{Cmd: "mytool sync --token TOKEN", Status: StatusSkipped},
			{Cmd: "mytool build", Status: StatusFail, output: "error: unknown flag --fast"},
		},
		WantStatus: StatusFail,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			run := &exampleRun{Steps: []exampleStep{{ID: "b1", Lines: test.Lines}}}
			resolveDependentFailures(run, plan)
			got := run.Steps[0].Lines[len(test.Lines)-1].Status
			if diff := cmp.Diff(test.WantStatus, got); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
