package main

import (
	"fmt"
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
