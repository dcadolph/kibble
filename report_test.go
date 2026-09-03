package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestVerdictLine checks the headline a run claims. A run that failed nothing
// but could not settle everything must not read as verified, because the whole
// point of the verdict taxonomy is lost the moment the summary rounds it off.
func TestVerdictLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Fail  int
		Gap   int
		Other int
		Want  string
	}{{ // Test 0: everything ran and worked.
		Want: "VERIFIED",
	}, { // Test 1: a documented line broke.
		Fail: 1, Want: "FAILED",
	}, { // Test 2: nothing broke, but a gap went unsettled.
		Gap: 1, Want: "INCOMPLETE",
	}, { // Test 3: a skip or timeout is unsettled too.
		Other: 2, Want: "INCOMPLETE",
	}, { // Test 4: a failure outranks a gap, since it is the stronger claim.
		Fail: 1, Gap: 3, Want: "FAILED",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := verdictLine(palette{}, test.Fail, test.Gap, test.Other)
			if !strings.HasPrefix(got, test.Want) {
				t.Errorf("verdict = %q, want it to start with %q", got, test.Want)
			}
		})
	}
}

// TestWriteFailure checks that a failed result prints where the broken line
// lives and what it was. The CI annotation has always carried the file, the
// line, and the command; a person at a terminal was shown a truncated error
// and left to find the rest themselves.
func TestWriteFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Result Result
		Want   []string
		WantNo string
	}{{ // Test 0: an install failure names its README line and command.
		Result: Result{
			Step: InstallStep{
				Kind: "go-install", Raw: "go install example.com/tool@latest",
				Line: 42, dir: "myrepo", readme: "README.md",
			},
			Status: StatusFail,
			Detail: "unrecognized import path",
		},
		Want: []string{"myrepo/README.md:42", "go install example.com/tool@latest", "unrecognized import path"},
	}, { // Test 1: an example failure names the failing line inside the block,
		// not the block's own start.
		Result: Result{
			Step:   InstallStep{Kind: "example", dir: "myrepo", readme: "README.md"},
			Status: StatusFail,
			example: &exampleRun{Steps: []exampleStep{{
				ID: "b1",
				Lines: []lineResult{
					{Cmd: "tool init", Status: StatusPass, Line: 10},
					{Cmd: "tool start", Status: StatusFail, Line: 11, Detail: `unknown command "start"`},
				},
			}}},
		},
		Want: []string{"myrepo/README.md:11", "tool start", `unknown command "start"`},
	}, { // Test 2: a passing result prints nothing at all.
		Result: Result{
			Step:   InstallStep{Kind: "go-install", Raw: "go install example.com/tool@latest", Line: 4},
			Status: StatusPass,
		},
		WantNo: "README",
	}, { // Test 3: a gap is reported in the row, not as a broken line, since
		// nothing was executed and found wanting.
		Result: Result{
			Step:   InstallStep{Kind: "brew", Raw: "brew install nope", Line: 9},
			Status: StatusGap,
			Detail: "no formula named nope",
		},
		WantNo: "brew install nope",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			writeFailure(&buf, palette{}, test.Result)
			got := buf.String()
			for _, want := range test.Want {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q:\n%s", want, got)
				}
			}
			if test.WantNo != "" && strings.Contains(got, test.WantNo) {
				t.Errorf("output should not mention %q:\n%s", test.WantNo, got)
			}
		})
	}
}

// TestWrapDetail checks the error wrapper keeps whole words and elides a long
// tail rather than running off the screen.
func TestWrapDetail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In    string
		Width int
		Want  []string
	}{{ // Test 0: a short message is one line.
		In: "unknown flag", Width: 20, Want: []string{"unknown flag"},
	}, { // Test 1: empty output produces no lines at all.
		In: "   ", Width: 20, Want: nil,
	}, { // Test 2: words wrap without being cut in half.
		In: "aaa bbb ccc ddd", Width: 7, Want: []string{"aaa bbb", "ccc ddd"},
	}, { // Test 3: a token longer than the width is left whole, since cutting
		// a URL or a module path makes it useless.
		In: "https://example.com/a/very/long/path", Width: 10,
		Want: []string{"https://example.com/a/very/long/path"},
	}, { // Test 4: more than four lines elides rather than filling the screen.
		In: "a b c d e f g h i j k l", Width: 1,
		Want: []string{"a", "b", "c", "d …"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := wrapDetail(test.In, test.Width)
			if diff := cmp.Diff(test.Want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
