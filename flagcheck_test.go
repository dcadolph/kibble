package main

import (
	"fmt"
	"strings"
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

// TestFlagDash checks that a cited flag keeps the dash prefix it was written
// with, so a single-dash bundle is probed as written rather than as a long
// flag. Probing `-Poy` as `--Poy` once convicted a working bundle as drift.
func TestFlagDash(t *testing.T) {
	t.Parallel()
	use := extractUsage([]string{"yq"}, "```sh\nyq -Poy file.yml\nyq --output-format=json file.yml\n```\n")["yq"]
	if use == nil {
		t.Fatal("no usage extracted")
	}
	if got := use.dashOf("Poy"); got != "-" {
		t.Errorf("dashOf(Poy) = %q, want -", got)
	}
	if got := use.dashOf("output-format"); got != "--" {
		t.Errorf("dashOf(output-format) = %q, want --", got)
	}
	probe := helpProbe(InstallStep{Usage: use})
	if !strings.Contains(probe, `"$bin" -Poy --help`) {
		t.Errorf("probe does not run the bundle with a single dash:\n%s", probe)
	}
	if strings.Contains(probe, "--Poy") {
		t.Errorf("probe still runs the bundle as a long flag:\n%s", probe)
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
			Status:   StatusVerified,
			helpText: help,
		},
		WantStatus: StatusVerified,
	}, { // Test 1: a flag cited on the bare binary and missing from the help
		// is drift, since the root screen is the one that settles it.
		Result: Result{
			Step: InstallStep{Repo: "r", Binary: "tool", Usage: &Usage{
				Flags: []string{"nope"}, FlagSub: map[string]string{"nope": ""}}},
			Status:   StatusVerified,
			helpText: help,
		},
		WantStatus: StatusDrift, WantDetail: "missing --nope",
	}, { // Test 1a: a flag cited on a subcommand whose help never arrived is
		// unverified, not drift. Blaming the document for a screen the probe
		// failed to collect is how a checker starts crying wolf.
		Result: Result{
			Step: InstallStep{Repo: "r", Binary: "tool", Usage: &Usage{
				Flags: []string{"every"}, FlagSub: map[string]string{"every": "schedule add"}}},
			Status:   StatusVerified,
			helpText: help,
		},
		WantStatus: StatusVerified,
		WantDetail: "0 cited flags ok, 0 subcommands cited, 1 unverified (--every)",
	}, { // Test 1b: a flag a table lists without ever invoking it cannot be
		// attributed to a screen, so it is unverified too.
		Result: Result{
			Step: InstallStep{Repo: "r", Binary: "tool", Usage: &Usage{
				Flags: []string{"allow-orphan"}}},
			Status:   StatusVerified,
			helpText: help,
		},
		WantStatus: StatusVerified,
		WantDetail: "0 cited flags ok, 0 subcommands cited, 1 unverified (--allow-orphan)",
	}, { // Test 1c: a flag cited on a subcommand that did answer with usage
		// is settled by that screen, so a missing flag is drift.
		Result: Result{
			Step: InstallStep{Repo: "r", Binary: "tool", Usage: &Usage{
				Flags: []string{"gone"}, FlagSub: map[string]string{"gone": "walk"}}},
			Status:    StatusVerified,
			helpText:  help,
			subCodes:  map[string]int{"walk": 0},
			helpBySub: map[string]string{"walk": "Usage: tool walk\n  --keep  keep going\n"},
		},
		WantStatus: StatusDrift, WantDetail: "missing --gone",
	}, { // Test 2: a rejected subcommand is drift.
		Result: Result{
			Step:     InstallStep{Repo: "r", Binary: "tool", Usage: &Usage{Subs: []string{"sync"}}},
			Status:   StatusVerified,
			helpText: help,
		},
		WantStatus: StatusDrift, WantDetail: "unknown subcommand sync",
	}, { // Test 3: no help output means the check is skipped, not drifted.
		Result: Result{
			Step:   InstallStep{Repo: "r", Binary: "tool", Usage: &Usage{Flags: []string{"json"}}},
			Status: StatusVerified,
		},
		WantStatus: StatusSkipped,
	}, { // Test 4: a failed install produces no flag check at all.
		Result: Result{
			Step:   InstallStep{Repo: "r", Binary: "tool", Usage: &Usage{Flags: []string{"json"}}},
			Status: StatusFail,
		},
		WantNone: true,
	}, { // Test 5: a step with no citations produces no flag check.
		Result:   Result{Step: InstallStep{Repo: "r", Binary: "tool"}, Status: StatusVerified},
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

// TestRejectedSub checks how a cited subcommand is judged against its help
// probe. The exit code is the general signal, since a command framework
// rejects an unknown subcommand nonzero whatever wording it prints.
func TestRejectedSub(t *testing.T) {
	t.Parallel()
	tests := []struct {
		HelpText  string
		HelpBySub map[string]string
		SubCodes  map[string]int
		Sub       string
		Want      bool
	}{{ // Test 0: a subcommand the binary accepted is not drift.
		SubCodes: map[string]int{"scan": 0}, Sub: "scan", Want: false,
	}, { // Test 1: a parser naming this subcommand as unknown is a rejection.
		SubCodes: map[string]int{"bogus": 1}, Sub: "bogus", Want: true,
		HelpBySub: map[string]string{"bogus": `unknown subcommand "bogus"`},
	}, { // Test 1a: a nonzero probe on its own is not. A subcommand that takes
		// arguments rather than flags exits nonzero on --help while existing,
		// and its message names the argument, not itself.
		SubCodes: map[string]int{"schedule": 1}, Sub: "schedule", Want: false,
		HelpBySub: map[string]string{
			"schedule": `mytool: schedule: unknown subcommand "--help"; use add, list, or remove`},
	}, { // Test 1b: another shape of the same thing.
		SubCodes: map[string]int{"run": 1}, Sub: "run", Want: false,
		HelpBySub: map[string]string{"run": `mytool: run: unknown workflow: "--help"`},
	}, { // Test 2: a probe that timed out says nothing about the docs.
		SubCodes: map[string]int{"serve": 124}, Sub: "serve", Want: false,
	}, { // Test 3: a probe that found no binary says nothing about the docs.
		SubCodes: map[string]int{"scan": 127}, Sub: "scan", Want: false,
	}, { // Test 4: a binary that answers cleanly is caught by its wording.
		HelpText: `Error: unknown command "gone" for "tool"`,
		SubCodes: map[string]int{"gone": 0}, Sub: "gone", Want: true,
	}, { // Test 5: a subcommand the probe never reached is not judged.
		SubCodes: map[string]int{}, Sub: "scan", Want: false,
	}, { // Test 6: a nested subcommand is judged on its own probe, by name.
		SubCodes: map[string]int{"walk rotate": 2}, Sub: "walk rotate", Want: true,
		HelpBySub: map[string]string{"walk rotate": `Error: unknown command "rotate"`},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			r := Result{helpText: test.HelpText, subCodes: test.SubCodes,
				helpBySub: test.HelpBySub}
			if got := rejectedSub(r, test.Sub); got != test.Want {
				t.Errorf("rejectedSub(%q) = %v, want %v", test.Sub, got, test.Want)
			}
		})
	}
}

// TestHelpIsPartial checks that a help screen pointing at another page of
// itself is recognized, so a flag kept on that page is not called drift.
func TestHelpIsPartial(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Help string
		Want bool
	}{{ // Test 0: nodemon names a help page, so its screen is partial.
		Help: "For advanced configuration use nodemon.json: nodemon --help config",
		Want: true,
	}, { // Test 1: a sentence telling a reader to run --help for more is not
		// naming a page, so the screen still counts as whole.
		Help: "Run tool --help for more information.",
		Want: false,
	}, { // Test 2: a plain help screen names no further page.
		Help: "Usage: tool [options]\n  --json  emit JSON\n",
		Want: false,
	}, { // Test 3: a topic named with a hyphen still counts.
		Help: "See tool --help advanced-usage for the rest.",
		Want: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := helpIsPartial(test.Help); got != test.Want {
				t.Errorf("helpIsPartial = %v, want %v", got, test.Want)
			}
		})
	}
}

