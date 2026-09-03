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
	// StatusVerified means the tool built or installed and the binary
	// responded to a smoke test. It is the only status that claims the
	// documented step actually works.
	StatusVerified Status = "VERIFIED"
	// StatusBuilt means it built or installed but the smoke test exited
	// non-zero, so the artifact exists without proof it runs.
	StatusBuilt Status = "BUILT"
	// StatusRan means the recipe completed with a zero exit but produced no
	// binary to smoke-test, so nothing was verified. It covers both a recipe
	// that should have produced a tool and did not, and one that legitimately
	// installs no binary, since kibble cannot tell the two apart.
	StatusRan Status = "RAN"
	// StatusExists means a documented package or formula was confirmed present
	// in its index but never installed, so its existence is known and its
	// installation is not.
	StatusExists Status = "EXISTS"
	// StatusCrossArch means the tool installed but the binary targets another
	// architecture, so it could not be smoke-tested on this host.
	StatusCrossArch Status = "CROSS-ARCH"
	// StatusTimeout means the build exceeded the timeout, so the result is unknown.
	StatusTimeout Status = "TIMEOUT"
	// StatusFail means the documented install failed to build or named
	// something that does not exist.
	StatusFail Status = "FAIL"
	// StatusSkipped means kibble intentionally did not run this step. The
	// Reason field carries why in a machine-readable form.
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

// Bucket is a coarse grouping of statuses, so a consumer can condense the
// fine-grained verdicts into a handful of categories without hard-coding the
// full status set.
type Bucket string

const (
	// BucketWorks holds the statuses that prove the documented step works.
	BucketWorks Bucket = "works"
	// BucketUnverified holds the statuses where a step ran or a package
	// exists but nothing confirmed it works.
	BucketUnverified Bucket = "unverified"
	// BucketBroken holds the statuses where a documented step failed.
	BucketBroken Bucket = "broken"
	// BucketDocDrift holds the statuses where the documentation itself is
	// wrong even if commands ran.
	BucketDocDrift Bucket = "doc-drift"
	// BucketNotAttempted holds steps kibble chose not to run.
	BucketNotAttempted Bucket = "not-attempted"
	// BucketInconclusive holds steps whose outcome kibble could not determine.
	BucketInconclusive Bucket = "inconclusive"
)

// Bucket maps a status to its coarse category. It is the one place the
// rollup is defined, so the report, the JSON, and strict mode all agree.
func (s Status) Bucket() Bucket {
	switch s {
	case StatusVerified:
		return BucketWorks
	case StatusBuilt, StatusRan, StatusExists, StatusCrossArch:
		return BucketUnverified
	case StatusFail:
		return BucketBroken
	case StatusGap, StatusDrift:
		return BucketDocDrift
	case StatusSkipped:
		return BucketNotAttempted
	default:
		return BucketInconclusive
	}
}

// FailsUnderStrict reports whether strict mode treats this status as a
// failure. Strict promotes everything that is not a clean pass and not a
// deliberate skip: an unverified step, a drifted doc, or an inconclusive run
// all become failures when the caller demands proof.
func (s Status) FailsUnderStrict() bool {
	switch s.Bucket() {
	case BucketWorks, BucketNotAttempted:
		return false
	default:
		return true
	}
}

// Reason is a machine-readable code for why a step landed on its status,
// chiefly why a step was skipped, so a consumer can filter and audit the
// reasons rather than parsing the human-facing detail string.
type Reason string

