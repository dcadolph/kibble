package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// planLine is a compact projection of a PlanLine for test comparison.
type planLine struct {
	Cmd       string
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
				Cmd: flatten(l.Cmd), Skip: l.Skip != "", Gap: l.Gap,
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
		Cfg          *ExamplesConfig
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
	}, { // Test 2a: a file no documented line creates is the document's gap.
		Markdown:  "```sh\ntool run ./secrets\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool run ./secrets", Skip: true, Gap: true}}},
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
	}, { // Test 2p: a notebook the docs never create is the reader's, the
		// same as any other file argument, and running it is a false failure.
		Markdown:  "```sh\ntool render notebook.ipynb\n```\n",
		WantSteps: [][]planLine{{{Cmd: "tool render notebook.ipynb", Skip: true, Gap: true}}},
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
			{Cmd: "tool load data.csv", Skip: true, Gap: true},
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
		Cfg:       &ExamplesConfig{Env: map[string]string{"TOKEN": "abc"}},
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
			plan := buildPlan("repo", dir, test.Markdown, []string{"tool"},
				[]PlanInstall{{Cmd: "go install example.com/tool@latest", Ecosystem: "go"}}, test.Cfg)
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
		{Cmd: "tool index --file missing.csv", Skip: true, Gap: true},
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

// TestSkipHeuristics checks the skip classes that keep kibble from blaming
// correct docs for what a clean container lacks.
func TestSkipHeuristics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Markdown string
		WantSkip string
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
		WantSkip: "git history or a remote",
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
		WantSkip: "never create",
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
	}, { // Test 12: a home config the docs never create is skipped.
		Markdown: "```sh\ntool --config ~/.config/tool/config.toml\n```\n",
		WantSkip: "references ~/.config/tool/config.toml",
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
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := buildPlan("repo", "", test.Markdown, []string{"tool"},
				[]PlanInstall{{Cmd: "go install example.com/tool@latest", Ecosystem: "go"}}, nil)
			var last PlanLine
			found := false
			for _, s := range plan.Steps {
				for _, l := range s.Lines {
					if strings.HasPrefix(flatten(l.Cmd), "tool") || strings.HasPrefix(flatten(l.Cmd), "git") || strings.HasPrefix(flatten(l.Cmd), "cd /") {
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
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := buildPlan("repo", "", test.Markdown, []string{"tool"},
				[]PlanInstall{{Cmd: "go install example.com/tool@latest", Ecosystem: "go"}}, nil)
			var got PlanLine
			found := false
			for _, s := range plan.Steps {
				for _, l := range s.Lines {
					if strings.HasPrefix(flatten(l.Cmd), "tool") {
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
