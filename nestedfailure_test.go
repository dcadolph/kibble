package main

import (
	"fmt"
	"testing"
)

// TestNestedCommandNotFound checks the verdict when a tool reports that a
// command it was asked to run does not exist. hyperfine documents
// `hyperfine 'hexdump file' 'xxd file'`, the container has neither program, and
// hyperfine exits 1 while saying the thing it benchmarked exited 127. The line
// kibble executed worked. What is absent is a program the container lacks, and
// blaming the document for that is the container's gap wearing the document's
// name.
func TestNestedCommandNotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Name       string
		Output     string
		WantStatus Status
	}{{ // Test 0: the hyperfine case, verbatim.
		Name: "benchmark harness",
		Output: "Error: Command terminated with non-zero exit code 127 in the first benchmark run. " +
			"Use the '-i'/'--ignore-failure' option if you want to ignore this.",
		WantStatus: StatusSkipped,
	}, { // Test 1: the plainer wording a runner might use.
		Name:       "plain wording",
		Output:     "child process exited 127",
		WantStatus: StatusSkipped,
	}, { // Test 2: an ordinary failure is untouched, so this cannot excuse a
		// real break. 127 has to actually appear as an exit status.
		Name:       "unrelated failure",
		Output:     "error: only 127 of 200 records matched",
		WantStatus: StatusFail,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			lr := classifyLineResult(lineResult{Cmd: "hyperfine 'hexdump file' 'xxd file'"},
				PlanLine{}, lineOutcome{code: 1, output: test.Output}, false, nil, lineTimeout)
			if lr.Status != test.WantStatus {
				t.Errorf("status = %s, want %s (detail %q)", lr.Status, test.WantStatus, lr.Detail)
			}
		})
	}
}

// TestCargoWrapperLineIsNotTheExplanation checks that cargo's closing line does
// not become a failure's detail. It is printed after the compiler has already
// said what broke, and being last it is what a tail-first reader hands back.
// mise's install reported exactly this and nothing else.
func TestCargoWrapperLineIsNotTheExplanation(t *testing.T) {
	t.Parallel()

	lines := []string{
		"error[E0433]: cannot find `lms` in the crate root",
		"warning: build failed, waiting for other jobs to finish...",
	}
	got := failureLine(lines)
	if got == "warning: build failed, waiting for other jobs to finish..." {
		t.Errorf("the wrapper line became the explanation: %q", got)
	}
	if got != "error[E0433]: cannot find `lms` in the crate root" {
		t.Errorf("failureLine = %q, want the compiler error", got)
	}
}
