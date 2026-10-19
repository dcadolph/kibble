package shell

import (
	"regexp"
	"slices"
	"strings"
)

// A documented line does not always name the program it runs in its first
// word. `xargs git branch -D` runs git, `env NAME=value tool sub` runs the
// tool, and `sudo -u git ls` runs ls even though git is one of its words.
// Every rule asking what a line runs has to step through these wrappers the
// same way, so the answer lives here once.

// wrapper describes a command that runs another command named in its own
// arguments, and the options that decide where that command starts.
type wrapper struct {
	// short holds the single-letter options that take the next word as a value
	// when nothing follows them in their own word.
	short string
	// optional holds the single-letter options whose value can only be
	// attached, as in xargs -i{}, so they never take the next word.
	optional string
	// long holds the long options that take the next word as a value when
	// written without an equals sign.
	long []string
	// opaque holds the single-letter options after which the wrapped command
	// cannot be named: one that reports on a command rather than running it,
	// as command -v does, or one that carries it inside a single string, as
	// env -S does.
	opaque string
	// opaqueLong holds the long forms of the opaque options.
	opaqueLong []string
	// operands is how many words the wrapper reads before the command, such
	// as timeout's duration.
	operands int
	// assigns marks a wrapper that accepts NAME=value words before the
	// command, as env and sudo do.
	assigns bool
}

// wrappers are the commands Invoked steps through. The value options come
// from each program's own help in the session image, since a wrong entry
// either swallows the command as a value or reads a value as the command. A
// wrapper missing from this table is not stepped through: the line runs the
// wrapper as far as kibble is concerned. bash's time keyword needs no entry,
// because the parser already yields the command it times as its own.
var wrappers = map[string]wrapper{
	"sudo": {
		short: "CDghpRrTtUu",
		long: []string{"--chdir", "--chroot", "--close-from", "--command-timeout", "--group",
			"--host", "--other-user", "--prompt", "--role", "--type", "--user"},
		opaque:     "elVv",
		opaqueLong: []string{"--edit", "--list", "--validate", "--version"},
		assigns:    true,
	},
	"env": {
		short:      "aCu",
		long:       []string{"--argv0", "--chdir", "--unset"},
		opaque:     "S",
		opaqueLong: []string{"--split-string"},
		assigns:    true,
	},
	"xargs": {
		short:    "adEILnPs",
		optional: "eil",
		long: []string{"--arg-file", "--delimiter", "--max-args", "--max-chars", "--max-lines",
			"--max-procs", "--process-slot-var"},
	},
	"command": {opaque: "Vv"},
	"nohup":   {},
	"timeout": {short: "ks", long: []string{"--kill-after", "--signal"}, operands: 1},
	"nice":    {short: "n", long: []string{"--adjustment"}},
}

// reAssignWord matches a NAME=value word, which env and sudo accept before the
// command they run.
var reAssignWord = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// Invoked returns the command a simple command actually runs, after stepping
// through wrappers such as sudo, env and xargs. When a wrapper only reports
// on a command, carries it inside a single string, or names no command at
// all, the result has no words, since nothing kibble can name runs.
func Invoked(c Cmd) Cmd {
	for {
		w, ok := wrappers[c.Name()]
		if !ok {
			return c
		}
		c.Words = w.command(c.Words[1:])
		if len(c.Words) == 0 {
			return c
		}
	}
}

// command returns the words of the command a wrapper runs, given the words
// after the wrapper's own name, or nil when it runs none that can be named.
func (w wrapper) command(args []string) []string {
	operands := w.operands
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			rest := args[i+1:]
			if operands > len(rest) {
				return nil
			}
			return rest[operands:]
		case strings.HasPrefix(a, "--"):
			name, _, attached := strings.Cut(a, "=")
			if slices.Contains(w.opaqueLong, name) {
				return nil
			}
			if !attached && slices.Contains(w.long, name) {
				i++
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			skip, opaque := w.cluster(a)
			if opaque {
				return nil
			}
			if skip {
				i++
			}
		case w.assigns && reAssignWord.MatchString(a):
		case operands > 0:
			operands--
		default:
			return args[i:]
		}
	}
	return nil
}

// cluster reads one word of single-letter options, as getopt does, and
// reports whether the next word is the value of its last option and whether
// one of its options makes the wrapped command impossible to name.
func (w wrapper) cluster(a string) (bool, bool) {
	for j := 1; j < len(a); j++ {
		ch := a[j]
		switch {
		case strings.IndexByte(w.opaque, ch) >= 0:
			return false, true
		case strings.IndexByte(w.optional, ch) >= 0:
			return false, false
		case strings.IndexByte(w.short, ch) >= 0:
			return j == len(a)-1, false
		}
	}
	return false, false
}
