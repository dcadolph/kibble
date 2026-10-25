package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	kplan "github.com/dcadolph/kibble/internal/plan"
)

// TestAnyFail checks the exit-code logic, including strict mode.
func TestAnyFail(t *testing.T) {
	t.Parallel()
	res := func(s Status) Result { return Result{Status: s} }
	tests := []struct {
		Results []Result
		Strict  bool
		Want    bool
	}{{ // Test 0: all pass, not strict.
		Results: []Result{res(StatusVerified), res(StatusSkipped)}, Want: false,
	}, { // Test 1: a build failure always fails.
		Results: []Result{res(StatusVerified), res(StatusFail)}, Want: true,
	}, { // Test 2: a timeout does not fail by default.
		Results: []Result{res(StatusTimeout)}, Strict: false, Want: false,
	}, { // Test 3: a timeout fails under strict.
		Results: []Result{res(StatusTimeout)}, Strict: true, Want: true,
	}, { // Test 4: a smoke failure fails under strict.
		Results: []Result{res(StatusBuilt)}, Strict: true, Want: true,
	}, { // Test 5: doc drift does not fail by default.
		Results: []Result{res(StatusDrift)}, Strict: false, Want: false,
	}, { // Test 6: doc drift fails under strict.
		Results: []Result{res(StatusDrift)}, Strict: true, Want: true,
	}, { // Test 7: a documentation gap reports but does not fail by default,
		// since a document may expect the reader to bring their own file.
		Results: []Result{res(StatusGap)}, Strict: false, Want: false,
	}, { // Test 8: a documentation gap fails under strict.
		Results: []Result{res(StatusGap)}, Strict: true, Want: true,
	}, { // Test 9: a gap alongside a real failure still fails.
		Results: []Result{res(StatusGap), res(StatusFail)}, Strict: false, Want: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := anyFail(test.Results, test.Strict); got != test.Want {
				t.Errorf("want %v got %v", test.Want, got)
			}
		})
	}
}

// TestExitCode checks the process exit status, including the code that says a
// run checked nothing because no repository had an install route kibble runs.
func TestExitCode(t *testing.T) {
	t.Parallel()
	res := func(s Status) Result { return Result{Status: s} }
	noRoute := Result{Step: InstallStep{Kind: "install"}, Status: StatusSkipped, Reason: ReasonNoInstallRoute}
	exampleSkip := Result{Step: InstallStep{Kind: "example"}, Status: StatusSkipped}
	aptSkip := Result{Step: InstallStep{Kind: "os-package"}, Status: StatusSkipped, Reason: ReasonUnsupportedMethod}
	tests := []struct {
		Results  []Result
		Strict   bool
		WantCode int
	}{{ // Test 0: a pass exits zero.
		Results: []Result{res(StatusVerified), res(StatusSkipped)}, WantCode: 0,
	}, { // Test 1: a failure exits one.
		Results: []Result{res(StatusVerified), res(StatusFail)}, WantCode: 1,
	}, { // Test 2: strict promotes drift to a failure.
		Results: []Result{res(StatusDrift)}, Strict: true, WantCode: 1,
	}, { // Test 3: a lone no-install-route row means nothing was checked.
		Results: []Result{noRoute}, WantCode: 3,
	}, { // Test 4: jq's shape, the row beside the example skip it causes.
		Results: []Result{noRoute, exampleSkip}, WantCode: 3,
	}, { // Test 5: another repository that verified means something was checked.
		Results: []Result{noRoute, res(StatusVerified)}, WantCode: 0,
	}, { // Test 6: a failure elsewhere outranks nothing checked.
		Results: []Result{noRoute, res(StatusFail)}, WantCode: 1,
	}, { // Test 7: buku's shape, an apt route kibble does not run and the example
		// skip it causes, is nothing checked too.
		Results: []Result{aptSkip, exampleSkip}, WantCode: 3,
	}, { // Test 8: an error elsewhere outranks nothing checked.
		Results: []Result{noRoute, res(StatusError)}, WantCode: 1,
	}, { // Test 9: a formula lookup is a check, so a brew row beside skips passes.
		Results: []Result{res(StatusExists), exampleSkip}, WantCode: 0,
	}, { // Test 10: strict does not turn nothing checked into a failure.
		Results: []Result{noRoute, exampleSkip}, Strict: true, WantCode: 3,
	}, { // Test 11: an empty run exits zero.
		WantCode: 0,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := exitCode(test.Results, test.Strict); got != test.WantCode {
				t.Errorf("want %d got %d", test.WantCode, got)
			}
		})
	}
}

