package main

import (
	"fmt"
	"strings"
)

// Asking an installed binary what it actually supports. The help screens and
// per-flag probes collected here are what the drift check compares the
// documentation against, and the exit code of a probe is trusted over any
// wording, since one framework's phrasing for an unknown flag is not another's.

// helpProbe returns the script section that collects the binary's help
// screens for flag checking. Every install script leaves the installed binary
// in $bin, so the same probe serves a Go, package, or clone install. Cited
// subcommands are probed too, capped and restricted to safe names so nothing
// unexpected reaches the shell. The list arrives with flag-bearing
// subcommands first, so the cap never drops one whose flags must be verified.
// Each subcommand probe reports its own exit code, which is how a binary that
// rejects a cited subcommand is caught without matching one framework's
// wording for the error.
func helpProbe(step InstallStep) string {
	if step.Usage == nil {
		return ""
	}
	var subs []string
	for _, s := range step.Usage.Subs {
		if len(subs) >= 12 {
			break
		}
		safe := true
		for _, tok := range strings.Fields(s) {
			if !reSubName.MatchString(tok) {
				safe = false
				break
			}
		}
		if safe {
			subs = append(subs, s)
		}
	}
	var b strings.Builder
	b.WriteString("printf '" + markerLead + "KIBBLE-HELP-START\\n'\n")
	b.WriteString(`kh=$(timeout 15 "$bin" --help 2>&1)` + "\n")
	b.WriteString(`printf '%s\n' "$kh" | head -n 200` + "\n")
	// The root screen ends here, said out loud. Without this boundary the
	// first probe's output shares a segment with the root screen, and a tool
	// with nothing to probe before its flags once had its whole help corpus
	// mistaken for probe output and trimmed away.
	b.WriteString("printf '" + markerLead + "KIBBLE-ROOT-END\\n'\n")
	for _, s := range subs {
		fmt.Fprintf(&b, `kh=$(timeout 15 "$bin" %s --help 2>&1); kc=$?`+"\n", s)
		b.WriteString(`printf '%s\n' "$kh" | head -n 200` + "\n")
		fmt.Fprintf(&b, "printf '"+markerLead+"KIBBLE-SUB %s CODE=%%d\\n' \"$kc\"\n", s)
	}
	// Every cited flag is probed against the binary itself, because a help
	// screen is metadata and a binary can accept flags its help hides. The
	// probe runs `bin <sub> <flag> --help`: an unknown flag makes the parser
	// reject it by name before help can fire, and a hidden but valid one
	// parses cleanly. Only that named rejection convicts, so a screen that
	// omits a working flag can no longer turn into an accusation.
	for _, f := range probeFlags(step.Usage) {
		flag := f.dashes + f.name
		args := flag
		if f.owner != "" {
			args = f.owner + " " + flag
		}
		fmt.Fprintf(&b, `kh=$(timeout 10 "$bin" %s --help 2>&1); kc=$?`+"\n", args)
		b.WriteString(`printf '%s\n' "$kh" | head -n 40` + "\n")
		fmt.Fprintf(&b, "printf '"+markerLead+"KIBBLE-FLAG %s CODE=%%d\\n' \"$kc\"\n", flag)
	}
	b.WriteString("printf '" + markerLead + "KIBBLE-HELP-END\\n'\n")
	return b.String()
}

// probedFlag names one cited flag and the subcommand it was cited on.
type probedFlag struct {
	// name is the flag without dashes.
	name string
	// dashes is the dash prefix the flag was written with, so a single-dash
	// bundle is probed as written rather than as a long flag.
	dashes string
	// owner is the citing subcommand, empty for the bare binary.
	owner string
}

// probeFlags returns the cited flags safe to hand to the shell, capped so a
// flag-heavy document cannot stretch the probe unboundedly.
func probeFlags(u *Usage) []probedFlag {
	var out []probedFlag
	for _, f := range u.Flags {
		if len(out) >= 16 {
			break
		}
		if !reSubName.MatchString(strings.ToLower(f)) {
			continue
		}
		owner := u.FlagSub[f]
		safe := owner == ""
		if owner != "" {
			safe = true
			for _, tok := range strings.Fields(owner) {
				if !reSubName.MatchString(tok) {
					safe = false
					break
				}
			}
		}
		if safe {
			out = append(out, probedFlag{name: f, dashes: u.dashOf(f), owner: owner})
		}
	}
	return out
}
