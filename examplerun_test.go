package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// examplePlan builds a small plan for executor tests: one step of runnable
// lines around one planned skip.
func examplePlan() *Plan {
	return &Plan{
		Repo: "repo", Modules: []string{"example.com/tool@latest"}, Binaries: []string{"tool"},
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
	}, { // Test 3: credential errors downgrade to a skip.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"KIBBLE-LINE b1:0 CODE=0",
			"error: ANTHROPIC_API_KEY is not set",
			"KIBBLE-LINE b1:1 CODE=2",
			"KIBBLE-LINE b1:2 CODE=0",
			"KIBBLE-DONE"),
		WantStatus: StatusPass,
		WantLines:  []Status{StatusPass, StatusSkipped, StatusPass},
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
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			res := classifyExample(step, examplePlan(), test.Out, test.Wrapped, 0)
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
		"go install 'example.com/tool@latest'",
		"apt-get install -y -qq --no-install-recommends age",
		"mkdir -p 'docs'",
		"export A='1'",
		"export B='2'",
		"KIBBLE-STEP b1 START",
		"timeout 90 tool init",
		"printf 'KIBBLE-LINE b1:1 SKIP\\n'",
		"KIBBLE-DONE",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
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
