package main

import (
	"fmt"
	"github.com/dcadolph/kibble/internal/docblock"
	kplan "github.com/dcadolph/kibble/internal/plan"
	"regexp"
	"strings"
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
// not having run, since the document's own hole stopped the line.
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
	for si := range run.Steps {
		s := &run.Steps[si]
		for li := range s.Lines {
			l := &s.Lines[li]
			if l.Status != StatusFail {
				continue
			}
			if cited := citedSkipped(l.output, skippedCmds); cited != "" {
				l.Status = StatusBlocked
				l.Reason = ReasonDependsOnSkipped
				l.Detail = fmt.Sprintf("failed naming `%s`, which did not run", cited)
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
			}
		}
	}
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
