package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dcadolph/kibble/internal/shell"
)

// What a documented line needs that the session does not have: files, globs,
// home paths, settings, and the repository tree they are checked against. A
// prose file kibble can fabricate becomes a fixture and is recorded as one, so
// a pass against invented input never reads as a pass against the document's.

// synthExtensions are file extensions kibble fabricates a fixture for when
// the docs reference a file they never create. Only prose formats are safe
// to fake; structured formats would change what the example means.
var synthExtensions = map[string]bool{".md": true, ".txt": true}

// synthFixture is the body written for a fabricated fixture file.
const synthFixture = `# Notes

A few plain lines for the documented example to read.
Nothing here is special; the example only needs a file to exist.
`

// missingGlob returns a quoted glob argument whose fixed directory prefix
// matches nothing in the repository, or empty. A doc line such as
// `lint "src/util/**/*.js"` shows the shape of a command against the reader's
// tree, and a repository without src/util cannot honestly run it.
func (pl *planner) missingGlob(flat string) string {
	for _, tok := range shell.Operands(flat) {
		if strings.HasPrefix(tok, "-") || strings.ContainsAny(tok, ":,") {
			continue
		}
		star := strings.Index(tok, "*")
		if star <= 0 || !strings.Contains(tok[:star], "/") {
			continue
		}
		prefix := tok[:strings.LastIndex(tok[:star], "/")+1]
		if strings.ContainsAny(prefix, "$~") {
			continue
		}
		found := false
		for path := range pl.tree {
			if strings.HasPrefix(path, prefix) {
				found = true
				break
			}
		}
		for path := range pl.created {
			if strings.HasPrefix(path, prefix) {
				found = true
				break
			}
		}
		if !found {
			return tok
		}
	}
	return ""
}

// missingFile returns the first file token a line references that neither
// the repo, an earlier line, nor a fixture provides. Files kibble can fake
// are added as fixtures instead of skipping the line.
func (pl *planner) missingFile(flat string) string {
	flat = stripComment(flat)
	fields := strings.Fields(flat)
	for i, raw := range fields {
		if i == 0 || isOutputArg(fields, i) {
			continue
		}
		tok := raw
		if j := strings.LastIndex(tok, "="); j >= 0 {
			tok = tok[j+1:]
		}
		tok = strings.TrimPrefix(tok, "@")
		if strings.HasPrefix(tok, "~/") && reHomeFileArg.MatchString(tok) {
			if pl.created[tok] || createsToken(flat, tok) {
				continue
			}
			return tok
		}
		if !reFileArg.MatchString(tok) && !reDotSlashArg.MatchString(tok) {
			continue
		}
		rel := strings.TrimPrefix(tok, "./")
		if pl.tree[rel] || pl.created[rel] || createsToken(flat, tok) {
			continue
		}
		if synthExtensions[filepath.Ext(rel)] {
			if !pl.fixed[rel] {
				pl.fixed[rel] = true
				pl.created[rel] = true
				pl.plan.Fixtures = append(pl.plan.Fixtures, Fixture{Path: rel, Contents: synthFixture})
			}
			pl.synthetic = append(pl.synthetic, rel)
			continue
		}
		return tok
	}
	return ""
}

// recordCreated tracks the paths a running line will produce, so later
// lines that read them are not flagged as missing their file.
func (pl *planner) recordCreated(flat string) {
	flat = stripComment(flat)
	for _, m := range reCreatedToken.FindAllStringSubmatch(flat, -1) {
		for _, tok := range m[1:] {
			if tok != "" {
				pl.created[strings.TrimPrefix(tok, "./")] = true
			}
		}
	}
	fields := strings.Fields(flat)
	if len(fields) < 2 {
		return
	}
	switch fields[0] {
	case "mkdir", "touch":
		for _, tok := range fields[1:] {
			if !strings.HasPrefix(tok, "-") {
				pl.created[strings.TrimPrefix(tok, "./")] = true
			}
		}
	case "cp", "mv":
		pl.created[strings.TrimPrefix(fields[len(fields)-1], "./")] = true
	}
}

