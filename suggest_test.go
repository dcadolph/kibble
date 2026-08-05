package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestFirstJSONObject checks that a reply is parsed whatever prose or fencing
// a model wraps it in, and that a malformed reply is an error rather than a
// silent empty result.
func TestFirstJSONObject(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In     string
		Want   string
		WantOK bool
	}{{ // Test 0: a bare object.
		In: `{"a":1}`, Want: `{"a":1}`, WantOK: true,
	}, { // Test 1: prose before and after.
		In: `Sure, here you go: {"a":1} Hope that helps.`, Want: `{"a":1}`, WantOK: true,
	}, { // Test 2: a fenced block.
		In: "```json\n{\"a\":1}\n```", Want: `{"a":1}`, WantOK: true,
	}, { // Test 3: nested objects keep their braces.
		In: `{"a":{"b":2}}`, Want: `{"a":{"b":2}}`, WantOK: true,
	}, { // Test 4: a brace inside a string is not structure.
		In: `{"cmd":"echo {}"}`, Want: `{"cmd":"echo {}"}`, WantOK: true,
	}, { // Test 5: an escaped quote does not end the string.
		In: `{"cmd":"say \"hi\" {"}`, Want: `{"cmd":"say \"hi\" {"}`, WantOK: true,
	}, { // Test 6: a top-level array parses too.
		In: `[{"a":1}]`, Want: `[{"a":1}]`, WantOK: true,
	}, { // Test 7: no JSON at all is an error.
		In: `I cannot help with that.`, WantOK: false,
	}, { // Test 8: an unbalanced object is an error.
		In: `{"a":1`, WantOK: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got, err := firstJSONObject(test.In)
			if test.WantOK != (err == nil) {
				t.Fatalf("err = %v, want ok = %v", err, test.WantOK)
			}
			if diff := cmp.Diff(test.Want, got); test.WantOK && diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestCertainSkip checks that reasons the engine decides on its own evidence
// are never sent to a model, while judgment calls are.
func TestCertainSkip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want bool
	}{{ // Test 0: a placeholder is settled evidence.
		In: "docs use a placeholder the reader must fill in", Want: true,
	}, { // Test 1: a missing file is settled evidence.
		In: "references main.rs, which the docs never create", Want: true,
	}, { // Test 2: another shell is settled evidence.
		In: "written for another shell, and the session runs bash", Want: true,
	}, { // Test 3: a terminal need is a judgment call worth asking about.
		In: "needs a terminal, which the container lacks", Want: false,
	}, { // Test 4: a local service is a judgment call.
		In: "needs a local service the docs assume is running", Want: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, certainSkip(test.In)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestYAMLScalar checks that a command survives being written into the config,
// so a rule matches the line it was generated from.
func TestYAMLScalar(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want string
	}{{ // Test 0: a plain command needs no quoting.
		In: "tool run", Want: "tool run",
	}, { // Test 1: a colon would start a mapping, so it is quoted.
		In: "tool run http://x", Want: `"tool run http://x"`,
	}, { // Test 2: an embedded quote is escaped.
		In: `tool say "hi"`, Want: `"tool say \"hi\""`,
	}, { // Test 3: empty stays a valid scalar.
		In: "", Want: `""`,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, yamlScalar(test.In)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestSuggestFlow drives the whole suggestion path through a stub advisor, so
// the seam, the candidate selection, and the rendered config are checked
// without a network call.
func TestSuggestFlow(t *testing.T) {
	t.Parallel()
	md := "## Usage\n\n```sh\ntool sync\ntool serve\n```\n"
	plan := buildPlan("repo", "", md, []string{"tool"},
		[]PlanInstall{{Cmd: "go install example.com/tool@latest", Ecosystem: "go"}}, nil)

	cands := suggestCandidates(plan)
	if len(cands) == 0 {
		t.Fatalf("no candidates from plan: %+v", plan.Steps)
	}

	var asked string
	advisor := AdvisorFunc(func(_ context.Context, system, user string) (string, error) {
		asked = user
		if !strings.Contains(system, "kibble") {
			t.Errorf("system prompt lost its brief: %q", system)
		}
		return `Here you go:
{"lines":[
  {"cmd":"tool sync","verdict":"nonzero","reason":"exits nonzero when it finds drift"},
  {"cmd":"tool serve","verdict":"skip","reason":"serves until interrupted"}
]}`, nil
	})

	got, err := askAdvisor(context.Background(), advisor, "repo", cands)
	if err != nil {
		t.Fatalf("askAdvisor: %v", err)
	}
	if !strings.Contains(asked, "tool sync") {
		t.Errorf("prompt did not carry the candidate lines: %q", asked)
	}

	var out strings.Builder
	if !writeSuggestedConfig(&out, "repo", cands, got) {
		t.Fatal("no config written for a disagreeing advisor")
	}
	for _, want := range []string{
		"version: 1",
		"examples:",
		"  steps:",
		"    - match: tool sync",
		"      nonzeroOk: true",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("config missing %q:\n%s", want, out.String())
		}
	}
}

// TestSuggestNoDisagreement checks that a repository the engine already
// understands produces no config, which is the good outcome rather than an
// empty file.
func TestSuggestNoDisagreement(t *testing.T) {
	t.Parallel()
	cands := []candidate{{Cmd: "tool run"}, {Cmd: "tool other", Skip: "needs a terminal"}}
	got := map[string]suggestion{
		"tool run":   {Cmd: "tool run", Verdict: "run"},
		"tool other": {Cmd: "tool other", Verdict: "skip"},
	}
	var out strings.Builder
	if writeSuggestedConfig(&out, "repo", cands, got) {
		t.Errorf("wrote a config when the advisor agreed throughout:\n%s", out.String())
	}
}
