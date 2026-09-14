package main

import (
	"regexp"
	"sort"
	"strings"

	"github.com/dcadolph/kibble/internal/shell"
)

// Overrides and inference about what a line invokes: the repository owner's
// .kibble.yml rules, and the guess at which binary a document is about when
// the package name and the command name differ.

// substituted applies the configured substitutions to a logical line, in a
// stable order so overlapping substitutions behave the same on every run.
func (pl *planner) substituted(line string) string {
	if pl.cfg == nil {
		return line
	}
	keys := make([]string, 0, len(pl.cfg.Substitutions))
	for k := range pl.cfg.Substitutions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, from := range keys {
		line = strings.ReplaceAll(line, from, pl.cfg.Substitutions[from])
	}
	return line
}

// applyRules applies matching .kibble.yml step rules to a prepared line.
// Rules run in order and each field of a matching rule wins over the
// planner's own judgment.
func (pl *planner) applyRules(line *PlanLine, step *PlanStep, flat string) {
	if pl.cfg == nil {
		return
	}
	for _, rule := range pl.cfg.Steps {
		if !pl.ruleSelects(rule, flat) {
			continue
		}
		if rule.Skip != "" {
			line.Skip = rule.Skip
			line.Gap = false
		}
		if rule.Run {
			line.Skip = ""
		}
		if rule.NonzeroOK {
			line.NonzeroOK = true
		}
		if rule.Background {
			step.Background = true
		}
		if rule.ReadyLog != "" {
			step.ReadyLog = rule.ReadyLog
		}
	}
}

// ruleSelects reports whether a configured rule applies to a line. Binary and
// subcommand are compared against what the line actually invokes, so a rule
// naming a tool cannot be triggered by that tool's name appearing in an
// argument or a path. Match compares whole words, so `tool run` no longer
// selects `tool run-production`, which is the kind of accident a rule written
// for one example used to have on every other example that shared a prefix.
func (pl *planner) ruleSelects(rule StepRule, flat string) bool {
	if rule.Binary != "" {
		bin, sub := invokedBinary(flat, map[string]bool{rule.Binary: true})
		if bin != rule.Binary {
			return false
		}
		if rule.Subcommand != "" && sub != rule.Subcommand {
			return false
		}
	}
	if rule.Match != "" && !shell.MatchesWords(flat, rule.Match) {
		return false
	}
	return rule.Binary != "" || rule.Match != ""
}

// docBinaryFloor is how many example lines must start with the same unknown
// command before kibble treats it as the tool the README documents.
const docBinaryFloor = 3

// reBinaryName matches the shape of a command-line tool's name. Binaries are
// lowercase by convention, which is what separates a real invocation from
// captured program output such as a benchmark's `Benchmark 1: ...` line.
var reBinaryName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// notDocumentedBinary are words that begin a command line without naming the
// tool a README is about: shell builtins and keywords, privilege wrappers, and
// the package managers that install tools rather than being one.
var notDocumentedBinary = map[string]bool{
	"sudo": true, "alias": true, "eval": true, "set": true, "unset": true,
	"local": true, "exit": true, "return": true, "exec": true, "trap": true,
	"read": true, "shift": true, "wait": true, "kill": true, "if": true,
	"then": true, "else": true, "elif": true, "fi": true, "for": true,
	"while": true, "do": true, "done": true, "case": true, "esac": true,
	"function": true, "time": true, "command": true, "type": true,
	"apt": true, "apt-get": true, "yum": true, "dnf": true, "pacman": true,
	"brew": true, "snap": true, "nix": true, "port": true, "scoop": true,
	"choco": true, "winget": true, "docker": true, "gem": true, "pipx": true,
}

// englishStopwords are common words that begin prose sentences inside code
// blocks, so binary inference does not mistake a repeated "the" or "if" for
// the tool the README documents.
var englishStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "this": true, "that": true, "these": true,
	"those": true, "it": true, "is": true, "are": true, "was": true, "be": true,
	"if": true, "then": true, "or": true, "and": true, "but": true, "to": true,
	"for": true, "in": true, "on": true, "of": true, "with": true, "you": true,
	"your": true, "we": true, "our": true, "will": true, "can": true, "note": true,
	"see": true, "here": true, "now": true, "all": true, "no": true, "yes": true,
}

// documentedBinary returns the command a README repeatedly invokes that is
// neither a shell builtin nor an already-known binary. A package seldom names
// its binary, as ripgrep provides rg, and the docs themselves are the most
// reliable statement of what the tool is called. The guess only widens which
// blocks are considered; the session verifies the binary exists before running
// anything, so a wrong guess costs coverage rather than correctness.
func documentedBinary(markdown string, known map[string]bool) string {
	counts := map[string]int{}
	knownSeen := false
	for _, block := range codeBlocks(markdown) {
		if block.Span || !shellLangs[block.Lang] {
			continue
		}
		for _, ln := range logicalLines(prepareLines(block.Lines)) {
			flat := strings.TrimSpace(flatten(ln))
			if flat == "" || strings.HasPrefix(flat, "#") {
				continue
			}
			first := strings.Fields(flat)[0]
			if known[first] {
				knownSeen = true
				continue
			}
			if knownCommands[first] || notDocumentedBinary[first] || englishStopwords[first] {
				continue
			}
			if commandEcosystem[first] != "" || !reBinaryName.MatchString(first) {
				continue
			}
			counts[first]++
		}
	}
	// When a documented binary already appears as a command, the real tool name
	// is confirmed and no guess is needed. Inference is only for the case where
	// the package installs a differently named binary that the docs invoke, as
	// ripgrep provides rg, and the package name never appears as a command.
	if knownSeen {
		return ""
	}
	best, bestCount := "", 0
	for name, n := range counts {
		if n > bestCount || (n == bestCount && name < best) {
			best, bestCount = name, n
		}
	}
	if bestCount < docBinaryFloor {
		return ""
	}
	return best
}

// invokedBinary returns the documented binary a line invokes and its first
// subcommand, or empty strings when the line invokes none. Leading VAR=value
// prefixes are stepped over, so `KEY=x tool sub` still names the tool.
func invokedBinary(flat string, binaries map[string]bool) (string, string) {
	line, ok := shell.Parse(flat)
	if !ok {
		return "", ""
	}
	for _, c := range line.Cmds {
		// Assignment prefixes are not words, so the parser has already set
		// them aside and `KEY=x tool sub` names the tool without stepping.
		if !binaries[c.Name()] {
			continue
		}
		sub := ""
		if a := c.Arg(0); reSubName.MatchString(a) {
			sub = a
		}
		return c.Name(), sub
	}
	return "", ""
}
