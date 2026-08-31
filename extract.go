package main

import (
	"path"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// InstallStep is one documented install command found in a repo README.
type InstallStep struct {
	// Repo is the repository directory name.
	Repo string
	// Kind classifies the command: go-install, brew, or git-clone.
	Kind string
	// Raw is the command line as written in the docs.
	Raw string
	// Module is the go module path with version, for go-install steps.
	Module string
	// Binary is the expected installed binary name, for go-install steps.
	Binary string
	// Run reports whether kibble actually executes this kind of step.
	Run bool
	// Line is the 1-based README line the command sits on, 0 when unknown.
	Line int
	// Usage is what the README cites for this binary, when anything is cited.
	Usage *Usage
	// Block is the install recipe for a git-clone step: the clone line and the
	// lines that follow it in the same code block, such as cd and make.
	Block []string
	// plan is the example plan attached to an example step.
	plan *Plan
	// dir is the local repo path an example step streams into its session.
	dir string
	// readme is the README file name the step was extracted from, so an
	// annotation points at the file the repository actually has.
	readme string
	// doc is the document an example step replays, relative to the repository
	// root. Empty on every other kind of step.
	doc string
}

// Extractor finds install steps in README markdown.
type Extractor interface {
	// Extract returns the install steps documented for repo.
	Extract(repo, markdown string) []InstallStep
}

// ExtractorFunc adapts a function to the Extractor interface.
type ExtractorFunc func(repo, markdown string) []InstallStep

// Extract calls f.
func (f ExtractorFunc) Extract(repo, markdown string) []InstallStep {
	return f(repo, markdown)
}

var (
	// reGoInstall matches a `go install <module>@<version>` invocation, allowing
	// leading flags such as `-v` before the module path.
	reGoInstall = regexp.MustCompile(`\bgo\s+install\s+(?:-\S+\s+)*(\S+@\S+)`)
	// reBrew matches a `brew install <target>` invocation.
	reBrew = regexp.MustCompile(`\bbrew\s+install\s+(\S+)`)
	// reGitClone matches a `git clone <target>` invocation.
	reGitClone = regexp.MustCompile(`\bgit\s+clone\s+(\S+)`)
)

// DefaultExtractor returns an Extractor that reads code from fenced blocks,
// indented blocks, and inline spans, and classifies the install commands
// inside them. Only text the author marked as code is considered, so plain
// prose does not count as a runnable step.
func DefaultExtractor() Extractor {
	return ExtractorFunc(func(repo, markdown string) []InstallStep {
		seen := map[string]bool{}
		var steps []InstallStep
		for _, block := range codeBlocks(markdown) {
			for i, line := range block.Lines {
				s, key, ok := classifyLine(repo, line)
				if !ok || seen[key] {
					continue
				}
				seen[key] = true
				if block.Line > 0 {
					s.Line = block.Line + i
				}
				if s.Kind == "git-clone" {
					s.Block = installRecipe(block.Lines[i:])
				}
				steps = append(steps, s)
			}
		}
		return steps
	})
}

// reTeardown matches a line that undoes an install, such as make uninstall or
// make clean. Install blocks often document teardown next to setup, and
// running it would remove the binary the recipe just produced.
var reTeardown = regexp.MustCompile(`\b(uninstall|clean)\b|^rm\s`)

// installRecipe trims an install block to its meaningful lines: in a prompted
// block only the prompted lines survive, so printed output is not mistaken for
// a command, trailing shell comments are stripped, and capture stops at the
// first teardown line. A blank or comment line after the recipe has more than
// its clone line ends it, since blocks often list alternative install options
// separated exactly that way, and alternatives are not a sequence to run.
func installRecipe(lines []string) []string {
	var out []string
	for _, l := range prepareLines(lines) {
		l = strings.TrimSpace(stripComment(strings.TrimPrefix(strings.TrimSpace(l), "$ ")))
		if l == "" || strings.HasPrefix(l, "#") {
			if len(out) > 1 {
				break
			}
			continue
		}
		if reTeardown.MatchString(l) {
			break
		}
		out = append(out, l)
	}
	return out
}

// codeBlock is one region of author-marked code in a README: a fenced block,
// an indented block, or an inline span.
type codeBlock struct {
	// Lang is the fence info language, empty for indented blocks and spans.
	Lang string
	// Heading is the text of the nearest section heading above the block.
	Heading string
	// Span reports whether this is an inline span rather than a block.
	Span bool
	// Line is the 1-based README line the block's content starts on.
	Line int
	// Lines are the raw code lines.
	Lines []string
}

// codeBlocks returns the code the author marked, grouped by block: each
// fenced or indented block is one group of lines, and each inline span is its
// own group.
func codeBlocks(markdown string) []codeBlock {
	src := []byte(markdown)
	doc := goldmark.New().Parser().Parse(text.NewReader(src))
	var blocks []codeBlock
	var heading string
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		b := codeBlock{Heading: heading}
		switch node := n.(type) {
		case *ast.Heading:
			heading = spanText(node, src)
			return ast.WalkContinue, nil
		case *ast.FencedCodeBlock:
			b.Lang = string(node.Language(src))
			b.Lines = strings.Split(blockText(node, src), "\n")
			b.Line = blockLine(node, src)
		case *ast.CodeBlock:
			b.Lines = strings.Split(blockText(node, src), "\n")
			b.Line = blockLine(node, src)
		case *ast.CodeSpan:
			b.Span = true
			b.Lines = strings.Split(spanText(node, src), "\n")
			b.Line = spanLine(node, src)
		default:
			return ast.WalkContinue, nil
		}
		blocks = append(blocks, b)
		return ast.WalkContinue, nil
	})
	return blocks
}

