package docblock

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestPrepareLinesContinuation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantLines []string
		In        []string
	}{{ // Test 0: A prompted command split across lines keeps its continuations.
		In:        []string{`$ rg somepattern \`, `    --colors 'match:none' \`, `    --colors 'match:fg:white'`},
		WantLines: []string{`rg somepattern \`, `--colors 'match:none' \`, `--colors 'match:fg:white'`},
	}, { // Test 1: Output after a finished command is still dropped.
		In:        []string{"$ echo hi", "hi", "$ echo bye"},
		WantLines: []string{"echo hi", "echo bye"},
	}, { // Test 2: A doubled backslash ends the command, so output stays dropped.
		In:        []string{`$ printf 'a\\`, "not a continuation"},
		WantLines: []string{`printf 'a\\`},
	}, { // Test 3: A block with no prompt is returned unchanged.
		In:        []string{"make build", "make test"},
		WantLines: []string{"make build", "make test"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := PrepareLines(test.In)
			if diff := cmp.Diff(test.WantLines, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestHasPlaceholder checks that a repository URL naming a stand-in owner is a
// placeholder and one naming a real owner is not.
func TestHasPlaceholder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		In   string
		Want bool
	}{{ // Test 0: The example owner is a stand-in.
		In: `lla install --git https://github.com/example/my-lla-plugins`, Want: true,
	}, { // Test 1: A your-org owner is a stand-in.
		In: `git clone https://gitlab.com/your-org/repo`, Want: true,
	}, { // Test 2: A real owner is not.
		In: `git clone https://github.com/dcadolph/kibble`, Want: false,
	}, { // Test 3: A synopsis slot with a space is a stand-in.
		In: `aqua update <command name> [<command name> ...]`, Want: true,
	}, { // Test 4: A redirect with spaces on both sides is not a slot.
		In: `tool < in > out`, Want: false,
	}, { // Test 5: A redirect with a space before the closing bracket is not a slot.
		In: `tool <in > out`, Want: false,
	}, { // Test 6: A single-word slot still is.
		In: `tool <in> out`, Want: true,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := HasPlaceholder(test.In); got != test.Want {
				t.Errorf("HasPlaceholder(%q) = %v, want %v", test.In, got, test.Want)
			}
		})
	}
}
