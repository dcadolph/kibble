package shell

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// The planner used to read documented commands with strings.Fields and a
// quote counter. That model cannot tell `tool --name "hello world"` from
// `tool --name hello world`, and every rule built on top of it inherited the
// mistake: a quoted path looked like two arguments, a filename inside a
// substitution looked like a bare word, and a configuration rule matched a
// substring of a command it was never written for.
//
// This file is the boundary where that stops. A documented line is parsed by
// a real bash parser, and everything downstream asks the parse rather than
// the string. What the parser cannot read is reported as unreadable instead
// of guessed at, since a verifier that guesses at syntax has no business
// claiming what the command did.

// Cmd is one simple command from a documented line, with its words as
// the shell would split them.
type Cmd struct {
	// Assigns are the NAME=value prefixes attached to this command.
	Assigns []string
	// Words are the command and its arguments, one word per argument, with
	// quoting resolved. A word whose value depends on an expansion keeps the
	// expansion's source text, since its real value is unknown until it runs.
	Words []string
	// Expanded marks a command holding a substitution or expansion whose
	// value kibble cannot know without running it.
	Expanded bool
}

// Name returns the command's program name, or empty when it has none.
func (c Cmd) Name() string {
	if len(c.Words) == 0 {
		return ""
	}
	return c.Words[0]
}

// Arg returns the nth argument after the program name, or empty when the
// command has no such argument.
func (c Cmd) Arg(n int) string {
	if n+1 >= len(c.Words) {
		return ""
	}
	return c.Words[n+1]
}

// Line is a parsed documented line.
type Line struct {
	// Cmds are the simple commands the line runs, in source order, including
	// those inside pipelines, lists, subshells, and compound statements.
	Cmds []Cmd
	// Structured marks a line that is more than one simple command: a
	// pipeline, a list, a redirect, a subshell, a loop, or a conditional.
	Structured bool
	// StateChanging marks a line whose effect is on the shell itself, so
	// running it anywhere but the session's own shell would lose that effect.
	// A bare assignment, a cd, an export, a source, and their relatives all
	// qualify, wherever they appear in the line.
	StateChanging bool
	// Heredoc marks a line carrying a here-document body.
	Heredoc bool
	// Background marks a line the document itself puts in the background.
	Background bool
}

// shellBuiltins are the commands whose whole purpose is to change the shell
// running them. A line containing one cannot be moved into a subshell
// without discarding what it was documented to do.
var shellBuiltins = map[string]bool{
	"cd": true, "export": true, "source": true, ".": true, "eval": true,
	"unset": true, "alias": true, "set": true, "shopt": true, "trap": true,
	"exec": true, "pushd": true, "popd": true, "declare": true, "typeset": true,
	"readonly": true, "local": true, "umask": true, "ulimit": true,
}

// Parse Parses one documented logical line. The second result is false
// when the line is not something a bash parser accepts, which is a fact
// about the line worth reporting rather than a reason to fall back to
// splitting on spaces.
func Parse(cmd string) (Line, bool) {
	parser := syntax.NewParser(syntax.KeepComments(false), syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(cmd), "")
	if err != nil {
		return Line{}, false
	}
	var line Line
	for _, stmt := range file.Stmts {
		if stmt.Background {
			line.Background = true
		}
		if len(stmt.Redirs) > 0 {
			line.Structured = true
		}
		for _, r := range stmt.Redirs {
			if r.Hdoc != nil {
				line.Heredoc = true
			}
		}
	}
	simple := 0
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.BinaryCmd, *syntax.Subshell, *syntax.IfClause, *syntax.ForClause,
			*syntax.WhileClause, *syntax.CaseClause, *syntax.FuncDecl, *syntax.Block,
			*syntax.LetClause, *syntax.TimeClause, *syntax.ArithmCmd, *syntax.TestClause:
			line.Structured = true
		case *syntax.DeclClause:
			// export, declare, local, readonly, and typeset are their own node
			// rather than ordinary calls, so a line changing the environment
			// through one would otherwise look like it changed nothing.
			simple++
			line.StateChanging = true
			c := Cmd{Words: []string{n.Variant.Value}}
			for _, a := range n.Args {
				if a.Name != nil {
					c.Assigns = append(c.Assigns, a.Name.Value)
				}
			}
			line.Cmds = append(line.Cmds, c)
		case *syntax.CallExpr:
			simple++
			c := Cmd{}
			for _, a := range n.Assigns {
				if a.Name != nil {
					c.Assigns = append(c.Assigns, a.Name.Value)
				}
			}
			for _, w := range n.Args {
				text, literal := wordText(w)
				if !literal {
					c.Expanded = true
				}
				c.Words = append(c.Words, text)
			}
			// A CallExpr with assignments and no words is a bare assignment,
			// which is the shell's own state and nothing else's.
			if len(c.Words) == 0 && len(c.Assigns) > 0 {
				line.StateChanging = true
			}
			if shellBuiltins[c.Name()] {
				line.StateChanging = true
			}
			line.Cmds = append(line.Cmds, c)
		}
		return true
	})
	if simple > 1 {
		line.Structured = true
	}
	return line, true
}

