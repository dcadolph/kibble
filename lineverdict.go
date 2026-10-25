package main

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	kplan "github.com/dcadolph/kibble/internal/plan"
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
func classifyLineResult(lr lineResult, l kplan.PlanLine, o lineOutcome, wrapped bool,
	documented map[string]bool, lineBudget time.Duration) lineResult {
	lr.Code = o.code
	// The same reason as classify: a documented line that colors its output
	// must not carry escapes into a report or an annotation. The shell's own
	// timing report goes with them: it is not the command speaking, and every
	// rule below reads the output as though it were.
	o.output = stripShellTiming(stripANSI(o.output))
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
	case reCrash.MatchString(o.output) && crashMissingName(o.output) != "":
		// The one crash that is not unexplained. A traceback whose last line
		// names the file or program that was missing is the tool saying what
		// it lacked, so it is settled the way a plain exit naming it would be.
		// Only the last line counts: a missing file earlier in a chained
		// traceback did not end the process, and a crash ending any other way
		// still convicts below.
		lr = settleMissing(lr, crashMissingName(o.output), tail)
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
	case reGitNoHistory.MatchString(o.output):
		// git speaking from outside the tool, with the same standing as the
		// shell's 127. The session's repository is one kibble initialized
		// with no commits, so a tool that reads history finds none, and the
		// reader's own repository would have it.
		lr.Status = StatusBlocked
		lr.Reason = ReasonMissingFixture
		lr.Detail = "needs git history, and the session's repository is one kibble made " +
			"with none: " + tail
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
	case reNestedNotFound.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Reason = ReasonMissingDependency
		lr.Detail = "ran a command the container lacks, reported by the tool that invoked it: " + tail
	case reToolCannotStart.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Reason = ReasonMissingDependency
		lr.Detail = "ran a command the container lacks, reported by the tool that invoked it: " + tail
	case reTTYErr.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Reason = ReasonInteractive
		lr.Detail = "needs a terminal, which the container lacks"
	case reOtherArch.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Reason = ReasonOtherPlatform
		lr.Detail = "needs an architecture the container does not have: " + tail
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
	case !o.logged && documentedNonzeroCode(o.code) && strings.TrimSpace(o.output) == "":
		// A search reports no match by exiting 1 and saying nothing. So does a
		// command that died without a word. Silence is the absence of evidence,
		// so it cannot be read as the good case, and it cannot be read as the
		// bad one either. Any ordinary exit qualifies and not only exit 1: a
		// tool returning 2 for "an error occurred" while printing nothing has
		// told the reader nothing, and calling the document broken on that
		// asserts more than the run established. The codes this excludes are
		// the ones that are evidence on their own: a timeout, a shell that
		// could not execute the command, and a death by signal all say what
		// happened without needing to print it.
		lr.Status = StatusBlocked
		lr.Reason = ReasonNoOutputNonzero
		lr.Detail = fmt.Sprintf("exited %d without output, which settles nothing: "+
			"a search does that on no match and a broken command does it too", o.code)
	case reUnresolvedRef.MatchString(o.output):
		// Neither a pass nor a break. The install machinery worked and told
		// kibble the ref is absent, which settles nothing about whether the
		// document is wrong or is showing the reader where to put their own.
		lr.Status = StatusBlocked
		lr.Reason = ReasonMissingFixture
		lr.Detail = "names a git ref this repository does not have, which a document writes " +
			"both as a placeholder the reader replaces and as a ref that moved: " + tail
	case registeredNoun(lr.Cmd, o.output) != "" || namelessRegisteredNoun(o.output) != "":
		// The tool named a preset, plugin, or similar that the command passed,
		// or said it has none without naming one. The reader defines those
		// first, and the tool says only that it has none.
		lr.Status = StatusBlocked
		lr.Reason = ReasonMissingFixture
		if noun := registeredNoun(lr.Cmd, o.output); noun != "" {
			lr.Detail = fmt.Sprintf("names a %s the reader defines first: %s", noun, tail)
		} else {
			lr.Detail = fmt.Sprintf("reached for a %s the reader defines first: %s",
				namelessRegisteredNoun(o.output), tail)
		}
	case reRemoteConfig.MatchString(o.output):
		lr.Status = StatusBlocked
		lr.Reason = ReasonMissingFixture
		lr.Detail = "names a remote or configuration section the reader configures first: " + tail
	case reConfigLoad.MatchString(o.output):
		lr.Status = StatusBlocked
		lr.Reason = ReasonMissingFixture
		lr.Detail = "could not load a configuration, which the reader's project supplies: " + tail
	case reNothingSearched.MatchString(o.output):
		// Not a skip: the line ran. Not a failure either, since a search that
		// was handed nothing to read has not tested what the document claims.
		lr.Status = StatusBlocked
		lr.Reason = ReasonNoDataExpected
		lr.Detail = "its filters matched no file in this repository, so the example settled nothing"
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
	case documentedNonzeroCode(o.code) && reNoInputFiles.MatchString(o.output):
		// "no input files" is a consumer saying it was handed nothing, which
		// means the search feeding it matched nothing in this fresh session.
		// That settles nothing about the document: the same recipe fed by a
		// reader's tree works exactly as written. The code this arrives with
		// depends only on which stage the pipeline reports, xargs answering
		// 123 for an invocation that failed or the search itself answering 1
		// for no match, and the same evidence must not read two ways because
		// of that. ripgrep's FAQ produces both, one where an earlier variant
		// of the same replacement had already rewritten every match, and one
		// where the FAQ searches for the placeholder foo in a repository that
		// contains no foo. The line ran and kibble could not tell, which is
		// blocked rather than skipped: skipping is a choice kibble makes
		// before running, and it made no such choice here.
		lr.Status = StatusBlocked
		lr.Reason = ReasonNoDataExpected
		lr.Detail = "the search feeding this pipeline matched nothing in the fresh session, " +
			"so what ran after it had no input: " + tail
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
	case reportedMissing(o.output) != "":
		// The tool named what it lacked and the command never passed it, so
		// the tool reached for it on its own. See settleMissing.
		lr = settleMissing(lr, reportedMissing(o.output), tail)
	case o.inherited:
		// Documents cd once and stay there, so a later section can fail on
		// what the directory holds rather than on what the section says:
		// uv's README pins a Python version inside a project an earlier
		// section created. The section was never tested as written.
		lr.Status = StatusBlocked
		lr.Reason = ReasonInheritedState
		lr.Detail = fmt.Sprintf("ran in %s, which an earlier section entered, so this "+
			"section's claim was not tested as written: exited %d: %s", o.cwd, o.code, tail)
	default:
		lr.Status = StatusFail
		lr.Detail = fmt.Sprintf("exited %d: %s", o.code, tail)
	}
	lr.output = o.output
	return lr
}

