package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/dcadolph/kibble/internal/sandbox"
)

// Replaying a document's examples in one container session: the per-line
// outcome types, the run itself, and the image and budget it gets.

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
	name := sandbox.Name()
	args := append([]string{"run", "--rm", "-i", "--name", name},
		append(sandbox.Args(), "-e", "GOTOOLCHAIN=auto", image, "bash", "-c", script)...)
	cmd := exec.CommandContext(ctx, sandbox.Bin(), args...)
	cmd.Cancel = sandbox.RemoveFunc(cmd, name)
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

// tarSkipDirs are directories a build produces or a package manager fills.
// Streaming them wastes the budget on output nobody documents, and on a big
// repository it pushes the source a document needs past the cap.
var tarSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, "out": true, ".gradle": true, ".idea": true,
	".next": true, ".venv": true, "__pycache__": true, "coverage": true,
	".terraform": true, ".tox": true, ".pytest_cache": true, ".mypy_cache": true,
}
