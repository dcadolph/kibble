package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestHelpCommands checks that the public command list is read from help
// output across the shapes cobra, clap, and argparse print.
func TestHelpCommands(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Help string
		Want []string
	}{{ // Test 0: cobra lists commands under its own heading.
		Help: "Usage:\n  tool [command]\n\nAvailable Commands:\n" +
			"  attest      Produce a signed seal\n  connect     Attach a source\n\n" +
			"Flags:\n  -h, --help   help for tool\n",
		Want: []string{"attest", "connect"},
	}, { // Test 1: help, completion, and version are not commands a document
		// is expected to cover.
		Help: "Available Commands:\n  completion  Generate script\n  help        Help about any command\n" +
			"  version     Print the version\n  index       Build the index\n",
		Want: []string{"index"},
	}, { // Test 2: clap and argparse use a bare Commands heading.
		Help: "Commands:\n  build    Build it\n  ship     Ship it\n\nOptions:\n  -h\n",
		Want: []string{"build", "ship"},
	}, { // Test 3: grouped help such as docker's lists commands under several
		// headings, and every group is part of the public surface. Keeping a
		// subcommand's own screen out of the root surface is the root-screen
		// segmentation's job, not this parser's.
		Help: "Management Commands:\n  builder  Manage builds\n\n" +
			"Commands:\n  attach   Attach local streams\n  run      Run a command\n",
		Want: []string{"builder", "attach", "run"},
	}, { // Test 4: a binary with no command list yields nothing.
		Help: "Usage: tool [options]\n\nOptions:\n  -h, --help\n",
		Want: nil,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := helpCommands(test.Help)
			if diff := cmp.Diff(test.Want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestMentions checks that a command counts as documented only when the text
// ties it to its binary or heads a reference row with it.
func TestMentions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Text string
		Cmd  string
		Want bool
	}{{ // Test 0: an invocation names the binary and the command together.
		Text: "Run `tool attest --out seal.json` to seal it.", Cmd: "attest", Want: true,
	}, { // Test 1: a reference table heads its row with the command.
		Text: "| `attest` | Produce a signed seal |", Cmd: "attest", Want: true,
	}, { // Test 2: a heading counts.
		Text: "### attest\n\nProduces a seal.", Cmd: "attest", Want: true,
	}, { // Test 3: prose using the word without the binary does not count,
		// so a command named list is not covered by unrelated sentences.
		Text: "The results list is sorted by score.", Cmd: "list", Want: false,
	}, { // Test 4: absent entirely.
		Text: "Nothing about it here.", Cmd: "attest", Want: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := mentions(test.Text, "tool", test.Cmd); got != test.Want {
				t.Errorf("mentions(%q) = %v, want %v", test.Cmd, got, test.Want)
			}
		})
	}
}

// TestDocCoverageChecks checks the two tiers: a command documented nowhere is
// a gap, and one documented only outside the README is reported without
// failing, since which commands earn README space is the author's call.
func TestDocCoverageChecks(t *testing.T) {
	t.Parallel()
	help := "Available Commands:\n  attest   Seal it\n  index    Build it\n"
	tests := []struct {
		Readme     string
		Reference  string
		WantStatus Status
		WantDetail string
	}{{ // Test 0: both commands in the README.
		Readme:     "Run `tool attest` then `tool index`.",
		WantStatus: StatusPass,
		WantDetail: "2 commands documented",
	}, { // Test 1: one missing from every document is a gap.
		Readme:     "Run `tool index`.",
		WantStatus: StatusGap,
		WantDetail: "1 of 2 commands documented nowhere: attest",
	}, { // Test 2: a command covered only by a reference file is documented,
		// and the README omission is reported without failing.
		Readme:     "Run `tool index`.",
		Reference:  "### attest\n\nSeals a finding.",
		WantStatus: StatusPass,
		WantDetail: "2 commands documented, 1 only outside the README (attest)",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "README.md"),
				[]byte(test.Readme), 0o600); err != nil {
				t.Fatalf("write README: %v", err)
			}
			if test.Reference != "" {
				if err := os.WriteFile(filepath.Join(dir, "REFERENCE.md"),
					[]byte(test.Reference), 0o600); err != nil {
					t.Fatalf("write REFERENCE: %v", err)
				}
			}
			in := []Result{{
				Status:   StatusPass,
				helpText: help,
				Step: InstallStep{Repo: "repo", Kind: "go-install", Binary: "tool",
					dir: dir, readme: "README.md"},
			}}
			got := docCoverageChecks(in)
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
