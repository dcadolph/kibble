package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
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
	// StatusGap means the documentation is incomplete: a documented line
	// names a file, directory, or variable that no documented step creates,
	// so a reader following the document cannot run it. Unlike a skip, the
	// gap is the document's, not the container's.
	StatusGap Status = "GAP"
	// StatusDrift means the docs cite a flag or subcommand the binary lacks.
	StatusDrift Status = "DRIFT"
	// StatusError means kibble itself could not run the step, so the result
	// says nothing about the document.
	StatusError Status = "ERROR"
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
	// Image is the container image kibble selected for the step's toolchain,
	// empty when the step ran in the configured default.
	Image string
	// helpText is the help output collected for flag checking.
	helpText string
	// subCodes maps each cited subcommand to the exit code its help probe
	// returned, so a subcommand the binary rejects is caught by its exit
	// rather than by one framework's wording for the error.
	subCodes map[string]int
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
		if strings.HasPrefix(step.Module, "git://github.com") {
			return Result{
				Step: step, Status: StatusFail,
				Detail: "clone uses git://, which GitHub turned off in 2022, so this fails for every reader",
			}
		}
		script = cloneScriptFor(step, int(d.Timeout.Seconds()))
	default:
		if _, isPkg := pkgKinds[step.Kind]; isPkg {
			script = pkgScriptFor(step, int(d.Timeout.Seconds())) + helpProbe(step)
			break
		}
		script = fmt.Sprintf(installScript, int(d.Timeout.Seconds()), step.Module, step.Binary)
		script += helpProbe(step)
	}

	ctx, cancel := context.WithTimeout(ctx, d.Timeout+60*time.Second)
	defer cancel()

	image := d.imageFor(step)
	start := time.Now()
	name := containerName()
	cmd := exec.CommandContext(ctx,
		"docker", "run", "--rm", "--name", name, image, "bash", "-c", script)
	cmd.Cancel = removeContainerFunc(cmd, name)
	out, _ := cmd.CombinedOutput()
	if ctx.Err() != nil && errors.Is(context.Cause(ctx), context.Canceled) {
		return Result{
			Step: step, Status: StatusError, Duration: time.Since(start),
			Detail: "run was interrupted, so this step has no verdict",
		}
	}
	res := classify(step, string(out), time.Since(start))
	if image != d.Image {
		res.Image = image
	}
	if res.Status == StatusFail {
		if name, missing := missingCommand(string(out)); missing {
			res.Status = StatusSkipped
			res.Detail = fmt.Sprintf("recipe needs %s, which %s does not provide", name, image)
		} else if reNetworkError.MatchString(string(out)) {
			res.Status = StatusError
			res.Detail = "network error during the step, result unknown: " + res.Detail
		}
	}
	return res
}

// reNetworkError matches container output that names a network failure, so a
// flaky connection is reported as kibble's error rather than broken docs.
var reNetworkError = regexp.MustCompile(
	`Connection refused|Could not resolve host|Temporary failure in name resolution|Network is unreachable|TLS handshake timeout|connection reset by peer|Connection timed out`)

// imageFor returns the container image a step runs in. A clone recipe runs in
// the image that provides the toolchain its commands assume, so a Rust or Node
// project builds with the tools its docs were written for. Everything else runs
// in the configured default.
func (d *DockerRunner) imageFor(step InstallStep) string {
	if pk, ok := pkgKinds[step.Kind]; ok {
		if tc, known := toolchains[pk.Ecosystem]; known && tc.Image != "" {
			return tc.Image
		}
		return d.Image
	}
	if step.Kind != "git-clone" {
		return d.Image
	}
	tc, ok := detectToolchain(step.Block, step.dir)
	if !ok || tc.Image == "" {
		return d.Image
	}
	return tc.Image
}

