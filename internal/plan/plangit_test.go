package plan

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/kibble/internal/docblock"
)

// TestGitReadsRepo pins which git forms run in kibble's repository. Each
// skipped form reads state the session invented, and each running form either
// writes state or asks git about itself. The verdicts behind both lists were
// measured against an unborn HEAD in the session image.
func TestGitReadsRepo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Flat      string
		Staged    bool
		WantReads bool
	}{{ // Test 0: gum's picker line, which listed no branches and failed.
		Flat: "git branch | cut -c 3- | gum choose --no-limit | xargs git branch -D", WantReads: true,
	}, { // Test 1: listing branches reads them.
		Flat: "git branch", WantReads: true,
	}, { // Test 2: creating a branch without checkout needs a commit.
		Flat: "git branch feature", WantReads: true,
	}, { // Test 3: listing tags reads them.
		Flat: "git tag", WantReads: true,
	}, { // Test 4: tagging needs a commit.
		Flat: "git tag v1", WantReads: true,
	}, { // Test 5: the stash reads history.
		Flat: "git stash list", WantReads: true,
	}, { // Test 6: a global option with a value before the subcommand.
		Flat: "git -C dir log", WantReads: true,
	}, { // Test 7: a config override before the subcommand.
		Flat: "git -c a=b log", WantReads: true,
	}, { // Test 8: --git-dir written as two words.
		Flat: "git --git-dir .git status", WantReads: true,
	}, { // Test 9: diffing against history.
		Flat: "git diff HEAD~1", WantReads: true,
	}, { // Test 10: searching tracked files when nothing is tracked.
		Flat: "git grep x", WantReads: true,
	}, { // Test 11: status reports kibble's untracked tree.
		Flat: "git status", WantReads: true,
	}, { // Test 12: the log reads history.
		Flat: "git log", WantReads: true,
	}, { // Test 13: git behind sudo.
		Flat: "sudo git status", WantReads: true,
	}, { // Test 14: git behind env.
		Flat: "env K=V git status", WantReads: true,
	}, { // Test 15: git behind timeout.
		Flat: "timeout 5 git fetch", WantReads: true,
	}, { // Test 16: git behind xargs with a value option.
		Flat: "xargs -I {} git branch -D {}", WantReads: true,
	}, { // Test 17: a commit with nothing staged.
		Flat: "git commit -m x", WantReads: true,
	}, { // Test 18: -a stages tracked files, and nothing is tracked.
		Flat: "git commit -am x", WantReads: true,
	}, { // Test 19: amending reads the previous commit.
		Flat: "git commit --amend", Staged: true, WantReads: true,
	}, { // Test 20: -C reuses an existing commit's message.
		Flat: "git commit -C HEAD", Staged: true, WantReads: true,
	}, { // Test 21: a fixup names an existing commit.
		Flat: "git commit --fixup=abc", Staged: true, WantReads: true,
	}, { // Test 22: a start point names a commit that does not exist.
		Flat: "git checkout -b x v1.0", WantReads: true,
	}, { // Test 23: the same through switch.
		Flat: "git switch -c x main", WantReads: true,
	}, { // Test 24: checking out paths reads the index.
		Flat: "git checkout .", WantReads: true,
	}, { // Test 25: checking out a branch reads it.
		Flat: "git checkout main", WantReads: true,
	}, { // Test 26: reading config reports what kibble set.
		Flat: "git config --get user.name", WantReads: true,
	}, { // Test 27: a bare key is a read.
		Flat: "git config user.name", WantReads: true,
	}, { // Test 28: listing the container's empty global config.
		Flat: "git config --global --list", WantReads: true,
	}, { // Test 29: listing remotes.
		Flat: "git remote -v", WantReads: true,
	}, { // Test 30: adding with -f fetches from the reader's remote.
		Flat: "git remote add -f origin https://example.com/x.git", WantReads: true,
	}, { // Test 31: pushing needs a remote.
		Flat: "git push -u origin main", WantReads: true,
	}, { // Test 32: help with a topic needs man, which the image lacks.
		Flat: "git help log", WantReads: true,
	}, { // Test 33: renaming a named branch reads it.
		Flat: "git branch -m master main", WantReads: true,
	}, { // Test 34: a commit before the add on the same line has nothing staged.
		Flat: "git commit -m x && git add .", WantReads: true,
	}, { // Test 35: fetching needs a remote.
		Flat: "git fetch origin", WantReads: true,
	}, { // Test 36: an unknown verb is skipped until someone allows it.
		Flat: "git frobnicate", WantReads: true,
	}, { // Test 37: an add before the commit on the same line stages it.
		Flat: "git add . && git commit -m x", WantReads: false,
	}, { // Test 38: an earlier documented add stages it.
		Flat: "git commit -m x", Staged: true, WantReads: false,
	}, { // Test 39: -a after an add commits the staged files.
		Flat: "git commit -am x", Staged: true, WantReads: false,
	}, { // Test 40: an empty commit needs nothing staged.
		Flat: "git commit --allow-empty -m x", WantReads: false,
	}, { // Test 41: a message value is not read as an option.
		Flat: `git commit -m "-C is not an option here"`, Staged: true, WantReads: false,
	}, { // Test 42: init builds state.
		Flat: "git init", WantReads: false,
	}, { // Test 43: clone builds state.
		Flat: "git clone https://example.com/x.git", WantReads: false,
	}, { // Test 44: setting a config value.
		Flat: "git config user.name x", WantReads: false,
	}, { // Test 45: setting a global value with a quoted value.
		Flat: `git config --global user.name "Your Name"`, WantReads: false,
	}, { // Test 46: the newer config subcommand syntax.
		Flat: "git config set user.email a@b", WantReads: false,
	}, { // Test 47: asking git its version.
		Flat: "git --version", WantReads: false,
	}, { // Test 48: asking git where its docs live.
		Flat: "git --html-path", WantReads: false,
	}, { // Test 49: bare help prints usage.
		Flat: "git help", WantReads: false,
	}, { // Test 50: creating a branch at an unborn HEAD.
		Flat: "git checkout -b feature", WantReads: false,
	}, { // Test 51: the same through switch.
		Flat: "git switch -c feature", WantReads: false,
	}, { // Test 52: renaming the current branch, as the new-repo snippet does.
		Flat: "git branch -M main", WantReads: false,
	}, { // Test 53: recording a remote.
		Flat: "git remote add origin https://example.com/x.git", WantReads: false,
	}, { // Test 54: sudo -u consumes git as a user name.
		Flat: "sudo -u git ls", WantReads: false,
	}, { // Test 55: a mention is not an invocation.
		Flat: `echo "see git log"`, WantReads: false,
	}, { // Test 56: git inside a quoted string is not stepped into.
		Flat: `sh -c "git log"`, WantReads: false,
	}, { // Test 57: a ref named to another tool is that tool's business.
		Flat: "tool diff origin/main", WantReads: false,
	}, { // Test 58: excluding .git is not running git.
		Flat: "fd --exclude .git", WantReads: false,
	}, { // Test 59: a global option before an allowed commit.
		Flat: "git -c user.name=x commit -m y", Staged: true, WantReads: false,
	}, { // Test 60: asking where git is does not run it.
		Flat: "command -v git", WantReads: false,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := gitReadsRepo(test.Flat, test.Staged); got != test.WantReads {
				t.Errorf("gitReadsRepo(%q, %v) = %v, want %v", test.Flat, test.Staged, got, test.WantReads)
			}
		})
	}
}

