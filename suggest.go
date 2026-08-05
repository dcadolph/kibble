package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// suggestSystem is the advisor's brief. It is deliberately narrow: the model
// classifies documented lines and never decides whether anything passed.
const suggestSystem = `You help configure kibble, a tool that runs a README's documented commands in a
clean Linux container to check the docs still work.

You will get the repository name and a list of documented command lines that kibble could
not classify on its own. For each line, decide which single category fits:

  run       the line should run as written and is expected to succeed
  nonzero   the line runs, but exiting nonzero is its documented behavior, as a linter or
            scanner that fails when it finds something
  skip      the line cannot honestly run in a clean container with no credentials, no
            terminal, no network services, and only the repository's own files

Judge only from the line and its section heading. Prefer skip when unsure, because a
wrong run costs a false failure while a wrong skip only costs coverage.

Reply with JSON only, no prose and no code fence:
{"lines":[{"cmd":"<the line verbatim>","verdict":"run|nonzero|skip","reason":"<short reason>"}]}

The reason is one clause a maintainer will read in a config file. Do not restate the
command. For skip, say what the container lacks.`

// suggestion is one classified line returned by the advisor.
type suggestion struct {
	// Cmd is the documented line, echoed back for matching.
	Cmd string `json:"cmd"`
	// Verdict is run, nonzero, or skip.
	Verdict string `json:"verdict"`
	// Reason is the short justification written into the config.
	Reason string `json:"reason"`
}

// suggestReply is the advisor's whole answer.
type suggestReply struct {
	// Lines are the classified lines.
	Lines []suggestion `json:"lines"`
}

// candidate is a documented line kibble wants a second opinion on, carried
// with the heading that gives it context.
type candidate struct {
	// Cmd is the documented line.
	Cmd string
	// Heading is the section the line appears under.
	Heading string
	// Skip is kibble's own reason for skipping, empty when it planned to run.
	Skip string
}

// suggestCandidates returns the lines worth asking about: every line kibble
// skipped for a reason a maintainer might overrule, and every line it planned
// to run whose exit code it cannot predict. Lines skipped for reasons that are
// certain, such as a placeholder the reader must fill in, are left alone.
func suggestCandidates(plan *Plan) []candidate {
	var out []candidate
	seen := map[string]bool{}
	for _, step := range plan.Steps {
		for _, line := range step.Lines {
			cmd := flatten(line.Cmd)
			if cmd == "" || seen[cmd] {
				continue
			}
			seen[cmd] = true
			if line.Skip == "" && !line.NonzeroOK {
				out = append(out, candidate{Cmd: cmd, Heading: step.Heading})
				continue
			}
			if line.Skip != "" && !certainSkip(line.Skip) {
				out = append(out, candidate{Cmd: cmd, Heading: step.Heading, Skip: line.Skip})
			}
		}
	}
	return out
}

// certainSkipReasons are skip reasons the engine is sure about, so a model is
// never asked to second-guess them.
var certainSkipReasons = []string{
	"placeholder", "never create", "which the docs never set", "interactive sign-in",
	"another shell", "kernel interfaces", "only the reader's system",
	"which was skipped", "session ended", "follows a skipped cd",
}

// certainSkip reports whether a skip reason is one the engine decides on its
// own evidence rather than a judgment call.
func certainSkip(reason string) bool {
	for _, r := range certainSkipReasons {
		if strings.Contains(reason, r) {
			return true
		}
	}
	return false
}

