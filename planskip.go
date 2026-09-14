package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/dcadolph/kibble/internal/shell"
)

// Why a documented line does not run. Every rule here is a decision kibble
// makes before executing anything, on evidence it already holds: a
// placeholder, another platform, a shell the container is not, a command that
// serves rather than returns.

// skipReason returns why a line cannot run in a clean container, or empty
// when it can. The checks run in order of how specific their reason is.
// Substitutions have already been applied, so a placeholder that survives
// here is one the reader was meant to fill in.
func (pl *planner) skipReason(cmd, flat string) (string, Reason, bool) {
	if rePlaceholder.MatchString(commandHead(flat)) {
		return "docs use a placeholder the reader must fill in", ReasonPlaceholder, false
	}
	// Asked before the rules that read the line as shell, and after the
	// placeholder rule, since `--key <YOUR-KEY>` is an unparseable line whose
	// real reason is the placeholder. The parse is of the line as written,
	// never the flattened form: flattening truncates a heredoc to its opening
	// word, which no parser accepts and which the session never runs. A line
	// left over here is one kibble cannot claim to understand, and running it
	// to see what happens would be executing something whose meaning nobody
	// established.
	if !shell.Parses(cmd) {
		return "is not something a shell parser accepts, so kibble did not guess at it",
			ReasonUnparseable, false
	}
	if reLocalhost.MatchString(flat) {
		return "needs a local service the docs assume is running", ReasonMissingDependency, false
	}
	if pl.getsOwnModule(flat) {
		return "adds this module to the reader's own project, not to itself", ReasonMissingFixture, false
	}
	bin, sub := invokedBinary(flat, pl.binaries)
	if bin != "" && reLogin.MatchString(flat) {
		return "needs an interactive sign-in", ReasonInteractive, false
	}
	if bin != "" && sub == "audio" {
		return "records audio, which the container cannot", ReasonInteractive, false
	}
	if bin != "" && interactiveSubs[sub] {
		return "starts an interactive or long-running session the container cannot judge",
			ReasonLongRunning, false
	}
	// A documented binary invoked bare is "run the tool", which the smoke test
	// already settled. For a watcher or a server it never returns, and waiting
	// out the timeout buys nothing the install step did not already prove.
	if bin != "" && len(strings.Fields(stripComment(flat))) == 1 {
		return "runs the tool with no arguments, which the install already proved",
			ReasonAlreadyProven, false
	}
	// A tool the document introduces as watching or serving does not return,
	// but only an invocation that actually reaches for the watching or serving
	// mode is skipped, so a one-shot subcommand of the same tool still runs.
	if bin != "" && pl.watcher && watcherInvocation(sub, flat) {
		return "the docs describe a tool that watches or serves, and this invocation does not return",
			ReasonLongRunning, false
	}
	if bin != "" && interactiveFlag(flat) {
		return "asks for an interactive session the container cannot hold", ReasonInteractive, false
	}
	if hasBareStdinDash(flat) {
		return "reads stdin, which the session does not provide", ReasonInteractive, false
	}
	if reGitState.MatchString(flat) {
		return "needs git history or a remote, which the fresh session repo lacks",
			ReasonMissingFixture, false
	}
	if dir := systemCd(flat); dir != "" {
		return fmt.Sprintf("changes into %s, which only the reader's system has", dir),
			ReasonMissingFixture, false
	}
	if reFishSource.MatchString(flat) {
		return "written for the fish shell, and the session runs bash", ReasonOtherPlatform, false
	}
	if reForeignShellFile.MatchString(flat) {
		return "written for another shell, and the session runs bash", ReasonOtherPlatform, false
	}
	if reForeignShellGen.MatchString(flat) {
		return "sources another shell's completions, and the session runs bash",
			ReasonOtherPlatform, false
	}
	if sh := foreignShellFlag(flat); sh != "" {
		return fmt.Sprintf("asks for the %s shell, which the container does not have", sh),
			ReasonOtherPlatform, false
	}
	if reKernelPath.MatchString(flat) {
		return "touches kernel interfaces the container does not expose", ReasonOtherPlatform, false
	}
	if miss := pl.missingGlob(flat); miss != "" {
		return fmt.Sprintf("globs %s, which the docs never create", miss), ReasonMissingFixture, true
	}
	if bin != "" && bareWordPlaceholder(flat) != "" {
		return fmt.Sprintf("docs use %q as a placeholder the reader must fill in",
			bareWordPlaceholder(flat)), ReasonPlaceholder, false
	}
	expandable := withoutSingleQuoted(flat)
	for v := range pl.badVars {
		if strings.Contains(expandable, "$"+v) || strings.Contains(expandable, "${"+v+"}") {
			return fmt.Sprintf("expands $%s, which a skipped line was to set", v),
				ReasonDependsOnSkipped, false
		}
	}
	if v := pl.unsetVar(flat); v != "" {
		return fmt.Sprintf("expands $%s, which the docs never set", v), ReasonMissingFixture, false
	}
	if path := pl.missingFile(flat); path != "" {
		return fmt.Sprintf("references %s, which the docs never create", path), ReasonMissingFixture, true
	}
	if p := pl.missingHomePath(flat); p != "" {
		return fmt.Sprintf("reads %s, which only the reader's machine has", p), ReasonMissingFixture, false
	}
	return "", "", false
}

