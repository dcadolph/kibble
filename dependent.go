package main

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/dcadolph/kibble/internal/docblock"
	kplan "github.com/dcadolph/kibble/internal/plan"
	"github.com/dcadolph/kibble/internal/shell"
)

// Failures that may follow from lines which never ran. None of these
// observations is causal, so none of them clears a document: they stop one
// root cause being reported as several broken lines, and leave the affected
// lines blocked rather than excused.

// resolveMissingBinaries downgrades failures on lines that invoke a
// documented binary the session does not have. A README can document several
// tools while its install provides one, as a conda alternative next to a
// cargo install, and a line calling the absent one says nothing about the
// docs being wrong.
func resolveMissingBinaries(run *exampleRun, plan *kplan.Plan, have map[string]bool) {
	if len(have) == 0 {
		return
	}
	bins := map[string]bool{}
	for _, b := range plan.Binaries {
		bins[b] = true
	}
	for si := range run.Steps {
		s := &run.Steps[si]
		for li := range s.Lines {
			l := &s.Lines[li]
			if l.Status != StatusFail {
				continue
			}
			if bin, _ := kplan.InvokedBinary(l.Cmd, bins); bin != "" && !have[bin] {
				l.Status = StatusSkipped
				l.Detail = fmt.Sprintf("invokes %s, which the documented install does not provide", bin)
			}
		}
	}
}

// resolveDependentFailures downgrades failures that may follow from lines
// which never ran: a failure whose output names such a command, and a failure
// in the same step and subcommand family as an earlier one. Neither
// observation is causal. A tool prints its own name in hints that have
// nothing to do with why it failed, and a second invocation of a subcommand
// is usually independent of the first. What the observation supports is that
// the session is no longer a clean test of this line, which is why these
// become blocked rather than skipped: the cascade stops being reported as
// several broken lines without any of them being called fine. A gap counts as
// not having run, since the document's own hole stopped the line. A Go build
// that finds the document's file beside the checkout's own package is blocked
// here too, since it takes the earlier file writes of the run to see it.
func resolveDependentFailures(run *exampleRun, plan *kplan.Plan) {
	bins := map[string]bool{}
	for _, b := range plan.Binaries {
		bins[b] = true
	}
	// Commands that ran and passed, so a failure naming one of them is not
	// excused by it.
	passed := map[string]bool{}
	for _, s := range run.Steps {
		for _, l := range s.Lines {
			if l.Status != StatusVerified {
				continue
			}
			if bin, sub := kplan.InvokedBinary(l.Cmd, bins); bin != "" && sub != "" {
				passed[bin+" "+sub] = true
			}
		}
	}
	var skippedCmds []string
	for _, s := range run.Steps {
		for _, l := range s.Lines {
			if l.Status != StatusSkipped && l.Status != StatusGap {
				continue
			}
			if bin, sub := kplan.InvokedBinary(l.Cmd, bins); bin != "" && sub != "" {
				skippedCmds = append(skippedCmds, bin+" "+sub)
			}
		}
	}
	// Earlier line results per document, for the shared-operand rule, which
	// reads across the document's steps rather than within one.
	prior := map[string][]lineResult{}
	// Go files the checkout held at its top before the session started.
	checkout := map[string]bool{}
	for _, f := range plan.RootGoFiles {
		checkout[f] = true
	}
	// Files the current document's file blocks wrote from the top of the
	// checkout, in run order. The session restores the checkout between
	// documents when it could save it, so an earlier document's files are
	// gone by the next one and are never counted for it.
	written, writtenDoc := map[string]bool{}, ""
	for si := range run.Steps {
		s := &run.Steps[si]
		if s.Doc != writtenDoc {
			written, writtenDoc = map[string]bool{}, s.Doc
		}
		for li := range s.Lines {
			l := &s.Lines[li]
			if l.Status != StatusFail {
				m := reFileWrite.FindStringSubmatch(l.Cmd)
				if m != nil && l.Status == StatusVerified && l.cwd == sessionRepoDir {
					written[m[1]] = true
				}
				prior[s.Doc] = append(prior[s.Doc], *l)
				continue
			}
			if doc, own := mixedCheckoutPackages(l.output, written, checkout); doc != "" {
				l.Status = StatusBlocked
				l.Reason = ReasonCheckoutState
				l.Detail = fmt.Sprintf("wrote %s into the repository's own checkout, beside %s of "+
					"another Go package, so the claim was not tested as written: %s", doc, own, l.Detail)
				continue
			}
			if cited := citedSkipped(l.output, skippedCmds); cited != "" {
				l.Status = StatusBlocked
				l.Reason = ReasonDependsOnSkipped
				l.Detail = fmt.Sprintf("failed naming `%s`, which did not run", cited)
				continue
			}
			if word := skippedProvider(s.Lines[:li], l.output); word != "" {
				l.Status = StatusBlocked
				l.Reason = ReasonMissingFixture
				l.Detail = fmt.Sprintf("follows a skipped line that would have provided %s", word)
				continue
			}
			if prior := earlierGapInFamily(s.Lines[:li], l.Cmd, bins); prior != "" {
				l.Status = StatusBlocked
				l.Reason = ReasonDependsOnSkipped
				l.Detail = fmt.Sprintf("failed after `%s`, which the document's own gap stopped", prior)
				continue
			}
			if need := namedSiblingNotRun(l.output, bins, passed); need != "" {
				l.Status = StatusBlocked
				l.Reason = ReasonDependsOnSkipped
				l.Detail = fmt.Sprintf("says to run `%s` first, which did not run", need)
			} else if op, earlier := sharedOperandNotPassed(prior[s.Doc], l.Cmd); op != "" {
				l.Status = StatusBlocked
				l.Reason = ReasonDependsOnSkipped
				l.Detail = fmt.Sprintf("names %s, which `%s` also named and did not pass", op, earlier)
			}
			prior[s.Doc] = append(prior[s.Doc], *l)
		}
	}
}

