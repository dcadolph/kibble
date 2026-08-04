// Command kibble verifies that a project's documented install steps actually
// work for a fresh user, by running each in a clean container from zero.
// It is the proving ground for your docs: kibble runs your README so your
// users do not have to find out it is stale.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
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
	// Examples reports whether to replay README example blocks.
	Examples bool
	// Plan reports whether to print the example plans and exit.
	Plan bool
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
	flag.BoolVar(&cfg.Examples, "examples", true, "replay README example blocks in the container")
	flag.BoolVar(&cfg.Plan, "plan", false, "print the example plans as JSON and exit")
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

	steps, plans := collect(paths, cfg.Examples || cfg.Plan)
	if cfg.Plan {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(plans)
		return
	}
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
	results = append(results, flagChecks(results)...)

	report(os.Stdout, results, cfg.JSON)
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		githubOutput(os.Stdout, results)
	}
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

// collect reads each repo's README, extracts its install steps, attaches
// the flag and subcommand usage the README cites for each installed binary,
// and, when examples are on, builds an example plan per repo.
func collect(paths []string, examples bool) ([]InstallStep, []*Plan) {
	ex := DefaultExtractor()
	var out []InstallStep
	var plans []*Plan
	for _, p := range paths {
		repo := filepath.Base(filepath.Clean(p))
		md, err := readREADME(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", repo, err)
			continue
		}
		steps := ex.Extract(repo, md)
		bins, installs := sessionInstalls(p, steps)
		usage := extractUsage(bins, md)
		for i := range steps {
			steps[i].dir = p
			if steps[i].Kind == "go-install" {
				steps[i].Usage = usage[steps[i].Binary]
			}
		}
		out = append(out, steps...)
		if !examples {
			continue
		}
		if step, plan := exampleStepFor(repo, p, md, sessionBinaries(repo, bins, installs), installs); plan != nil {
			plans = append(plans, plan)
			if step != nil {
				out = append(out, *step)
			}
		}
	}
	return out, plans
}

// sessionInstalls returns the documented binaries and the installs that put
// them on PATH. Every install kind counts, not only Go, so a Rust or Node
// project's examples run against the tool its own README installs.
func sessionInstalls(dir string, steps []InstallStep) ([]string, []PlanInstall) {
	var bins []string
	var all []PlanInstall
	for _, s := range steps {
		var in PlanInstall
		switch {
		case s.Kind == "go-install":
			in = PlanInstall{Cmd: "go install " + s.Module, Ecosystem: "go", binary: s.Binary}
		case pkgKinds[s.Kind].Ecosystem != "":
			pk := pkgKinds[s.Kind]
			in = PlanInstall{
				Cmd: shellCommand(s.Raw), Ecosystem: pk.Ecosystem,
				binary: s.Binary, bootstrap: pk.Bootstrap,
			}
		default:
			continue
		}
		all = append(all, in)
		if s.Binary != "" && !slices.Contains(bins, s.Binary) {
			bins = append(bins, s.Binary)
		}
	}
	return bins, oncePerBinary(sameEcosystem(all, dir))
}

// sessionBinaries pads the documented binaries with the repository's own name
// for the example session, because a package rarely names its binary, as
// fd-find provides fd. The padded name is a PATH candidate only; usage
// extraction keeps the unpadded list so a flag table attributes correctly.
func sessionBinaries(repo string, bins []string, installs []PlanInstall) []string {
	if len(installs) > 0 && reSimpleWord.MatchString(repo) && !slices.Contains(bins, repo) {
		return append(append([]string{}, bins...), repo)
	}
	return bins
}

// oncePerBinary drops installs that provide a tool an earlier install already
// provides. A project documenting uv, pip, and pipx installs of the same tool
// is listing three routes to one binary, and the session needs one.
func oncePerBinary(installs []PlanInstall) []PlanInstall {
	seen := map[string]bool{}
	var out []PlanInstall
	for _, in := range installs {
		if in.binary != "" && seen[in.binary] {
			continue
		}
		seen[in.binary] = true
		out = append(out, in)
	}
	return out
}

// sameEcosystem narrows documented installs to one toolchain, since a README
// offering both a cargo and an npm install is offering alternatives and a
// session needs only one. The repository's own manifests choose, so the tool
// is installed the way the project is built; otherwise the first install wins.
// Several installs of the chosen toolchain are all kept, since a project that
// documents two binaries needs both.
func sameEcosystem(installs []PlanInstall, dir string) []PlanInstall {
	if len(installs) == 0 {
		return nil
	}
	want := installs[0].Ecosystem
	if eco, ok := ecosystemFromManifests(dir); ok {
		for _, in := range installs {
			if in.Ecosystem == eco {
				want = eco
				break
			}
		}
	}
	var out []PlanInstall
	for _, in := range installs {
		if in.Ecosystem == want {
			out = append(out, in)
		}
	}
	return out
}

// exampleStepFor builds a repo's example plan and, when it has steps, the
// install step that runs it. A bad .kibble.yml is reported and examples are
// dropped for the repo, so a config typo cannot pass as a green check.
func exampleStepFor(repo, dir, md string, bins []string, installs []PlanInstall) (*InstallStep, *Plan) {
	cfg, err := loadExamplesConfig(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skip %s examples: bad .kibble.yml: %v\n", repo, err)
		return nil, nil
	}
	if cfg != nil && cfg.Disable {
		return nil, nil
	}
	plan := buildPlan(repo, dir, md, bins, installs, cfg)
	if len(plan.Steps) == 0 {
		return nil, plan
	}
	lines := 0
	for _, s := range plan.Steps {
		lines += len(s.Lines)
	}
	step := &InstallStep{
		Repo: repo, Kind: "example", Run: true,
		Raw:  fmt.Sprintf("%d blocks, %d lines", len(plan.Steps), lines),
		plan: plan, dir: dir,
	}
	return step, plan
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
// counts, and so does an error, since kibble could not do its job and CI should
// notice. In strict mode, timeouts, smoke-test failures, and doc drift count too.
func anyFail(results []Result, strict bool) bool {
	for _, r := range results {
		if r.Status == StatusFail || r.Status == StatusError {
			return true
		}
		if strict && (r.Status == StatusTimeout || r.Status == StatusPassBuild || r.Status == StatusDrift) {
			return true
		}
	}
	return false
}