// createsToken reports whether the line itself creates the token, such as a
// redirect target, so the target of `echo x > f.yaml` is not marked missing.
// isOutputArg reports whether a command writes the argument at i rather than
// reading it. A copy's destination and a mkdir's path are things the line
// produces, so requiring them to exist first would report the document as
// incomplete for a step that is doing the creating.
func isOutputArg(fields []string, i int) bool {
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "mkdir", "touch", "mktemp":
		return true
	case "cp", "mv", "install", "ln":
		return i == len(fields)-1
	}
	return false
}

func createsToken(flat, tok string) bool {
	for _, m := range reCreatedToken.FindAllStringSubmatch(flat, -1) {
		for _, t := range m[1:] {
			if t == tok {
				return true
			}
		}
	}
	return false
}

// outputFlags name a flag whose value is a path the command writes.
var outputFlags = map[string]bool{
	"-o": true, "--out": true, "--output": true, "--outfile": true,
	"--out-dir": true, "--output-dir": true, "--dest": true,
	"--destination": true, "--to": true, "--target": true, "--log": true,
}

// hasBareStdinDash reports whether a line passes a bare - argument with no
// pipe feeding it, meaning it would block reading the session's empty stdin.
func hasBareStdinDash(flat string) bool {
	if shell.HasPipe(flat) {
		return false
	}
	for _, tok := range shell.Operands(flat) {
		if tok == "-" {
			return true
		}
	}
	return false
}

// repoTree returns the set of repo-relative file and directory paths in the
// local checkout, capped so a huge repo cannot stall planning. The .git
// directory is ignored.
func repoTree(dir string) map[string]bool {
	tree := map[string]bool{}
	if dir == "" {
		return tree
	}
	count := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return nil
		}
		tree[filepath.ToSlash(rel)] = true
		if count++; count > 20000 {
			return filepath.SkipAll
		}
		return nil
	})
	return tree
}

// reSettingMention matches an environment setting named anywhere in the
// document. Readers are commonly told about a variable in a table or a
// sentence rather than in a runnable line, and a document that names one has
// told the reader what to supply.
var reSettingMention = regexp.MustCompile(`\b[A-Z][A-Z0-9]{2,}(_[A-Z0-9*]+)+\b`)

// documentedSettingNames collects every environment setting the document
// mentions, in any context.
func documentedSettingNames(markdown string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reSettingMention.FindAllString(markdown, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// moduledPath returns the repository's own Go module path, or empty when the
// directory has no go.mod.
func moduledPath(dir string) string {
	if dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// getsOwnModule reports whether a line asks Go to fetch the repository's own
// module. A library documents `go get <its own path>` for the reader to run
// inside their project; run inside the repository it would add the module to
// itself, which Go refuses. The document is right and the session is simply
// standing in the wrong directory.
func (pl *planner) getsOwnModule(flat string) bool {
	if pl.module == "" {
		return false
	}
	fields := strings.Fields(stripComment(flat))
	if len(fields) < 3 || fields[0] != "go" {
		return false
	}
	if fields[1] != "get" && fields[1] != "install" {
		return false
	}
	for _, tok := range fields[2:] {
		path, _, _ := strings.Cut(tok, "@")
		if path == pl.module || strings.HasPrefix(path, pl.module+"/") {
			return true
		}
	}
	return false
}

// missingHomePath returns the first path under the reader's home directory
// that no documented step creates. A document naming ~/src/project is telling
// the reader where their own work lives, not describing a file it ships.
func (pl *planner) missingHomePath(flat string) string {
	fields := strings.Fields(stripComment(flat))
	for i, tok := range fields {
		if i == 0 || isOutputArg(fields, i) {
			continue
		}
		// A path given to an output flag is one the line writes, not one the
		// reader must already have, and the container's home can hold it.
		if i > 0 && outputFlags[strings.TrimSuffix(fields[i-1], "=")] {
			continue
		}
		if !reHomePathArg.MatchString(tok) || pl.created[tok] {
			continue
		}
		return tok
	}
	return ""
}
