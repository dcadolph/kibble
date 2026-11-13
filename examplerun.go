package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	kplan "github.com/dcadolph/kibble/internal/plan"
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
	// Doc is the document the block came from, carried so a result can say
	// where to look. One session covers a repository's whole documentation
	// tree, so a block identifier alone no longer locates anything.
	Doc string
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
	// cwd is the directory the line ran in, empty when the session did not say.
	cwd string
}

// markerLead is printed before every session marker. A documented command
// whose output ends without a trailing newline would otherwise leave the
// marker appended to that output, where the parser cannot see it.
const markerLead = `\n`

// lineTimeout bounds one wrapped example line, so a command that waits for
// input or serves forever cannot eat the whole session budget.
const lineTimeout = 90 * time.Second

// exampleSkip returns why an example plan is skipped before any container
// starts, or "" when it runs.
func exampleSkip(plan *kplan.Plan) string {
	switch {
	case plan == nil || len(plan.Steps) == 0:
		return "no example blocks found"
	case len(plan.Installs) == 0:
		return "no documented install puts a binary on PATH; examples not run"
	}
	return ""
}

// runExample replays a repo's example plan in one clean container: the
// documented binaries are installed, the repo tree is copied in, and every
// plan step runs in documented order in a single shell, so files and
// environment persist between blocks the way they do in a real terminal.
func (d *DockerRunner) runExample(ctx context.Context, step InstallStep) Result {
	plan := step.plan
	res := Result{Step: step}
	if detail := exampleSkip(plan); detail != "" {
		res.Status = StatusSkipped
		res.Detail = detail
		return res
	}
	image, ok, blocked := d.exampleImage(plan, step.dir)
	if !ok {
		res.Status = StatusSkipped
		res.Detail = "documented installs need more than one toolchain; no single image serves them"
		return res
	}
	if blocked != "" {
		res.Status = StatusBlocked
		res.Reason = ReasonMissingDependency
		res.Detail = blocked
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
	// Decided before the container starts, by a stat-only walk, so a repository
	// that cannot be streamed whole never gets a partial one.
	if !repoFits(step.dir) {
		return Result{
			Step: step, Status: StatusError, Duration: time.Since(start),
			Detail: fmt.Sprintf("working tree exceeds the %d MB kibble will stream, so the examples have no verdict",
				repoStreamCap>>20),
		}
	}
	// Streamed rather than buffered. Holding the whole archive in memory is what
	// kept the cap small enough to exclude ordinary projects.
	pr, pw := io.Pipe()
	go func() {
		_, err := repoTarTo(pw, step.dir)
		_ = pw.CloseWithError(err)
	}()
	defer func() { _ = pr.Close() }()
	cmd.Stdin = pr
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil && errors.Is(context.Cause(ctx), context.Canceled) {
		return Result{
			Step: step, Status: StatusError, Duration: time.Since(start),
			Detail: "run was interrupted, so the examples have no verdict",
		}
	}
	end := sessionEnd{expired: errors.Is(ctx.Err(), context.DeadlineExceeded), err: err}
	res = classifyExample(step, plan, string(out), wrapped, time.Since(start), d.lineBudget(), end)
	if image != d.Image {
		res.Image = image
	}
	return res
}

// exampleImage returns the image an example session runs in. Every documented
// install must be servable by one image, since the session is one shell: a
// project installed with both cargo and npm names two toolchains and is
// reported as a skip rather than run in an image missing one of them. A
// Python session runs on the newest release the checkout at dir states it
// supports, and blocked is set when that range allows no image.
func (d *DockerRunner) exampleImage(plan *kplan.Plan, dir string) (image string, ok bool, blocked string) {
	eco := ""
	for _, in := range plan.Installs {
		if in.Ecosystem == "" {
			continue
		}
		if eco != "" && eco != in.Ecosystem {
			return "", false, ""
		}
		eco = in.Ecosystem
	}
	if tc, known := toolchains[eco]; known && tc.Image != "" {
		image, blocked = toolchainImage(tc, dir)
		return image, true, blocked
	}
	return d.Image, true, ""
}

// sessionBudget bounds the whole example session: each module build gets the
// install timeout, each runnable line gets a share, and setup gets a grace
// period, capped so one repo cannot stall the run.
func sessionBudget(plan *kplan.Plan, install time.Duration) time.Duration {
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

// repoStreamCap bounds what kibble will send into the session. The old cap was
// 20 MB and the whole archive was built in memory first, so the ceiling was
// really the memory three concurrent workers could afford rather than anything
// about documentation. Measured against real projects it was far too low: mise
// carries a 45 MB working tree, hugo 33 MB and eslint 29 MB, all with .git and
// generated directories already excluded, and every one of them came back with
// no verdict at all. Streaming instead of buffering removes the memory reason
// for a small cap, and what is left is a runaway guard.
const repoStreamCap = 512 << 20

// treeSpec says which part of a checkout an archive carries.
type treeSpec struct {
	// sub is the directory under the checkout that is walked, empty for all of
	// it. Archive names stay relative to the checkout either way.
	sub string
	// skip holds the directory names left out.
	skip map[string]bool
	// maxFile is the size above which a file is left out, 0 for no limit.
	maxFile int64
	// dirs reports whether directories get entries of their own, so that an
	// empty one, such as a git ref directory, exists after extraction.
	dirs bool
	// only holds the names directly under the walked directory that are
	// carried, nil for all of them.
	only map[string]bool
}

// leftOut reports whether the entry at path, under the walked directory root,
// is outside what the spec carries.
func (s treeSpec) leftOut(root, path string, d fs.DirEntry) bool {
	if d.IsDir() && s.skip[d.Name()] {
		return true
	}
	if s.only == nil || path == root {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}
	top, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	return !s.only[top]
}

// sessionTree is what an example session receives: the working tree without
// generated directories and without files over 2 MB, so documented example
// files exist in the container.
var sessionTree = treeSpec{skip: tarSkipDirs, maxFile: 2 << 20}

// gitTree is what a source build receives: the objects and refs of the
// checkout's git directory, from which the container restores the committed
// tree. Nothing else in that directory is sent. Its config is where a CI
// checkout keeps the token it fetched with and where a remote URL can hold a
// password, and its hooks are programs from the host, so neither may reach a
// container that runs documented commands with the network open.
var gitTree = treeSpec{sub: ".git", dirs: true, only: map[string]bool{
	"HEAD": true, "objects": true, "refs": true, "packed-refs": true, "shallow": true,
}}

// repoFits reports whether the working tree is small enough to stream, and is
// a stat-only walk so the answer is known before a container starts. Deciding
// after the fact is not an option: a partially streamed repository makes a
// document look broken when the missing piece is kibble's.
func repoFits(dir string) bool {
	return treeFits(dir, sessionTree)
}

// treeFits reports whether the part of dir that spec selects fits under
// [repoStreamCap], walking stat-only.
func treeFits(dir string, spec treeSpec) bool {
	if dir == "" {
		return true
	}
	total := int64(0)
	fits := true
	root := filepath.Join(dir, spec.sub)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if spec.leftOut(root, path, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || (spec.maxFile > 0 && info.Size() > spec.maxFile) {
			return nil
		}
		if total += info.Size(); total > repoStreamCap {
			fits = false
			return filepath.SkipAll
		}
		return nil
	})
	return fits
}

// repoTar packs the repo working tree for the session. Call [repoFits] first:
// this writes whatever it walks and does not decide whether the result is
// complete.
func repoTar(dir string) ([]byte, bool) {
	var buf bytes.Buffer
	truncated, _ := repoTarTo(&buf, dir)
	return buf.Bytes(), truncated
}

// repoTarTo writes the session archive to w as it walks, so nothing larger
// than one file is held at once.
func repoTarTo(w io.Writer, dir string) (bool, error) {
	return treeTarTo(w, dir, sessionTree)
}

// treeTarTo writes the part of dir that spec selects to w as a tar stream,
// one file at a time, and reports whether [repoStreamCap] cut it short.
func treeTarTo(w io.Writer, dir string, spec treeSpec) (bool, error) {
	if dir == "" {
		return false, nil
	}
	truncated := false
	tw := tar.NewWriter(w)
	total := int64(0)
	root := filepath.Join(dir, spec.sub)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		if spec.leftOut(root, path, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if spec.dirs && rel != "." {
				if err := tw.WriteHeader(&tar.Header{
					Typeflag: tar.TypeDir, Name: filepath.ToSlash(rel) + "/", Mode: 0o755,
				}); err != nil {
					return filepath.SkipAll
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || (spec.maxFile > 0 && info.Size() > spec.maxFile) {
			return nil
		}
		if total += info.Size(); total > repoStreamCap {
			truncated = true
			return filepath.SkipAll
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer func() { _ = f.Close() }()
		hdr := &tar.Header{
			Name: filepath.ToSlash(rel),
			Mode: int64(info.Mode().Perm()),
			Size: info.Size(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return filepath.SkipAll
		}
		if err := copyPadded(tw, f, info.Size()); err != nil {
			return filepath.SkipAll
		}
		return nil
	})
	err := tw.Close()
	return truncated, err
}

// copyPadded writes exactly size bytes to w, taken from r and then from zeros
// when r runs short. A file that shrank after its tar header was written still
// owes the archive its declared length, and stopping there would cut every
// later file out of the stream and leave a partial repository behind one file
// that changed mid-walk.
func copyPadded(w io.Writer, r io.Reader, size int64) error {
	n, err := io.CopyN(w, r, size)
	if err == nil {
		return nil
	}
	_, err = io.CopyN(w, zeroReader{}, size-n)
	return err
}

// zeroReader reads as an endless run of zero bytes.
type zeroReader struct{}

// Read fills p with zeros.
func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
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
