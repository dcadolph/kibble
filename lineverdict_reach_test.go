package main

import (
	"fmt"
	"strings"
	"testing"

	kplan "github.com/dcadolph/kibble/internal/plan"
	"github.com/google/go-cmp/cmp"
)

// TestClassifyNewlyReachedLines pins the rules for lines kibble only began to
// run once it could build a tool from its checkout. The outputs are what
// navi, headscale, trivy, and poetry printed in those runs. Each excuse is
// paired with a failure of the same shape that it must not reach.
func TestClassifyNewlyReachedLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantStatus Status
		WantReason Reason
		WantDetail string
		Cmd        string
		Output     string
		Code       int
		NonzeroOK  bool
		OwnProject bool
	}{{ // Test 0: navi saying fzf must be installed is something not installed.
		Cmd: "navi --query \"change branch\" --best-match",
		Output: "navi was unable to call fzf.\n                Please make sure it's correctly installed.\n" +
			"                Refer to https://github.com/junegunn/fzf for more info.\n",
		Code: 33, WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
		WantDetail: "reports something not installed",
	}, { // Test 1: a client that waited out its deadline could not reach a service.
		Cmd:    "headscale nodes list",
		Output: "Error: listing nodes: Get \"http://local/api/v1/node\": context deadline exceeded\n",
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
		WantDetail: "could not reach a service",
	}, { // Test 2: a socket read that timed out is the same refusal.
		Cmd:    "tool status",
		Output: "error: read tcp 10.0.0.2:4411->10.0.0.9:443: i/o timeout\n",
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
	}, { // Test 3: a tool that crashed after a deadline message still crashed.
		Cmd:    "headscale nodes list",
		Output: "context deadline exceeded\npanic: runtime error: invalid memory address or nil pointer dereference\n",
		Code:   2, WantStatus: StatusFail,
	}, { // Test 4: a flag the tool does not know is not excused by the word installed.
		Cmd:    "navi --best-macth",
		Output: "error: unexpected argument '--best-macth' found\n",
		Code:   2, WantStatus: StatusFail,
	}, { // Test 5: a crash word in a table cell is a report, so the documented exit stands.
		Cmd: "trivy image --exit-code 1 python:3.4-alpine3.9", Output: trivyTable,
		Code: 1, NonzeroOK: true, WantStatus: StatusVerified,
		WantDetail: "exit 1 is documented behavior",
	}, { // Test 6: the same table with no documented exit fails, and not as a crash.
		Cmd: "trivy image python:3.4-alpine3.9", Output: trivyTable,
		Code: 1, WantStatus: StatusFail, WantDetail: "exited 1",
	}, { // Test 7: a panic printed bare after the table is the tool dying.
		Cmd:    "trivy image --exit-code 1 python:3.4-alpine3.9",
		Output: trivyTable + "panic: runtime error: index out of range [3] with length 3\n",
		Code:   2, NonzeroOK: true, WantStatus: StatusFail, WantDetail: "crashed with exit 2",
	}, { // Test 8: the shell reporting a segfault bare is a crash whatever the document blesses.
		Cmd: "trivy image --exit-code 1 alpine", Output: "Segmentation fault (core dumped)\n",
		Code: 1, NonzeroOK: true, WantStatus: StatusFail, WantDetail: "crashed with exit 1",
	}, { // Test 9: a traceback a tool quotes behind a bar is the build it ran, not its own death.
		Cmd: "poetry add pendulum@^2.0.5", Output: poetryQuotedBuild,
		Code: 1, OwnProject: true, WantStatus: StatusBlocked, WantReason: ReasonCheckoutState,
		WantDetail: "ran in the repository's own checkout",
	}, { // Test 10: the same traceback printed bare is the tool's own crash.
		Cmd: "poetry add pendulum@^2.0.5", Output: strings.ReplaceAll(poetryQuotedBuild, "    | ", ""),
		Code: 1, OwnProject: true, WantStatus: StatusFail, WantDetail: "crashed with exit 1",
	}, { // Test 11: a cluster context the tool does not have is a name the reader defines.
		Cmd: "trivy k8s --report summary cluster",
		Output: "2026-11-15T21:30:22Z\tFATAL\tFatal error\tfailed getting k8s cluster: " +
			"context \"cluster\" does not exist\n",
		Code: 1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "names a context the reader defines first",
	}, { // Test 12: a subcommand that does not exist is the tool's own vocabulary.
		Cmd: "trivy k9s cluster", Output: "Error: subcommand \"k9s\" does not exist\n",
		Code: 1, WantStatus: StatusFail,
	}, { // Test 13: a plugin with no build for this platform settles nothing.
		Cmd: "trivy plugin install github.com/aquasecurity/trivy-plugin-kubectl", Output: noPlatform,
		Code: 1, WantStatus: StatusBlocked, WantReason: ReasonOtherPlatform,
		WantDetail: "found nothing for this platform",
	}, { // Test 14: and is not read as the exit the document asked for.
		Cmd: "trivy plugin run kubectl pod web -- --exit-code 1", Output: noPlatform,
		Code: 1, NonzeroOK: true, WantStatus: StatusBlocked, WantReason: ReasonOtherPlatform,
	}, { // Test 15: a plugin that is simply not found is still a failure.
		Cmd:    "trivy plugin install github.com/aquasecurity/trivy-plugin-kubectl",
		Output: "FATAL\tFatal error\tplugin install error: plugin not found\n",
		Code:   1, WantStatus: StatusFail,
	}, { // Test 16: an uninstaller finding nothing to remove reports something not installed.
		Cmd:    "curl -sSL https://install.python-poetry.org | POETRY_UNINSTALL=1 python3 -",
		Output: "Poetry is not currently installed.\n",
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
		WantDetail: "reports something not installed",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			lr := lineResult{Cmd: test.Cmd, Code: -1}
			o := lineOutcome{code: test.Code, output: test.Output, ownProject: test.OwnProject}
			if test.OwnProject {
				o.cwd = sessionRepoDir
			}
			line := kplan.PlanLine{Cmd: test.Cmd, NonzeroOK: test.NonzeroOK}
			got := classifyLineResult(lr, line, o, false, nil, lineTimeout)
			if diff := cmp.Diff(test.WantStatus, got.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s\ndetail: %s", diff, got.Detail)
			}
			if diff := cmp.Diff(test.WantReason, got.Reason); diff != "" {
				t.Errorf("reason mismatch (-want +got):\n%s", diff)
			}
			if !strings.Contains(got.Detail, test.WantDetail) {
				t.Errorf("detail = %q, want it to contain %q", got.Detail, test.WantDetail)
			}
		})
	}
}

