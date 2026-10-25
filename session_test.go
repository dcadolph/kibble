package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/kibble/internal/config"
	kplan "github.com/dcadolph/kibble/internal/plan"
)

// TestCommitEditor runs git against the editor the session installs, one
// commit form per case. The editor true made git abort every commit that
// opened an editor, which failed a documented `git commit` that works for
// any reader who types a message.
func TestCommitEditor(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	editor := filepath.Join(t.TempDir(), "editor")
	if err := os.WriteFile(editor, []byte(commitEditor), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		Setup       [][]string
		Args        []string
		WantSubject string
		WantOK      bool
	}{{ // Test 0: A bare commit after an add gets a message.
		Setup:  [][]string{{"add", "."}},
		Args:   []string{"commit"},
		WantOK: true, WantSubject: "kibble",
	}, { // Test 1: The diff commit -v appends below the scissors is not a message.
		Setup:  [][]string{{"add", "."}},
		Args:   []string{"commit", "-v"},
		WantOK: true, WantSubject: "kibble",
	}, { // Test 2: commit -a opens the same editor.
		Setup:  [][]string{{"add", "a.txt"}},
		Args:   []string{"commit", "-a"},
		WantOK: true, WantSubject: "kibble",
	}, { // Test 3: An empty commit with no message.
		Args:   []string{"commit", "--allow-empty"},
		WantOK: true, WantSubject: "kibble",
	}, { // Test 4: A message given to -e is kept.
		Setup:  [][]string{{"add", "."}},
		Args:   []string{"commit", "-e", "-m", "x"},
		WantOK: true, WantSubject: "x",
	}, { // Test 5: A message given to -m never opens the editor.
		Setup:  [][]string{{"add", "."}},
		Args:   []string{"commit", "-m", "y"},
		WantOK: true, WantSubject: "y",
	}, { // Test 6: Nothing staged still fails, before any editor opens.
		Args:   []string{"commit"},
		WantOK: false,
	}, { // Test 7: Known limit, an unedited template still aborts.
		Setup:  [][]string{{"add", "."}},
		Args:   []string{"commit", "-t", "tmpl.txt"},
		WantOK: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := testGitRepo(t)
			for _, args := range test.Setup {
				if _, err := runGit(dir, editor, args...); err != nil {
					t.Fatalf("setup git %v: %v", args, err)
				}
			}
			out, err := runGit(dir, editor, test.Args...)
			if diff := cmp.Diff(test.WantOK, err == nil); diff != "" {
				t.Fatalf("git %v success mismatch (-want +got):\n%s\noutput: %s", test.Args, diff, out)
			}
			if !test.WantOK {
				return
			}
			subject, err := runGit(dir, editor, "log", "-1", "--format=%s")
			if err != nil {
				t.Fatalf("git log: %v", err)
			}
			if diff := cmp.Diff(test.WantSubject, strings.TrimSpace(subject)); diff != "" {
				t.Errorf("subject mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestCommitEditorLeavesPatch checks that the editor hands back any file other
// than a commit message untouched, so git add -e, an allowed form, stages
// exactly what it staged under the editor true. Writing into its patch would
// corrupt it.
func TestCommitEditorLeavesPatch(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	editor := filepath.Join(t.TempDir(), "editor")
	if err := os.WriteFile(editor, []byte(commitEditor), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := func(editor string) string {
		dir := testGitRepo(t)
		if _, err := runGit(dir, editor, "add", "a.txt"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\nmore\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := runGit(dir, editor, "add", "-e"); err != nil {
			t.Fatalf("git add -e: %v\n%s", err, out)
		}
		out, err := runGit(dir, editor, "diff", "--cached")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	want := staged("true")
	if !strings.Contains(want, "+more") {
		t.Fatalf("git add -e under the editor true staged no change:\n%s", want)
	}
	if diff := cmp.Diff(want, staged(editor)); diff != "" {
		t.Errorf("staged change mismatch (-want +got):\n%s", diff)
	}
}

// testGitRepo returns a fresh repository holding two untracked files and a
// commit template, configured the way the session configures its own.
func testGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"a.txt": "a\n", "b.txt": "b\n", "tmpl.txt": "tmpl\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "kibble@localhost"},
		{"config", "user.name", "kibble"},
	} {
		if out, err := runGit(dir, "true", args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// runGit runs git in dir with the given editor, isolated from the global and
// system configuration so the machine's hooks and settings do not apply.
func runGit(dir, editor string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_EDITOR="+editor,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return string(out), fmt.Errorf("git %v exited %d", args, exitErr.ExitCode())
		}
		return string(out), err
	}
	return string(out), nil
}

// TestSessionScriptDocuments checks that each document gets a shell of its
// own and that every document after the first gets a new repository, while a
// session of one document keeps the repository the preamble made.
func TestSessionScriptDocuments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Docs       []string
		WantShells int
		WantResets int
	}{{ // Test 0: One document across two blocks.
		Docs:       []string{"README.md", "README.md"},
		WantShells: 1, WantResets: 0,
	}, { // Test 1: Two documents.
		Docs:       []string{"README.md", "docs/guide.md", "docs/guide.md"},
		WantShells: 2, WantResets: 1,
	}, { // Test 2: Three documents.
		Docs:       []string{"README.md", "docs/a.md", "docs/b.md"},
		WantShells: 3, WantResets: 2,
	}, { // Test 3: No blocks.
		WantShells: 0, WantResets: 0,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := &kplan.Plan{
				Repo: "repo", Installs: []kplan.PlanInstall{{Cmd: "true"}}, Binaries: []string{"tool"},
				Env:      map[string]string{"KIBBLE_TEST": "1"},
				Fixtures: []config.Fixture{{Path: "notes.txt", Contents: "x"}},
			}
			for i, doc := range test.Docs {
				plan.Steps = append(plan.Steps, kplan.PlanStep{
					ID: fmt.Sprintf("b%d", i+1), Doc: doc,
					Lines: []kplan.PlanLine{{Cmd: "echo hi"}},
				})
			}
			script, _ := sessionScript(plan, 240, 90)
			got := []int{
				strings.Count(script, "(\ncd /work/repo\n"),
				strings.Count(script, documentEnd),
				strings.Count(script, "rm -rf .git\n"+sessionRepo),
				strings.Count(script, binaryRestore+treeRestore+"rm -rf .git\n"+sessionRepo),
			}
			want := []int{test.WantShells, test.WantShells, test.WantResets, test.WantResets}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("shells, ends, resets, restores mismatch (-want +got):\n%s\n%s", diff, script)
			}
			// The binaries are copied after the HAVE loop finds them and before
			// any document runs, and only when there is a boundary to restore at.
			stash := strings.Index(script, "__kbin=()")
			if diff := cmp.Diff(test.WantResets > 0, stash >= 0); diff != "" {
				t.Errorf("stash present mismatch (-want +got):\n%s", diff)
			}
			if stash >= 0 {
				have := strings.Index(script, "KIBBLE-NOBIN")
				first := strings.Index(script, "KIBBLE-STEP b1 START")
				if stash < have || stash > first {
					t.Errorf("stash at %d, want after the HAVE loop (%d) and before b1 (%d)", stash, have, first)
				}
			}
			// The tree is archived after the fixtures and the exports and before
			// any document opens, and only when there is a boundary to restore at.
			save := strings.Index(script, treeSave)
			if diff := cmp.Diff(test.WantResets > 0, save >= 0); diff != "" {
				t.Errorf("tree archive present mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantResets, strings.Count(script, treeRestore)); diff != "" {
				t.Errorf("tree restores mismatch (-want +got):\n%s", diff)
			}
			if save >= 0 {
				fixture := strings.Index(script, "base64 -d > 'notes.txt'")
				export := strings.Index(script, "export KIBBLE_TEST='1'")
				first := strings.Index(script, "(\ncd /work/repo\n")
				if save < fixture || save < export || save > first {
					t.Errorf("archive at %d, want after the fixture (%d) and export (%d) and before "+
						"the first document (%d)", save, fixture, export, first)
				}
			}
			// The repository is replaced between documents, never inside one.
			for i := 1; i < len(test.Docs); i++ {
				prev := strings.Index(script, fmt.Sprintf("KIBBLE-STEP b%d START", i))
				next := strings.Index(script, fmt.Sprintf("KIBBLE-STEP b%d START", i+1))
				reset := strings.Contains(script[prev:next], "rm -rf .git")
				if diff := cmp.Diff(test.Docs[i] != test.Docs[i-1], reset); diff != "" {
					t.Errorf("reset before b%d mismatch (-want +got):\n%s", i+1, diff)
				}
			}
		})
	}
}

// TestSessionScriptBackgroundGroup checks that a background block starts in a
// process group of its own, which is what the document's end kills.
func TestSessionScriptBackgroundGroup(t *testing.T) {
	t.Parallel()
	plan := &kplan.Plan{
		Repo:     "repo",
		Installs: []kplan.PlanInstall{{Cmd: "true"}},
		Steps:    []kplan.PlanStep{{ID: "b1", Background: true, Lines: []kplan.PlanLine{{Cmd: "tool serve"}}}},
	}
	script, _ := sessionScript(plan, 240, 90)
	for _, want := range []string{"set -m\n(\n", "KIBBLE_BG=\"${KIBBLE_BG:-} $!\"\nset +m\n", `kill -- -"$__p"`} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q", want)
		}
	}
}

// TestSessionScriptNewShellPath checks where the session takes a new shell's
// PATH: after every install, so an installer's rc edit is seen, and before the
// binaries are looked for, which is the point of taking it. A clone install
// runs from /work and every other install from the repository copy.
func TestSessionScriptNewShellPath(t *testing.T) {
	t.Parallel()
	plan := &kplan.Plan{
		Repo: "repo", Binaries: []string{"tool"},
		Installs: []kplan.PlanInstall{{Cmd: "pip install ."}, {Cmd: "~/.tool/install", Dir: "/work"}},
		Steps:    []kplan.PlanStep{{ID: "b1", Lines: []kplan.PlanLine{{Cmd: "tool"}}}},
	}
	script, _ := sessionScript(plan, 240, 90)
	at := strings.Index(script, newShellPath)
	if at < 0 {
		t.Fatalf("script missing the new-shell PATH step:\n%s", script)
	}
	if last := strings.LastIndex(script, "KIBBLE-BUILD CODE"); last > at {
		t.Error("new-shell PATH step runs before an install")
	}
	if have := strings.Index(script, "KIBBLE-HAVE"); have < at {
		t.Error("binaries are looked for before the new-shell PATH step")
	}
	if !strings.Contains(script, "bash -ec 'cd /work\n~/.tool/install'") {
		t.Error("clone install does not run from /work")
	}
	if !strings.Contains(script, "bash -ec 'pip install .'") {
		t.Error("package install no longer runs from the repository copy")
	}
	bare, _ := sessionScript(&kplan.Plan{Repo: "repo"}, 240, 90)
	if strings.Contains(bare, newShellPath) {
		t.Error("session without installs gained the new-shell PATH step")
	}
}

// TestPipecode runs the session's __pipecode function under bash for each
// list of pipeline exit codes. A 127 or 126 anywhere is the result, because a
// missing or unrunnable command must not hide behind an earlier failure.
func TestPipecode(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	script, _ := sessionScript(&kplan.Plan{Repo: "repo"}, 240, 90)
	fn := regexp.MustCompile(`(?s)__pipecode\(\) \{\n.*?\n\}\n`).FindString(script)
	if fn == "" {
		t.Fatalf("script missing the __pipecode function:\n%s", script)
	}
	tests := []struct {
		In         string
		WantResult string
	}{
		{In: "0 0", WantResult: "0"},       // Test 0: All zero.
		{In: "1 0", WantResult: "1"},       // Test 1: First failure wins.
		{In: "141 0", WantResult: "0"},     // Test 2: SIGPIPE is ignored.
		{In: "120 127", WantResult: "127"}, // Test 3: A 127 after another failure wins.
		{In: "23 127", WantResult: "127"},  // Test 4: A 127 last wins.
		{In: "0 1 127", WantResult: "127"}, // Test 5: A 127 after a failure wins.
		{In: "126 1", WantResult: "126"},   // Test 6: A leading 126 wins.
		{In: "1 126", WantResult: "126"},   // Test 7: A trailing 126 wins.
		{In: "127 1", WantResult: "127"},   // Test 8: A leading 127 wins.
		{In: "0 141 2", WantResult: "2"},   // Test 9: SIGPIPE skipped, then the last.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			out, err := exec.Command("bash", "-c", fn+"__pipecode "+test.In).Output()
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(test.WantResult, string(out)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSessionScriptAssignPrefix checks how lines carrying an environment prefix are wrapped.
func TestSessionScriptAssignPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Cmd         string
		WantContain string
		WantAbsent  string
		WantWrapped bool
	}{{ // Test 0: An assignment prefix runs under a shell inside the timeout.
		Cmd: "UV_INDEX=x uv lock", WantContain: "timeout 90 bash -c 'UV_INDEX=x uv lock",
		WantAbsent: "timeout 90 UV_INDEX=", WantWrapped: true,
	}, { // Test 1: A plain command is wrapped directly.
		Cmd: "uv lock", WantContain: "timeout 90 uv lock", WantWrapped: true,
	}, { // Test 2: An expanded assignment stays unwrapped.
		Cmd: "FOO=$BAR uv lock", WantContain: "\nFOO=$BAR uv lock\n",
		WantAbsent: "timeout 90 ", WantWrapped: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := &kplan.Plan{
				Repo: "repo",
				Steps: []kplan.PlanStep{{
					ID: "b1", Doc: "README.md", Lines: []kplan.PlanLine{{Cmd: test.Cmd}},
				}},
			}
			script, wrapped := sessionScript(plan, 240, 90)
			if !strings.Contains(script, test.WantContain) {
				t.Errorf("script lacks %q:\n%s", test.WantContain, script)
			}
			if test.WantAbsent != "" && strings.Contains(script, test.WantAbsent) {
				t.Errorf("script holds %q:\n%s", test.WantAbsent, script)
			}
			if diff := cmp.Diff(test.WantWrapped, wrapped["b1:0"]); diff != "" {
				t.Errorf("wrapped mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSessionScriptLineCwd checks that each line records the directory it ran
// in before it runs and reports it in its marker, so a failure can be read
// against the directory an earlier section left the shell in.
func TestSessionScriptLineCwd(t *testing.T) {
	t.Parallel()
	plan := &kplan.Plan{
		Repo: "repo",
		Steps: []kplan.PlanStep{{
			ID: "b1", Doc: "README.md", Lines: []kplan.PlanLine{{Cmd: "cd example"}, {Cmd: "uv lock"}},
		}},
	}
	script, _ := sessionScript(plan, 240, 90)
	for _, want := range []string{
		"__cwd=$PWD\ncd example\n",
		"__cwd=$PWD\ntimeout 90 uv lock\n",
		`KIBBLE-LINE b1:1 CODE=%d CWD=%s\n' "$(__pipecode "${__ps[@]}")" "$__cwd"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
}
