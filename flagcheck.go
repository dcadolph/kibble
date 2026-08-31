package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Usage is what a README cites for one installed binary: the flags used on
// lines that invoke it, and the subcommands those lines call.
type Usage struct {
	// Flags are the cited flag names, without leading dashes.
	Flags []string
	// Subs are the cited subcommand names.
	Subs []string
	// FlagSub maps a cited flag to the subcommand it was cited on, empty for
	// a flag cited on the bare binary. Without it a flag missing from the
	// collected help cannot be told apart from a flag whose subcommand was
	// never probed, and the second is not drift.
	FlagSub map[string]string
}

var (
	// reFlagToken matches a flag token such as --output-dir or -json. The name
	// must be at least two characters, so ambiguous short flags are ignored.
	reFlagToken = regexp.MustCompile(`(^|\s)(--?[A-Za-z][A-Za-z0-9_-]+)(=|\s|$)`)
	// reSubName matches a plausible subcommand name.
	reSubName = regexp.MustCompile(`^[a-z][a-z0-9_-]+$`)
)

// extractUsage scans a README's code lines and returns the flags and
// subcommands cited for each named binary. Only lines that invoke the binary
// by name are considered, so flags of other tools do not count.
func extractUsage(binaries []string, markdown string) map[string]*Usage {
	byBin := map[string]*Usage{}
	flagSeen := map[string]bool{}
	subSeen := map[string]bool{}
	flagged := map[string]bool{}
	for _, line := range codeLines(markdown) {
		line = stripComment(line)
		for _, seg := range splitSegments(line) {
			fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(seg), "$ "))
			if len(fields) == 0 {
				continue
			}
			bin := fields[0]
			var use *Usage
			for _, b := range binaries {
				if bin == b {
					if byBin[b] == nil {
						byBin[b] = &Usage{}
					}
					use = byBin[b]
					break
				}
			}
			if use == nil {
				continue
			}
			flags := reFlagToken.FindAllStringSubmatch(seg, -1)
			var sub string
			if len(fields) > 1 && reSubName.MatchString(fields[1]) {
				sub = fields[1]
				// A flag on a nested invocation such as `tool walk rotate --x`
				// lives on the nested command, so capture the two-token path
				// and probe its help rather than only the parent's.
				if len(flags) > 0 && len(fields) > 2 && reSubName.MatchString(fields[2]) {
					sub = fields[1] + " " + fields[2]
				}
				if key := bin + "|" + sub; !subSeen[key] {
					subSeen[key] = true
					use.Subs = append(use.Subs, sub)
				}
			}
			if sub != "" && len(flags) > 0 {
				flagged[bin+"|"+sub] = true
			}
			for _, m := range flags {
				name := strings.TrimLeft(m[2], "-")
				if key := bin + "|" + name; !flagSeen[key] {
					flagSeen[key] = true
					use.Flags = append(use.Flags, name)
					if use.FlagSub == nil {
						use.FlagSub = map[string]string{}
					}
					use.FlagSub[name] = sub
				}
			}
		}
	}
	// A flag table documents the binary without invoking it, so its rows are
	// attributed to the repo's single binary when there is exactly one.
	if len(binaries) == 1 {
		bin := binaries[0]
		for _, name := range tableFlags(markdown) {
			if key := bin + "|" + name; !flagSeen[key] {
				flagSeen[key] = true
				if byBin[bin] == nil {
					byBin[bin] = &Usage{}
				}
				byBin[bin].Flags = append(byBin[bin].Flags, name)
			}
		}
	}
	// Subcommands cited with flags come first, so the probe cap never cuts a
	// subcommand whose flags need verifying.
	for bin, use := range byBin {
		sort.SliceStable(use.Subs, func(i, j int) bool {
			return flagged[bin+"|"+use.Subs[i]] && !flagged[bin+"|"+use.Subs[j]]
		})
	}
	return byBin
}

// reTableFlagRow matches a markdown table row whose first cell is a single
// backticked flag, the common shape of a README flag table.
var reTableFlagRow = regexp.MustCompile("^\\|\\s*`(--?[A-Za-z][A-Za-z0-9_-]+)`\\s*\\|")

// tableFlags returns the flags a README documents in markdown tables, so a
// flag table drifts the same way a cited command line does.
func tableFlags(markdown string) []string {
	var out []string
	for _, line := range strings.Split(markdown, "\n") {
		if m := reTableFlagRow.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			out = append(out, strings.TrimLeft(m[1], "-"))
		}
	}
	return out
}

// stripComment removes a trailing shell comment from a code line.
func stripComment(line string) string {
	if i := strings.Index(line, " #"); i >= 0 {
		return line[:i]
	}
	return line
}

// reSegment matches the pipes and command separators that end one simple
// command in a shell line.
var reSegment = regexp.MustCompile(`\|\||&&|;|\|`)

// splitSegments splits a shell line on pipes and command separators, so a
// binary invoked mid-pipeline is still recognized.
func splitSegments(line string) []string {
	return reSegment.Split(line, -1)
}

