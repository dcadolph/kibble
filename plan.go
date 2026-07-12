package main

import (
	"fmt"
	"io/fs"
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
	// Modules are the go modules installed to put the binaries on PATH.
	Modules []string `json:"modules,omitempty"`
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
	// Excluded counts code blocks that are not shell recipes.
	Excluded int `json:"excluded,omitempty"`
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
	// NonzeroOK accepts a nonzero exit as documented behavior.
	NonzeroOK bool `json:"nonzeroOk,omitempty"`
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
}

// synthFixture is the body written for a fabricated fixture file.
const synthFixture = `# Notes

A few plain lines for the documented example to read.
Nothing here is special; the example only needs a file to exist.
`

var (
	// rePlaceholder matches tokens a reader must replace before running:
	// angle-bracket slots, xxxx runs, path/to/ stand-ins, and values that
	// trail off in an ellipsis.
	rePlaceholder = regexp.MustCompile(`<[^<>\s]+>|\bxxxx\b|\bpath/to/|\S\.\.\.(\s|$)`)
	// reLogin matches a command that starts an interactive sign-in.
	reLogin = regexp.MustCompile(`\b(login|signin|sign-in|logout)\b`)
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
		`^\.?/?[\w][\w./+-]*\.(md|txt|yaml|yml|json|csv|ics|toml|ini|env|conf|cfg|xml|html|wav|png|jpg|gif|rb|py|js|go)$`)
	// reDotSlashArg matches a whole ./-prefixed path token of any shape.
	reDotSlashArg = regexp.MustCompile(`^\./[\w][\w./+-]*$`)
	// reCreatedToken matches a token a line creates: a redirect target, an
	// -o argument, or the arguments of mkdir, touch, cp, or mv.
	reCreatedToken = regexp.MustCompile(`>{1,2}\s*([^\s&|;]+)|\s-o\s+(\S+)`)
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
func buildPlan(repo, dir, markdown string, binaries, modules []string, cfg *ExamplesConfig) *Plan {
	p := &Plan{Repo: repo, Modules: modules, Binaries: binaries}
	pl := &planner{
		plan:     p,
		binaries: map[string]bool{},
		tree:     repoTree(dir),
		created:  map[string]bool{},
		poisoned: map[string]bool{},
		badVars:  map[string]bool{},
		packages: map[string]bool{},
		fixed:    map[string]bool{},
		cfg:      cfg,
	}
	for _, b := range binaries {
		pl.binaries[b] = true
	}
	if cfg != nil {
		p.Env = cfg.Env
		p.Fixtures = append(p.Fixtures, cfg.Fixtures...)
		for _, f := range cfg.Fixtures {
			pl.created[f.Path] = true
		}
		for _, pkg := range cfg.Packages {
			pl.packages[pkg] = true
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
	// poisoned marks binaries whose documented sign-in was skipped, so
	// later invocations skip instead of failing on missing credentials.
	poisoned map[string]bool
	// badVars holds variables assigned by skipped lines; later lines that
	// expand them skip instead of running with an empty value.
	badVars map[string]bool
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
	nonzero := false
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
		line := PlanLine{Cmd: ln}
		if reNonzeroNote.MatchString(trailingComment(flat)) {
			line.NonzeroOK = true
		} else if nonzero {
			line.NonzeroOK = true
			nonzero = false
		}
		line.Skip = pl.skipReason(flat)
		pl.applyRules(&line, &step, flat)
		if line.Skip == "" {
			pl.recordCreated(flat)
		} else if m := reAssignPrefix.FindStringSubmatch(flat); m != nil {
			pl.badVars[m[1]] = true
		}
		step.Lines = append(step.Lines, line)
	}
	if len(step.Lines) == 0 {
		return
	}
	pl.plan.Steps = append(pl.plan.Steps, step)
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
func (pl *planner) skipReason(flat string) string {
	if rePlaceholder.MatchString(commandHead(flat)) {
		return "docs use a placeholder the reader must fill in"
	}
	if reLocalhost.MatchString(flat) {
		return "needs a local service the docs assume is running"
	}
	bin, sub := invokedBinary(flat, pl.binaries)
	if bin != "" && reLogin.MatchString(flat) {
		pl.poisoned[bin] = true
		return "needs an interactive sign-in"
	}
	if bin != "" && pl.poisoned[bin] && !isInfoInvocation(flat) {
		return "needs the sign-in the docs run first, which was skipped"
	}
	if bin != "" && sub == "audio" {
		return "records audio, which the container cannot"
	}
	if bin != "" && interactiveSubs[sub] {
		return "starts an interactive or long-running session the container cannot judge"
	}
	if hasBareStdinDash(flat) {
		return "reads stdin, which the session does not provide"
	}
	for v := range pl.badVars {
		if strings.Contains(flat, "$"+v) || strings.Contains(flat, "${"+v+"}") {
			return fmt.Sprintf("expands $%s, which a skipped line was to set", v)
		}
	}
	if path := pl.missingFile(flat); path != "" {
		return fmt.Sprintf("references %s, which the docs never create", path)
	}
	return ""
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
		if i == 0 {
			continue
		}
		tok := raw
		if j := strings.LastIndex(tok, "="); j >= 0 {
			tok = tok[j+1:]
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

// isInfoInvocation reports whether a line only asks a binary about itself,
// which works without the sign-in the rest of the docs need.
func isInfoInvocation(flat string) bool {
	f := stripComment(flat)
	return strings.Contains(f, "--help") || strings.Contains(f, "--version") ||
		strings.Contains(f, " version") || strings.Contains(f, " help")
}

// hasBareStdinDash reports whether a line passes a bare - argument with no
// pipe feeding it, meaning it would block reading the session's empty stdin.
func hasBareStdinDash(flat string) bool {
	f := stripComment(flat)
	if strings.Contains(f, "|") {
		return false
	}
	for _, tok := range strings.Fields(f)[1:] {
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