// pkgScriptFor builds the container script for a documented package install.
// The documented line runs verbatim, so what kibble verifies is the command a
// reader would actually type.
func pkgScriptFor(step InstallStep, timeoutSecs int) string {
	pk := pkgKinds[step.Kind]
	body := shellCommand(step.Raw)
	if pk.Bootstrap != "" {
		body = pk.Bootstrap + "\n" + body
	}
	body = strings.ReplaceAll(body, "'", `'\''`)
	return fmt.Sprintf(pkgScript, pk.BinDir, timeoutSecs, body, step.Binary)
}

// shellCommand returns a documented line in the form a shell can run: the
// prompt marker many READMEs prefix and any trailing comment are removed, so
// `$ cargo install ripgrep   # from crates.io` runs as written without them.
func shellCommand(raw string) string {
	return strings.TrimSpace(stripComment(strings.TrimPrefix(strings.TrimSpace(raw), "$ ")))
}

// pkgScript installs a documented package and smoke-tests whatever binary the
// install added. The bin directory is compared before and after, so a package
// whose binary carries a different name, as ripgrep provides rg, is still
// found and tested rather than reported as missing.
const pkgScript = `set -u
BINDIR=%[1]s
mkdir -p "$BINDIR" 2>/dev/null || true
before=$(ls "$BINDIR" 2>/dev/null | sort)
out=$(timeout %[2]d bash -ec '%[3]s' 2>&1); code=$?
if [ "$code" -ne 0 ]; then
  printf 'BUILDCODE=%%d\n' "$code"
  printf '%%s\n' "$out" | tail -n 3
  exit 0
fi
printf 'BUILDCODE=0\n'
after=$(ls "$BINDIR" 2>/dev/null | sort)
bin=$(comm -13 <(printf '%%s\n' "$before") <(printf '%%s\n' "$after") | head -n1)
if [ -n "$bin" ]; then bin="$BINDIR/$bin"; fi
if [ -z "$bin" ] && command -v "%[4]s" >/dev/null 2>&1; then bin=$(command -v "%[4]s"); fi
if [ -z "$bin" ]; then
  printf 'NOBIN=1\n'
  exit 0
fi
sout=$(timeout 15 "$bin" --version 2>&1); scode=$?
if [ "$scode" -ne 0 ]; then sout=$(timeout 15 "$bin" --help 2>&1); scode=$?; fi
printf 'SMOKECODE=%%d\n' "$scode"
printf 'SMOKELINE=%%s\n' "$(printf '%%s' "$sout" | head -n1 | cut -c1-70)"
`

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
if [ -x "$GOBIN/%[3]s" ]; then
  bin="$GOBIN/%[3]s"
elif command -v "%[3]s" >/dev/null 2>&1; then
  bin=$(command -v "%[3]s")
else
  bin=$(find /work -maxdepth 5 -type f -perm -u+x -name "%[3]s" 2>/dev/null | head -n1)
  if [ -z "$bin" ]; then b=$(ls "$GOBIN" 2>/dev/null | head -n1); [ -n "$b" ] && bin="$GOBIN/$b"; fi
fi
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
// screens for flag checking. Every install script leaves the installed binary
// in $bin, so the same probe serves a Go, package, or clone install. Cited
// subcommands are probed too, capped and restricted to safe names so nothing
// unexpected reaches the shell. The list arrives with flag-bearing
// subcommands first, so the cap never drops one whose flags must be verified.
// Each subcommand probe reports its own exit code, which is how a binary that
// rejects a cited subcommand is caught without matching one framework's
// wording for the error.
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
	b.WriteString("printf '" + markerLead + "KIBBLE-HELP-START\\n'\n")
	b.WriteString(`kh=$(timeout 15 "$bin" --help 2>&1)` + "\n")
	b.WriteString(`printf '%s\n' "$kh" | head -n 200` + "\n")
	for _, s := range subs {
		fmt.Fprintf(&b, `kh=$(timeout 15 "$bin" %s --help 2>&1); kc=$?`+"\n", s)
		b.WriteString(`printf '%s\n' "$kh" | head -n 200` + "\n")
		fmt.Fprintf(&b, "printf '"+markerLead+"KIBBLE-SUB %s CODE=%%d\\n' \"$kc\"\n", s)
	}
	b.WriteString("printf '" + markerLead + "KIBBLE-HELP-END\\n'\n")
	return b.String()
}

