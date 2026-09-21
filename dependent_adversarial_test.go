package main

import (
	"fmt"
	kplan "github.com/dcadolph/kibble/internal/plan"
	"testing"
)

// TestSuppressionNeedsMoreThanAMention feeds resolveDependentFailures the
// shapes a textual dependency check gets wrong. Suppression here is safe by
// construction, since a suppressed line lands on BLOCKED rather than being
// excused, but a failure turned into BLOCKED on a coincidence is still a real
// documentation break that stopped being reported as one. These cases record
// which coincidences currently reach that far.
func TestSuppressionNeedsMoreThanAMention(t *testing.T) {
	t.Parallel()

	plan := &kplan.Plan{Binaries: []string{"tool"}}

	tests := []struct {
		Name    string
		Skipped lineResult
		Failing lineResult
		Want    Status
	}{{ // Test 0: the honest case. The failure names the skipped command as
		// the thing it needed, and the cascade should stop being reported.
		Name:    "genuine prerequisite",
		Skipped: lineResult{Cmd: "tool setup", Status: StatusSkipped},
		Failing: lineResult{Cmd: "tool deploy", Status: StatusFail,
			output: "error: run tool setup first"},
		Want: StatusBlocked,
	}, { // Test 1: the skipped command is named inside a URL, not as a cause.
		Name:    "named in a url",
		Skipped: lineResult{Cmd: "tool setup", Status: StatusSkipped},
		Failing: lineResult{Cmd: "tool deploy", Status: StatusFail,
			output: "error: bad flag. See https://example.com/tool setup/guide"},
		Want: StatusFail,
	}, { // Test 2: the failure is unrelated and merely echoes the argument the
		// document gave it, which happens to spell a skipped command.
		Name:    "echoed argument",
		Skipped: lineResult{Cmd: "tool setup", Status: StatusSkipped},
		Failing: lineResult{Cmd: "tool deploy", Status: StatusFail,
			output: `error: unknown value "tool setup" for --mode`},
		Want: StatusFail,
	}, { // Test 3: a help hint pointing at another subcommand is advice, not a
		// prerequisite the failing line depended on.
		Name:    "help hint",
		Skipped: lineResult{Cmd: "unrelated thing", Status: StatusSkipped},
		Failing: lineResult{Cmd: "tool deploy", Status: StatusFail,
			output: "error: bad flag\nTry tool help for usage."},
		Want: StatusFail,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			run := &exampleRun{Steps: []exampleStep{{
				ID:    "b1",
				Lines: []lineResult{test.Skipped, test.Failing},
			}}}
			resolveDependentFailures(run, plan)
			got := run.Steps[0].Lines[1].Status
			if got != test.Want {
				t.Errorf("status = %s, want %s (detail %q)",
					got, test.Want, run.Steps[0].Lines[1].Detail)
			}
		})
	}
}

// TestRepeatedSubcommandIsIndependent checks the family rule. Two documented
// invocations of the same subcommand are usually separate examples, so the
// second failing after the first was skipped is not evidence the first caused
// it.
func TestRepeatedSubcommandIsIndependent(t *testing.T) {
	t.Parallel()

	run := &exampleRun{Steps: []exampleStep{{
		ID: "b1",
		Lines: []lineResult{
			{Cmd: "tool build --debug", Status: StatusSkipped},
			{Cmd: "tool build --release", Status: StatusFail, output: "error: bad flag --release"},
		},
	}}}
	resolveDependentFailures(run, &kplan.Plan{Binaries: []string{"tool"}})
	if got := run.Steps[0].Lines[1].Status; got != StatusFail {
		t.Errorf("status = %s, want FAIL (detail %q)", got, run.Steps[0].Lines[1].Detail)
	}
}
