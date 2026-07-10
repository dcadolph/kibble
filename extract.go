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
	// Run reports whether v1 actually executes this kind of step.
	Run bool
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
	// reGoInstall matches a `go install <module>@<version>` invocation.
	reGoInstall = regexp.MustCompile(`\bgo\s+install\s+(\S+@\S+)`)
	// reBrew matches a `brew install <target>` invocation.
	reBrew = regexp.MustCompile(`\bbrew\s+install\s+(\S+)`)
	// reGitClone matches a `git clone <target>` invocation.
	reGitClone = regexp.MustCompile(`\bgit\s+clone\s+(\S+)`)
)

// DefaultExtractor returns an Extractor that reads fenced code blocks and
// classifies the install commands inside them. Only fenced blocks are
// considered, so prose mentions in backticks do not count as runnable steps.
func DefaultExtractor() Extractor {
	return ExtractorFunc(func(repo, markdown string) []InstallStep {
		src := []byte(markdown)
		doc := goldmark.New().Parser().Parse(text.NewReader(src))
		seen := map[string]bool{}
		var steps []InstallStep
		add := func(s InstallStep, key string) {
			if seen[key] {
				return
			}
			seen[key] = true
			steps = append(steps, s)
		}
		_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			var code string
			switch node := n.(type) {
			case *ast.FencedCodeBlock:
				code = blockText(node, src)
			case *ast.CodeBlock:
				code = blockText(node, src)
			case *ast.CodeSpan:
				code = spanText(node, src)
			default:
				return ast.WalkContinue, nil
			}
			for _, line := range strings.Split(code, "\n") {
				if s, key, ok := classifyLine(repo, line); ok {
					add(s, key)
				}
			}
			return ast.WalkContinue, nil
		})
		return steps
	})
}

// classifyLine matches one code line against the known install patterns.
func classifyLine(repo, line string) (InstallStep, string, bool) {
	if m := reGoInstall.FindStringSubmatch(line); m != nil {
		mod := m[1]
		bin := path.Base(strings.SplitN(mod, "@", 2)[0])
		return InstallStep{
			Repo: repo, Kind: "go-install", Raw: strings.TrimSpace(line),
			Module: mod, Binary: bin, Run: true,
		}, repo + "|go|" + mod, true
	}
	if m := reBrew.FindStringSubmatch(line); m != nil {
		return InstallStep{
			Repo: repo, Kind: "brew", Raw: strings.TrimSpace(line), Module: m[1],
		}, repo + "|brew|" + m[1], true
	}
	if m := reGitClone.FindStringSubmatch(line); m != nil {
		return InstallStep{
			Repo: repo, Kind: "git-clone", Raw: strings.TrimSpace(line), Module: m[1],
		}, repo + "|clone|" + m[1], true
	}
	return InstallStep{}, "", false
}

// liner is implemented by code block nodes that expose their raw lines.
type liner interface {
	Lines() *text.Segments
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
// written inline in prose are not missed.
func spanText(n ast.Node, src []byte) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			seg := t.Segment
			b.Write(seg.Value(src))
		}
	}
	return b.String()
}
