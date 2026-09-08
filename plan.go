package main

import (
	"fmt"
	"sort"
	"strings"
)

// Plan describes what kibble will replay for one repository and how it decided:
// the types the plan is made of, the walk that builds it, and the block-level
// judgment about which code fences are recipes at all.

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
	// SkipReason is the machine-readable code for Skip, so a consumer can
	// filter and audit skip reasons without parsing the human-facing string.
	SkipReason Reason `json:"skipReason,omitempty"`
	// Gap marks a Skip whose cause is the document rather than the
	// container: the line names something no documented step creates.
	Gap bool `json:"gap,omitempty"`
	// NonzeroOK accepts a nonzero exit as documented behavior.
	NonzeroOK bool `json:"nonzeroOk,omitempty"`
	// Synthetic names the files kibble fabricated so this line had something
	// to read. The document referenced them and never created them, so a pass
	// here proves the command accepts kibble's invented input, which is a
	// weaker claim than the documented example working.
	Synthetic []string `json:"synthetic,omitempty"`
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
	// synthetic collects the fixtures fabricated while classifying the line
	// currently being planned, and is drained onto that line. A line that
	// reads a file kibble invented was not tested against the document's own
	// example, and the verdict has to be able to say so.
	synthetic []string
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
		pl.synthetic = nil
		line.Skip, line.SkipReason, line.Gap = pl.skipReason(ln, flat)
		line.Synthetic = pl.synthetic
		if line.Skip == "" && scopedTo != "" {
			line.Skip = fmt.Sprintf("documented for %s rather than this container", scopedTo)
			line.SkipReason = ReasonOtherPlatform
		}
		if line.Skip == "" && lostDir {
			line.Skip = "follows a skipped cd, so it would run in the wrong directory"
			line.SkipReason = ReasonDependsOnSkipped
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