var (
	// reFileWrite matches the line a planned file block runs and captures the
	// path it writes, relative to the directory the line ran in. The
	// terminator may carry a number after the part matched here.
	reFileWrite = regexp.MustCompile(`^(?:mkdir -p '[^']+' && )?cat > '([^']+)' <<'KIBBLE_FILE_END`)
	// reMixedPackages matches Go refusing a directory that holds two packages
	// and captures the file it names for each and the directory.
	reMixedPackages = regexp.MustCompile(
		`found packages \w+ \(([^)\s]+\.go)\) and \w+ \(([^)\s]+\.go)\) in (\S+)`)
)

// mixedCheckoutPackages returns the document's file and the checkout's file
// when Go reports two packages in the repository root, the document's file
// blocks wrote one of the two files it names, and the checkout held the other
// before the session started. sqlc's tutorials tell the reader in prose to work
// in a new directory, so the session stays in the checkout and writes
// tutorial.go beside sqlc's own root package. A clash between two files the
// document wrote is the document's own, and so is a clash with a file its
// commands made some other way, such as a generator's output. A file written
// into a subdirectory is not the one Go names at the root, and a clash in any
// other directory is left as it reads, since only the root's files are known
// to be the checkout's.
func mixedCheckoutPackages(output string, written, checkout map[string]bool) (doc, own string) {
	m := reMixedPackages.FindStringSubmatch(output)
	if m == nil || m[3] != sessionRepoDir {
		return "", ""
	}
	switch a, b := m[1], m[2]; {
	case written[a] && !written[b] && checkout[b]:
		return a, b
	case written[b] && !written[a] && checkout[a]:
		return b, a
	}
	return "", ""
}

var (
	// reAbsentQuoted matches output reporting a quoted name as absent, such
	// as Go's `no such tool "air"`, and captures the name.
	reAbsentQuoted = regexp.MustCompile(
		"(?i)(?:no such (?:tool|file|command)|not found)[^\"'`\\n]{0,20}[\"'`]([\\w.-]+)[\"'`]" +
			"|[\"'`]([\\w.-]+)[\"'`][^\\n]{0,20}(?:not found|no such file)")
	// reAbsentBare matches a shell reporting a bare name as absent, such as
	// "air: command not found", and captures the name.
	reAbsentBare = regexp.MustCompile(`(?i)(?:^|[\s:])([\w.-]+): (?:command )?not found`)
)

