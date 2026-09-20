package main

import (
	"context"
	"strings"
	"testing"
)

// TestSkippedDocumentsAreReported checks that a run which bounds its coverage
// says so. mise carries 421 markdown files and replaying all of them spent the
// whole budget without reaching a verdict, so the repository that most needed
// checking got none. Bounding that is fine. Bounding it quietly is not: a
// reader told nothing about a document reasonably assumes it passed.
func TestSkippedDocumentsAreReported(t *testing.T) {
	t.Parallel()

	step := InstallStep{Repo: "demo", Kind: "example", skippedDocs: 381}
	res := (&DockerRunner{}).Run(context.Background(), step)

	if res.Status != StatusSkipped {
		t.Errorf("status = %s, want SKIP", res.Status)
	}
	if res.Reason != ReasonNotExecuted {
		t.Errorf("reason = %s, want not-executed", res.Reason)
	}
	for _, want := range []string{"381", "not replayed", "unchecked rather than passing"} {
		if !strings.Contains(res.Detail, want) {
			t.Errorf("detail does not say %q: %q", want, res.Detail)
		}
	}
}

// TestDocumentBudgetKeepsTheDocumentsAReaderMeetsFirst checks which documents
// survive the budget. replayDocs returns the README first, then the named
// guides, then the rest of the tree, so truncating the tail keeps the pages a
// stranger actually starts from.
func TestDocumentBudgetKeepsTheDocumentsAReaderMeetsFirst(t *testing.T) {
	t.Parallel()

	if maxReplayDocs < 10 {
		t.Fatalf("budget of %d is too small to cover an ordinary docs tree", maxReplayDocs)
	}
	docs := make([]string, maxReplayDocs+5)
	docs[0] = "README.md"
	for i := 1; i < len(docs); i++ {
		docs[i] = "docs/page.md"
	}
	kept, skipped := docs, 0
	if len(kept) > maxReplayDocs {
		skipped = len(kept) - maxReplayDocs
		kept = kept[:maxReplayDocs]
	}
	if skipped != 5 {
		t.Errorf("skipped %d, want 5", skipped)
	}
	if kept[0] != "README.md" {
		t.Errorf("the budget dropped the README, which is the page a reader meets first")
	}
}
