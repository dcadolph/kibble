package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// exampleRun carries the per-line outcomes of an example session, so the
// JSON report can show every documented line's result.
type exampleRun struct {
	// Steps are the step outcomes in plan order.
	Steps []exampleStep
}

// exampleStep is the outcome of one plan step.
type exampleStep struct {
	// ID is the plan step ID.
	ID string
	// Heading is the section heading the block appears under.
	Heading string
	// Lines are the line outcomes in documented order.
	Lines []lineResult
}

// lineResult is the outcome of one plan line.
type lineResult struct {
	// Cmd is the flattened command, for display.
	Cmd string
	// Status classifies the line.
	Status Status
	// Reason is the machine-readable why behind a skip or gap, empty when the
	// status speaks for itself.
	Reason Reason
	// Code is the exit code the line returned, or -1 when it never ran.
	Code int
	// Detail explains a skip, failure, or documented nonzero exit.
	Detail string
	// Line is the 1-based README line the command sits on, 0 when unknown.
	Line int
	// Synthetic names the fabricated files this line read, carried from the
	// plan so a pass can say what it was a pass against.
	Synthetic []string
	// output is what the line printed, kept for dependency analysis.
	output string
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

// markerLead is printed before every session marker. A documented command
// whose output ends without a trailing newline would otherwise leave the
// marker appended to that output, where the parser cannot see it.
const markerLead = `\n`

// lineTimeout bounds one wrapped example line, so a command that waits for
// input or serves forever cannot eat the whole session budget.
const lineTimeout = 90 * time.Second

// runExample replays a repo's example plan in one clean container: the
// documented binaries are installed, the repo tree is copied in, and every
// plan step runs in documented order in a single shell, so files and
// environment persist between blocks the way they do in a real terminal.
func (d *DockerRunner) runExample(ctx context.Context, step InstallStep) Result {
	plan := step.plan
	res := Result{Step: step}
	if plan == nil || len(plan.Steps) == 0 {
		res.Status = StatusSkipped
		res.Detail = "no example blocks found"
		return res
	}
	if len(plan.Installs) == 0 {
		res.Status = StatusSkipped
		res.Detail = "no documented install puts a binary on PATH; examples not run"
		return res
	}
	image, ok := d.exampleImage(plan)
	if !ok {
		res.Status = StatusSkipped
		res.Detail = "documented installs need more than one toolchain; no single image serves them"
		return res
	}

	script, wrapped := sessionScript(plan, int(d.Timeout.Seconds()), int(d.lineBudget().Seconds()))
	ctx, cancel := context.WithTimeout(ctx, sessionBudget(plan, d.Timeout))
	defer cancel()

	start := time.Now()
	name := containerName()
	args := append([]string{"run", "--rm", "-i", "--name", name},
		append(hardenedArgs(), "-e", "GOTOOLCHAIN=auto", image, "bash", "-c", script)...)
	cmd := exec.CommandContext(ctx, dockerBin(), args...)
	cmd.Cancel = removeContainerFunc(cmd, name)
	repo, truncated := repoTar(step.dir)
	if truncated {
		return Result{
			Step: step, Status: StatusError, Duration: time.Since(start),
			Detail: "repository too large to stream whole, so the examples have no verdict",
		}
	}
	cmd.Stdin = bytes.NewReader(repo)
	out, _ := cmd.CombinedOutput()
	if ctx.Err() != nil && errors.Is(context.Cause(ctx), context.Canceled) {
		return Result{
			Step: step, Status: StatusError, Duration: time.Since(start),
			Detail: "run was interrupted, so the examples have no verdict",
		}
	}
	res = classifyExample(step, plan, string(out), wrapped, time.Since(start), d.lineBudget())
	if image != d.Image {
		res.Image = image
	}
	return res
}

// exampleImage returns the image an example session runs in. Every documented
// install must be servable by one image, since the session is one shell: a
// project installed with both cargo and npm names two toolchains and is
// reported as a skip rather than run in an image missing one of them.
func (d *DockerRunner) exampleImage(plan *Plan) (string, bool) {
	eco := ""
	for _, in := range plan.Installs {
		if in.Ecosystem == "" {
			continue
		}
		if eco != "" && eco != in.Ecosystem {
			return "", false
		}
		eco = in.Ecosystem
	}
	if tc, known := toolchains[eco]; known && tc.Image != "" {
		return tc.Image, true
	}
	return d.Image, true
}

// redirectDirs returns the parent directories of a line's redirect targets,
// for targets nested under a directory. Docs redirect into standard user
// directories such as ~/.local/share that exist on real systems, so the
// session creates them rather than failing a correct document. Targets with
// expansions beyond a leading ~ are left alone, since their value is unknown.
func redirectDirs(flat string) []string {
	var out []string
	for _, m := range reCreatedToken.FindAllStringSubmatch(flat, -1) {
		for _, tok := range m[1:] {
			if tok == "" || !strings.Contains(tok, "/") {
				continue
			}
			dir := filepath.Dir(tok)
			if strings.ContainsAny(dir, "$`\"'") {
				continue
			}
			if strings.HasPrefix(dir, "~/") {
				dir = `"$HOME"/` + shellSafe(dir[2:])
			} else {
				dir = "'" + shellSafe(dir) + "'"
			}
			out = append(out, dir)
		}
	}
	return out
}

// sessionBudget bounds the whole example session: each module build gets the
// install timeout, each runnable line gets a share, and setup gets a grace
// period, capped so one repo cannot stall the run.
func sessionBudget(plan *Plan, install time.Duration) time.Duration {
	lines := 0
	for _, s := range plan.Steps {
		for _, l := range s.Lines {
			if l.Skip == "" {
				lines++
			}
		}
	}
	budget := time.Duration(len(plan.Installs))*install +
		time.Duration(lines)*20*time.Second + 3*time.Minute
	if budget > 20*time.Minute {
		budget = 20 * time.Minute
	}
	return budget
}

// sessionScript renders the plan as one bash script with markers the parent
// parses. It returns the script and the set of step:line keys that were
// wrapped in a line timeout, so a 124 exit can be read as a hang.
func sessionScript(plan *Plan, installSecs, lineSecs int) (string, map[string]bool) {
	wrapped := map[string]bool{}
	var b strings.Builder
	b.WriteString(`export GOBIN="$(go env GOPATH 2>/dev/null || echo /root/go)/bin"
export PATH="$GOBIN:$HOME/.local/bin:${CARGO_HOME:-$HOME/.cargo}/bin:$PATH"
export EDITOR=true VISUAL=true GIT_EDITOR=true
export GIT_TERMINAL_PROMPT=0
export DEBIAN_FRONTEND=noninteractive
mkdir -p "$GOBIN" /work/repo
tar -xf - -C /work/repo >/dev/null 2>&1 || true
exec </dev/null
cd /work/repo
git init -q >/dev/null 2>&1
git config user.email kibble@localhost >/dev/null 2>&1
git config user.name kibble >/dev/null 2>&1
__pipecode() {
  local last=${!#} i=1 c
  while [ "$i" -lt "$#" ]; do
    c=${!i}
    if [ "$c" -ne 0 ] && [ "$c" -ne 141 ]; then printf '%s' "$c"; return; fi
    i=$((i+1))
  done
  printf '%s' "$last"
}
export -f __pipecode
`)
	if len(plan.Packages) > 0 {
		fmt.Fprintf(&b, `apt-get update -qq >/dev/null 2>&1
apt-get install -y -qq --no-install-recommends %s >/dev/null 2>&1
printf '\nKIBBLE-PKGS CODE=%%d\n' "$?"
`, strings.Join(plan.Packages, " "))
	}
	for _, in := range plan.Installs {
		if in.bootstrap != "" {
			fmt.Fprintf(&b, "%s >/dev/null 2>&1 || true\n", in.bootstrap)
		}
		fmt.Fprintf(&b, `out=$(timeout %d bash -ec '%s' 2>&1); code=$?
printf '\nKIBBLE-BUILD CODE=%%d\n' "$code"
if [ "$code" -ne 0 ]; then printf '%%s\n' "$out" | tail -n 12; printf '\nKIBBLE-ABORT\n'; exit 0; fi
`, installSecs, shellSafe(in.Cmd))
	}
	if len(plan.Binaries) > 0 {
		var quoted []string
		for _, b := range plan.Binaries {
			quoted = append(quoted, "'"+shellSafe(b)+"'")
		}
		fmt.Fprintf(&b, `have=0
for kb in %s; do
  if command -v "$kb" >/dev/null 2>&1; then have=1; printf '\nKIBBLE-HAVE %%s\n' "$kb"; fi
done
if [ "$have" -eq 0 ]; then printf '\nKIBBLE-NOBIN\n'; exit 0; fi
`, strings.Join(quoted, " "))
	}
	for _, f := range plan.Fixtures {
		if dir := filepath.Dir(f.Path); dir != "." {
			fmt.Fprintf(&b, "mkdir -p '%s'\n", shellSafe(dir))
		}
		enc := base64.StdEncoding.EncodeToString([]byte(f.Contents))
		fmt.Fprintf(&b, "printf '%%s' '%s' | base64 -d > '%s'\n", enc, shellSafe(f.Path))
	}
	for _, k := range sortedKeys(plan.Env) {
		fmt.Fprintf(&b, "export %s='%s'\n", k, shellSafe(plan.Env[k]))
	}
	for _, s := range plan.Steps {
		fmt.Fprintf(&b, "printf '"+markerLead+"KIBBLE-STEP %s START\\n'\n", s.ID)
		if s.Background {
			writeBackgroundStep(&b, s)
			continue
		}
		for i, l := range s.Lines {
			if l.Skip != "" {
				fmt.Fprintf(&b, "printf '"+markerLead+"KIBBLE-LINE %s:%d SKIP\\n'\n", s.ID, i)
				continue
			}
			cmd := l.Cmd
			for _, dir := range redirectDirs(flatten(cmd)) {
				fmt.Fprintf(&b, "mkdir -p %s >/dev/null 2>&1 || true\n", dir)
			}
			switch {
			case isSimpleCommand(flatten(cmd)):
				cmd = fmt.Sprintf("timeout %d %s", lineSecs, cmd)
				wrapped[fmt.Sprintf("%s:%d", s.ID, i)] = true
			case isolatable(cmd):
				// The line runs in a shell of its own so timeout has
				// something to kill. Its pipeline status is reduced inside
				// that shell by the same rule the session uses outside, so
				// the exit reported means what an unwrapped line's would.
				cmd = fmt.Sprintf("timeout %d bash -c '%s\n__ps=(\"${PIPESTATUS[@]}\")\n"+
					"exit \"$(__pipecode \"${__ps[@]}\")\"'",
					lineSecs, shellSafe(cmd))
				wrapped[fmt.Sprintf("%s:%d", s.ID, i)] = true
			}
			b.WriteString(cmd + "\n")
			// The pipeline's status is captured before anything else runs, so a
			// producer that fails is not hidden by a consumer that exits zero.
			// A stage killed by SIGPIPE (141) is the benign case of a consumer
			// closing early, such as piping into head, and does not convict.
			b.WriteString("__ps=(\"${PIPESTATUS[@]}\")\n")
			fmt.Fprintf(&b,
				"printf '"+markerLead+"KIBBLE-LINE %s:%d CODE=%%d\\n' \"$(__pipecode \"${__ps[@]}\")\"\n", s.ID, i)
		}
	}
	b.WriteString(`[ -n "${KIBBLE_BG:-}" ] && kill $KIBBLE_BG >/dev/null 2>&1
printf '\nKIBBLE-DONE\n'
`)
	return b.String(), wrapped
}

// writeBackgroundStep renders a background step: its lines run in a subshell
// behind the session, and readiness is a log match when the plan names one.
//
// Each line records its own exit as it finishes. That matters because a
// background block is usually a short setup line or two followed by the one
// command that serves and never returns, and reporting the readiness result
// for all of them claimed evidence for lines nothing had checked: a setup
// line could fail outright while the service still came up, and the step
// reported every line as fine. Only the line still running when readiness is
// judged gets the readiness verdict now, and it is reported as readiness
// rather than as an exit code, because it never produced one.
func writeBackgroundStep(b *strings.Builder, s PlanStep) {
	log := "/tmp/kibble-" + s.ID + ".log"
	status := "/tmp/kibble-" + s.ID + ".status"
	fmt.Fprintf(b, ": > %s\n(\n", status)
	for i, l := range s.Lines {
		if l.Skip != "" {
			continue
		}
		b.WriteString(l.Cmd + "\n")
		fmt.Fprintf(b, "printf 'KIBBLE-BG %s:%d CODE=%%d\\n' \"$?\" >> %s\n", s.ID, i, status)
	}
	fmt.Fprintf(b, ") >%s 2>&1 &\nKIBBLE_BG=\"${KIBBLE_BG:-} $!\"\n", log)
	if s.ReadyLog != "" {
		fmt.Fprintf(b, `ready=1
for i in $(seq 1 30); do grep -q '%s' %s 2>/dev/null && ready=0 && break; sleep 1; done
`, shellSafe(s.ReadyLog), log)
	} else {
		b.WriteString("sleep 2\nready=0\n")
	}
	for i, l := range s.Lines {
		if l.Skip != "" {
			fmt.Fprintf(b, "printf '"+markerLead+"KIBBLE-LINE %s:%d SKIP\\n'\n", s.ID, i)
			continue
		}
		// A line that finished has its own exit. A line with none is the one
		// still running, and readiness is all that is known about it.
		fmt.Fprintf(b, `__c=$(sed -n 's/^KIBBLE-BG %s:%d CODE=//p' %s 2>/dev/null | tail -n1)
if [ -n "$__c" ]; then printf '`+markerLead+`KIBBLE-LINE %s:%d CODE=%%d\n' "$__c"
else printf '`+markerLead+`KIBBLE-LINE %s:%d BG=%%d\n' "$ready"; fi
`, s.ID, i, status, s.ID, i, s.ID, i)
	}
}

// shellSafe escapes single quotes for embedding inside a single-quoted
// shell string.
func shellSafe(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}

// sortedKeys returns a map's keys in stable order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isSimpleCommand reports whether a line is one plain command with no shell
// structure, so prefixing it with a timeout does not change what it means.
// Builtins and assignments must run in the session shell unwrapped. The
// judgment comes from a parse rather than a scan for metacharacters, which
// used to refuse `tool --name "a | b"` for a pipe inside a quoted argument.
func isSimpleCommand(flat string) bool {
	line, ok := parseShell(flat)
	if !ok {
		return false
	}
	return len(line.Cmds) == 1 && !line.Structured && !line.StateChanging &&
		!line.Background && !line.Heredoc && line.Cmds[0].Name() != ""
}

// isolatable reports whether a line that is not simple can still be run in
// its own shell, which is what lets it carry a timeout. Without this, a line
// with any shell structure had no bound at all: `tool serve | tee log` could
// spend the entire session budget, and every line behind it reported that the
// session ended rather than what it did.
//
// The limits are what a subshell costs. A line that changes the shell cannot
// be isolated, since a cd or an export performed in a subshell is discarded
// when it exits. A heredoc cannot, since rewriting one risks changing the
// data it carries. Nor can a line that expands a variable: an earlier
// documented line may have set it without exporting it, and a subshell would
// see an empty value and fail a document that works. Exporting those
// assignments would fix the visibility and change what the tools receive, so
// the narrower rule is the honest one.
func isolatable(cmd string) bool {
	line, ok := parseShell(cmd)
	if !ok {
		return false
	}
	if len(line.Cmds) == 0 || !line.Structured || line.StateChanging ||
		line.Background || line.Heredoc {
		return false
	}
	for _, c := range line.Cmds {
		if c.Expanded {
			return false
		}
	}
	return true
}

// lineOutcome is one parsed KIBBLE-LINE marker with the output that
// preceded it.
type lineOutcome struct {
	// code is the exit code, or -1 for a planned skip marker.
	code int
	// output is the text the line printed before its marker.
	output string
	// background marks a line still running when its step was judged, so it
	// never produced an exit code and ready carries what is known instead.
	background bool
	// ready reports whether the step reached its documented readiness signal.
	ready bool
}

// markerTail splits a line on a session marker, returning the output that
// preceded the marker, the text that followed it, and whether the marker is
// present. Markers are found anywhere in a line rather than only at its
// start, so a documented command whose output ends without a newline cannot
// hide the marker the session printed next.
func markerTail(line, marker string) (before, after string, ok bool) {
	i := strings.Index(line, marker)
	if i < 0 {
		return "", "", false
	}
	return line[:i], line[i+len(marker):], true
}

// classifyExample parses session output into a Result: per-line outcomes
// feed step results, and the worst outcome names the repo's example status.
func classifyExample(step InstallStep, plan *Plan, out string, wrapped map[string]bool,
	dur, lineBudget time.Duration) Result {
	res := Result{Step: step, Duration: dur}
	outcomes := map[string]lineOutcome{}
	have := map[string]bool{}
	var chunk []string
	aborted, done, noBin := false, false, false
	pkgCode := 0
	keep := func(s string) {
		if strings.TrimSpace(s) != "" && len(chunk) < 200 {
			chunk = append(chunk, s)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		switch pre, rest, ok := markerTail(line, "KIBBLE-HAVE "); {
		case strings.Contains(line, "KIBBLE-NOBIN"):
			noBin = true
		case ok:
			keep(pre)
			have[strings.TrimSpace(rest)] = true
		case strings.Contains(line, "KIBBLE-PKGS CODE="):
			_, code, _ := markerTail(line, "KIBBLE-PKGS CODE=")
			pkgCode, _ = strconv.Atoi(strings.TrimSpace(code))
			chunk = nil
		case strings.Contains(line, "KIBBLE-BUILD CODE="):
			chunk = nil
		case strings.Contains(line, "KIBBLE-ABORT"):
			aborted = true
		case strings.Contains(line, "KIBBLE-STEP "):
			chunk = nil
		case strings.Contains(line, "KIBBLE-DONE"):
			done = true
		case reLineMarker.MatchString(line):
			m := reLineMarker.FindStringSubmatch(line)
			keep(line[:len(line)-len(m[0])])
			o := lineOutcome{code: -1, output: strings.Join(chunk, "\n")}
			switch {
			case m[3] != "":
				o.code, _ = strconv.Atoi(m[3])
			case m[4] != "":
				ready, _ := strconv.Atoi(m[4])
				o.background = true
				o.ready = ready == 0
			}
			outcomes[m[1]+":"+m[2]] = o
			chunk = nil
		default:
			keep(line)
		}
	}
	if aborted {
		res.Status = StatusSkipped
		res.Detail = "documented install failed in the session; examples not run"
		return res
	}
	if noBin {
		res.Status = StatusSkipped
		res.Detail = "installed tool is not on PATH under any documented name; examples not run"
		return res
	}
	run, worst, detail := buildOutcomes(plan, outcomes, wrapped, done, have, lineBudget)
	res.example = run
	res.Status = worst
	res.Detail = detail
	if pkgCode != 0 && res.Status == StatusVerified {
		res.Detail += "; package install failed"
	}
	return res
}

// buildOutcomes walks the plan against the recorded markers, resolves
// failures that only depend on skipped lines, and returns the per-step
// outcomes with the aggregate status and its summary detail.
func buildOutcomes(plan *Plan, outcomes map[string]lineOutcome, wrapped map[string]bool,
	done bool, have map[string]bool, lineBudget time.Duration) (*exampleRun, Status, string) {
	run := &exampleRun{}
	documented := documentedSettings(plan)
	ended := false
	for _, s := range plan.Steps {
		es := exampleStep{ID: s.ID, Heading: s.Heading}
		for i, l := range s.Lines {
			key := fmt.Sprintf("%s:%d", s.ID, i)
			lr := lineResult{Cmd: flatten(l.Cmd), Code: -1, Line: l.Line, Synthetic: l.Synthetic}
			o, seen := outcomes[key]
			switch {
			case l.Skip != "":
				lr.Status = StatusSkipped
				if l.Gap {
					lr.Status = StatusGap
				}
				lr.Reason = l.SkipReason
				lr.Detail = l.Skip
			case !seen && (ended || done):
				// The session stopped before reaching this line. Kibble chose
				// nothing here, so the line is unestablished rather than
				// skipped: a run that spent its budget early must not report
				// the lines it never reached as deliberate.
				lr.Status = StatusBlocked
				lr.Reason = ReasonDependsOnSkipped
				lr.Detail = "not run: the session ended before reaching it"
			case !seen:
				lr.Status = StatusTimeout
				lr.Detail = "session ended while this line ran"
				ended = true
			default:
				lr = classifyLineResult(lr, l, o, wrapped[key], documented, lineBudget)
			}
			es.Lines = append(es.Lines, lr)
		}
		run.Steps = append(run.Steps, es)
	}
	resolveMissingBinaries(run, plan, have)
	resolveDependentFailures(run, plan)
	status, detail := summarize(run)
	return run, status, detail
}

// resolveMissingBinaries downgrades failures on lines that invoke a
// documented binary the session does not have. A README can document several
// tools while its install provides one, as a conda alternative next to a
// cargo install, and a line calling the absent one says nothing about the
// docs being wrong.
func resolveMissingBinaries(run *exampleRun, plan *Plan, have map[string]bool) {
	if len(have) == 0 {
		return
	}
	bins := map[string]bool{}
	for _, b := range plan.Binaries {
		bins[b] = true
	}
	for si := range run.Steps {
		s := &run.Steps[si]
		for li := range s.Lines {
			l := &s.Lines[li]
			if l.Status != StatusFail {
				continue
			}
			if bin, _ := invokedBinary(l.Cmd, bins); bin != "" && !have[bin] {
				l.Status = StatusSkipped
				l.Detail = fmt.Sprintf("invokes %s, which the documented install does not provide", bin)
			}
		}
	}
}

// resolveDependentFailures downgrades failures that may follow from lines
// which never ran: a failure whose output names such a command, and a failure
// in the same step and subcommand family as an earlier one. Neither
// observation is causal. A tool prints its own name in hints that have
// nothing to do with why it failed, and a second invocation of a subcommand
// is usually independent of the first. What the observation supports is that
// the session is no longer a clean test of this line, which is why these
// become blocked rather than skipped: the cascade stops being reported as
// several broken lines without any of them being called fine. A gap counts as
// not having run, since the document's own hole stopped the line.
func resolveDependentFailures(run *exampleRun, plan *Plan) {
	bins := map[string]bool{}
	for _, b := range plan.Binaries {
		bins[b] = true
	}
	// Commands that ran and passed, so a failure naming one of them is not
	// excused by it.
	passed := map[string]bool{}
	for _, s := range run.Steps {
		for _, l := range s.Lines {
			if l.Status != StatusVerified {
				continue
			}
			if bin, sub := invokedBinary(l.Cmd, bins); bin != "" && sub != "" {
				passed[bin+" "+sub] = true
			}
		}
	}
	var skippedCmds []string
	for _, s := range run.Steps {
		for _, l := range s.Lines {
			if l.Status != StatusSkipped && l.Status != StatusGap {
				continue
			}
			if bin, sub := invokedBinary(l.Cmd, bins); bin != "" && sub != "" {
				skippedCmds = append(skippedCmds, bin+" "+sub)
			}
		}
	}
	for si := range run.Steps {
		s := &run.Steps[si]
		for li := range s.Lines {
			l := &s.Lines[li]
			if l.Status != StatusFail {
				continue
			}
			if cited := citedSkipped(l.output, skippedCmds); cited != "" {
				l.Status = StatusBlocked
				l.Reason = ReasonDependsOnSkipped
				l.Detail = fmt.Sprintf("failed naming `%s`, which did not run", cited)
				continue
			}
			if prior := earlierSkipInFamily(s.Lines[:li], l.Cmd, bins); prior != "" {
				l.Status = StatusBlocked
				l.Reason = ReasonDependsOnSkipped
				l.Detail = fmt.Sprintf("failed after `%s` did not run", prior)
				continue
			}
			if need := namedSiblingNotRun(l.output, bins, passed); need != "" {
				l.Status = StatusBlocked
				l.Reason = ReasonDependsOnSkipped
				l.Detail = fmt.Sprintf("says to run `%s` first, which did not run", need)
			}
		}
	}
}

// citedSkipped returns the first skipped command a failure's output names.
func citedSkipped(output string, skippedCmds []string) string {
	for _, c := range skippedCmds {
		if strings.Contains(output, c) {
			return c
		}
	}
	return ""
}

// earlierSkipInFamily returns the command of an earlier skipped line that
// shares the failing line's binary and subcommand, or empty when none does.
func earlierSkipInFamily(prior []lineResult, cmd string, bins map[string]bool) string {
	bin, sub := invokedBinary(cmd, bins)
	if bin == "" || sub == "" {
		return ""
	}
	for _, p := range prior {
		if p.Status != StatusSkipped && p.Status != StatusGap {
			continue
		}
		if pb, ps := invokedBinary(p.Cmd, bins); pb == bin && ps == sub {
			return flatten(p.Cmd)
		}
	}
	return ""
}

// summarize reduces per-line outcomes to the aggregate status and detail:
// the first failure names the broken line, a timeout names the hang, a pass
// counts coverage, and a run with nothing to do says why. Blocked lines are
// counted and reported but do not outrank a pass, since a session that
// verified lines did verify them; what they must never do is disappear, so
// the count travels with every summary that has one.
func summarize(run *exampleRun) (Status, string) {
	ran, skipped, gaps, blocked, synthetic := 0, 0, 0, 0, 0
	var firstFail, firstTimeout, firstSkip, firstGap, firstBlocked string
	for _, s := range run.Steps {
		for _, l := range s.Lines {
			switch l.Status {
			case StatusVerified:
				ran++
				if len(l.Synthetic) > 0 {
					synthetic++
				}
			case StatusSkipped:
				skipped++
				if firstSkip == "" {
					firstSkip = l.Detail
				}
			case StatusBlocked:
				blocked++
				if firstBlocked == "" {
					firstBlocked = fmt.Sprintf("%s %q %s", s.ID, l.Cmd, l.Detail)
				}
			case StatusGap:
				gaps++
				if firstGap == "" {
					firstGap = fmt.Sprintf("%s %q %s", s.ID, l.Cmd, l.Detail)
				}
			case StatusTimeout:
				if firstTimeout == "" {
					firstTimeout = fmt.Sprintf("%s %q %s", s.ID, l.Cmd, l.Detail)
				}
			case StatusFail:
				if firstFail == "" {
					firstFail = fmt.Sprintf("%s %q %s", s.ID, l.Cmd, l.Detail)
				}
			}
		}
	}
	tally := fmt.Sprintf("%d lines ran, %d skipped", ran, skipped)
	if blocked > 0 {
		tally += fmt.Sprintf(", %d blocked", blocked)
	}
	if synthetic > 0 {
		// Named on the summary line rather than only in the JSON, since the
		// summary is what a reader actually reads before believing the green.
		tally += fmt.Sprintf(" (%d against fabricated files)", synthetic)
	}
	switch {
	case firstFail != "":
		return StatusFail, firstFail
	case firstTimeout != "":
		return StatusTimeout, firstTimeout
	case gaps > 0:
		return StatusGap, fmt.Sprintf("%d %s, first: %s",
			gaps, plural(gaps, "documentation gap", "documentation gaps"), firstGap)
	case ran > 0:
		return StatusVerified, tally
	case blocked > 0:
		// Nothing was verified and something was tried without settling. The
		// session has no verdict on this document and must not imply one.
		return StatusBlocked, fmt.Sprintf("%s, first: %s", tally, firstBlocked)
	default:
		detail := "no lines runnable"
		if firstSkip != "" {
			detail += ": " + firstSkip
		}
		return StatusSkipped, detail
	}
}

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
	if words, ok := shellArgWords(cmd); ok {
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

// tarSkipDirs are directories a build produces or a package manager fills.
// Streaming them wastes the budget on output nobody documents, and on a big
// repository it pushes the source a document needs past the cap.
var tarSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, "out": true, ".gradle": true, ".idea": true,
	".next": true, ".venv": true, "__pycache__": true, "coverage": true,
	".terraform": true, ".tox": true, ".pytest_cache": true, ".mypy_cache": true,
}

// repoTar packs the repo working tree for the session, without generated
// directories and without files over 2 MB, so documented example files exist
// in the container. The stream is capped at 20 MB. Truncation is reported
// rather than absorbed: a repository that arrives incomplete makes a document
// look broken when the missing piece is kibble's, and a verdict on that is
// worse than no verdict.
func repoTar(dir string) ([]byte, bool) {
	var buf bytes.Buffer
	if dir == "" {
		return buf.Bytes(), false
	}
	truncated := false
	tw := tar.NewWriter(&buf)
	total := int64(0)
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if tarSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > 2<<20 {
			return nil
		}
		if total += info.Size(); total > 20<<20 {
			truncated = true
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		hdr := &tar.Header{
			Name: filepath.ToSlash(rel),
			Mode: int64(info.Mode().Perm()),
			Size: int64(len(b)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return filepath.SkipAll
		}
		if _, err := tw.Write(b); err != nil {
			return filepath.SkipAll
		}
		return nil
	})
	_ = tw.Close()
	return buf.Bytes(), truncated
}

// plural picks the singular or plural word for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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

// namedSiblingNotRun returns a command of the same binary that a failure's
// own output tells the reader to run, when that command never ran in this
// session. A tool naming its own prerequisite is describing session state,
// not a hole in the document: the document did run the equivalent step, and
// the session skipped it for a reason it already reported.
func namedSiblingNotRun(output string, bins map[string]bool, passed map[string]bool) string {
	for bin := range bins {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(bin) + `\s+([a-z][a-z0-9_-]+)`)
		for _, m := range re.FindAllStringSubmatch(output, -1) {
			cmd := bin + " " + m[1]
			if !passed[cmd] {
				return cmd
			}
		}
	}
	return ""
}
