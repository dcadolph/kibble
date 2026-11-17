package main

import (
	"fmt"
	"path"
	"strings"
)

// readerSupplied reports whether a name a line passed, and that no step
// made, is the reader's own input rather than the document's gap. A document
// that points into its repository, as trivy's `examples/misconf/mixed` does,
// owes the reader that path. One that names a chart, a parameters file, or a
// project directory the checkout holds nothing like is showing a flag on input
// the reader brings: every such gap in the 2026-11-11 sweep was of that kind,
// and none was a fault of the document. A path is the document's when its
// first part is at the top of the checkout, or one letter off a name that is,
// since a typo of the repository's own file is the most common rot of all.
// Absolute paths, home paths, and paths above the checkout are always the
// reader's. A relative name read from a directory other than the checkout's
// top keeps the old verdict, since roots says nothing about where it went.
// With no roots known, nothing is the reader's.
func readerSupplied(name, cwd string, roots map[string]bool) bool {
	if len(roots) == 0 {
		return false
	}
	name = strings.Trim(name, `"'`)
	switch {
	case strings.HasPrefix(name, sessionRepoDir+"/"):
		name = strings.TrimPrefix(name, sessionRepoDir+"/")
	case strings.HasPrefix(name, "/"), strings.HasPrefix(name, "~"), strings.HasPrefix(name, "../"):
		return true
	}
	if cwd != "" && cwd != sessionRepoDir {
		return false
	}
	name = path.Clean(strings.TrimPrefix(name, "./"))
	if name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return name != "."
	}
	first, _, _ := strings.Cut(name, "/")
	for root := range roots {
		if nearName(first, root) {
			return false
		}
	}
	return true
}

// nearName reports whether a name is a checkout root or a slip of one: the
// same name in another case, or one edit away. A name of twelve characters or
// more may be two edits away. Names under four characters must match exactly,
// since one edit turns most short words into other short words.
func nearName(name, root string) bool {
	a, b := strings.ToLower(name), strings.ToLower(root)
	if a == b {
		return true
	}
	if len(a) < 4 || len(b) < 4 {
		return false
	}
	limit := 1
	if len(a) >= 12 {
		limit = 2
	}
	return editDistance(a, b) <= limit
}

// editDistance is the optimal string alignment distance between two strings,
// by byte: insertions, deletions, substitutions, and swaps of two neighbors
// each cost one, so "exampels" is one edit from "examples".
func editDistance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}

// readerSuppliedResult reads a line that failed on a name the reader brings.
// The line ran, but on input it never had, so it settles nothing.
func readerSuppliedResult(lr lineResult, name, tail string) lineResult {
	lr.Status = StatusSkipped
	lr.Reason = ReasonMissingFixture
	clean := strings.Trim(name, `"'`)
	if strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "~") || strings.HasPrefix(clean, "../") {
		lr.Detail = fmt.Sprintf("names %s, a path on the reader's machine that no documented step makes", name)
	} else {
		lr.Detail = fmt.Sprintf("names %s, which the reader supplies: nothing in the checkout starts "+
			"there and no documented step makes it", name)
	}
	if tail != "" {
		lr.Detail += ": " + tail
	}
	return lr
}