// askAdvisor sends the candidates and returns the advisor's classifications,
// keyed by the command line. A reply that does not parse is an error, never a
// silent empty result.
func askAdvisor(ctx context.Context, a Advisor, repo string, cands []candidate) (
	map[string]suggestion, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s\n\nLines:\n", repo)
	for _, c := range cands {
		fmt.Fprintf(&b, "- cmd: %s\n", c.Cmd)
		if c.Heading != "" {
			fmt.Fprintf(&b, "  section: %s\n", c.Heading)
		}
		if c.Skip != "" {
			fmt.Fprintf(&b, "  kibble would skip because: %s\n", c.Skip)
		}
	}
	reply, err := a.Chat(ctx, suggestSystem, b.String())
	if err != nil {
		return nil, err
	}
	obj, err := firstJSONObject(reply)
	if err != nil {
		return nil, err
	}
	var parsed suggestReply
	if err := json.Unmarshal([]byte(obj), &parsed); err != nil {
		return nil, fmt.Errorf("%w: reply was not the requested JSON: %w", ErrAdvisor, err)
	}
	out := map[string]suggestion{}
	for _, s := range parsed.Lines {
		if cmd := strings.TrimSpace(s.Cmd); cmd != "" {
			out[cmd] = s
		}
	}
	return out, nil
}

// writeSuggestedConfig renders the advisor's classifications as a .kibble.yml
// for a human to read and commit. Only lines where the advisor disagrees with
// the engine become rules, so the file stays small and every entry earns its
// place. It reports whether any rule was written.
func writeSuggestedConfig(w io.Writer, repo string, cands []candidate,
	got map[string]suggestion) bool {
	type rule struct {
		// cmd is the documented line the rule matches.
		cmd string
		// verdict is the advisor's classification.
		verdict string
		// reason is the advisor's justification.
		reason string
	}
	var rules []rule
	for _, c := range cands {
		s, ok := got[c.Cmd]
		if !ok {
			continue
		}
		switch s.Verdict {
		case "nonzero":
			rules = append(rules, rule{c.Cmd, "nonzero", s.Reason})
		case "skip":
			if c.Skip == "" {
				rules = append(rules, rule{c.Cmd, "skip", s.Reason})
			}
		case "run":
			if c.Skip != "" {
				rules = append(rules, rule{c.Cmd, "run", s.Reason})
			}
		}
	}
	if len(rules) == 0 {
		return false
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].cmd < rules[j].cmd })

	_, _ = fmt.Fprintf(w, "# .kibble.yml proposed for %s.\n", repo)
	_, _ = fmt.Fprintln(w, "# Every entry is a judgment call kibble's heuristics could not make on their")
	_, _ = fmt.Fprintln(w, "# own. Read each one, delete what you disagree with, and commit the rest.")
	_, _ = fmt.Fprintln(w, "# Once committed, runs stay deterministic: no model is consulted again.")
	_, _ = fmt.Fprintln(w, "version: 1")
	_, _ = fmt.Fprintln(w, "examples:")
	_, _ = fmt.Fprintln(w, "  steps:")
	for _, r := range rules {
		_, _ = fmt.Fprintf(w, "    - match: %s\n", yamlScalar(r.cmd))
		switch r.verdict {
		case "nonzero":
			_, _ = fmt.Fprintln(w, "      nonzeroOk: true")
		case "skip":
			_, _ = fmt.Fprintf(w, "      skip: %s\n", yamlScalar(reasonText(r.reason)))
		case "run":
			_, _ = fmt.Fprintln(w, "      run: true")
		}
		if r.verdict != "skip" && r.reason != "" {
			_, _ = fmt.Fprintf(w, "      # %s\n", oneLine(r.reason))
		}
	}
	return true
}

// reasonText returns a usable skip reason, falling back when the advisor
// gave none, so the config never carries an empty explanation.
func reasonText(reason string) string {
	if r := oneLine(reason); r != "" {
		return r
	}
	return "cannot run in a clean container"
}

// oneLine collapses a reason to a single trimmed line for a config comment.
func oneLine(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// yamlScalar quotes a value for YAML when quoting is needed, so a command
// containing a colon or a quote round-trips.
func yamlScalar(s string) string {
	s = oneLine(s)
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, `:#{}[]&*!|>'"%@`+"`") || strings.HasPrefix(s, " ") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	return s
}
