package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// fakeASTGrep answers the way ast-grep 0.51.0 does. Its run subcommand is the
// default, and only a line starting with --pattern reaches it, so a probe
// that puts --lang first sees a rejection the documented line never meets.
const fakeASTGrep = `#!/bin/sh
case "$*" in
"--help")
	printf 'Usage: ast-grep [OPTIONS] <COMMAND>\n\nCommands:\n'
	printf '  run          Run one time search or rewrite in command line. (default command)\n'
	printf '  scan         Scan and rewrite code by configuration\n'
	printf '  help         Print this message or the help of the given subcommand(s)\n\n'
	printf 'Options:\n  -c, --config <CONFIG_FILE>\n          Path to ast-grep root config\n'
	printf '      --custom-languages <CUSTOM_LANGUAGES>\n          Control native libraries (default: ignore)\n'
	exit 0 ;;
"run --help")
	printf 'Usage: ast-grep run [OPTIONS] --pattern <PATTERN> [PATHS]...\n\nOptions:\n'
	printf '  -p, --pattern <PATTERN>\n  -r, --rewrite <FIX>\n  -l, --lang <LANG>\n'
	exit 0 ;;
"--pattern --help")
	printf "error: a value is required for '--pattern <PATTERN>' but none was supplied\n"
	exit 2 ;;
--*" --help")
	printf "error: unexpected argument '%s' found\n\nUsage: ast-grep [OPTIONS] <COMMAND>\n" "$1"
	exit 2 ;;
esac
exit 2
`

// TestFlagCheckDefaultSubcommand checks that a flag belonging to the default
// subcommand is not drift. ast-grep's README:76 cites --lang and --rewrite on
// a line with no subcommand, and kibble reported both missing. The tail of a
// leading-flag line is reported, not probed, because a flag's value cannot be
// told from a subcommand.
func TestFlagCheckDefaultSubcommand(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	tests := []struct {
		Readme     string
		WantStatus Status
		WantDetail string
	}{{ // Test 0: ast-grep's documented line, flags on the default run.
		Readme:     "```\nast-grep --pattern 'var code = $PATTERN' --rewrite 'let code = new $PATTERN' --lang ts\n```\n",
		WantStatus: StatusVerified, WantDetail: "1 cited flags ok, 0 subcommands cited, 2 unverified (--lang --rewrite)",
	}, { // Test 1: a flag after the first positional is reported, not probed.
		Readme:     "```\nast-grep --pattern x --bogus\n```\n",
		WantStatus: StatusVerified,
		WantDetail: "1 cited flags ok, 0 subcommands cited, 1 unverified (--bogus)",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			bin := filepath.Join(t.TempDir(), "ast-grep")
			if err := os.WriteFile(bin, []byte(fakeASTGrep), 0o700); err != nil { //nolint:gosec // Test fixture must be executable.
				t.Fatal(err)
			}
			use := extractUsage([]string{"ast-grep"}, test.Readme)["ast-grep"]
			if use == nil {
				t.Fatal("no usage extracted")
			}
			step := InstallStep{Repo: "ast-grep", Binary: "ast-grep", Usage: use}
			script := "bin=" + bin + "\n" + helpProbe(step)
			out, err := exec.Command("sh", "-c", script).CombinedOutput()
			if err != nil {
				t.Fatalf("probe script: %v\n%s", err, out)
			}
			res := classify(step, string(out), 0)
			res.Status = StatusVerified
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