// helpFlags returns the set of flag names, without dashes, that a binary's
// collected help output advertises.
func helpFlags(helpText string) map[string]bool {
	out := map[string]bool{}
	for _, m := range reFlagToken.FindAllStringSubmatch(helpText, -1) {
		out[strings.TrimLeft(m[2], "-")] = true
	}
	return out
}

// reUsageScreen matches output that is a usage screen rather than a
// complaint. A parser built on the standard library's flag package answers
// --help by printing usage and exiting nonzero, so the exit code alone says
// nothing about whether the subcommand exists.
var reUsageScreen = regexp.MustCompile(`(?im)^\s*(usage|options|flags|arguments|commands):`)

// unknownNamed builds a pattern for a parser saying it does not have the
// subcommand that was probed. Naming matters: a subcommand that exists but
// takes no --help answers "schedule: unknown subcommand \"--help\"", which
// says the argument was wrong, not the subcommand.
func unknownNamed(leaf string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(unknown|invalid|unrecognized|no such) ` +
		`(command|subcommand)[^\n]{0,16}\b` + regexp.QuoteMeta(leaf) + `\b`)
}

// rejectedSub reports whether the binary rejected a subcommand the docs cite.
// A nonzero probe is not enough on its own: a subcommand that answers with its
// own usage screen exists, whatever it exits with. Only a parser saying so, or
// a nonzero probe that printed no usage at all, counts as a rejection.
func rejectedSub(r Result, sub string) bool {
	parts := strings.Fields(sub)
	leaf := parts[len(parts)-1]
	if strings.Contains(r.helpText, fmt.Sprintf("unknown command %q", leaf)) {
		return true
	}
	if _, probed := r.subCodes[sub]; !probed {
		return false
	}
	// The exit code alone proves nothing. A subcommand that takes arguments
	// rather than flags exits nonzero on --help while plainly existing, so a
	// rejection has to be a parser naming this subcommand as the unknown one.
	named := unknownNamed(leaf)
	return named.MatchString(r.helpBySub[sub]) || named.MatchString(r.helpText)
}

// flagChecks derives one flag-check result per binary whose install succeeded
// and whose README cites usage. A cited flag missing from every collected
// help screen, or a subcommand the binary rejects, is reported as drift.
func flagChecks(results []Result) []Result {
	var out []Result
	for _, r := range results {
		if r.Step.Usage == nil || (r.Status != StatusPass && r.Status != StatusPassBuild) {
			continue
		}
		check := Result{
			Step: InstallStep{
				Repo: r.Step.Repo, Kind: "flag-check", Binary: r.Step.Binary,
				dir: r.Step.dir, readme: r.Step.readme,
			},
		}
		known := helpFlags(r.helpText)
		if len(known) == 0 {
			check.Status = StatusSkipped
			check.Detail = "no help output to check against"
			out = append(out, check)
			continue
		}
		var missing, unverifiable []string
		for _, f := range r.Step.Usage.Flags {
			if known[f] {
				continue
			}
			// A flag cited on a subcommand kibble never got help for is
			// unverifiable, not missing, and so is one a flag table lists
			// without ever invoking it. Reporting either as drift blames the
			// document for a screen the probe did not collect.
			owner, attributed := r.Step.Usage.FlagSub[f]
			if !attributed || (owner != "" && !helpfulSub(r, owner)) {
				unverifiable = append(unverifiable, "--"+f)
				continue
			}
			missing = append(missing, "--"+f)
		}
		sort.Strings(unverifiable)
		var badSubs []string
		for _, s := range r.Step.Usage.Subs {
			if rejectedSub(r, s) {
				badSubs = append(badSubs, s)
			}
		}
		sort.Strings(missing)
		sort.Strings(badSubs)
		switch {
		case len(missing) == 0 && len(badSubs) == 0:
			check.Status = StatusPass
			check.Detail = fmt.Sprintf("%d cited flags ok, %d subcommands cited",
				len(r.Step.Usage.Flags)-len(unverifiable), len(r.Step.Usage.Subs))
			if len(unverifiable) > 0 {
				check.Detail += fmt.Sprintf(", %d unverified (%s)",
					len(unverifiable), strings.Join(unverifiable, " "))
			}
		default:
			check.Status = StatusDrift
			var parts []string
			if len(missing) > 0 {
				parts = append(parts, "missing "+strings.Join(missing, " "))
			}
			if len(badSubs) > 0 {
				parts = append(parts, "unknown subcommand "+strings.Join(badSubs, " "))
			}
			check.Detail = strings.Join(parts, ", ")
		}
		out = append(out, check)
	}
	return out
}

// helpfulSub reports whether a subcommand's help probe produced a screen worth
// checking a flag against. A probe that never ran, or that answered with
// nothing resembling usage, cannot settle whether a flag exists.
func helpfulSub(r Result, sub string) bool {
	if _, probed := r.subCodes[sub]; !probed {
		return false
	}
	return reUsageScreen.MatchString(r.helpBySub[sub])
}
