package main

import (
	"regexp"
	"strings"

	"github.com/dcadolph/kibble/internal/shell"
)

// Reading a document into candidate commands: which fences are shell, how a
// block's physical lines become logical ones, and how a logical line is
// rendered for analysis. Nothing here decides whether a line runs.

// shellLangs are fence languages treated as shell recipes. The empty string
// covers indented blocks and fences with no info string.
var shellLangs = map[string]bool{
	"": true, "sh": true, "bash": true, "shell": true, "zsh": true,
	"console": true, "shell-session": true, "text": true, "plain": true,
}

// knownCommands are shell commands accepted in example blocks. A block
// qualifies as a recipe only when every line starts with a known command, a
// documented binary, a package tool, or a variable assignment, so prose and
// non-shell snippets never reach the executor.
var knownCommands = map[string]bool{
	"echo": true, "printf": true, "export": true, "cd": true, "mkdir": true,
	"cp": true, "mv": true, "rm": true, "cat": true, "tee": true,
	"chmod": true, "touch": true, "ls": true, "pwd": true, "which": true,
	"grep": true, "sed": true, "awk": true, "head": true, "tail": true,
	"sort": true, "wc": true, "tar": true, "curl": true, "git": true,
	"go": true, "make": true, "env": true, "sleep": true, "true": true,
	"source": true, "sh": true, "bash": true, "test": true, "date": true,
}

// packageTools maps commands the docs may invoke to the Debian package that
// provides them, for tools the golang base image lacks.
var packageTools = map[string]string{
	"age": "age", "age-keygen": "age", "jq": "jq", "rg": "ripgrep",
	"sqlite3": "sqlite3", "unzip": "unzip", "tree": "tree",
}

