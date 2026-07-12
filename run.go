package main

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Status is the outcome classification of an install attempt.
type Status string

const (
	// StatusPass means the tool built and the binary responded to a smoke test.
	StatusPass Status = "PASS"
	// StatusPassBuild means it built but the smoke test exited non-zero.
	StatusPassBuild Status = "PASS-BUILD"
	// StatusTimeout means the build exceeded the timeout, so the result is unknown.
	StatusTimeout Status = "TIMEOUT"
	// StatusFail means the documented install failed to build.
	StatusFail Status = "FAIL"
	// StatusSkipped means v1 does not execute this kind of step.
	StatusSkipped Status = "SKIP"
	// StatusDrift means the docs cite a flag or subcommand the binary lacks.
	StatusDrift Status = "DRIFT"
)

// Result is the outcome of attempting one install step.
type Result struct {
	// Step is the install step that was attempted.
	Step InstallStep
	// Status is the outcome classification.
	Status Status
	// Duration is how long the attempt took.
	Duration time.Duration
	// SmokeLine is the first line the installed binary printed.
	SmokeLine string
	// Detail carries the error tail on failure or a note otherwise.
	Detail string
	// helpText is the help output collected for flag checking.
	helpText string
	// example carries per-line outcomes for an example session.
	example *exampleRun
}

// Runner executes an install step in an isolated environment.
type Runner interface {
	// Run attempts the step and returns its result.
	Run(ctx context.Context, step InstallStep) Result
}

// DockerRunner runs install steps in a clean container.
type DockerRunner struct {
	// Image is the container image used for each install.
	Image string
	// Timeout is the per-step build timeout.
	Timeout time.Duration
	// Fetch checks URLs for the brew formula verification. When nil, a
	// default HTTP client is used.
	Fetch Fetcher
}

// Run executes the step: go-install and git-clone run in a fresh container
// and smoke-test the result, brew is verified without installing, and
// example steps replay the README's example blocks in one session.
func (d *DockerRunner) Run(ctx context.Context, step InstallStep) Result {
	var script string
	switch step.Kind {
	case "example":
		return d.runExample(ctx, step)
	case "brew":
		fetch := d.Fetch
		if fetch == nil {
			fetch = defaultFetcher()
		}
		start := time.Now()
		res := checkBrew(step, fetch)
		res.Duration = time.Since(start)
		return res
	case "git-clone":
		script = cloneScriptFor(step, int(d.Timeout.Seconds()))
	default:
		script = fmt.Sprintf(installScript, int(d.Timeout.Seconds()), step.Module, step.Binary)
		script += helpProbe(step)
	}

	ctx, cancel := context.WithTimeout(ctx, d.Timeout+60*time.Second)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", d.Image, "bash", "-c", script)
	out, _ := cmd.CombinedOutput()
	return classify(step, string(out), time.Since(start))
}

// reSSHRemote matches a GitHub SSH remote such as git@github.com:owner/repo.git.
var reSSHRemote = regexp.MustCompile(`git@github\.com:([\w.-]+)/([\w.-]+?)(\.git)?(\s|$)`)

// rewriteSSH converts GitHub SSH remotes to HTTPS, since a clean container
// has no SSH key and a public repository clones fine without one.
func rewriteSSH(line string) string {
	return reSSHRemote.ReplaceAllString(line, "https://github.com/$1/$2.git$4")
}

// cloneScriptFor builds the container script for a git-clone install recipe:
// the documented lines run in order, and whatever binary lands in GOBIN is
// smoke-tested.
func cloneScriptFor(step InstallStep, timeoutSecs int) string {
	recipe := make([]string, 0, len(step.Block))
	for _, l := range step.Block {
		recipe = append(recipe, rewriteSSH(l))
	}
	body := strings.Join(recipe, "\n")
	body = strings.ReplaceAll(body, "'", `'\''`)
	return fmt.Sprintf(cloneScript, timeoutSecs, body, step.Repo)
}

// cloneScript runs a documented clone recipe and smoke-tests the result. It
// prints the same markers as installScript, plus NOBIN when the recipe
// produced no binary to test.
const cloneScript = `set -u
export GOBIN=/root/gobin
mkdir -p "$GOBIN" /work
cd /work
out=$(timeout %[1]d bash -ec '%[2]s' 2>&1); code=$?
if [ "$code" -ne 0 ]; then
  printf 'BUILDCODE=%%d\n' "$code"
  printf '%%s\n' "$out" | tail -n 3
  exit 0
fi
printf 'BUILDCODE=0\n'
bin=''
if [ -x "$GOBIN/%[3]s" ]; then bin="$GOBIN/%[3]s"; else bin=$(ls "$GOBIN" 2>/dev/null | head -n1); [ -n "$bin" ] && bin="$GOBIN/$bin"; fi
if [ -z "$bin" ]; then
  printf 'NOBIN=1\n'
  exit 0
fi
sout=$(timeout 15 "$bin" --version 2>&1); scode=$?
if [ "$scode" -ne 0 ]; then sout=$(timeout 15 "$bin" --help 2>&1); scode=$?; fi
printf 'SMOKECODE=%%d\n' "$scode"
printf 'SMOKELINE=%%s\n' "$(printf '%%s' "$sout" | head -n1 | cut -c1-70)"
`

