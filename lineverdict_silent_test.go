package main

import (
	"fmt"
	kplan "github.com/dcadolph/kibble/internal/plan"
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
		Logged     bool
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
	}, { // Test 7: a line whose step redirected output to a log is not quiet,
		// it is unobserved. Excusing it would turn kibble's blind spot into the
		// document's alibi, so the exit code still convicts.
		Name: "logged", Code: 4, Logged: true, WantStatus: StatusFail,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			lr := classifyLineResult(lineResult{Cmd: "tool check"}, kplan.PlanLine{},
				lineOutcome{code: test.Code, output: "", logged: test.Logged},
				test.Wrapped, nil, lineTimeout)
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
			lr := classifyLineResult(lineResult{Cmd: "time rg pattern"}, kplan.PlanLine{},
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

// TestHelperProgramNotStarted checks the verdict when a tool cannot launch a
// helper program a flag named. The shell never runs that program, so its own
// "not found" never appears and the rules reading for it see nothing. A
// document that shows the reader a script and never writes it to disk is
// incomplete, which is a gap and not a broken command.
func TestHelperProgramNotStarted(t *testing.T) {
	t.Parallel()

	const rgErr = `rg: bench/raw.csv: preprocessor command could not start: ` +
		`'"pre-rg" "bench/raw.csv"': No such file or directory (os error 2)`

	tests := []struct {
		Name       string
		Cmd        string
		Output     string
		WantStatus Status
	}{{ // Test 0: the helper is named by the line, so the document is missing
		// the step that would create it.
		Name: "helper named by the line", Cmd: "rg --pre pre-rg 'fn is_empty' -c",
		Output: rgErr, WantStatus: StatusGap,
	}, { // Test 1: a helper the line never names is not this line's gap.
		Name: "helper not in the line", Cmd: "rg 'fn is_empty' -c",
		Output: rgErr, WantStatus: StatusFail,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			lr := classifyLineResult(lineResult{Cmd: test.Cmd}, kplan.PlanLine{},
				lineOutcome{code: 2, output: test.Output}, false, nil, lineTimeout)
			if lr.Status != test.WantStatus {
				t.Errorf("status = %s, want %s (detail %q)", lr.Status, test.WantStatus, lr.Detail)
			}
		})
	}
}

// TestNothingSearchedIsUnsettled checks the verdict when a search's own
// filters leave it nothing to read. ripgrep's guide demonstrates `-tc` against
// whatever tree the reader has, and ripgrep's own tree holds no C, so the
// example finds nothing. The command worked. The corpus simply does not
// contain what the example targets, which is a fact about the corpus and not
// a hole in the document.
func TestNothingSearchedIsUnsettled(t *testing.T) {
	t.Parallel()

	const rgOut = "No files were searched, which means ripgrep probably applied a " +
		"filter you didn't expect.\nRunning with --debug will show why files are being skipped."

	tests := []struct {
		Name       string
		Output     string
		Code       int
		WantStatus Status
	}{{ // Test 0: the filters excluded everything, so nothing was established.
		Name: "nothing searched", Output: rgOut, Code: 2, WantStatus: StatusBlocked,
	}, { // Test 1: a real error alongside it is still a real error.
		Name:   "genuine error",
		Output: "error: unknown flag --nope", Code: 2, WantStatus: StatusFail,
	}, { // Test 2: a clean exit is unaffected.
		Name: "clean", Output: "", Code: 0, WantStatus: StatusVerified,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			lr := classifyLineResult(lineResult{Cmd: "rg 'int main' -tc"}, kplan.PlanLine{},
				lineOutcome{code: test.Code, output: test.Output}, false, nil, lineTimeout)
			if lr.Status != test.WantStatus {
				t.Errorf("status = %s, want %s (detail %q)", lr.Status, test.WantStatus, lr.Detail)
			}
		})
	}
}
