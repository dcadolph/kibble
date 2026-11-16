package main

import (
	"os"
	"path"
	"path/filepath"

	"github.com/go-git/go-git/v5/plumbing/format/index"
)

// trackedSet is what the git index says a checkout tracks.
type trackedSet struct {
	// dirs holds every directory with at least one tracked file under it, as a
	// slash-separated path relative to the checkout.
	dirs map[string]bool
	// files holds every tracked file, named the same way.
	files map[string]bool
}

// tracked returns the files of a checkout that git tracks and the directories
// that hold them. It reads the git index and nothing else. A checkout with no
// index of its own, such as a source tarball or a linked worktree, returns an
// empty set.
func tracked(dir string) trackedSet {
	f, err := os.Open(filepath.Join(dir, ".git", "index"))
	if err != nil {
		return trackedSet{}
	}
	defer func() { _ = f.Close() }()
	var idx index.Index
	if err := index.NewDecoder(f).Decode(&idx); err != nil {
		return trackedSet{}
	}
	set := trackedSet{dirs: map[string]bool{}, files: map[string]bool{}}
	for _, e := range idx.Entries {
		set.files[e.Name] = true
		for d := path.Dir(e.Name); d != "." && !set.dirs[d]; d = path.Dir(d) {
			set.dirs[d] = true
		}
	}
	return set
}
