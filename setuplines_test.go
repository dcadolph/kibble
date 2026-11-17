package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	kplan "github.com/dcadolph/kibble/internal/plan"
)

// TestSetupLinesSettleNothing checks that a run whose only passing lines set
// up the session reads as having tested nothing. headscale's documents wrote
// six files, exported two variables, and made a directory, every line of its
// own tool was blocked, and the run exited 0 as if a claim had been settled.
func TestSetupLinesSettleNothing(t *testing.T) {
	t.Parallel()
	setup := []kplan.PlanLine{
		{Cmd: "export HEADSCALE_DERP_URLS='[]'"},
		{Cmd: "cat > 'config.yaml' <<'KIBBLE_FILE_END'\nkey: 1\nKIBBLE_FILE_END", File: "config.yaml"},
		{Cmd: "mkdir -p ./headscale/{config,lib}"},
	}
	tests := []struct {
		Extra      []kplan.PlanLine
		Codes      []int
		Output     string
		WantStatus Status
		WantExit   int
	}{{ // Test 0: only setup passed, so nothing was tested.
		Codes: []int{0, 0, 0}, WantStatus: StatusSkipped, WantExit: 3,
	}, { // Test 1: a line of the tool passed beside the setup.
		Extra: []kplan.PlanLine{{Cmd: "headscale version"}}, Codes: []int{0, 0, 0, 0},
		WantStatus: StatusVerified, WantExit: 0,
	}, { // Test 2: a line of the tool failed beside the setup.
		Extra: []kplan.PlanLine{{Cmd: "headscale version"}}, Codes: []int{0, 0, 0, 2},
		Output: "Error: unknown flag: --nope", WantStatus: StatusFail, WantExit: 1,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			lines := append(append([]kplan.PlanLine(nil), setup...), test.Extra...)
			plan := &kplan.Plan{Repo: "headscale", Binaries: []string{"headscale"},
				Steps: []kplan.PlanStep{{ID: "b1", Doc: "README.md", Heading: "Setup", Lines: lines}}}
			out := []string{"KIBBLE-BUILD CODE=0", "KIBBLE-STEP b1 START"}
			for i, code := range test.Codes {
				if i == len(test.Codes)-1 && test.Output != "" {
					out = append(out, test.Output)
				}
				out = append(out, fmt.Sprintf("KIBBLE-LINE b1:%d CODE=%d CWD=/work/repo", i, code))
			}
			out = append(out, "KIBBLE-DONE")
			res := classifyExample(InstallStep{Repo: "headscale", Kind: "example"}, plan, sessionOut(out...),
				nil, 0, lineTimeout, sessionEnd{})
			if diff := cmp.Diff(test.WantStatus, res.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s\ndetail: %s", diff, res.Detail)
			}
			if diff := cmp.Diff(test.WantExit, exitCode([]Result{res}, false)); diff != "" {
				t.Errorf("exit code mismatch (-want +got):\n%s", diff)
			}
			var b bytes.Buffer
			reportJSON(&b, []Result{res})
			if got := strings.Count(b.String(), `"setup": true`); got != len(setup) {
				t.Errorf("JSON marks %d setup lines, want %d:\n%s", got, len(setup), b.String())
			}
		})
	}
}