// TestCollectNoInstallRoute checks that a repository with no install step
// kibble runs reaches the report as a row naming what was read, instead of
// leaving the run silent and green.
func TestCollectNoInstallRoute(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Files      map[string]string
		WantRow    bool
		WantDetail string
	}{{ // Test 0: jq's shape, a README documenting no install kibble extracts.
		Files:      map[string]string{"README.md": "# r\n\nDownload a release.\n"},
		WantRow:    true,
		WantDetail: "no install route kibble runs in README.md and 0 install documents (routes seen: none)",
	}, { // Test 1: an install of another package is named as a route seen, and the
		// install document that held nothing is counted.
		Files: map[string]string{
			"go.mod":     "module example.com/tool\n",
			"README.md":  "# r\n\n```sh\npip install black\n```\n",
			"INSTALL.md": "# Install\n\nDownload a release.\n",
		},
		WantRow: true,
		WantDetail: "no install route kibble runs in README.md and 1 install document " +
			"(routes seen: pip-install)",
	}, { // Test 2: a documented install of the tool itself produces no row.
		Files: map[string]string{"README.md": "# r\n\n```sh\ngo install example.com/tool@latest\n```\n"},
	}, { // Test 3: a brew formula is looked up on its own row, so no row here.
		Files: map[string]string{"README.md": "# r\n\n```sh\nbrew install x\n```\n"},
	}, { // Test 4: an apt route is reported on its own row as not run, so no row here.
		Files: map[string]string{"README.md": "# r\n\n```sh\nsudo apt-get install x\n```\n"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, body := range test.Files {
				writeRepoFile(t, dir, name, body)
			}
			_, _, problems := collect([]string{dir}, true)
			var rows []Result
			for _, p := range problems {
				if p.Reason == ReasonNoInstallRoute {
					rows = append(rows, p)
				}
			}
			if !test.WantRow {
				if len(rows) != 0 {
					t.Fatalf("want no row, got %+v", rows)
				}
				return
			}
			if len(rows) != 1 {
				t.Fatalf("want one no-install-route row, got %+v", problems)
			}
			r := rows[0]
			if r.Status != StatusSkipped || r.Step.Kind != "install" || r.Step.readme != "README.md" {
				t.Errorf("row = %+v, want an install SKIP naming README.md", r)
			}
			if diff := cmp.Diff(test.WantDetail, r.Detail); diff != "" {
				t.Errorf("detail mismatch (-want +got):\n%s", diff)
			}
			if anyFail(problems, false) {
				t.Errorf("the row alone must not fail the run")
			}
		})
	}
}

// TestHasRunnable checks detection of executable steps.
func TestHasRunnable(t *testing.T) {
	t.Parallel()
	if hasRunnable([]InstallStep{{Run: false}}) {
		t.Errorf("want false when no step is runnable")
	}
	if !hasRunnable([]InstallStep{{Run: false}, {Run: true}}) {
		t.Errorf("want true when a runnable step exists")
	}
	noInstall := &kplan.Plan{Steps: []kplan.PlanStep{{}}}
	if hasRunnable([]InstallStep{{Kind: "example", Run: true, plan: noInstall}}) {
		t.Errorf("want false when the only step is an example with no install to run first")
	}
}