// skippedProvider returns the name a failing line reports as absent when an
// earlier line of the same block was skipped for a missing fixture or
// dependency and would have provided it. air's README skips `go get -tool
// github.com/air-verse/air@latest`, since it adds a tool to the reader's own
// module, and the next line fails with `go: no such tool "air"`. The name
// must be a whole argument of the skipped line or the last element of a
// module path in it, never part of a URL, and a placeholder skip provides
// nothing.
func skippedProvider(earlier []lineResult, output string) string {
	var names []string
	for _, line := range strings.Split(output, "\n") {
		for _, m := range reAbsentQuoted.FindAllStringSubmatch(line, -1) {
			names = append(names, m[1]+m[2])
		}
		for _, m := range reAbsentBare.FindAllStringSubmatch(line, -1) {
			names = append(names, m[1])
		}
	}
	for _, prev := range earlier {
		if prev.Status != StatusSkipped ||
			(prev.Reason != ReasonMissingFixture && prev.Reason != ReasonMissingDependency) {
			continue
		}
		for _, f := range strings.Fields(prev.Cmd) {
			if strings.Contains(f, "://") {
				continue
			}
			mod, _, _ := strings.Cut(f, "@")
			for _, n := range names {
				if n != "" && (f == n || path.Base(mod) == n) {
					return n
				}
			}
		}
	}
	return ""
}

// sharedOperandNotPassed returns a file-shaped operand of a failing line and
// the first earlier line of the same document that also named it and was
// skipped, blocked, or stopped by a gap. A query on a database whose load was
// blocked is the same hole surfacing again. An earlier failure does not count,
// since a break repeated on several lines of one file is still several
// reports. Like the other rules here it is not causal: the earlier line still
// reports, and the later ones stop counting as separate breaks.
func sharedOperandNotPassed(prior []lineResult, cmd string) (operand, earlier string) {
	for _, op := range fileOperands(cmd) {
		for _, p := range prior {
			if p.Status == StatusVerified || p.Status == StatusFail {
				continue
			}
			for _, pop := range fileOperands(p.Cmd) {
				if pop == op {
					return op, docblock.Flatten(p.Cmd)
				}
			}
		}
	}
	return "", ""
}

// reScriptFile matches a source file an interpreter runs, which is the program
// of a line such as python3 scripts/bench.py rather than data it reads.
var reScriptFile = regexp.MustCompile(`\.(py|js|mjs|cjs|ts|sh|bash|rb|pl|php|lua)$`)

// fileOperands returns the operands of a line shaped like files: a dot or a
// slash, no whitespace, not a flag, not a URL and not a script. A flag written
// as --flag=value contributes its value. The program is not an operand, whether
// it is the command name or a script handed to an interpreter, so two runs of
// the same program with different arguments never pair.
func fileOperands(cmd string) []string {
	words := shell.Operands(cmd)
	if words == nil {
		if fields := strings.Fields(cmd); len(fields) > 1 {
			words = fields[1:]
		}
	}
	var out []string
	for _, w := range words {
		if strings.HasPrefix(w, "-") {
			_, v, ok := strings.Cut(w, "=")
			if !ok {
				continue
			}
			w = v
		}
		switch {
		case w == "" || strings.ContainsAny(w, " \t\n"):
		case strings.HasPrefix(w, "http://"), strings.HasPrefix(w, "https://"), strings.HasPrefix(w, "ftp://"):
		case reScriptFile.MatchString(w):
		case strings.ContainsAny(w, "./"):
			out = append(out, w)
		}
	}
	return out
}

// rePrerequisiteNear matches the phrasings a tool uses to say that something
// had to happen first. Suppression needs one of these: a command name on its
// own appears in URLs, in arguments the tool is rejecting, and in help hints
// pointing somewhere else entirely, and none of those is the failing line
// saying it depended on anything.
var rePrerequisiteNear = regexp.MustCompile(
	`(?i)\b(run|running|execute|invoke|call|use|do|try)\b[^.\n]{0,40}$|` +
		`(?i)\b(requires?|required|need(s|ed)?|must|should|first|before|` +
		`not initiali[sz]ed|no such|missing)\b[^.\n]{0,40}$`)