const (
	// ReasonInteractive marks a command that waits for input.
	ReasonInteractive Reason = "interactive"
	// ReasonLongRunning marks a watcher, server, or daemon that does not return.
	ReasonLongRunning Reason = "long-running"
	// ReasonPlaceholder marks a command holding a template token to fill in.
	ReasonPlaceholder Reason = "placeholder"
	// ReasonMissingFixture marks a command needing a file the docs never create.
	ReasonMissingFixture Reason = "missing-fixture"
	// ReasonOtherPlatform marks a command scoped to another operating system.
	ReasonOtherPlatform Reason = "other-platform"
	// ReasonNeedsCredentials marks a command that needs authentication.
	ReasonNeedsCredentials Reason = "needs-credentials"
	// ReasonNoDataExpected marks a command whose empty result is normal.
	ReasonNoDataExpected Reason = "no-data-expected"
	// ReasonMissingDependency marks a command needing a tool the docs assume.
	ReasonMissingDependency Reason = "missing-dependency"
	// ReasonDependsOnSkipped marks a command that follows a skipped one.
	ReasonDependsOnSkipped Reason = "depends-on-skipped"
	// ReasonAlreadyProven marks a command the install smoke test already covered.
	ReasonAlreadyProven Reason = "already-proven"
	// ReasonNoOutputExit1 marks a quiet exit-1, as a search does on no match.
	ReasonNoOutputExit1 Reason = "no-output-exit1"
	// ReasonUnrecognizedTarget marks an install target kibble cannot parse.
	ReasonUnrecognizedTarget Reason = "unrecognized-target"
	// ReasonUnreachable marks a lookup kibble could not complete over the network.
	ReasonUnreachable Reason = "unreachable"
	// ReasonNotExecuted marks a step kibble has not run in this kind of pass.
	ReasonNotExecuted Reason = "not-executed"
	// ReasonUnsupportedMethod marks a documented install method kibble sees but
	// does not run, such as a piped shell installer or a system package.
	ReasonUnsupportedMethod Reason = "unsupported-method"
)

// Result is the outcome of attempting one install step.
type Result struct {
	// Step is the install step that was attempted.
	Step InstallStep
	// Status is the outcome classification.
	Status Status
	// Reason is the machine-readable why behind a status that needs one,
	// chiefly a skip. It is empty when the status speaks for itself.
	Reason Reason
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
	// helpRoot is the binary's own help screen, before any subcommand probe.
	// Doc coverage reads the public surface from here alone, since a
	// subcommand's screen lists that subcommand's children, not the root's.
	helpRoot string
	// helpBySub is each probed subcommand's own help screen. Keeping the
	// screens apart is what lets a check say whether a subcommand answered
	// at all, rather than searching one pile of text and guessing.
	helpBySub map[string]string
	// helpByFlag is what the binary said when each cited flag was probed
	// against it, keyed by the flag name without dashes. A help screen is
	// metadata; this is the binary's own answer.
	helpByFlag map[string]string
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
	// BrewInstall runs documented brew installs for real instead of checking
	// that the formula exists. Slower by minutes, and the only way a brew
	// step earns the right to fail a build.
	BrewInstall bool
}

// Run executes the step: go-install and git-clone run in a fresh container
// and smoke-test the result, brew is verified without installing, and
// example steps replay the README's example blocks in one session.
func (d *DockerRunner) Run(ctx context.Context, step InstallStep) Result {
	var script string
	switch step.Kind {
	case "example":
		res := d.runExample(ctx, step)
		// Several documents can be replayed for one repository, so a result
		// that is not about the README says which document it is about.
		if step.doc != "" && step.doc != step.readme {
			res.Detail = step.doc + ": " + res.Detail
		}
		return res
	case "brew":
		if d.BrewInstall {
			return d.runBrewInstall(ctx, step)
		}
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
	// The official Go images pin GOTOOLCHAIN=local, so a module asking for a
	// newer Go than the image ships fails to install. A reader running the
	// default fetches that toolchain, so the session matches them instead.
	args := append([]string{"run", "--rm", "--name", name},
		append(hardenedArgs(), "-e", "GOTOOLCHAIN=auto", image, "bash", "-c", script)...)
	cmd := exec.CommandContext(ctx, dockerBin(), args...)
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
			res.Reason = ReasonMissingDependency
			res.Detail = fmt.Sprintf("recipe needs %s, which %s does not provide", name, image)
		} else if m := reNewerToolchain.FindStringSubmatch(string(out)); m != nil {
			// The module asks for a newer toolchain than the image ships and
			// the session pins GOTOOLCHAIN, so the install a reader would get
			// was never attempted. That is kibble's gap, not the document's.
			res.Status = StatusSkipped
			res.Reason = ReasonMissingDependency
			res.Detail = fmt.Sprintf("needs Go %s, newer than %s provides", m[1], image)
		} else if reNetworkError.MatchString(string(out)) {
			res.Status = StatusError
			res.Detail = "network error during the step, result unknown: " + res.Detail
		} else if strings.HasPrefix(step.Module, "git://github.com") {
			// The clone ran and failed, so the verdict is earned. Git's own
			// message does not say why, and the reason is worth stating: the
			// unencrypted protocol has been off since 2022 and no reader can
			// get past it.
			res.Detail = "clone uses git://, which GitHub turned off in 2022, so this fails for every reader"
		}
	}
	return res
}