// writeRepoFile writes one file into a test repository directory.
func writeRepoFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestCollectProblems checks that a repository kibble cannot read produces a
// result rather than silence. A path with no README and a malformed config
// both used to leave the run green with nothing verified.
func TestCollectProblems(t *testing.T) {
	t.Parallel()
	const readme = "# repo\n\n```sh\ngo install example.com/tool@latest\n```\n"

	good := t.TempDir()
	writeRepoFile(t, good, "README.md", readme)

	lower := t.TempDir()
	writeRepoFile(t, lower, "readme.md", readme)

	badCfg := t.TempDir()
	writeRepoFile(t, badCfg, "README.md", readme)
	writeRepoFile(t, badCfg, ".kibble.yml", "version: 1\nexamples:\n  steps: [ unbalanced\n")

	tests := []struct {
		Dir            string
		WantStatuses   []Status
		WantDetail     string
		WantReadmeName string
	}{{ // Test 0: a readable repo reports no problem.
		Dir: good, WantReadmeName: "README.md",
	}, { // Test 1: a lowercase readme is found and named for annotations.
		Dir: lower, WantReadmeName: "readme.md",
	}, { // Test 2: a directory with no README is an error, not silence.
		Dir: t.TempDir(), WantStatuses: []Status{StatusError}, WantDetail: "read nothing",
	}, { // Test 3: a malformed config is an error, not a dropped layer.
		Dir:          badCfg,
		WantStatuses: []Status{StatusError}, WantDetail: "bad .kibble.yml",
		WantReadmeName: "README.md",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			steps, _, problems := collect([]string{test.Dir}, true)
			var got []Status
			for _, p := range problems {
				got = append(got, p.Status)
			}
			if diff := cmp.Diff(test.WantStatuses, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("problem statuses mismatch (-want +got):\n%s", diff)
			}
			if test.WantDetail != "" && !strings.Contains(problems[0].Detail, test.WantDetail) {
				t.Errorf("detail %q does not contain %q", problems[0].Detail, test.WantDetail)
			}
			if anyFail(problems, false) != (len(test.WantStatuses) > 0) {
				t.Errorf("anyFail = %v, want %v", anyFail(problems, false), len(test.WantStatuses) > 0)
			}
			if test.WantReadmeName == "" {
				return
			}
			if len(steps) == 0 {
				t.Fatalf("no steps extracted")
			}
			if steps[0].readme != test.WantReadmeName {
				t.Errorf("readme = %q, want %q", steps[0].readme, test.WantReadmeName)
			}
		})
	}
}