// Outputs of the newly reached lines, cut to the rows that decide them.
const (
	// trivyTable is a vulnerability table whose one row quotes a crash word.
	trivyTable = "python:3.4-alpine3.9 (alpine 3.9.2)\n" +
		"┌─────────┬───────────────┬──────────────────────────────────────────────────────────────┐\n" +
		"│ openssl │ CVE-2020-1967 │ openssl: Segmentation fault in SSL_check_chain causes denial │\n" +
		"│         │               │ https://avd.aquasec.com/nvd/cve-2020-1967                    │\n" +
		"└─────────┴───────────────┴──────────────────────────────────────────────────────────────┘\n"
	// poetryQuotedBuild is poetry reporting a dependency whose build backend died.
	poetryQuotedBuild = "  - Installing pendulum (2.1.2)\n\nPEP517 build of a dependency failed\n\n" +
		"Backend subprocess exited when trying to invoke build_wheel\n\n" +
		"    | Traceback (most recent call last):\n" +
		"    |   File \"/tmp/tmp3i3k32w4/pendulum-2.1.2/build.py\", line 5, in <module>\n" +
		"    |     from distutils.command.build_ext import build_ext\n" +
		"    | ModuleNotFoundError: No module named 'distutils'\n\n" +
		"Note: This error originates from the build backend, and is likely not a problem with poetry\n\n" +
		"You can verify this by running pip wheel --no-cache-dir --use-pep517 \"pendulum (==2.1.2)\".\n"
	// noPlatform is trivy refusing a plugin that has no build for the container.
	noPlatform = "2026-11-15T21:33:58Z\tFATAL\tFatal error\tplugin install error: failed to install " +
		"the plugin: platform selection error: platform not found\n"
)