// codeLines returns every line the author marked as code, across all blocks.
func codeLines(markdown string) []string {
	var lines []string
	for _, b := range codeBlocks(markdown) {
		lines = append(lines, b.Lines...)
	}
	return lines
}

// classifyLine matches one code line against the known install patterns.
func classifyLine(repo, line string) (InstallStep, string, bool) {
	if m := reGoInstall.FindStringSubmatch(line); m != nil {
		mod := m[1]
		bin := binaryName(mod)
		return InstallStep{
			Repo: repo, Kind: "go-install", Raw: strings.TrimSpace(line),
			Module: mod, Binary: bin, Run: true,
		}, repo + "|go|" + mod, true
	}
	if m := reBrew.FindStringSubmatch(line); m != nil {
		return InstallStep{
			Repo: repo, Kind: "brew", Raw: strings.TrimSpace(line), Module: m[1], Run: true,
		}, repo + "|brew|" + m[1], true
	}
	if kind, pkg, ok := classifyPackage(line); ok {
		return InstallStep{
			Repo: repo, Kind: kind, Raw: strings.TrimSpace(line),
			Module: pkg, Binary: packageBinary(pkg), Run: true,
		}, repo + "|" + kind + "|" + pkg, true
	}
	if m := reGitClone.FindStringSubmatch(line); m != nil {
		return InstallStep{
			Repo: repo, Kind: "git-clone", Raw: strings.TrimSpace(line), Module: m[1], Run: true,
		}, repo + "|clone|" + m[1], true
	}
	return InstallStep{}, "", false
}

// pkgInvocations are the documented package installs kibble runs, in the order
// they are tried. Each names the leading words that identify the command and
// the step kind the match produces.
var pkgInvocations = []struct {
	// Kind is the step kind a match produces.
	Kind string
	// Words are the leading command words that identify the invocation.
	Words []string
	// Global reports whether the line must also carry a global flag, as npm
	// installs project dependencies rather than a tool without one.
	Global bool
}{
	{Kind: "cargo-install", Words: []string{"cargo", "install"}},
	{Kind: "uv-install", Words: []string{"uv", "tool", "install"}},
	{Kind: "pipx-install", Words: []string{"pipx", "install"}},
	{Kind: "pip-install", Words: []string{"pip", "install"}},
	{Kind: "pip-install", Words: []string{"pip3", "install"}},
	{Kind: "pip-install", Words: []string{"python", "-m", "pip", "install"}},
	{Kind: "pip-install", Words: []string{"python3", "-m", "pip", "install"}},
	{Kind: "npm-install", Words: []string{"npm", "install"}, Global: true},
	{Kind: "npm-install", Words: []string{"npm", "i"}, Global: true},
	{Kind: "npm-install", Words: []string{"pnpm", "add"}, Global: true},
	{Kind: "npm-install", Words: []string{"yarn", "global", "add"}},
}

// bootstrapPackages are the package managers and build plumbing a README
// installs to prepare the machine rather than to document its own tool. A
// line such as `pip install --upgrade pip` is setup, not an install a reader
// is meant to end up with, so kibble does not verify it as one.
var bootstrapPackages = map[string]bool{
	"pip": true, "pip3": true, "npm": true, "cargo": true, "uv": true,
	"pipx": true, "yarn": true, "pnpm": true, "setuptools": true, "wheel": true,
	"virtualenv": true, "build": true,
}

// classifyPackage matches a line against the known package installs and
// returns the step kind and the package being installed.
func classifyPackage(line string) (string, string, bool) {
	for _, inv := range pkgInvocations {
		if inv.Global && !hasGlobalFlag(line) {
			continue
		}
		pkg := normalizePackage(installTarget(line, inv.Words...))
		if pkg == "" || bootstrapPackages[pkg] {
			continue
		}
		return inv.Kind, pkg, true
	}
	return "", "", false
}

// reGlobalFlag matches the flag that makes a node install a tool install
// rather than a project dependency install.
var reGlobalFlag = regexp.MustCompile(`(^|\s)(-g|--global)(\s|$)`)

