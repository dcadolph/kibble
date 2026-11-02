package plan

import (
	"fmt"
	"github.com/dcadolph/kibble/internal/config"
	"github.com/dcadolph/kibble/internal/docblock"
	"github.com/dcadolph/kibble/internal/verdict"
	"testing"
)

// TestRuleSelects checks the structured selectors against what a line
// actually invokes, rather than against its text. A rule naming a tool must
// not fire on a line that merely mentions the tool's name.
func TestRuleSelects(t *testing.T) {
	t.Parallel()

	pl := &planner{binaries: map[string]bool{"tool": true}}
	tests := []struct {
		Rule config.StepRule
		Line string
		Want bool
	}{{ // Test 0: binary and subcommand both match what is invoked.
		Rule: config.StepRule{Binary: "tool", Subcommand: "serve"},
		Line: "tool serve --port 8080", Want: true,
	}, { // Test 1: the same binary with a different subcommand does not.
		Rule: config.StepRule{Binary: "tool", Subcommand: "serve"},
		Line: "tool build", Want: false,
	}, { // Test 2: a subcommand sharing a prefix is a different subcommand.
		Rule: config.StepRule{Binary: "tool", Subcommand: "serve"},
		Line: "tool serve-all", Want: false,
	}, { // Test 3: the tool's name in an argument is not an invocation, so a
		// rule about the tool does not reach a line that only names it.
		Rule: config.StepRule{Binary: "tool", Subcommand: "serve"},
		Line: "cat docs/tool serve.md", Want: false,
	}, { // Test 4: a binary rule with no subcommand selects any invocation.
		Rule: config.StepRule{Binary: "tool"}, Line: "tool anything", Want: true,
	}, { // Test 5: binary and match must both hold when both are given.
		Rule: config.StepRule{Binary: "tool", Match: "--port"},
		Line: "tool serve --port 8080", Want: true,
	}, { // Test 6: the same rule does not select without the match.
		Rule: config.StepRule{Binary: "tool", Match: "--port"},
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

// TestSkipReasonReadsPathCommands checks that a reader-named operand of a
// command that reads paths skips as a placeholder when nothing provides it,
// and that provided names and unrelated commands run.
func TestSkipReasonReadsPathCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Tree       map[string]bool
		Created    map[string]bool
		Line       string
		WantSkip   string
		WantReason verdict.Reason
	}{{ // Test 0: tree of a stand-in directory is a placeholder.
		Line:       `tree my-dotfiles`,
		WantSkip:   `docs use "my-dotfiles" as a placeholder the reader must fill in`,
		WantReason: verdict.ReasonPlaceholder,
	}, { // Test 1: cd into a directory an earlier line made runs.
		Created: map[string]bool{"my-app": true}, Line: `cd my-app`,
	}, { // Test 2: cat of a name the tree provides runs.
		Tree: map[string]bool{"my-notes": true}, Line: `cat my-notes`,
	}, { // Test 3: a commit message is not a path, so only the git rule fires.
		Line:       `git commit -m my-message`,
		WantSkip:   "reads the git repository, which in the session is one kibble made rather than the reader's",
		WantReason: verdict.ReasonMissingFixture,
	}, { // Test 4: tree of an ordinary directory runs.
		Line: `tree docs`,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			tree := test.Tree
			if tree == nil {
				tree = map[string]bool{}
			}
			pl := newReaderNameTestPlanner(tree)
			for k := range test.Created {
				pl.created[k] = true
			}
			skip, reason, _ := pl.skipReason(test.Line, docblock.Flatten(test.Line))
			if skip != test.WantSkip || reason != test.WantReason {
				t.Errorf("skipReason = (%q, %q), want (%q, %q)",
					skip, reason, test.WantSkip, test.WantReason)
			}
		})
	}
}
