package shell

import (
	"fmt"
	"testing"
)

// TestFunctionDefinitionChangesTheShell pins that defining a function counts as
// changing the shell. A definition made inside a subshell is gone when it
// exits, so a line carrying one cannot be isolated for a timeout. hyperfine's
// README defines my_function and exports it on the next line, and the export
// reported that no such function existed: the document was right and the
// session had thrown the definition away.
func TestFunctionDefinitionChangesTheShell(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Name              string
		Line              string
		WantStateChanging bool
	}{{ // Test 0: the hyperfine case, a one line definition.
		Name: "inline definition", Line: "my_function() { sleep 1; }", WantStateChanging: true,
	}, { // Test 1: the function keyword form defines just the same.
		Name: "function keyword", Line: "function my_function { sleep 1; }", WantStateChanging: true,
	}, { // Test 2: cd still counts, so the existing rule is intact.
		Name: "cd", Line: "cd build", WantStateChanging: true,
	}, { // Test 3: an ordinary command changes nothing about the shell and must
		// stay isolatable, or every line loses its timeout.
		Name: "ordinary command", Line: "tool build --release", WantStateChanging: false,
	}, { // Test 4: a pipeline is structured but does not change the shell.
		Name: "pipeline", Line: "tool list | head -3", WantStateChanging: false,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			line, ok := Parse(test.Line)
			if !ok {
				t.Fatalf("did not parse: %q", test.Line)
			}
			if line.StateChanging != test.WantStateChanging {
				t.Errorf("StateChanging = %v, want %v for %q",
					line.StateChanging, test.WantStateChanging, test.Line)
			}
		})
	}
}
