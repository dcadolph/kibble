package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestFlagCheckUnreleased checks that a cited flag the installed release
// rejects but the source at HEAD defines is reported as unreleased, not as
// drift. yq's docs cite --ini-key-value-delimiters, added after v4.54.1, and
// nali's cite --color, and both were reported missing.
func TestFlagCheckUnreleased(t *testing.T) {
	t.Parallel()
	const yqRoot = `	rootCmd.PersistentFlags().StringVar(&yqlib.ConfiguredINIPreferences.KeyValueDelimiters, ` +
		`"ini-key-value-delimiters", yqlib.ConfiguredINIPreferences.KeyValueDelimiters, "sequence of delimiters")` + "\n"
	tests := []struct {
		Source     map[string]string
		Kind       string
		Flag       string
		Smoke      string
		WantStatus Status
		WantDetail string
	}{{ // Test 0: yq's flag, defined by cobra at HEAD and absent from the release.
		Source: map[string]string{"cmd/root.go": yqRoot}, Kind: "go-install",
		Flag: "ini-key-value-delimiters", Smoke: "yq (https://github.com/mikefarah/yq/) version v4.54.1",
		WantStatus: StatusVerified,
		WantDetail: "0 cited flags ok, 0 subcommands cited, 1 unreleased (--ini-key-value-delimiters), " +
			"defined in the source at HEAD but not in the installed " +
			"yq (https://github.com/mikefarah/yq/) version v4.54.1",
	}, { // Test 1: nali's flag, defined with a quoted name and no version line.
		Source: map[string]string{
			"cmd/root.go": `	rootCmd.Flags().String("color", "auto", "Colorize output")` + "\n",
		},
		Kind: "go-install", Flag: "color",
		WantStatus: StatusVerified,
		WantDetail: "0 cited flags ok, 0 subcommands cited, 1 unreleased (--color), " +
			"defined in the source at HEAD but not in the installed release",
	}, { // Test 2: an argparse definition names the flag with its dashes.
		Source: map[string]string{"cli.py": `parser.add_argument("--dry-run", action="store_true")` + "\n"},
		Kind:   "pipx-install", Flag: "dry-run",
		WantStatus: StatusVerified,
		WantDetail: "0 cited flags ok, 0 subcommands cited, 1 unreleased (--dry-run), " +
			"defined in the source at HEAD but not in the installed release",
	}, { // Test 3: a flag the source never defines is drift.
		Source: map[string]string{"cmd/root.go": yqRoot}, Kind: "go-install",
		Flag:       "gone",
		WantStatus: StatusDrift, WantDetail: "missing --gone",
	}, { // Test 4: the word in an unrelated string is not a definition.
		Source: map[string]string{"theme.go": `	palette := map[string]string{"color": "red"}` + "\n"},
		Kind:   "go-install", Flag: "color",
		WantStatus: StatusDrift, WantDetail: "missing --color",
	}, { // Test 4a: a longer flag that starts with the name is not it.
		Source: map[string]string{"cli.py": `parser.add_argument("--color-mode")` + "\n"},
		Kind:   "pipx-install", Flag: "color",
		WantStatus: StatusDrift, WantDetail: "missing --color",
	}, { // Test 5: a mention in the docs is not a definition.
		Source: map[string]string{"docs/usage.md": "Run `tool --color always`.\n"},
		Kind:   "go-install", Flag: "color",
		WantStatus: StatusDrift, WantDetail: "missing --color",
	}, { // Test 6: code2prompt is a Rust binary, and its website's JavaScript
		// defining a json option says nothing about the binary's flags.
		Source: map[string]string{
			"website/public/assets/js/app.js": `$.option("json", {type: "boolean"})` + "\n",
			"crates/code2prompt/src/args.rs":  `    #[arg(long = "output-file")]` + "\n",
		},
		Kind: "cargo-install", Flag: "json",
		WantStatus: StatusDrift, WantDetail: "missing --json",
	}, { // Test 7: an install kibble cannot tie to a language marks nothing unreleased.
		Source: map[string]string{"cmd/root.go": yqRoot}, Kind: "git-clone",
		Flag:       "ini-key-value-delimiters",
		WantStatus: StatusDrift, WantDetail: "missing --ini-key-value-delimiters",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, body := range test.Source {
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
					t.Fatal(err)
				}
				writeRepoFile(t, filepath.Dir(p), filepath.Base(p), body)
			}
			res := Result{
				Step: InstallStep{
					Repo: "tool", Kind: test.Kind, Binary: "tool", dir: dir,
					Usage: &Usage{Flags: []string{test.Flag}, FlagSub: map[string]string{test.Flag: ""}},
				},
				Status:     StatusVerified,
				SmokeLine:  test.Smoke,
				helpText:   "Usage: tool [flags]\n  -h, --help   help for tool\n",
				helpByFlag: map[string]string{test.Flag: "Error: unknown flag: --" + test.Flag},
			}
			got := flagChecks([]Result{res})
			if len(got) != 1 {
				t.Fatalf("got %d flag checks, want 1", len(got))
			}
			if diff := cmp.Diff(test.WantStatus, got[0].Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantDetail, got[0].Detail); diff != "" {
				t.Errorf("detail mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestFlagCheckUnreleasedSubcommand checks that a cited subcommand the release
// rejects but the source at HEAD declares is reported as unreleased, not as
// drift. sqlc's docs cite analyze and fmt, which its latest release lacks.
func TestFlagCheckUnreleasedSubcommand(t *testing.T) {
	t.Parallel()
	const sqlcCmd = "\t\tUse:   \"analyze [query-file]\",\n\t\tUse:   \"fmt\",\n\tUse:   \"generate\",\n"
	tests := []struct {
		Source     map[string]string
		Kind       string
		Subs       []string
		WantStatus Status
		WantDetail string
	}{{ // Test 0: sqlc's two cobra subcommands are unreleased.
		Source: map[string]string{"internal/cmd/cmd.go": sqlcCmd}, Kind: "go-install",
		Subs:       []string{"analyze", "fmt"},
		WantStatus: StatusVerified,
		WantDetail: "0 cited flags ok, 2 subcommands cited, 2 unreleased (analyze fmt), " +
			"defined in the source at HEAD but not in the installed release",
	}, { // Test 1: a subcommand neither help nor source knows still drifts.
		Source: map[string]string{"internal/cmd/cmd.go": sqlcCmd}, Kind: "go-install",
		Subs:       []string{"vet"},
		WantStatus: StatusDrift, WantDetail: "unknown subcommand vet",
	}, { // Test 2: urfave, clap, and argparse declarations count too.
		Source: map[string]string{
			"cmd/app.go":  "\t{Name: \"sync\", Usage: \"sync it\"},\n",
			"src/main.rs": "    .subcommand(Command::new(\"pack\"))\n    #[command(name = \"lint\")]\n",
			"cli.py":      "sub.add_parser('serve')\n",
		},
		Kind: "go-install", Subs: []string{"sync"},
		WantStatus: StatusVerified,
		WantDetail: "0 cited flags ok, 1 subcommands cited, 1 unreleased (sync), " +
			"defined in the source at HEAD but not in the installed release",
	}, { // Test 3: the name in a comment or a string is not a declaration.
		Source: map[string]string{"cmd/root.go": "\t// run analyze first\n\tfmt.Println(\"analyze\")\n"},
		Kind:   "go-install", Subs: []string{"analyze"},
		WantStatus: StatusDrift, WantDetail: "unknown subcommand analyze",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, body := range test.Source {
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
					t.Fatal(err)
				}
				writeRepoFile(t, filepath.Dir(p), filepath.Base(p), body)
			}
			help := "Usage: tool [command]\n  -h, --help   help for tool\n"
			for _, s := range test.Subs {
				help += fmt.Sprintf("Error: unknown command %q for \"tool\"\n", s)
			}
			res := Result{
				Step: InstallStep{
					Repo: "tool", Kind: test.Kind, Binary: "tool", dir: dir,
					Usage: &Usage{Subs: test.Subs},
				},
				Status:   StatusVerified,
				helpText: help,
			}
			got := flagChecks([]Result{res})
			if len(got) != 1 {
				t.Fatalf("got %d flag checks, want 1", len(got))
			}
			if diff := cmp.Diff(test.WantStatus, got[0].Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantDetail, got[0].Detail); diff != "" {
				t.Errorf("detail mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
