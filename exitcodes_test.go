package main

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	kplan "github.com/dcadolph/kibble/internal/plan"
)

// TestDocumentedNotFoundCode checks that a documented binary exiting with a
// code its documents call not found reads as no data, and that any other
// code, or a code the documents never named, still fails.
func TestDocumentedNotFoundCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Codes      []int
		Cmd        string
		Output     string
		Code       int
		WantStatus Status
	}{{ // Test 0: witr's exit 2 with nothing printed is its documented not found.
		Codes: []int{2}, Cmd: "witr --port 5000 --json", Code: 2,
		WantStatus: StatusSkipped,
	}, { // Test 1: witr's exit 2 naming a missing process is the same lookup.
		Codes: []int{2}, Cmd: "witr --pid 8123 --warnings", Output: "Error: process 8123 does not exist",
		Code: 2, WantStatus: StatusSkipped,
	}, { // Test 2: another documented code still fails.
		Codes: []int{2}, Cmd: "witr --pid 1", Output: "internal error", Code: 5,
		WantStatus: StatusFail,
	}, { // Test 3: with no table the same exit fails.
		Cmd: "witr --port 5000 --json", Output: "}", Code: 2,
		WantStatus: StatusFail,
	}, { // Test 4: a line that is not the documented binary is not excused.
		Codes: []int{2}, Cmd: "ls /nope", Output: "ls: cannot access '/nope'", Code: 2,
		WantStatus: StatusFail,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := &kplan.Plan{Repo: "witr", Binaries: []string{"witr"}, NotFoundCodes: test.Codes,
				Steps: []kplan.PlanStep{{ID: "b1", Doc: "README.md", Heading: "Usage",
					Lines: []kplan.PlanLine{{Cmd: test.Cmd}}}}}
			out := sessionOut("KIBBLE-BUILD CODE=0", "KIBBLE-STEP b1 START", test.Output,
				fmt.Sprintf("KIBBLE-LINE b1:0 CODE=%d CWD=/work/repo", test.Code), "KIBBLE-DONE")
			res := classifyExample(InstallStep{Repo: "witr", Kind: "example"}, plan, out, nil, 0,
				lineTimeout, sessionEnd{})
			if res.example == nil {
				t.Fatalf("no per-line outcomes, status %s: %s", res.Status, res.Detail)
			}
			got := res.example.Steps[0].Lines[0]
			if diff := cmp.Diff(test.WantStatus, got.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s\ndetail: %s", diff, got.Detail)
			}
		})
	}
}
