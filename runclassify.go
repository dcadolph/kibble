package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Reading a step's container output into a verdict, and pulling the one line
// worth showing out of a build log. What counts as the failure line matters:
// a reader given the wrong line goes looking in the wrong place.

// classify turns container output into a Result.
func classify(step InstallStep, out string, dur time.Duration) Result {
	// Tools colorize their own output, and those escapes are noise everywhere
	// they land: they corrupt a JSON report a caller parses, break column
	// alignment in the table, and turn a CI annotation into gibberish. Kibble
	// adds its own color at render time, so the captured text is stripped once
	// here and every consumer downstream gets clean strings.
	out = stripANSI(out)
	res := Result{Step: step, Duration: dur}
	buildCode, smokeCode := -1, -1
	noBin := false
	inHelp := false
	subCodes := map[string]int{}
	helpBySub := map[string]string{}
	helpByFlag := map[string]string{}
	var tail, help, cur []string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "KIBBLE-HELP-START"):
			inHelp = true
		case strings.HasPrefix(line, "KIBBLE-HELP-END"):
			inHelp = false
		case strings.HasPrefix(line, "KIBBLE-ROOT-END"):
			res.helpRoot = strings.Join(help, "\n")
			cur = nil
		case reSubMarker.MatchString(line):
			m := reSubMarker.FindStringSubmatch(line)
			subCodes[m[1]], _ = strconv.Atoi(m[2])
			helpBySub[m[1]] = strings.Join(cur, "\n")
			cur = nil
		case reFlagMarker.MatchString(line):
			m := reFlagMarker.FindStringSubmatch(line)
			name := strings.TrimLeft(m[1], "-")
			// A probe screen never joins the help corpus: an unknown-flag
			// error quotes the flag, and a corpus holding that quote would
			// count the missing flag as known and blind the check.
			help = help[:len(help)-len(cur)]
			helpByFlag[name] = strings.Join(cur, "\n")
			cur = nil
		case inHelp:
			help = append(help, line)
			cur = append(cur, line)
		case strings.HasPrefix(line, "BUILDCODE="):
			buildCode, _ = strconv.Atoi(strings.TrimPrefix(line, "BUILDCODE="))
		case strings.HasPrefix(line, "SMOKECODE="):
			smokeCode, _ = strconv.Atoi(strings.TrimPrefix(line, "SMOKECODE="))
		case strings.HasPrefix(line, "SMOKELINE="):
			res.SmokeLine = strings.TrimPrefix(line, "SMOKELINE=")
		case strings.HasPrefix(line, "NOBIN="):
			noBin = true
		default:
			if strings.TrimSpace(line) != "" {
				tail = append(tail, line)
			}
		}
	}
	res.helpText = strings.Join(help, "\n")
	if res.helpRoot == "" {
		res.helpRoot = res.helpText
	}
	if len(helpBySub) > 0 {
		res.helpBySub = helpBySub
	}
	if len(helpByFlag) > 0 {
		res.helpByFlag = helpByFlag
	}
	if len(subCodes) > 0 {
		res.subCodes = subCodes
	}
	switch {
	case buildCode == -1:
		res.Status = StatusError
		res.Detail = "kibble could not run the step (container error): " + lastLine(tail)
	case buildCode == 124:
		res.Status = StatusTimeout
		res.Detail = fmt.Sprintf("exceeded timeout after %s", dur.Round(time.Second))
	case buildCode != 0:
		res.Status = StatusFail
		res.Detail = buildFailLine(tail)
	case noBin:
		res.Status = StatusRan
		res.Detail = "recipe exited 0 but produced no binary to smoke-test"
	case smokeCode == 0:
		res.Status = StatusVerified
	case reArchMismatch.MatchString(res.SmokeLine):
		// Only the smoke line convicts. Scanning the whole output once let an
		// arch string anywhere in a build log or help screen relabel a real
		// smoke-test crash as a harmless cross-architecture skip.
		res.Status = StatusCrossArch
		res.SmokeLine = ""
		res.Detail = "installed, but the binary targets another architecture, smoke test not possible here"
	default:
		res.Status = StatusBuilt
		res.Detail = fmt.Sprintf("binary built but smoke exit=%d", smokeCode)
	}
	return res
}

