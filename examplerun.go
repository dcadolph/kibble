package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
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
	// reNetErr matches errors that mean the command needed a network
	// service the container does not run.
	reNetErr = regexp.MustCompile(
		`(?i)connection refused|no such host|dial tcp|network is unreachable|could not connect`)
	// reNoData matches a query that ran correctly and found nothing, which a
	// fresh session often cannot avoid: the docs query dates and terms that
	// have no entries yet.
	reNoData = regexp.MustCompile(
		`(?i)\bno (entries|results|matches|data|records)\b|\bfound no\b|\bnothing (found|to (show|report))\b`)
	// reEmptyInput matches a command that rejected the empty input the
	// session's stubbed editor produced.
	reEmptyInput = regexp.MustCompile(
		`(?i)\b(entry|body|input|message|text) is empty\b|\bempty (entry|body|input|message)\b`)
	// reNoExec matches the Go exec error for a missing helper program.
	reNoExec = regexp.MustCompile(`executable file not found`)
	// reLineMarker parses a KIBBLE-LINE marker into step ID, line index,
	// and either an exit code or the SKIP token.
	reLineMarker = regexp.MustCompile(`^KIBBLE-LINE (\S+):(\d+) (?:CODE=(-?\d+)|SKIP)$`)
)

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
	if len(plan.Modules) == 0 {
		res.Status = StatusSkipped
		res.Detail = "no go install step puts a binary on PATH; examples not run"
		return res
	}

	script, wrapped := sessionScript(plan, int(d.Timeout.Seconds()))
	ctx, cancel := context.WithTimeout(ctx, sessionBudget(plan, d.Timeout))
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-i", d.Image, "bash", "-c", script)
	cmd.Stdin = bytes.NewReader(repoTar(step.dir))
	out, _ := cmd.CombinedOutput()
	return classifyExample(step, plan, string(out), wrapped, time.Since(start))
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
	budget := time.Duration(len(plan.Modules))*install +
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
	b.WriteString(`export GOBIN=/root/gobin
export PATH="$GOBIN:$PATH"
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
printf 'KIBBLE-PKGS CODE=%%d\n' "$?"
`, strings.Join(plan.Packages, " "))
	}
	for _, mod := range plan.Modules {
		fmt.Fprintf(&b, `out=$(timeout %d go install '%s' 2>&1); code=$?
printf 'KIBBLE-BUILD CODE=%%d\n' "$code"
if [ "$code" -ne 0 ]; then printf '%%s\n' "$out" | tail -n 3; printf 'KIBBLE-ABORT\n'; exit 0; fi
`, installSecs, mod)
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
		fmt.Fprintf(&b, "printf 'KIBBLE-STEP %s START\\n'\n", s.ID)
		if s.Background {
			writeBackgroundStep(&b, s)
			continue
		}
		for i, l := range s.Lines {
			if l.Skip != "" {
				fmt.Fprintf(&b, "printf 'KIBBLE-LINE %s:%d SKIP\\n'\n", s.ID, i)
				continue
			}
			cmd := l.Cmd
			if isSimpleCommand(flatten(cmd)) {
				cmd = fmt.Sprintf("timeout %d %s", int(lineTimeout.Seconds()), cmd)
				wrapped[fmt.Sprintf("%s:%d", s.ID, i)] = true
			}
			b.WriteString(cmd + "\n")
			fmt.Fprintf(&b, "printf 'KIBBLE-LINE %s:%d CODE=%%d\\n' \"$?\"\n", s.ID, i)
		}
	}
	b.WriteString(`[ -n "${KIBBLE_BG:-}" ] && kill $KIBBLE_BG >/dev/null 2>&1
printf 'KIBBLE-DONE\n'
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
			fmt.Fprintf(b, "printf 'KIBBLE-LINE %s:%d SKIP\\n'\n", s.ID, i)
			continue
		}
		fmt.Fprintf(b, "printf 'KIBBLE-LINE %s:%d CODE=%%d\\n' \"$ready\"\n", s.ID, i)
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

// classifyExample parses session output into a Result: per-line outcomes
// feed step results, and the worst outcome names the repo's example status.
func classifyExample(step InstallStep, plan *Plan, out string, wrapped map[string]bool,
	dur time.Duration) Result {
	res := Result{Step: step, Duration: dur}
	outcomes := map[string]lineOutcome{}
	var chunk []string
	aborted, done := false, false
	pkgCode := 0
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "KIBBLE-PKGS CODE="):
			pkgCode, _ = strconv.Atoi(strings.TrimPrefix(line, "KIBBLE-PKGS CODE="))
			chunk = nil
		case strings.HasPrefix(line, "KIBBLE-BUILD CODE="):
			chunk = nil
		case strings.HasPrefix(line, "KIBBLE-ABORT"):
			aborted = true
		case strings.HasPrefix(line, "KIBBLE-STEP "):
			chunk = nil
		case strings.HasPrefix(line, "KIBBLE-DONE"):
			done = true
		case reLineMarker.MatchString(line):
			m := reLineMarker.FindStringSubmatch(line)
			code := -1
			if m[3] != "" {
				code, _ = strconv.Atoi(m[3])
			}
			outcomes[m[1]+":"+m[2]] = lineOutcome{code: code, output: strings.Join(chunk, "\n")}
			chunk = nil
		default:
			if strings.TrimSpace(line) != "" && len(chunk) < 200 {
				chunk = append(chunk, line)
			}
		}
	}
	if aborted {
		res.Status = StatusSkipped
		res.Detail = "documented install failed in the session; examples not run"
		return res
	}
	run, worst, detail := buildOutcomes(plan, outcomes, wrapped, done)
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
	done bool) (*exampleRun, Status, string) {
	run := &exampleRun{}
	ended := false
	for _, s := range plan.Steps {
		es := exampleStep{ID: s.ID, Heading: s.Heading}
		for i, l := range s.Lines {
			key := fmt.Sprintf("%s:%d", s.ID, i)
			lr := lineResult{Cmd: flatten(l.Cmd), Code: -1}
			o, seen := outcomes[key]
			switch {
			case l.Skip != "":
				lr.Status = StatusSkipped
				lr.Detail = l.Skip
			case !seen && (ended || done):
				lr.Status = StatusSkipped
				lr.Detail = "not run: session ended earlier"
			case !seen:
				lr.Status = StatusTimeout
				lr.Detail = "session ended while this line ran"
				ended = true
			default:
				lr = classifyLineResult(lr, l, o, wrapped[key])
			}
			es.Lines = append(es.Lines, lr)
		}
		run.Steps = append(run.Steps, es)
	}
	resolveDependentFailures(run, plan)
	status, detail := summarize(run)
	return run, status, detail
}

// resolveDependentFailures downgrades failures caused by skipped lines: a
// failure whose output names a skipped command needed that command, and a
// failure in the same step and subcommand family as an earlier skipped line
// follows a recipe the session could not fully run. Both are skips, not
// documentation failures.
func resolveDependentFailures(run *exampleRun, plan *Plan) {
	bins := map[string]bool{}
	for _, b := range plan.Binaries {
		bins[b] = true
	}
	var skippedCmds []string
	for _, s := range run.Steps {
		for _, l := range s.Lines {
			if l.Status != StatusSkipped {
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
				l.Detail = fmt.Sprintf("needs `%s`, which was skipped", cited)
				continue
			}
			if prior := earlierSkipInFamily(s.Lines[:li], l.Cmd, bins); prior != "" {
				l.Status = StatusSkipped
				l.Detail = fmt.Sprintf("follows `%s`, which was skipped", prior)
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
		if p.Status != StatusSkipped {
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
	ran, skipped := 0, 0
	var firstFail, firstTimeout, firstSkip string
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
func classifyLineResult(lr lineResult, l PlanLine, o lineOutcome, wrapped bool) lineResult {
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
	case reTTYErr.MatchString(o.output):
		lr.Status = StatusSkipped
		lr.Detail = "needs a terminal, which the container lacks"
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
