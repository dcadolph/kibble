// Command kibble verifies that a project's documented install steps actually
// work for a fresh user, by running each in a clean container from zero.
// Eating your own dog food means using what you ship the way a stranger
// would, and kibble is the bowl: it reads your README so your users do not
// have to find out it is stale.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"syscall"
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
	// Suggest reports whether to propose a .kibble.yml and exit.
	Suggest bool
	// MCP reports whether to serve the Model Context Protocol over stdio.
	MCP bool
	// BrewInstall runs documented brew installs instead of checking that the
	// formula exists.
	BrewInstall bool
}

// main parses flags, collects install steps, runs them, and reports.
func main() {
	var cfg config
	flag.StringVar(&cfg.Image, "image", "golang:1.26", "container image for clean-room installs")
	flag.DurationVar(&cfg.Timeout, "timeout", 240*time.Second, "per-step build timeout")
	flag.IntVar(&cfg.Workers, "workers", 3, "max concurrent installs")
	flag.BoolVar(&cfg.JSON, "json", false, "emit results as JSON to stdout")
	flag.BoolVar(&cfg.Version, "version", false, "print the version and exit")
	flag.BoolVar(&cfg.Strict, "strict", false,
		"also fail on timeouts, smoke-test failures, drift, and documentation gaps")
	flag.BoolVar(&cfg.Examples, "examples", true, "replay each document's example blocks in the container")
	flag.BoolVar(&cfg.Plan, "plan", false, "print the example plans as JSON and exit")
	flag.BoolVar(&cfg.Suggest, "suggest", false,
		"propose a .kibble.yml using a configured model and exit")
	flag.BoolVar(&cfg.BrewInstall, "brew-install", false,
		"run documented brew installs for real instead of checking the formula exists")
	flag.BoolVar(&cfg.MCP, "mcp", false,
		"serve the Model Context Protocol over stdio so an agent can drive kibble")
	flag.Parse()

	if cfg.MCP {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		// A client closing the pipe ends the session, which is how a stdio
		// server is meant to finish rather than a failure to report.
		if err := serveMCP(ctx, cfg); err != nil && ctx.Err() == nil &&
			!errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "EOF") {
			fmt.Fprintf(os.Stderr, "kibble: mcp server stopped: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if cfg.Version {
		fmt.Println(kibbleVersion())
		return
	}

	// With no path, check the directory the reader is standing in, the way
	// git and cargo do. A repository is recognized by having a README, since
	// that is the document kibble exists to run. Anywhere else, say how to
	// use it rather than reporting nothing found.
	paths := flag.Args()
	if len(paths) == 0 {
		if _, _, err := readREADME("."); err != nil {
			fmt.Fprintln(os.Stderr, "usage: kibble [flags] [repo-path...]")
			fmt.Fprintln(os.Stderr, "with no path, kibble checks the current directory")
			os.Exit(2)
		}
		paths = []string{"."}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	steps, plans, problems := collect(paths, cfg.Examples || cfg.Plan || cfg.Suggest)
	if cfg.Suggest {
		os.Exit(suggestConfigs(ctx, os.Stdout, plans))
	}
	if cfg.Plan {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(plans); err != nil {
			fmt.Fprintf(os.Stderr, "kibble could not write the plan: %v\n", err)
			os.Exit(1)
		}
		if len(problems) > 0 {
			os.Exit(1)
		}
		return
	}
	if len(steps) == 0 && len(problems) == 0 {
		fmt.Fprintln(os.Stderr, "no install steps found")
		os.Exit(0)
	}

	if hasRunnable(steps) {
		if err := DockerAvailable(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "kibble needs Docker to run install steps: %v\n", err)
			os.Exit(2)
		}
	}

	runner := &DockerRunner{Image: cfg.Image, Timeout: cfg.Timeout, BrewInstall: cfg.BrewInstall}
	results := runAll(ctx, runner, steps, cfg.Workers)
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "kibble: interrupted")
		os.Exit(130)
	}
	results = append(results, flagChecks(results)...)
	results = append(results, docCoverageChecks(results)...)
	results = append(results, problems...)

	report(os.Stdout, results, cfg.JSON)
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		githubOutput(os.Stdout, results)
	}
	if anyFail(results, cfg.Strict) {
		os.Exit(1)
	}
}

