package main

import (
	"fmt"
	"testing"

	kplan "github.com/dcadolph/kibble/internal/plan"
)

// TestTerminalErrorIsNotAFailure checks the verdict when a documented line
// needs a terminal the container does not have. fzf draws its interface on a
// terminal, and without one it exits 2 printing only the text of ENOTTY. The
// line is a reader's to run at a keyboard, so the honest verdict is that the
// container could not judge it.
func TestTerminalErrorIsNotAFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Name       string
		Output     string
		Code       int
		WantStatus Status
		WantReason Reason
	}{{ // Test 0: fzf without a terminal, verbatim.
		Name: "fzf ENOTTY", Output: "inappropriate ioctl for device", Code: 2,
		WantStatus: StatusSkipped, WantReason: ReasonInteractive,
	}, { // Test 1: the capitalized strerror form other tools print.
		Name: "strerror form", Output: "error: Inappropriate ioctl for device", Code: 1,
		WantStatus: StatusSkipped, WantReason: ReasonInteractive,
	}, { // Test 2: a wording matched before this change still is.
		Name: "dev tty", Output: "failed to open /dev/tty", Code: 1,
		WantStatus: StatusSkipped, WantReason: ReasonInteractive,
	}, { // Test 3: a line that exits 0 passes whatever it printed.
		Name: "passing line", Output: "inappropriate ioctl for device", Code: 0,
		WantStatus: StatusVerified,
	}, { // Test 4: an ordinary failure is untouched.
		Name: "unrelated failure", Output: "error: no such option --heigth", Code: 2,
		WantStatus: StatusFail,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			lr := classifyLineResult(lineResult{Cmd: "fzf --height 40%"},
				kplan.PlanLine{}, lineOutcome{code: test.Code, output: test.Output}, false, nil, lineTimeout)
			if lr.Status != test.WantStatus {
				t.Errorf("status = %s, want %s (detail %q)", lr.Status, test.WantStatus, lr.Detail)
			}
			if lr.Reason != test.WantReason {
				t.Errorf("reason = %q, want %q", lr.Reason, test.WantReason)
			}
		})
	}
}