// TestSynopsisNotCited checks that a signature line contributes no flag
// citations: split on its pipes, a synopsis strands flags on the parent
// command, and that mis-attribution once probed a real flag in the wrong
// position and convicted it.
func TestSynopsisNotCited(t *testing.T) {
	t.Parallel()
	md := "```sh\ntool schedule [add <workflow> --every <dur> | list | remove <index>]\n" +
		"tool schedule add pto --every 168h\n```\n"
	got := extractUsage([]string{"tool"}, md)
	use := got["tool"]
	if use == nil {
		t.Fatal("no usage extracted")
	}
	if owner := use.FlagSub["every"]; owner != "schedule add" {
		t.Errorf("every attributed to %q, want %q from the real invocation", owner, "schedule add")
	}
}

// TestFlagRejectedPosition checks that a parser complaining about an unknown
// subcommand never convicts a flag, since the probe put it in that position.
func TestFlagRejectedPosition(t *testing.T) {
	t.Parallel()
	probe := `mytool: schedule: unknown subcommand "--every"; use add, list, or remove`
	if flagRejected(probe, "every") {
		t.Error("a subcommand-position complaint convicted the flag")
	}
	if !flagRejected(`unknown flag: --every`, "every") {
		t.Error("a real flag rejection was not recognized")
	}
}

// TestFlagProbeOutranksScreens checks that the binary's own answer to a
// probed flag beats what any help screen shows, in both directions.
func TestFlagProbeOutranksScreens(t *testing.T) {
	t.Parallel()
	help := "Usage of tool:\n  -json emit JSON\n"
	tests := []struct {
		Probe      string
		WantStatus Status
		WantDetail string
	}{{ // Test 0: a hidden but valid flag parses cleanly under the probe, so
		// a screen that omits it cannot turn it into an accusation.
		Probe:      "Usage of tool:\n  -json emit JSON\n",
		WantStatus: StatusVerified,
		WantDetail: "2 cited flags ok, 0 subcommands cited",
	}, { // Test 1: the parser refusing the flag by name convicts it.
		Probe:      `unknown flag: --debug`,
		WantStatus: StatusDrift,
		WantDetail: "missing --debug",
	}, { // Test 2: a probe that fails without naming the flag got past the
		// parser, which is acceptance where it counts: parsers reject unknown
		// flags first and by name, so a later error is the tool's own.
		Probe:      "tool: config not found",
		WantStatus: StatusVerified,
		WantDetail: "2 cited flags ok, 0 subcommands cited",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			r := Result{
				Step: InstallStep{Repo: "r", Binary: "tool", Usage: &Usage{
					Flags:   []string{"json", "debug"},
					FlagSub: map[string]string{"json": "", "debug": ""},
				}},
				Status:     StatusVerified,
				helpText:   help,
				helpByFlag: map[string]string{"debug": test.Probe},
			}
			got := flagChecks([]Result{r})
			if len(got) != 1 {
				t.Fatalf("got %d checks, want 1", len(got))
			}
			if got[0].Status != test.WantStatus {
				t.Errorf("status = %s, want %s (detail %q)",
					got[0].Status, test.WantStatus, got[0].Detail)
			}
			if got[0].Detail != test.WantDetail {
				t.Errorf("detail = %q, want %q", got[0].Detail, test.WantDetail)
			}
		})
	}
}
