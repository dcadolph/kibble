package main

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestShellWords pins the distinction the old whitespace split could not
// make. Each case is a line whose meaning depends on quoting, which is
// exactly where a verifier reading commands as space-separated tokens starts
// answering questions about a command the reader never wrote.
func TestShellWords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		In        string
		WantWords []string
		WantOK    bool
	}{{ // Test 0: a quoted argument is one word, not two.
		In: `tool --name "hello world"`,
		WantWords: []string{"tool", "--name", "hello world"}, WantOK: true,
	}, { // Test 1: the unquoted form really is two arguments, and the two
		// lines must not come back identical.
		In:        "tool --name hello world",
		WantWords: []string{"tool", "--name", "hello", "world"}, WantOK: true,
	}, { // Test 2: single quotes resolve the same way, including a line the
		// old quote counter called unbalanced because of the apostrophe.
		In: `echo "it's fine"`, WantWords: []string{"echo", "it's fine"}, WantOK: true,
	}, { // Test 3: an expansion keeps its source text rather than being
		// silently dropped or treated as a literal path.
		In:        `tool --output "$(dirname "$FILE")/foo.json"`,
		WantWords: []string{"tool", "--output", "$(dirname \"$FILE\")/foo.json"}, WantOK: true,
	}, { // Test 4: a genuinely unterminated quote does not parse, which is a
		// fact worth reporting rather than splitting on spaces anyway.
		In: `echo "a" "b`, WantWords: nil, WantOK: false,
	}, { // Test 5: an escaped space stays inside its word.
		In: `tool /tmp/some\ path/x.md`,
		WantWords: []string{"tool", "/tmp/some path/x.md"}, WantOK: true,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got, ok := shellWords(test.In)
			if ok != test.WantOK {
				t.Fatalf("ok = %v, want %v", ok, test.WantOK)
			}
			if diff := cmp.Diff(test.WantWords, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("words mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestParseShellShape pins the structural facts the executor needs: whether a
// line is more than one command, whether it changes the shell it runs in, and
// whether it carries a heredoc. Those three decide whether a line can be
// given its own timeout without changing what it means.
func TestParseShellShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		In            string
		WantStruct    bool
		WantState     bool
		WantHeredoc   bool
		WantFirstName string
	}{{ // Test 0: a plain invocation is one command that changes nothing.
		In: "tool run notes.md", WantFirstName: "tool",
	}, { // Test 1: a pipeline is structured but still changes nothing, so it
		// can be isolated. This is the case that had no timeout at all.
		In: "tool run | tee output.log", WantStruct: true, WantFirstName: "tool",
	}, { // Test 2: a cd changes the session and must stay in it, even though
		// it is part of a larger line.
		In: "cd build && make", WantStruct: true, WantState: true, WantFirstName: "cd",
	}, { // Test 3: a bare assignment is the shell's own state.
		In: "TOKEN=abc123", WantState: true,
	}, { // Test 4: an assignment prefixing a command is not: it applies to
		// that command only, so the line can still be isolated.
		In: "TOKEN=abc123 tool push", WantFirstName: "tool",
	}, { // Test 5: a heredoc is flagged so it is never rewritten.
		In:          "cat > script.sh <<'EOF'\nhello\nEOF",
		WantStruct:  true,
		WantHeredoc: true, WantFirstName: "cat",
	}, { // Test 6: an export anywhere in the line marks it state-changing.
		In: "tool env && export PATH=/x:$PATH", WantStruct: true, WantState: true,
		WantFirstName: "tool",
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			line, ok := parseShell(test.In)
			if !ok {
				t.Fatalf("parseShell(%q) did not parse", test.In)
			}
			if line.Structured != test.WantStruct {
				t.Errorf("structured = %v, want %v", line.Structured, test.WantStruct)
			}
			if line.StateChanging != test.WantState {
				t.Errorf("stateChanging = %v, want %v", line.StateChanging, test.WantState)
			}
			if line.Heredoc != test.WantHeredoc {
				t.Errorf("heredoc = %v, want %v", line.Heredoc, test.WantHeredoc)
			}
			name := ""
			if len(line.Cmds) > 0 {
				name = line.Cmds[0].Name()
			}
			if name != test.WantFirstName {
				t.Errorf("first command = %q, want %q", name, test.WantFirstName)
			}
		})
	}
}

// TestMatchesWords pins the escape hatch's word boundary. A rule written for
// one documented example used to select every other example sharing a prefix,
// because the comparison was a substring of the raw line.
func TestMatchesWords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Line  string
		Match string
		Want  bool
	}{{ // Test 0: the exact command selects.
		Line: "tool run notes.md", Match: "tool run", Want: true,
	}, { // Test 1: a longer subcommand sharing the prefix does not. This is
		// the accident the substring comparison made on every such pair.
		Line: "tool run-production --now", Match: "tool run", Want: false,
	}, { // Test 2: a flag after the match does not prevent selection.
		Line: "tool run --mode fast", Match: "tool run", Want: true,
	}, { // Test 3: the words must be consecutive.
		Line: "tool --verbose run", Match: "tool run", Want: false,
	}, { // Test 4: quoting is resolved on both sides, so a match written the
		// way the document writes it selects the same line.
		Line: `tool --name "hello world"`, Match: `--name "hello world"`, Want: true,
	}, { // Test 5: a match that is a single word still works.
		Line: "tool build && tool test", Match: "test", Want: true,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := matchesWords(test.Line, test.Match); got != test.Want {
				t.Errorf("matchesWords(%q, %q) = %v, want %v",
					test.Line, test.Match, got, test.Want)
			}
		})
	}
}

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
