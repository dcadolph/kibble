package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// replayDirs are the directories whose documents are replayed alongside the
// README. A docs tree is where install and usage instructions go once a README
// gets crowded, and it rots faster than the front page because nobody reads it
// on the way past.
var replayDirs = map[string]bool{"docs": true, "doc": true}

// replayNames are top-level documents replayed by name. Each one exists to
// walk a reader through commands, so its lines are meant to work.
var replayNames = map[string]bool{
	"getting_started": true, "getting-started": true, "gettingstarted": true,
	"quickstart": true, "quick-start": true, "install": true,
	"installation": true, "usage": true, "tutorial": true, "upgrading": true,
	"guide": true, "user-guide": true, "user_guide": true, "userguide": true,
	"howto": true, "how-to": true, "cookbook": true, "recipes": true,
	"examples": true, "walkthrough": true, "migration": true, "faq": true,
}

// skipNames are documents that describe the project rather than instruct a
// reader, so running their commands proves nothing about the docs a user
// follows. Contributing guides in particular tell maintainers to run tests,
// which is not what a reader is being asked to do.
var skipNames = map[string]bool{
	"changelog": true, "contributing": true, "code_of_conduct": true,
	"security": true, "license": true, "authors": true, "history": true,
	"notice": true, "roadmap": true, "commercial": true, "architecture": true,
	"skill": true, "agents": true, "claude": true,
}

// skipPrefixes drop documents written for an agent or a maintainer rather
// than a reader following instructions, whatever suffix the name carries.
var skipPrefixes = []string{"claude", "agent", "skill", "adr-", "rfc-", "design"}

// skipDirs are directories whose markdown is generated, vendored, or internal
// notes rather than documentation a reader follows.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "testdata": true,
	"dist": true, "build": true, "public": true, "site": true, "target": true,
	"adr": true, "decisions": true, "rfcs": true, "internal": true,
}

// replayDocs returns the documents whose examples are replayed, as paths
// relative to dir, with the README first. Selection is conservative on
// purpose: a document kibble runs by mistake spends a container and reports a
// failure about instructions nobody was following.
func replayDocs(dir, readme string, cfg *ExamplesConfig) []string {
	if dir == "" {
		return []string{readme}
	}
	out := []string{readme}
	seen := map[string]bool{readme: true}
	var found []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		if d.IsDir() {
			if path != dir && skipDirs[strings.ToLower(d.Name())] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(d.Name())) != ".md" || seen[rel] {
			return nil
		}
		base := strings.ToLower(strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())))
		if skipNames[base] || hasSkipPrefix(base) {
			return nil
		}
		top := strings.Split(filepath.ToSlash(rel), "/")[0]
		inDocs := top != rel && replayDirs[strings.ToLower(top)]
		if !inDocs && !replayNames[base] {
			return nil
		}
		if !hasShellBlock(filepath.Join(dir, rel)) {
			return nil
		}
		found = append(found, rel)
		return nil
	})
	sort.Strings(found)
	for _, f := range found {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return applyDocRules(out, readme, cfg)
}

// isInstallDoc reports whether a document's base name marks it as install
// instructions a reader follows to get the tool. It is deliberately narrower
// than replayNames: a tutorial or an FAQ can show an install command in
// passing, and extracting that as a step kibble runs would spend a container
// on a line nobody was told to run.
func isInstallDoc(base string) bool {
	if strings.Contains(base, "install") {
		return true
	}
	switch base {
	case "getting_started", "getting-started", "gettingstarted",
		"setup", "quickstart", "quick-start", "start":
		return true
	}
	return false
}

// installDocs returns the documents to extract install steps from, as paths
// relative to dir, with the README first. Install instructions live in the
// README and in install-named documents, in the tree or under a docs
// directory, so those are read and nothing else is, to keep a stray command in
// an unrelated document from becoming a step.
func installDocs(dir, readme string) []string {
	if dir == "" {
		return []string{readme}
	}
	out := []string{readme}
	seen := map[string]bool{readme: true}
	var found []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		if d.IsDir() {
			if path != dir && skipDirs[strings.ToLower(d.Name())] {
				return filepath.SkipDir
			}
			return nil
		}
		if !docExtensions[strings.ToLower(filepath.Ext(d.Name()))] || seen[rel] {
			return nil
		}
		base := strings.ToLower(strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())))
		if skipNames[base] || hasSkipPrefix(base) || !isInstallDoc(base) {
			return nil
		}
		found = append(found, rel)
		return nil
	})
	sort.Strings(found)
	for _, f := range found {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// applyDocRules lets a repository settle what the convention cannot: docs adds
// a document the rules skip, skipDocs drops one they pick up. The README is
// never dropped, since a repository with no runnable README has nothing to say.
func applyDocRules(docs []string, readme string, cfg *ExamplesConfig) []string {
	if cfg == nil {
		return docs
	}
	have := map[string]bool{}
	for _, d := range docs {
		have[d] = true
	}
	for _, d := range cfg.Docs {
		if !have[d] {
			have[d] = true
			docs = append(docs, d)
		}
	}
	drop := map[string]bool{}
	for _, d := range cfg.SkipDocs {
		if d != readme {
			drop[d] = true
		}
	}
	var out []string
	for _, d := range docs {
		if !drop[d] {
			out = append(out, d)
		}
	}
	return out
}

// hasShellBlock reports whether a document contains a fenced shell block.
// A document with nothing to run costs a container and proves nothing.
func hasShellBlock(path string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, b := range codeBlocks(string(body)) {
		if !b.Span && shellLangs[b.Lang] && len(b.Lines) > 0 {
			return true
		}
	}
	return false
}

// hasSkipPrefix reports whether a document's name marks it as written for an
// agent or a maintainer.
func hasSkipPrefix(base string) bool {
	for _, p := range skipPrefixes {
		if strings.HasPrefix(base, p) {
			return true
		}
	}
	return false
}