// containerSeq numbers containers within one run so every step gets a name
// kibble can tear down on its own.
var containerSeq atomic.Uint64

// containerName returns a container name unique to this process and step.
func containerName() string {
	return fmt.Sprintf("kibble-%d-%d", os.Getpid(), containerSeq.Add(1))
}

// removeContainerFunc returns the cancel hook for a docker run. Killing the
// docker client leaves the container running, since the daemon owns it, so
// the hook removes the container by name before killing the client. Without
// it an interrupted run leaves containers building in the background.
func removeContainerFunc(cmd *exec.Cmd, name string) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
		return cmd.Process.Kill()
	}
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
// When the module builds but kibble's guess at the binary name finds nothing,
// whatever landed in GOBIN is smoke-tested instead, and an empty GOBIN prints
// NOBIN, so a wrong guess never reads as the documented install failing.
const installScript = `set -u
export GOBIN=/root/gobin
mkdir -p "$GOBIN"
out=$(timeout %d go install '%s' 2>&1); code=$?
if [ "$code" -ne 0 ]; then
  printf 'BUILDCODE=%%d\n' "$code"
  printf '%%s\n' "$out" | tail -n 3
  exit 0
fi
printf 'BUILDCODE=0\n'
bin="$GOBIN/%s"
if [ ! -x "$bin" ]; then
  b=$(ls "$GOBIN" 2>/dev/null | head -n1)
  if [ -n "$b" ]; then bin="$GOBIN/$b"; fi
fi
if [ ! -x "$bin" ]; then
  printf 'NOBIN=1\n'
  exit 0
fi
sout=$(timeout 15 "$bin" --version 2>&1); scode=$?
if [ "$scode" -ne 0 ]; then sout=$(timeout 15 "$bin" --help 2>&1); scode=$?; fi
printf 'SMOKECODE=%%d\n' "$scode"
printf 'SMOKELINE=%%s\n' "$(printf '%%s' "$sout" | head -n1 | cut -c1-70)"
`

// classify turns container output into a Result.
func classify(step InstallStep, out string, dur time.Duration) Result {
	res := Result{Step: step, Duration: dur}
	buildCode, smokeCode := -1, -1
	noBin := false
	inHelp := false
	subCodes := map[string]int{}
	var tail, help []string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "KIBBLE-HELP-START"):
			inHelp = true
		case strings.HasPrefix(line, "KIBBLE-HELP-END"):
			inHelp = false
		case reSubMarker.MatchString(line):
			m := reSubMarker.FindStringSubmatch(line)
			subCodes[m[1]], _ = strconv.Atoi(m[2])
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
	if len(subCodes) > 0 {
		res.subCodes = subCodes
	}
	switch {
	case buildCode == -1:
		res.Status = StatusError
		res.Detail = "kibble could not run the step (container error): " + lastLine(tail)
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
	case reArchMismatch.MatchString(res.SmokeLine) || reArchMismatch.MatchString(out):
		res.Status = StatusPass
		res.SmokeLine = ""
		res.Detail = "installed, but the binary targets another architecture, smoke test not possible here"
	default:
		res.Status = StatusPassBuild
		res.Detail = fmt.Sprintf("binary built but smoke exit=%d", smokeCode)
	}
	return res
}

// reSubMarker parses a KIBBLE-SUB marker into the cited subcommand and the
// exit code its help probe returned.
var reSubMarker = regexp.MustCompile(`^KIBBLE-SUB (.+) CODE=(-?\d+)$`)

// reArchMismatch matches the errors a binary built for another architecture
// produces, so an emulation artifact of the host is not reported as the tool
// failing its smoke test.
var reArchMismatch = regexp.MustCompile(`qemu-\w+: |Exec format error|cannot execute binary file`)

// lastLine returns the final non-empty line, for compact error detail.
func lastLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}
