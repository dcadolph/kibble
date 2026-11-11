package main

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	kplan "github.com/dcadolph/kibble/internal/plan"
)

// TestSuppressionNeedsMoreThanAMention feeds resolveDependentFailures the
// shapes a textual dependency check gets wrong. Suppression here is safe by
// construction, since a suppressed line lands on BLOCKED rather than being
// excused, but a failure turned into BLOCKED on a coincidence is still a real
// documentation break that stopped being reported as one. These cases record
// which coincidences currently reach that far.
func TestSuppressionNeedsMoreThanAMention(t *testing.T) {
	t.Parallel()

	plan := &kplan.Plan{Binaries: []string{"tool"}}

	tests := []struct {
		Name    string
		Skipped lineResult
		Failing lineResult
		Want    Status
	}{{ // Test 0: the honest case. The failure names the skipped command as
		// the thing it needed, and the cascade should stop being reported.
		Name:    "genuine prerequisite",
		Skipped: lineResult{Cmd: "tool setup", Status: StatusSkipped},
		Failing: lineResult{Cmd: "tool deploy", Status: StatusFail,
			output: "error: run tool setup first"},
		Want: StatusBlocked,
	}, { // Test 1: the skipped command is named inside a URL, not as a cause.
		Name:    "named in a url",
		Skipped: lineResult{Cmd: "tool setup", Status: StatusSkipped},
		Failing: lineResult{Cmd: "tool deploy", Status: StatusFail,
			output: "error: bad flag. See https://example.com/tool setup/guide"},
		Want: StatusFail,
	}, { // Test 2: the failure is unrelated and merely echoes the argument the
		// document gave it, which happens to spell a skipped command.
		Name:    "echoed argument",
		Skipped: lineResult{Cmd: "tool setup", Status: StatusSkipped},
		Failing: lineResult{Cmd: "tool deploy", Status: StatusFail,
			output: `error: unknown value "tool setup" for --mode`},
		Want: StatusFail,
	}, { // Test 3: a help hint pointing at another subcommand is advice, not a
		// prerequisite the failing line depended on.
		Name:    "help hint",
		Skipped: lineResult{Cmd: "unrelated thing", Status: StatusSkipped},
		Failing: lineResult{Cmd: "tool deploy", Status: StatusFail,
			output: "error: bad flag\nTry tool help for usage."},
		Want: StatusFail,
	}, { // Test 4: air's skipped go get would have provided the tool the next
		// line reports absent.
		Name: "skipped provider",
		Skipped: lineResult{Cmd: "go get -tool github.com/air-verse/air@latest", Status: StatusSkipped,
			Reason: ReasonMissingFixture},
		Failing: lineResult{Cmd: "go tool air -v", Status: StatusFail,
			output: `go: no such tool "air"`},
		Want: StatusBlocked,
	}, { // Test 5: the same skip does not excuse an unrelated failure.
		Name: "skipped provider, unrelated failure",
		Skipped: lineResult{Cmd: "go get -tool github.com/air-verse/air@latest", Status: StatusSkipped,
			Reason: ReasonMissingFixture},
		Failing: lineResult{Cmd: "go tool air -v", Status: StatusFail,
			output: "open /work/repo/.air.toml: permission denied"},
		Want: StatusFail,
	}, { // Test 6: a placeholder skip provides nothing.
		Name: "placeholder skip",
		Skipped: lineResult{Cmd: "go get -tool github.com/<owner>/air@latest", Status: StatusSkipped,
			Reason: ReasonPlaceholder},
		Failing: lineResult{Cmd: "go tool air -v", Status: StatusFail,
			output: `go: no such tool "air"`},
		Want: StatusFail,
	}, { // Test 7: a name that appears only inside a URL of the skipped line
		// was not provided by it.
		Name: "provider named in a url",
		Skipped: lineResult{Cmd: "curl -o x https://example.com/air", Status: StatusSkipped,
			Reason: ReasonMissingDependency},
		Failing: lineResult{Cmd: "go tool air -v", Status: StatusFail,
			output: `go: no such tool "air"`},
		Want: StatusFail,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d %s", testNum, test.Name), func(t *testing.T) {
			t.Parallel()
			run := &exampleRun{Steps: []exampleStep{{
				ID:    "b1",
				Lines: []lineResult{test.Skipped, test.Failing},
			}}}
			resolveDependentFailures(run, plan)
			got := run.Steps[0].Lines[1].Status
			if got != test.Want {
				t.Errorf("status = %s, want %s (detail %q)",
					got, test.Want, run.Steps[0].Lines[1].Detail)
			}
		})
	}
}