// reAbsoluteCd captures the target of a cd into an absolute directory.
var reAbsoluteCd = regexp.MustCompile(`(?:^|&&|;)\s*cd\s+(/\S*)`)

// systemCd returns the absolute directory a line changes into when that
// directory is one the docs assume exists on the reader's system, such as a
// BSD ports tree. The session's own directories are exempt.
func systemCd(flat string) string {
	m := reAbsoluteCd.FindStringSubmatch(flat)
	if m == nil {
		return ""
	}
	for _, ok := range []string{"/work", "/tmp", "/root"} {
		if m[1] == ok || strings.HasPrefix(m[1], ok+"/") {
			return ""
		}
	}
	return m[1]
}

// reVarExpansion matches a shell variable expansion such as $HOME or ${HOME}.
// Command substitution and positional parameters do not match, since neither
// starts with a letter or underscore.
var reVarExpansion = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)

// containerVars are the variables the session provides, so expanding one is
// honest even though no documented line assigns it: the container's own
// environment, plus the editor and git variables the session script exports.
var containerVars = map[string]bool{
	"HOME": true, "PATH": true, "PWD": true, "OLDPWD": true, "SHLVL": true,
	"HOSTNAME": true, "TERM": true, "LANG": true, "TMPDIR": true, "USER": true,
	"GOPATH": true, "GOBIN": true, "GOROOT": true,
	"CARGO_HOME": true, "RUSTUP_HOME": true,
	"EDITOR": true, "VISUAL": true, "GIT_EDITOR": true, "GIT_TERMINAL_PROMPT": true,
	"DEBIAN_FRONTEND": true,
}

// unsetVar returns the first variable a line expands that nothing in the
// session sets, or empty when every expansion resolves. A README written for
// an interactive shell cites variables such as HISTFILE that no documented
// line assigns and no container provides, so the line is skipped rather than
// failed: the document is right, the container is simply not that shell.
func (pl *planner) unsetVar(flat string) string {
	local := localAssignments(flat)
	for _, m := range reVarExpansion.FindAllStringSubmatch(withoutSingleQuoted(flat), -1) {
		name := m[1]
		if pl.setVars[name] || containerVars[name] || local[name] {
			continue
		}
		return name
	}
	return ""
}

// withoutSingleQuoted blanks the contents of single-quoted spans, since a
// shell expands nothing inside them. It keeps an awk or sed program such as
// `awk '{print $NF}'` from reading as a line that expands a variable the
// session never sets, which would skip a working documented line. Quotes
// inside a double-quoted span are literal text and do not open one.
func withoutSingleQuoted(flat string) string {
	b := []byte(flat)
	inSingle, inDouble := false, false
	for i := range b {
		switch {
		case b[i] == '"' && !inSingle:
			inDouble = !inDouble
		case b[i] == '\'' && !inDouble:
			inSingle = !inSingle
			b[i] = ' '
		case inSingle:
			b[i] = ' '
		}
	}
	return string(b)
}

// localAssignments returns the variables a line assigns before its command,
// as in `FOO=bar cmd`, since a line may expand what it just set.
func localAssignments(flat string) map[string]bool {
	out := map[string]bool{}
	line, ok := shell.Parse(flat)
	if !ok {
		return out
	}
	// The parser separates assignment prefixes from words, so this no longer
	// has to scan tokens and stop at the first one that does not look like an
	// assignment. It also sees the ones a list puts later in the line, which
	// scanning from the front could never reach.
	for _, c := range line.Cmds {
		for _, a := range c.Assigns {
			out[a] = true
		}
	}
	return out
}

// reFishSource matches piping into a bare `source`, fish's idiom for loading
// shell integration, which bash cannot run.
var reFishSource = regexp.MustCompile(`\|\s*source\s*$`)

