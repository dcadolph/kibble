package main

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestExtractUsage checks that cited flags and subcommands are attributed to
// the right binary, and that other tools' flags are ignored.
func TestExtractUsage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In        string
		Binaries  []string
		WantFlags []string
		WantSubs  []string
		WantNone  bool
	}{{ // Test 0: flags on direct invocations, with prompt and =value forms.
		In:        "```sh\n$ tool fix --dialect=american notes.md\ntool check --json notes.md\n```\n",
		Binaries:  []string{"tool"},
		WantFlags: []string{"dialect", "json"},
		WantSubs:  []string{"fix", "check"},
	}, { // Test 1: a binary invoked mid-pipeline is still recognized.
		In:        "```sh\necho hi | tool fix --rewrite\n```\n",
		Binaries:  []string{"tool"},
		WantFlags: []string{"rewrite"},
		WantSubs:  []string{"fix"},
	}, { // Test 2: another tool's flags do not count against ours.
		In:       "```sh\ndocker run --rm img\ngit clone --depth 1 x\n```\n",
		Binaries: []string{"tool"},
		WantNone: true,
	}, { // Test 3: flags in prose-only inline code are not attributed.
		In:       "Pass `--manager you@work.com` to skip lookup.\n",
		Binaries: []string{"tool"},
		WantNone: true,
	}, { // Test 4: duplicate citations are deduplicated.
		In:        "```sh\ntool fix --json a.md\ntool fix --json b.md\n```\n",
		Binaries:  []string{"tool"},
		WantFlags: []string{"json"},
		WantSubs:  []string{"fix"},
	}, { // Test 5: a trailing comment does not contribute citations.
		In:        "```sh\ntool run --real   # or use --imaginary\n```\n",
		Binaries:  []string{"tool"},
		WantFlags: []string{"real"},
		WantSubs:  []string{"run"},
	}, { // Test 6: a flag on a nested invocation captures the two-token path.
		In:        "```sh\ntool walk rotate ./secrets --older-than 90d\n```\n",
		Binaries:  []string{"tool"},
		WantFlags: []string{"older-than"},
		WantSubs:  []string{"walk rotate"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := extractUsage(test.Binaries, test.In)
			if test.WantNone {
				if len(got) != 0 {
					t.Fatalf("want no usage, got %+v", got)
				}
				return
			}
			use := got[test.Binaries[0]]
			if use == nil {
				t.Fatalf("no usage extracted for %s", test.Binaries[0])
			}
			less := func(a, b string) bool { return a < b }
			if diff := cmp.Diff(test.WantFlags, use.Flags, cmpopts.SortSlices(less), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("flags mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantSubs, use.Subs, cmpopts.SortSlices(less), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("subs mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSubOrdering checks that a subcommand cited with a flag sorts before
// flagless ones, so the probe cap cannot cut the subcommand its flag lives on.
func TestSubOrdering(t *testing.T) {
	t.Parallel()
	md := "```sh\ntool alpha\ntool beta\ntool gamma\ntool delta --deep\ntool beta --bold\n```\n"
	use := extractUsage([]string{"tool"}, md)["tool"]
	if use == nil {
		t.Fatal("no usage extracted")
	}
	want := []string{"alpha", "beta", "gamma", "delta"}
	if diff := cmp.Diff(want, use.Subs, cmpopts.SortSlices(func(a, b string) bool { return a < b })); diff != "" {
		t.Fatalf("subs content mismatch (-want +got):\n%s", diff)
	}
	flaggedFirst := map[string]bool{"delta": true, "beta": true}
	if !flaggedFirst[use.Subs[0]] || !flaggedFirst[use.Subs[1]] {
		t.Errorf("flag-bearing subcommands must sort first, got order %v", use.Subs)
	}
}

// TestFlagChecks checks drift detection against collected help output.
func TestFlagChecks(t *testing.T) {
	t.Parallel()
	help := "Usage of tool:\n  -json emit JSON\n      --dialect string   pick one\nError: unknown command \"sync\" for \"tool\"\n"
	tests := []struct {
		Result     Result
		WantStatus Status
		WantDetail string
		WantNone   bool
	}{{ // Test 0: all cited flags exist, dash count normalized.
		Result: Result{
			Step:     InstallStep{Repo: "r", Binary: "tool", Usage: &Usage{Flags: []string{"json", "dialect"}}},
			Status:   StatusPass,
			helpText: help,
		},
		WantStatus: StatusPass,
	}, { // Test 1: a cited flag missing from every help screen is drift.
		Result: Result{
			Step:     InstallStep{Repo: "r", Binary: "tool", Usage: &Usage{Flags: []string{"nope"}}},
			Status:   StatusPass,
			helpText: help,
		},
		WantStatus: StatusDrift, WantDetail: "missing --nope",
	}, { // Test 2: a rejected subcommand is drift.
		Result: Result{
			Step:     InstallStep{Repo: "r", Binary: "tool", Usage: &Usage{Subs: []string{"sync"}}},
			Status:   StatusPass,
			helpText: help,
		},
		WantStatus: StatusDrift, WantDetail: "unknown subcommand sync",
	}, { // Test 3: no help output means the check is skipped, not drifted.
		Result: Result{
			Step:   InstallStep{Repo: "r", Binary: "tool", Usage: &Usage{Flags: []string{"json"}}},
			Status: StatusPass,
		},
		WantStatus: StatusSkipped,
	}, { // Test 4: a failed install produces no flag check at all.
		Result: Result{
			Step:   InstallStep{Repo: "r", Binary: "tool", Usage: &Usage{Flags: []string{"json"}}},
			Status: StatusFail,
		},
		WantNone: true,
	}, { // Test 5: a step with no citations produces no flag check.
		Result:   Result{Step: InstallStep{Repo: "r", Binary: "tool"}, Status: StatusPass},
		WantNone: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := flagChecks([]Result{test.Result})
			if test.WantNone {
				if len(got) != 0 {
					t.Fatalf("want no checks, got %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("want 1 check, got %d", len(got))
			}
			if diff := cmp.Diff(test.WantStatus, got[0].Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s", diff)
			}
			if test.WantDetail != "" {
				if diff := cmp.Diff(test.WantDetail, got[0].Detail); diff != "" {
					t.Errorf("detail mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}