// TestRepeatedSubcommandIsIndependent checks the family rule. Two documented
// invocations of the same subcommand are usually separate examples, so the
// second failing after the first was skipped is not evidence the first caused
// it.
func TestRepeatedSubcommandIsIndependent(t *testing.T) {
	t.Parallel()

	run := &exampleRun{Steps: []exampleStep{{
		ID: "b1",
		Lines: []lineResult{
			{Cmd: "tool build --debug", Status: StatusSkipped},
			{Cmd: "tool build --release", Status: StatusFail, output: "error: bad flag --release"},
		},
	}}}
	resolveDependentFailures(run, &kplan.Plan{Binaries: []string{"tool"}})
	if got := run.Steps[0].Lines[1].Status; got != StatusFail {
		t.Errorf("status = %s, want FAIL (detail %q)", got, run.Steps[0].Lines[1].Detail)
	}
}

// TestSharedOperandAfterLineNotPassed checks the shared-operand rule. A query
// on a file an earlier line of the same document named and did not pass is
// blocked behind that line, and nothing weaker than a shared file pairs them.
func TestSharedOperandAfterLineNotPassed(t *testing.T) {
	t.Parallel()
	const query = "Error: no such table: dogs"
	tests := []struct {
		Steps      []exampleStep
		WantStatus []Status
		WantDetail string
	}{{ // Test 0: Queries after a blocked insert on the same database are blocked.
		Steps: []exampleStep{{ID: "b1", Doc: "README.md", Lines: []lineResult{
			{Cmd: "dbtool insert dogs.db dogs dogs.csv --csv", Status: StatusBlocked},
			{Cmd: `dbtool dogs.db "select id, name from dogs"`, Status: StatusFail, output: query},
			{Cmd: `dbtool dogs.db "select * from dogs" --csv`, Status: StatusFail, output: query},
			{Cmd: `dbtool dogs.db "select * from dogs" --table`, Status: StatusFail, output: query},
		}}},
		WantStatus: []Status{StatusBlocked, StatusBlocked, StatusBlocked, StatusBlocked},
		WantDetail: "names dogs.db, which `dbtool insert dogs.db dogs dogs.csv --csv` also named and did not pass",
	}, { // Test 1: The same queries after a verified insert stay failures.
		Steps: []exampleStep{{ID: "b1", Doc: "README.md", Lines: []lineResult{
			{Cmd: "dbtool insert dogs.db dogs dogs.csv --csv", Status: StatusVerified},
			{Cmd: `dbtool dogs.db "select id, name from dogs"`, Status: StatusFail, output: query},
			{Cmd: `dbtool dogs.db "select * from dogs" --csv`, Status: StatusFail, output: query},
		}}},
		WantStatus: []Status{StatusVerified, StatusFail, StatusFail},
	}, { // Test 2: A shared bare word is not a file, so it does not pair.
		Steps: []exampleStep{{ID: "b1", Doc: "README.md", Lines: []lineResult{
			{Cmd: "dbtool build release", Status: StatusBlocked},
			{Cmd: "dbtool test release", Status: StatusFail, output: "error: test failed"},
		}}},
		WantStatus: []Status{StatusBlocked, StatusFail},
	}, { // Test 3: A shared file in a different document does not pair.
		Steps: []exampleStep{
			{ID: "b1", Doc: "docs/load.md", Lines: []lineResult{
				{Cmd: "dbtool insert dogs.db dogs dogs.csv --csv", Status: StatusBlocked},
			}},
			{ID: "b2", Doc: "README.md", Lines: []lineResult{
				{Cmd: `dbtool dogs.db "select * from dogs"`, Status: StatusFail, output: query},
			}},
		},
		WantStatus: []Status{StatusBlocked, StatusFail},
	}, { // Test 4: A flag's value counts, and a step earlier in the document pairs.
		Steps: []exampleStep{
			{ID: "b1", Doc: "README.md", Lines: []lineResult{
				{Cmd: "dbtool load --file=data/dogs.db", Status: StatusSkipped},
			}},
			{ID: "b2", Doc: "README.md", Lines: []lineResult{
				{Cmd: "dbtool query --db data/dogs.db", Status: StatusFail, output: query},
			}},
		},
		WantStatus: []Status{StatusSkipped, StatusBlocked},
		WantDetail: "names data/dogs.db, which `dbtool load --file=data/dogs.db` also named and did not pass",
	}, { // Test 5: A URL and a query string holding a dot are not files.
		Steps: []exampleStep{{ID: "b1", Doc: "README.md", Lines: []lineResult{
			{Cmd: `dbtool fetch https://example.com/x "select a.b from t"`, Status: StatusBlocked},
			{Cmd: `dbtool show https://example.com/x "select a.b from t"`, Status: StatusFail, output: "error"},
		}}},
		WantStatus: []Status{StatusBlocked, StatusFail},
	}, { // Test 6: Two runs of one script with different arguments are separate.
		Steps: []exampleStep{{ID: "b1", Doc: "README.md", Lines: []lineResult{
			{Cmd: "python3 scripts/bench.py --engine both", Status: StatusBlocked},
			{Cmd: "python3 scripts/bench.py --engine one", Status: StatusFail, output: "error"},
		}}},
		WantStatus: []Status{StatusBlocked, StatusFail},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			run := &exampleRun{Steps: test.Steps}
			resolveDependentFailures(run, &kplan.Plan{Binaries: []string{"dbtool"}})
			var got []Status
			last := ""
			for _, s := range run.Steps {
				for _, l := range s.Lines {
					got = append(got, l.Status)
					if l.Reason == ReasonDependsOnSkipped {
						last = l.Detail
					}
				}
			}
			if diff := cmp.Diff(test.WantStatus, got); diff != "" {
				t.Errorf("status mismatch (-want +got):\n%s", diff)
			}
			if test.WantDetail != "" && last != test.WantDetail {
				t.Errorf("detail = %q, want %q", last, test.WantDetail)
			}
		})
	}
}