// reNewerToolchain matches a module requiring a newer Go than the image ships,
// such as "requires go >= 1.26.6 (running go 1.26.5)". A reader running the
// default GOTOOLCHAIN would fetch that toolchain, so the document is not wrong.
var reNewerToolchain = regexp.MustCompile(`requires go >= ([0-9][0-9.]*)`)

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
	// The package name came out of a document, so it is interpolated into the
	// owned-bins lookup only when it carries no shell syntax. Without the
	// lookup the script simply falls back to the bin diff.
	own := ""
	if pk.OwnedBins != "" && !strings.ContainsAny(step.Module, "$\x60;&|<>()'\" ") {
		own = fmt.Sprintf(pk.OwnedBins, step.Module)
	}
	return fmt.Sprintf(pkgScript, pk.BinDir, timeoutSecs, body, step.Binary, own)
}

// shellCommand returns a documented line in the form a shell can run: the
// prompt marker many READMEs prefix and any trailing comment are removed, so
// `$ cargo install ripgrep   # from crates.io` runs as written without them.
func shellCommand(raw string) string {
	return strings.TrimSpace(stripComment(strings.TrimPrefix(strings.TrimSpace(raw), "$ ")))
}

// pkgScript installs a documented package and smoke-tests the binary it
// added. The package's own name is preferred among the new files, because a
// pip install drops its dependencies' entry points into the same directory
// and the alphabetically first of those is what got smoke-tested in the
// package's name. The diff fallback still covers a package whose binary
// carries a different name, as ripgrep provides rg.
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
newbins=$(comm -13 <(printf '%%s\n' "$before") <(printf '%%s\n' "$after"))
own=''
%[5]s
bin=$(printf '%%s\n' "$newbins" | grep -Fx '%[4]s' | head -n1)
if [ -z "$bin" ] && [ -n "$own" ]; then
  bin=$(printf '%%s\n' "$newbins" | grep -Fxf <(printf '%%s\n' "$own") | head -n1)
fi
[ -z "$bin" ] && bin=$(printf '%%s\n' "$newbins" | grep -v '^$' | head -n1)
if [ -n "$bin" ]; then bin="$BINDIR/$bin"; fi
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
	// The root screen ends here, said out loud. Without this boundary the
	// first probe's output shares a segment with the root screen, and a tool
	// with nothing to probe before its flags once had its whole help corpus
	// mistaken for probe output and trimmed away.
	b.WriteString("printf '" + markerLead + "KIBBLE-ROOT-END\\n'\n")
	for _, s := range subs {
		fmt.Fprintf(&b, `kh=$(timeout 15 "$bin" %s --help 2>&1); kc=$?`+"\n", s)
		b.WriteString(`printf '%s\n' "$kh" | head -n 200` + "\n")
		fmt.Fprintf(&b, "printf '"+markerLead+"KIBBLE-SUB %s CODE=%%d\\n' \"$kc\"\n", s)
	}
	// Every cited flag is probed against the binary itself, because a help
	// screen is metadata and a binary can accept flags its help hides. The
	// probe runs `bin <sub> <flag> --help`: an unknown flag makes the parser
	// reject it by name before help can fire, and a hidden but valid one
	// parses cleanly. Only that named rejection convicts, so a screen that
	// omits a working flag can no longer turn into an accusation.
	for _, f := range probeFlags(step.Usage) {
		flag := "--" + f.name
		args := flag
		if f.owner != "" {
			args = f.owner + " " + flag
		}
		fmt.Fprintf(&b, `kh=$(timeout 10 "$bin" %s --help 2>&1); kc=$?`+"\n", args)
		b.WriteString(`printf '%s\n' "$kh" | head -n 40` + "\n")
		fmt.Fprintf(&b, "printf '"+markerLead+"KIBBLE-FLAG %s CODE=%%d\\n' \"$kc\"\n", flag)
	}
	b.WriteString("printf '" + markerLead + "KIBBLE-HELP-END\\n'\n")
	return b.String()
}