// TestCollectUsageBeyondGo checks that a package install carries the flags the
// README cites for it, so the docs comparison is not limited to Go projects.
// A package that provides a differently named binary has nothing to key on, so
// it collects no usage and is left uncompared rather than misattributed.
func TestCollectUsageBeyondGo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Readme    string
		WantKind  string
		WantFlags []string
	}{{ // Test 0: a cargo install whose package names the binary.
		Readme:   "# r\n\n```sh\ncargo install fd-find\n```\n\n```sh\nfd-find --hidden x\n```\n",
		WantKind: "cargo-install", WantFlags: []string{"hidden"},
	}, { // Test 1: an npm install whose package names the binary.
		Readme:   "# r\n\n```sh\nnpm install -g eslint\n```\n\n```sh\neslint --fix src\n```\n",
		WantKind: "npm-install", WantFlags: []string{"fix"},
	}, { // Test 2: a go install still collects its cited flags.
		Readme: "# r\n\n```sh\ngo install example.com/tool@latest\n```\n\n" +
			"```sh\ntool --json run\n```\n",
		WantKind: "go-install", WantFlags: []string{"json"},
	}, { // Test 3: a package providing another binary has nothing to key on.
		Readme:   "# r\n\n```sh\ncargo install ripgrep\n```\n\n```sh\nrg --hidden x\n```\n",
		WantKind: "cargo-install",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeRepoFile(t, dir, "README.md", test.Readme)
			steps, _, problems := collect([]string{dir}, false)
			if len(problems) != 0 {
				t.Fatalf("unexpected problems: %v", problems)
			}
			var got *InstallStep
			for i := range steps {
				if steps[i].Kind == test.WantKind {
					got = &steps[i]
				}
			}
			if got == nil {
				t.Fatalf("no %s step in %v", test.WantKind, steps)
			}
			var flags []string
			if got.Usage != nil {
				flags = got.Usage.Flags
			}
			if diff := cmp.Diff(test.WantFlags, flags, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("cited flags mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRepoName checks that a report names a repository something a reader
// recognizes, whatever path was typed to reach it.
func TestRepoName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Path string
		Want string
	}{{ // Test 0: a named path keeps its name.
		Path: "/src/example/mytool", Want: "mytool",
	}, { // Test 1: a trailing slash changes nothing.
		Path: "/src/example/mytool/", Want: "mytool",
	}, { // Test 2: a relative path names the directory it lands in.
		Path: ".", Want: filepath.Base(mustWD(t)),
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := repoName(test.Path); got != test.Want {
				t.Errorf("repoName(%q) = %q, want %q", test.Path, got, test.Want)
			}
		})
	}
}

// mustWD returns the working directory or fails the test.
func mustWD(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

// TestSessionInstallsClone checks when a clone recipe becomes the session's
// install. fzf documents only package managers kibble does not run and its own
// clone-and-install script, so without the recipe its examples never ran.
func TestSessionInstallsClone(t *testing.T) {
	t.Parallel()
	clone := func(repo, url string, block ...string) InstallStep {
		return InstallStep{Repo: repo, Kind: "git-clone", Module: url, Block: block, Run: true}
	}
	const fzfURL = "https://github.com/junegunn/fzf.git"
	fzf := clone("fzf", fzfURL, "git clone --depth 1 "+fzfURL+" ~/.fzf", "~/.fzf/install")
	goFzf := InstallStep{Repo: "fzf", Kind: "go-install", Module: "github.com/junegunn/fzf@latest", Run: true}
	ssh := clone("tool", "git@github.com:o/tool.git", "git clone git@github.com:o/tool.git")
	tpm := clone("dotfiles", "https://github.com/tmux-plugins/tpm",
		"git clone https://github.com/tmux-plugins/tpm ~/.tmux/plugins/tpm")
	cargo := clone("tool", "https://github.com/o/tool",
		"git clone https://github.com/o/tool", "cd tool && cargo build")
	tests := []struct {
		Steps        []InstallStep
		WantInstalls []kplan.PlanInstall
	}{{ // Test 0: The fzf README: a self clone and nothing kibble runs.
		Steps: []InstallStep{{Repo: "fzf", Kind: "brew", Module: "fzf"}, fzf},
		WantInstalls: []kplan.PlanInstall{{
			Cmd: "git clone --depth 1 " + fzfURL + " ~/.fzf\n~/.fzf/install", Dir: "/work",
		}},
	}, { // Test 1: An SSH clone is rewritten as the standalone step rewrites it.
		Steps:        []InstallStep{ssh},
		WantInstalls: []kplan.PlanInstall{{Cmd: "git clone https://github.com/o/tool.git", Dir: "/work"}},
	}, { // Test 2: A Go install is cheaper and already handled, so it wins.
		Steps:        []InstallStep{fzf, goFzf},
		WantInstalls: []kplan.PlanInstall{{Cmd: "go install " + goFzf.Module, Ecosystem: "go"}},
	}, { // Test 3: Cloning another repository installs nothing the examples call.
		Steps: []InstallStep{tpm},
	}, { // Test 4: Two self clones are two routes to one tool, so the first is used.
		Steps: []InstallStep{cargo, clone("tool", "https://github.com/o/tool.git", "git clone x")},
		WantInstalls: []kplan.PlanInstall{{
			Cmd: "git clone https://github.com/o/tool\ncd tool && cargo build", Ecosystem: "rust",
			Dir: "/work",
		}},
	}, { // Test 5: A clone step without a recipe has nothing to run.
		Steps: []InstallStep{clone("tool", "https://github.com/o/tool.git")},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			_, got := sessionInstalls(t.TempDir(), test.Steps)
			opts := []cmp.Option{cmpopts.EquateEmpty(), cmpopts.IgnoreFields(kplan.PlanInstall{}, "Binary")}
			if diff := cmp.Diff(test.WantInstalls, got, opts...); diff != "" {
				t.Errorf("installs mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
