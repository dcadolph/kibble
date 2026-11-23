package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	kplan "github.com/dcadolph/kibble/internal/plan"
)

// trivyRoots is the top of trivy's checkout at 0231990, trimmed.
var trivyRoots = []string{".github", "README.md", "cmd", "contrib", "docs", "examples", "go.mod", "pkg"}

// TestReaderSupplied checks which missing names are the reader's input. The
// cases are the false gaps of the 2026-11-11 sweep and the true ones beside
// them.
func TestReaderSupplied(t *testing.T) {
	t.Parallel()
	roots := map[string]bool{}
	for _, r := range trivyRoots {
		roots[r] = true
	}
	tests := []struct {
		Name  string
		Cwd   string
		Roots map[string]bool
		Want  bool
	}{
		{Name: "examples/misconf/mixed", Want: false},                   // Test 0: points into a tracked directory.
		{Name: "READMEE.md", Want: false},                               // Test 1: one letter off a root file.
		{Name: "readme.md", Want: false},                                // Test 2: a root file in another case.
		{Name: "params.json", Want: true},                               // Test 3: a flag example's input.
		{Name: "./charts/mySql", Want: true},                            // Test 4: the reader's chart.
		{Name: "./rpms", Want: true},                                    // Test 5: the reader's archives.
		{Name: "/workspace/project", Want: true},                        // Test 6: an absolute path.
		{Name: "/usr/local/share/trivy/templates/html.tpl", Want: true}, // Test 7: a package install's path.
		{Name: "vuln-list-update", Want: true},                          // Test 8: another repository.
		{Name: "../project-feature-search", Want: true},                 // Test 9: above the checkout.
		{Name: "~/.config/trivy.yaml", Want: true},                      // Test 10: the reader's home.
		{Name: "/work/repo/examples/none", Want: false},                 // Test 11: the session's checkout.
		{Name: "params.json", Cwd: "/work/repo/sub", Want: false},       // Test 12: read from elsewhere.
		{Name: "params.json", Roots: map[string]bool{}, Want: false},    // Test 13: no roots known.
		{Name: "./examples", Cwd: "/work/repo", Want: false},            // Test 14: a root itself.
		{Name: "exampels/misconf", Want: false},                         // Test 15: a typo of a root directory.
		{Name: "pkgs", Want: true},                                      // Test 16: short names match exactly.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			r := roots
			if test.Roots != nil {
				r = test.Roots
			}
			if diff := cmp.Diff(test.Want, readerSupplied(test.Name, test.Cwd, r)); diff != "" {
				t.Errorf("readerSupplied(%q) mismatch (-want +got):\n%s", test.Name, diff)
			}
		})
	}
}

// TestReaderSuppliedInRun checks the rule through a whole session, on what
// trivy printed in the 2026-11-15 run. A name the reader supplies is skipped,
// and a name pointing into the checkout stays the document's gap.
func TestReaderSuppliedInRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Line       kplan.PlanLine
		Need       string
		Output     string
		Roots      []string
		WantStatus Status
		WantDetail string
	}{{ // Test 0: a tracked directory's missing child is the document's gap.
		Line:       kplan.PlanLine{Cmd: "trivy config --severity HIGH,CRITICAL examples/misconf/mixed"},
		Output:     "FATAL\tFatal error\trun error: lstat examples/misconf/mixed: no such file or directory",
		Roots:      trivyRoots,
		WantStatus: StatusGap, WantDetail: "references examples/misconf/mixed, which no documented step creates",
	}, { // Test 1: a project path on the reader's machine is skipped.
		Line:       kplan.PlanLine{Cmd: `trivy fs --config="" --ignorefile="" --secret-config="" /workspace/project`},
		Output:     "FATAL\tFatal error\trun error: lstat /workspace/project: no such file or directory",
		Roots:      trivyRoots,
		WantStatus: StatusSkipped, WantDetail: "names /workspace/project, a path on the reader's machine",
	}, { // Test 2: a cd into another repository is skipped.
		Line:       kplan.PlanLine{Cmd: "cd vuln-list-update"},
		Output:     "bash: line 262: cd: vuln-list-update: No such file or directory",
		Roots:      trivyRoots,
		WantStatus: StatusSkipped, WantDetail: "names vuln-list-update, which the reader supplies",
	}, { // Test 3: a planned need the reader brings is skipped.
		Line:       kplan.PlanLine{Cmd: "trivy config --cf-params params.json ./infrastructure/cf", Need: "params.json"},
		Need:       "0",
		Roots:      trivyRoots,
		WantStatus: StatusSkipped, WantDetail: "names params.json, which the reader supplies",
	}, { // Test 4: the same need without roots keeps the old gap.
		Line:       kplan.PlanLine{Cmd: "trivy config --cf-params params.json ./infrastructure/cf", Need: "params.json"},
		Need:       "0",
		WantStatus: StatusGap, WantDetail: "references params.json, which no documented step creates",
	}, { // Test 5: without roots a cd into a missing directory keeps the old gap.
		Line:       kplan.PlanLine{Cmd: "cd vuln-list-update"},
		Output:     "bash: line 262: cd: vuln-list-update: No such file or directory",
		WantStatus: StatusGap, WantDetail: "references vuln-list-update, which no documented step creates",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := &kplan.Plan{Repo: "trivy", Binaries: []string{"trivy"}, Roots: test.Roots,
				Steps: []kplan.PlanStep{{ID: "b1", Doc: "docs/guide.md", Lines: []kplan.PlanLine{test.Line}}}}
			out := []string{"KIBBLE-BUILD CODE=0", "KIBBLE-STEP b1 START"}
			if test.Need != "" {
				out = append(out, "KIBBLE-NEED b1:0 "+test.Need)
			}
			if test.Output != "" {
				out = append(out, test.Output)
			}
			out = append(out, "KIBBLE-LINE b1:0 CODE=1 CWD=/work/repo", "KIBBLE-DONE")
			res := classifyExample(InstallStep{Repo: "trivy", Kind: "example"}, plan, sessionOut(out...),
				nil, 0, lineTimeout, sessionEnd{})
			if res.example == nil {
				t.Fatalf("no per-line outcomes, status %s: %s", res.Status, res.Detail)
			}
			got := res.example.Steps[0].Lines[0]
			if diff := cmp.Diff(test.WantStatus, got.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s\ndetail: %s", diff, got.Detail)
			}
			if !strings.Contains(got.Detail, test.WantDetail) {
				t.Errorf("detail %q does not contain %q", got.Detail, test.WantDetail)
			}
		})
	}
}

