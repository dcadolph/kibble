package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dcadolph/kibble/internal/shell"
)

// Turning one line's exit and output into a verdict. The distinction this file
// exists to hold is between evidence and resemblance: evidence from outside
// the tool excuses a line, a resemblance leaves it blocked, and everything
// else is a failure. See docs/DESIGN.md for why that split is the whole point.

// documentedNonzeroCode reports whether a nonzero exit is the kind a document
// can legitimately call expected behavior. A tool may exit nonzero to signal a
// finding, such as a linter that returns 1 when it flags something, but only an
// ordinary program exit qualifies. A timeout (124), a shell "cannot execute"
// or "not found" (126 and 127), and a signal death (128 and up, such as 139
// for a segfault or 134 for an abort) are never documented behavior, and a
// document that blesses nonzero exits must not launder a crash into a pass.
func documentedNonzeroCode(code int) bool {
	return code >= 1 && code <= 125 && code != 124
}

// classifyLineResult turns one recorded exit into a line result. The rules
// fall into three kinds and the distinction is the point. Some evidence comes
// from outside the tool, such as the shell's own 127 or a terminal error, and
// excuses the line as a skip. Some evidence is only a resemblance, such as a
// 403 that may be a missing account or a wrong argument, and leaves the line
// blocked: run, unexplained, and claiming nothing about the document. What
// resembles nothing is a failure.
func classifyLineResult(lr lineResult, l PlanLine, o lineOutcome, wrapped bool,
	documented map[string]bool, lineBudget time.Duration) lineResult {
	lr.Code = o.code
	// The same reason as classify: a documented line that colors its output
	// must not carry escapes into a report or an annotation.
	o.output = stripANSI(o.output)
	tail := failureLine(strings.Split(o.output, "\n"))
	switch {
	case o.background:
		// The line never exited, so there is no exit code to read. What the
		// session observed is whether the service it started announced
		// itself, and the verdict says exactly that and no more.
		if o.ready {
			lr.Status = StatusVerified
			lr.Detail = "started and reached its documented readiness signal, without exiting"
			return lr
		}
		lr.Status = StatusBlocked
		lr.Reason = ReasonLongRunning
		lr.Detail = "ran without exiting and never reached its documented readiness signal"
		lr.output = o.output
		return lr
	case wrapped && o.code == 124:
		lr.Status = StatusTimeout
		lr.Detail = fmt.Sprintf("gave no result within %s", lineBudget)
	case o.code == 0:
		lr.Status = StatusVerified
		if len(lr.Synthetic) > 0 {
			// The claim is narrower than a bare pass and has to say so: the
			// command accepted a file kibble wrote, because the document
			// named one and never created it.
			lr.Detail = "ran against " + strings.Join(lr.Synthetic, ", ") +
				", which kibble fabricated because no documented step creates it"
		}
	case l.NonzeroOK && documentedNonzeroCode(o.code):
		lr.Status = StatusVerified
		lr.Detail = fmt.Sprintf("exit %d is documented behavior", o.code)
	case reCrash.MatchString(o.output):
		// Checked before every excuse: a process that died did not report a
		// condition kibble can forgive, whatever else it printed first.
		lr.Status = StatusFail
		lr.Detail = fmt.Sprintf("crashed with exit %d: %s", o.code, tail)
	case o.code == 127 || reNoExec.MatchString(o.output) ||
		missingCommandName(lr.Cmd, o.output) != "":
		lr.Status = StatusSkipped
		lr.Reason = ReasonMissingDependency
		lr.Detail = "invokes a command the container lacks: " + tail
	case reNotBuiltIn.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Reason = ReasonMissingDependency
		lr.Detail = "needs a build feature this install does not include: " + tail
	case reMissingBinary.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Reason = ReasonMissingDependency
		lr.Detail = "names a program absent from PATH: " + tail
	case reMissingDep.MatchString(o.output):
		// A tool says "is not installed" about a system package the container
		// lacks and about its own plugins alike. The first is the container's
		// gap and the second is a step the document never wrote down, and the
		// wording does not separate them.
		lr.Status = StatusBlocked
		lr.Reason = ReasonMissingDependency
		lr.Detail = "reports something not installed, which may be the container or a missing step: " + tail
	case reTTYErr.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Reason = ReasonInteractive
		lr.Detail = "needs a terminal, which the container lacks"
	case undocumentedSetting(o.output, documented) != "":
		// The tool named a setting the document never mentions, so the reader
		// is not missing an account, they are missing a step nobody wrote down.
		lr.Status = StatusGap
		lr.Detail = fmt.Sprintf("needs %s, which no documented step sets: %s",
			undocumentedSetting(o.output, documented), tail)
	case reSettingName.MatchString(o.output) && reMissingPhrase.MatchString(o.output):
		// The document names this setting, so supplying it is the reader's
		// job and the container simply cannot.
		lr.Status = StatusSkipped
		lr.Reason = ReasonMissingFixture
		lr.Detail = "needs a setting the reader supplies: " + tail
	case reCredErr.MatchString(o.output):
		// A refusal is a refusal. Whether the reader is missing an account or
		// the document names the wrong resource produces the same 403, and
		// this rule cannot tell which, so it settles neither.
		lr.Status = StatusBlocked
		lr.Reason = ReasonNeedsCredentials
		lr.Detail = "was refused, which may be missing credentials or a wrong argument: " + tail
	case reNetErr.MatchString(o.output):
		// The container runs no services, and a document may also name a port
		// nothing was ever going to serve. Both refuse the connection.
		lr.Status = StatusBlocked
		lr.Reason = ReasonMissingDependency
		lr.Detail = "could not reach a service, which the container may lack or the document may misname: " + tail
	case reNoChange.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Reason = ReasonNoDataExpected
		lr.Detail = "changed nothing, since the session cannot approve it: " + tail
	case o.code == 1 && strings.TrimSpace(o.output) == "":
		// A search reports no match by exiting 1 and saying nothing. So does
		// a command that died without a word. Silence is the absence of
		// evidence, so it cannot be read as the good case.
		lr.Status = StatusBlocked
		lr.Reason = ReasonNoOutputExit1
		lr.Detail = "exited 1 without output, which a search does on no match and a broken command also does"
	case reNoData.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Reason = ReasonNoDataExpected
		lr.Detail = "query found no data in the fresh session"
	case tail == "not found":
		// Two words and a nonzero exit. They are what a lookup prints when it
		// holds nothing and what a broken command prints when it breaks.
		lr.Status = StatusBlocked
		lr.Reason = ReasonNoDataExpected
		lr.Detail = "said only \"not found\", which settles nothing about the document"
	case reEmptyInput.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Reason = ReasonInteractive
		lr.Detail = "rejected the empty input of the session's stubbed editor"
	case o.code == 123 && reNoInputFiles.MatchString(o.output):
		// Exit 123 is xargs reporting that an invocation it ran failed, and
		// "no input files" is that invocation saying it was handed nothing.
		// Together they mean the pipeline's search matched nothing in this
		// fresh session, which settles nothing about the document: the same
		// recipe fed by a reader's tree works exactly as written. ripgrep's
		// FAQ hit this when an earlier documented variant of the same
		// replacement had already rewritten every match the later variant
		// would have found.
		lr.Status = StatusSkipped
		lr.Reason = ReasonNoDataExpected
		lr.Detail = "the pipeline's search matched nothing in the fresh session: " + tail
	case missingFileArg(lr.Cmd, o.output) != "":
		// The tool asked for a file the command names and the session does
		// not have. That is the document assuming the reader brings a file,
		// or forgetting the step that creates it, and either way it is a hole
		// in the document, never proof the tool is broken. A guide's "here is
		// how you would search some-utf16-file" earns the same verdict as a
		// missing setup step: report it, and let a person or the author
		// settle which it was.
		lr.Status = StatusGap
		lr.Detail = fmt.Sprintf("references %s, which no documented step creates: %s",
			missingFileArg(lr.Cmd, o.output), tail)
	default:
		lr.Status = StatusFail
		lr.Detail = fmt.Sprintf("exited %d: %s", o.code, tail)
	}
	lr.output = o.output
	return lr
}

