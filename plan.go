package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Plan describes how kibble replays one repo's documented examples: which
// code blocks run, the lines inside them, and the fixtures, packages, and
// environment the session needs. Every judgment call lives in the plan, so
// the executor stays deterministic and the plan can be inspected with -plan.
type Plan struct {
	// Repo is the repository directory name.
	Repo string `json:"repo"`
	// Installs are the documented installs run to put the binaries on PATH.
	Installs []PlanInstall `json:"installs,omitempty"`
	// Binaries are the documented binaries the session installs.
	Binaries []string `json:"binaries,omitempty"`
	// Packages are Debian packages installed before any step runs.
	Packages []string `json:"packages,omitempty"`
	// Env is extra environment exported for the whole session.
	Env map[string]string `json:"env,omitempty"`
	// Fixtures are files written into the workdir before any step runs.
	Fixtures []Fixture `json:"fixtures,omitempty"`
	// Steps are the example blocks in documented order.
	Steps []PlanStep `json:"steps,omitempty"`
	// Settings are environment names the document mentions anywhere, prose
	// and tables included. A tool that fails asking for one of these is
	// asking the reader for something the document already told them about.
	Settings []string `json:"settings,omitempty"`
	// Excluded counts code blocks that are not shell recipes.
	Excluded int `json:"excluded,omitempty"`
}

// PlanInstall is one documented install the session runs before replaying
// examples, so the binary the examples call is on PATH.
type PlanInstall struct {
	// Cmd is the shell command that performs the install.
	Cmd string `json:"cmd"`
	// Ecosystem is the toolchain the command needs, empty when it is Go and
	// the configured default image already provides it.
	Ecosystem string `json:"ecosystem,omitempty"`
	// binary is the tool this install is expected to provide. It exists to
	// drop alternative installs of the same tool and is not part of output.
	binary string
	// bootstrap installs the package manager itself when the image lacks it.
	bootstrap string
}

// Fixture is a file the executor writes into the session workdir.
type Fixture struct {
	// Path is the file path, relative to the session workdir.
	Path string `json:"path" yaml:"path"`
	// Contents is the file body.
	Contents string `json:"contents" yaml:"contents"`
}

// PlanStep is one documented code block prepared for execution.
type PlanStep struct {
	// ID names the step by its order in the plan, such as b3.
	ID string `json:"id"`
	// Heading is the section heading the block appears under.
	Heading string `json:"heading,omitempty"`
	// Lines are the logical shell lines of the block.
	Lines []PlanLine `json:"lines"`
	// Background runs the step behind the session and kills it at the end.
	Background bool `json:"background,omitempty"`
	// ReadyLog is output that marks a background step ready.
	ReadyLog string `json:"readyLog,omitempty"`
}

// PlanLine is one logical command from a block: a single documented line, or
// several physical lines joined by a continuation or a heredoc.
type PlanLine struct {
	// Cmd is the command exactly as the session will run it.
	Cmd string `json:"cmd"`
	// Skip is why the line does not run; empty means it runs.
	Skip string `json:"skip,omitempty"`
	// Gap marks a Skip whose cause is the document rather than the
	// container: the line names something no documented step creates.
	Gap bool `json:"gap,omitempty"`
	// NonzeroOK accepts a nonzero exit as documented behavior.
	NonzeroOK bool `json:"nonzeroOk,omitempty"`
	// Line is the 1-based README line the command sits on, 0 when unknown.
	Line int `json:"line,omitempty"`
}

// Runnable reports whether any line of the step actually runs.
func (s PlanStep) Runnable() bool {
	for _, l := range s.Lines {
		if l.Skip == "" {
			return true
		}
	}
	return false
}

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

// synthExtensions are file extensions kibble fabricates a fixture for when
// the docs reference a file they never create. Only prose formats are safe
// to fake; structured formats would change what the example means.
var synthExtensions = map[string]bool{".md": true, ".txt": true}

