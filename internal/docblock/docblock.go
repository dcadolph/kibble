// Package docblock reads a markdown document into the regions of it that an
// author marked as code, and into the logical lines those regions contain.
//
// It exists because the layer above it, which decides what a documented line
// means and whether to run it, was living in the same file as the parser that
// produces the lines. Two concerns in one file is why the planner could not be
// separated: anything importing the reader also imported the install model.
// Nothing here knows what an install step is, and nothing here may learn.
package docblock

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// Block is one region of author-marked code in a README: a fenced block,
// an indented block, or an inline span.
type Block struct {
	// Lang is the fence info language, empty for indented blocks and spans.
	Lang string
	// Heading is the text of the nearest section heading above the block.
	Heading string
	// Intro is the paragraph that introduces the block: the nearest prose
	// above it under the same heading. Docs scope a block in the sentence
	// before it, "on macOS run the following", and a planner that cannot see
	// that sentence convicts commands written for another operating system.
	Intro string
	// Span reports whether this is an inline span rather than a block.
	Span bool
	// Line is the 1-based README line the block's content starts on.
	Line int
	// Lines are the raw code lines.
	Lines []string
}

// CodeBlocks returns the code the author marked, grouped by block: each
// fenced or indented block is one group of lines, and each inline span is its
// own group.
func CodeBlocks(markdown string) []Block {
	src := []byte(markdown)
	doc := goldmark.New().Parser().Parse(text.NewReader(src))
	var blocks []Block
	var heading, intro string
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if p, ok := n.(*ast.Paragraph); ok {
			intro = spanText(p, src)
			return ast.WalkContinue, nil
		}
		b := Block{Heading: heading, Intro: intro}
		switch node := n.(type) {
		case *ast.Heading:
			heading = spanText(node, src)
			intro = ""
			return ast.WalkContinue, nil
		case *ast.FencedCodeBlock:
			b.Lang = string(node.Language(src))
			b.Lines = strings.Split(BlockText(node, src), "\n")
			b.Line = BlockLine(node, src)
		case *ast.CodeBlock:
			b.Lines = strings.Split(BlockText(node, src), "\n")
			b.Line = BlockLine(node, src)
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

// CodeLines returns every line the author marked as code, across all blocks.
func CodeLines(markdown string) []string {
	var lines []string
	for _, b := range CodeBlocks(markdown) {
		lines = append(lines, b.Lines...)
	}
	return lines
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

// BlockLine returns the README line a code block's content starts on.
func BlockLine(l liner, src []byte) int {
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

// BlockText returns the raw text inside a fenced or indented code block.
func BlockText(l liner, src []byte) string {
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
