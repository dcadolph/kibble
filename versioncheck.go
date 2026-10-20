package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// lsRemoteTimeout bounds one git ls-remote call.
const lsRemoteTimeout = 10 * time.Second

// releaseTag is a release tag as spelled in the remote and its numeric parts.
type releaseTag struct {
	// Name is the tag as spelled, without the refs/tags/ prefix.
	Name string
	// Parts holds the dot-separated integers of the tag.
	Parts []int
}

// releaseTags parses git ls-remote --tags output into release tags sorted ascending.
func releaseTags(out string) []releaseTag {
	seen := make(map[string]bool)
	var tags []releaseTag
	for _, line := range strings.Split(out, "\n") {
		_, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || strings.HasSuffix(ref, "^{}") {
			continue
		}
		name, ok := strings.CutPrefix(ref, "refs/tags/")
		if !ok || seen[name] {
			continue
		}
		p := parts(strings.TrimPrefix(name, "v"))
		if len(p) < 2 || len(p) > 4 {
			continue
		}
		seen[name] = true
		tags = append(tags, releaseTag{Name: name, Parts: p})
	}
	slices.SortFunc(tags, func(a, b releaseTag) int {
		if c := compareParts(a.Parts, b.Parts); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return tags
}

// compareParts compares two version number lists numerically, treating missing parts as zero.
func compareParts(a, b []int) int {
	for i := 0; i < max(len(a), len(b)); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// parts splits a dotted version into its integers, or returns nil if any part is not one.
func parts(v string) []int {
	fields := strings.Split(v, ".")
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || f[0] == '+' || f[0] == '-' {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// latestTag lists the release tags of the repository at url, sorted ascending. It
// never prompts: no credential helper runs, and an empty GIT_ASKPASS keeps an
// exported askpass program from opening a dialog that GIT_TERMINAL_PROMPT does not
// cover. Killing git leaves its HTTP helper holding the output pipe, so the wait
// delay bounds the call and the low-speed limit makes a stalled helper exit on its
// own. The url is HTTP(S) or file, since an SSH remote can still ask for a passphrase.
func latestTag(ctx context.Context, url string) ([]releaseTag, error) {
	ctx, cancel := context.WithTimeout(ctx, lsRemoteTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-c", "credential.helper=",
		"-c", "http.lowSpeedLimit=1", "-c", "http.lowSpeedTime=10",
		"ls-remote", "--tags", url)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ls-remote failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return releaseTags(stdout.String()), nil
}

// annotateVersions records each verified install's reported version and the
// repository's newest release tag. A row is annotated only when its version is
// itself one of the repository's release tags, since anything else, such as a
// dev build, an interpreter's version, or a help screen, is a claim the tag list
// cannot vouch for. Status and detail are never touched.
func annotateVersions(results []Result, tags map[string][]releaseTag) {
	for i := range results {
		r := &results[i]
		list := tags[r.Step.Repo]
		if r.Status != StatusVerified || r.SmokeLine == "" || len(list) == 0 {
			continue
		}
		v := versionOf(r.SmokeLine)
		if v == "" {
			continue
		}
		p := parts(v)
		isTag := func(t releaseTag) bool { return compareParts(t.Parts, p) == 0 }
		if !slices.ContainsFunc(list, isTag) {
			continue
		}
		r.Version = v
		r.Latest = list[len(list)-1].Name
	}
}

// repoRemotes maps each repository name to the URL its tags are read from: the
// checkout's origin, else the repository's first documented clone. Only HTTP(S)
// URLs are kept, after GitHub SSH remotes are rewritten, since any other SSH
// remote could ask for a passphrase.
func repoRemotes(paths []string, steps []InstallStep) map[string]string {
	remotes := make(map[string]string)
	for _, p := range paths {
		out, err := exec.Command("git", "-C", p, "config", "--get", "remote.origin.url").Output()
		if err != nil {
			continue
		}
		if u := httpRemote(strings.TrimSpace(string(out))); u != "" {
			remotes[repoName(p)] = u
		}
	}
	seen := make(map[string]bool)
	for _, s := range steps {
		if s.Kind != "git-clone" || seen[s.Repo] {
			continue
		}
		seen[s.Repo] = true
		if _, ok := remotes[s.Repo]; ok {
			continue
		}
		if u := httpRemote(s.Module); u != "" {
			remotes[s.Repo] = u
		}
	}
	return remotes
}

// httpRemote returns url as an HTTP(S) address, rewriting a GitHub SSH remote,
// or empty for any other form.
func httpRemote(url string) string {
	u := rewriteSSH(url)
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		return u
	}
	return ""
}

// lookupTags reads every repository's release tags concurrently, one lookup per
// repository. A failed lookup leaves its repository out, which leaves its rows
// without version fields: the comparison is evidence, so no verdict waits on it.
func lookupTags(ctx context.Context, remotes map[string]string) map[string][]releaseTag {
	var mu sync.Mutex
	var wg sync.WaitGroup
	tags := make(map[string][]releaseTag, len(remotes))
	for name, url := range remotes {
		wg.Go(func() {
			list, err := latestTag(ctx, url)
			if err != nil {
				return
			}
			mu.Lock()
			tags[name] = list
			mu.Unlock()
		})
	}
	wg.Wait()
	return tags
}

// startTagLookup begins reading the repositories' release tags in the
// background, so the lookups overlap the installs instead of adding to them.
func startTagLookup(ctx context.Context, paths []string, steps []InstallStep) <-chan map[string][]releaseTag {
	done := make(chan map[string][]releaseTag, 1)
	remotes := repoRemotes(paths, steps)
	go func() { done <- lookupTags(ctx, remotes) }()
	return done
}

// versionOf returns the first two-to-four part dotted version on line, without a leading v.
func versionOf(line string) string {
	isDigit := func(i int) bool { return i < len(line) && line[i] >= '0' && line[i] <= '9' }
	for i := 0; i < len(line); {
		if !isDigit(i) || (i > 0 && (isDigit(i-1) || line[i-1] == '.')) {
			i++
			continue
		}
		end, n := i, 1
		for {
			for isDigit(end) {
				end++
			}
			if end+1 < len(line) && line[end] == '.' && isDigit(end+1) {
				end++
				n++
				continue
			}
			break
		}
		if n >= 2 && n <= 4 {
			return line[i:end]
		}
		i = end
	}
	return ""
}
