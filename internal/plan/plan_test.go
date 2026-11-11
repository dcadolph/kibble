package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dcadolph/kibble/internal/config"
	"github.com/dcadolph/kibble/internal/docblock"
	"github.com/dcadolph/kibble/internal/verdict"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// planLine is a compact projection of a PlanLine for test comparison.
type planLine struct {
	Cmd       string
	Need      string
	Skip      bool
	Gap       bool
	NonzeroOK bool
}

// projectPlan reduces a plan's steps to compact lines for comparison.
func projectPlan(p *Plan) [][]planLine {
	var out [][]planLine
	for _, s := range p.Steps {
		var step []planLine
		for _, l := range s.Lines {
			step = append(step, planLine{
				Cmd: docblock.Flatten(l.Cmd), Need: l.Need, Skip: l.Skip != "", Gap: l.Gap,
				NonzeroOK: l.NonzeroOK,
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
		Cfg          *config.ExamplesConfig
		Module       string
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
	}, { // Test 2: a login line skips itself but no longer skips what follows.
		// The executor downgrades a real credential failure to a skip, so a
		// later line runs and is judged on its own output instead of being
		// assumed broken, which is where most of a document's coverage went.
		Markdown: "```sh\ntool login\ntool sync\ntool --version\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "tool login", Skip: true},
			{Cmd: "tool sync"},
			{Cmd: "tool --version"},
		}},
	}, { // Test 2a: a file no line is seen to create runs, and the session
		// checks for it, since only the run can prove the document's gap.
		Markdown:  "```sh\ntool run ./secrets\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool run ./secrets", Need: "./secrets"}}},
	}, { // Test 2b: an unset variable is not a gap. An interactive shell
		// provides names such as HISTFILE that no documented line assigns, so
		// the document is right and only the container is not that shell.
		Markdown:  "```sh\ntool run --token $TOKEN\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool run --token $TOKEN", Skip: true}}},
	}, { // Test 2c: a placeholder is the reader's to fill in, so it is not a gap.
		Markdown:  "```sh\ntool add --key <api-key>\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool add --key <api-key>", Skip: true}}},
	}, { // Test 2d: a documented line that creates the file clears the gap.
		Markdown:  "```sh\ntouch data.txt\ntool run data.txt\n```\n",
		WantSteps: [][]planLine{{{Cmd: "touch data.txt"}, {Cmd: "tool run data.txt"}}},
	}, { // Test 2e: a copy's destination is what the line produces, so it is
		// not a missing input even though nothing created it first.
		Markdown: "```sh\nmkdir -p ~/.config/tool\ncp conf.md ~/.config/tool/conf.md\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "mkdir -p ~/.config/tool"},
			{Cmd: "cp conf.md ~/.config/tool/conf.md"},
		}},
		WantFixtures: []string{"conf.md"},
	}, { // Test 2f: `go get` of the repository's own module documents library
		// use in the reader's project, so running it here would add the module
		// to itself. That is the session's wrong directory, not a doc bug.
		Markdown:  "```sh\ngo get example.com/tool/sanitize\n```\n",
		Module:    "example.com/tool",
		WantSteps: [][]planLine{{{Cmd: "go get example.com/tool/sanitize", Skip: true}}},
	}, { // Test 2g: a line naming a remote-tracking revision needs history the
		// session's copy of the repository does not have.
		Markdown:  "```sh\ntool --base origin/main~4\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool --base origin/main~4", Skip: true}}},
	}, { // Test 2h: a path under the reader's home is theirs, not the
		// document's to create, and an interactive flag cannot be answered.
		// Bare -i is left alone: it means in-place far more often than it
		// means interactive, and skipping it breaks the lines that follow.
		Markdown: "```sh\ntool ingest git ~/src/project\ntool ask --interactive\ntool fix -i notes.md\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "tool ingest git ~/src/project", Skip: true},
			{Cmd: "tool ask --interactive", Skip: true},
			{Cmd: "tool fix -i notes.md"},
		}},
		WantFixtures: []string{"notes.md"},
	}, { // Test 2i: an assignment whose value is a bracketed placeholder is
		// skipped. Bash would read the brackets as redirects and hang.
		Markdown:  "```sh\nSECRET_KEY=<from openssl rand -base64 32>\n```\n",
		WantSteps: [][]planLine{{{Cmd: "SECRET_KEY=<from openssl rand -base64 32>", Skip: true}}},
	}, { // Test 2j: a reference page prints a synopsis rather than a command a
		// reader copies whole, and the capitals and brackets say so.
		Markdown: "```sh\ntool related TOPIC [--limit N]\ntool serve\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "tool related TOPIC [--limit N]", Skip: true},
			{Cmd: "tool serve", Skip: true},
		}},
	}, { // Test 2k: a bracketed placeholder holds words, not just one token,
		// and running it starts whatever the reader was meant to substitute.
		// A shell test opens with a space, so it is not mistaken for one.
		Markdown: "```sh\ntool [your node app]\ntool run\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "tool [your node app]", Skip: true},
			{Cmd: "tool run"},
		}},
	}, { // Test 2l: sourcing another shell's completions cannot work in bash,
		// though generating them is fine.
		Markdown: "```sh\nsource <(tool --generate complete-zsh)\ntool run\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "source <(tool --generate complete-zsh)", Skip: true},
			{Cmd: "tool run"},
		}},
	}, { // Test 2m: a home path a line reads is the reader's, but one an
		// output flag names is written by the line and the container can hold it.
		Markdown: "```sh\ncat $HOME/.toolrc\ntool run --out $HOME/out.txt\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "cat $HOME/.toolrc", Skip: true},
			{Cmd: "tool run --out $HOME/out.txt"},
		}},
	}, { // Test 2n: the documented binary on its own is the smoke test again,
		// and for a watcher it never returns.
		Markdown:  "```sh\ntool\ntool run\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool", Skip: true}, {Cmd: "tool run"}}},
	}, { // Test 2o: a block that shows a command failing is teaching why the
		// corrected form follows, so the demonstrated failure is the document
		// working, whatever words the tool chose for the complaint.
		Markdown: "```\n$ tool run '\\n'\nthe literal is not allowed in a regex\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "tool run '\\n'", NonzeroOK: true},
		}},
	}, { // Test 2p: a notebook is never fabricated, the same as any other file
		// argument, so the session checks for it before the line.
		Markdown:  "```sh\ntool render notebook.ipynb\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool render notebook.ipynb", Need: "notebook.ipynb"}}},
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
	}, { // Test 7: structured missing files are checked at run time, localhost skips.
		Markdown: "```sh\ntool load data.csv\ntool ping --base-url http://localhost:9\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "tool load data.csv", Need: "data.csv"},
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
		Cfg: &config.ExamplesConfig{Steps: []config.StepRule{
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
		Cfg: &config.ExamplesConfig{
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
	}, { // Test 16: a variable no documented line sets is skipped, not failed.
		Markdown:  "```sh\ntool run < $HISTFILE\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool run < $HISTFILE", Skip: true}}},
	}, { // Test 17: a variable an earlier line exports resolves and runs.
		Markdown: "```sh\nexport TOKEN=abc\ntool run --key $TOKEN\n```\n",
		WantSteps: [][]planLine{{
			{Cmd: "export TOKEN=abc"},
			{Cmd: "tool run --key $TOKEN"},
		}},
	}, { // Test 18: a variable assigned as a prefix on the same line resolves.
		Markdown:  "```sh\nMODE=fast tool run --bind 'reload($MODE)'\n```\n",
		WantSteps: [][]planLine{{{Cmd: "MODE=fast tool run --bind 'reload($MODE)'"}}},
	}, { // Test 19: the container's own variables resolve.
		Markdown:  "```sh\ntool run --out $HOME/out\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool run --out $HOME/out"}}},
	}, { // Test 20: a variable the config exports resolves.
		Markdown:  "```sh\ntool run --key $TOKEN\n```\n",
		Cfg:       &config.ExamplesConfig{Env: map[string]string{"TOKEN": "abc"}},
		WantSteps: [][]planLine{{{Cmd: "tool run --key $TOKEN"}}},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := ""
			if test.Module != "" {
				dir = t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "go.mod"),
					[]byte("module "+test.Module+"\n"), 0o600); err != nil {
					t.Fatalf("write go.mod: %v", err)
				}
			}
			plan := BuildPlan("repo", dir, test.Markdown, []string{"tool"},
				[]PlanInstall{{Cmd: "go install example.com/tool@latest", Ecosystem: "go"}}, test.Cfg, testRecognizer())
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
	plan := BuildPlan("repo", dir, md, []string{"tool"}, nil, nil, testRecognizer())
	want := [][]planLine{{
		{Cmd: "tool index --file examples/people.csv"},
		{Cmd: "tool index --file missing.csv", Need: "missing.csv"},
	}}
	if diff := cmp.Diff(want, projectPlan(plan)); diff != "" {
		t.Errorf("steps mismatch (-want +got):\n%s", diff)
	}
}

