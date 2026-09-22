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