var (
	// reTTYErr matches errors that mean the command needed a terminal.
	reTTYErr = regexp.MustCompile(
		`(?i)/dev/tty|not a (tty|terminal)|terminal is required|requires a terminal|no tty` +
			`|needs an interactive terminal|interactive terminal|must be run interactively` +
			`|not interactive|requires a controlling terminal`)
	// reCredErr matches errors that mean the command needed credentials a
	// clean container cannot have.
	reCredErr = regexp.MustCompile(
		`(?i)api[_ ]?key|credential|unauthorized|forbidden|\b401\b|\b403\b` +
			`|not (logged|signed) in|\blog ?in\b|authenticat|missing (token|key)`)
	// reSettingName matches an environment setting a tool names in its own
	// error, such as MYTOOL_CLIENT_ID or MYTOOL_BACKEND_*. Matching the shape
	// rather than a list of suffixes keeps the rule from needing a new entry
	// for every credential a tool invents.
	reSettingName = regexp.MustCompile(`\b[A-Z][A-Z0-9]{2,}(_[A-Z0-9*]+)+\b`)
	// reMissingPhrase matches the wording that says a required setting is
	// absent. The name alone is not enough: a document may legitimately print
	// a variable it already set, so the tool must also say it is missing.
	reMissingPhrase = regexp.MustCompile(
		`(?i)\b(not set|unset|is required|are required|missing|not configured` +
			`|no .{0,20}configured|please set|must set)\b` +
			`|\bset [A-Z][A-Z0-9_*]{2,}`)
	// reNetErr matches errors that mean the command needed a network
	// service the container does not run.
	reNetErr = regexp.MustCompile(
		`(?i)connection refused|no such host|dial tcp|network is unreachable|could not connect|cannot connect`)
	// reNoData matches a query that ran correctly and found nothing, which a
	// fresh session often cannot avoid: the docs query dates and terms that
	// have no entries yet.
	reNoData = regexp.MustCompile(
		`(?i)\bno (entries|results|matches|data|records)\b|\bfound no\b` +
			`|\bnothing (found|to (show|report))\b|\bno \w+(\s\w+)? found\b` +
			`|\bno [a-z]+ (backups?|snapshots?|indexes|indices)\b`)
	// reEmptyInput matches a command that rejected the empty input the
	// session's stubbed editor produced.
	reEmptyInput = regexp.MustCompile(
		`(?i)\b(entry|body|input|message|text) is empty\b|\bempty (entry|body|input|message)\b`)
	// reNoChange matches a tool reporting it changed nothing. A session that
	// cannot answer an interactive approval sees this for any command whose
	// docs assume a person is watching, and a tool that changed nothing did
	// not break the document.
	reNoChange = regexp.MustCompile(
		`(?i)\bnothing was changed\b|\bno changes (were )?made\b` +
			`|\baborted by (the )?user\b|\bnothing to (do|change|commit)\b`)
	// reShellNotFound matches a shell reporting a command it cannot find, and
	// captures the name. dash is /bin/sh on Debian images, so it is what make
	// runs recipes with, and it says "zip: not found" without the word
	// command. Requiring that word, or exit 127, misses every missing tool a
	// Makefile reaches for. The name is captured because the same shape is
	// what a tool prints about its own inputs, and only the name says which.
	reShellNotFound = regexp.MustCompile(`(?m)\b([\w.+-]+): (?:command )?not found\b`)
	// reNotBuiltIn matches a tool reporting that an optional capability was
	// not compiled into this build, as ripgrep does for PCRE2 when installed
	// without the feature. The command is right; the build is smaller.
	reNotBuiltIn = regexp.MustCompile(
		`(?i)not available in this build|not compiled (in|with)|` +
			`built without|requires the \S+ feature|feature is not enabled`)
	// reNoExec matches the Go exec error for a missing helper program.
	reNoExec = regexp.MustCompile(`executable file not found`)
	// reMissingBinary matches a tool reporting that a program is absent from
	// PATH. PATH is the shell's own idea, so a tool naming it is describing
	// the container rather than its own state, which makes this the one
	// missing-dependency wording strong enough to excuse a line.
	reMissingBinary = regexp.MustCompile(`(?i)\bnot found in (your )?\$?PATH\b`)
	// reMissingDep matches a tool reporting that something it needs is not
	// installed, such as vhs requiring ffmpeg. The wording is not proof of
	// the container's gap: a tool says the same words about its own plugins,
	// extensions, and optional components, which are steps the document owes
	// the reader. The line is therefore blocked rather than skipped.
	reMissingDep = regexp.MustCompile(
		`(?i)\bis not installed\b|\b(please|must) install\b` +
			`|\brequires? \S+ to be installed\b`)
	// reCrash matches a process dying rather than reporting. A crash convicts
	// whatever else the output happens to say, so it is checked before any
	// rule that excuses a line: a tool that panicked after printing "no
	// results" did not find no results, it broke.
	reCrash = regexp.MustCompile(
		`(?im)^(panic|fatal error|traceback \(most recent call last\)):` +
			`|\bsegmentation fault\b|\bassertion failed\b|\bstack overflow\b` +
			`|\bunhandled exception\b|\bthread '.*' panicked at\b`)
	// reLineMarker parses a KIBBLE-LINE marker into step ID, line index,
	// and either an exit code or the SKIP token. The marker is matched
	// anywhere in a line rather than only at its start, so a documented
	// command whose output ends without a newline cannot swallow the marker
	// that follows it and turn a real result into a missing one.
	// A BG marker carries a readiness result rather than an exit code, for a
	// background line that was still running when the step was judged.
	reLineMarker = regexp.MustCompile(`KIBBLE-LINE (\S+):(\d+) (?:CODE=(-?\d+)|BG=(\d+)|SKIP)$`)
)

