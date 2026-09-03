package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	// reCommandsHeading matches the heading a command-line parser prints above
	// its subcommand list. Cobra says "Available Commands:", clap and argparse
	// say "Commands:" or "SUBCOMMANDS:", so all three are recognized.
	reCommandsHeading = regexp.MustCompile(`(?i)^\s*(\w+\s+)?(sub)?commands:\s*$`)
	// reCommandEntry matches one entry in that list: an indented name followed
	// by its one-line description.
	reCommandEntry = regexp.MustCompile(`^\s+([a-z][a-z0-9_-]*)(\s{2,}\S|\s*$)`)
	// reOtherHeading matches the next heading, which ends the command list.
	reOtherHeading = regexp.MustCompile(`(?i)^\s*[A-Za-z][A-Za-z ]*:\s*$`)
)

// helpCommands returns the public subcommands a binary advertises, read from
// the first command list in its help output. Anything a parser hides from
// help is deliberately not part of the public surface, so reading help rather
// than the source is what separates "chose not to document" from "forgot to".
func helpCommands(helpText string) []string {
	// Grouped help such as docker's prints several command lists under
	// headings like "Management Commands:", so every list in the screen
	// counts. A blank line or a different heading only closes the current
	// list; a later commands heading opens the next one.
	var out []string
	seen := map[string]bool{}
	inList := false
	for _, line := range strings.Split(helpText, "\n") {
		switch {
		case reCommandsHeading.MatchString(line):
			inList = true
		case !inList:
			continue
		case strings.TrimSpace(line) == "" || reOtherHeading.MatchString(line):
			inList = false
		default:
			m := reCommandEntry.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			if name := m[1]; !seen[name] && !helpNonCommands[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// helpNonCommands are entries a parser lists beside real subcommands that no
// document is expected to cover.
var helpNonCommands = map[string]bool{
	"help": true, "completion": true, "version": true,
}

// docSet is a repository's documentation: the README on its own, and every
// document together. The split lets a check say a command is documented
// somewhere without claiming a reader would find it.
type docSet struct {
	// Readme is the README's text.
	Readme string
	// All is every document's text, the README included.
	All string
}

// docExtensions are the file types read as documentation.
var docExtensions = map[string]bool{".md": true, ".mdx": true, ".rst": true, ".txt": true}

// readDocSet collects the repository's documentation. It reads the README plus
// any markdown beside it and under docs/, so a command documented in a
// reference file counts as covered even when the README stays curated. The
// readme argument is the file's name within dir, not its text.
func readDocSet(dir, readme string) docSet {
	var set docSet
	if dir == "" {
		return set
	}
	if body, err := os.ReadFile(filepath.Join(dir, readme)); err == nil {
		set.Readme = string(body)
	}
	var b strings.Builder
	b.WriteString(set.Readme)
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == "node_modules" || name == "vendor" ||
				name == "testdata" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if !docExtensions[strings.ToLower(filepath.Ext(name))] {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		b.WriteByte('\n')
		b.Write(body)
		return nil
	})
	set.All = b.String()
	return set
}

// mentions reports whether a document names a subcommand of a binary. The
// binary's own name must appear beside it, so a command called "list" is not
// counted covered by unrelated prose that happens to use the word.
func mentions(text, binary, cmd string) bool {
	re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(binary) + `\b[^\n]{0,80}\b` +
		regexp.QuoteMeta(cmd) + `\b`)
	if re.MatchString(text) {
		return true
	}
	// A reference table often heads a row with the bare command name.
	row := regexp.MustCompile(`(?m)^[|\s#*` + "`" + `-]*` + regexp.QuoteMeta(cmd) + `\b`)
	return row.MatchString(text)
}

// docCoverageChecks compares each binary's advertised commands against the
// repository's documentation. A command missing from every document is a hole
// somebody has to fill. A command documented only outside the README is
// reported as a count rather than one finding each, since which commands earn
// README space is the author's editorial call and nagging per command would
// bury the first result.
func docCoverageChecks(results []Result) []Result {
	var out []Result
	for _, r := range results {
		// Only an install step carries help output. Checking every passing
		// result would emit an empty check for the brew and example steps too.
		if r.helpText == "" {
			continue
		}
		if r.Status != StatusVerified && r.Status != StatusBuilt {
			continue
		}
		root := r.helpRoot
		if root == "" {
			root = r.helpText
		}
		cmds := helpCommands(root)
		check := Result{
			Step: InstallStep{
				Repo: r.Step.Repo, Kind: "doc-coverage", Binary: r.Step.Binary,
				dir: r.Step.dir, readme: r.Step.readme,
			},
		}
		if len(cmds) == 0 {
			check.Status = StatusSkipped
			check.Detail = "no command list in the help output to check against"
			out = append(out, check)
			continue
		}
		docs := readDocSet(r.Step.dir, r.Step.readme)
		var undocumented, outsideReadme []string
		for _, c := range cmds {
			switch {
			case !mentions(docs.All, r.Step.Binary, c):
				undocumented = append(undocumented, c)
			case !mentions(docs.Readme, r.Step.Binary, c):
				outsideReadme = append(outsideReadme, c)
			}
		}
		sort.Strings(undocumented)
		sort.Strings(outsideReadme)
		switch {
		case len(undocumented) > 0:
			check.Status = StatusGap
			check.Detail = fmt.Sprintf("%d of %d commands documented nowhere: %s",
				len(undocumented), len(cmds), strings.Join(undocumented, " "))
		default:
			check.Status = StatusVerified
			check.Detail = fmt.Sprintf("%d commands documented", len(cmds))
			if len(outsideReadme) > 0 {
				check.Detail += fmt.Sprintf(", %d only outside the README (%s)",
					len(outsideReadme), strings.Join(outsideReadme, " "))
			}
		}
		out = append(out, check)
	}
	return out
}