// TestConfigKeySetting checks that a line refused for a configuration key the
// documents set in a file reads as a setting the reader supplies. ntfy's 24
// user, access, token, and tier lines failed this way on the 2026-11-22 run,
// because auth-file lives in server.yml and no shell line sets it.
func TestConfigKeySetting(t *testing.T) {
	t.Parallel()
	const ntfyOut = "option database-url or auth-file not set; auth is unconfigured for this server"
	tests := []struct {
		Output     string
		Settings   []string
		WantStatus Status
		WantReason Reason
		WantDetail string
	}{{ // Test 0: the documents set auth-file, so the reader supplies it.
		Output: ntfyOut, Settings: []string{"auth-file"},
		WantStatus: StatusSkipped, WantReason: ReasonMissingFixture,
		WantDetail: "needs auth-file, a configuration key the documents set in a file the reader edits",
	}, { // Test 1: a key the documents never set keeps the line's verdict.
		Output: ntfyOut, WantStatus: StatusFail,
	}, { // Test 2: a missing flag is never read as a key, even one the documents set.
		Output: "error: --auth-file is required", Settings: []string{"auth-file"},
		WantStatus: StatusFail,
	}, { // Test 3: a key named without saying it is missing settles nothing here.
		Output:   "error: opening auth-file /var/lib/ntfy/user.db: database is locked",
		Settings: []string{"auth-file"}, WantStatus: StatusFail,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := &kplan.Plan{Repo: "ntfy", Binaries: []string{"ntfy"}, Settings: test.Settings,
				Steps: []kplan.PlanStep{{ID: "b1", Doc: "docs/config.md",
					Lines: []kplan.PlanLine{{Cmd: "ntfy user list"}}}}}
			out := sessionOut("KIBBLE-BUILD CODE=0", "KIBBLE-STEP b1 START", test.Output,
				"KIBBLE-LINE b1:0 CODE=1 CWD=/work/repo", "KIBBLE-DONE")
			res := classifyExample(InstallStep{Repo: "ntfy", Kind: "example"}, plan, out,
				nil, 0, lineTimeout, sessionEnd{})
			if res.example == nil {
				t.Fatalf("no per-line outcomes, status %s: %s", res.Status, res.Detail)
			}
			got := res.example.Steps[0].Lines[0]
			if diff := cmp.Diff([]string{string(test.WantStatus), string(test.WantReason)},
				[]string{string(got.Status), string(got.Reason)}); diff != "" {
				t.Errorf("verdict mismatch (-want +got):\n%s\ndetail: %s", diff, got.Detail)
			}
			if !strings.Contains(got.Detail, test.WantDetail) {
				t.Errorf("detail %q does not contain %q", got.Detail, test.WantDetail)
			}
		})
	}
}