// TestBuildPlanRootGoFiles checks that a plan records the Go files at the top
// of the checkout and nothing below it, and that merging documents keeps them
// once.
func TestBuildPlanRootGoFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Files     []string
		Dirs      []string
		Links     map[string]string
		NoDir     bool
		WantFiles []string
	}{{ // Test 0: Root Go files are kept in name order, other files and nested ones are not.
		Files:     []string{"zlib.go", "doc.go", "README.md", "cmd/tool/main.go", "go.mod"},
		WantFiles: []string{"doc.go", "zlib.go"},
	}, { // Test 1: A directory named like a Go file is not one.
		Files:     []string{"main.go"},
		Dirs:      []string{"gen.go"},
		WantFiles: []string{"main.go"},
	}, { // Test 2: A checkout with no Go file at its top records none.
		Files: []string{"README.md", "internal/x/x.go"},
	}, { // Test 3: A plan built without a checkout records none.
		NoDir: true,
	}, { // Test 4: A link to a file is a root Go file, a link to a directory or nothing is not.
		Files: []string{"internal/gen/real.go", "README.md"},
		Links: map[string]string{
			"api.go": "internal/gen/real.go", "dir.go": "internal", "gone.go": "nope",
		},
		WantFiles: []string{"api.go"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := ""
			if !test.NoDir {
				dir = t.TempDir()
			}
			for _, d := range test.Dirs {
				if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range test.Files {
				full := filepath.Join(dir, f)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte("package x\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for name, target := range test.Links {
				if err := os.Symlink(filepath.Join(dir, target), filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			md := "```sh\ntool run\n```\n"
			plan := BuildPlan("repo", dir, md, []string{"tool"}, nil, nil, testRecognizer())
			if diff := cmp.Diff(test.WantFiles, plan.RootGoFiles, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("root Go files mismatch (-want +got):\n%s", diff)
			}
			merged := &Plan{}
			merged.Merge("README.md", plan)
			merged.Merge("docs/guide.md", BuildPlan("repo", dir, md, []string{"tool"}, nil, nil,
				testRecognizer()))
			if diff := cmp.Diff(test.WantFiles, merged.RootGoFiles, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("merged root Go files mismatch (-want +got):\n%s", diff)
			}
		})
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
			if diff := cmp.Diff(test.Want, docblock.LogicalLines(test.In)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSkipHeuristics checks the skip classes that keep kibble from blaming
// correct docs for what a clean container lacks.
func TestSkipHeuristics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Markdown string
		WantSkip string
		WantNeed string
		WantRun  bool
		WantNZ   bool
	}{{ // Test 0: a brace placeholder is the reader's to fill in.
		Markdown: "```sh\ntool {source_file_or_directory}\n```\n",
		WantSkip: "placeholder",
	}, { // Test 1: a shell variable expansion is not a brace placeholder.
		Markdown: "```sh\nexport V=x\ntool run ${V}\n```\n",
		WantRun:  true,
	}, { // Test 2: git needing a remote skips in the fresh session repo.
		Markdown: "```sh\ngit fetch origin\n```\n",
		WantSkip: "one kibble made",
	}, { // Test 3: git config is fine in a fresh repo.
		Markdown: "```sh\ngit config user.name kibble\n```\n",
		WantRun:  true,
	}, { // Test 4: cd into a system directory only the reader has.
		Markdown: "```sh\ncd /usr/ports/textproc/tool\nmake install\n```\n",
		WantSkip: "only the reader's system",
	}, { // Test 5: a bare placeholder word as a positional argument.
		Markdown: "```sh\ntool pattern path -x echo\n```\n",
		WantSkip: "placeholder",
	}, { // Test 6: a check subcommand may exit nonzero by design.
		Markdown: "```sh\ntool check\n```\n",
		WantRun:  true, WantNZ: true,
	}, { // Test 7: an @-prefixed missing file is still a missing file.
		Markdown: "```sh\ntool check @args.json\n```\n",
		WantRun:  true, WantNeed: "args.json",
	}, { // Test 8: the Go all-packages pattern is not an ellipsis placeholder.
		Markdown: "```sh\ntool ./...\n```\n",
		WantRun:  true,
	}, { // Test 9: an all-caps stand-in ending in a noun is a placeholder.
		Markdown: "```sh\ntool send SOMEFILE\n```\n",
		WantSkip: "placeholder",
	}, { // Test 10: an output flag creates its file, so it is not missing.
		Markdown: "```sh\ntool scan -out results.json ./...\n```\n",
		WantRun:  true,
	}, { // Test 11: a foreign shell requested by flag skips.
		Markdown: "```sh\ntool --shell zsh 'echo hi'\n```\n",
		WantSkip: "zsh shell",
	}, { // Test 12: a home config no line is seen to create is checked at run time.
		Markdown: "```sh\ntool --runOptions ~/.config/tool/config.toml\n```\n",
		WantRun:  true, WantNeed: "~/.config/tool/config.toml",
	}, { // Test 13: a rule spec inside a flag is not a missing file glob.
		Markdown: "```sh\ntool --exclude-rules=\"cmd/.*:G204\" ./...\n```\n",
		WantRun:  true,
	}, { // Test 14: a conventional fake home path is a placeholder.
		Markdown: "```sh\ntool grep TODO /home/user\n```\n",
		WantSkip: "placeholder",
	}, { // Test 15: a truncated value in quotes is a placeholder.
		Markdown: "```sh\ntool --token 'abc-v1....'\n```\n",
		WantSkip: "placeholder",
	}, { // Test 16: the Go pattern is still not a placeholder after the change.
		Markdown: "```sh\ntool run ./...\n```\n",
		WantRun:  true,
	}, { // Test 17: an awk field reference is not a shell expansion.
		Markdown: "```sh\ntool list | awk '{print $NF}'\n```\n",
		WantRun:  true,
	}, { // Test 18: a variable named inside single quotes is literal text.
		Markdown: "```sh\ntool grep '$MISSING'\n```\n",
		WantRun:  true,
	}, { // Test 19: an apostrophe in double quotes does not open a quoted span.
		Markdown: "```sh\ntool say \"don't\" $MISSING\n```\n",
		WantSkip: "which the docs never set",
	}, { // Test 20: an unquoted expansion the docs never set still skips.
		Markdown: "```sh\ntool run $MISSING\n```\n",
		WantSkip: "which the docs never set",
	}, { // Test 21: a serve subcommand is still an interactive skip.
		Markdown: "```sh\ntool serve\n```\n",
		WantSkip: "interactive or long-running",
	}, { // Test 22: a web subcommand is no longer treated as interactive, since
		// it collides with plenty of one-shot commands.
		Markdown: "```sh\ntool web status\n```\n",
		WantRun:  true,
	}, { // Test 23: an agent subcommand runs rather than being skipped by name.
		Markdown: "```sh\ntool agent list\n```\n",
		WantRun:  true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := BuildPlan("repo", "", test.Markdown, []string{"tool"},
				[]PlanInstall{{Cmd: "go install example.com/tool@latest", Ecosystem: "go"}}, nil, testRecognizer())
			var last PlanLine
			found := false
			for _, s := range plan.Steps {
				for _, l := range s.Lines {
					if strings.HasPrefix(docblock.Flatten(l.Cmd), "tool") || strings.HasPrefix(docblock.Flatten(l.Cmd), "git") || strings.HasPrefix(docblock.Flatten(l.Cmd), "cd /") {
						last, found = l, true
					}
				}
			}
			if !found {
				t.Fatalf("no matching line in plan: %+v", plan.Steps)
			}
			if test.WantRun && last.Skip != "" {
				t.Errorf("line skipped: %q", last.Skip)
			}
			if test.WantSkip != "" && !strings.Contains(last.Skip, test.WantSkip) {
				t.Errorf("skip %q does not contain %q", last.Skip, test.WantSkip)
			}
			if test.WantNZ && !last.NonzeroOK {
				t.Error("expected NonzeroOK")
			}
			if diff := cmp.Diff(test.WantNeed, last.Need); diff != "" {
				t.Errorf("need mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestFindingFlagNonzeroOK checks that a check-mode flag marks only its own line, while a
// documented note spreads to every line running the same command.
func TestFindingFlagNonzeroOK(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Markdown string
		Binary   string
		Want     []bool
	}{{ // Test 0: a check-mode flag on a documented binary is a documented exit.
		Markdown: "```sh\npinact run --fix=false --no-api\n```\n", Binary: "pinact",
		Want: []bool{true},
	}, { // Test 1: the same binary without the flag is not.
		Markdown: "```sh\npinact run\n```\n", Binary: "pinact", Want: []bool{false},
	}, { // Test 2: a bare check flag on a documented binary counts.
		Markdown: "```sh\ntool --check\n```\n", Binary: "tool", Want: []bool{true},
	}, { // Test 3: an undocumented binary does not count, and the planner drops the line.
		Markdown: "```sh\nother --check\n```\n", Binary: "tool", Want: nil,
	}, { // Test 4: a note spreads to a plain line of the same command.
		Markdown: "```sh\n# exits non-zero when it finds any\ntool run\n```\n" +
			"text\n```sh\ntool run\n```\n",
		Binary: "tool", Want: []bool{true, true},
	}, { // Test 5: a flag marks its own line and does not spread.
		Markdown: "```sh\ntool run --check\n```\ntext\n```sh\ntool run\n```\n",
		Binary:   "tool", Want: []bool{true, false},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := BuildPlan("repo", "", test.Markdown, []string{test.Binary}, nil, nil,
				testRecognizer())
			var got []bool
			for _, l := range projectPlan(plan) {
				for _, pl := range l {
					got = append(got, pl.NonzeroOK)
				}
			}
			if diff := cmp.Diff(test.Want, got); diff != "" {
				t.Errorf("NonzeroOK mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestWatcherInvocation checks that a tool described as a watcher skips only
// the invocations that actually reach its watching or serving mode, so a
// one-shot subcommand of the same tool still runs.
func TestWatcherInvocation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Sub  string
		Flat string
		Want bool
	}{{ // Test 0: a one-shot subcommand runs.
		Sub: "build", Flat: "tool build", Want: false,
	}, { // Test 1: a serve subcommand does not return.
		Sub: "serve", Flat: "tool serve", Want: true,
	}, { // Test 2: an explicit watch flag does not return.
		Sub: "run", Flat: "tool run --watch", Want: true,
	}, { // Test 3: serving via a port flag does not return.
		Sub: "run", Flat: "tool run --port 8080", Want: true,
	}, { // Test 4: a one-shot build with a flag still runs.
		Sub: "build", Flat: "tool build --output dist", Want: false,
	}, { // Test 5: an info query about a serve subcommand returns, so it runs.
		Sub: "serve", Flat: "tool serve --help", Want: false,
	}, { // Test 6: a bare version query is never a watcher.
		Sub: "", Flat: "tool --version", Want: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := watcherInvocation(test.Sub, test.Flat); got != test.Want {
				t.Errorf("watcherInvocation(%q, %q) = %v, want %v", test.Sub, test.Flat, got, test.Want)
			}
		})
	}
}

// TestDescribedAsWatcher checks that a document introducing a tool as one that
// keeps running is recognized, and that watching mentioned away from the tool's
// name is not, since a wrong call here costs a check that would have run.
func TestDescribedAsWatcher(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Markdown string
		Bin      string
		Want     bool
	}{{ // Test 0: nodemon's opening sentence says what it does.
		Markdown: "# nodemon\n\nnodemon is a tool that helps develop Node.js based " +
			"applications by automatically restarting the node application when file " +
			"changes in the directory are detected.\n",
		Bin: "nodemon", Want: true,
	}, { // Test 1: a server is the same shape.
		Markdown: "# hugo\n\nhugo builds your site and can serve it locally.\n",
		Bin:      "hugo", Want: true,
	}, { // Test 2: a search tool is not, though its docs mention files.
		Markdown: "# ripgrep\n\nripgrep recursively searches directories for a regex " +
			"pattern while respecting your gitignore.\n",
		Bin: "rg", Want: false,
	}, { // Test 3: watching mentioned far from the tool's name does not count.
		Markdown: "# tool\n\ntool converts files.\n\n" + strings.Repeat("prose. ", 40) +
			"You can watch the output directory yourself.\n",
		Bin: "tool", Want: false,
	}, { // Test 4: no binary, nothing to judge.
		Markdown: "# tool\n\nit watches things.\n", Bin: "", Want: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := describedAsWatcher(test.Markdown, test.Bin); got != test.Want {
				t.Errorf("describedAsWatcher(%q) = %v, want %v", test.Bin, got, test.Want)
			}
		})
	}
}

// TestPlatformScopedBlocks checks that a block whose introducing sentence
// scopes it to another operating system is skipped rather than run and
// convicted. The fixture prose is ripgrep's FAQ shape, which documents the
// GNU form and then the BSD form, and kibble once ran the BSD form on Linux
// and called the document broken.
func TestPlatformScopedBlocks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Markdown string
		WantSkip string
	}{{ // Test 0: a block introduced as BSD sed for macOS is skipped.
		Markdown: "Note: the above assumes GNU sed. If you are using BSD sed " +
			"(the default on macOS and FreeBSD) then you must modify the " +
			"command to be the following:\n\n```sh\ntool run --fix ''\n```\n",
		WantSkip: "documented for",
	}, { // Test 1: a block introduced for Windows is skipped.
		Markdown: "On Windows, run the following:\n\n```sh\ntool run --fix\n```\n",
		WantSkip: "documented for",
	}, { // Test 2: naming Linux in the same sentence is a contrast, not a
		// scope, and the block runs.
		Markdown: "This works the same on Linux and macOS:\n\n```sh\ntool run --fix\n```\n",
	}, { // Test 3: a block with no introduction runs.
		Markdown: "```sh\ntool run --fix\n```\n",
	}, { // Test 4: a heading between the prose and the block resets the
		// introduction, so a macOS sentence in the previous section cannot
		// scope this one.
		Markdown: "This section is about macOS.\n\n## Elsewhere\n\n```sh\ntool run --fix\n```\n",
	}, { // Test 5: an earlier sentence naming macOS does not scope the block
		// when the final sentence moves on.
		Markdown: "Users on macOS often ask this. The answer is the same " +
			"everywhere, run the following:\n\n```sh\ntool run --fix\n```\n",
	}, { // Test 6: a block under a bare macOS heading is skipped.
		Markdown: "### macOS\n\n```sh\ntool run --fix\n```\n",
		WantSkip: "documented for",
	}, { // Test 7: a bare Windows heading scopes a block whose own
		// introduction names no platform.
		Markdown: "## Windows\n\nRun the following:\n\n```sh\ntool run --fix\n```\n",
		WantSkip: "documented for",
	}, { // Test 8: a heading that only mentions macOS does not scope the block.
		Markdown: "## Building on macOS\n\n```sh\ntool run --fix\n```\n",
	}, { // Test 9: a bare Linux heading runs.
		Markdown: "## Linux\n\n```sh\ntool run --fix\n```\n",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := BuildPlan("repo", "", test.Markdown, []string{"tool"},
				[]PlanInstall{{Cmd: "go install example.com/tool@latest", Ecosystem: "go"}}, nil, testRecognizer())
			var got PlanLine
			found := false
			for _, s := range plan.Steps {
				for _, l := range s.Lines {
					if strings.HasPrefix(docblock.Flatten(l.Cmd), "tool") {
						got, found = l, true
					}
				}
			}
			if !found {
				t.Fatal("plan lost the block's line")
			}
			if test.WantSkip == "" && got.Skip != "" {
				t.Errorf("line skipped %q, want it to run", got.Skip)
			}
			if test.WantSkip != "" && !strings.Contains(got.Skip, test.WantSkip) {
				t.Errorf("skip = %q, want it to contain %q", got.Skip, test.WantSkip)
			}
		})
	}
}

// testRecognizer mirrors what the command line supplies in production: the
// planner is told which documented lines install, rather than knowing.
func testRecognizer() *Recognizer {
	clone := regexp.MustCompile(`\bgit\s+clone\b`)
	goInstall := regexp.MustCompile(`\bgo\s+install\s+(?:-\S+\s+)*(\S+@\S+)`)
	brew := regexp.MustCompile(`\bbrew\s+install\s+((?:-[\w-]+\s+)*)(\S+)`)
	return &Recognizer{
		Clones:   func(flat string) bool { return clone.MatchString(flat) },
		Installs: func(flat string) bool { return goInstall.MatchString(flat) || brew.MatchString(flat) },
	}
}

// newReaderNameTestPlanner returns a planner that treats the given binaries as
// documented and holds the given repo files.
func newReaderNameTestPlanner(tree map[string]bool, bins ...string) *planner {
	binaries := map[string]bool{}
	for _, b := range bins {
		binaries[b] = true
	}
	return &planner{
		plan:     &Plan{},
		binaries: binaries,
		tree:     tree,
		created:  map[string]bool{},
		fixed:    map[string]bool{},
	}
}

// TestMissingRedirectIn checks that a < redirect naming a file nothing
// provides is reported, and that provided files and writes are not.
func TestMissingRedirectIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Line       string
		WantResult string
	}{{ // Test 0: the input of a redirect is the missing file.
		Line: `repren --patterns=patfile < input > output`, WantResult: "input",
	}, { // Test 1: a file in the tree is present.
		Line: `tool < have.txt`, WantResult: "",
	}, { // Test 2: a heredoc is not a file redirect.
		Line: `tool <<EOF`, WantResult: "",
	}, { // Test 3: a device is not a missing file.
		Line: `tool < /dev/null`, WantResult: "",
	}, { // Test 4: a line with no input redirect reports nothing.
		Line: `tool a > b`, WantResult: "",
	}, { // Test 5: a less-than sign inside double quotes is not a redirect.
		Line: `jq ".a < 3" have.txt`, WantResult: "",
	}, { // Test 6: a process substitution is not a file redirect.
		Line: `diff <(tool a) <(tool b)`, WantResult: "",
	}, { // Test 7: a redirect written without a space is still read.
		Line: `sort <input >out`, WantResult: "input",
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			pl := newReaderNameTestPlanner(map[string]bool{"have.txt": true})
			if diff := cmp.Diff(test.WantResult, pl.missingRedirectIn(test.Line)); diff != "" {
				t.Errorf("missingRedirectIn mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestBareWordPlaceholderFiles checks that the word files is a stand-in.
func TestBareWordPlaceholderFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Line       string
		WantResult string
	}{{ // Test 0: files is a conventional placeholder.
		Line: `refurb files`, WantResult: "files",
	}, { // Test 1: a real subcommand is not.
		Line: `refurb check`, WantResult: "",
	}, { // Test 2: a DNS record type is a literal value.
		Line: `wait4x dns AAAA example.com`, WantResult: "",
	}, { // Test 3: an all-caps topic is still a stand-in.
		Line: `tool TOPIC`, WantResult: "TOPIC",
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.WantResult, bareWordPlaceholder(test.Line)); diff != "" {
				t.Errorf("bareWordPlaceholder mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSkipReasonReaderNames checks the skip text, verdict reason, and gap flag
// each reader-name rule produces through skipReason.
func TestSkipReasonReaderNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Line       string
		WantSkip   string
		WantNeed   string
		WantReason verdict.Reason
		WantGap    bool
	}{{ // Test 0: files is a placeholder word.
		Line:       `gita files`,
		WantSkip:   `docs use "files" as a placeholder the reader must fill in`,
		WantReason: verdict.ReasonPlaceholder,
	}, { // Test 1: a missing redirect input is checked at run time.
		Line:     `gita run < input`,
		WantNeed: "input",
	}, { // Test 2: a stand-in repository owner is a placeholder.
		Line:       `gita install --git https://github.com/example/my-plugins`,
		WantSkip:   "docs use a placeholder the reader must fill in",
		WantReason: verdict.ReasonPlaceholder,
	}, { // Test 3: a checksum file is checked at run time rather than fabricated.
		Line:     `gita verify aqua_checksums.txt`,
		WantNeed: "aqua_checksums.txt",
	}, { // Test 4: a numbered operand is a placeholder.
		Line:       `gita group add repo1 repo2`,
		WantSkip:   `docs use "repo1" as a placeholder the reader must fill in`,
		WantReason: verdict.ReasonPlaceholder,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			pl := newReaderNameTestPlanner(map[string]bool{}, "gita")
			skip, reason, gap := pl.skipReason(test.Line, docblock.Flatten(test.Line))
			if skip != test.WantSkip || reason != test.WantReason || gap != test.WantGap {
				t.Errorf("skipReason = (%q, %q, %v), want (%q, %q, %v)",
					skip, reason, gap, test.WantSkip, test.WantReason, test.WantGap)
			}
			if pl.need != test.WantNeed {
				t.Errorf("need = %q, want %q", pl.need, test.WantNeed)
			}
		})
	}
}

// TestSkipReasonReservedHost checks that a reserved example host in host
// position skips as a placeholder and that ordinary hosts and files run.
func TestSkipReasonReservedHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Line     string
		WantSkip bool
	}{{ // Test 0: a bare URL on a reserved host.
		Line: `tool run https://api.example.com/openapi.json`, WantSkip: true,
	}, { // Test 1: a path placeholder and a reserved host together.
		Line:     `tool --config-file /path/to/config.toml run https://api.example.com/openapi.json`,
		WantSkip: true,
	}, { // Test 2: a reserved host ahead of a flag.
		Line: `uvx tool run https://api.example.com/openapi.json --phases=examples`, WantSkip: true,
	}, { // Test 3: a real host runs.
		Line: `tool https://api.github.com/repos/x/y`, WantSkip: false,
	}, { // Test 4: a quoted sentence is one token with the host mid-token, so it runs.
		Line: `tool commit -m "see example.com"`, WantSkip: false,
	}, { // Test 5: a userinfo at a reserved host.
		Line: `tool user@example.org`, WantSkip: true,
	}, { // Test 6: a flag value on a reserved test domain.
		Line: `tool --url http://foo.test:8080/x`, WantSkip: true,
	}, { // Test 7: a file name ending in test is not a host.
		Line: `tool mytest.txt`, WantSkip: false,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			pl := newReaderNameTestPlanner(map[string]bool{}, "tool")
			skip, reason, _ := pl.skipReason(test.Line, docblock.Flatten(test.Line))
			if got := skip != ""; got != test.WantSkip {
				t.Fatalf("skip = %q, want skip %v", skip, test.WantSkip)
			}
			if test.WantSkip && reason != verdict.ReasonPlaceholder {
				t.Errorf("reason = %q, want %q", reason, verdict.ReasonPlaceholder)
			}
		})
	}
}

// TestSkipReasonOutsideCheckout checks that a first operand under ../ skips and
// that other relative operands run.
func TestSkipReasonOutsideCheckout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Line     string
		WantSkip bool
	}{{ // Test 0: an editable install from a sibling directory.
		Line: `uv add --editable ../projects/bar/`, WantSkip: true,
	}, { // Test 1: cd to the parent is not a ../ operand.
		Line: `cd ..`, WantSkip: false,
	}, { // Test 2: a ./ path stays inside the checkout.
		Line: `cat ./x.md`, WantSkip: false,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			pl := newReaderNameTestPlanner(map[string]bool{}, "tool")
			skip, reason, _ := pl.skipReason(test.Line, docblock.Flatten(test.Line))
			if got := skip != ""; got != test.WantSkip {
				t.Fatalf("skip = %q, want skip %v", skip, test.WantSkip)
			}
			if test.WantSkip && reason != verdict.ReasonMissingFixture {
				t.Errorf("reason = %q, want %q", reason, verdict.ReasonMissingFixture)
			}
		})
	}
}

// TestSkipReasonReaderFile checks that a my-prefixed file name the tree lacks
// is given to the reader as a fixture, so the line runs rather than skips.
func TestSkipReasonReaderFile(t *testing.T) {
	t.Parallel()
	pl := newReaderNameTestPlanner(map[string]bool{}, "tool")
	line := `tool myfile.txt`
	if skip, _, _ := pl.skipReason(line, docblock.Flatten(line)); skip != "" {
		t.Errorf("skipReason = %q, want no skip", skip)
	}
	var fixtures []string
	for _, f := range pl.plan.Fixtures {
		fixtures = append(fixtures, f.Path)
	}
	if diff := cmp.Diff([]string{"myfile.txt"}, fixtures); diff != "" {
		t.Errorf("fixtures mismatch (-want +got):\n%s", diff)
	}
}

// TestReaderNamedOperand checks the operands the reader is expected to
// replace, and the ones that are real: example values such as foo, file
// names, output paths, flags, and names the repo provides.
func TestReaderNamedOperand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Tree       map[string]bool
		Line       string
		WantResult string
	}{{ // Test 0: a numbered noun is a stand-in.
		Line: `gita group add repo1 repo2 -n my-group`, WantResult: "repo1",
	}, { // Test 1: a my-prefixed name with a trailing slash is a stand-in.
		Line: `tach report my_package/`, WantResult: "my_package",
	}, { // Test 2: a my-prefixed directory after a boolean flag is a stand-in.
		Line: `repren --patterns=patfile --full mydir1`, WantResult: "mydir1",
	}, { // Test 3: a value written after a space is read as an operand.
		Line: `tool --name my-app`, WantResult: "my-app",
	}, { // Test 4: foo is a real example value.
		Line: `rg foo`, WantResult: "",
	}, { // Test 5: a comma list of example values has no stand-in.
		Line: `aqua rm foo,suzuki-shunsuke/foo`, WantResult: "",
	}, { // Test 6: a my-prefixed file name is left to the file rule.
		Line: `repren --from foo --to bar myfile.txt`, WantResult: "",
	}, { // Test 7: a style file is left to the file rule.
		Line: `glow -s mystyle.json`, WantResult: "",
	}, { // Test 8: an output path is one the line writes.
		Line: `tool -o my-out/`, WantResult: "",
	}, { // Test 9: a name in the tree is real.
		Tree: map[string]bool{"my-app": true}, Line: `tool my-app`, WantResult: "",
	}, { // Test 10: an ordinary word is not a stand-in.
		Line: `tool build`, WantResult: "",
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			pl := newReaderNameTestPlanner(test.Tree)
			if diff := cmp.Diff(test.WantResult, pl.readerNamedOperand(test.Line)); diff != "" {
				t.Errorf("readerNamedOperand mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestBuildPlanProvidedTools checks that a tool an example installs is a known
// command for the lines after it, as uv's README installs ruff and then runs
// it, and that a tool installed by a block that does not run stays unknown.
func TestBuildPlanProvidedTools(t *testing.T) {
	t.Parallel()
	toolInstall := regexp.MustCompile(`^uv tool install (\S+)$`)
	rec := testRecognizer()
	rec.Provides = func(flat string) string {
		if m := toolInstall.FindStringSubmatch(flat); m != nil {
			return m[1]
		}
		return ""
	}
	tests := []struct {
		Markdown string
		Rec      *Recognizer
		WantCmds []string
		WantExcl int
	}{{ // Test 0: ruff installed by an earlier line qualifies.
		Markdown: "```console\n$ uv tool install ruff\n$ ruff --version\n```\n",
		Rec:      rec, WantCmds: []string{"uv tool install ruff", "ruff --version"},
	}, { // Test 1: ruff installed by an earlier block qualifies.
		Markdown: "```sh\nuv tool install ruff\n```\n\n```sh\nruff check\n```\n",
		Rec:      rec, WantCmds: []string{"uv tool install ruff", "ruff check"},
	}, { // Test 2: without the recognizer's answer, the call is an unknown command.
		Markdown: "```console\n$ uv tool install ruff\n$ ruff --version\n```\n",
		Rec:      testRecognizer(), WantExcl: 1,
	}, { // Test 3: an install in a block that does not qualify provides nothing.
		Markdown: "```sh\nuv tool install ruff\nprose here\n```\n\n```sh\nruff check\n```\n",
		Rec:      rec, WantExcl: 2,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := BuildPlan("uv", t.TempDir(), test.Markdown, []string{"uv"}, nil, nil, test.Rec)
			var cmds []string
			for _, st := range plan.Steps {
				for _, l := range st.Lines {
					cmds = append(cmds, l.Cmd)
				}
			}
			if diff := cmp.Diff(test.WantCmds, cmds, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("planned lines mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantExcl, plan.Excluded); diff != "" {
				t.Errorf("excluded mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestQualifies checks that the program a line runs, not a leading
// assignment, decides whether the line qualifies.
func TestQualifies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Line       string
		Binaries   []string
		WantResult bool
	}{{ // Test 0: an undocumented program behind an assignment does not qualify.
		Line: "GOOS=linux goreleaser build", WantResult: false,
	}, { // Test 1: a bare assignment runs no program and does not qualify.
		Line: "FOO=bar", WantResult: false,
	}, { // Test 2: an assignment before a known command qualifies.
		Line: "FOO=bar make build", WantResult: true,
	}, { // Test 3: an assignment before a documented binary qualifies.
		Line: "FOO=bar mytool run", Binaries: []string{"mytool"}, WantResult: true,
	}, { // Test 4: an exported assignment alone qualifies.
		Line: "export FOO=bar", WantResult: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			pl := newReaderNameTestPlanner(nil, test.Binaries...)
			pl.provided = map[string]bool{}
			pl.packages = map[string]bool{}
			pl.rec = &Recognizer{}
			got := pl.qualifies([]string{test.Line})
			if diff := cmp.Diff(test.WantResult, got); diff != "" {
				t.Errorf("qualifies mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestPlanNeed checks that a file the planner sees no line create no longer
// skips the line. The line runs and carries the file, so the session can say
// whether it exists. uv's marimo guide creates its notebook in a block the plan
// leaves out, which the line carries too, since that block never runs.
func TestPlanNeed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Markdown      string
		Cfg           *config.ExamplesConfig
		WantNeed      string
		WantUnplanned string
		WantSkip      string
	}{{ // Test 0: uv's marimo guide, where the notebook comes from an unplanned block.
		Markdown: "```console\n$ uvx marimo edit my_notebook.py\n```\n\n" +
			"```console\n$ uv add --script my_notebook.py numpy\n```\n",
		WantNeed: "my_notebook.py", WantUnplanned: "the code block at line 2",
	}, { // Test 1: a bare output fence is named by its location, not its first output line.
		Markdown: "## Replay\n\n```\nReplaying 2 cases from .schemathesis/default/cache/crashes\n" +
			"Case 1 failed\nCase 2 failed\n```\n\n" +
			"```bash\nuv run schemathesis replay --auth-wfc ./auth.yaml --auth-wfc-user admin\n```\n",
		WantNeed: "./auth.yaml", WantUnplanned: `the code block at line 4 under "Replay"`,
	}, { // Test 2: a file with no earlier line at all still runs and carries the file.
		Markdown: "```sh\nuv render notebook.ipynb\n```\n",
		WantNeed: "notebook.ipynb",
	}, { // Test 3: a background step cannot report the check, so it keeps the plan-time gap.
		Markdown: "```sh\nuv load notebook.ipynb\n```\n",
		Cfg:      &config.ExamplesConfig{Steps: []config.StepRule{{Match: "uv load", Background: true}}},
		WantSkip: "references notebook.ipynb, which the docs never create",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := BuildPlan("repo", "", test.Markdown, []string{"uv"}, nil, test.Cfg, testRecognizer())
			if len(plan.Steps) == 0 {
				t.Fatal("no steps planned")
			}
			steps := plan.Steps
			l := steps[len(steps)-1].Lines[len(steps[len(steps)-1].Lines)-1]
			if diff := cmp.Diff(test.WantNeed, l.Need); diff != "" {
				t.Errorf("need mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantUnplanned, l.Unplanned); diff != "" {
				t.Errorf("unplanned mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantSkip, l.Skip); diff != "" {
				t.Errorf("skip mismatch (-want +got):\n%s", diff)
			}
			if test.WantSkip != "" && !l.Gap {
				t.Error("background fallback lost its gap flag")
			}
		})
	}
}

// TestPlanFile checks that a fenced block titled with a file path is planned as
// the document writing that file, so a later line reading it needs nothing, and
// that a title the checkout already holds or a caption is not written.
func TestPlanFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Markdown  string
		Tree      []string
		WantLines []string
	}{{ // Test 0: uv's coiled guide writes process.py, then runs it.
		Markdown: "```python title=\"process.py\"\nimport coiled\nprint(1)\n```\n\n" +
			"```console\n$ uv run process.py\n```\n",
		WantLines: []string{
			"cat > 'process.py' <<'KIBBLE_FILE_END'\nimport coiled\nprint(1)\nKIBBLE_FILE_END" +
				" file=process.py need= line=2",
			"uv run process.py file= need= line=7",
		},
	}, { // Test 1: uv's bazel guide writes a script in a directory and never runs its lines.
		Markdown: "```bash title=\"bazel/uv-auth-helper\"\n#!/usr/bin/env bash\nexec uv auth helper\n```\n\n" +
			"```console\n$ chmod +x bazel/uv-auth-helper\n```\n",
		WantLines: []string{
			"mkdir -p 'bazel' && cat > 'bazel/uv-auth-helper' <<'KIBBLE_FILE_END'\n" +
				"#!/usr/bin/env bash\nexec uv auth helper\nKIBBLE_FILE_END file=bazel/uv-auth-helper need= line=2",
			"chmod +x bazel/uv-auth-helper file= need= line=7",
		},
	}, { // Test 2: a title the checkout already holds is never written.
		Markdown:  "```toml title=\"pyproject.toml\"\n[project]\nname = \"x\"\n```\n",
		Tree:      []string{"pyproject.toml"},
		WantLines: nil,
	}, { // Test 3: a caption title is not a file, so the block is planned as before.
		Markdown:  "```bash title=\"Terminal\"\nuv sync\n```\n",
		WantLines: []string{"uv sync file= need= line=2"},
	}, { // Test 4: a content line equal to the terminator gets a suffixed terminator.
		Markdown: "```text title=\"notes.txt\"\nKIBBLE_FILE_END\n```\n",
		WantLines: []string{
			"cat > 'notes.txt' <<'KIBBLE_FILE_END_1'\nKIBBLE_FILE_END\nKIBBLE_FILE_END_1" +
				" file=notes.txt need= line=2",
		},
	}, { // Test 5: a title listing several names, or a path into .git, is not a file.
		Markdown: "```js title=\"prettier.config.mjs, .prettierrc.mjs\"\nexport default {}\n```\n\n" +
			"```sh title=\".git/hooks/pre-commit\"\nuv sync\n```\n",
		WantLines: []string{"uv sync file= need= line=6"},
	}, { // Test 6: a shell block captioned by a comment naming a file writes it.
		Markdown: "```sh\n# scripts/run.sh\n\necho a\necho b\n```\n",
		WantLines: []string{
			"mkdir -p 'scripts' && cat > 'scripts/run.sh' <<'KIBBLE_FILE_END'\necho a\necho b\nKIBBLE_FILE_END" +
				" file=scripts/run.sh need= line=2",
		},
	}, { // Test 7: a comment naming a platform is not a file, so the block runs.
		Markdown:  "```sh\n# Linux\nuv sync\n```\n",
		WantLines: []string{"uv sync file= need= line=3"},
	}, { // Test 8: a comment naming a placeholder path is not a file either.
		Markdown:  "```sh\n# path/to/run.sh\nuv sync\n```\n",
		WantLines: []string{"uv sync file= need= line=3"},
	}, { // Test 9: flask's quickstart names the file in the sentence above it.
		Markdown: "Create a file named `app.py`:\n\n```python\nfrom flask import Flask\n```\n\n" +
			"```sh\nuv run app.py\n```\n",
		WantLines: []string{
			"cat > 'app.py' <<'KIBBLE_FILE_END'\nfrom flask import Flask\nKIBBLE_FILE_END file=app.py need= line=4",
			"uv run app.py file= need= line=8",
		},
	}, { // Test 10: two named files leave the block unwritten.
		Markdown:  "Copy `a.py` into `b.py`:\n\n```python\nx = 1\n```\n",
		WantLines: nil,
	}, { // Test 11: a file the fence language cannot hold is not this block.
		Markdown:  "Edit `config.yaml` and add:\n\n```python\nx = 1\n```\n",
		WantLines: nil,
	}, { // Test 12: a sentence that only mentions the file does not ask for it.
		Markdown:  "The `app.py` module looks like this:\n\n```python\nx = 1\n```\n",
		WantLines: nil,
	}, { // Test 13: a paragraph separated from the fence by another block is not its lead.
		Markdown:  "Create `app.py`:\n\n```sh\nuv sync\n```\n\n```python\nx = 1\n```\n",
		WantLines: []string{"uv sync file= need= line=4"},
	}, { // Test 14: flask names the file in a comment on the block's first line.
		Markdown: "```python\n# save this as app.py\nfrom flask import Flask\n```\n",
		WantLines: []string{
			"cat > 'app.py' <<'KIBBLE_FILE_END'\n# save this as app.py\nfrom flask import Flask\nKIBBLE_FILE_END" +
				" file=app.py need= line=2",
		},
	}, { // Test 15: a first-line comment that only mentions a file asks for nothing.
		Markdown:  "```python\n# see app.py for the rest\nx = 1\n```\n",
		WantLines: nil,
	}, { // Test 16: a file comment the checkout already holds writes nothing.
		Markdown:  "```sh\n# run.sh\nuv sync\n```\n",
		Tree:      []string{"run.sh"},
		WantLines: nil,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, f := range test.Tree {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			plan := BuildPlan("repo", dir, test.Markdown, []string{"uv"}, nil, nil, testRecognizer())
			var got []string
			for _, s := range plan.Steps {
				for _, l := range s.Lines {
					got = append(got, fmt.Sprintf("%s file=%s need=%s line=%d", l.Cmd, l.File, l.Need, l.Line))
				}
			}
			if diff := cmp.Diff(test.WantLines, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("lines mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestPlanOutsideCheckoutAfterCd checks that a ../ operand is called the
// reader's only before the document changes directory. dotenvx's README runs
// `cd apps/app1` and then reads ../../.env.keys, the reader's monorepo root.
func TestPlanOutsideCheckoutAfterCd(t *testing.T) {
	t.Parallel()
	const outside = "lies outside the repository checkout"
	tests := []struct {
		Markdown    string
		WantOutside bool
	}{{ // Test 0: no cd, so ../ leaves the checkout.
		Markdown:    "```sh\n$ dotenvx get -fk ../../.env.keys -f .env\n```\n",
		WantOutside: true,
	}, { // Test 1: after a cd, ../ is relative to where the document went.
		Markdown: "```sh\n$ cd apps/app1\n$ dotenvx get -fk ../../.env.keys -f .env\n```\n",
	}, { // Test 2: a cd in an earlier block of the same document counts.
		Markdown: "```sh\n$ mkdir -p apps/app1 && cd apps/app1\n```\n\n" +
			"```sh\n$ dotenvx get -fk ../../.env.keys -f .env\n```\n",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := BuildPlan("dotenvx", "", test.Markdown, []string{"dotenvx"}, nil, nil, testRecognizer())
			if len(plan.Steps) == 0 {
				t.Fatal("no steps planned")
			}
			steps := plan.Steps
			l := steps[len(steps)-1].Lines[len(steps[len(steps)-1].Lines)-1]
			if got := strings.Contains(l.Skip, outside); got != test.WantOutside {
				t.Errorf("outside skip = %v, want %v (skip %q)", got, test.WantOutside, l.Skip)
			}
		})
	}
}

// TestPlanCreates checks that a running line records the paths it writes, so
// a later line missing one of them after this line failed can name it, and
// that uv's --output-file target is created rather than fabricated.
func TestPlanCreates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Markdown      string
		WantCreates   []string
		WantSynthetic []string
	}{{ // Test 0: A redirect target.
		Markdown:    "```sh\n$ tool list > out.txt\n```\n",
		WantCreates: []string{"out.txt"},
	}, { // Test 1: uv's --output-file is written, not fabricated.
		Markdown: "```sh\n$ uv pip compile requirements.in --universal " +
			"--output-file requirements.txt\n```\n",
		WantCreates: []string{"requirements.txt"},
	}, { // Test 2: The destination of a copy.
		Markdown:    "```sh\n$ cp a.conf b.conf\n```\n",
		WantCreates: []string{"b.conf"},
	}, { // Test 3: A skipped line creates nothing.
		Markdown: "```sh\n$ tool get https://api.example.com/v1 > out.txt\n```\n",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := BuildPlan("tool", "", test.Markdown, []string{"tool", "uv"}, nil, nil, testRecognizer())
			if len(plan.Steps) == 0 {
				t.Fatal("no steps planned")
			}
			l := plan.Steps[len(plan.Steps)-1].Lines[0]
			if diff := cmp.Diff(test.WantCreates, l.Creates, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("creates mismatch (-want +got):\n%s\nskip %q", diff, l.Skip)
			}
			if diff := cmp.Diff(test.WantSynthetic, l.Synthetic, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("synthetic mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestPackageManagerBlocksAreNotCreators checks that install blocks which can
// create no file in the working directory are not named as a possible creator
// of a file a later line reads, while blocks that can are.
func TestPackageManagerBlocksAreNotCreators(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Markdown      string
		Binary        string
		WantCmd       string
		WantNeed      string
		WantUnplanned string
	}{{ // Test 0: glow's shape names only the nix block.
		Markdown: "# Glow\n\n```sh\n# macOS or Linux\nbrew install glow\n```\n\n" +
			"```sh\nsudo port install glow\n```\n\n```sh\npacman -S glow\n```\n\n" +
			"```sh\nnix-shell -p glow --command glow\n```\n\n```sh\nglow -s mystyle.json\n```\n",
		Binary:        "glow",
		WantCmd:       "glow -s mystyle.json",
		WantNeed:      "mystyle.json",
		WantUnplanned: "the code block at line 17",
	}, { // Test 1: rich-cli's shape leaves no unplanned block.
		Markdown: "# Rich\n\n```sh\npipx install rich-cli\n```\n\n```sh\nbrew install rich\n```\n\n" +
			"```sh\npython -m pip install rich-cli\n```\n\n" +
			"```sh\nmamba install -c conda-forge rich-cli\n```\n\n```sh\nrich loop.py\n```\n",
		Binary:   "rich",
		WantCmd:  "rich loop.py",
		WantNeed: "loop.py",
	}, { // Test 2: a redirect makes a block a possible creator.
		Markdown: "# Tool\n\n```sh\nfancygen > out.json\n```\n\n```sh\ntool read out.json\n```\n",
		Binary:   "tool", WantCmd: "tool read out.json", WantNeed: "out.json",
		WantUnplanned: "the code block at line 4",
	}, { // Test 3: an editable pip install makes a block a possible creator.
		Markdown: "# Tool\n\n```sh\npip install -e .\n```\n\n```sh\ntool read out.json\n```\n",
		Binary:   "tool", WantCmd: "tool read out.json", WantNeed: "out.json",
		WantUnplanned: "the code block at line 4",
	}, { // Test 4: a package manager's run verb can write anything.
		Markdown: "# Tool\n\n```sh\npipx run fancygen\n```\n\n```sh\ntool read out.json\n```\n",
		Binary:   "tool", WantCmd: "tool read out.json", WantNeed: "out.json",
		WantUnplanned: "the code block at line 4",
	}, { // Test 5: brew bundle dump writes a Brewfile into the working directory.
		Markdown: "# Tool\n\n```sh\nbrew bundle dump\n```\n\n```sh\ntool read out.json\n```\n",
		Binary:   "tool", WantCmd: "tool read out.json", WantNeed: "out.json",
		WantUnplanned: "the code block at line 4",
	}, { // Test 6: a pip option outside the index-only set can name a file pip writes.
		Markdown: "# Tool\n\n```sh\npip install --report out.json rich\n```\n\n" +
			"```sh\ntool read out.json\n```\n",
		Binary: "tool", WantCmd: "tool read out.json", WantNeed: "out.json",
		WantUnplanned: "the code block at line 4",
	}, { // Test 7: a conda prefix can sit under the working directory.
		Markdown: "# Tool\n\n```sh\nconda install -p env rich\n```\n\n" +
			"```sh\ntool read out.json\n```\n",
		Binary: "tool", WantCmd: "tool read out.json", WantNeed: "out.json",
		WantUnplanned: "the code block at line 4",
	}, { // Test 8: options before the install verb still read as an install.
		Markdown: "# Tool\n\n```sh\nsudo apt-get -y install curl\n```\n\n" +
			"```sh\nsudo pacman -Syu curl\n```\n\n```sh\ntool read out.json\n```\n",
		Binary: "tool", WantCmd: "tool read out.json", WantNeed: "out.json",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := BuildPlan("repo", t.TempDir(), test.Markdown, []string{test.Binary},
				nil, nil, testRecognizer())
			var got *PlanLine
			for _, s := range plan.Steps {
				for i := range s.Lines {
					if docblock.Flatten(s.Lines[i].Cmd) == test.WantCmd {
						got = &s.Lines[i]
					}
				}
			}
			if got == nil {
				t.Fatalf("line %q not planned", test.WantCmd)
			}
			if got.Need != test.WantNeed {
				t.Errorf("need = %q, want %q", got.Need, test.WantNeed)
			}
			if !strings.HasPrefix(got.Unplanned, test.WantUnplanned) ||
				(test.WantUnplanned == "" && got.Unplanned != "") {
				t.Errorf("unplanned = %q, want prefix %q", got.Unplanned, test.WantUnplanned)
			}
		})
	}
}

// TestCommentOnlyBlockStillPlans checks that a block whose remaining lines are
// all comments is neither counted as excluded nor hides the step after it.
func TestCommentOnlyBlockStillPlans(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Markdown string
	}{{ // Test 0: A block of only a comment.
		Markdown: "# Tool\n\n```sh\n# only a comment\n```\n\n```sh\ntool run\n```\n",
	}, { // Test 1: A comment left once the recognizer sets the install aside.
		Markdown: "# Tool\n\n```sh\n# macOS or Linux\nbrew install tool\n```\n\n" +
			"```sh\ntool run\n```\n",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := BuildPlan("repo", t.TempDir(), test.Markdown, []string{"tool"}, nil, nil,
				testRecognizer())
			want := [][]planLine{{{Cmd: "tool run"}}}
			if diff := cmp.Diff(want, projectPlan(plan)); diff != "" {
				t.Errorf("steps mismatch (-want +got):\n%s", diff)
			}
			if plan.Excluded != 0 {
				t.Errorf("excluded = %d, want 0", plan.Excluded)
			}
		})
	}
}

// TestPlanShown checks that comment lines directly under a command are kept
// as the output the document shows for it.
func TestPlanShown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Markdown  string
		WantShown [][]string
	}{{ // Test 0: A refusal shown as a comment under the command.
		Markdown: "```sh\nSAFE_RM_SCOPE=\"$HOME\" tool -rf /\n" +
			"# rm: target '/' skipped, unsafe directory scope\n```\n",
		WantShown: [][]string{{"rm: target '/' skipped, unsafe directory scope"}},
	}, { // Test 1: A note comment is collected too, and only by the line above it.
		Markdown:  "```sh\ntool check .\n# exits 1 when it finds a problem\ntool fmt .\n```\n",
		WantShown: [][]string{{"exits 1 when it finds a problem"}, nil},
	}, { // Test 2: Several comment lines in a row all belong to the command.
		Markdown:  "```sh\ntool list\n#  alpha one\n# beta two\n```\n",
		WantShown: [][]string{{"alpha one", "beta two"}},
	}, { // Test 3: A comment before the first command belongs to no line.
		Markdown:  "```sh\n# first install it\ntool list\n```\n",
		WantShown: [][]string{nil},
	}, { // Test 4: A blank line ends what the command shows.
		Markdown:  "```sh\ntool list\n\n# now build it\ntool build\n```\n",
		WantShown: [][]string{nil, nil},
	}, { // Test 5: A skipped line keeps what the document shows for it.
		Markdown: "```sh\ntool get https://api.example.com/v1\n" +
			"# {\"ok\": true}\n```\n",
		WantShown: [][]string{{"{\"ok\": true}"}},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := BuildPlan("tool", "", test.Markdown, []string{"tool"}, nil, nil, testRecognizer())
			if len(plan.Steps) != 1 {
				t.Fatalf("steps = %d, want 1", len(plan.Steps))
			}
			var got [][]string
			for _, l := range plan.Steps[0].Lines {
				got = append(got, l.Shown)
			}
			if diff := cmp.Diff(test.WantShown, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("shown mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestDescribedAsLinter checks that a subcommand the page introduces as
// linting for problems is check mode for that page, and that a subcommand
// described as checking something else is not.
func TestDescribedAsLinter(t *testing.T) {
	t.Parallel()
	recheck := "# recheck\n\nThe `recheck` command lints Markdown files for structure and " +
		"prose problems.\n\n"
	tests := []struct {
		Markdown    string
		WantSubs    []string
		WantNonzero []bool
	}{{ // Test 0: Both a plain run and one whose stdout goes to a file are check mode.
		Markdown: recheck + "```sh\ntool recheck docs\n```\n\n" +
			"```sh\ntool recheck docs --format=json > out.json\n```\n",
		WantSubs: []string{"recheck"}, WantNonzero: []bool{true, true},
	}, { // Test 1: Checking whether something exists is not linting.
		Markdown: "# init\n\nThe `init` command checks whether a project exists.\n\n" +
			"```sh\ntool init\n```\n",
		WantNonzero: []bool{false},
	}, { // Test 2: A linter subcommand of another page does not carry over.
		Markdown:    "# build\n\nThe `build` command bundles files.\n\n```sh\ntool recheck docs\n```\n",
		WantNonzero: []bool{false},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.WantSubs, describedAsLinter(test.Markdown),
				cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("describedAsLinter mismatch (-want +got):\n%s", diff)
			}
			plan := BuildPlan("repo", "", test.Markdown, []string{"tool"}, nil, nil, testRecognizer())
			var got []bool
			for _, l := range projectPlan(plan) {
				for _, pl := range l {
					got = append(got, pl.NonzeroOK)
				}
			}
			if diff := cmp.Diff(test.WantNonzero, got); diff != "" {
				t.Errorf("NonzeroOK mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestLostDirectorySpansBlocks checks that a skipped cd loses the directory for
// the rest of its section, and that a later cd the session can make restores it.
func TestLostDirectorySpansBlocks(t *testing.T) {
	t.Parallel()
	const lost = "follows a skipped cd"
	tests := []struct {
		Markdown string
		WantLost []bool
	}{{ // Test 0: A skipped cd in one block skips the next block's lines.
		Markdown: "```sh\ntool init\ncd <your-repo>\n```\n\n```sh\ngit add .gitmodules\ntool sync\n```\n",
		WantLost: []bool{false, false, true, true},
	}, { // Test 1: A later cd into a directory the tree holds clears it.
		Markdown: "```sh\ntool init\ncd <your-repo>\n```\n\n```sh\ntool sync\n```\n\n" +
			"```sh\ncd examples\ntool run\n```\n",
		WantLost: []bool{false, false, true, false, false},
	}, { // Test 2: A skipped cd still skips the rest of its own block.
		Markdown: "```sh\ncd <your-repo>\ntool sync\n```\n",
		WantLost: []bool{false, true},
	}, { // Test 3: A cd after a skipped cd in the same block reads from the lost
		// directory, so it does not clear it.
		Markdown: "```sh\ncd <your-repo>\ncd examples\ntool sync\n```\n",
		WantLost: []bool{false, true, true},
	}, { // Test 4: A document without a skipped cd loses nothing.
		Markdown: "```sh\ncd examples\ntool sync\n```\n\n```sh\ntool run\n```\n",
		WantLost: []bool{false, false, false},
	}, { // Test 5: A new section ends the lost directory.
		Markdown: "## Build\n\n```sh\ncd <your-repo>\n```\n\n```sh\ntool sync\n```\n\n" +
			"## Install\n\n```sh\ntool run\n```\n",
		WantLost: []bool{false, true, false},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "examples"), 0o755); err != nil {
				t.Fatal(err)
			}
			plan := BuildPlan("repo", dir, test.Markdown, []string{"tool"}, nil, nil, testRecognizer())
			var got []bool
			for _, s := range plan.Steps {
				for _, l := range s.Lines {
					isLost := strings.Contains(l.Skip, lost)
					if isLost && l.SkipReason != verdict.ReasonDependsOnSkipped {
						t.Errorf("%q: reason %q, want %q", l.Cmd, l.SkipReason, verdict.ReasonDependsOnSkipped)
					}
					got = append(got, isLost)
				}
			}
			if diff := cmp.Diff(test.WantLost, got); diff != "" {
				t.Errorf("lost mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestImpliedFindingFlag checks that a flag the document says implies a
// check-mode flag puts a line in check mode for that document only.
func TestImpliedFindingFlag(t *testing.T) {
	t.Parallel()
	sarif := "# SARIF\n\n`--format sarif` implies `--fix=false`, so files are not modified.\n\n"
	tests := []struct {
		Markdown    string
		WantFlags   []string
		WantNonzero []bool
	}{{ // Test 0: The implied flag, with its value as a separate word or joined.
		Markdown: sarif + "```sh\ntool run --format sarif\n```\n\n" +
			"```sh\ntool run --format=sarif > out.json\n```\n",
		WantFlags: []string{"--format sarif"}, WantNonzero: []bool{true, true},
	}, { // Test 1: The implied flag must be one of the fixed set.
		Markdown:    "# Text\n\n`--fix` implies `--format text`.\n\n```sh\ntool run --fix\n```\n",
		WantNonzero: []bool{false},
	}, { // Test 2: Another value of the same flag is not check mode.
		Markdown:    sarif + "```sh\ntool run --format json\n```\n",
		WantFlags:   []string{"--format sarif"},
		WantNonzero: []bool{false},
	}, { // Test 3: The sentence on another page does not carry over.
		Markdown:    "# Run\n\n```sh\ntool run --format sarif\n```\n",
		WantNonzero: []bool{false},
	}, { // Test 4: An explicit --fix beside the implied flag turns fixing back on.
		Markdown:    sarif + "```sh\ntool run --format sarif --fix\n```\n",
		WantFlags:   []string{"--format sarif"},
		WantNonzero: []bool{false},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.WantFlags, impliedFindingFlags(test.Markdown),
				cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("impliedFindingFlags mismatch (-want +got):\n%s", diff)
			}
			plan := BuildPlan("repo", "", test.Markdown, []string{"tool"}, nil, nil, testRecognizer())
			var got []bool
			for _, l := range projectPlan(plan) {
				for _, pl := range l {
					got = append(got, pl.NonzeroOK)
				}
			}
			if diff := cmp.Diff(test.WantNonzero, got); diff != "" {
				t.Errorf("NonzeroOK mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
