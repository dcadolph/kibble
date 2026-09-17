package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestTableCollapsesOneCauseAcrossDocuments checks that a single problem
// affecting many documents is printed once with a count. mise returned 224 rows
// saying the same thing, which reads as a catastrophe rather than as the one
// problem it was. The counts underneath must not change: a reader who collapses
// rows still has to be told how many checks ran and how many failed.
func TestTableCollapsesOneCauseAcrossDocuments(t *testing.T) {
	t.Parallel()

	same := func(doc string) Result {
		return Result{
			Step:     InstallStep{Repo: "demo", Kind: "example", doc: doc},
			Status:   StatusError,
			Detail:   doc + ": repository too large to stream whole, so the examples have no verdict",
			Duration: time.Second,
		}
	}
	results := []Result{
		same("README.md"), same("docs/a.md"), same("docs/b.md"), same("docs/c.md"),
		{
			Step:   InstallStep{Repo: "demo", Kind: "go-install"},
			Status: StatusVerified, Duration: time.Second,
		},
	}

	var buf bytes.Buffer
	reportTable(&buf, results)
	out := buf.String()

	if got := strings.Count(out, "repository too large"); got != 1 {
		t.Errorf("the shared cause printed %d times, want 1:\n%s", got, out)
	}
	if !strings.Contains(out, "[4 documents]") {
		t.Errorf("collapsed row does not say how many documents it covers:\n%s", out)
	}
	// Five checks ran. Collapsing the display must not lose that.
	if !strings.Contains(out, "5 checks") {
		t.Errorf("summary lost the real check count:\n%s", out)
	}
	if !strings.Contains(out, "1 passed") {
		t.Errorf("summary lost the passing check:\n%s", out)
	}
}

// TestTableKeepsDistinctCausesApart checks that rows are only collapsed when
// they say the same thing. Two different problems in two documents are two
// findings and have to stay visible.
func TestTableKeepsDistinctCausesApart(t *testing.T) {
	t.Parallel()

	results := []Result{{
		Step:   InstallStep{Repo: "demo", Kind: "example", doc: "README.md"},
		Status: StatusFail, Detail: "README.md: a documented line exited 2",
	}, {
		Step:   InstallStep{Repo: "demo", Kind: "example", doc: "docs/guide.md"},
		Status: StatusFail, Detail: "docs/guide.md: a different line exited 1",
	}}

	var buf bytes.Buffer
	reportTable(&buf, results)
	out := buf.String()

	for _, want := range []string{"a documented line exited 2", "a different line exited 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("distinct finding was collapsed away, missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "documents]") {
		t.Errorf("distinct findings were counted as one cause:\n%s", out)
	}
}

// TestUnresolvableGitRefIsUnsettled checks the verdict when a documented VCS
// ref is not in the repository. pipx documents `git+...@branch` and
// `@abc123def` as illustrations a reader replaces, and also documents real
// refs. The text git returns is identical either way, and a short hexadecimal
// placeholder is shaped exactly like a real short commit, so the honest answer
// is that the run settled nothing rather than that the document is broken.
func TestUnresolvableGitRefIsUnsettled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Name       string
		Output     string
		WantStatus Status
	}{{ // Test 0: a placeholder naming its own kind.
		Name:       "word placeholder",
		Output:     "error: pathspec 'branch' did not match any file(s) known to git",
		WantStatus: StatusBlocked,
	}, { // Test 1: a stand-in commit, indistinguishable from a real short one.
		Name:       "fake short sha",
		Output:     "error: pathspec 'abc123def' did not match any file(s) known to git",
		WantStatus: StatusBlocked,
	}, { // Test 2: the same shape when the remote is asked directly.
		Name:       "remote ref missing",
		Output:     "fatal: could not find remote branch 'fix-something' to clone",
		WantStatus: StatusBlocked,
	}, { // Test 3: an unrelated failure is untouched, so this rule cannot be
		// used to excuse an ordinary break.
		Name:       "real failure",
		Output:     "error: unknown flag --nope",
		WantStatus: StatusFail,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			lr := classifyLineResult(lineResult{Cmd: "pipx install git+https://example.com/x.git@branch"},
				PlanLine{}, lineOutcome{code: 1, output: test.Output}, false, nil, lineTimeout)
			if lr.Status != test.WantStatus {
				t.Errorf("status = %s, want %s (detail %q)", lr.Status, test.WantStatus, lr.Detail)
			}
		})
	}
}
