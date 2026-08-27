package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// TestClassify checks that container output maps to the right status. A build
// timeout must not be reported as a failure.
func TestClassify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In         string
		WantStatus Status
		WantSmoke  string
		WantDetail string
	}{{ // Test 0: built and smoke test passed.
		In:         "BUILDCODE=0\nSMOKECODE=0\nSMOKELINE=tool version 1.0\n",
		WantStatus: StatusPass, WantSmoke: "tool version 1.0",
	}, { // Test 1: built but the binary did not respond cleanly.
		In:         "BUILDCODE=0\nSMOKECODE=2\nSMOKELINE=Usage of tool:\n",
		WantStatus: StatusPassBuild, WantSmoke: "Usage of tool:",
	}, { // Test 2: build exceeded the timeout, so the result is unknown.
		In:         "BUILDCODE=124\ngo: downloading github.com/pkg/errors v0.9.1\n",
		WantStatus: StatusTimeout,
	}, { // Test 3: build failed with a compile error.
		In:         "BUILDCODE=1\npkg/foo/bar.go:10: undefined: Baz\n",
		WantStatus: StatusFail, WantDetail: "undefined: Baz",
	}, { // Test 4: no marker at all is kibble's own error, not a doc failure.
		In:         "docker: Error response from daemon: pull access denied\n",
		WantStatus: StatusError, WantDetail: "kibble could not run the step",
	}, { // Test 5: a recipe that runs but produces no binary still passes.
		In:         "BUILDCODE=0\nNOBIN=1\n",
		WantStatus: StatusPass, WantDetail: "no binary produced",
	}}
	step := InstallStep{Repo: "repo", Kind: "go-install", Binary: "tool"}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := classify(step, test.In, 5*time.Second)
			if diff := cmp.Diff(test.WantStatus, got.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantSmoke, got.SmokeLine); diff != "" {
				t.Errorf("smoke mismatch (-want +got):\n%s", diff)
			}
			if test.WantDetail != "" && !strings.Contains(got.Detail, test.WantDetail) {
				t.Errorf("detail %q does not contain %q", got.Detail, test.WantDetail)
			}
		})
	}
}

// TestClassifySubcommandCodes checks that each cited subcommand's help probe
// reports its own exit code, and that the markers do not leak into the help
// text the flag check reads.
func TestClassifySubcommandCodes(t *testing.T) {
	t.Parallel()
	out := "BUILDCODE=0\nSMOKECODE=0\nSMOKELINE=tool 1.0\n" +
		"KIBBLE-HELP-START\nUsage: tool [flags]\n  --json  emit json\n" +
		"KIBBLE-SUB scan CODE=0\n" +
		"KIBBLE-SUB walk rotate CODE=1\n" +
		"KIBBLE-HELP-END\n"
	got := classify(InstallStep{Repo: "repo", Kind: "go-install", Binary: "tool"}, out, 0)
	want := map[string]int{"scan": 0, "walk rotate": 1}
	if diff := cmp.Diff(want, got.subCodes); diff != "" {
		t.Errorf("subCodes mismatch (-want +got):\n%s", diff)
	}
	if strings.Contains(got.helpText, "KIBBLE-SUB") {
		t.Errorf("help text carries a probe marker: %q", got.helpText)
	}
	if !strings.Contains(got.helpText, "--json") {
		t.Errorf("help text lost its flags: %q", got.helpText)
	}
}

// TestInstallScriptFindsBinary checks that the Go install script falls back to
// whatever landed in GOBIN and prints NOBIN when nothing did, so a wrong guess
// at the binary name is never reported as the documented install failing.
func TestInstallScriptFindsBinary(t *testing.T) {
	t.Parallel()
	script := fmt.Sprintf(installScript, 60, "example.com/x/cmd/...@latest", "...")
	for _, want := range []string{
		`bin="$GOBIN/..."`,
		`b=$(ls "$GOBIN" 2>/dev/null | head -n1)`,
		"printf 'NOBIN=1\\n'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
		}
	}
	res := classify(InstallStep{Kind: "go-install", Binary: "..."}, "BUILDCODE=0\nNOBIN=1\n", 0)
	if res.Status != StatusPass {
		t.Errorf("status = %s, want %s", res.Status, StatusPass)
	}
}

// TestDockerExampleSession replays a plan in a real container and checks that
// every documented line reports its own result, including one whose output
// ends without a newline. It is the end-to-end guard on the marker protocol,
// which no amount of parsing tests can cover on its own.
func TestDockerExampleSession(t *testing.T) {
	if testing.Short() {
		t.Skip("container test skipped in short mode")
	}
	if os.Getenv("KIBBLE_INTEGRATION") == "" {
		t.Skip("set KIBBLE_INTEGRATION=1 to run container tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := DockerAvailable(ctx); err != nil {
		t.Skipf("docker is not reachable: %v", err)
	}
	plan := &Plan{
		Repo:     "repo",
		Installs: []PlanInstall{{Cmd: "true"}},
		Steps: []PlanStep{{ID: "b1", Lines: []PlanLine{
			{Cmd: "printf no-trailing-newline"},
			{Cmd: "echo second"},
			{Cmd: "sh -c 'printf glued; exit 3'"},
			{Cmd: "echo fourth"},
			{Cmd: "echo fifth", Skip: "planned skip"},
		}}},
	}
	runner := &DockerRunner{Image: "debian:stable-slim", Timeout: 60 * time.Second}
	step := InstallStep{Repo: "repo", Kind: "example", Run: true, plan: plan, dir: t.TempDir()}
	res := runner.runExample(ctx, step)
	if res.example == nil {
		t.Fatalf("no per-line outcomes, status %s: %s", res.Status, res.Detail)
	}
	var got []Status
	for _, l := range res.example.Steps[0].Lines {
		got = append(got, l.Status)
	}
	want := []Status{StatusPass, StatusPass, StatusFail, StatusPass, StatusSkipped}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("line statuses mismatch (-want +got):\n%s\ndetail: %s", diff, res.Detail)
	}
	if res.Status != StatusFail {
		t.Errorf("status = %s, want %s (detail %q)", res.Status, StatusFail, res.Detail)
	}
}

// TestRewriteSSH checks that GitHub SSH remotes become HTTPS for keyless clones.
func TestRewriteSSH(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want string
	}{{ // Test 0: an ssh remote with .git suffix.
		In:   "git clone git@github.com:dcadolph/slop-chop.git",
		Want: "git clone https://github.com/dcadolph/slop-chop.git",
	}, { // Test 1: an ssh remote without the suffix.
		In:   "git clone git@github.com:dcadolph/midden",
		Want: "git clone https://github.com/dcadolph/midden.git",
	}, { // Test 2: an https remote is untouched.
		In:   "git clone https://github.com/dcadolph/cipher && cd cipher",
		Want: "git clone https://github.com/dcadolph/cipher && cd cipher",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, rewriteSSH(test.In)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestTruncate checks the ellipsis helper used by the table report.
func TestTruncate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		N    int
		Want string
	}{{ // Test 0: shorter than the limit is unchanged.
		In: "short", N: 10, Want: "short",
	}, { // Test 1: longer than the limit is cut with an ellipsis.
		In: "abcdefghij", N: 5, Want: "abcd…",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, truncate(test.In, test.N)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
