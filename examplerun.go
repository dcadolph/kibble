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
	// Code is the exit code the line returned, or -1 when it never ran.
	Code int
	// Detail explains a skip, failure, or documented nonzero exit.
	Detail string
	// Line is the 1-based README line the command sits on, 0 when unknown.
	Line int
	// output is what the line printed, kept for dependency analysis.
	output string
}

var (
	// reTTYErr matches errors that mean the command needed a terminal.
	reTTYErr = regexp.MustCompile(
		`(?i)/dev/tty|not a (tty|terminal)|terminal is required|requires a terminal|no tty`)
	// reCredErr matches errors that mean the command needed credentials a
	// clean container cannot have.
	reCredErr = regexp.MustCompile(
		`(?i)api[_ ]?key|credential|unauthorized|forbidden|\b401\b|\b403\b` +
			`|not (logged|signed) in|\blog ?in\b|authenticat|missing (token|key)`)
	// reSettingName matches an environment setting a tool names in its own
	// error, such as VAMOOSE_CLIENT_ID or VAMOOSE_BAMBOOHR_*. Matching the
	// shape rather than a list of suffixes keeps the rule from needing a new
	// entry for every credential a tool invents.
	reSettingName = regexp.MustCompile(`\b[A-Z][A-Z0-9]{2,}(_[A-Z0-9*]+)+\b`)
	// reMissingPhrase matches the wording that says a required setting is
	// absent. The name alone is not enough: a document may legitimately print
	// a variable it already set, so the tool must also say it is missing.
	reMissingPhrase = regexp.MustCompile(
		`(?i)\b(not set|unset|is required|are required|missing|not configured` +
			`|no .{0,20}configured|please set|must set|\bset\b .{0,20}or\b)\b`)
	// reNetErr matches errors that mean the command needed a network
	// service the container does not run.
	reNetErr = regexp.MustCompile(
		`(?i)connection refused|no such host|dial tcp|network is unreachable|could not connect|cannot connect`)
	// reNoData matches a query that ran correctly and found nothing, which a
	// fresh session often cannot avoid: the docs query dates and terms that
	// have no entries yet.
	reNoData = regexp.MustCompile(
		`(?i)\bno (entries|results|matches|data|records)\b|\bfound no\b` +
			`|\bnothing (found|to (show|report))\b|\bno \w+(\s\w+)? found\b`)
	// reEmptyInput matches a command that rejected the empty input the
	// session's stubbed editor produced.
	reEmptyInput = regexp.MustCompile(
		`(?i)\b(entry|body|input|message|text) is empty\b|\bempty (entry|body|input|message)\b`)
	// reNoExec matches the Go exec error for a missing helper program.
	reNoExec = regexp.MustCompile(`executable file not found`)
	// reMissingDep matches a tool reporting that a system dependency it needs
	// is absent, such as vhs requiring ffmpeg. The dependency is the container's
	// gap, not the document's, so the line is skipped.
	reMissingDep = regexp.MustCompile(
		`(?i)\bis not installed\b|\b(please|must) install\b|\bnot found in (your )?\$?PATH\b` +
			`|\brequires? \S+ to be installed\b`)
	// reLineMarker parses a KIBBLE-LINE marker into step ID, line index,
	// and either an exit code or the SKIP token. The marker is matched
	// anywhere in a line rather than only at its start, so a documented
	// command whose output ends without a newline cannot swallow the marker
	// that follows it and turn a real result into a missing one.
	reLineMarker = regexp.MustCompile(`KIBBLE-LINE (\S+):(\d+) (?:CODE=(-?\d+)|SKIP)$`)
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

	script, wrapped := sessionScript(plan, int(d.Timeout.Seconds()))
	ctx, cancel := context.WithTimeout(ctx, sessionBudget(plan, d.Timeout))
	defer cancel()

	start := time.Now()
	name := containerName()
	cmd := exec.CommandContext(ctx,
		"docker", "run", "--rm", "-i", "--name", name, image, "bash", "-c", script)
	cmd.Cancel = removeContainerFunc(cmd, name)
	cmd.Stdin = bytes.NewReader(repoTar(step.dir))
	out, _ := cmd.CombinedOutput()
	if ctx.Err() != nil && errors.Is(context.Cause(ctx), context.Canceled) {
		return Result{
			Step: step, Status: StatusError, Duration: time.Since(start),
			Detail: "run was interrupted, so the examples have no verdict",
		}
	}
	res = classifyExample(step, plan, string(out), wrapped, time.Since(start))
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
func sessionScript(plan *Plan, installSecs int) (string, map[string]bool) {
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
if [ "$code" -ne 0 ]; then printf '%%s\n' "$out" | tail -n 3; printf '\nKIBBLE-ABORT\n'; exit 0; fi
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
			if isSimpleCommand(flatten(cmd)) {
				cmd = fmt.Sprintf("timeout %d %s", int(lineTimeout.Seconds()), cmd)
				wrapped[fmt.Sprintf("%s:%d", s.ID, i)] = true
			}
			b.WriteString(cmd + "\n")
			fmt.Fprintf(&b,
				"printf '"+markerLead+"KIBBLE-LINE %s:%d CODE=%%d\\n' \"$?\"\n", s.ID, i)
		}
	}
	b.WriteString(`[ -n "${KIBBLE_BG:-}" ] && kill $KIBBLE_BG >/dev/null 2>&1
printf '\nKIBBLE-DONE\n'
`)
	return b.String(), wrapped
}

