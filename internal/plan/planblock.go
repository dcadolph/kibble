package plan

import (
	"regexp"
)

// Reading a document into candidate commands: which fences are shell, how a
// block's physical lines become logical ones, and how a logical line is
// rendered for analysis. Nothing here decides whether a line runs.

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
	// reNonzeroNote matches a comment that documents a nonzero exit.
	reNonzeroNote = regexp.MustCompile(`(?i)non-?zero|exits? [1-9]|fails? (if|when)`)
	// reFileArg matches a whole token that names a relative file with a
	// known extension, so missing example files are caught before they run.
	// A space belongs in the character class because the planner reads a line
	// with a shell parser: `tool read "my notes.md"` arrives as one word, and
	// a pattern that stopped at the space would report nothing missing.
	reFileArg = regexp.MustCompile(
		`^\.?/?[\w][\w ./+-]*\.(md|txt|yaml|yml|json|csv|ics|toml|ini|env|conf|cfg|xml|html|wav|png|jpg|gif|svg|pdf|ipynb|log|sql|proto|rb|py|js|go|rs|ts|tsx|jsx|c|h|cpp|hpp|java|kt|swift|sh|pl|lua|zig|pem|crt|key|der)$`)
	// reDotSlashArg matches a whole ./-prefixed path token of any shape.
	reDotSlashArg = regexp.MustCompile(`^\./[\w][\w ./+-]*$`)
	// reHomeFileArg matches a ~/-prefixed file token with a known extension, so
	// a documented read of a home config the docs never create is skipped
	// rather than failed. The home directory is outside the repo, so such a
	// file is never faked, only reported as absent.
	reHomeFileArg = regexp.MustCompile(
		`^~/[\w.][\w ./+-]*\.(md|txt|yaml|yml|json|toml|ini|conf|cfg|env|sh|rc)$`)
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
	// reSimpleWord matches a bare command word.
	reSimpleWord = regexp.MustCompile(`^[A-Za-z][\w.+-]*$`)
)

// CreatesToken reports whether a line produces the named path: a redirect
// target, an -o argument, or an argument to mkdir, touch, cp or mv. The
// session writer asks this to know which files a step will bring into being.
func CreatesToken(flat string) [][]string {
	return reCreatedToken.FindAllStringSubmatch(flat, -1)
}

// IsSimpleWord reports whether a token is a bare command word.
func IsSimpleWord(s string) bool { return reSimpleWord.MatchString(s) }