// probedFlag names one cited flag and the subcommand it was cited on.
type probedFlag struct {
	// name is the flag without dashes.
	name string
	// owner is the citing subcommand, empty for the bare binary.
	owner string
}

// probeFlags returns the cited flags safe to hand to the shell, capped so a
// flag-heavy document cannot stretch the probe unboundedly.
func probeFlags(u *Usage) []probedFlag {
	var out []probedFlag
	for _, f := range u.Flags {
		if len(out) >= 16 {
			break
		}
		if !reSubName.MatchString(strings.ToLower(f)) {
			continue
		}
		owner := u.FlagSub[f]
		safe := owner == ""
		if owner != "" {
			safe = true
			for _, tok := range strings.Fields(owner) {
				if !reSubName.MatchString(tok) {
					safe = false
					break
				}
			}
		}
		if safe {
			out = append(out, probedFlag{name: f, owner: owner})
		}
	}
	return out
}

// hardenedArgs are the docker flags every kibble container runs under. A
// documented command is text from a repository, which makes it untrusted
// input, so the container gets no capability the session does not need and
// bounded memory and process counts. The capabilities kept are the minimal
// set apt needs, since sessions install Debian packages the docs depend on.
// Network stays on because verifying an install is fetching it; that is a
// conscious tradeoff the README's security section owns.
func hardenedArgs() []string {
	return append([]string{
		"--memory=4g",
		"--pids-limit=1024",
		"--security-opt", "no-new-privileges",
		"--cap-drop", "ALL",
		"--cap-add", "CHOWN",
		"--cap-add", "DAC_OVERRIDE",
		"--cap-add", "FOWNER",
		"--cap-add", "SETGID",
		"--cap-add", "SETUID",
	}, metadataBlackhole()...)
}

// cloudMetadataHosts are the names a cloud instance answers on to hand out
// credentials. A documented line kibble runs is untrusted, and kibble's own
// home is a CI runner inside exactly this kind of instance, so the convenient
// path to them is closed.
var cloudMetadataHosts = []string{
	"metadata.google.internal",
	"metadata.goog",
	"instance-data",
	"instance-data.ec2.internal",
	"metadata.packet.net",
}

// metadataBlackhole points the well-known metadata hostnames at an address
// that answers nothing.
//
// This closes the named path and not the numbered one. The addresses these
// names resolve to, 169.254.169.254 above all, stay reachable, because Docker
// has no portable flag that drops a route to a single address and the two
// alternatives are worse: an internal network breaks every install kibble
// exists to run, and host firewall rules are not kibble's to install. The
// honest description of this is a speed bump, and the security document says
// so rather than implying a wall.
func metadataBlackhole() []string {
	args := make([]string, 0, len(cloudMetadataHosts)*2)
	for _, host := range cloudMetadataHosts {
		args = append(args, "--add-host", host+":0.0.0.0")
	}
	return args
}

// containerSeq numbers containers within one run so every step gets a name
// kibble can tear down on its own.
var containerSeq atomic.Uint64

// brewImage is the Homebrew project's own Linux image, so a documented brew
// install runs against the real package manager rather than a guess about it.
const brewImage = "homebrew/brew"