// writeBackgroundStep renders a background step: its lines run in a
// subshell behind the session, readiness is a log match when the plan names
// one, and every runnable line shares the readiness result.
func writeBackgroundStep(b *strings.Builder, s PlanStep) {
	log := "/tmp/kibble-" + s.ID + ".log"
	b.WriteString("(\n")
	for _, l := range s.Lines {
		if l.Skip == "" {
			b.WriteString(l.Cmd + "\n")
		}
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
		fmt.Fprintf(b,
			"printf '"+markerLead+"KIBBLE-LINE %s:%d CODE=%%d\\n' \"$ready\"\n", s.ID, i)
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

// isSimpleCommand reports whether a flattened line is one plain command
// with no shell structure, so a timeout wrapper does not change what it
// means. Builtins and assignments must run in the session shell unwrapped.
func isSimpleCommand(flat string) bool {
	if strings.ContainsAny(flat, "|;&<>`#") || strings.Contains(flat, "$(") {
		return false
	}
	fields := strings.Fields(flat)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "cd", "export", "source", ".", "eval", "unset", "alias":
		return false
	}
	return !reAssignPrefix.MatchString(flat)
}

// lineOutcome is one parsed KIBBLE-LINE marker with the output that
// preceded it.
type lineOutcome struct {
	// code is the exit code, or -1 for a planned skip marker.
	code int
	// output is the text the line printed before its marker.
	output string
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
	dur time.Duration) Result {
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
			code := -1
			if m[3] != "" {
				code, _ = strconv.Atoi(m[3])
			}
			outcomes[m[1]+":"+m[2]] = lineOutcome{code: code, output: strings.Join(chunk, "\n")}
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
	run, worst, detail := buildOutcomes(plan, outcomes, wrapped, done, have)
	res.example = run
	res.Status = worst
	res.Detail = detail
	if pkgCode != 0 && res.Status == StatusPass {
		res.Detail += "; package install failed"
	}
	return res
}

// buildOutcomes walks the plan against the recorded markers, resolves
// failures that only depend on skipped lines, and returns the per-step
// outcomes with the aggregate status and its summary detail.
func buildOutcomes(plan *Plan, outcomes map[string]lineOutcome, wrapped map[string]bool,
	done bool, have map[string]bool) (*exampleRun, Status, string) {
	run := &exampleRun{}
	documented := documentedSettings(plan)
	ended := false
	for _, s := range plan.Steps {
		es := exampleStep{ID: s.ID, Heading: s.Heading}
		for i, l := range s.Lines {
			key := fmt.Sprintf("%s:%d", s.ID, i)
			lr := lineResult{Cmd: flatten(l.Cmd), Code: -1, Line: l.Line}
			o, seen := outcomes[key]
			switch {
			case l.Skip != "":
				lr.Status = StatusSkipped
				if l.Gap {
					lr.Status = StatusGap
				}
				lr.Detail = l.Skip
			case !seen && (ended || done):
				lr.Status = StatusSkipped
				lr.Detail = "not run: session ended earlier"
			case !seen:
				lr.Status = StatusTimeout
				lr.Detail = "session ended while this line ran"
				ended = true
			default:
				lr = classifyLineResult(lr, l, o, wrapped[key], documented)
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

// resolveDependentFailures downgrades failures caused by lines that never
// ran: a failure whose output names such a command needed it, and a failure
// in the same step and subcommand family as an earlier one follows a recipe
// the session could not fully run. Both are skips, not documentation
// failures. A gap counts as not having run, since the document's own hole
// stopped the line, and reporting the same hole once per dependent command
// would bury the one line worth fixing.
func resolveDependentFailures(run *exampleRun, plan *Plan) {
	bins := map[string]bool{}
	for _, b := range plan.Binaries {
		bins[b] = true
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
				l.Status = StatusSkipped
				l.Detail = fmt.Sprintf("needs `%s`, which did not run", cited)
				continue
			}
			if prior := earlierSkipInFamily(s.Lines[:li], l.Cmd, bins); prior != "" {
				l.Status = StatusSkipped
				l.Detail = fmt.Sprintf("follows `%s`, which did not run", prior)
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
// counts coverage, and a run with nothing to do says why.
func summarize(run *exampleRun) (Status, string) {
	ran, skipped, gaps := 0, 0, 0
	var firstFail, firstTimeout, firstSkip, firstGap string
	for _, s := range run.Steps {
		for _, l := range s.Lines {
			switch l.Status {
			case StatusPass:
				ran++
			case StatusSkipped:
				skipped++
				if firstSkip == "" {
					firstSkip = l.Detail
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
	switch {
	case firstFail != "":
		return StatusFail, firstFail
	case firstTimeout != "":
		return StatusTimeout, firstTimeout
	case gaps > 0:
		return StatusGap, fmt.Sprintf("%d %s, first: %s",
			gaps, plural(gaps, "documentation gap", "documentation gaps"), firstGap)
	case ran > 0:
		return StatusPass, fmt.Sprintf("%d lines ran, %d skipped", ran, skipped)
	default:
		detail := "no lines runnable"
		if firstSkip != "" {
			detail += ": " + firstSkip
		}
		return StatusSkipped, detail
	}
}

// classifyLineResult turns one recorded exit into a line result. Errors that
// only mean the container lacks a terminal, credentials, a network service,
// a helper command, or data are honest skips, not documentation failures.
func classifyLineResult(lr lineResult, l PlanLine, o lineOutcome, wrapped bool,
	documented map[string]bool) lineResult {
	lr.Code = o.code
	tail := lastLine(strings.Split(o.output, "\n"))
	switch {
	case wrapped && o.code == 124:
		lr.Status = StatusTimeout
		lr.Detail = fmt.Sprintf("gave no result within %s", lineTimeout)
	case o.code == 0:
		lr.Status = StatusPass
	case l.NonzeroOK:
		lr.Status = StatusPass
		lr.Detail = fmt.Sprintf("exit %d is documented behavior", o.code)
	case o.code == 127 || strings.Contains(o.output, "command not found") ||
		reNoExec.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Detail = "invokes a command the container lacks: " + tail
	case reMissingDep.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Detail = "needs a system dependency the container lacks: " + tail
	case reTTYErr.MatchString(o.output):
		lr.Status = StatusSkipped
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
		lr.Detail = "needs a setting the reader supplies: " + tail
	case reCredErr.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Detail = "needs credentials a clean container lacks"
	case reNetErr.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Detail = "needs a network service the container lacks"
	case reNoData.MatchString(o.output) || tail == "not found":
		lr.Status = StatusSkipped
		lr.Detail = "query found no data in the fresh session"
	case reEmptyInput.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Detail = "rejected the empty input of the session's stubbed editor"
	default:
		lr.Status = StatusFail
		lr.Detail = fmt.Sprintf("exited %d: %s", o.code, tail)
	}
	lr.output = o.output
	return lr
}

// repoTar packs the repo working tree for the session, without .git and
// without files over 2 MB, so documented example files exist in the
// container. The stream is capped at 20 MB; a huge repo arrives truncated
// and any file beyond the cap reads as missing, which is honest.
func repoTar(dir string) []byte {
	var buf bytes.Buffer
	if dir == "" {
		return buf.Bytes()
	}
	tw := tar.NewWriter(&buf)
	total := int64(0)
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
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
	return buf.Bytes()
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
// document covers them all. A wildcard such as VAMOOSE_BAMBOOHR_* counts as
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
