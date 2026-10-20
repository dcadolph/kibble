package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestReleaseTags checks which tag shapes count as releases and how they sort.
func TestReleaseTags(t *testing.T) {
	t.Parallel()
	const sha = "0123456789abcdef0123456789abcdef01234567"
	ref := func(names ...string) string {
		var b strings.Builder
		for _, n := range names {
			b.WriteString(sha + "\trefs/tags/" + n + "\n")
		}
		return b.String()
	}
	tests := []struct {
		In       string
		WantTags []releaseTag
	}{{ // Test 0: plain tags with and without v.
		In: ref("15.2.0", "v0.74.5", "3.2.4"),
		WantTags: []releaseTag{
			{"v0.74.5", []int{0, 74, 5}}, {"3.2.4", []int{3, 2, 4}}, {"15.2.0", []int{15, 2, 0}},
		},
	}, { // Test 1: four-part tag is accepted.
		In:       ref("v0.16.8.2"),
		WantTags: []releaseTag{{"v0.16.8.2", []int{0, 16, 8, 2}}},
	}, { // Test 2: sub-crate tags are ignored.
		In: ref("wincolor-0.1.6", "ignore-0.4.33"),
	}, { // Test 3: pre-release tags are ignored.
		In: ref("v1.0.0-rc.3", "1.0.0rc2", "v2.9.0-2"),
	}, { // Test 4: ecosystem suffixes are ignored.
		In: ref("v3.3.1nodejs", "v3.3.1rust"),
	}, { // Test 5: monorepo tags are ignored.
		In: ref("@redocly/cli@2.5.0"),
	}, { // Test 6: junk is ignored.
		In: ref("main", "112/merge", "v0"),
	}, { // Test 7: gum orders numerically, not lexically.
		In: ref("v2.0.2", "v0.17.0", "v2.0.0"),
		WantTags: []releaseTag{
			{"v0.17.0", []int{0, 17, 0}}, {"v2.0.0", []int{2, 0, 0}}, {"v2.0.2", []int{2, 0, 2}},
		},
	}, { // Test 8: peeled lines are dropped.
		In:       ref("v1.0.0") + sha + "\trefs/tags/v1.0.0^{}\n" + sha + "\trefs/tags/v9.9.9^{}\n",
		WantTags: []releaseTag{{"v1.0.0", []int{1, 0, 0}}},
	}, { // Test 9: duplicates collapse and equal parts tie-break by name.
		In: ref("v1.0", "1.0", "v1.0"),
		WantTags: []releaseTag{
			{"1.0", []int{1, 0}}, {"v1.0", []int{1, 0}},
		},
	}, { // Test 10: empty output and a five-part tag.
		In: ref("1.2.3.4.5") + "\n",
	}, { // Test 11: signs and spaces in numbers are rejected.
		In: ref("v+1.2", "v1.-2", "v1. 2"),
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := releaseTags(test.In)
			if diff := cmp.Diff(test.WantTags, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestCompareParts checks numeric comparison with missing parts as zero.
func TestCompareParts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		A          []int
		B          []int
		WantResult int
	}{{ // Test 0: equal.
		A: []int{1, 2, 3}, B: []int{1, 2, 3},
	}, { // Test 1: missing parts are zero.
		A: []int{1, 2}, B: []int{1, 2, 0, 0},
	}, { // Test 2: numeric not lexical.
		A: []int{0, 17, 0}, B: []int{2, 0, 0}, WantResult: -1,
	}, { // Test 3: ten beats nine.
		A: []int{10, 0}, B: []int{9, 9}, WantResult: 1,
	}, { // Test 4: a longer tail wins.
		A: []int{1, 2}, B: []int{1, 2, 0, 1}, WantResult: -1,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := compareParts(test.A, test.B); got != test.WantResult {
				t.Errorf("compareParts(%v, %v) = %d, want %d", test.A, test.B, got, test.WantResult)
			}
		})
	}
}

// TestVersionOf checks version extraction from smoke output lines.
func TestVersionOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In         string
		WantResult string
	}{
		{ // Test 0: version with a commit hash.
			In: "0.74.5 (c0a193e0)", WantResult: "0.74.5",
		}, { // Test 1: leading v is stripped.
			In: "v0.23.0", WantResult: "0.23.0",
		}, { // Test 2: name then version.
			In: "ripgrep 14.1.0", WantResult: "14.1.0",
		}, { // Test 3: version word and v prefix.
			In: "gum version v2.0.2", WantResult: "2.0.2",
		}, { // Test 4: bare version.
			In: "3.1.14", WantResult: "3.1.14",
		}, { // Test 5: devel build.
			In: "(devel)",
		}, { // Test 6: unknown build.
			In: "glow version unknown (built from source)",
		}, { // Test 7: help fallback.
			In: "Usage: foo [OPTIONS]",
		}, { // Test 8: wrapper printing its interpreter first.
			In: "Python 3.12.1", WantResult: "3.12.1",
		}, { // Test 9: fd.
			In: "fd 10.5.0", WantResult: "10.5.0",
		}, { // Test 10: hexyl.
			In: "hexyl 0.17.0", WantResult: "0.17.0",
		}, { // Test 11: five parts are rejected whole.
			In: "1.2.3.4.5",
		}, { // Test 12: trailing sentence dot.
			In: "version 1.2.3.", WantResult: "1.2.3",
		}, { // Test 13: a letter before the digits does not block extraction.
			In: "x1.2.3", WantResult: "1.2.3",
		}, { // Test 14: four parts accepted.
			In: "gita v0.16.8.2", WantResult: "0.16.8.2",
		}, { // Test 15: a single integer is not a version.
			In: "build 7", WantResult: "",
		}, { // Test 16: a rejected run does not hide a later version.
			In: "1.2.3.4.5 then 6.7", WantResult: "6.7",
		}, { // Test 17: empty line.
			In: "",
		}, { // Test 18: a run glued to a preceding dot is not a version.
			In: "build.2.3",
		},
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := versionOf(test.In); got != test.WantResult {
				t.Errorf("versionOf(%q) = %q, want %q", test.In, got, test.WantResult)
			}
		})
	}
}

