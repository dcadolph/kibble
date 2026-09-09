package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/dcadolph/kibble/internal/sandbox"
)

// Running one documented install step in a clean container and deciding what
// its outcome was. The Result this produces is the unit everything downstream
// reports on.

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
	// LineTimeout bounds one documented example line. Zero means the default,
	// which is what every caller but a test wants.
	LineTimeout time.Duration
}

// lineBudget returns the per-line timeout this runner applies.
func (d *DockerRunner) lineBudget() time.Duration {
	if d.LineTimeout > 0 {
		return d.LineTimeout
	}
	return lineTimeout
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
	name := sandbox.Name()
	// The official Go images pin GOTOOLCHAIN=local, so a module asking for a
	// newer Go than the image ships fails to install. A reader running the
	// default fetches that toolchain, so the session matches them instead.
	args := append([]string{"run", "--rm", "--name", name},
		append(sandbox.Args(), "-e", "GOTOOLCHAIN=auto", image, "bash", "-c", script)...)
	cmd := exec.CommandContext(ctx, sandbox.Bin(), args...)
	cmd.Cancel = sandbox.RemoveFunc(cmd, name)
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
		} else if m := reNewerRust.FindStringSubmatch(string(out)); m != nil {
			// The crate needs a newer rustc than the image ships. A reader with
			// an up-to-date toolchain would build it, so the image's older rustc
			// is kibble's limit, not the document's error.
			res.Status = StatusSkipped
			res.Reason = ReasonMissingDependency
			res.Detail = fmt.Sprintf("needs rustc %s, newer than %s provides", m[1], image)
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

// reNewerRust matches a crate that needs a newer rustc than the image ships,
// such as "requires rustc 1.80 or newer". A reader with a current toolchain
// would build it, so the older image is kibble's limit, not the document's.
var reNewerRust = regexp.MustCompile(`(?i)requires rustc ([0-9][0-9.]*)`)

// reNetworkError matches container output that names a network failure, so a
// flaky connection is reported as kibble's error rather than broken docs.
var reNetworkError = regexp.MustCompile(
	`Connection refused|Could not resolve host|Temporary failure in name resolution|Network is unreachable|TLS handshake timeout|connection reset by peer|Connection timed out|` +
		// Node and npm report a failed fetch as a libuv errno rather than in
		// any of the wordings above, so a registry that blinked was read as the
		// document being wrong. That is how a corpus repository failed one run
		// and passed the next two with nothing changed between them.
		`\b(EAI_AGAIN|ENOTFOUND|ECONNRESET|ETIMEDOUT|ECONNREFUSED|ENETUNREACH|EHOSTUNREACH)\b|` +
		`getaddrinfo|socket hang up|request to \S+ failed, reason`)

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