// wordText renders one shell word as the value it will have, and reports
// whether that value is known without running anything. A literal, a quoted
// literal, and a concatenation of both are known. An expansion is not, so
// its source text is returned and the word is marked unknown, which lets a
// caller see the shape of the argument without pretending to know its value.
func wordText(w *syntax.Word) (string, bool) {
	var b strings.Builder
	literal := true
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			// The parser keeps escapes in a literal's text, so `some\ path`
			// arrives with its backslash. Resolving it here is what makes the
			// word equal the filename the command will actually receive.
			b.WriteString(unescapeLit(p.Value))
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				switch q := inner.(type) {
				case *syntax.Lit:
					b.WriteString(q.Value)
				default:
					literal = false
					b.WriteString(nodeText(q))
				}
			}
		default:
			literal = false
			b.WriteString(nodeText(part))
		}
	}
	return b.String(), literal
}

// unescapeLit resolves the backslash escapes of an unquoted literal, so a
// word's text is the value the command receives rather than its source. A
// trailing backslash is left alone, since it escapes nothing.
func unescapeLit(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			if s[i] == '\n' {
				continue
			}
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// nodeText returns a node's original source text, for the parts of a word
// whose value cannot be known without running the line.
func nodeText(node syntax.Node) string {
	var b strings.Builder
	if err := syntax.NewPrinter().Print(&b, node); err != nil {
		return ""
	}
	return b.String()
}

// Words returns the words of the first simple command in a line, with
// quoting resolved, and reports whether the line parsed. It is the honest
// replacement for strings.Fields: a quoted argument comes back as one word.
func Words(cmd string) ([]string, bool) {
	line, ok := Parse(cmd)
	if !ok || len(line.Cmds) == 0 {
		return nil, ok
	}
	return line.Cmds[0].Words, true
}

// ArgWords returns every word of every simple command in a line, which
// is what a rule about the line's arguments needs to consider. Assignment
// prefixes are not words and are not included.
func ArgWords(cmd string) ([]string, bool) {
	line, ok := Parse(cmd)
	if !ok {
		return nil, false
	}
	var out []string
	for _, c := range line.Cmds {
		out = append(out, c.Words...)
	}
	return out, true
}

// Operands returns every word of a line that is not a command name, so
// a rule about a line's arguments never trips over the program itself. A
// tool named `pattern` is the command in `pattern build` and a placeholder in
// `tool build pattern`, and only the position separates them.
func Operands(cmd string) []string {
	line, ok := Parse(cmd)
	if !ok {
		return nil
	}
	var out []string
	for _, c := range line.Cmds {
		if len(c.Words) > 1 {
			out = append(out, c.Words[1:]...)
		}
	}
	return out
}

// HasPipe reports whether a line joins commands with a pipe, which is
// what decides whether a bare `-` argument is fed by an upstream command or
// would sit reading the session's empty stdin. A pipe character inside a
// quoted argument is not one, which is the reason this asks the parse rather
// than the string.
func HasPipe(cmd string) bool {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(cmd), "")
	if err != nil {
		return false
	}
	piped := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if b, ok := node.(*syntax.BinaryCmd); ok && b.Op == syntax.Pipe {
			piped = true
		}
		return true
	})
	return piped
}

// MatchesWords reports whether a line contains the match text as a run of
// consecutive whole words. Both sides are read as shell, so quoting is
// resolved the same way on each and a match written the way the document
// writes it selects the same line. When either side does not parse, the
// comparison falls back to a substring, which is the old behavior and the
// only thing left to do with text no parser can read.
func MatchesWords(line, match string) bool {
	hay, hayOK := ArgWords(line)
	needle, needleOK := ArgWords(match)
	if !hayOK || !needleOK || len(needle) == 0 {
		return strings.Contains(line, match)
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		found := true
		for j, w := range needle {
			if hay[i+j] != w {
				found = false
				break
			}
		}
		if found {
			return true
		}
	}
	return false
}

// Parses reports whether a string is something a bash parser accepts. It
// replaces a quote counter that called `echo "it's fine"` unbalanced and
// `echo "a" "b` balanced, being wrong in both directions.
func Parses(s string) bool {
	_, ok := Parse(s)
	return ok
}