// newTagRepo creates a bare repository holding one empty commit tagged with each name.
func newTagRepo(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	run := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("", "init", "--bare", "-q")
	tree := run("", "hash-object", "-w", "-t", "tree", "--stdin")
	commit := run("", "commit-tree", tree, "-m", "empty")
	for _, n := range names {
		run("", "update-ref", "refs/tags/"+n, commit)
	}
	return dir
}

// TestLatestTag checks tag listing against a local repository and an unreachable host.
func TestLatestTag(t *testing.T) {
	t.Parallel()
	t.Run("file url", func(t *testing.T) {
		t.Parallel()
		dir := newTagRepo(t, "v1.0.0", "v1.2.0", "junk")
		got, err := latestTag(context.Background(), "file://"+dir)
		if err != nil {
			t.Fatalf("latestTag: %v", err)
		}
		want := []releaseTag{{"v1.0.0", []int{1, 0, 0}}, {"v1.2.0", []int{1, 2, 0}}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		got, err := latestTag(context.Background(), "https://nonexistent.invalid/x.git")
		if err == nil {
			t.Fatalf("latestTag = %v, want error", got)
		}
		if elapsed := time.Since(start); elapsed >= 10*time.Second {
			t.Errorf("took %v, want under 10s", elapsed)
		}
	})
	t.Run("stalled server", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		start := time.Now()
		got, err := latestTag(ctx, srv.URL+"/stalled.git")
		if err == nil {
			t.Fatalf("latestTag = %v, want error", got)
		}
		if elapsed := time.Since(start); elapsed >= 5*time.Second {
			t.Errorf("took %v, want under 5s", elapsed)
		}
	})
}

// TestLatestTagNoPrompt checks that a remote asking for credentials fails at once
// instead of running an askpass program or a terminal prompt. It sets the
// environment, so it cannot run in parallel.
func TestLatestTagNoPrompt(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "asked")
	askpass := filepath.Join(dir, "askpass.sh")
	script := "#!/bin/sh\ntouch '" + marker + "'\necho user\n"
	if err := os.WriteFile(askpass, []byte(script), 0o755); err != nil {
		t.Fatalf("write askpass: %v", err)
	}
	t.Setenv("GIT_ASKPASS", askpass)
	t.Setenv("SSH_ASKPASS", askpass)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="private"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	start := time.Now()
	got, err := latestTag(context.Background(), srv.URL+"/private.git")
	if err == nil {
		t.Fatalf("latestTag = %v, want error", got)
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Errorf("took %v, want under 5s", elapsed)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("askpass ran, want no prompt of any kind")
	}
}

// tagsOf builds a sorted tag list from tag names, the way ls-remote output parses.
func tagsOf(t *testing.T, names ...string) []releaseTag {
	t.Helper()
	var b strings.Builder
	for _, n := range names {
		b.WriteString("0123456789abcdef0123456789abcdef01234567\trefs/tags/" + n + "\n")
	}
	return releaseTags(b.String())
}