// TestMixedPackagesInCheckout checks the rule for a document's Go file written
// beside the repository's own package. Go refusing to build two packages in
// the checkout's root is the session's doing only when a file block of the
// document wrote one of the two files and the checkout held the other before
// the session started.
//
//nolint:funlen // Test function.
func TestMixedPackagesInCheckout(t *testing.T) {
	t.Parallel()
	const (
		write  = "cat > 'tutorial.go' <<'KIBBLE_FILE_END'"
		mixed  = "found packages sqlc (gomod_test.go) and main (tutorial.go) in /work/repo"
		build  = "go build ."
		nested = "mkdir -p 'cmd/app' && cat > 'cmd/app/tutorial.go' <<'KIBBLE_FILE_END'"
	)
	wrote := lineResult{Cmd: write, Status: StatusVerified, cwd: sessionRepoDir}
	failed := lineResult{Cmd: build, Status: StatusFail, Detail: "exited 1: " + mixed, output: mixed}
	held := []string{"doc.go", "gomod_test.go"}
	one := func(lines ...lineResult) []exampleStep {
		return []exampleStep{{ID: "b1", Doc: "README.md", Lines: lines}}
	}
	tests := []struct {
		Steps      []exampleStep
		Checkout   []string
		Target     string
		WantStatus Status
		WantReason Reason
		WantDetail string
	}{{ // Test 0: The document's file landed beside the checkout's own package.
		Steps: one(wrote, failed), Checkout: held,
		WantStatus: StatusBlocked, WantReason: ReasonCheckoutState,
		WantDetail: "wrote tutorial.go into the repository's own checkout, beside gomod_test.go of " +
			"another Go package, so the claim was not tested as written: exited 1: " + mixed,
	}, { // Test 1: Neither file is the document's, so the failure stands.
		Steps: one(lineResult{Cmd: "cat > 'notes.txt' <<'KIBBLE_FILE_END'", Status: StatusVerified,
			cwd: sessionRepoDir}, failed),
		Checkout:   held,
		WantStatus: StatusFail,
	}, { // Test 2: The document wrote both files, so the clash is its own.
		Steps: one(lineResult{Cmd: "cat > 'gomod_test.go' <<'KIBBLE_FILE_END'", Status: StatusVerified,
			cwd: sessionRepoDir}, wrote, failed),
		Checkout:   held,
		WantStatus: StatusFail,
	}, { // Test 3: A directory outside the checkout holds no package of the repository.
		Steps: one(wrote, lineResult{Cmd: build, Status: StatusFail,
			output: "found packages sqlc (gomod_test.go) and main (tutorial.go) in /tmp/app"}),
		Checkout:   held,
		WantStatus: StatusFail,
	}, { // Test 4: A directory the session made inside the checkout is not the checkout's package.
		Steps: one(wrote, lineResult{Cmd: build, Status: StatusFail,
			output: "found packages db (db.go) and main (tutorial.go) in /work/repo/app"}),
		Checkout:   []string{"db.go"},
		WantStatus: StatusFail,
	}, { // Test 5: A write that was skipped put nothing in the checkout.
		Steps:      one(lineResult{Cmd: write, Status: StatusSkipped, cwd: sessionRepoDir}, failed),
		Checkout:   held,
		WantStatus: StatusFail,
	}, { // Test 6: A write after the failure did not cause it.
		Steps: one(failed, wrote), Checkout: held,
		WantStatus: StatusFail,
	}, { // Test 7: A file written into a subdirectory is not the one Go names at the root.
		Steps:      one(lineResult{Cmd: nested, Status: StatusVerified, cwd: sessionRepoDir}, failed),
		Checkout:   held,
		WantStatus: StatusFail,
	}, { // Test 8: Any other Go build error after the write stays a failure.
		Steps: one(wrote, lineResult{Cmd: build, Status: StatusFail,
			output: "./tutorial.go:9:2: undefined: tutorial"}),
		Checkout:   held,
		WantStatus: StatusFail,
	}, { // Test 9: A second file the checkout never held is the document's doing.
		Steps: one(wrote, lineResult{Cmd: "sqlc generate", Status: StatusVerified, cwd: sessionRepoDir},
			lineResult{Cmd: build, Status: StatusFail,
				output: "found packages main (tutorial.go) and tutorial (models.go) in /work/repo"}),
		Checkout:   held,
		WantStatus: StatusFail,
	}, { // Test 10: A file a shell line wrote is not a file block's, and not the checkout's.
		Steps: one(wrote, lineResult{Cmd: "cat > models.go <<EOF", Status: StatusVerified,
			cwd: sessionRepoDir}, lineResult{Cmd: build, Status: StatusFail,
			output: "found packages main (tutorial.go) and models (models.go) in /work/repo"}),
		Checkout:   held,
		WantStatus: StatusFail,
	}, { // Test 11: A checkout with no Go file at its top has no package to clash with.
		Steps:      one(wrote, failed),
		WantStatus: StatusFail,
	}, { // Test 12: The write and the build sit in separate blocks, as the planner emits them.
		Steps: []exampleStep{
			{ID: "b29", Doc: "docs/tutorials/getting-started.md", Lines: []lineResult{wrote}},
			{ID: "b30", Doc: "docs/tutorials/getting-started.md", Lines: []lineResult{failed}},
		},
		Checkout:   held,
		WantStatus: StatusBlocked, WantReason: ReasonCheckoutState,
	}, { // Test 13: Go names the document's file first when it sorts first.
		Steps: one(lineResult{Cmd: "cat > 'main.go' <<'KIBBLE_FILE_END'", Status: StatusVerified,
			cwd: sessionRepoDir}, lineResult{Cmd: build, Status: StatusFail, Detail: "exited 1",
			output: "found packages main (main.go) and zlib (zlib.go) in /work/repo"}),
		Checkout:   []string{"zlib.go"},
		WantStatus: StatusBlocked, WantReason: ReasonCheckoutState,
		WantDetail: "wrote main.go into the repository's own checkout, beside zlib.go of " +
			"another Go package, so the claim was not tested as written: exited 1",
	}, { // Test 14: An earlier document's file is gone once the checkout is restored.
		Steps: []exampleStep{
			{ID: "b1", Doc: "docs/a.md", Lines: []lineResult{wrote}},
			{ID: "b2", Doc: "docs/b.md", Lines: []lineResult{failed}},
		},
		Checkout:   held,
		WantStatus: StatusFail,
	}, { // Test 15: A file written after entering a directory is not at the root.
		Steps:      one(lineResult{Cmd: write, Status: StatusVerified, cwd: "/work/repo/app"}, failed),
		Checkout:   held,
		WantStatus: StatusFail,
	}, { // Test 16: A write whose directory the session did not report proves nothing.
		Steps:      one(lineResult{Cmd: write, Status: StatusVerified}, failed),
		Checkout:   held,
		WantStatus: StatusFail,
	}, { // Test 17: A write that failed put nothing reliable in the checkout.
		Steps:      one(lineResult{Cmd: write, Status: StatusFail, cwd: sessionRepoDir}, failed),
		Checkout:   held,
		WantStatus: StatusFail,
	}, { // Test 18: A write that was blocked did not run as written either.
		Steps:      one(lineResult{Cmd: write, Status: StatusBlocked, cwd: sessionRepoDir}, failed),
		Checkout:   held,
		WantStatus: StatusFail,
	}, { // Test 19: A later line failing for its own reason is not excused by the blocked build.
		Steps: one(wrote, failed, lineResult{Cmd: "docker build .", Status: StatusFail,
			output: "ERROR: failed to solve: dockerfile parse error on line 3"}),
		Checkout:   held,
		Target:     "docker build .",
		WantStatus: StatusFail,
	}, { // Test 20: A numbered terminator marks a file block all the same.
		Steps: one(lineResult{Cmd: "cat > 'tutorial.go' <<'KIBBLE_FILE_END_1'", Status: StatusVerified,
			cwd: sessionRepoDir}, failed),
		Checkout:   held,
		WantStatus: StatusBlocked, WantReason: ReasonCheckoutState,
	}, { // Test 21: Go prefixes the importing position when the root is reached by an import.
		Steps: one(wrote, lineResult{Cmd: build, Status: StatusFail,
			output: "cmd/tool/main.go:3:8: " + mixed}),
		Checkout:   held,
		WantStatus: StatusBlocked, WantReason: ReasonCheckoutState,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			steps := make([]exampleStep, len(test.Steps))
			for i, s := range test.Steps {
				steps[i] = exampleStep{ID: s.ID, Doc: s.Doc, Lines: slices.Clone(s.Lines)}
			}
			run := &exampleRun{Steps: steps}
			resolveDependentFailures(run, &kplan.Plan{RootGoFiles: test.Checkout})
			target := test.Target
			if target == "" {
				target = build
			}
			var got lineResult
			for _, s := range run.Steps {
				for _, l := range s.Lines {
					if l.Cmd == target {
						got = l
					}
				}
			}
			if got.Status != test.WantStatus || got.Reason != test.WantReason {
				t.Errorf("got %s %q, want %s %q (detail %q)",
					got.Status, got.Reason, test.WantStatus, test.WantReason, got.Detail)
			}
			if test.WantDetail != "" && got.Detail != test.WantDetail {
				t.Errorf("detail = %q, want %q", got.Detail, test.WantDetail)
			}
		})
	}
}
