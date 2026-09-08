package main

import (
	"fmt"
	"testing"
)

// TestRuleSelects checks the structured selectors against what a line
// actually invokes, rather than against its text. A rule naming a tool must
// not fire on a line that merely mentions the tool's name.
func TestRuleSelects(t *testing.T) {
	t.Parallel()

	pl := &planner{binaries: map[string]bool{"tool": true}}
	tests := []struct {
		Rule StepRule
		Line string
		Want bool
	}{{ // Test 0: binary and subcommand both match what is invoked.
		Rule: StepRule{Binary: "tool", Subcommand: "serve"},
		Line: "tool serve --port 8080", Want: true,
	}, { // Test 1: the same binary with a different subcommand does not.
		Rule: StepRule{Binary: "tool", Subcommand: "serve"},
		Line: "tool build", Want: false,
	}, { // Test 2: a subcommand sharing a prefix is a different subcommand.
		Rule: StepRule{Binary: "tool", Subcommand: "serve"},
		Line: "tool serve-all", Want: false,
	}, { // Test 3: the tool's name in an argument is not an invocation, so a
		// rule about the tool does not reach a line that only names it.
		Rule: StepRule{Binary: "tool", Subcommand: "serve"},
		Line: "cat docs/tool serve.md", Want: false,
	}, { // Test 4: a binary rule with no subcommand selects any invocation.
		Rule: StepRule{Binary: "tool"}, Line: "tool anything", Want: true,
	}, { // Test 5: binary and match must both hold when both are given.
		Rule: StepRule{Binary: "tool", Match: "--port"},
		Line: "tool serve --port 8080", Want: true,
	}, { // Test 6: the same rule does not select without the match.
		Rule: StepRule{Binary: "tool", Match: "--port"},
		Line: "tool serve", Want: false,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := pl.ruleSelects(test.Rule, test.Line); got != test.Want {
				t.Errorf("ruleSelects(%+v, %q) = %v, want %v",
					test.Rule, test.Line, got, test.Want)
			}
		})
	}
}
