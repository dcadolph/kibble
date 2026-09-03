package main

import (
	"io"
	"os"
)

// ANSI escapes for the report. Kept as constants rather than a library, since
// the whole need is six colors and a reset.
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiBlue   = "\x1b[34m"
)

// palette paints report text, or leaves it alone when the destination is not
// a terminal. Every run through a pipe, a CI log, or a file gets plain text,
// because an escape sequence in a build log is noise a reader cannot remove.
type palette struct {
	// on reports whether to emit escapes at all.
	on bool
}

// newPalette decides whether w can take color. Color is for a person watching
// a terminal: a pipe, a file, NO_COLOR, or a dumb terminal all mean plain.
func newPalette(w io.Writer) palette {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return palette{}
	}
	if os.Getenv("CLICOLOR_FORCE") != "" {
		return palette{on: true}
	}
	f, ok := w.(*os.File)
	if !ok {
		return palette{}
	}
	info, err := f.Stat()
	if err != nil {
		return palette{}
	}
	return palette{on: info.Mode()&os.ModeCharDevice != 0}
}

// paint wraps s in an escape, or returns it unchanged when color is off.
func (p palette) paint(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	return code + s + ansiReset
}

// strong wraps s in two escapes at once. Nesting the single-style helpers
// emits a redundant reset, since the first one clears every attribute.
func (p palette) strong(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	return ansiBold + code + s + ansiReset
}

// bold, dim, and the colors read at the call site as what they mean.
func (p palette) bold(s string) string   { return p.paint(ansiBold, s) }
func (p palette) dim(s string) string    { return p.paint(ansiDim, s) }
func (p palette) red(s string) string    { return p.paint(ansiRed, s) }
func (p palette) green(s string) string  { return p.paint(ansiGreen, s) }
func (p palette) yellow(s string) string { return p.paint(ansiYellow, s) }
func (p palette) blue(s string) string   { return p.paint(ansiBlue, s) }

// mark returns the symbol and color for a status. The symbol carries the
// meaning on its own, so a reader without color still sees which line failed.
func (p palette) mark(s Status) string {
	switch s {
	case StatusVerified:
		return p.green("✓")
	case StatusBuilt, StatusRan, StatusExists, StatusCrossArch:
		// Ran or exists, but not proven to work. A tilde, not a check, so a
		// reader never mistakes an unverified step for a verified one.
		return p.yellow("~")
	case StatusFail, StatusError:
		return p.red("✗")
	case StatusGap, StatusDrift:
		return p.yellow("!")
	case StatusTimeout:
		return p.yellow("⏱")
	default:
		return p.dim("–")
	}
}

// statusWord names a status that is not a plain pass, so a log search for
// FAIL or GAP still finds the line. A pass needs no word: the mark says it,
// and repeating it in every row buries the rows that matter.
func (p palette) statusWord(s Status) string {
	switch s {
	case StatusVerified:
		return ""
	case StatusFail, StatusError:
		return p.red(string(s)) + "  "
	case StatusBuilt, StatusRan, StatusExists, StatusCrossArch, StatusGap, StatusDrift, StatusTimeout:
		return p.yellow(string(s)) + "  "
	default:
		return p.dim(string(s)) + "  "
	}
}