// reSubMarker parses a KIBBLE-SUB marker into the cited subcommand and the
// exit code its help probe returned.
var reSubMarker = regexp.MustCompile(`^KIBBLE-SUB (.+) CODE=(-?\d+)$`)

// reFlagMarker parses a KIBBLE-FLAG marker into the probed flag and the exit
// code the binary answered with.
var reFlagMarker = regexp.MustCompile(`^KIBBLE-FLAG (--?\S+) CODE=(-?\d+)$`)

// reArchMismatch matches the errors a binary built for another architecture
// produces, so an emulation artifact of the host is not reported as the tool
// failing its smoke test.
var reArchMismatch = regexp.MustCompile(`qemu-\w+: |Exec format error|cannot execute binary file`)

// reANSI matches the escape sequences a program writes to color or reposition
// its own output. Both the color form and the wider set of control sequences
// are covered, since a progress bar redrawing itself is as unwelcome in a
// report as a color code.
var reANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\r`)

// stripANSI removes terminal control sequences from captured output.
func stripANSI(s string) string {
	if !strings.ContainsAny(s, "\x1b\r") {
		return s
	}
	return reANSI.ReplaceAllString(s, "")
}

// lastLine returns the final non-empty line, for compact error detail.
func lastLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

// reWrapperSummary matches the closing line a build tool prints after the tool
// it invoked has already reported the real error. It names the target that
// failed and nothing about why.
var reWrapperSummary = regexp.MustCompile(
	`^(make(\[\d+\])?: (\*\*\*|Entering|Leaving)|npm ERR!|yarn ERR|` +
		`error: could not compile|error: failed to compile|error: build failed|` +
		`FAILED:|ninja: build stopped|` +
		`To reuse those artifacts|Blocking waiting for file lock|` +
		`(Compiling|Building|Finished|Downloading|Updating) )`)

// reUsageHeading matches the banner a tool prints above its own usage screen
// when it rejects an argument.
var reUsageHeading = regexp.MustCompile(`(?i)^(usage|options|flags|commands)\b|^usage:`)

// reErrorDeclaration matches the line where a parser says what it refused.
// It is the sentence a reader needs, and it sits at the top of the output,
// above the usage screen a tool prints after it.
var reErrorDeclaration = regexp.MustCompile(
	`(?i)^(error|fatal|panic)\b|flag provided but not defined|` +
		`\b(unknown|unrecognized|invalid|unexpected) (flag|option|command|subcommand|argument)\b|` +
		`\bno such (flag|option|command|subcommand)\b`)

// failureLine returns the most informative line of a failure. Reporting a
// wrapper's summary hides the error a reader needs, so the summary is skipped
// in favor of the last line that says what actually broke. A tool that answers
// a bad argument by printing its whole usage screen is the other direction of
// the same mistake: the last line there is the final flag's description, which
// tells a reader nothing, so an error the parser declared above the screen
// wins over anything below it.
// buildFailLine picks the line that best explains a failed build. A real error
// declaration, such as cargo's "error[E0432]: unresolved import", is preferred
// over a build tool's trailing summary, since the summary names the target
// that failed and nothing about why. It falls back to failureLine when no such
// declaration was captured.
func buildFailLine(lines []string) string {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if reErrorDeclaration.MatchString(line) && !reWrapperSummary.MatchString(line) {
			return line
		}
	}
	return failureLine(lines)
}

func failureLine(lines []string) string {
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if !reErrorDeclaration.MatchString(line) {
			continue
		}
		// The declaration only outranks the tail when a usage screen follows
		// it, since that is the case where the tail is boilerplate.
		for _, rest := range lines[i+1:] {
			if reUsageHeading.MatchString(strings.TrimSpace(rest)) {
				return line
			}
		}
		break
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || reWrapperSummary.MatchString(line) {
			continue
		}
		return line
	}
	return lastLine(lines)
}