var (
	// reTTYErr matches errors that mean the command needed a terminal. The last
	// is the text of ENOTTY, which a terminal call returns on anything that is
	// not one. It is all fzf prints when it has no terminal to draw on, and
	// before it was matched every interactive fzf line in the README failed.
	reTTYErr = regexp.MustCompile(
		`(?i)/dev/tty|not a (tty|terminal)|terminal is required|requires a terminal|no tty` +
			`|needs an interactive terminal|interactive terminal|must be run interactively` +
			`|not interactive|requires a controlling terminal|inappropriate ioctl for device` +
			`|no such device or address`)
	// reOtherArch matches a binary built for an architecture the container
	// cannot run, such as an emulator that lacks the target's loader.
	reOtherArch = regexp.MustCompile(
		`(?i)qemu-\w+: could not open|cannot execute binary file|exec format error`)
	// reCredErr matches errors that mean the command needed credentials a
	// clean container cannot have.
	reCredErr = regexp.MustCompile(
		`(?i)api[_ ]?key|credential|unauthorized|forbidden|\b401\b|\b403\b` +
			`|not (logged|signed) in|\blog ?in\b|authenticat|missing (token|key)|rate limit`)
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
	// reNestedNotFound matches a tool reporting that a command it was asked to
	// run could not be found, by exit code rather than by the shell's wording.
	// A benchmark harness is the clear case: hyperfine documents
	// `hyperfine 'hexdump file' 'xxd file'`, the container has neither program,
	// and hyperfine exits 1 while saying the thing it ran exited 127. The line
	// kibble executed worked; what is absent is a program the container lacks,
	// and that is the container's gap rather than the document's.
	reNestedNotFound = regexp.MustCompile(
		`(?i)non-?zero exit (code|status) 127\b|\bexit(ed)? (code |status )?127\b`)
	// reUnresolvedRef matches git reporting that a ref named on the command line
	// is not in the repository it fetched. A document writes that two ways and
	// the text is identical: as a placeholder the reader replaces, which pipx
	// does with `git+...@branch` and `@abc123def`, and as a real ref that has
	// since been deleted or renamed. Nothing in the output separates them, and
	// a short hexadecimal placeholder is shaped exactly like a real short
	// commit, so kibble cannot decide and must not pretend to.
	reUnresolvedRef = regexp.MustCompile(
		`(?i)pathspec '([^']+)' did not match any file\(s\) known to git` +
			`|could not find remote (branch|ref) '?([^\s']+)'?` +
			`|(?:remote )?ref '?([^\s']+)'? not found`)
	// reUnknownRegistered matches a tool saying it has no preset, profile,
	// plugin, or similar of the name given. The noun list is closed on purpose:
	// argument, flag, option, command, and subcommand name a tool's own
	// vocabulary, so a document using one wrongly is broken.
	reUnknownRegistered = regexp.MustCompile(
		`(?i)unknown (preset|profile|alias|plugin|group|context|workspace|project|remote` +
			`|registry|template|theme) '?([^\s'"]+)'?`)
	// reUnknownNameless matches the same refusal over the same closed noun list,
	// with an optional group that captures a name written after the noun and a
	// space or a colon. The nameless form applies only when that group is empty,
	// i.e. the noun is at the end of the text or is followed by a closing quote
	// or punctuation. When a name follows, only the named form decides, because
	// it deliberately returns FAIL for a name the command never passed.
	reUnknownNameless = regexp.MustCompile(
		`(?i)\bunknown (preset|profile|alias|plugin|group|context|workspace|project|remote` +
			`|registry|template|theme)\b( +'?[^\s'"]|\s*:\s*'?[^\s'"])?`)
	// reRegisteredNotFound matches the same refusal worded as a name not found.
	reRegisteredNotFound = regexp.MustCompile(
		`(?i)\b(preset|profile|alias|plugin|group|context|workspace|project|remote|registry` +
			`|template|theme) '([^']+)' not found`)
	// reRemoteConfig matches a tool naming a remote or configuration section
	// that the reader configures first, in rclone's family of wordings.
	reRemoteConfig = regexp.MustCompile(
		`(?i)didn't find section in config file|couldn't find remote|didn't find backend` +
			`|no such remote|remote .{1,40} not found|section .{1,40} not found in config`)
	// reConfigLoad matches a tool that could not load a configuration, which
	// the reader's project supplies and a fresh session does not have.
	reConfigLoad = regexp.MustCompile(
		`(?i)(failed|unable|could not) to (load|resolve|read) (the )?config(uration)?( file)?`)
	// reNothingSearched matches a search reporting that its filters left it
	// nothing to look at. The command ran and worked; what it says is that
	// this repository holds no file of the kind the example targets, as
	// ripgrep's own guide demonstrates `-tc` against a tree with no C in it.
	// That is a fact about the corpus and not about the document, so the line
	// is unsettled rather than broken.
	reNothingSearched = regexp.MustCompile(
		`(?i)\bno files were searched\b|\bno files (to search|matched|were matched)\b` +
			`|\bnothing to search\b|\bno files found to search\b`)
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
	// reGitNoHistory matches git reporting that the repository it was handed
	// has no commits, in the three wordings git uses for an unborn HEAD.
	reGitNoHistory = regexp.MustCompile(
		`ambiguous argument 'HEAD': unknown revision|does not have any commits yet` +
			`|bad default revision 'HEAD'`)
	// reToolCannotStart matches a tool reporting that a program it runs does
	// not exist, as lla says "Failed to run ripgrep" and Node says "spawn ava
	// ENOENT". The tool launched the program itself, so the shell never saw the
	// name and its own "not found" never appears.
	reToolCannotStart = regexp.MustCompile(
		`(?i)failed to (run|spawn|exec(ute)?|launch|start) ([^\s:]+): no such file or directory` +
			`|spawn (\S+) ENOENT`)
	// reLineMarker parses a KIBBLE-LINE marker into step ID, line index,
	// and either an exit code or the SKIP token. The marker is matched
	// anywhere in a line rather than only at its start, so a documented
	// command whose output ends without a newline cannot swallow the marker
	// that follows it and turn a real result into a missing one.
	// A BG marker carries a readiness result rather than an exit code, for a
	// background line that was still running when the step was judged. A CODE
	// marker may carry the directory the line ran in after it.
	reLineMarker = regexp.MustCompile(
		`KIBBLE-LINE (\S+):(\d+) (?:CODE=(-?\d+)(?: CWD=(.*))?|BG=(\d+)|SKIP)$`)
)

