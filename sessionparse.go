package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Reading the session's output back into per-line outcomes. The marker
// protocol is fragile by nature, since documented commands print whatever
// they like, so the parsing is deliberately tolerant about where a marker sits
// and strict about what counts as one.

// lineOutcome is one parsed KIBBLE-LINE marker with the output that
// preceded it.
type lineOutcome struct {
	// code is the exit code, or -1 for a planned skip marker.
	code int
	// output is the text the line printed before its marker.
	output string
	// background marks a line still running when its step was judged, so it
	// never produced an exit code and ready carries what is known instead.
	background bool
	// ready reports whether the step reached its documented readiness signal.
	ready bool
}

// markerTail splits a line on a session marker, returning the output that
// preceded the marker, the text that followed it, and whether the marker is
// present. Markers are found anywhere in a line rather than only at its
// start, so a documented command whose output ends without a newline cannot
// hide the marker the session printed next.
func markerTail(line, marker string) (before, after string, ok bool) {
	i := strings.Index(line, marker)
	if i < 0 {
		return "", "", false
	}
	return line[:i], line[i+len(marker):], true
}

// classifyExample parses session output into a Result: per-line outcomes
// feed step results, and the worst outcome names the repo's example status.
func classifyExample(step InstallStep, plan *Plan, out string, wrapped map[string]bool,
	dur, lineBudget time.Duration) Result {
	res := Result{Step: step, Duration: dur}
	outcomes := map[string]lineOutcome{}
	have := map[string]bool{}
	var chunk []string
	aborted, done, noBin := false, false, false
	pkgCode := 0
	keep := func(s string) {
		if strings.TrimSpace(s) != "" && len(chunk) < 200 {
			chunk = append(chunk, s)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		switch pre, rest, ok := markerTail(line, "KIBBLE-HAVE "); {
		case strings.Contains(line, "KIBBLE-NOBIN"):
			noBin = true
		case ok:
			keep(pre)
			have[strings.TrimSpace(rest)] = true
		case strings.Contains(line, "KIBBLE-PKGS CODE="):
			_, code, _ := markerTail(line, "KIBBLE-PKGS CODE=")
			pkgCode, _ = strconv.Atoi(strings.TrimSpace(code))
			chunk = nil
		case strings.Contains(line, "KIBBLE-BUILD CODE="):
			chunk = nil
		case strings.Contains(line, "KIBBLE-ABORT"):
			aborted = true
		case strings.Contains(line, "KIBBLE-STEP "):
			chunk = nil
		case strings.Contains(line, "KIBBLE-DONE"):
			done = true
		case reLineMarker.MatchString(line):
			m := reLineMarker.FindStringSubmatch(line)
			keep(line[:len(line)-len(m[0])])
			o := lineOutcome{code: -1, output: strings.Join(chunk, "\n")}
			switch {
			case m[3] != "":
				o.code, _ = strconv.Atoi(m[3])
			case m[4] != "":
				ready, _ := strconv.Atoi(m[4])
				o.background = true
				o.ready = ready == 0
			}
			outcomes[m[1]+":"+m[2]] = o
			chunk = nil
		default:
			keep(line)
		}
	}
	if aborted {
		res.Status = StatusSkipped
		res.Detail = "documented install failed in the session; examples not run"
		return res
	}
	if noBin {
		res.Status = StatusSkipped
		res.Detail = "installed tool is not on PATH under any documented name; examples not run"
		return res
	}
	run, worst, detail := buildOutcomes(plan, outcomes, wrapped, done, have, lineBudget)
	res.example = run
	res.Status = worst
	res.Detail = detail
	if pkgCode != 0 && res.Status == StatusVerified {
		res.Detail += "; package install failed"
	}
	return res
}

// buildOutcomes walks the plan against the recorded markers, resolves
// failures that only depend on skipped lines, and returns the per-step
// outcomes with the aggregate status and its summary detail.
func buildOutcomes(plan *Plan, outcomes map[string]lineOutcome, wrapped map[string]bool,
	done bool, have map[string]bool, lineBudget time.Duration) (*exampleRun, Status, string) {
	run := &exampleRun{}
	documented := documentedSettings(plan)
	ended := false
	for _, s := range plan.Steps {
		es := exampleStep{ID: s.ID, Heading: s.Heading}
		for i, l := range s.Lines {
			key := fmt.Sprintf("%s:%d", s.ID, i)
			lr := lineResult{Cmd: flatten(l.Cmd), Code: -1, Line: l.Line, Synthetic: l.Synthetic}
			o, seen := outcomes[key]
			switch {
			case l.Skip != "":
				lr.Status = StatusSkipped
				if l.Gap {
					lr.Status = StatusGap
				}
				lr.Reason = l.SkipReason
				lr.Detail = l.Skip
			case !seen && (ended || done):
				// The session stopped before reaching this line. Kibble chose
				// nothing here, so the line is unestablished rather than
				// skipped: a run that spent its budget early must not report
				// the lines it never reached as deliberate.
				lr.Status = StatusBlocked
				lr.Reason = ReasonDependsOnSkipped
				lr.Detail = "not run: the session ended before reaching it"
			case !seen:
				lr.Status = StatusTimeout
				lr.Detail = "session ended while this line ran"
				ended = true
			default:
				lr = classifyLineResult(lr, l, o, wrapped[key], documented, lineBudget)
			}
			es.Lines = append(es.Lines, lr)
		}
		run.Steps = append(run.Steps, es)
	}
	resolveMissingBinaries(run, plan, have)
	resolveDependentFailures(run, plan)
	status, detail := summarize(run)
	return run, status, detail
}

// summarize reduces per-line outcomes to the aggregate status and detail:
// the first failure names the broken line, a timeout names the hang, a pass
// counts coverage, and a run with nothing to do says why. Blocked lines are
// counted and reported but do not outrank a pass, since a session that
// verified lines did verify them; what they must never do is disappear, so
// the count travels with every summary that has one.
func summarize(run *exampleRun) (Status, string) {
	ran, skipped, gaps, blocked, synthetic := 0, 0, 0, 0, 0
	var firstFail, firstTimeout, firstSkip, firstGap, firstBlocked string
	for _, s := range run.Steps {
		for _, l := range s.Lines {
			switch l.Status {
			case StatusVerified:
				ran++
				if len(l.Synthetic) > 0 {
					synthetic++
				}
			case StatusSkipped:
				skipped++
				if firstSkip == "" {
					firstSkip = l.Detail
				}
			case StatusBlocked:
				blocked++
				if firstBlocked == "" {
					firstBlocked = fmt.Sprintf("%s %q %s", s.ID, l.Cmd, l.Detail)
				}
			case StatusGap:
				gaps++
				if firstGap == "" {
					firstGap = fmt.Sprintf("%s %q %s", s.ID, l.Cmd, l.Detail)
				}
			case StatusTimeout:
				if firstTimeout == "" {
					firstTimeout = fmt.Sprintf("%s %q %s", s.ID, l.Cmd, l.Detail)
				}
			case StatusFail:
				if firstFail == "" {
					firstFail = fmt.Sprintf("%s %q %s", s.ID, l.Cmd, l.Detail)
				}
			}
		}
	}
	tally := fmt.Sprintf("%d lines ran, %d skipped", ran, skipped)
	if blocked > 0 {
		tally += fmt.Sprintf(", %d blocked", blocked)
	}
	if synthetic > 0 {
		// Named on the summary line rather than only in the JSON, since the
		// summary is what a reader actually reads before believing the green.
		tally += fmt.Sprintf(" (%d against fabricated files)", synthetic)
	}
	switch {
	case firstFail != "":
		return StatusFail, firstFail
	case firstTimeout != "":
		return StatusTimeout, firstTimeout
	case gaps > 0:
		return StatusGap, fmt.Sprintf("%d %s, first: %s",
			gaps, plural(gaps, "documentation gap", "documentation gaps"), firstGap)
	case ran > 0:
		return StatusVerified, tally
	case blocked > 0:
		// Nothing was verified and something was tried without settling. The
		// session has no verdict on this document and must not imply one.
		return StatusBlocked, fmt.Sprintf("%s, first: %s", tally, firstBlocked)
	default:
		detail := "no lines runnable"
		if firstSkip != "" {
			detail += ": " + firstSkip
		}
		return StatusSkipped, detail
	}
}

// plural picks the singular or plural word for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