// brewInstallScript runs the documented formula install and smoke-tests the
// binary the formula itself installed. Brew is asked which file that is, since
// a formula pulls in dependencies that install binaries of their own: taking
// whatever appeared in the bin directory smoke-tested a compiler shipped with
// rich's dependencies instead of rich. The bin diff remains as the fallback for
// a formula brew will not list.
const brewInstallScript = `set -u
before=$(ls "$(brew --prefix)/bin" 2>/dev/null | sort)
out=$(timeout %d brew install %s 2>&1); code=$?
if [ "$code" -ne 0 ]; then
  printf 'BUILDCODE=%%d\n' "$code"
  printf '%%s\n' "$out" | tail -n 3
  exit 0
fi
printf 'BUILDCODE=0\n'
bin=$(brew list --verbose %s 2>/dev/null | grep -E '/bin/[^/]+$' | head -n1)
if [ -z "$bin" ]; then
  after=$(ls "$(brew --prefix)/bin" 2>/dev/null | sort)
  b=$(comm -13 <(printf '%%s\n' "$before") <(printf '%%s\n' "$after") | head -n1)
  [ -n "$b" ] && bin="$(brew --prefix)/bin/$b"
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

// runBrewInstall installs a documented formula for real. Only an install that
// actually ran can call a documented brew line broken: the formula namespace
// has aliases, casks, and taps, and asking an index about a name instead of
// installing it is how a working line gets accused.
func (d *DockerRunner) runBrewInstall(ctx context.Context, step InstallStep) Result {
	start := time.Now()
	// A cask installs applications on macOS. A Linux container cannot judge
	// one, and saying so is the honest answer.
	if name, ok := strings.CutPrefix(step.Module, "cask:"); ok {
		return Result{
			Step: step, Status: StatusSkipped, Reason: ReasonOtherPlatform, Duration: time.Since(start),
			Detail: fmt.Sprintf("%s is a cask, which installs on macOS rather than in this container", name),
		}
	}
	if strings.ContainsAny(step.Module, "$`;&|<>()") {
		return Result{
			Step: step, Status: StatusSkipped, Reason: ReasonUnrecognizedTarget, Duration: time.Since(start),
			Detail: "formula name has shell characters, so it is not run",
		}
	}
	ctx, cancel := context.WithTimeout(ctx, d.Timeout+2*time.Minute)
	defer cancel()
	script := fmt.Sprintf(brewInstallScript, int(d.Timeout.Seconds()), step.Module, step.Module)
	name := containerName()
	args := append([]string{"run", "--rm", "--name", name},
		append(hardenedArgs(), brewImage, "bash", "-c", script)...)
	cmd := exec.CommandContext(ctx, dockerBin(), args...)
	cmd.Cancel = removeContainerFunc(cmd, name)
	out, _ := cmd.CombinedOutput()
	if ctx.Err() != nil && errors.Is(context.Cause(ctx), context.Canceled) {
		return Result{
			Step: step, Status: StatusError, Duration: time.Since(start),
			Detail: "run was interrupted, so this step has no verdict",
		}
	}
	res := classify(step, string(out), time.Since(start))
	res.Image = brewImage
	return res
}

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
		_ = exec.CommandContext(ctx, dockerBin(), "rm", "-f", name).Run()
		return cmd.Process.Kill()
	}
}

// dockerBin returns the container client binary to invoke. Podman and other
// drop-in replacements speak docker's command line, so KIBBLE_DOCKER names
// the binary and docker stays the default.
func dockerBin() string {
	if bin := os.Getenv("KIBBLE_DOCKER"); bin != "" {
		return bin
	}
	return "docker"
}

// DockerAvailable reports an error when the docker CLI cannot reach a running
// daemon, so kibble can fail fast with a clear message instead of reporting
// every install as a container error.
func DockerAvailable(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, dockerBin(), "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("cannot reach the %s daemon: %s", dockerBin(), strings.TrimSpace(string(out)))
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
	// Tools colorize their own output, and those escapes are noise everywhere
	// they land: they corrupt a JSON report a caller parses, break column
	// alignment in the table, and turn a CI annotation into gibberish. Kibble
	// adds its own color at render time, so the captured text is stripped once
	// here and every consumer downstream gets clean strings.
	out = stripANSI(out)
	res := Result{Step: step, Duration: dur}
	buildCode, smokeCode := -1, -1
	noBin := false
	inHelp := false
	subCodes := map[string]int{}
	helpBySub := map[string]string{}
	helpByFlag := map[string]string{}
	var tail, help, cur []string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "KIBBLE-HELP-START"):
			inHelp = true
		case strings.HasPrefix(line, "KIBBLE-HELP-END"):
			inHelp = false
		case strings.HasPrefix(line, "KIBBLE-ROOT-END"):
			res.helpRoot = strings.Join(help, "\n")
			cur = nil
		case reSubMarker.MatchString(line):
			m := reSubMarker.FindStringSubmatch(line)
			subCodes[m[1]], _ = strconv.Atoi(m[2])
			helpBySub[m[1]] = strings.Join(cur, "\n")
			cur = nil
		case reFlagMarker.MatchString(line):
			m := reFlagMarker.FindStringSubmatch(line)
			name := strings.TrimLeft(m[1], "-")
			// A probe screen never joins the help corpus: an unknown-flag
			// error quotes the flag, and a corpus holding that quote would
			// count the missing flag as known and blind the check.
			help = help[:len(help)-len(cur)]
			helpByFlag[name] = strings.Join(cur, "\n")
			cur = nil
		case inHelp:
			help = append(help, line)
			cur = append(cur, line)
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
	if res.helpRoot == "" {
		res.helpRoot = res.helpText
	}
	if len(helpBySub) > 0 {
		res.helpBySub = helpBySub
	}
	if len(helpByFlag) > 0 {
		res.helpByFlag = helpByFlag
	}
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
		res.Status = StatusRan
		res.Detail = "recipe exited 0 but produced no binary to smoke-test"
	case smokeCode == 0:
		res.Status = StatusVerified
	case reArchMismatch.MatchString(res.SmokeLine):
		// Only the smoke line convicts. Scanning the whole output once let an
		// arch string anywhere in a build log or help screen relabel a real
		// smoke-test crash as a harmless cross-architecture skip.
		res.Status = StatusCrossArch
		res.SmokeLine = ""
		res.Detail = "installed, but the binary targets another architecture, smoke test not possible here"
	default:
		res.Status = StatusBuilt
		res.Detail = fmt.Sprintf("binary built but smoke exit=%d", smokeCode)
	}
	return res
}