// suggestConfigs proposes a .kibble.yml per repository and returns the exit
// code. The model classifies only the lines the engine could not settle, and
// its answer is written for a human to read and commit, never applied to a
// run. A repository the engine already understands produces no file, which is
// the good outcome.
func suggestConfigs(ctx context.Context, w io.Writer, plans []*Plan) int {
	advisor, ok := NewAdvisor()
	if !ok {
		fmt.Fprintln(os.Stderr, advisorHelp)
		return 2
	}
	wrote := false
	for _, plan := range plans {
		cands := suggestCandidates(plan)
		if len(cands) == 0 {
			fmt.Fprintf(os.Stderr, "%s: nothing to ask about\n", plan.Repo)
			continue
		}
		got, err := askAdvisor(ctx, advisor, plan.Repo, cands)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", plan.Repo, err)
			return 1
		}
		if writeSuggestedConfig(w, plan.Repo, cands, got) {
			wrote = true
			continue
		}
		fmt.Fprintf(os.Stderr, "%s: the model agreed with every call kibble made\n", plan.Repo)
	}
	if !wrote {
		fmt.Fprintln(os.Stderr, "no config needed")
	}
	return 0
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
// and, when examples are on, builds an example plan per repo. It also returns
// a result per repository kibble could not read, so a path with no README and
// a malformed config both reach the report instead of passing as silence.
func collect(paths []string, examples bool) ([]InstallStep, []*Plan, []Result) {
	ex := DefaultExtractor()
	var out []InstallStep
	var plans []*Plan
	var problems []Result
	for _, p := range paths {
		repo := repoName(p)
		md, name, err := readREADME(p)
		if err != nil {
			problems = append(problems, Result{
				Step:   InstallStep{Repo: repo, Kind: "readme", dir: p},
				Status: StatusError,
				Detail: fmt.Sprintf("kibble read nothing from %s: %v", p, err),
			})
			continue
		}
		steps := ex.Extract(repo, md)
		bins, installs := sessionInstalls(p, steps)
		// Flags and subcommands are cited across the whole documentation set,
		// not only the README, and a reference page drifts faster than a front
		// page because nobody reads it on the way past. A flag cited on a
		// subcommand whose help never arrived is reported as unverified rather
		// than as drift, so reading more documents cannot invent a failure.
		usage := extractUsage(bins, readDocSet(p, name).All)
		for i := range steps {
			steps[i].dir = p
			steps[i].readme = name
			if steps[i].Binary != "" {
				steps[i].Usage = usage[steps[i].Binary]
			}
		}
		out = append(out, steps...)
		if !examples {
			continue
		}
		cfg, err := loadExamplesConfig(p)
		if err != nil {
			problems = append(problems, Result{
				Step:   InstallStep{Repo: repo, Kind: "config", dir: p, readme: name},
				Status: StatusError,
				Detail: fmt.Sprintf("bad .kibble.yml, so examples did not run: %v", err),
			})
			continue
		}
		// Instructions outlive the README they started in. A docs tree is
		// where install and usage steps go once the front page fills up, and
		// it rots faster because nobody reads it on the way past, so every
		// document that walks a reader through commands gets its own session.
		sessionBins := sessionBinaries(repo, bins, installs)
		// Settings are documented once for a repository, often in the README,
		// and every document's session inherits them. Reading only the
		// document being replayed would report a key as undocumented because
		// the page citing it is a different page.
		repoSettings := documentedSettingNames(readDocSet(p, name).All)
		for _, doc := range replayDocs(p, name, cfg) {
			text := md
			if doc != name {
				body, rerr := os.ReadFile(filepath.Join(p, doc))
				if rerr != nil {
					continue
				}
				text = string(body)
			}
			step, plan, perr := exampleStepFor(repo, p, doc, text, sessionBins, installs, cfg)
			if plan != nil {
				plan.Settings = repoSettings
			}
			if perr != nil || plan == nil {
				continue
			}
			plans = append(plans, plan)
			if step != nil {
				step.readme = name
				out = append(out, *step)
			}
		}
	}
	return out, plans, problems
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
// install step that runs it. A bad .kibble.yml is returned as an error rather
// than dropped, so a config typo cannot pass as a green check.
func exampleStepFor(repo, dir, doc, md string, bins []string, installs []PlanInstall,
	cfg *ExamplesConfig) (*InstallStep, *Plan, error) {
	if cfg != nil && cfg.Disable {
		return nil, nil, nil
	}
	plan := buildPlan(repo, dir, md, bins, installs, cfg)
	if len(plan.Steps) == 0 {
		return nil, plan, nil
	}
	lines := 0
	for _, s := range plan.Steps {
		lines += len(s.Lines)
	}
	step := &InstallStep{
		Repo: repo, Kind: "example", Run: true,
		Raw:  fmt.Sprintf("%d blocks, %d lines", len(plan.Steps), lines),
		plan: plan, dir: dir, doc: doc,
	}
	return step, plan, nil
}

// readmeNames are the README file names kibble looks for, in order.
var readmeNames = []string{"README.md", "readme.md", "README.MD"}

// readREADME returns a repo directory's README contents and the file name it
// came from, so an annotation can point at the file the repository has. The
// directory is listed rather than opened by name, because a case-insensitive
// filesystem answers to README.md for a file named readme.md and would have
// kibble annotate a path that does not exist on a case-sensitive host.
func readREADME(dir string) (string, string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", err
	}
	present := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			present[e.Name()] = true
		}
	}
	for _, name := range readmeNames {
		if !present[name] {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", "", err
		}
		return string(b), name, nil
	}
	return "", "", fmt.Errorf("no README found")
}

// runAll executes steps with bounded concurrency and returns their results.
// A canceled run stops handing out work, so an interrupt does not queue more
// containers behind the ones already being torn down.
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
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			results[i] = Result{
				Step: step, Status: StatusError,
				Detail: "run was interrupted before this step started",
			}
			continue
		}
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
// notice. In strict mode, timeouts, smoke-test failures, doc drift, and
// documentation gaps count too. A gap never fails a default run: it says the
// document is incomplete, which is worth reporting but is not proof of a
// broken install, and a checker that breaks everyone's build gets uninstalled.
func anyFail(results []Result, strict bool) bool {
	for _, r := range results {
		if r.Status == StatusFail || r.Status == StatusError {
			return true
		}
		if strict && (r.Status == StatusTimeout || r.Status == StatusPassBuild ||
			r.Status == StatusDrift || r.Status == StatusGap) {
			return true
		}
	}
	return false
}

// repoName is the name a report calls a repository. A relative path such as
// "." or ".." names nothing a reader recognizes, so it resolves to the
// directory's real name before falling back to the path as given.
func repoName(path string) string {
	clean := filepath.Base(filepath.Clean(path))
	if clean != "." && clean != ".." && clean != string(filepath.Separator) {
		return clean
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return clean
	}
	return filepath.Base(abs)
}
