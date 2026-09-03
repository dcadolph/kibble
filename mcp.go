package main

import (
	"context"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// checkInput asks for a run against one repository.
type checkInput struct {
	// Path is the repository directory to check.
	Path string `json:"path" jsonschema:"the repository directory to check"`
	// Strict also fails on timeouts, smoke failures, drift, and gaps.
	Strict bool `json:"strict,omitempty" jsonschema:"also fail on timeouts, drift, and documentation gaps"`
}

// checkOutput is what a run reports back.
type checkOutput struct {
	// Repo is the repository directory name.
	Repo string `json:"repo"`
	// Checks are the per-step outcomes.
	Checks []checkRow `json:"checks"`
	// Pass reports whether the run would exit zero.
	Pass bool `json:"pass"`
	// Summary counts the outcomes in one line.
	Summary string `json:"summary"`
}

// checkRow is one step's outcome.
type checkRow struct {
	// Kind is the step: go-install, example, flag-check, doc-coverage, brew.
	Kind string `json:"kind"`
	// Status is PASS, FAIL, GAP, SKIP, DRIFT, TIMEOUT, or ERROR.
	Status string `json:"status"`
	// Detail explains the outcome.
	Detail string `json:"detail,omitempty"`
	// Document is the file an example step replayed, when it is not the README.
	Document string `json:"document,omitempty"`
}

// planInput asks what a run would do, without doing it.
type planInput struct {
	// Path is the repository directory to plan.
	Path string `json:"path" jsonschema:"the repository directory to plan"`
}

// planOutput is what a run would attempt and what it would leave alone.
type planOutput struct {
	// Documents are the plans, one per document that would be replayed.
	Documents []planDoc `json:"documents"`
	// Runnable counts the lines that would run across every document.
	Runnable int `json:"runnable"`
	// Skipped counts the lines that would not.
	Skipped int `json:"skipped"`
}

// planDoc is one document's plan.
type planDoc struct {
	// Repo is the repository directory name.
	Repo string `json:"repo"`
	// Binaries are the tools the session installs.
	Binaries []string `json:"binaries,omitempty"`
	// Lines are the documented lines and what would happen to each.
	Lines []planRow `json:"lines"`
}

// planRow is one documented line and kibble's judgment of it.
type planRow struct {
	// Cmd is the command as the session would run it.
	Cmd string `json:"cmd"`
	// Skip is why the line would not run, empty when it would.
	Skip string `json:"skip,omitempty"`
	// Gap marks a skip whose cause is the document rather than the container.
	Gap bool `json:"gap,omitempty"`
}

// serveMCP runs kibble as a Model Context Protocol server over stdio, so an
// agent can check a repository's documentation with the same engine and the
// same verdicts the command line gives.
func serveMCP(ctx context.Context, cfg config) error {
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "kibble",
		Version: kibbleVersion(),
	}, nil)
	registerTools(srv, cfg)
	return srv.Run(ctx, &mcpsdk.StdioTransport{})
}

// registerTools adds kibble's tools to the protocol server. The descriptions
// are written in the words someone uses when they ask for this, so a client's
// tool picker surfaces the right one, and they say plainly which tool is cheap.
func registerTools(srv *mcpsdk.Server, cfg config) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:  "plan_docs",
		Title: "See what kibble would run",
		Description: "Show what kibble would run from a repository's documentation and what " +
			"it would leave alone, without running anything. Use this first: it takes " +
			"milliseconds, needs no Docker, and answers why a documented line is skipped. " +
			"Every line comes back with kibble's judgment, and a line marked as a gap means " +
			"the document names a file, directory, or setting that no documented step " +
			"creates, which is a hole a reader would fall into.",
	}, planTool)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name:  "check_docs",
		Title: "Run the documented steps in a clean container",
		Description: "Verify that a repository's documented install steps and examples " +
			"actually work for a reader with nothing installed, by running them in a clean " +
			"container from zero. Use it to find out whether a README or a docs tree has " +
			"gone stale, whether a quickstart still works, or whether the docs cite flags " +
			"and subcommands the binary no longer has. This is slow and needs Docker: it " +
			"pulls images and builds the project, so expect minutes rather than seconds. " +
			"Run plan_docs first if you only need to know what would be attempted.",
	}, checkToolFor(cfg))
}

// planTool answers what a run would attempt, without a container.
func planTool(_ context.Context, _ *mcpsdk.CallToolRequest, in planInput) (
	*mcpsdk.CallToolResult, planOutput, error) {
	if strings.TrimSpace(in.Path) == "" {
		return nil, planOutput{}, fmt.Errorf("path is required")
	}
	_, plans, problems := collect([]string{in.Path}, true)
	if len(problems) > 0 {
		return nil, planOutput{}, fmt.Errorf("%s", problems[0].Detail)
	}
	var out planOutput
	for _, p := range plans {
		doc := planDoc{Repo: p.Repo, Binaries: p.Binaries}
		for _, step := range p.Steps {
			for _, l := range step.Lines {
				doc.Lines = append(doc.Lines, planRow{
					Cmd: flatten(l.Cmd), Skip: l.Skip, Gap: l.Gap,
				})
				if l.Skip == "" {
					out.Runnable++
				} else {
					out.Skipped++
				}
			}
		}
		out.Documents = append(out.Documents, doc)
	}
	text := fmt.Sprintf("%d lines would run, %d would not, across %d documents.",
		out.Runnable, out.Skipped, len(out.Documents))
	return textResult(text), out, nil
}

// checkToolFor builds the tool that runs the documented steps, carrying the
// image, timeout, worker, and brew settings the command line was started with.
func checkToolFor(cfg config) func(context.Context, *mcpsdk.CallToolRequest, checkInput) (
	*mcpsdk.CallToolResult, checkOutput, error) {
	return func(ctx context.Context, _ *mcpsdk.CallToolRequest, in checkInput) (
		*mcpsdk.CallToolResult, checkOutput, error) {
		if strings.TrimSpace(in.Path) == "" {
			return nil, checkOutput{}, fmt.Errorf("path is required")
		}
		steps, _, problems := collect([]string{in.Path}, true)
		if hasRunnable(steps) {
			if err := DockerAvailable(ctx); err != nil {
				return nil, checkOutput{}, fmt.Errorf("kibble needs Docker to run install steps: %w", err)
			}
		}
		runner := &DockerRunner{Image: cfg.Image, Timeout: cfg.Timeout, BrewInstall: cfg.BrewInstall}
		results := runAll(ctx, runner, steps, cfg.Workers)
		results = append(results, flagChecks(results)...)
		results = append(results, docCoverageChecks(results)...)
		results = append(results, problems...)

		out := checkOutput{Pass: !anyFail(results, in.Strict)}
		counts := map[Status]int{}
		for _, r := range results {
			out.Repo = r.Step.Repo
			counts[r.Status]++
			out.Checks = append(out.Checks, checkRow{
				Kind: r.Step.Kind, Status: string(r.Status),
				Detail: r.Detail, Document: r.Step.doc,
			})
		}
		out.Summary = fmt.Sprintf("%d pass, %d fail, %d gap, of %d checks",
			counts[StatusVerified], counts[StatusFail], counts[StatusGap], len(results))
		return textResult(out.Summary), out, nil
	}
}

// textResult wraps a one-line summary as the tool's content, so a caller that
// reads only the text still learns the outcome.
func textResult(text string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: text}},
	}
}
