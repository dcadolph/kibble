package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// planLine is a compact projection of a PlanLine for test comparison.
type planLine struct {
	Cmd       string
	Skip      bool
	NonzeroOK bool
}

// projectPlan reduces a plan's steps to compact lines for comparison.
func projectPlan(p *Plan) [][]planLine {
	var out [][]planLine
	for _, s := range p.Steps {
		var step []planLine
		for _, l := range s.Lines {
			step = append(step, planLine{
				Cmd: flatten(l.Cmd), Skip: l.Skip != "", NonzeroOK: l.NonzeroOK,
			})
		}
		out = append(out, step)
	}
	return out
}

func TestBuildPlan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Markdown     string
		Cfg          *ExamplesConfig
		WantSteps    [][]planLine
		WantFixtures []string
		WantPackages []string
		WantExcluded int
	}{{ // Test 0: prose and non-shell blocks are excluded, commands kept.
		Markdown: "# T\n```sh\ntool run notes.md\n```\n```yaml\nkey: value\n```\n" +
			"```\nSome prose sentence here.\n```\n",
		WantSteps:    [][]planLine{{{Cmd: "tool run notes.md"}}},
		WantFixtures: []string{"notes.md"},
		WantExcluded: 1,
	}, { // Test 1: placeholders skip the line.
		Markdown:  "```sh\ntool add --key <api-key>\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool add --key <api-key>", Skip: true}}},
	}, { // Test 2: a login line poisons later invocations, info lines exempt.
		Markdown: "```sh\ntool login\ntool sync\ntool --version\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "tool login", Skip: true},
			{Cmd: "tool sync", Skip: true},
			{Cmd: "tool --version"},
		}},
	}, { // Test 3: two-column usage blocks lose the description column.
		Markdown: "```\ntool add \"x\"      Append an entry to today.\n" +
			"tool list          Print every entry.\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool add \"x\""}, {Cmd: "tool list"}}},
	}, { // Test 4: a nonzero comment marks the next line and its family.
		Markdown: "```sh\n# Flags what it finds (exits non-zero if it finds any)\n" +
			"tool check a.md\ntool check b.md\ntool other a.md\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "tool check a.md", NonzeroOK: true},
			{Cmd: "tool check b.md", NonzeroOK: true},
			{Cmd: "tool other a.md"},
		}},
		WantFixtures: []string{"a.md", "b.md"},
	}, { // Test 5: git clone blocks belong to the install checks.
		Markdown:  "```sh\ngit clone https://github.com/o/r.git\ncd r\nmake install\n```\n",
		WantSteps: nil,
	}, { // Test 6: go install lines drop, the rest of the block stays.
		Markdown:  "```sh\ngo install example.com/tool@latest\ntool --version\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool --version"}}},
	}, { // Test 7: structured missing files skip, localhost skips.
		Markdown: "```sh\ntool load data.csv\ntool ping --base-url http://localhost:9\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "tool load data.csv", Skip: true},
			{Cmd: "tool ping --base-url http://localhost:9", Skip: true},
		}},
	}, { // Test 8: a skipped export poisons lines expanding its variable.
		Markdown: "```sh\nexport KEY=<fill-me>\ntool use \"$KEY\"\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "export KEY=<fill-me>", Skip: true},
			{Cmd: "tool use \"$KEY\"", Skip: true},
		}},
	}, { // Test 9: package tools are collected for the session.
		Markdown:     "```sh\nage-keygen -o key.txt\ntool read key.txt\n```\n",
		WantSteps:    [][]planLine{{{Cmd: "age-keygen -o key.txt"}, {Cmd: "tool read key.txt"}}},
		WantPackages: []string{"age"},
	}, { // Test 10: interactive subcommands skip instead of hanging.
		Markdown:  "```sh\ntool demo\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool demo", Skip: true}}},
	}, { // Test 11: a bare - argument without a pipe reads missing stdin.
		Markdown: "```sh\ntool import - --date 2026-06-10\necho x | tool import -\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "tool import - --date 2026-06-10", Skip: true},
			{Cmd: "echo x | tool import -"},
		}},
	}, { // Test 12: config rules force lines to run or skip and mark nonzero.
		Markdown: "```sh\ntool serve\ntool check x.md\n```\n",
		Cfg: &ExamplesConfig{Steps: []StepRule{
			{Match: "tool serve", Run: true},
			{Match: "tool check", NonzeroOK: true},
		}},
		WantSteps: [][]planLine{{
			{Cmd: "tool serve"},
			{Cmd: "tool check x.md", NonzeroOK: true},
		}},
		WantFixtures: []string{"x.md"},
	}, { // Test 13: config substitutions resolve placeholders before checks.
		Markdown: "```sh\ntool add --key <api-key>\n```\n",
		Cfg: &ExamplesConfig{
			Substitutions: map[string]string{"<api-key>": "dummy"},
		},
		WantSteps: [][]planLine{{{Cmd: "tool add --key dummy"}}},
	}, { // Test 14: heredocs stay one logical line.
		Markdown: "```sh\ncat > hook <<'EOF'\ntool precommit\nEOF\nchmod +x hook\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "cat > hook <<'EOF'"},
			{Cmd: "chmod +x hook"},
		}},
	}, { // Test 15: prompt-style blocks keep only prompted lines.
		Markdown:  "```console\n$ tool run\noutput line\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool run"}}},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := buildPlan("repo", "", test.Markdown,
				[]string{"tool"}, []string{"example.com/tool@latest"}, test.Cfg)
			if diff := cmp.Diff(test.WantSteps, projectPlan(plan), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("steps mismatch (-want +got):\n%s", diff)
			}
			var fixtures []string
			for _, f := range plan.Fixtures {
				fixtures = append(fixtures, f.Path)
			}
			if diff := cmp.Diff(test.WantFixtures, fixtures, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("fixtures mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantPackages, plan.Packages, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("packages mismatch (-want +got):\n%s", diff)
			}
			if plan.Excluded != test.WantExcluded {
				t.Errorf("excluded = %d, want %d", plan.Excluded, test.WantExcluded)
			}
		})
	}
}

func TestBuildPlanRepoTree(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "examples"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "examples", "people.csv"), []byte("a,b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	md := "```sh\ntool index --file examples/people.csv\ntool index --file missing.csv\n```\n"
	plan := buildPlan("repo", dir, md, []string{"tool"}, nil, nil)
	want := [][]planLine{{
		{Cmd: "tool index --file examples/people.csv"},
		{Cmd: "tool index --file missing.csv", Skip: true},
	}}
	if diff := cmp.Diff(want, projectPlan(plan)); diff != "" {
		t.Errorf("steps mismatch (-want +got):\n%s", diff)
	}
}

func TestLogicalLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   []string
		Want []string
	}{{ // Test 0: continuations join.
		In:   []string{`tool run --a 1 \`, "  --b 2", "tool other"},
		Want: []string{"tool run --a 1 \\\n  --b 2", "tool other"},
	}, { // Test 1: heredocs run to their terminator.
		In:   []string{"cat > f <<'EOF'", "body", "EOF", "next"},
		Want: []string{"cat > f <<'EOF'\nbody\nEOF", "next"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, logicalLines(test.In)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
