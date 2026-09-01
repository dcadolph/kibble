package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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
		Results: []Result{res(StatusPass), res(StatusSkipped)}, Want: false,
	}, { // Test 1: a build failure always fails.
		Results: []Result{res(StatusPass), res(StatusFail)}, Want: true,
	}, { // Test 2: a timeout does not fail by default.
		Results: []Result{res(StatusTimeout)}, Strict: false, Want: false,
	}, { // Test 3: a timeout fails under strict.
		Results: []Result{res(StatusTimeout)}, Strict: true, Want: true,
	}, { // Test 4: a smoke failure fails under strict.
		Results: []Result{res(StatusPassBuild)}, Strict: true, Want: true,
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

// TestHasRunnable checks detection of executable steps.
func TestHasRunnable(t *testing.T) {
	t.Parallel()
	if hasRunnable([]InstallStep{{Run: false}}) {
		t.Errorf("want false when no step is runnable")
	}
	if !hasRunnable([]InstallStep{{Run: false}, {Run: true}}) {
		t.Errorf("want true when a runnable step exists")
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