// reNoInputFiles matches a pipeline stage reporting it was run with nothing
// to read, the way sed says "no input files" when xargs hands it an empty
// argument list.
var reNoInputFiles = regexp.MustCompile(`(?i)\bno input files?\b`)

// reEnoent matches an error that names the file a tool could not find. The
// name is captured so it can be checked against the command's own arguments:
// only a file the documented line itself asked for convicts the document.
// reCommandNotStart matches a tool reporting that a helper program it was
// told to run could not be executed. ripgrep says it of a --pre preprocessor,
// and the shape is the same wherever a flag names a program the tool launches.
// The shell never sees the name, so its own "not found" never appears and the
// rules that read for it find nothing.
var reCommandNotStart = regexp.MustCompile(`(?i)command could not start: '?"?([^\s'"]+)`)

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
	// A helper the line names by a flag, which the tool could not launch. The
	// document showed the reader its contents and never wrote it to disk, so
	// what is missing is a step and not the reader's own file.
	if m := reCommandNotStart.FindStringSubmatch(output); m != nil {
		helper := strings.Trim(m[1], "'\"\x60")
		for _, tok := range shell.ArgWordsOrFields(cmd) {
			if tok == helper {
				return helper
			}
		}
	}
	for _, name := range reportedNames(output) {
		if namedInCommand(cmd, name) {
			return name
		}
	}
	return ""
}