// TestFailureNamingUnrunBlock checks that a failure telling the reader to run
// a command the documents show, in a block the plan left out, is blocked
// behind that command, and that nothing weaker than the tool asking for it
// pairs the two.
func TestFailureNamingUnrunBlock(t *testing.T) {
	t.Parallel()
	const nix = "Warning: golangci-lint not found. Run 'nix develop' to ensure all dependencies are available."
	tests := []struct {
		Unrun      []string
		Output     string
		WantStatus Status
		WantDetail string
	}{{ // Test 0: the tool asks for a command the documents show and kibble left out.
		Unrun: []string{"nix develop"}, Output: nix, WantStatus: StatusBlocked,
		WantDetail: "failed naming `nix develop`, which the documents show in a block kibble did not run",
	}, { // Test 1: the same output with no such block in the documents is a failure.
		Output: nix, WantStatus: StatusFail,
	}, { // Test 2: a different command left out does not pair.
		Unrun: []string{"nix build"}, Output: nix, WantStatus: StatusFail,
	}, { // Test 3: a hint pointing at help is advice, not a prerequisite.
		Unrun: []string{"nix develop"}, Output: "error: bad target. See nix develop --help for usage.",
		WantStatus: StatusFail,
	}, { // Test 4: the command inside a longer token is not the command.
		Unrun: []string{"nix develop"}, Output: "error: must run my-nix developer-mode first",
		WantStatus: StatusFail,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			run := &exampleRun{Steps: []exampleStep{{ID: "b1", Doc: "README.md", Lines: []lineResult{
				{Cmd: "make test", Status: StatusFail, Detail: "exited 2", output: test.Output},
			}}}}
			resolveDependentFailures(run, &kplan.Plan{Binaries: []string{"tool"}, Unrun: test.Unrun})
			got := run.Steps[0].Lines[0]
			if diff := cmp.Diff(test.WantStatus, got.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s\ndetail: %s", diff, got.Detail)
			}
			if test.WantDetail != "" && got.Detail != test.WantDetail {
				t.Errorf("detail = %q, want %q", got.Detail, test.WantDetail)
			}
		})
	}
}

// TestFailureAfterStoppedScaffold checks that a failure of a documented tool
// is blocked when an earlier line of its document was to create the project
// and the checkout stopped it by already holding one, as goreleaser's quick
// start does, and that nothing short of that pairs the two.
func TestFailureAfterStoppedScaffold(t *testing.T) {
	t.Parallel()
	const pro = "you are using a GoReleaser Pro configuration file with GoReleaser OSS"
	stopped := lineResult{
		Cmd: "goreleaser init", Status: StatusBlocked, Reason: ReasonCheckoutState,
		output: "command failed error=.goreleaser.yaml already exists",
	}
	tests := []struct {
		Earlier    lineResult
		EarlierDoc string
		Cmd        string
		Output     string
		Status     Status
		Code       int
		WantStatus Status
		WantDetail string
	}{{ // Test 0: a build after the stopped init read the checkout's own project.
		Earlier: stopped, Cmd: "goreleaser build --single-target --clean", Output: pro,
		WantStatus: StatusBlocked, WantDetail: "follows `goreleaser init`, which the repository's own files stopped",
	}, { // Test 1: a flag the tool does not know is its answer to the line as written.
		Earlier: stopped, Cmd: "goreleaser build --single-traget", Output: "unknown flag: --single-traget",
		WantStatus: StatusFail,
	}, { // Test 2: a crash stands whatever the line read.
		Earlier: stopped, Cmd: "goreleaser build", Output: "panic: runtime error: nil map\n",
		WantStatus: StatusFail,
	}, { // Test 3: another program's failure is not the scaffolded tool's.
		Earlier: stopped, Cmd: "make release", Output: pro, WantStatus: StatusFail,
	}, { // Test 4: an init that passed set the project up, so the failure stands.
		Earlier: lineResult{Cmd: "goreleaser init", Status: StatusVerified},
		Cmd:     "goreleaser build", Output: pro, WantStatus: StatusFail,
	}, { // Test 5: an init blocked for another reason is not a stopped scaffold.
		Earlier: lineResult{
			Cmd: "goreleaser init", Status: StatusBlocked, Reason: ReasonMissingFixture,
			output: "needs git history",
		},
		Cmd: "goreleaser build", Output: pro, WantStatus: StatusFail,
	}, { // Test 6: an init stopped in another document says nothing about this one.
		Earlier: stopped, EarlierDoc: "docs/other.md", Cmd: "goreleaser build", Output: pro,
		WantStatus: StatusFail,
	}, { // Test 7: a documented nonzero exit after the stopped init answered the checkout's project.
		Earlier: stopped, Cmd: "goreleaser check", Output: pro, Status: StatusVerified, Code: 1,
		WantStatus: StatusBlocked, WantDetail: "so its exit 1 answered the checkout's project",
	}, { // Test 8: the same documented exit after an init that passed stands.
		Earlier: lineResult{Cmd: "goreleaser init", Status: StatusVerified}, Cmd: "goreleaser check",
		Output: pro, Status: StatusVerified, Code: 1, WantStatus: StatusVerified,
	}, { // Test 9: a zero exit after the stopped init is a pass the rule does not reach.
		Earlier: stopped, Cmd: "goreleaser --help", Status: StatusVerified, Code: 0,
		WantStatus: StatusVerified,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			doc := "docs/quick-start.md"
			if test.Status == "" {
				test.Status, test.Code = StatusFail, 1
			}
			if test.EarlierDoc == "" {
				test.EarlierDoc = doc
			}
			run := &exampleRun{Steps: []exampleStep{
				{ID: "b1", Doc: test.EarlierDoc, Lines: []lineResult{test.Earlier}},
				{ID: "b2", Doc: doc, Lines: []lineResult{
					{Cmd: test.Cmd, Status: test.Status, Code: test.Code, Detail: "exited 1", output: test.Output},
				}},
			}}
			resolveDependentFailures(run, &kplan.Plan{Binaries: []string{"goreleaser"}})
			got := run.Steps[1].Lines[0]
			if diff := cmp.Diff(test.WantStatus, got.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s\ndetail: %s", diff, got.Detail)
			}
			if !strings.Contains(got.Detail, test.WantDetail) {
				t.Errorf("detail = %q, want it to contain %q", got.Detail, test.WantDetail)
			}
		})
	}
}