// reForeignShellFile matches loading a file whose extension names another
// shell: nushell, fish, PowerShell, xonsh, tcsh, csh, elvish. bash cannot
// execute any of them.
var reForeignShellFile = regexp.MustCompile(`(^|\s)(source|\.)\s+\S+\.(nu|fish|ps1|xsh|tcsh|csh|elv)\b`)

// reForeignShellGen matches sourcing completions generated for another shell,
// as in source <(rg --generate complete-zsh). The generator runs fine; it is
// the sourcing of another shell's syntax that the session cannot do.
var reForeignShellGen = regexp.MustCompile(
	`(^|\s)(source|\.)\s[^\n]*\b(zsh|fish|ksh|csh|tcsh|elvish|nushell|powershell|pwsh)\b`)

// foreignShells are shells a documented `--shell NAME` flag can name that the
// session's bash image does not provide, as in a benchmark run under zsh.
var foreignShells = map[string]bool{
	"zsh": true, "fish": true, "tcsh": true, "csh": true, "ksh": true,
	"elvish": true, "nu": true, "nushell": true, "pwsh": true, "powershell": true,
}

// reShellFlag captures the argument of a documented --shell flag.
var reShellFlag = regexp.MustCompile(`--shell[= ]([A-Za-z]+)`)

// foreignShellFlag returns the shell a line asks for through --shell when that
// shell is not the bash the session provides, or empty otherwise.
func foreignShellFlag(flat string) string {
	if m := reShellFlag.FindStringSubmatch(flat); m != nil && foreignShells[m[1]] {
		return m[1]
	}
	return ""
}

// reKernelPath matches a reference to /proc or /sys, kernel interfaces a
// container cannot honestly provide, as in a benchmark dropping page caches.
var reKernelPath = regexp.MustCompile(`(^|[\s'"=])/(proc|sys)/`)

// placeholderWords are bare words docs conventionally use where the reader
// supplies a real value, as in `fd pattern path`. Only exact, unquoted
// positional tokens count, so a real file named pattern.txt is unaffected.
var placeholderWords = map[string]bool{
	"pattern": true, "path": true, "file": true, "filename": true, "dirname": true,
	"query": true, "regex": true, "searchterm": true, "yourfile": true,
}

// upperPlaceholderWords are all-caps stand-ins docs use for a value the reader
// supplies, beyond the ones the noun-suffix rule already catches.
var upperPlaceholderWords = map[string]bool{
	"INPUT": true, "OUTPUT": true, "SRC": true, "DEST": true, "URL": true,
	"PATTERN": true, "QUERY": true, "TARGET": true, "SOURCE": true, "ARG": true,
}

// reUpperPlaceholder matches an all-caps token ending in a placeholder noun,
// the SOMEFILE, MYDIR, CONFIGPATH convention docs use for a reader-supplied
// value. Acronyms such as GET or TCP do not end in these nouns, so they are
// left to run.
var reUpperPlaceholder = regexp.MustCompile(`^[A-Z][A-Z0-9_]*(FILE|DIR|DIRECTORY|PATH|NAME)$`)

// reUpperArg matches an all-caps positional argument such as TOPIC or SUBJECT.
// A reference page prints a synopsis rather than a command a reader copies
// whole, and the capitals are the convention that says so.
var reUpperArg = regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)

// bareWordPlaceholder returns the first positional token that is a
// conventional placeholder word, or empty when none is. The command word
// itself is exempt, since a tool could be named pattern.
func bareWordPlaceholder(flat string) string {
	for _, tok := range shell.Operands(flat) {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		if placeholderWords[tok] || upperPlaceholderWords[tok] ||
			reUpperPlaceholder.MatchString(tok) || reUpperArg.MatchString(tok) {
			return tok
		}
	}
	return ""
}

// interactiveSubs are subcommands that open an interactive session or serve
// until interrupted. The container cannot show or judge one, so invoking a
// documented binary with one of these is skipped rather than left to hang.
var interactiveSubs = map[string]bool{
	"demo": true, "serve": true, "server": true, "daemon": true,
	"tui": true, "dashboard": true, "repl": true, "console": true,
	"record": true, "watch": true, "attach": true, "shell": true, "top": true,
	"listen": true, "proxy": true, "gateway": true,
}

// findingSubs are subcommands whose documented behavior is to exit nonzero
// when they find something, the way a linter fails a run it flags. A nonzero
// exit from these is the tool working, not the docs breaking.
var findingSubs = map[string]bool{
	"check": true, "lint": true, "audit": true, "vet": true, "diff": true,
	"format": true, "fmt": true,
}

