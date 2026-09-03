package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// githubOutput emits GitHub Actions workflow commands for the run: an error
// annotation pinned to the README line of every failed step, a warning for
// every drift or error, and a results table appended to the job's step
// summary. The annotations put a broken doc line inline in the pull request
// the way a failing test would appear.
func githubOutput(w io.Writer, results []Result) {
	for _, r := range results {
		file := readmePath(r.Step.dir, r.Step.readme)
		switch r.Status {
		case StatusFail:
			if r.example != nil {
				annotateExample(w, file, r.example)
				continue
			}
			annotate(w, "error", file, r.Step.Line,
				fmt.Sprintf("kibble: documented %s failed: %s: %s", r.Step.Kind, r.Step.Raw, r.Detail))
		case StatusDrift:
			annotate(w, "warning", file, r.Step.Line,
				fmt.Sprintf("kibble: docs drifted from the binary: %s", r.Detail))
		case StatusError:
			annotate(w, "warning", file, 0, fmt.Sprintf("kibble: %s", r.Detail))
		}
	}
	writeStepSummary(results)
}

// annotateExample emits one error annotation per failed example line, at the
// README line the command sits on.
func annotateExample(w io.Writer, file string, run *exampleRun) {
	for _, s := range run.Steps {
		for _, l := range s.Lines {
			if l.Status != StatusFail {
				continue
			}
			annotate(w, "error", file, l.Line,
				fmt.Sprintf("kibble: documented example failed: %s: %s", l.Cmd, l.Detail))
		}
	}
}

// annotate writes one workflow command. A zero line anchors to the top of the
// file, since an annotation without a line is dropped by some renderers.
func annotate(w io.Writer, level, file string, line int, msg string) {
	if line < 1 {
		line = 1
	}
	_, _ = fmt.Fprintf(w, "::%s file=%s,line=%d::%s\n", level, file, line, escapeAnnotation(msg))
}

// escapeAnnotation escapes the characters GitHub workflow commands reserve.
func escapeAnnotation(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	s = strings.ReplaceAll(s, "\r", "%0D")
	s = strings.ReplaceAll(s, "\n", "%0A")
	return s
}

// readmePath returns the repo-relative README path for annotations, using the
// file name the repository actually has so an annotation is not pinned to a
// README.md that never existed. In a workflow the target is the checkout
// root, so the path is the file name or the subdirectory it sits in.
func readmePath(dir, name string) string {
	if name == "" {
		name = readmeNames[0]
	}
	dir = filepath.ToSlash(filepath.Clean(dir))
	if dir == "." || dir == "" || filepath.IsAbs(dir) {
		return name
	}
	return dir + "/" + name
}

// writeStepSummary appends a markdown results table to the job's step summary
// when the environment provides one, so the run's verdict is readable on the
// workflow page without opening logs.
func writeStepSummary(results []Result) {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintln(f, "### kibble: dogfooding your docs")
	_, _ = fmt.Fprintln(f)
	_, _ = fmt.Fprintln(f, "| Repo | Step | Status | Time | Detail |")
	_, _ = fmt.Fprintln(f, "| ---- | ---- | ------ | ---- | ------ |")
	for _, r := range results {
		detail := r.Detail
		if detail == "" {
			detail = r.SmokeLine
		}
		_, _ = fmt.Fprintf(f, "| %s | %s | %s | %s | %s |\n",
			r.Step.Repo, r.Step.Kind, statusBadge(r.Status),
			r.Duration.Round(time.Second), strings.ReplaceAll(truncate(detail, 80), "|", "\\|"))
	}
	_, _ = fmt.Fprintln(f)
}

// statusBadge returns a status with the emoji GitHub renders in summaries,
// so the verdict reads at a glance.
func statusBadge(s Status) string {
	switch s {
	case StatusVerified, StatusBuilt:
		return "✅ " + string(s)
	case StatusFail:
		return "❌ " + string(s)
	case StatusDrift, StatusError, StatusTimeout:
		return "⚠️ " + string(s)
	default:
		return "⏭️ " + string(s)
	}
}
