package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeTool is the interface a fixture's binary truly has. The harness answers
// help probes from it the way the container does: a real subcommand returns
// its screen and exit zero, an invented one returns a parser rejection naming
// it, and a flag probe echoes either acceptance or a refusal that names the
// flag. Simulating the binary is what lets a mutation cite any name it likes
// and still receive the answer a real probe would have collected.
type fakeTool struct {
	// Root is the help screen the bare binary prints.
	Root string
	// Subs maps each real subcommand to the help screen it prints.
	Subs map[string]string
	// Flags are the exact long flags the binary accepts, anywhere. The probe
	// must match them exactly: answering by substring once accepted a typo
	// because it was a prefix of a real flag, which a real binary never does.
	Flags []string
}

// probe fills the result's help corpus for the citations in usage, mirroring
// what classify builds from the container's probe markers.
func (f fakeTool) probe(r *Result, usage *Usage) {
	help := []string{f.Root}
	r.helpRoot = f.Root
	r.subCodes = map[string]int{}
	r.helpBySub = map[string]string{}
	r.helpByFlag = map[string]string{}
	if usage == nil {
		r.helpText = f.Root
		return
	}
	for _, s := range usage.Subs {
		parts := strings.Fields(s)
		leaf := parts[len(parts)-1]
		if screen, ok := f.Subs[leaf]; ok {
			r.subCodes[s] = 0
			r.helpBySub[s] = screen
			help = append(help, screen)
			continue
		}
		r.subCodes[s] = 1
		r.helpBySub[s] = fmt.Sprintf("tool: unknown command %q for tool", leaf)
	}
	r.helpText = strings.Join(help, "\n")
	for _, fl := range usage.Flags {
		if slices.Contains(f.Flags, fl) {
			r.helpByFlag[fl] = f.Root
			continue
		}
		r.helpByFlag[fl] = "tool: unknown flag: --" + fl
	}
}

// staticVerdicts runs the judgment pipeline on a repository directory without
// Docker: real extraction from the files on disk, a simulated install whose
// probes are answered by the fake tool, a fake fetcher for brew lookups, then
// the same flag and coverage checks a real run appends.
func staticVerdicts(dir string, tool fakeTool, fetch Fetcher) []Result {
	steps, _, problems := collect([]string{dir}, false)
	var results []Result
	for _, s := range steps {
		if s.Kind == "brew" {
			results = append(results, checkBrew(s, fetch))
			continue
		}
		r := Result{Step: s, Status: StatusPass}
		if s.Binary != "" {
			tool.probe(&r, s.Usage)
		}
		results = append(results, r)
	}
	results = append(results, flagChecks(results)...)
	results = append(results, docCoverageChecks(results)...)
	results = append(results, problems...)
	return results
}

// nonGreen returns the results that would draw a reader's eye: anything that
// is not a pass or an intentional skip.
func nonGreen(results []Result) []Result {
	var out []Result
	for _, r := range results {
		switch r.Status {
		case StatusPass, StatusPassBuild, StatusSkipped:
		default:
			out = append(out, r)
		}
	}
	return out
}

// TestDocumentMutations corrupts a healthy document one edit at a time and
// checks that the verdict flips. Each case proves two things through the real
// extractor: the untouched tree comes back green, so the checks do not cry
// wolf, and the single mutation is caught, so rot cannot pass silently. Unit
// tests pin behaviors one function at a time; this pins the property the tool
// exists for.
func TestDocumentMutations(t *testing.T) {
	t.Parallel()

	readme := `# mytool

## Install

` + "```sh" + `
go install example.com/mytool@latest
` + "```" + `

Or with brew:

` + "```sh" + `
brew install example/tap/mytool
` + "```" + `

## Usage

` + "```sh" + `
mytool --dialect plain
mytool walk --depth 3
mytool sync
` + "```" + `
`

	tool := fakeTool{
		Root: `Usage: mytool [flags] <command>

Commands:
  walk   Walk the tree
  sync   Bring the tree up to date

Flags:
      --dialect string   output dialect
`,
		Subs: map[string]string{
			"walk": "Usage: mytool walk\n\nFlags:\n      --depth int   how deep to go\n",
			"sync": "Usage: mytool sync\n",
		},
		Flags: []string{"dialect", "depth"},
	}

	fetch := FetcherFunc(func(url string) (int, error) {
		if strings.Contains(url, "homebrew-tap/HEAD/Formula/mytool.rb") {
			return 200, nil
		}
		return 404, nil
	})

	tests := []struct {
		Find       string
		Replace    string
		WantStatus Status
		WantDetail string
	}{{ // Test 0: a typo in a root flag is drift, convicted by the probe.
		Find: "--dialect plain", Replace: "--dialec plain",
		WantStatus: StatusDrift, WantDetail: "missing --dialec",
	}, { // Test 1: a typo in a subcommand flag is drift the same way.
		Find: "--depth 3", Replace: "--dpeth 3",
		WantStatus: StatusDrift, WantDetail: "missing --dpeth",
	}, { // Test 2: a renamed subcommand is drift, since the binary rejects it
		// by name.
		Find: "mytool sync", Replace: "mytool stync",
		WantStatus: StatusDrift, WantDetail: "unknown subcommand stync",
	}, { // Test 3: dropping a command's only mention is a coverage gap. The
		// help still advertises it; the docs no longer say what it is.
		Find: "mytool sync\n", Replace: "",
		WantStatus: StatusGap, WantDetail: "documented nowhere: sync",
	}, { // Test 4: a typo in a brew name is a gap, never a failure, because
		// an index miss is not an executed line.
		Find: "brew install example/tap/mytool", Replace: "brew install example/tap/mytol",
		WantStatus: StatusGap, WantDetail: "-brew-install to settle it",
	}}

	write := func(t *testing.T, text string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	// The healthy tree must be entirely green before any mutation counts.
	// A harness whose baseline already carries a finding proves nothing.
	if bad := nonGreen(staticVerdicts(write(t, readme), tool, fetch)); len(bad) != 0 {
		for _, r := range bad {
			t.Errorf("healthy tree not green: %s %s %s", r.Step.Kind, r.Status, r.Detail)
		}
		t.Fatal("baseline is dirty, mutations prove nothing")
	}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if strings.Count(readme, test.Find) != 1 {
				t.Fatalf("mutation target %q not found exactly once", test.Find)
			}
			mutated := strings.Replace(readme, test.Find, test.Replace, 1)
			results := staticVerdicts(write(t, mutated), tool, fetch)
			for _, r := range results {
				if r.Status == test.WantStatus && strings.Contains(r.Detail, test.WantDetail) {
					return
				}
			}
			t.Errorf("mutation %q -> %q escaped: want %s containing %q",
				test.Find, test.Replace, test.WantStatus, test.WantDetail)
			for _, r := range results {
				t.Logf("  got: %-12s %-6s %s", r.Step.Kind, r.Status, r.Detail)
			}
		})
	}
}