// reNodeEnoent matches Node's ENOENT, which names the path after the system
// call that failed: "ENOENT: no such file or directory, scandir '<path>'".
var reNodeEnoent = regexp.MustCompile(`ENOENT: no such file or directory, \w+ '([^']+)'`)

// reNotExist matches a tool's own sentence that a named path is absent, as
// tach says "The path 'my_package' does not exist."
var reNotExist = regexp.MustCompile(`(?i)\b(?:path|file|directory) '([^']+)' does not exist`)

// rePyNoFile matches the line a Python traceback ends with when a file or a
// program it tried to run was missing. Python names it after the message.
var rePyNoFile = regexp.MustCompile(
	`^FileNotFoundError: \[Errno 2\] No such file or directory: '([^']+)'`)

// reErrnoName matches an errno constant such as ENOENT, which the generic
// shape captures from "Error: ENOENT: no such file or directory" as though it
// were the missing name.
var reErrnoName = regexp.MustCompile(`^E[A-Z]+$`)

// reportedNames returns every name the output reports missing, one per shape
// that matched, the specific shapes first. Node's wording also matches the
// generic shape, which would otherwise capture ENOENT itself.
func reportedNames(output string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{reNodeEnoent, reNotExist, reEnoent} {
		m := re.FindStringSubmatch(output)
		if m == nil {
			continue
		}
		name := strings.Trim(m[1], "'\"\x60")
		if name == "" || strings.HasPrefix(name, "-") || reErrnoName.MatchString(name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// reportedMissing returns the first name the output reports missing, or empty
// when it reports none.
func reportedMissing(output string) string {
	if names := reportedNames(output); len(names) > 0 {
		return names[0]
	}
	return ""
}

// crashMissingName returns the file or program a crash's last line names as
// missing, or empty when the crash ended any other way. Only the last line
// counts, since that is the error that ended the process.
func crashMissingName(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	for _, re := range []*regexp.Regexp{rePyNoFile, reNodeEnoent} {
		if m := re.FindStringSubmatch(last); m != nil {
			return m[1]
		}
	}
	return ""
}

// namedInCommand reports whether the command passed name. Words are compared
// whole against the words the shell would pass, so a quoted path containing a
// space is one argument here rather than two. The value of a --flag=name
// counts as the word name, and a trailing slash is ignored on both sides, so
// the my_package/ a command passes is the my_package a tool says is missing.
// A file-shaped name, one carrying a dot or a slash, is matched even inside a
// larger argument.
func namedInCommand(cmd, name string) bool {
	want := strings.TrimSuffix(name, "/")
	if want == "" {
		return false
	}
	for _, tok := range shell.ArgWordsOrFields(cmd) {
		if strings.HasPrefix(tok, "-") {
			if _, val, ok := strings.Cut(tok, "="); ok {
				tok = val
			}
		}
		if strings.TrimSuffix(tok, "/") == want {
			return true
		}
	}
	return strings.ContainsAny(name, "./") && strings.Contains(cmd, name)
}

// settleMissing gives the verdict for a name a tool reported missing. A name
// the command passed is a hole in the document, as missingFileArg holds. A
// path the command never named is something the tool reached for that no
// documented step made, and the tool or the document may be at fault, which is
// what blocked means. A bare word, with no slash and no extension, is a
// program the tool tried to run, and the container lacks it.
func settleMissing(lr lineResult, name, tail string) lineResult {
	switch {
	case namedInCommand(lr.Cmd, name):
		lr.Status = StatusGap
		lr.Detail = fmt.Sprintf("references %s, which no documented step creates: %s", name, tail)
	case strings.Contains(name, "/") || path.Ext(name) != "":
		lr.Status = StatusBlocked
		lr.Reason = ReasonMissingFixture
		lr.Detail = fmt.Sprintf("reads %s, which no documented step creates: %s", name, tail)
	default:
		lr.Status = StatusSkipped
		lr.Reason = ReasonMissingDependency
		lr.Detail = "ran a command the container lacks, reported by the tool that invoked it: " + tail
	}
	return lr
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
	for _, tok := range shell.ArgWordsOrFields(cmd) {
		if tok == name {
			return ""
		}
	}
	return name
}

// documentedSettings collects every environment setting the document names,
// including in lines that do not run, so a placeholder export still counts as
// the document telling the reader what to supply.
func documentedSettings(plan *kplan.Plan) map[string]bool {
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

// registeredNoun returns the lowercase noun of a registered name the tool
// reported missing when the command passed that name as a word, or empty. A
// name the command never passed is the tool's own business, so it convicts
// nothing here.
func registeredNoun(cmd, output string) string {
	words := shell.ArgWordsOrFields(cmd)
	for _, re := range []*regexp.Regexp{reUnknownRegistered, reRegisteredNotFound} {
		for _, m := range re.FindAllStringSubmatch(output, -1) {
			for _, tok := range words {
				if _, val, ok := strings.Cut(tok, "="); ok && val == m[2] {
					return strings.ToLower(m[1])
				}
				if tok == m[2] {
					return strings.ToLower(m[1])
				}
			}
		}
	}
	return ""
}

// namelessRegisteredNoun returns the lowercase noun of a registered name the
// tool said it does not know without naming one, or empty. A noun followed by
// a space and a name is left to registeredNoun.
func namelessRegisteredNoun(output string) string {
	for _, m := range reUnknownNameless.FindAllStringSubmatch(output, -1) {
		if m[2] == "" {
			return strings.ToLower(m[1])
		}
	}
	return ""
}
