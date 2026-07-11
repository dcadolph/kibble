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
			var sub string
			if len(fields) > 1 && reSubName.MatchString(fields[1]) {
				sub = fields[1]
				if key := bin + "|" + sub; !subSeen[key] {
					subSeen[key] = true
					use.Subs = append(use.Subs, sub)
				}
			}
			flags := reFlagToken.FindAllStringSubmatch(seg, -1)
			if sub != "" && len(flags) > 0 {
				flagged[bin+"|"+sub] = true
			}
			for _, m := range flags {
				name := strings.TrimLeft(m[2], "-")
				if key := bin + "|" + name; !flagSeen[key] {
					flagSeen[key] = true
					use.Flags = append(use.Flags, name)
				}
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

// stripComment removes a trailing shell comment from a code line.
func stripComment(line string) string {
	if i := strings.Index(line, " #"); i >= 0 {
		return line[:i]
	}
	return line
}

// splitSegments splits a shell line on pipes and command separators, so a
// binary invoked mid-pipeline is still recognized.
func splitSegments(line string) []string {
	return regexp.MustCompile(`\|\||&&|;|\|`).Split(line, -1)
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
			Step: InstallStep{Repo: r.Step.Repo, Kind: "flag-check", Binary: r.Step.Binary},
		}
		known := helpFlags(r.helpText)
		if len(known) == 0 {
			check.Status = StatusSkipped
			check.Detail = "no help output to check against"
			out = append(out, check)
			continue
		}
		var missing []string
		for _, f := range r.Step.Usage.Flags {
			if !known[f] {
				missing = append(missing, "--"+f)
			}
		}
		var badSubs []string
		for _, s := range r.Step.Usage.Subs {
			if strings.Contains(r.helpText, fmt.Sprintf("unknown command %q", s)) {
				badSubs = append(badSubs, s)
			}
		}
		sort.Strings(missing)
		sort.Strings(badSubs)
		switch {
		case len(missing) == 0 && len(badSubs) == 0:
			check.Status = StatusPass
			check.Detail = fmt.Sprintf("%d cited flags ok, %d subcommands cited",
				len(r.Step.Usage.Flags), len(r.Step.Usage.Subs))
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
