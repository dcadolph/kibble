package main

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	kplan "github.com/dcadolph/kibble/internal/plan"
)

// TestClassifyExampleDocumentExit checks how lines with no marker are read.
// A session that finished means the shell that stopped short was one
// document's own, so its remaining lines say so and later documents keep
// their results. A session that never finished still reports the line it
// stopped on as the hang.
func TestClassifyExampleDocumentExit(t *testing.T) {
	t.Parallel()
	const exited = "not established: the document's shell exited at or before this line"
	const ended = "not run: the session ended before reaching it"
	plan := &kplan.Plan{Repo: "repo", Steps: []kplan.PlanStep{
		{ID: "b1", Doc: "README.md", Lines: []kplan.PlanLine{{Cmd: "echo a"}, {Cmd: "exit 3"}, {Cmd: "echo b"}}},
		{ID: "b2", Doc: "docs/guide.md", Lines: []kplan.PlanLine{{Cmd: "echo c"}}},
	}}
	step := InstallStep{Repo: "repo", Kind: "example"}
	tests := []struct {
		Out         string
		WantStatus  []Status
		WantDetails []string
	}{{ // Test 0: The first document exits and the second still runs.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"a",
			"KIBBLE-LINE b1:0 CODE=0",
			"KIBBLE-STEP b2 START",
			"c",
			"KIBBLE-LINE b2:0 CODE=0",
			"KIBBLE-DONE"),
		WantStatus:  []Status{StatusVerified, StatusBlocked, StatusBlocked, StatusVerified},
		WantDetails: []string{"", exited, exited, ""},
	}, { // Test 1: The session stops on the line and never finishes.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"a",
			"KIBBLE-LINE b1:0 CODE=0"),
		WantStatus:  []Status{StatusVerified, StatusTimeout, StatusBlocked, StatusBlocked},
		WantDetails: []string{"", "session ended while this line ran", ended, ended},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			res := classifyExample(step, plan, test.Out, nil, 0, lineTimeout)
			if res.example == nil {
				t.Fatalf("no per-line outcomes, status %s: %s", res.Status, res.Detail)
			}
			var status []Status
			var details []string
			for _, s := range res.example.Steps {
				for _, l := range s.Lines {
					status = append(status, l.Status)
					if l.Status == StatusVerified {
						details = append(details, "")
						continue
					}
					details = append(details, l.Detail)
				}
			}
			if diff := cmp.Diff(test.WantStatus, status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantDetails, details); diff != "" {
				t.Errorf("detail mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
