// Command kibble verifies that a project's documented install steps actually
// work for a fresh user, by running each in a clean container from zero.
// It is the proving ground for your docs: kibble runs your README so your
// users do not have to find out it is stale.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"
)

// config holds the resolved run options.
type config struct {
	// Image is the container image used for clean-room installs.
	Image string
	// Timeout is the per-step build timeout.
	Timeout time.Duration
	// Workers is the maximum number of concurrent installs.
	Workers int
	// JSON reports whether to emit machine-readable output.
	JSON bool
	// Version reports whether to print the version and exit.
	Version bool
	// Strict reports whether timeouts and smoke failures also fail the run.
	Strict bool
}

// main parses flags, collects install steps, runs them, and reports.
func main() {
	var cfg config
	flag.StringVar(&cfg.Image, "image", "golang:1.26", "container image for clean-room installs")
	flag.DurationVar(&cfg.Timeout, "timeout", 240*time.Second, "per-step build timeout")
	flag.IntVar(&cfg.Workers, "workers", 3, "max concurrent installs")
	flag.BoolVar(&cfg.JSON, "json", false, "emit results as JSON to stdout")
	flag.BoolVar(&cfg.Version, "version", false, "print the version and exit")
	flag.BoolVar(&cfg.Strict, "strict", false, "also fail on timeouts and smoke-test failures")
	flag.Parse()

	if cfg.Version {
		fmt.Println(kibbleVersion())
		return
	}

	paths := flag.Args()
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kibble [flags] <repo-path>...")
		os.Exit(2)
	}

	steps := collect(paths)
	if len(steps) == 0 {
		fmt.Fprintln(os.Stderr, "no install steps found")
		os.Exit(0)
	}

	if hasRunnable(steps) {
		if err := DockerAvailable(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "kibble needs Docker to run install steps: %v\n", err)
			os.Exit(2)
		}
	}

	runner := &DockerRunner{Image: cfg.Image, Timeout: cfg.Timeout}
	results := runAll(context.Background(), runner, steps, cfg.Workers)

	report(os.Stdout, results, cfg.JSON)
	if anyFail(results, cfg.Strict) {
		os.Exit(1)
	}
}

// kibbleVersion reports kibble's own version from the build info, so a binary
// installed with `go install` names its real version instead of dev. kibble
// eats its own dog food: the version bug it catches in others, it avoids.
func kibbleVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}

// collect reads each repo's README and extracts its install steps.
func collect(paths []string) []InstallStep {
	ex := DefaultExtractor()
	var out []InstallStep
	for _, p := range paths {
		repo := filepath.Base(filepath.Clean(p))
		md, err := readREADME(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", repo, err)
			continue
		}
		out = append(out, ex.Extract(repo, md)...)
	}
	return out
}

// readREADME returns the README contents for a repo directory.
func readREADME(dir string) (string, error) {
	for _, name := range []string{"README.md", "readme.md", "README.MD"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("no README found")
}

// runAll executes steps with bounded concurrency and returns their results.
func runAll(ctx context.Context, r Runner, steps []InstallStep, workers int) []Result {
	if workers < 1 {
		workers = 1
	}
	results := make([]Result, len(steps))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, step := range steps {
		if !step.Run {
			results[i] = Result{Step: step, Status: StatusSkipped, Detail: "not executed yet"}
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, s InstallStep) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = r.Run(ctx, s)
		}(i, step)
	}
	wg.Wait()
	return results
}

// hasRunnable reports whether any step will actually be executed.
func hasRunnable(steps []InstallStep) bool {
	for _, s := range steps {
		if s.Run {
			return true
		}
	}
	return false
}

// anyFail reports whether the run should exit non-zero. A build failure always
// counts. In strict mode, timeouts and smoke-test failures count too.
func anyFail(results []Result, strict bool) bool {
	for _, r := range results {
		if r.Status == StatusFail {
			return true
		}
		if strict && (r.Status == StatusTimeout || r.Status == StatusPassBuild) {
			return true
		}
	}
	return false
}
