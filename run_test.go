package main

import (
	"fmt"
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
	}, { // Test 4: no marker at all means the container itself errored.
		In:         "docker: Error response from daemon: pull access denied\n",
		WantStatus: StatusFail,
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
