package plan

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestMissingGlobExcluded checks that a glob given to a flag that leaves
// files out is not a file the line needs. trivy's skipping guide showed
// `--skip-files "./testdata/*/bar"` and kibble read it as a gap, though a
// pattern of what to skip needs nothing to exist.
func TestMissingGlobExcluded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Cmd      string
		WantGlob string
	}{
		{Cmd: `tool image --skip-files "./testdata/*/bar" .`, WantGlob: ""},            // Test 0: a skip flag.
		{Cmd: `tool config --skip-dirs "./foo/**/*.tf" .`, WantGlob: ""},               // Test 1: another skip flag.
		{Cmd: `tool lint --exclude "vendor/*" .`, WantGlob: ""},                        // Test 2: an exclude flag.
		{Cmd: `tool lint --ignore-path "build/*" .`, WantGlob: ""},                     // Test 3: an ignore flag.
		{Cmd: `tool lint "src/util/**/*.js"`, WantGlob: "src/util/**/*.js"},            // Test 4: a glob it reads.
		{Cmd: `tool lint --exclude "vendor/*" "src/**/*.js"`, WantGlob: "src/**/*.js"}, // Test 5: both.
		{Cmd: `tool lint --files "src/**/*.js"`, WantGlob: "src/**/*.js"},              // Test 6: a flag that reads.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			md := "```sh\n" + test.Cmd + "\n```\n"
			plan := BuildPlan("repo", "", md, []string{"tool"},
				[]PlanInstall{{Cmd: "go install example.com/tool@latest", Ecosystem: "go"}}, nil, testRecognizer())
			got := ""
			for _, s := range plan.Steps {
				for _, l := range s.Lines {
					if rest, ok := strings.CutPrefix(l.Skip, "globs "); ok {
						got, _, _ = strings.Cut(rest, ",")
					}
				}
			}
			if diff := cmp.Diff(test.WantGlob, got); diff != "" {
				t.Errorf("glob mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