var (
	// rePlaceholder matches tokens a reader must replace before running:
	// angle-bracket slots, xxxx runs, path/to/ and /home/user stand-ins,
	// double-brace templates such as a config example's {{.Field | quote}}, and
	// values that trail off in an ellipsis. The ellipsis is ignored after a
	// slash so a Go package pattern such as ./... is not mistaken for one.
	rePlaceholder = regexp.MustCompile(
		`<[^<>\s]+>|=<[^<>]+>|(^|[^${])\{[A-Za-z][A-Za-z0-9_]*\}|(^|\s)\[[^\[\]\s]+\](\s|$)` +
			`|\{\{[^{}]*\}\}` +
			`|\[[^\[\]]*--[^\[\]]*\]|\[[A-Za-z][^\[\]]*\]` +
			`|\bxxxx\b|\*\*\*|\bpath/to/|/(home|Users)/(user|you|me|username|yourname)\b` +
			`|(^|[^/])\.\.\.($|[\s'".])`)
	// reLogin matches a command that starts an interactive sign-in.
	reLogin = regexp.MustCompile(`\b(login|signin|sign-in|logout)\b`)
	// reGitState matches a git invocation that needs history, tags, or a
	// remote. The session replays examples in a freshly initialized repo with
	// no commits and no remotes, so these commands fail there even when the
	// docs are right.
	reGitState = regexp.MustCompile(
		`\bgit\s+(-\S+\s+)*(fetch|pull|push|show|describe|log|rebase|merge|cherry-pick|revert|blame|bisect|shortlog|submodule)\b|\bgit\b[^|;&]*\b(origin|upstream)\b` +
			`|\b(origin|upstream)/[A-Za-z0-9._/-]+`)
	// reLocalhost matches a reference to a service on the local machine,
	// which a clean container does not have.
	reLocalhost = regexp.MustCompile(`\blocalhost\b|127\.0\.0\.1`)
	// reTwoColumn matches a usage line whose command is followed by a prose
	// description column: at least three spaces, then a capitalized sentence.
	reTwoColumn = regexp.MustCompile(`^(.*\S)\s{3,}([A-Z].*)$`)
	// reNonzeroNote matches a comment that documents a nonzero exit.
	reNonzeroNote = regexp.MustCompile(`(?i)non-?zero|exits? [1-9]|fails? (if|when)`)
	// reFileArg matches a whole token that names a relative file with a
	// known extension, so missing example files are caught before they run.
	reFileArg = regexp.MustCompile(
		`^\.?/?[\w][\w./+-]*\.(md|txt|yaml|yml|json|csv|ics|toml|ini|env|conf|cfg|xml|html|wav|png|jpg|gif|svg|pdf|ipynb|log|sql|proto|rb|py|js|go|rs|ts|tsx|jsx|c|h|cpp|hpp|java|kt|swift|sh|pl|lua|zig|pem|crt|key|der)$`)
	// reDotSlashArg matches a whole ./-prefixed path token of any shape.
	reDotSlashArg = regexp.MustCompile(`^\./[\w][\w./+-]*$`)
	// reHomeFileArg matches a ~/-prefixed file token with a known extension, so
	// a documented read of a home config the docs never create is skipped
	// rather than failed. The home directory is outside the repo, so such a
	// file is never faked, only reported as absent.
	reHomeFileArg = regexp.MustCompile(
		`^~/[\w.][\w./+-]*\.(md|txt|yaml|yml|json|toml|ini|conf|cfg|env|sh|rc)$`)
	// reHomePathArg matches any path under the reader's home directory, such
	// as ~/src/project. The container's home holds none of it, and a document
	// citing one is showing the reader where their own work lives.
	reHomePathArg = regexp.MustCompile(`^(~|\$HOME|\$\{HOME\})/[\w.][\w./+-]*$`)
	// reCreatedToken matches a token a line creates: a redirect target, an
	// -o argument, or the arguments of mkdir, touch, cp, or mv.
	reCreatedToken = regexp.MustCompile(
		`>{1,2}\s*([^\s&|;]+)|\s-o\s+(\S+)|--?(?:output|outfile|dest|out|o)[= ]([^\s&|;]+)`)
	// reAssignPrefix matches a leading VAR= or export VAR= assignment and
	// captures the variable name.
	reAssignPrefix = regexp.MustCompile(`^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=`)
	// reHeredoc matches a heredoc start and captures its terminator.
	reHeredoc = regexp.MustCompile(`<<-?\s*['"]?(\w+)['"]?`)
	// reSimpleWord matches a bare command word.
	reSimpleWord = regexp.MustCompile(`^[A-Za-z][\w.+-]*$`)
)

// reShownError matches output a doc displays under a command to demonstrate
// it failing, such as a usage screen or an error message.
var reShownError = regexp.MustCompile(
	`^(USAGE:|usage:|[Ee]rror[: ])` +
		`|\b(is not allowed|not supported|cannot be|could not|unable to|` +
		`failed to|invalid |unknown |no such |must be |not permitted|` +
		`is required|not recognized)`)

// shownFailures returns the commands of a prompted block whose displayed
// output is an error. Docs sometimes show a command failing on purpose, to
// teach why the corrected form that follows is needed, and the demonstrated
// failure exiting nonzero is the document working as written.
func shownFailures(block codeBlock) map[string]bool {
	out := map[string]bool{}
	current := ""
	for _, raw := range block.Lines {
		t := strings.TrimSpace(raw)
		if strings.HasPrefix(t, "$ ") {
			current = strings.TrimSpace(strings.TrimPrefix(t, "$ "))
			continue
		}
		if current != "" && reShownError.MatchString(t) {
			out[current] = true
		}
	}
	return out
}