// reNoInputFiles matches a pipeline stage reporting it was run with nothing
// to read, the way sed says "no input files" when xargs hands it an empty
// argument list.
var reNoInputFiles = regexp.MustCompile(`(?i)\bno input files?\b`)

// reEnoent matches an error that names the file a tool could not find. The
// name is captured so it can be checked against the command's own arguments:
// only a file the documented line itself asked for convicts the document.
var reEnoent = regexp.MustCompile(
	`(?i)([^\s:'"]+)'?: (?:no such file or directory|` +
		`io error for operation on [^\s:]+: no such file or directory)`)

// missingFileArg returns the file the output reports as missing when the
// command itself named it, or empty when the failure is about anything else.
// The name must appear in the command so a tool complaining about its own
// internals does not demote a real failure to a gap. A bare word is matched
// only as a whole argument, but a file-shaped name, one carrying a dot or a
// slash, is matched even inside a larger argument, so a filename embedded in a
// quoted expression such as `load("file1.yaml")` is still seen as the reader's
// missing file rather than a broken tool.
func missingFileArg(cmd, output string) string {
	m := reEnoent.FindStringSubmatch(output)
	if m == nil {
		return ""
	}
	name := strings.Trim(m[1], "'\"\x60")
	if name == "" || strings.HasPrefix(name, "-") {
		return ""
	}
	// Whole-word comparison against the words the shell would pass, so a
	// quoted path containing a space is one argument here rather than two,
	// and a name matches the argument the command really named.
	for _, tok := range shellArgWordsOf(cmd) {
		if tok == name {
			return name
		}
	}
	if strings.ContainsAny(name, "./") && strings.Contains(cmd, name) {
		return name
	}
	return ""
}

