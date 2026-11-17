package plan

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestOtherProject checks that a block the paragraph above sends to another
// repository is skipped. trivy's compliance guide runs `make command-id` "on
// [trivy-checks project](...)", and the line failed in trivy's own checkout.
func TestOtherProject(t *testing.T) {
	t.Parallel()
	const checks = "Find the next command ID by running the command on " +
		"[trivy-checks project](https://github.com/aquasecurity/trivy-checks)."
	tests := []struct {
		Repo     string
		Lead     string
		WantSkip string
	}{{ // Test 0: run on another linked repository.
		Repo: "trivy", Lead: checks, WantSkip: "runs in trivy-checks, another repository the docs link to",
	}, { // Test 1: the same link from inside that repository is its own.
		Repo: "trivy-checks", Lead: checks,
	}, { // Test 2: a link with no run in its sentence.
		Repo: "trivy", Lead: "See [cobra](https://github.com/spf13/cobra) on flags.",
	}, { // Test 3: a link into this repository's pages names no other project.
		Repo: "trivy", Lead: "Run it from [the releases](https://github.com/aquasecurity/trivy/releases).",
	}, { // Test 4: run in this repository by name.
		Repo: "tool", Lead: "Run this in the [tool repository](https://github.com/acme/tool).",
	}, { // Test 5: a clone of another repository is not a scope.
		Repo: "trivy", Lead: "Clone [trivy-db](https://github.com/aquasecurity/trivy-db) and run:",
	}, { // Test 6: the link in an earlier sentence of the paragraph still scopes it.
		Repo: "trivy", Lead: checks + " It prints the next free ID.",
		WantSkip: "runs in trivy-checks, another repository the docs link to",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			md := "## Command ID\n\n" + test.Lead + "\n\n```sh\nmake command-id\n```\n"
			plan := BuildPlan(test.Repo, "", md, []string{"tool"},
				[]PlanInstall{{Cmd: "go install example.com/tool@latest", Ecosystem: "go"}}, nil, testRecognizer())
			got := ""
			for _, s := range plan.Steps {
				for _, l := range s.Lines {
					got = l.Skip
				}
			}
			if diff := cmp.Diff(test.WantSkip, got); diff != "" {
				t.Errorf("skip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