// helpProbe returns the script section that collects the binary's help
// screens for flag checking. Cited subcommands are probed too, capped and
// restricted to safe names so nothing unexpected reaches the shell. The list
// arrives with flag-bearing subcommands first, so the cap never drops one
// whose flags must be verified.
func helpProbe(step InstallStep) string {
	if step.Usage == nil {
		return ""
	}
	var subs []string
	for _, s := range step.Usage.Subs {
		if len(subs) >= 12 {
			break
		}
		safe := true
		for _, tok := range strings.Fields(s) {
			if !reSubName.MatchString(tok) {
				safe = false
				break
			}
		}
		if safe {
			subs = append(subs, s)
		}
	}
	var b strings.Builder
	b.WriteString("echo KIBBLE-HELP-START\n")
	b.WriteString(`timeout 15 "$GOBIN/$bin" --help 2>&1 | head -n 200` + "\n")
	for _, s := range subs {
		fmt.Fprintf(&b, `timeout 15 "$GOBIN/$bin" %s --help 2>&1 | head -n 200`+"\n", s)
	}
	b.WriteString("echo KIBBLE-HELP-END\n")
	return b.String()
}

// DockerAvailable reports an error when the docker CLI cannot reach a running
// daemon, so kibble can fail fast with a clear message instead of reporting
// every install as a container error.
func DockerAvailable(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("cannot reach docker daemon: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// installScript builds, then smoke-tests, one module inside the container. It
// prints BUILDCODE, SMOKECODE, and SMOKELINE markers for the parent to read. A
// build timeout surfaces as BUILDCODE=124 so it is not mistaken for a failure.
const installScript = `set -u
export GOBIN=/root/gobin
mkdir -p "$GOBIN"
out=$(timeout %d go install '%s' 2>&1); code=$?
if [ "$code" -ne 0 ]; then
  printf 'BUILDCODE=%%d\n' "$code"
  printf '%%s\n' "$out" | tail -n 3
  exit 0
fi
bin='%s'
sout=$(timeout 15 "$GOBIN/$bin" --version 2>&1); scode=$?
if [ "$scode" -ne 0 ]; then sout=$(timeout 15 "$GOBIN/$bin" --help 2>&1); scode=$?; fi
printf 'BUILDCODE=0\n'
printf 'SMOKECODE=%%d\n' "$scode"
printf 'SMOKELINE=%%s\n' "$(printf '%%s' "$sout" | head -n1 | cut -c1-70)"
`

// classify turns container output into a Result.
func classify(step InstallStep, out string, dur time.Duration) Result {
	res := Result{Step: step, Duration: dur}
	buildCode, smokeCode := -1, -1
	noBin := false
	inHelp := false
	var tail, help []string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "KIBBLE-HELP-START"):
			inHelp = true
		case strings.HasPrefix(line, "KIBBLE-HELP-END"):
			inHelp = false
		case inHelp:
			help = append(help, line)
		case strings.HasPrefix(line, "BUILDCODE="):
			buildCode, _ = strconv.Atoi(strings.TrimPrefix(line, "BUILDCODE="))
		case strings.HasPrefix(line, "SMOKECODE="):
			smokeCode, _ = strconv.Atoi(strings.TrimPrefix(line, "SMOKECODE="))
		case strings.HasPrefix(line, "SMOKELINE="):
			res.SmokeLine = strings.TrimPrefix(line, "SMOKELINE=")
		case strings.HasPrefix(line, "NOBIN="):
			noBin = true
		default:
			if strings.TrimSpace(line) != "" {
				tail = append(tail, line)
			}
		}
	}
	res.helpText = strings.Join(help, "\n")
	switch {
	case buildCode == -1:
		res.Status = StatusFail
		res.Detail = "no build marker (container error): " + lastLine(tail)
	case buildCode == 124:
		res.Status = StatusTimeout
		res.Detail = fmt.Sprintf("exceeded timeout after %s", dur.Round(time.Second))
	case buildCode != 0:
		res.Status = StatusFail
		res.Detail = lastLine(tail)
	case noBin:
		res.Status = StatusPass
		res.Detail = "recipe ran (no binary produced to smoke-test)"
	case smokeCode == 0:
		res.Status = StatusPass
	default:
		res.Status = StatusPassBuild
		res.Detail = fmt.Sprintf("binary built but smoke exit=%d", smokeCode)
	}
	return res
}

// lastLine returns the final non-empty line, for compact error detail.
func lastLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}
