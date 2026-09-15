package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestSilentNonzeroIsBlocked pins where silence stops being an excuse. A
// command that exits nonzero and prints nothing has told the reader nothing,
// so the document is neither proven nor broken. The exception is the set of
// codes that are evidence on their own: a timeout, a shell that could not run
// the command, and a death by signal all say what happened without printing.
func TestSilentNonzeroIsBlocked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Name       string
		Code       int
		Wrapped    bool
		WantStatus Status
	}{{ // Test 0: a quiet exit 1, which a search does on no match.
		Name: "exit1", Code: 1, WantStatus: StatusBlocked,
	}, { // Test 1: a quiet exit 2, which a tool does for "an error occurred".
		// This is the ripgrep case: reporting the document broken on it would
		// claim more than the run established.
		Name: "exit2", Code: 2, WantStatus: StatusBlocked,
	}, { // Test 2: the top of the ordinary range is still ordinary.
		Name: "exit125", Code: 125, WantStatus: StatusBlocked,
	}, { // Test 3: a segfault is evidence, so silence does not excuse it.
		Name: "segfault", Code: 139, WantStatus: StatusFail,
	}, { // Test 4: an abort likewise.
		Name: "abort", Code: 134, WantStatus: StatusFail,
	}, { // Test 5: 126 is the shell saying it could not execute the command.
		Name: "cannot-execute", Code: 126, WantStatus: StatusFail,
	}, { // Test 6: a wrapped 124 is the timeout kibble imposed, not silence.
		Name: "timeout", Code: 124, Wrapped: true, WantStatus: StatusTimeout,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			lr := classifyLineResult(lineResult{Cmd: "tool check"}, PlanLine{},
				lineOutcome{code: test.Code, output: ""}, test.Wrapped, nil, lineTimeout)
			if lr.Status != test.WantStatus {
				t.Errorf("status = %s, want %s (detail %q)", lr.Status, test.WantStatus, lr.Detail)
			}
		})
	}
}

// TestShellTimingIsNotOutput checks that the shell's `time` report does not
// count as the command speaking. A documented `time rg ...` that exits nonzero
// and prints nothing else has said nothing, and the timing lines must not
// become its explanation or make silence look like speech.
func TestShellTimingIsNotOutput(t *testing.T) {
	t.Parallel()

	timing := "\nreal\t0m0.102s\nuser\t0m0.030s\nsys\t0m0.102s\n"
	tests := []struct {
		Name       string
		Output     string
		Code       int
		WantStatus Status
	}{{ // Test 0: timing alone is silence, so the line settles nothing.
		Name: "timing only", Output: timing, Code: 2, WantStatus: StatusBlocked,
	}, { // Test 1: a real error with timing after it keeps the error.
		Name: "error then timing", Output: "rg: bad pattern" + timing,
		Code: 2, WantStatus: StatusFail,
	}, { // Test 2: timing on a clean exit changes nothing.
		Name: "timing on success", Output: timing, Code: 0, WantStatus: StatusVerified,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			lr := classifyLineResult(lineResult{Cmd: "time rg pattern"}, PlanLine{},
				lineOutcome{code: test.Code, output: test.Output}, false, nil, lineTimeout)
			if lr.Status != test.WantStatus {
				t.Errorf("status = %s, want %s (detail %q)", lr.Status, test.WantStatus, lr.Detail)
			}
			if strings.Contains(lr.Detail, "0m0.") {
				t.Errorf("detail carries the timing report: %q", lr.Detail)
			}
		})
	}
}
