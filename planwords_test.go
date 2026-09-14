package main

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestPlannerReadsQuotedArguments checks that the planner reads a documented
// line the way a shell reads it, rather than by splitting on spaces. A quoted
// path is one argument, and a flag written inside a quoted argument is not a
// flag the line passes. Splitting on whitespace gets both wrong: it reports a
// fragment of a path as the missing file, and it finds flags in prose.
func TestPlannerReadsQuotedArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Line     string
		WantFile string
	}{{ // Test 0: a quoted path is one file, not the word before its space.
		Line: `tool validate "my config.json"`, WantFile: "my config.json",
	}, { // Test 1: the same shape unquoted is missing for the same reason.
		Line: `tool validate config.json`, WantFile: "config.json",
	}, { // Test 2: single quotes bind as tightly as double ones.
		Line: `tool validate 'my data.csv'`, WantFile: "my data.csv",
	}, { // Test 3: a path the repo ships is not missing, spaces and all.
		Line: `tool validate "have space.json"`, WantFile: "",
	}, { // Test 4: an extension kibble can fabricate becomes a fixture rather
		// than a missing file, and the space does not change that.
		Line: `tool read "my notes.md"`, WantFile: "",
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			pl := &planner{
				plan:     &Plan{},
				binaries: map[string]bool{"tool": true},
				tree:     map[string]bool{"have space.json": true},
				created:  map[string]bool{},
				fixed:    map[string]bool{},
			}
			got := pl.missingFile(test.Line)
			if diff := cmp.Diff(test.WantFile, got); diff != "" {
				t.Errorf("missingFile mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestInteractiveFlagIgnoresQuotedText checks that --interactive counts only
// when the line passes it. A document explaining the flag inside a quoted
// argument is describing it, not using it, and skipping that line would leave
// a working example unverified.
func TestInteractiveFlagIgnoresQuotedText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Line string
		Want bool
	}{{ // Test 0: the flag is passed, so the line is interactive.
		Line: `tool run --interactive`, Want: true,
	}, { // Test 1: the flag sits inside a quoted argument, so it is text.
		Line: `tool run --message "pass --interactive to prompt"`, Want: false,
	}, { // Test 2: no flag at all.
		Line: `tool run`, Want: false,
	}, { // Test 3: the short form never counts, quoted or not.
		Line: `tool run -i`, Want: false,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := interactiveFlag(test.Line)
			if diff := cmp.Diff(test.Want, got); diff != "" {
				t.Errorf("interactiveFlag mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestShellFirstWord checks the program name a line runs, including the cases
// that made indexing a whitespace split wrong: a blank line has no program and
// must not panic, and a quoted program name is one word.
func TestShellFirstWord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Line string
		Want string
	}{{ // Test 0: an ordinary invocation.
		Line: "tool build", Want: "tool",
	}, { // Test 1: a blank line runs nothing and returns nothing.
		Line: "", Want: "",
	}, { // Test 2: whitespace only is still nothing.
		Line: "   ", Want: "",
	}, { // Test 3: an assignment prefix is not the program.
		Line: "FOO=1 tool build", Want: "tool",
	}, { // Test 4: a quoted program name holding a space is one word.
		Line: `"my tool" build`, Want: "my tool",
	}, { // Test 5: a line no bash parser accepts still yields its first word.
		Line: `tool "unclosed`, Want: "tool",
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := shellFirstWord(test.Line)
			if diff := cmp.Diff(test.Want, got); diff != "" {
				t.Errorf("shellFirstWord mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