// interactiveFlag reports whether a line asks a binary to run interactively.
// Only the long form counts: bare -i means in-place to sed, ignore-case to
// grep, and interactive to git rebase, so reading it as a prompt skips working
// lines and breaks the ones that follow them. A repo whose -i does mean
// interactive says so in .kibble.yml.
func interactiveFlag(flat string) bool {
	for _, f := range strings.Fields(stripComment(flat)) {
		if f == "--interactive" {
			return true
		}
	}
	return false
}

// reWatcherWord matches how a document describes a tool that keeps running:
// it watches files, restarts something, serves, or listens.
var reWatcherWord = regexp.MustCompile(
	`(?i)\b(watch(es|ing)?|restart(s|ing)?|monitor(s|ing)?|serv(e|es|ing|er)|` +
		`daemon|listen(s|ing)?|live[- ]reload|hot[- ]reload|file changes)\b`)

// reWatcherSub matches a subcommand name that names a watching or serving mode
// that does not return. Ambiguous names such as run and start are left out, so
// a one-shot invocation is run and, if it hangs anyway, settled by the line
// timeout rather than skipped in advance.
var reWatcherSub = regexp.MustCompile(`(?i)^(watch|serve|server|dev|preview|monitor|daemon|listen)$`)

// reWatcherFlag matches a flag that puts a command into a watching or serving
// mode, chiefly a dev server's address or an explicit watch.
var reWatcherFlag = regexp.MustCompile(
	`(?i)(^|\s)(--watch|--serve|--server|--daemon|--reload|--hot|--live|` +
		`--port|--address|--listen|--host)(\b|=)`)

// watcherInvocation reports whether an invocation of a tool the document
// describes as a watcher actually reaches its watching or serving mode. An
// info query such as --help returns and is never a watcher, and a one-shot
// subcommand of the same tool is left to run rather than skipped alongside the
// long-running one.
func watcherInvocation(sub, flat string) bool {
	if isInfoInvocation(flat) {
		return false
	}
	if sub != "" && reWatcherSub.MatchString(sub) {
		return true
	}
	return reWatcherFlag.MatchString(flat)
}

// describedAsWatcher reports whether a document introduces a binary as a tool
// that keeps running. The words have to sit near the tool's name, since a
// document can mention watching for reasons that say nothing about the
// command, and being wrong here costs a check that would have run.
// reOtherPlatform matches the operating systems a Linux container cannot
// stand in for. BSD sed counts because the FreeBSD and macOS sed shares a
// name with GNU sed and nothing else, which is exactly why docs call it out.
var reOtherPlatform = regexp.MustCompile(`(?i)\b(macOS|OS X|FreeBSD|OpenBSD|NetBSD|Windows|BSD sed)\b`)

// reThisPlatform matches the prose that names the container's own world. A
// sentence naming both worlds is contrasting them, not scoping the block.
var reThisPlatform = regexp.MustCompile(`(?i)\b(Linux|GNU)\b`)

// otherPlatform returns the platform a block's introducing sentence scopes it
// to, or empty when the block is for everyone. Only the last sentence counts:
// the FAQ that documents the GNU form, then says "if you are using BSD sed
// (the default on macOS) use the following", scopes only the second block,
// and the sentence before that one mentions GNU without scoping anything.
// Running a macOS command on Linux convicts a document that told the reader
// exactly what to run where, the same false positive as installing a cask.
func otherPlatform(intro string) string {
	intro = strings.TrimSpace(intro)
	if intro == "" {
		return ""
	}
	sentences := strings.Split(intro, ". ")
	last := sentences[len(sentences)-1]
	m := reOtherPlatform.FindString(last)
	if m == "" || reThisPlatform.MatchString(last) {
		return ""
	}
	return m
}

func describedAsWatcher(markdown, bin string) bool {
	if bin == "" {
		return false
	}
	// Only prose describes what a tool is. A code block containing `tool serve`
	// says the tool has a serve subcommand, which is a different fact and one
	// the interactive-subcommand rule already covers.
	head := proseOnly(markdown)
	if len(head) > 1500 {
		head = head[:1500]
	}
	re, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(bin) + `\b`)
	if err != nil {
		return false
	}
	for _, loc := range re.FindAllStringIndex(head, -1) {
		start := max(0, loc[0]-40)
		end := min(len(head), loc[1]+160)
		if reWatcherWord.MatchString(head[start:end]) {
			return true
		}
	}
	return false
}

// isInfoInvocation reports whether a line only asks a binary about itself,
// which returns even when running the tool would not.
func isInfoInvocation(flat string) bool {
	f := stripComment(flat)
	return strings.Contains(f, "--help") || strings.Contains(f, "--version") ||
		strings.Contains(f, " version") || strings.Contains(f, " help")
}