// sourceLineIndex returns a lookup from a prepared logical line back to its
// 1-based README line. The prepared line may have lost a prompt marker or
// gained continuation lines, so the match compares the first physical line
// against each raw block line with the prompt stripped. Unmatched lines fall
// back to the block's first line, and 0 means the block's position is unknown.
func sourceLineIndex(block codeBlock) func(string) int {
	return func(ln string) int {
		if block.Line == 0 {
			return 0
		}
		first := strings.TrimSpace(ln)
		if i := strings.Index(first, "\n"); i >= 0 {
			first = strings.TrimSpace(first[:i])
		}
		for i, raw := range block.Lines {
			raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "$ "))
			if raw == first {
				return block.Line + i
			}
		}
		return block.Line
	}
}

// prepareLines normalizes a block's raw lines: prompt-style blocks keep only
// the prompted lines, and two-column usage blocks drop the prose column.
func prepareLines(raw []string) []string {
	prompted := false
	for _, l := range raw {
		if strings.HasPrefix(strings.TrimSpace(l), "$ ") {
			prompted = true
			break
		}
	}
	if prompted {
		var out []string
		for _, l := range raw {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "$ ") {
				out = append(out, strings.TrimPrefix(t, "$ "))
			}
		}
		return out
	}
	if twoColumn(raw) {
		var out []string
		for _, l := range raw {
			if m := reTwoColumn.FindStringSubmatch(l); m != nil && commandColumn(m[1]) {
				out = append(out, m[1])
				continue
			}
			out = append(out, l)
		}
		return out
	}
	return raw
}

// twoColumn reports whether a block is a usage table: at least two lines,
// and at least half of the nonempty ones, pair a command with a trailing
// prose description column.
func twoColumn(raw []string) bool {
	total, hits := 0, 0
	for _, l := range raw {
		if strings.TrimSpace(l) == "" {
			continue
		}
		total++
		if m := reTwoColumn.FindStringSubmatch(l); m != nil && commandColumn(m[1]) {
			hits++
		}
	}
	return hits >= 2 && hits*2 >= total
}

// commandColumn reports whether a two-column split left a command the shell
// can actually read, so the split did not cut through a quoted string. It
// used to count quote characters, which called `echo "it's fine"` unbalanced
// for the apostrophe and `echo "a" "b` balanced for the even count.
func commandColumn(s string) bool {
	return shell.Parses(s)
}

// logicalLines groups physical lines into logical commands: a trailing
// backslash joins the next line, and a heredoc runs to its terminator.
func logicalLines(raw []string) []string {
	var out []string
	for i := 0; i < len(raw); i++ {
		line := raw[i]
		for strings.HasSuffix(strings.TrimRight(line, " \t"), `\`) && i+1 < len(raw) {
			i++
			line += "\n" + raw[i]
		}
		if m := reHeredoc.FindStringSubmatch(line); m != nil {
			for i+1 < len(raw) {
				i++
				line += "\n" + raw[i]
				if strings.TrimSpace(raw[i]) == m[1] {
					break
				}
			}
		}
		out = append(out, line)
	}
	return out
}

// flatten renders a logical line as one analyzable string: continuations
// collapse to spaces and only a heredoc's first line is kept, since the
// body is data rather than commands.
func flatten(logical string) string {
	lines := strings.Split(logical, "\n")
	if reHeredoc.MatchString(lines[0]) {
		return lines[0]
	}
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(strings.TrimRight(l, " \t"), `\`)
	}
	return strings.TrimSpace(strings.Join(lines, " "))
}

// commandHead returns the command portion of a line, without a trailing
// comment, so placeholder checks ignore prose in comments.
func commandHead(flat string) string {
	return strings.TrimSpace(stripComment(flat))
}

// trailingComment returns the trailing shell comment of a line, or empty.
func trailingComment(flat string) string {
	if i := strings.Index(flat, " #"); i >= 0 {
		return flat[i:]
	}
	return ""
}

// proseOnly returns a document with its fenced code blocks removed, so a rule
// about what a document says is not answered by what it demonstrates.
func proseOnly(markdown string) string {
	var b strings.Builder
	fenced := false
	for _, line := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if !fenced {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