// TestNamesRemoteRef checks that a remote-tracking ref handed to any tool is
// seen, and that a URL or a bare remote name is not mistaken for one.
func TestNamesRemoteRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Flat     string
		WantRefs bool
	}{{ // Test 0: a revision relative to a remote branch.
		Flat: "tool --base origin/main~4", WantRefs: true,
	}, { // Test 1: an attached option value.
		Flat: "tool --base=upstream/main", WantRefs: true,
	}, { // Test 2: a URL whose path contains a remote's name.
		Flat: "curl -fsSL https://github.com/upstream/repo", WantRefs: false,
	}, { // Test 3: a remote's name alone is not a ref.
		Flat: "echo origin", WantRefs: false,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := namesRemoteRef(test.Flat); got != test.WantRefs {
				t.Errorf("namesRemoteRef(%q) = %v, want %v", test.Flat, got, test.WantRefs)
			}
		})
	}
}

// TestGitStagedAcrossLines checks that a commit runs only after the same
// document stages something on a line that itself runs, so a skipped add
// does not unlock the commit after it.
func TestGitStagedAcrossLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Markdown string
		WantSkip []bool
	}{{ // Test 0: init, add, commit all run.
		Markdown: "```sh\ngit init\ngit add .\ngit commit -m first\n```\n",
		WantSkip: []bool{false, false, false},
	}, { // Test 1: a commit with no add before it skips.
		Markdown: "```sh\ngit init\ngit commit -m first\n```\n",
		WantSkip: []bool{false, true},
	}, { // Test 2: an add in an earlier block still counts.
		Markdown: "```sh\ngit add .\n```\n\nThen:\n\n```sh\ngit commit -m first\n```\n",
		WantSkip: []bool{false, false},
	}, { // Test 3: a skipped add does not stage anything.
		Markdown: "```sh\ngit add <file>\ngit commit -m first\n```\n",
		WantSkip: []bool{true, true},
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := BuildPlan("repo", "", test.Markdown, []string{"tool"},
				[]PlanInstall{{Cmd: "go install example.com/tool@latest", Ecosystem: "go"}}, nil, testRecognizer())
			var got []bool
			for _, s := range plan.Steps {
				for _, l := range s.Lines {
					if strings.HasPrefix(docblock.Flatten(l.Cmd), "git") {
						got = append(got, l.Skip != "")
					}
				}
			}
			if diff := cmp.Diff(test.WantSkip, got); diff != "" {
				t.Errorf("skip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestInvokedBinaryUnwraps checks that the documented binary is found behind
// the same wrappers the git rule steps through, so the two never disagree on
// what a line runs.
func TestInvokedBinaryUnwraps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Flat    string
		WantBin string
		WantSub string
	}{{ // Test 0: env with an assignment.
		Flat: "env K=V tool serve", WantBin: "tool", WantSub: "serve",
	}, { // Test 1: sudo.
		Flat: "sudo tool serve", WantBin: "tool", WantSub: "serve",
	}, { // Test 2: sudo -u consumes the tool's name as a user.
		Flat: "sudo -u tool ls", WantBin: "", WantSub: "",
	}, { // Test 3: xargs at the end of a pipeline.
		Flat: "ls | xargs tool check", WantBin: "tool", WantSub: "check",
	}, { // Test 4: an unwrapped invocation is unchanged.
		Flat: "tool run", WantBin: "tool", WantSub: "run",
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			bin, sub := InvokedBinary(test.Flat, map[string]bool{"tool": true})
			if diff := cmp.Diff([]string{test.WantBin, test.WantSub}, []string{bin, sub}); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