// hasGlobalFlag reports whether a line installs globally.
func hasGlobalFlag(line string) bool {
	return reGlobalFlag.MatchString(line)
}

// valueFlags are flags that consume the argument after them, so the argument
// is not mistaken for the package being installed.
var valueFlags = map[string]bool{
	"--path": true, "--git": true, "--branch": true, "--tag": true, "--rev": true,
	"--registry": true, "--root": true, "--features": true, "--bin": true,
	"--index": true, "--profile": true, "--target": true, "--version": true,
	"--python": true, "--prefix": true, "-r": true, "--requirement": true,
	"-e": true, "--editable": true, "-c": true, "--constraint": true,
}

// reVersionSpec matches the start of a version constraint on a package name,
// such as the `==1.2` in black==1.2.
var reVersionSpec = regexp.MustCompile(`(==|>=|<=|~=|!=|>|<)`)

// normalizePackage reduces a documented package argument to its bare name, so
// `"black[jupyter]"` and `ruff>=0.5` both name the package they install. The
// documented line still runs verbatim; this is only what kibble calls it.
func normalizePackage(pkg string) string {
	pkg = strings.Trim(pkg, `"'`)
	if i := strings.Index(pkg, "["); i > 0 {
		pkg = pkg[:i]
	}
	if loc := reVersionSpec.FindStringIndex(pkg); loc != nil && loc[0] > 0 {
		pkg = pkg[:loc[0]]
	}
	return pkg
}

// installTarget returns the package a line installs, or empty when the line
// does not match the given command words or names no package. A local install
// such as `cargo install --path .` names none, since the clone recipe already
// covers building the repository itself.
func installTarget(line string, words ...string) string {
	fields := strings.Fields(stripComment(strings.TrimPrefix(strings.TrimSpace(line), "$ ")))
	for len(fields) > 0 && (fields[0] == "sudo" || strings.Contains(fields[0], "=")) {
		fields = fields[1:]
	}
	if len(fields) < len(words) {
		return ""
	}
	for i, w := range words {
		if fields[i] != w {
			return ""
		}
	}
	rest := fields[len(words):]
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		if valueFlags[tok] {
			i++
			continue
		}
		if strings.HasPrefix(tok, "-") {
			continue
		}
		if tok == "." || strings.HasPrefix(tok, "./") || strings.HasPrefix(tok, "/") {
			return ""
		}
		return tok
	}
	return ""
}

// packageBinary guesses the binary name a package provides, for the case where
// the install adds nothing detectable to the bin directory. A scoped node
// package and a versioned request both reduce to the bare name.
func packageBinary(pkg string) string {
	if i := strings.LastIndex(pkg, "@"); i > 0 {
		pkg = pkg[:i]
	}
	if i := strings.LastIndex(pkg, "/"); i >= 0 {
		pkg = pkg[i+1:]
	}
	return strings.TrimPrefix(pkg, "@")
}

// binaryName returns the binary name `go install` produces for a module path.
// Go names the binary after the last path element, unless that element is a
// major-version suffix like v2, in which case it uses the preceding element.
func binaryName(modulePath string) string {
	p := strings.SplitN(modulePath, "@", 2)[0]
	base := path.Base(p)
	if isMajorVersion(base) {
		base = path.Base(path.Dir(p))
	}
	return base
}

// isMajorVersion reports whether s is a module major-version suffix like v2.
func isMajorVersion(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// liner is implemented by code block nodes that expose their raw lines.
type liner interface {
	Lines() *text.Segments
}

// lineAt returns the 1-based line number of a byte offset in src.
func lineAt(src []byte, offset int) int {
	if offset > len(src) {
		offset = len(src)
	}
	return 1 + strings.Count(string(src[:offset]), "\n")
}

// blockLine returns the README line a code block's content starts on.
func blockLine(l liner, src []byte) int {
	lines := l.Lines()
	if lines.Len() == 0 {
		return 0
	}
	return lineAt(src, lines.At(0).Start)
}

// spanLine returns the README line an inline code span sits on.
func spanLine(n ast.Node, src []byte) int {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			return lineAt(src, t.Segment.Start)
		}
	}
	return 0
}

// blockText returns the raw text inside a fenced or indented code block.
func blockText(l liner, src []byte) string {
	var b strings.Builder
	lines := l.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		b.Write(seg.Value(src))
	}
	return b.String()
}

// spanText returns the text inside an inline code span, so install commands
// written inline in prose are not missed. It descends into nested inline nodes,
// so a heading that mixes prose with code spans keeps both.
func spanText(n ast.Node, src []byte) string {
	var b strings.Builder
	var walk func(ast.Node)
	walk = func(node ast.Node) {
		for c := node.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				b.Write(t.Segment.Value(src))
				continue
			}
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
