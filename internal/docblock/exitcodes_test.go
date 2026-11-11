package docblock

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestNotFoundCodes checks that only a documented code meaning nothing was
// found is collected, from a prose run, a table, or a list under an exit code
// heading, and that a failure code or a code outside such a section is not.
func TestNotFoundCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Markdown  string
		WantCodes []int
	}{{ // Test 0: witr's prose run names 2 as not found and nothing else.
		Markdown: "## Process and port debugging\n\n" +
			"Exit codes: 0 found, 1 found with warnings (not a failure), 2 not found,\n" +
			"3 permission denied (retry with sudo), 4 ambiguous name or bad input\n" +
			"(re-run with --pid), 5 internal error, 6 found but what started it can't\n" +
			"be traced. Run `witr --help` for all options.\n",
		WantCodes: []int{2},
	}, { // Test 1: a table under an exit status heading.
		Markdown: "## Exit status\n\n| Code | Meaning |\n|---|---|\n| 0 | success |\n" +
			"| 1 | no matches |\n| 2 | error |\n",
		WantCodes: []int{1},
	}, { // Test 2: a list under an exit codes heading.
		Markdown:  "### Exit codes\n\n- `0`: ok\n- `3`: nothing found\n- `4`: bad input\n",
		WantCodes: []int{3},
	}, { // Test 3: a documented failure is never a not-found code.
		Markdown:  "## Exit codes\n\n- 2 permission denied\n",
		WantCodes: nil,
	}, { // Test 4: not found outside an exit code section is not a code.
		Markdown:  "## Usage\n\nIf the file is missing, 2 not found is printed.\n",
		WantCodes: nil,
	}, { // Test 5: the section ends at the next heading.
		Markdown:  "## Exit codes\n\n- 1 success\n\n## Errors\n\n- 2 not found\n",
		WantCodes: nil,
	}, { // Test 6: zero and shell codes are never collected.
		Markdown:  "## Exit codes\n\n- 0 not found\n- 127 not found\n",
		WantCodes: nil,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := NotFoundCodes(test.Markdown)
			if diff := cmp.Diff(test.WantCodes, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("codes mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
