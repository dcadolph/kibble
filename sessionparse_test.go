package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	kplan "github.com/dcadolph/kibble/internal/plan"
)

// TestClassifyExampleDocumentExit checks how lines with no marker are read.
// A session that finished means the shell that stopped short was one
// document's own, so its remaining lines say so and later documents keep
// their results. A session that never finished still reports the line it
// stopped on as the hang when its budget ran out.
func TestClassifyExampleDocumentExit(t *testing.T) {
	t.Parallel()
	const exited = "not established: the document's shell exited at or before this line"
	const ended = "not run: the session ended before reaching it"
	plan := &kplan.Plan{Repo: "repo", Steps: []kplan.PlanStep{
		{ID: "b1", Doc: "README.md", Lines: []kplan.PlanLine{{Cmd: "echo a"}, {Cmd: "exit 3"}, {Cmd: "echo b"}}},
		{ID: "b2", Doc: "docs/guide.md", Lines: []kplan.PlanLine{{Cmd: "echo c"}}},
	}}
	step := InstallStep{Repo: "repo", Kind: "example"}
	tests := []struct {
		Out         string
		End         sessionEnd
		WantStatus  []Status
		WantDetails []string
	}{{ // Test 0: The first document exits and the second still runs.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"a",
			"KIBBLE-LINE b1:0 CODE=0",
			"KIBBLE-STEP b2 START",
			"c",
			"KIBBLE-LINE b2:0 CODE=0",
			"KIBBLE-DONE"),
		WantStatus:  []Status{StatusVerified, StatusBlocked, StatusBlocked, StatusVerified},
		WantDetails: []string{"", exited, exited, ""},
	}, { // Test 1: The session stops on the line and never finishes.
		Out: sessionOut(
			"KIBBLE-BUILD CODE=0",
			"KIBBLE-STEP b1 START",
			"a",
			"KIBBLE-LINE b1:0 CODE=0"),
		End:         sessionEnd{expired: true},
		WantStatus:  []Status{StatusVerified, StatusTimeout, StatusBlocked, StatusBlocked},
		WantDetails: []string{"", "session ended while this line ran", ended, ended},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			res := classifyExample(step, plan, test.Out, nil, 0, lineTimeout, test.End)
			if res.example == nil {
				t.Fatalf("no per-line outcomes, status %s: %s", res.Status, res.Detail)
			}
			var status []Status
			var details []string
			for _, s := range res.example.Steps {
				for _, l := range s.Lines {
					status = append(status, l.Status)
					if l.Status == StatusVerified {
						details = append(details, "")
						continue
					}
					details = append(details, l.Detail)
				}
			}
			if diff := cmp.Diff(test.WantStatus, status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantDetails, details); diff != "" {
				t.Errorf("detail mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestLineMarker checks that a line marker carries the directory the line ran
// in when the session printed one, and parses as before when it did not.
func TestLineMarker(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Line     string
		WantCode string
		WantCwd  string
		WantBG   string
	}{{ // Test 0: An exit code with the directory.
		Line:     "KIBBLE-LINE b1:0 CODE=2 CWD=/work/repo/example",
		WantCode: "2", WantCwd: "/work/repo/example",
	}, { // Test 1: A directory with a space in its name.
		Line:     "KIBBLE-LINE b1:0 CODE=0 CWD=/work/repo/my dir",
		WantCode: "0", WantCwd: "/work/repo/my dir",
	}, { // Test 2: An exit code without a directory.
		Line: "KIBBLE-LINE b1:0 CODE=1", WantCode: "1",
	}, { // Test 3: A background readiness marker.
		Line: "KIBBLE-LINE b1:0 BG=0", WantBG: "0",
	}, { // Test 4: Output that ended without a newline before the marker.
		Line: "doneKIBBLE-LINE b1:3 CODE=0 CWD=/work/repo", WantCode: "0", WantCwd: "/work/repo",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			m := reLineMarker.FindStringSubmatch(test.Line)
			if m == nil {
				t.Fatalf("no marker in %q", test.Line)
			}
			got := []string{m[3], m[4], m[5]}
			if diff := cmp.Diff([]string{test.WantCode, test.WantCwd, test.WantBG}, got); diff != "" {
				t.Errorf("marker mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestClassifyExampleInheritedState checks that a failure in a directory an
// earlier section entered is inconclusive, while a failure in the section that
// entered it, or in the repository root, still convicts. The shape is uv's
// README: Projects runs `uv init example` and `cd example`, and Python versions
// later fails on the requires-python the example project pinned.
func TestClassifyExampleInheritedState(t *testing.T) {
	t.Parallel()
	const pinned = "error: The requested Python version `3.11` is incompatible with the project " +
		"`requires-python` value of `>=3.14.9`."
	plan := &kplan.Plan{Repo: "uv", Steps: []kplan.PlanStep{
		{ID: "b1", Doc: "README.md", Heading: "Projects", Lines: []kplan.PlanLine{
			{Cmd: "uv init example"}, {Cmd: "cd example"}, {Cmd: "uv lock"},
		}},
		{ID: "b2", Doc: "README.md", Heading: "Python versions", Lines: []kplan.PlanLine{
			{Cmd: "uv python pin 3.11"},
		}},
		{ID: "b3", Doc: "README.md", Heading: "Projects", Lines: []kplan.PlanLine{
			{Cmd: "uv sync"},
		}},
	}}
	step := InstallStep{Repo: "uv", Kind: "example"}
	tests := []struct {
		Marker     string
		Key        string
		WantStatus Status
		WantReason Reason
	}{{ // Test 0: A later section fails in the directory Projects entered.
		Key: "b2:0", Marker: "KIBBLE-LINE b2:0 CODE=2 CWD=/work/repo/example",
		WantStatus: StatusBlocked, WantReason: ReasonInheritedState,
	}, { // Test 1: The section that entered the directory fails there.
		Key: "b1:2", Marker: "KIBBLE-LINE b1:2 CODE=2 CWD=/work/repo/example",
		WantStatus: StatusFail,
	}, { // Test 2: A later block of the same section fails there.
		Key: "b3:0", Marker: "KIBBLE-LINE b3:0 CODE=2 CWD=/work/repo/example",
		WantStatus: StatusFail,
	}, { // Test 3: A later section fails in the repository root.
		Key: "b2:0", Marker: "KIBBLE-LINE b2:0 CODE=2 CWD=/work/repo",
		WantStatus: StatusFail,
	}, { // Test 4: A marker without a directory settles nothing about one.
		Key: "b2:0", Marker: "KIBBLE-LINE b2:0 CODE=2",
		WantStatus: StatusFail,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			markers := map[string]string{
				"b1:0": "KIBBLE-LINE b1:0 CODE=0 CWD=/work/repo",
				"b1:1": "KIBBLE-LINE b1:1 CODE=0 CWD=/work/repo",
				"b1:2": "KIBBLE-LINE b1:2 CODE=0 CWD=/work/repo/example",
				"b2:0": "KIBBLE-LINE b2:0 CODE=0 CWD=/work/repo/example",
				"b3:0": "KIBBLE-LINE b3:0 CODE=0 CWD=/work/repo/example",
			}
			markers[test.Key] = test.Marker
			lines := []string{"KIBBLE-BUILD CODE=0"}
			for _, s := range plan.Steps {
				lines = append(lines, "KIBBLE-STEP "+s.ID+" START")
				for i := range s.Lines {
					key := fmt.Sprintf("%s:%d", s.ID, i)
					if key == test.Key {
						lines = append(lines, pinned)
					}
					lines = append(lines, markers[key])
				}
			}
			lines = append(lines, "KIBBLE-DONE")
			res := classifyExample(step, plan, sessionOut(lines...), nil, 0, lineTimeout, sessionEnd{})
			if res.example == nil {
				t.Fatalf("no per-line outcomes, status %s: %s", res.Status, res.Detail)
			}
			var got lineResult
			for _, s := range res.example.Steps {
				for i, l := range s.Lines {
					if fmt.Sprintf("%s:%d", s.ID, i) == test.Key {
						got = l
					}
				}
			}
			if diff := cmp.Diff(test.WantStatus, got.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s\ndetail: %s", diff, got.Detail)
			}
			if diff := cmp.Diff(test.WantReason, got.Reason); diff != "" {
				t.Errorf("reason mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestClassifyExampleEngineDeath checks a session the container ended with
// budget left. The line that was running has no verdict and the session is an
// error, since the engine dying says nothing about the document. The same
// output from a session whose budget ran out still reads as the hang.
func TestClassifyExampleEngineDeath(t *testing.T) {
	t.Parallel()
	plan := &kplan.Plan{Repo: "repo", Steps: []kplan.PlanStep{
		{ID: "b1", Doc: "README.md", Lines: []kplan.PlanLine{{Cmd: "schemathesis --version"}}},
		{ID: "b2", Doc: "README.md", Lines: []kplan.PlanLine{{Cmd: "export USERNAME=user"}}},
	}}
	step := InstallStep{Repo: "repo", Kind: "example"}
	died := fmt.Errorf("exit status 125: %w",
		errors.New("error waiting for container: copying response body from Docker: unexpected EOF"))
	out := sessionOut(
		"KIBBLE-BUILD CODE=0",
		"KIBBLE-STEP b1 START",
		"schemathesis 4.3.0",
		"KIBBLE-LINE b1:0 CODE=0",
		"KIBBLE-STEP b2 START")
	tests := []struct {
		End         sessionEnd
		WantStatus  Status
		WantDetail  string
		WantLines   []Status
		WantReasons []Reason
	}{{ // Test 0: The engine dies with budget left.
		End:        sessionEnd{err: died},
		WantStatus: StatusError,
		WantDetail: "the container ended before the session finished, so the examples have no verdict: " +
			"exit status 125: error waiting for container: copying response body from Docker: unexpected EOF",
		WantLines:   []Status{StatusVerified, StatusBlocked},
		WantReasons: []Reason{"", ReasonNotExecuted},
	}, { // Test 1: The budget ran out, so the running line is the hang.
		End:         sessionEnd{expired: true, err: died},
		WantStatus:  StatusTimeout,
		WantDetail:  `README.md b2 "export USERNAME=user" session ended while this line ran`,
		WantLines:   []Status{StatusVerified, StatusTimeout},
		WantReasons: []Reason{"", ""},
	}, { // Test 2: The engine dies with no exit error to carry.
		End:         sessionEnd{},
		WantStatus:  StatusError,
		WantDetail:  "the container ended before the session finished, so the examples have no verdict",
		WantLines:   []Status{StatusVerified, StatusBlocked},
		WantReasons: []Reason{"", ReasonNotExecuted},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			res := classifyExample(step, plan, out, nil, 0, lineTimeout, test.End)
			if res.Status != test.WantStatus {
				t.Errorf("status = %s, want %s", res.Status, test.WantStatus)
			}
			if diff := cmp.Diff(test.WantDetail, res.Detail); diff != "" {
				t.Errorf("detail mismatch (-want +got):\n%s", diff)
			}
			if res.example == nil {
				t.Fatalf("no per-line outcomes")
			}
			var lines []Status
			var reasons []Reason
			for _, s := range res.example.Steps {
				for _, l := range s.Lines {
					lines = append(lines, l.Status)
					reasons = append(reasons, l.Reason)
				}
			}
			if diff := cmp.Diff(test.WantLines, lines); diff != "" {
				t.Errorf("line status mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantReasons, reasons); diff != "" {
				t.Errorf("line reason mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestClassifyExampleCheckoutState checks that a scaffold refused by the checkout is inconclusive.
func TestClassifyExampleCheckoutState(t *testing.T) {
	t.Parallel()
	const refused = "error: Project is already initialized in `/work/repo` " +
		"(`pyproject.toml` file exists)"
	plan := &kplan.Plan{Repo: "uv", Steps: []kplan.PlanStep{
		{ID: "b1", Doc: "README.md", Heading: "Projects", Lines: []kplan.PlanLine{
			{Cmd: "cd example"}, {Cmd: "uv init"},
		}},
	}}
	step := InstallStep{Repo: "uv", Kind: "example"}
	tests := []struct {
		Marker     string
		Key        string
		WantStatus Status
		WantReason Reason
	}{{ // Test 0: A scaffold refused in the checkout is inconclusive.
		Key: "b1:1", Marker: "KIBBLE-LINE b1:1 CODE=2 CWD=/work/repo",
		WantStatus: StatusBlocked, WantReason: ReasonCheckoutState,
	}, { // Test 1: The same output in a directory the document chose still fails.
		Key: "b1:1", Marker: "KIBBLE-LINE b1:1 CODE=2 CWD=/work/repo/example",
		WantStatus: StatusFail,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			lines := []string{
				"KIBBLE-BUILD CODE=0", "KIBBLE-STEP b1 START",
				"KIBBLE-LINE b1:0 CODE=0 CWD=/work/repo/example", refused, test.Marker, "KIBBLE-DONE",
			}
			res := classifyExample(step, plan, sessionOut(lines...), nil, 0, lineTimeout, sessionEnd{})
			if res.example == nil {
				t.Fatalf("no per-line outcomes, status %s: %s", res.Status, res.Detail)
			}
			got := res.example.Steps[0].Lines[1]
			if diff := cmp.Diff(test.WantStatus, got.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s\ndetail: %s", diff, got.Detail)
			}
			if diff := cmp.Diff(test.WantReason, got.Reason); diff != "" {
				t.Errorf("reason mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestClassifyExampleNeed checks how a line that started without a file it
// reads is settled. The gap is the document's only when every earlier line of
// the same document passed. Any earlier line that did not pass, or a block the
// plan left out, may be what creates the file, so the line is blocked naming
// it. A line that passed without the file never needed it.
func TestClassifyExampleNeed(t *testing.T) {
	t.Parallel()
	const marimo = "uvx marimo edit my_notebook.py"
	need := kplan.PlanLine{Cmd: "uv add --script my_notebook.py numpy", Need: "my_notebook.py"}
	step := InstallStep{Repo: "uv", Kind: "example"}
	tests := []struct {
		First      kplan.PlanLine
		FirstDoc   string
		Unplanned  string
		Need       string
		Code       string
		WantStatus Status
		WantReason Reason
		WantDetail string
	}{{ // Test 0: Every earlier line passed, so the gap is earned.
		First: kplan.PlanLine{Cmd: "uv --version"}, Need: "0", Code: "2",
		WantStatus: StatusGap, WantReason: ReasonMissingFixture,
		WantDetail: "references my_notebook.py, which no documented step creates",
	}, { // Test 1: An earlier skipped line may create the file.
		First: kplan.PlanLine{Cmd: marimo, Skip: "starts an interactive session"}, Need: "0", Code: "2",
		WantStatus: StatusBlocked, WantReason: ReasonDependsOnSkipped,
		WantDetail: "references my_notebook.py, which an earlier line that did not pass may create: " + marimo,
	}, { // Test 2: A block the plan left out may create the file.
		First: kplan.PlanLine{Cmd: "uv --version"}, Unplanned: marimo, Need: "0", Code: "2",
		WantStatus: StatusBlocked, WantReason: ReasonDependsOnSkipped,
		WantDetail: "references my_notebook.py, which an earlier line that did not pass may create: " + marimo,
	}, { // Test 3: The file was there, so the line reads by its own exit code.
		First: kplan.PlanLine{Cmd: "uv --version"}, Need: "1", Code: "2",
		WantStatus: StatusFail,
	}, { // Test 4: The file was there and the line passed.
		First: kplan.PlanLine{Cmd: "uv --version"}, Need: "1", Code: "0",
		WantStatus: StatusVerified,
	}, { // Test 5: A line that passed without the file never needed it.
		First: kplan.PlanLine{Cmd: marimo, Skip: "starts an interactive session"}, Need: "0", Code: "0",
		WantStatus: StatusVerified,
	}, { // Test 6: A skipped line in another document does not block this one.
		First: kplan.PlanLine{Cmd: marimo, Skip: "starts an interactive session"}, FirstDoc: "README.md",
		Need: "0", Code: "2",
		WantStatus: StatusGap, WantReason: ReasonMissingFixture,
		WantDetail: "references my_notebook.py, which no documented step creates",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			doc := "docs/marimo.md"
			firstDoc := doc
			if test.FirstDoc != "" {
				firstDoc = test.FirstDoc
			}
			line := need
			line.Unplanned = test.Unplanned
			plan := &kplan.Plan{Repo: "uv", Steps: []kplan.PlanStep{
				{ID: "b1", Doc: firstDoc, Lines: []kplan.PlanLine{test.First}},
				{ID: "b2", Doc: doc, Lines: []kplan.PlanLine{line}},
			}}
			first := "KIBBLE-LINE b1:0 CODE=0 CWD=/work/repo"
			if test.First.Skip != "" {
				first = "KIBBLE-LINE b1:0 SKIP"
			}
			out := sessionOut(
				"KIBBLE-BUILD CODE=0",
				"KIBBLE-STEP b1 START", first,
				"KIBBLE-STEP b2 START",
				"KIBBLE-NEED b2:0 "+test.Need,
				"error: failed to read from file `my_notebook.py`",
				"KIBBLE-LINE b2:0 CODE="+test.Code+" CWD=/work/repo",
				"KIBBLE-DONE")
			res := classifyExample(step, plan, out, nil, 0, lineTimeout, sessionEnd{})
			if res.example == nil {
				t.Fatalf("no per-line outcomes, status %s: %s", res.Status, res.Detail)
			}
			got := res.example.Steps[1].Lines[0]
			if diff := cmp.Diff(test.WantStatus, got.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s\ndetail: %s", diff, got.Detail)
			}
			if diff := cmp.Diff(test.WantReason, got.Reason); diff != "" {
				t.Errorf("reason mismatch (-want +got):\n%s", diff)
			}
			if test.WantDetail != "" {
				if diff := cmp.Diff(test.WantDetail, got.Detail); diff != "" {
					t.Errorf("detail mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}
