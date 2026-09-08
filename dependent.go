package main

import (
	"fmt"
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
func resolveMissingBinaries(run *exampleRun, plan *Plan, have map[string]bool) {
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
			if bin, _ := invokedBinary(l.Cmd, bins); bin != "" && !have[bin] {
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
func resolveDependentFailures(run *exampleRun, plan *Plan) {
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
			if bin, sub := invokedBinary(l.Cmd, bins); bin != "" && sub != "" {
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
			if bin, sub := invokedBinary(l.Cmd, bins); bin != "" && sub != "" {
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
			if prior := earlierSkipInFamily(s.Lines[:li], l.Cmd, bins); prior != "" {
				l.Status = StatusBlocked
				l.Reason = ReasonDependsOnSkipped
				l.Detail = fmt.Sprintf("failed after `%s` did not run", prior)
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

// citedSkipped returns the first skipped command a failure's output names.
func citedSkipped(output string, skippedCmds []string) string {
	for _, c := range skippedCmds {
		if strings.Contains(output, c) {
			return c
		}
	}
	return ""
}

// earlierSkipInFamily returns the command of an earlier skipped line that
// shares the failing line's binary and subcommand, or empty when none does.
func earlierSkipInFamily(prior []lineResult, cmd string, bins map[string]bool) string {
	bin, sub := invokedBinary(cmd, bins)
	if bin == "" || sub == "" {
		return ""
	}
	for _, p := range prior {
		if p.Status != StatusSkipped && p.Status != StatusGap {
			continue
		}
		if pb, ps := invokedBinary(p.Cmd, bins); pb == bin && ps == sub {
			return flatten(p.Cmd)
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
			if !passed[cmd] {
				return cmd
			}
		}
	}
	return ""
}