// reAdvisory matches the tail of a sentence that is offering help rather than
// naming a prerequisite. "Try tool help for usage" tells the reader where to
// look next; it does not say the failing line needed that command to run.
var reAdvisory = regexp.MustCompile(`(?i)\b(try|see|for (usage|help|more)|--help|documentation)\b`)

// citesAsPrerequisite reports whether the output names cmd as something that
// had to run first, rather than merely containing its text. The name has to
// stand as whole words, and the words leading up to it have to be making a
// demand rather than a suggestion.
func citesAsPrerequisite(output, cmd string) bool {
	for _, line := range strings.Split(output, "\n") {
		for _, idx := range wholeWordIndexes(line, cmd) {
			before := line[:idx]
			if reAdvisory.MatchString(before) {
				continue
			}
			if rePrerequisiteNear.MatchString(before) {
				return true
			}
		}
	}
	return false
}

// wholeWordIndexes returns every offset in line where cmd appears bounded by
// non-word characters, so a name inside a URL path or a longer token does not
// count as the output naming that command.
func wholeWordIndexes(line, cmd string) []int {
	var out []int
	for off := 0; ; {
		i := strings.Index(line[off:], cmd)
		if i < 0 {
			return out
		}
		i += off
		off = i + 1
		if i > 0 && isCmdChar(line[i-1]) {
			continue
		}
		if end := i + len(cmd); end < len(line) && isCmdChar(line[end]) {
			continue
		}
		out = append(out, i)
	}
}

// isCmdChar reports whether a byte can sit inside a command token, so a
// boundary check knows what counts as touching one. A slash counts, since a
// path segment spelling a command name is a path and not an invocation.
func isCmdChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '-', b == '_', b == '.', b == '/':
		return true
	}
	return false
}

// citedSkipped returns the first skipped command a failure's output names as
// something it needed first.
func citedSkipped(output string, skippedCmds []string) string {
	for _, c := range skippedCmds {
		if citesAsPrerequisite(output, c) {
			return c
		}
	}
	return ""
}

// earlierGapInFamily returns the command of an earlier line, in the same
// binary and subcommand family, that the document's own hole stopped.
//
// This rule used to accept any earlier skip, and its own comment conceded that
// "a second invocation of a subcommand is usually independent of the first".
// It was: `tool build --debug` being skipped says nothing about why
// `tool build --release` failed, and suppressing on that turned a reported
// break into an unsettled one. A gap is the case the rule was written for and
// the only one it can carry. A gap means a documented step never ran because
// the document never supplied what it needed, so the next command in the same
// family failing is the same hole surfacing twice, and reporting it once is
// the point. A skip is kibble's own choice not to run something, which is a
// fact about kibble and not about the document's sequence.
func earlierGapInFamily(prior []lineResult, cmd string, bins map[string]bool) string {
	bin, sub := kplan.InvokedBinary(cmd, bins)
	if bin == "" || sub == "" {
		return ""
	}
	for _, p := range prior {
		if p.Status != StatusGap {
			continue
		}
		if pb, ps := kplan.InvokedBinary(p.Cmd, bins); pb == bin && ps == sub {
			return docblock.Flatten(p.Cmd)
		}
	}
	return ""
}

// namedSiblingNotRun returns a command of the same binary that a failure's
// own output tells the reader to run, when that command never ran in this
// session. A tool naming its own prerequisite is describing session state,
// not a hole in the document: the document did run the equivalent step, and
// the session skipped it for a reason it already reported.
func namedSiblingNotRun(output string, bins map[string]bool, passed map[string]bool) string {
	for bin := range bins {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(bin) + `\s+([a-z][a-z0-9_-]+)`)
		for _, m := range re.FindAllStringSubmatch(output, -1) {
			cmd := bin + " " + m[1]
			if passed[cmd] {
				continue
			}
			// The same standard as a cited skip. A tool that prints its own
			// name in a hint is pointing the reader somewhere, not reporting
			// that the failing line needed that command to have run.
			if !citesAsPrerequisite(output, cmd) {
				continue
			}
			return cmd
		}
	}
	return ""
}