// TestCrashAfterToolGone checks that a crash after which the documented tool no
// longer starts is blocked as an environment change, read end to end from the
// session's markers, and that a crash the tool survives still fails. The
// output is poetry's after an earlier document turned off virtualenv creation
// and `poetry sync --only dev` removed its own dependencies.
func TestCrashAfterToolGone(t *testing.T) {
	t.Parallel()
	const cleo = "Traceback (most recent call last):\n  File \"/opt/kibble-tool/bin/poetry\", line 5, in <module>\n" +
		"ModuleNotFoundError: No module named 'cleo'"
	tests := []struct {
		Probe      string
		Output     string
		WantStatus Status
		WantReason Reason
	}{{ // Test 0: the probe failed after the crash, so the environment changed.
		Probe: "KIBBLE-PROBE b1:1 CODE=1", Output: cleo,
		WantStatus: StatusBlocked, WantReason: ReasonEnvironmentChanged,
	}, { // Test 1: the tool still starts, so the crash convicts.
		Probe: "KIBBLE-PROBE b1:1 CODE=0", Output: cleo, WantStatus: StatusFail,
	}, { // Test 2: no probe ran, as for a tool whose --version failed at the start.
		Output: cleo, WantStatus: StatusFail,
	}, { // Test 3: a plain nonzero exit with the tool gone is not a crash, and fails.
		Probe: "KIBBLE-PROBE b1:1 CODE=1", Output: "error: the lock file is out of date",
		WantStatus: StatusFail,
	}, { // Test 4: a probe naming another line leaves this one alone.
		Probe: "KIBBLE-PROBE b1:0 CODE=1", Output: cleo, WantStatus: StatusFail,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			out := []string{"KIBBLE-BUILD CODE=0", "KIBBLE-STEP b1 START", "KIBBLE-LINE b1:0 CODE=0",
				test.Output, "KIBBLE-LINE b1:1 CODE=1"}
			if test.Probe != "" {
				out = append(out, test.Probe)
			}
			out = append(out, "KIBBLE-LINE b1:2 CODE=0", "KIBBLE-DONE")
			plan := examplePlan()
			res := classifyExample(InstallStep{Repo: "repo", Kind: "example", plan: plan}, plan,
				sessionOut(out...), nil, 0, lineTimeout, sessionEnd{})
			if res.example == nil || len(res.example.Steps) == 0 || len(res.example.Steps[0].Lines) < 2 {
				t.Fatalf("no line results: %+v", res)
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

// TestFailureOnUnresolvedHost checks that a request to a host that did not
// resolve is blocked when an earlier line of its document named that host and
// did not pass, as tailscale's Kubernetes guide does with services its skipped
// kubectl lines set up, and that a dead host with no such line still fails.
func TestFailureOnUnresolvedHost(t *testing.T) {
	t.Parallel()
	const curl = "curl: (6) Could not resolve host: nginx"
	skipped := lineResult{Cmd: "kubectl logs nginx ts-sidecar", Status: StatusSkipped,
		Reason: ReasonMissingDependency}
	tests := []struct {
		Earlier    lineResult
		EarlierDoc string
		Cmd        string
		Output     string
		WantStatus Status
		WantDetail string
	}{{ // Test 0: the service a skipped line named has nothing to resolve to.
		Earlier: skipped, Cmd: "curl http://nginx", Output: curl, WantStatus: StatusBlocked,
		WantDetail: "could not resolve host nginx, which `kubectl logs nginx ts-sidecar` also named",
	}, { // Test 1: a Go client's lookup error with a port reads the same.
		Earlier: lineResult{Cmd: "make proxy | kubectl apply -f-", Status: StatusSkipped},
		Cmd:     "tool get http://proxy:8080/health", Output: "dial tcp: lookup proxy: no such host",
		WantStatus: StatusBlocked, WantDetail: "could not resolve host proxy",
	}, { // Test 2: a line that named the host and passed leaves the failure standing.
		Earlier: lineResult{Cmd: "kubectl logs nginx ts-sidecar", Status: StatusVerified},
		Cmd:     "curl http://nginx", Output: curl, WantStatus: StatusFail,
	}, { // Test 3: a dead domain no earlier line names is a real break.
		Earlier: skipped, Cmd: "curl https://downloads.example.org/tool.tgz",
		Output:     "curl: (6) Could not resolve host: downloads.example.org",
		WantStatus: StatusFail,
	}, { // Test 4: a refused connection is not a name that failed to resolve.
		Earlier: skipped, Cmd: "curl http://nginx",
		Output:     "curl: (7) Failed to connect to nginx port 80: Connection refused",
		WantStatus: StatusFail,
	}, { // Test 5: the host inside a longer word is not the host.
		Earlier: lineResult{Cmd: "kubectl logs nginx-proxy", Status: StatusSkipped},
		Cmd:     "curl http://nginx", Output: curl, WantStatus: StatusFail,
	}, { // Test 6: a skipped line in another document says nothing about this one.
		Earlier: skipped, EarlierDoc: "docs/other.md", Cmd: "curl http://nginx", Output: curl,
		WantStatus: StatusFail,
	}, { // Test 7: the earlier line's program is not a name it set up.
		Earlier: lineResult{Cmd: "nginx -t", Status: StatusSkipped},
		Cmd:     "curl http://nginx", Output: curl, WantStatus: StatusFail,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			doc := "docs/k8s/README.md"
			if test.EarlierDoc == "" {
				test.EarlierDoc = doc
			}
			run := &exampleRun{Steps: []exampleStep{
				{ID: "b1", Doc: test.EarlierDoc, Lines: []lineResult{test.Earlier}},
				{ID: "b2", Doc: doc, Lines: []lineResult{
					{Cmd: test.Cmd, Status: StatusFail, Code: 6, Detail: "exited 6", output: test.Output},
				}},
			}}
			resolveDependentFailures(run, &kplan.Plan{Binaries: []string{"tailscale"}})
			got := run.Steps[1].Lines[0]
			if diff := cmp.Diff(test.WantStatus, got.Status); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s\ndetail: %s", diff, got.Detail)
			}
			if !strings.Contains(got.Detail, test.WantDetail) {
				t.Errorf("detail = %q, want it to contain %q", got.Detail, test.WantDetail)
			}
		})
	}
}

// TestInheritedAcrossDetails checks that a collapsed details element is a
// section of its own, so a setup that ran in the directory a sibling setup
// under the same heading entered is blocked, read end to end from the
// session's markers. glance's README offers a template setup that enters a
// directory beside a manual one that then met the template's files.
func TestInheritedAcrossDetails(t *testing.T) {
	t.Parallel()
	const exists = "mkdir: cannot create directory 'config': File exists"
	tests := []struct {
		First, Second int
		WantStatus    Status
		WantReason    Reason
	}{{ // Test 0: sibling details under one heading are separate sections.
		First: 1, Second: 2, WantStatus: StatusBlocked, WantReason: ReasonInheritedState,
	}, { // Test 1: both blocks in one details element are one section that entered it.
		First: 1, Second: 1, WantStatus: StatusFail,
	}, { // Test 2: outside any details, one heading is one section as before.
		First: 0, Second: 0, WantStatus: StatusFail,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := &kplan.Plan{Repo: "glance", Steps: []kplan.PlanStep{
				{ID: "b1", Doc: "README.md", Heading: "Installation", Details: test.First,
					Lines: []kplan.PlanLine{{Cmd: "mkdir glance && cd glance"}}},
				{ID: "b2", Doc: "README.md", Heading: "Installation", Details: test.Second,
					Lines: []kplan.PlanLine{{Cmd: "mkdir config"}}},
			}}
			out := sessionOut("KIBBLE-BUILD CODE=0",
				"KIBBLE-STEP b1 START", "KIBBLE-LINE b1:0 CODE=0 CWD=/work/repo/glance",
				"KIBBLE-STEP b2 START", exists, "KIBBLE-LINE b2:0 CODE=1 CWD=/work/repo/glance",
				"KIBBLE-DONE")
			res := classifyExample(InstallStep{Repo: "glance", Kind: "example"}, plan, out, nil, 0,
				lineTimeout, sessionEnd{})
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
		})
	}
}
