package plan

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestMergeKeepsRoots checks that the checkout's top-level names survive the
// merge of each document's plan into the repository's session plan. Without
// them every missing name reads as the document's gap again.
func TestMergeKeepsRoots(t *testing.T) {
	t.Parallel()
	merged := &Plan{Repo: "trivy"}
	merged.Merge("README.md", &Plan{Roots: []string{"docs", "examples"}})
	merged.Merge("docs/guide.md", &Plan{Roots: []string{"docs", "examples"}})
	if diff := cmp.Diff([]string{"docs", "examples"}, merged.Roots); diff != "" {
		t.Errorf("roots mismatch (-want +got):\n%s", diff)
	}
}