// interactiveSubs are subcommands that open an interactive session or serve
// until interrupted. The container cannot show or judge one, so invoking a
// documented binary with one of these is skipped rather than left to hang.
var interactiveSubs = map[string]bool{
	"demo": true, "serve": true, "server": true, "daemon": true, "app": true,
	"tui": true, "dashboard": true, "repl": true, "console": true, "web": true,
	"record": true, "watch": true, "attach": true, "shell": true, "top": true,
	"bot": true, "listen": true, "proxy": true, "gateway": true, "agent": true,
}

// findingSubs are subcommands whose documented behavior is to exit nonzero
// when they find something, the way a linter fails a run it flags. A nonzero
// exit from these is the tool working, not the docs breaking.
var findingSubs = map[string]bool{
	"check": true, "lint": true, "audit": true, "vet": true, "diff": true,
	"format": true, "fmt": true,
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

// synthFixture is the body written for a fabricated fixture file.
const synthFixture = `# Notes

A few plain lines for the documented example to read.
Nothing here is special; the example only needs a file to exist.
`

var (
	// rePlaceholder matches tokens a reader must replace before running:
	// angle-bracket slots, xxxx runs, path/to/ and /home/user stand-ins, and
	// values that trail off in an ellipsis. The ellipsis is ignored after a
	// slash so a Go package pattern such as ./... is not mistaken for one.
	rePlaceholder = regexp.MustCompile(
		`<[^<>\s]+>|=<[^<>]+>|(^|[^${])\{[A-Za-z][A-Za-z0-9_]*\}|(^|\s)\[[^\[\]\s]+\](\s|$)` +
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

// buildPlan turns a README's code blocks into an execution plan for one
// repo. binaries and modules come from the repo's go-install steps, dir is
// the local checkout used to resolve file references, and cfg carries the
// repo's .kibble.yml overrides, if any.
func buildPlan(repo, dir, markdown string, binaries []string, installs []PlanInstall, cfg *ExamplesConfig) *Plan {
	p := &Plan{Repo: repo, Installs: installs, Binaries: binaries}
	pl := &planner{
		plan:     p,
		binaries: map[string]bool{},
		tree:     repoTree(dir),
		module:   moduledPath(dir),
		created:  map[string]bool{},
		badVars:  map[string]bool{},
		setVars:  map[string]bool{},
		packages: map[string]bool{},
		fixed:    map[string]bool{},
		cfg:      cfg,
	}
	for _, b := range binaries {
		pl.binaries[b] = true
	}
	if len(installs) > 0 {
		if b := documentedBinary(markdown, pl.binaries); b != "" {
			pl.binaries[b] = true
			p.Binaries = append(p.Binaries, b)
		}
	}
	if cfg != nil {
		p.Env = cfg.Env
		for k := range cfg.Env {
			pl.setVars[k] = true
		}
		p.Fixtures = append(p.Fixtures, cfg.Fixtures...)
		for _, f := range cfg.Fixtures {
			pl.created[f.Path] = true
		}
		for _, pkg := range cfg.Packages {
			pl.packages[pkg] = true
		}
	}
	pl.plan.Settings = documentedSettingNames(markdown)
	for b := range pl.binaries {
		if describedAsWatcher(markdown, b) {
			pl.watcher = true
			break
		}
	}
	for _, block := range codeBlocks(markdown) {
		if block.Span || !shellLangs[block.Lang] {
			continue
		}
		pl.addBlock(block)
	}
	for pkg := range pl.packages {
		p.Packages = append(p.Packages, pkg)
	}
	sort.Strings(p.Packages)
	pl.spreadNonzeroOK()
	return p
}

// spreadNonzeroOK propagates a documented nonzero exit to every line that
// invokes the same binary and subcommand. A note like "exits non-zero if it
// finds any" describes the command, not the one line it sits on.
func (pl *planner) spreadNonzeroOK() {
	ok := map[string]bool{}
	for _, s := range pl.plan.Steps {
		for _, l := range s.Lines {
			if l.NonzeroOK {
				if bin, sub := invokedBinary(flatten(l.Cmd), pl.binaries); bin != "" {
					ok[bin+"|"+sub] = true
				}
			}
		}
	}
	if len(ok) == 0 {
		return
	}
	for si := range pl.plan.Steps {
		for li := range pl.plan.Steps[si].Lines {
			l := &pl.plan.Steps[si].Lines[li]
			if bin, sub := invokedBinary(flatten(l.Cmd), pl.binaries); ok[bin+"|"+sub] {
				l.NonzeroOK = true
			}
		}
	}
}

// planner accumulates plan state as blocks are processed in document order.
type planner struct {
	// plan is the plan being built.
	plan *Plan
	// binaries is the set of documented binary names.
	binaries map[string]bool
	// tree is the set of repo-relative paths in the local checkout.
	tree map[string]bool
	// created is the set of paths earlier lines or fixtures produce.
	created map[string]bool
	// module is the repository's own Go module path, empty when it has none.
	module string
	// watcher marks a documented binary the document describes as watching,
	// serving, or restarting, so its invocations never return on their own.
	watcher bool
	// badVars holds variables assigned by skipped lines; later lines that
	// expand them skip instead of running with an empty value.
	badVars map[string]bool
	// setVars holds variables the session provides: those the config exports
	// and those an earlier line assigned. A line expanding anything else is
	// relying on a shell the container is not.
	setVars map[string]bool
	// packages collects Debian packages the session must install.
	packages map[string]bool
	// fixed tracks fixture paths already fabricated, to avoid duplicates.
	fixed map[string]bool
	// cfg is the repo's .kibble.yml overrides, or nil.
	cfg *ExamplesConfig
}

// addBlock processes one shell-looking code block into a plan step. Blocks
// that do not qualify as recipes are counted and dropped. A block with a
// git clone line is the install recipe the clone check already runs, so the
// whole block is left to it; a lone go install or brew line is dropped and
// the rest of its block still runs, since the session installs on its own.
func (pl *planner) addBlock(block codeBlock) {
	lines := logicalLines(prepareLines(block.Lines))
	if len(lines) == 0 {
		return
	}
	kept := lines[:0]
	for _, ln := range lines {
		flat := flatten(ln)
		if reGitClone.MatchString(flat) {
			return
		}
		if reGoInstall.MatchString(flat) || reBrew.MatchString(flat) {
			continue
		}
		kept = append(kept, ln)
	}
	lines = kept
	if len(lines) == 0 {
		return
	}
	if !pl.qualifies(lines) {
		pl.plan.Excluded++
		return
	}
	step := PlanStep{
		ID:      fmt.Sprintf("b%d", len(pl.plan.Steps)+1),
		Heading: block.Heading,
	}
	scopedTo := otherPlatform(block.Intro)
	lineIn := sourceLineIndex(block)
	shownErr := shownFailures(block)
	nonzero := false
	lostDir := false
	for _, ln := range lines {
		ln = pl.substituted(ln)
		flat := flatten(ln)
		if trimmed := strings.TrimSpace(flat); trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if reNonzeroNote.MatchString(trimmed) {
				nonzero = true
			} else if trimmed != "" {
				nonzero = false
			}
			continue
		}
		line := PlanLine{Cmd: ln, Line: lineIn(ln)}
		if reNonzeroNote.MatchString(trailingComment(flat)) {
			line.NonzeroOK = true
		} else if nonzero {
			line.NonzeroOK = true
			nonzero = false
		}
		if _, sub := invokedBinary(flat, pl.binaries); findingSubs[sub] {
			line.NonzeroOK = true
		}
		if shownErr[strings.TrimSpace(flat)] {
			line.NonzeroOK = true
		}
		line.Skip, line.Gap = pl.skipReason(flat)
		if line.Skip == "" && scopedTo != "" {
			line.Skip = fmt.Sprintf("documented for %s rather than this container", scopedTo)
		}
		if line.Skip == "" && lostDir {
			line.Skip = "follows a skipped cd, so it would run in the wrong directory"
		}
		pl.applyRules(&line, &step, flat)
		if line.Skip != "" && strings.HasPrefix(strings.TrimSpace(flat), "cd ") {
			lostDir = true
		}
		if m := reAssignPrefix.FindStringSubmatch(flat); m != nil {
			if line.Skip == "" {
				pl.setVars[m[1]] = true
			} else {
				pl.badVars[m[1]] = true
			}
		}
		if line.Skip == "" {
			pl.recordCreated(flat)
		}
		step.Lines = append(step.Lines, line)
	}
	if len(step.Lines) == 0 {
		return
	}
	pl.plan.Steps = append(pl.plan.Steps, step)
}

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

// qualifies reports whether every command line of a block starts with a
// known command, a documented binary, a package tool, or an assignment.
func (pl *planner) qualifies(lines []string) bool {
	commands := 0
	for _, ln := range lines {
		flat := strings.TrimSpace(flatten(ln))
		if flat == "" || strings.HasPrefix(flat, "#") {
			continue
		}
		first := strings.Fields(flat)[0]
		switch {
		case knownCommands[first], pl.binaries[first]:
		case packageTools[first] != "":
			pl.packages[packageTools[first]] = true
		case reAssignPrefix.MatchString(flat) && reSimpleWord.MatchString(strings.SplitN(first, "=", 2)[0]):
		default:
			return false
		}
		commands++
	}
	return commands > 0
}

// skipReason returns why a line cannot run in a clean container, or empty
// when it can. The checks run in order of how specific their reason is.
// Substitutions have already been applied, so a placeholder that survives
// here is one the reader was meant to fill in.
func (pl *planner) skipReason(flat string) (string, bool) {
	if rePlaceholder.MatchString(commandHead(flat)) {
		return "docs use a placeholder the reader must fill in", false
	}
	if reLocalhost.MatchString(flat) {
		return "needs a local service the docs assume is running", false
	}
	if pl.getsOwnModule(flat) {
		return "adds this module to the reader's own project, not to itself", false
	}
	bin, sub := invokedBinary(flat, pl.binaries)
	if bin != "" && reLogin.MatchString(flat) {
		return "needs an interactive sign-in", false
	}
	if bin != "" && sub == "audio" {
		return "records audio, which the container cannot", false
	}
	if bin != "" && interactiveSubs[sub] {
		return "starts an interactive or long-running session the container cannot judge", false
	}
	// A documented binary invoked bare is "run the tool", which the smoke test
	// already settled. For a watcher or a server it never returns, and waiting
	// out the timeout buys nothing the install step did not already prove.
	if bin != "" && len(strings.Fields(stripComment(flat))) == 1 {
		return "runs the tool with no arguments, which the install already proved", false
	}
	// A tool the document introduces as watching or serving does not return,
	// whatever arguments it is given, so every invocation but a question about
	// the tool itself would run until the timeout and report nothing.
	if bin != "" && pl.watcher && !isInfoInvocation(flat) {
		return "the docs describe a tool that watches or serves, so it does not return", false
	}
	if bin != "" && interactiveFlag(flat) {
		return "asks for an interactive session the container cannot hold", false
	}
	if hasBareStdinDash(flat) {
		return "reads stdin, which the session does not provide", false
	}
	if reGitState.MatchString(flat) {
		return "needs git history or a remote, which the fresh session repo lacks", false
	}
	if dir := systemCd(flat); dir != "" {
		return fmt.Sprintf("changes into %s, which only the reader's system has", dir), false
	}
	if reFishSource.MatchString(flat) {
		return "written for the fish shell, and the session runs bash", false
	}
	if reForeignShellFile.MatchString(flat) {
		return "written for another shell, and the session runs bash", false
	}
	if reForeignShellGen.MatchString(flat) {
		return "sources another shell's completions, and the session runs bash", false
	}
	if sh := foreignShellFlag(flat); sh != "" {
		return fmt.Sprintf("asks for the %s shell, which the container does not have", sh), false
	}
	if reKernelPath.MatchString(flat) {
		return "touches kernel interfaces the container does not expose", false
	}
	if miss := pl.missingGlob(flat); miss != "" {
		return fmt.Sprintf("globs %s, which the docs never create", miss), true
	}
	if bin != "" && bareWordPlaceholder(flat) != "" {
		return fmt.Sprintf("docs use %q as a placeholder the reader must fill in",
			bareWordPlaceholder(flat)), false
	}
	expandable := withoutSingleQuoted(flat)
	for v := range pl.badVars {
		if strings.Contains(expandable, "$"+v) || strings.Contains(expandable, "${"+v+"}") {
			return fmt.Sprintf("expands $%s, which a skipped line was to set", v), false
		}
	}
	if v := pl.unsetVar(flat); v != "" {
		return fmt.Sprintf("expands $%s, which the docs never set", v), false
	}
	if path := pl.missingFile(flat); path != "" {
		return fmt.Sprintf("references %s, which the docs never create", path), true
	}
	if p := pl.missingHomePath(flat); p != "" {
		return fmt.Sprintf("reads %s, which only the reader's machine has", p), false
	}
	return "", false
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
	fields := strings.Fields(strings.TrimSpace(flat))
	for _, f := range fields {
		if f == "export" {
			continue
		}
		m := reAssignPrefix.FindStringSubmatch(f)
		if m == nil {
			break
		}
		out[m[1]] = true
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

// missingGlob returns a quoted glob argument whose fixed directory prefix
// matches nothing in the repository, or empty. A doc line such as
// `lint "src/util/**/*.js"` shows the shape of a command against the reader's
// tree, and a repository without src/util cannot honestly run it.
func (pl *planner) missingGlob(flat string) string {
	for _, tok := range strings.Fields(stripComment(flat)) {
		tok = strings.Trim(tok, `'"`)
		if strings.HasPrefix(tok, "-") || strings.ContainsAny(tok, ":,") {
			continue
		}
		star := strings.Index(tok, "*")
		if star <= 0 || !strings.Contains(tok[:star], "/") {
			continue
		}
		prefix := tok[:strings.LastIndex(tok[:star], "/")+1]
		if strings.ContainsAny(prefix, "$~") {
			continue
		}
		found := false
		for path := range pl.tree {
			if strings.HasPrefix(path, prefix) {
				found = true
				break
			}
		}
		for path := range pl.created {
			if strings.HasPrefix(path, prefix) {
				found = true
				break
			}
		}
		if !found {
			return tok
		}
	}
	return ""
}

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
	fields := strings.Fields(stripComment(flat))
	for i, tok := range fields {
		tok = strings.Trim(tok, `'"`)
		if i == 0 || strings.HasPrefix(tok, "-") {
			continue
		}
		if placeholderWords[tok] || upperPlaceholderWords[tok] ||
			reUpperPlaceholder.MatchString(tok) || reUpperArg.MatchString(tok) {
			return tok
		}
	}
	return ""
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
		if rule.Match == "" || !strings.Contains(flat, rule.Match) {
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

// missingFile returns the first file token a line references that neither
// the repo, an earlier line, nor a fixture provides. Files kibble can fake
// are added as fixtures instead of skipping the line.
func (pl *planner) missingFile(flat string) string {
	flat = stripComment(flat)
	fields := strings.Fields(flat)
	for i, raw := range fields {
		if i == 0 || isOutputArg(fields, i) {
			continue
		}
		tok := raw
		if j := strings.LastIndex(tok, "="); j >= 0 {
			tok = tok[j+1:]
		}
		tok = strings.TrimPrefix(tok, "@")
		if strings.HasPrefix(tok, "~/") && reHomeFileArg.MatchString(tok) {
			if pl.created[tok] || createsToken(flat, tok) {
				continue
			}
			return tok
		}
		if !reFileArg.MatchString(tok) && !reDotSlashArg.MatchString(tok) {
			continue
		}
		rel := strings.TrimPrefix(tok, "./")
		if pl.tree[rel] || pl.created[rel] || createsToken(flat, tok) {
			continue
		}
		if synthExtensions[filepath.Ext(rel)] {
			if !pl.fixed[rel] {
				pl.fixed[rel] = true
				pl.created[rel] = true
				pl.plan.Fixtures = append(pl.plan.Fixtures, Fixture{Path: rel, Contents: synthFixture})
			}
			continue
		}
		return tok
	}
	return ""
}

// recordCreated tracks the paths a running line will produce, so later
// lines that read them are not flagged as missing their file.
func (pl *planner) recordCreated(flat string) {
	flat = stripComment(flat)
	for _, m := range reCreatedToken.FindAllStringSubmatch(flat, -1) {
		for _, tok := range m[1:] {
			if tok != "" {
				pl.created[strings.TrimPrefix(tok, "./")] = true
			}
		}
	}
	fields := strings.Fields(flat)
	if len(fields) < 2 {
		return
	}
	switch fields[0] {
	case "mkdir", "touch":
		for _, tok := range fields[1:] {
			if !strings.HasPrefix(tok, "-") {
				pl.created[strings.TrimPrefix(tok, "./")] = true
			}
		}
	case "cp", "mv":
		pl.created[strings.TrimPrefix(fields[len(fields)-1], "./")] = true
	}
}

// createsToken reports whether the line itself creates the token, such as a
// redirect target, so the target of `echo x > f.yaml` is not marked missing.
// isOutputArg reports whether a command writes the argument at i rather than
// reading it. A copy's destination and a mkdir's path are things the line
// produces, so requiring them to exist first would report the document as
// incomplete for a step that is doing the creating.
func isOutputArg(fields []string, i int) bool {
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "mkdir", "touch", "mktemp":
		return true
	case "cp", "mv", "install", "ln":
		return i == len(fields)-1
	}
	return false
}

func createsToken(flat, tok string) bool {
	for _, m := range reCreatedToken.FindAllStringSubmatch(flat, -1) {
		for _, t := range m[1:] {
			if t == tok {
				return true
			}
		}
	}
	return false
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
			if m := reTwoColumn.FindStringSubmatch(l); m != nil && balancedQuotes(m[1]) {
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
		if m := reTwoColumn.FindStringSubmatch(l); m != nil && balancedQuotes(m[1]) {
			hits++
		}
	}
	return hits >= 2 && hits*2 >= total
}

// balancedQuotes reports whether s contains an even number of double and
// single quotes, so a two-column split never cuts inside a quoted string.
func balancedQuotes(s string) bool {
	return strings.Count(s, `"`)%2 == 0 && strings.Count(s, `'`)%2 == 0
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

// invokedBinary returns the documented binary a line invokes and its first
// subcommand, or empty strings when the line invokes none. Leading VAR=value
// prefixes are stepped over, so `KEY=x tool sub` still names the tool.
func invokedBinary(flat string, binaries map[string]bool) (string, string) {
	for _, seg := range splitSegments(stripComment(flat)) {
		fields := strings.Fields(seg)
		i := 0
		for i < len(fields) && reAssignPrefix.MatchString(fields[i]) {
			i++
		}
		if i >= len(fields) || !binaries[fields[i]] {
			continue
		}
		sub := ""
		if i+1 < len(fields) && reSubName.MatchString(fields[i+1]) {
			sub = fields[i+1]
		}
		return fields[i], sub
	}
	return "", ""
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

// outputFlags name a flag whose value is a path the command writes.
var outputFlags = map[string]bool{
	"-o": true, "--out": true, "--output": true, "--outfile": true,
	"--out-dir": true, "--output-dir": true, "--dest": true,
	"--destination": true, "--to": true, "--target": true, "--log": true,
}

// hasBareStdinDash reports whether a line passes a bare - argument with no
// pipe feeding it, meaning it would block reading the session's empty stdin.
func hasBareStdinDash(flat string) bool {
	f := stripComment(flat)
	if strings.Contains(f, "|") {
		return false
	}
	fields := strings.Fields(f)
	if len(fields) < 2 {
		return false
	}
	for _, tok := range fields[1:] {
		if tok == "-" {
			return true
		}
	}
	return false
}

// repoTree returns the set of repo-relative file and directory paths in the
// local checkout, capped so a huge repo cannot stall planning. The .git
// directory is ignored.
func repoTree(dir string) map[string]bool {
	tree := map[string]bool{}
	if dir == "" {
		return tree
	}
	count := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return nil
		}
		tree[filepath.ToSlash(rel)] = true
		if count++; count > 20000 {
			return filepath.SkipAll
		}
		return nil
	})
	return tree
}

// reSettingMention matches an environment setting named anywhere in the
// document. Readers are commonly told about a variable in a table or a
// sentence rather than in a runnable line, and a document that names one has
// told the reader what to supply.
var reSettingMention = regexp.MustCompile(`\b[A-Z][A-Z0-9]{2,}(_[A-Z0-9*]+)+\b`)

// documentedSettingNames collects every environment setting the document
// mentions, in any context.
func documentedSettingNames(markdown string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reSettingMention.FindAllString(markdown, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// moduledPath returns the repository's own Go module path, or empty when the
// directory has no go.mod.
func moduledPath(dir string) string {
	if dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// getsOwnModule reports whether a line asks Go to fetch the repository's own
// module. A library documents `go get <its own path>` for the reader to run
// inside their project; run inside the repository it would add the module to
// itself, which Go refuses. The document is right and the session is simply
// standing in the wrong directory.
func (pl *planner) getsOwnModule(flat string) bool {
	if pl.module == "" {
		return false
	}
	fields := strings.Fields(stripComment(flat))
	if len(fields) < 3 || fields[0] != "go" {
		return false
	}
	if fields[1] != "get" && fields[1] != "install" {
		return false
	}
	for _, tok := range fields[2:] {
		path, _, _ := strings.Cut(tok, "@")
		if path == pl.module || strings.HasPrefix(path, pl.module+"/") {
			return true
		}
	}
	return false
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

// missingHomePath returns the first path under the reader's home directory
// that no documented step creates. A document naming ~/src/project is telling
// the reader where their own work lives, not describing a file it ships.
func (pl *planner) missingHomePath(flat string) string {
	fields := strings.Fields(stripComment(flat))
	for i, tok := range fields {
		if i == 0 || isOutputArg(fields, i) {
			continue
		}
		// A path given to an output flag is one the line writes, not one the
		// reader must already have, and the container's home can hold it.
		if i > 0 && outputFlags[strings.TrimSuffix(fields[i-1], "=")] {
			continue
		}
		if !reHomePathArg.MatchString(tok) || pl.created[tok] {
			continue
		}
		return tok
	}
	return ""
}

// reWatcherWord matches how a document describes a tool that keeps running:
// it watches files, restarts something, serves, or listens.
var reWatcherWord = regexp.MustCompile(
	`(?i)\b(watch(es|ing)?|restart(s|ing)?|monitor(s|ing)?|serv(e|es|ing|er)|` +
		`daemon|listen(s|ing)?|live[- ]reload|hot[- ]reload|file changes)\b`)

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