// reSubMarker parses a KIBBLE-SUB marker into the cited subcommand and the
// exit code its help probe returned.
var reSubMarker = regexp.MustCompile(`^KIBBLE-SUB (.+) CODE=(-?\d+)$`)

// reFlagMarker parses a KIBBLE-FLAG marker into the probed flag and the exit
// code the binary answered with.
var reFlagMarker = regexp.MustCompile(`^KIBBLE-FLAG (--?\S+) CODE=(-?\d+)$`)

// reArchMismatch matches the errors a binary built for another architecture
// produces, so an emulation artifact of the host is not reported as the tool
// failing its smoke test.
var reArchMismatch = regexp.MustCompile(`qemu-\w+: |Exec format error|cannot execute binary file`)

// reANSI matches the escape sequences a program writes to color or reposition
// its own output. Both the color form and the wider set of control sequences
// are covered, since a progress bar redrawing itself is as unwelcome in a
// report as a color code.
var reANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\r`)

// stripANSI removes terminal control sequences from captured output.
func stripANSI(s string) string {
	if !strings.ContainsAny(s, "\x1b\r") {
		return s
	}
	return reANSI.ReplaceAllString(s, "")
}

// lastLine returns the final non-empty line, for compact error detail.
func lastLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

// reWrapperSummary matches the closing line a build tool prints after the tool
// it invoked has already reported the real error. It names the target that
// failed and nothing about why.
var reWrapperSummary = regexp.MustCompile(
	`^(make(\[\d+\])?: (\*\*\*|Entering|Leaving)|npm ERR!|yarn ERR|` +
		`error: could not compile|error: build failed|FAILED:|ninja: build stopped)`)

// reUsageHeading matches the banner a tool prints above its own usage screen
// when it rejects an argument.
var reUsageHeading = regexp.MustCompile(`(?i)^(usage|options|flags|commands)\b|^usage:`)

// reErrorDeclaration matches the line where a parser says what it refused.
// It is the sentence a reader needs, and it sits at the top of the output,
// above the usage screen a tool prints after it.
var reErrorDeclaration = regexp.MustCompile(
	`(?i)^(error|fatal|panic)\b|flag provided but not defined|` +
		`\b(unknown|unrecognized|invalid|unexpected) (flag|option|command|subcommand|argument)\b|` +
		`\bno such (flag|option|command|subcommand)\b`)

// failureLine returns the most informative line of a failure. Reporting a
// wrapper's summary hides the error a reader needs, so the summary is skipped
// in favor of the last line that says what actually broke. A tool that answers
// a bad argument by printing its whole usage screen is the other direction of
// the same mistake: the last line there is the final flag's description, which
// tells a reader nothing, so an error the parser declared above the screen
// wins over anything below it.
func failureLine(lines []string) string {
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if !reErrorDeclaration.MatchString(line) {
			continue
		}
		// The declaration only outranks the tail when a usage screen follows
		// it, since that is the case where the tail is boilerplate.
		for _, rest := range lines[i+1:] {
			if reUsageHeading.MatchString(strings.TrimSpace(rest)) {
				return line
			}
		}
		break
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || reWrapperSummary.MatchString(line) {
			continue
		}
		return line
	}
	return lastLine(lines)
}