// TestAnnotateVersions checks which rows gain a version and a latest tag.
func TestAnnotateVersions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Repo        string
		Status      Status
		SmokeLine   string
		Tags        []string
		WantVersion string
		WantLatest  string
	}{{ // Test 0: settled match.
		Repo: "fd", Status: StatusVerified, SmokeLine: "fd 10.5.0",
		Tags:        []string{"v10.4.0", "v10.5.0"},
		WantVersion: "10.5.0", WantLatest: "v10.5.0",
	}, { // Test 1: settled behind, the rich-cli case.
		Repo: "rich-cli", Status: StatusVerified, SmokeLine: "1.8.0",
		Tags:        []string{"v1.8.0", "v1.8.1"},
		WantVersion: "1.8.0", WantLatest: "v1.8.1",
	}, { // Test 2: latest keeps a tag spelled without v.
		Repo: "ripgrep", Status: StatusVerified, SmokeLine: "ripgrep 15.2.0",
		Tags:        []string{"14.1.0", "15.2.0"},
		WantVersion: "15.2.0", WantLatest: "15.2.0",
	}, { // Test 3: unsettled, no version on the line.
		Repo: "glow", Status: StatusVerified, SmokeLine: "glow version unknown (built from source)",
		Tags: []string{"v3.0.0"},
	}, { // Test 4: a BUILT row with a smoke line is left alone.
		Repo: "fd", Status: StatusBuilt, SmokeLine: "fd 10.5.0", Tags: []string{"v10.5.0"},
	}, { // Test 5: a verified row with no smoke line.
		Repo: "fd", Status: StatusVerified, Tags: []string{"v10.5.0"},
	}, { // Test 6: the repository has no release tags.
		Repo: "fd", Status: StatusVerified, SmokeLine: "fd 10.5.0", Tags: []string{},
	}, { // Test 7: an interpreter's version is not one of the tags.
		Repo: "wrapper", Status: StatusVerified, SmokeLine: "Python 3.12.1",
		Tags: []string{"v1.0.0"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			row := Result{
				Step: InstallStep{Repo: test.Repo}, Status: test.Status,
				SmokeLine: test.SmokeLine, Detail: "detail",
			}
			results := []Result{row}
			annotateVersions(results, map[string][]releaseTag{test.Repo: tagsOf(t, test.Tags...)})
			got := results[0]
			if got.Version != test.WantVersion || got.Latest != test.WantLatest {
				t.Errorf("version, latest = %q, %q, want %q, %q",
					got.Version, got.Latest, test.WantVersion, test.WantLatest)
			}
			if got.Status != row.Status || got.Detail != row.Detail {
				t.Errorf("status, detail = %q, %q, want them unchanged", got.Status, got.Detail)
			}
		})
	}
	t.Run("repository absent from the map", func(t *testing.T) {
		t.Parallel()
		results := []Result{{
			Step: InstallStep{Repo: "fd"}, Status: StatusVerified, SmokeLine: "fd 10.5.0",
		}}
		annotateVersions(results, map[string][]releaseTag{"other": tagsOf(t, "v10.5.0")})
		if results[0].Version != "" || results[0].Latest != "" {
			t.Errorf("version, latest = %q, %q, want both empty",
				results[0].Version, results[0].Latest)
		}
	})
}

// newRemoteRepo creates a directory named proj, initialized as a repository with
// the given origin when origin is non-empty, and left a plain directory otherwise.
func newRemoteRepo(t *testing.T, origin string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if origin == "" {
		return dir
	}
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", origin}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

// TestRepoRemotes checks where each repository's tags are read from.
func TestRepoRemotes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Origin      string
		Clones      []string
		WantRemotes map[string]string
	}{{ // Test 0: a GitHub SSH origin becomes HTTPS.
		Origin:      "git@github.com:owner/repo.git",
		WantRemotes: map[string]string{"proj": "https://github.com/owner/repo.git"},
	}, { // Test 1: no origin falls back to the documented clone.
		Clones:      []string{"https://github.com/owner/clone.git"},
		WantRemotes: map[string]string{"proj": "https://github.com/owner/clone.git"},
	}, { // Test 2: no origin and no clone step gives no entry.
	}, { // Test 3: an SSH origin outside GitHub gives no entry.
		Origin: "git@gitlab.com:owner/repo.git",
	}, { // Test 4: the origin wins over a documented clone.
		Origin:      "https://github.com/owner/origin.git",
		Clones:      []string{"https://github.com/owner/clone.git"},
		WantRemotes: map[string]string{"proj": "https://github.com/owner/origin.git"},
	}, { // Test 5: a GitHub SSH clone step becomes HTTPS.
		Clones:      []string{"git@github.com:owner/clone.git"},
		WantRemotes: map[string]string{"proj": "https://github.com/owner/clone.git"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := newRemoteRepo(t, test.Origin)
			var steps []InstallStep
			for _, c := range test.Clones {
				steps = append(steps, InstallStep{Repo: "proj", Kind: "git-clone", Module: c})
			}
			got := repoRemotes([]string{dir}, steps)
			if diff := cmp.Diff(test.WantRemotes, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestLookupTags checks that every reachable repository is read and a failed
// lookup leaves only its own repository out.
func TestLookupTags(t *testing.T) {
	t.Parallel()
	remotes := map[string]string{
		"a":       "file://" + newTagRepo(t, "v1.0.0"),
		"b":       "file://" + newTagRepo(t, "2.0.0", "2.1.0"),
		"missing": "file://" + filepath.Join(t.TempDir(), "absent"),
	}
	got := lookupTags(context.Background(), remotes)
	want := map[string][]releaseTag{
		"a": {{"v1.0.0", []int{1, 0, 0}}},
		"b": {{"2.0.0", []int{2, 0, 0}}, {"2.1.0", []int{2, 1, 0}}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}