// shellArgWordsOf returns a line's words, falling back to whitespace
// splitting only when the line does not parse. A line reaching here has
// already run, so it came from a document kibble could read; the fallback
// exists so a parse kibble did not anticipate degrades to the old answer
// rather than to no answer.
func shellArgWordsOf(cmd string) []string {
	if words, ok := shell.ArgWords(cmd); ok {
		return words
	}
	out := strings.Fields(cmd)
	for i, tok := range out {
		out[i] = strings.Trim(tok, "'\"\x60")
	}
	return out
}

// missingCommandName returns the program a shell reported missing, or empty
// when the output's "not found" is the tool talking about its own input. The
// two are worded identically: dash says "zip: not found" for a program a
// Makefile reached for, and a tool says "apikey: not found" for a key it
// looked up. The name separates them. A name the documented line passes as an
// argument is the tool's subject, not a program the container lacks, so it
// convicts nothing and the line falls through to its real verdict.
func missingCommandName(cmd, output string) string {
	m := reShellNotFound.FindStringSubmatch(output)
	if m == nil {
		return ""
	}
	name := m[1]
	for _, tok := range shellArgWordsOf(cmd) {
		if tok == name {
			return ""
		}
	}
	return name
}

// documentedSettings collects every environment setting the document names,
// including in lines that do not run, so a placeholder export still counts as
// the document telling the reader what to supply.
func documentedSettings(plan *Plan) map[string]bool {
	out := map[string]bool{}
	if plan == nil {
		return out
	}
	for _, n := range plan.Settings {
		out[n] = true
	}
	for _, s := range plan.Steps {
		for _, l := range s.Lines {
			for _, m := range reSettingName.FindAllString(l.Cmd, -1) {
				out[m] = true
			}
		}
	}
	return out
}

// undocumentedSetting returns the first setting a failure names that the
// document never mentions, or empty when the output names none or the
// document covers them all. A wildcard such as MYTOOL_BACKEND_* counts as
// documented when the document names anything sharing its prefix.
func undocumentedSetting(output string, documented map[string]bool) string {
	if !reMissingPhrase.MatchString(output) {
		return ""
	}
	for _, m := range reSettingName.FindAllString(output, -1) {
		if documented[m] {
			continue
		}
		if prefix := strings.TrimSuffix(m, "*"); prefix != m {
			covered := false
			for d := range documented {
				if strings.HasPrefix(d, prefix) {
					covered = true
					break
				}
			}
			if covered {
				continue
			}
		}
		return m
	}
	return ""
}
