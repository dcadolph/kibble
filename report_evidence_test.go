package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/kibble/internal/sandbox"
)

// TestReportJSONCarriesEvidence checks that a verdict travels with what it was
// established against. A bare VERIFIED reads as "this documentation works",
// which is wider than anything a single container run can show, so the row has
// to say which platform, which image, whether a network was reachable, whether
// credentials were supplied, and which inputs kibble invented.
func TestReportJSONCarriesEvidence(t *testing.T) {
	t.Parallel()

	results := []Result{{
		Step:     InstallStep{Repo: "demo", Kind: "example"},
		Status:   StatusVerified,
		Duration: 3 * time.Second,
		Image:    "golang:1",
		platform: sandbox.Platform{Arch: "arm64", Host: "arm64"},
		example: &exampleRun{Steps: []exampleStep{{
			ID: "b1",
			Lines: []lineResult{
				{Cmd: "tool read notes.md", Status: StatusVerified, Synthetic: []string{"notes.md"}},
				{Cmd: "tool read notes.md again", Status: StatusVerified, Synthetic: []string{"notes.md"}},
				{Cmd: "tool read data.csv", Status: StatusVerified, Synthetic: []string{"data.csv"}},
			},
		}}},
	}}

	var buf bytes.Buffer
	reportJSON(&buf, results)

	var rows []struct {
		Status   string `json:"status"`
		Evidence struct {
			Platform    string   `json:"platform"`
			Image       string   `json:"image"`
			Network     string   `json:"network"`
			Credentials string   `json:"credentials"`
			Synthetic   []string `json:"synthetic_inputs"`
			RanAt       string   `json:"ran_at"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	got := rows[0].Evidence

	if diff := cmp.Diff("linux/arm64", got.Platform); diff != "" {
		t.Errorf("platform mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("golang:1", got.Image); diff != "" {
		t.Errorf("image mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("enabled", got.Network); diff != "" {
		t.Errorf("network mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("none", got.Credentials); diff != "" {
		t.Errorf("credentials mismatch (-want +got):\n%s", diff)
	}
	// The same fabricated file served two lines, and listing it twice would
	// overstate how much of the run stood on invented input.
	if diff := cmp.Diff([]string{"notes.md", "data.csv"}, got.Synthetic); diff != "" {
		t.Errorf("synthetic inputs mismatch (-want +got):\n%s", diff)
	}
	if _, err := time.Parse(time.RFC3339, got.RanAt); err != nil {
		t.Errorf("ran_at %q is not RFC3339: %v", got.RanAt, err)
	}
}

// TestReportJSONEvidenceWithoutSynthetic checks that a run standing entirely on
// the document's own inputs says nothing about fabricated ones, rather than
// reporting an empty list that reads as a claim.
func TestReportJSONEvidenceWithoutSynthetic(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	reportJSON(&buf, []Result{{
		Step: InstallStep{Repo: "demo", Kind: "go-install"}, Status: StatusVerified,
	}})
	if bytes.Contains(buf.Bytes(), []byte("synthetic_inputs")) {
		t.Errorf("evidence names synthetic inputs when there were none:\n%s", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"credentials": "none"`)) {
		t.Errorf("evidence omits the credential fact:\n%s", buf.String())
	}
}

// TestReportJSONEvidencePlatform checks that each row names the platform its
// own container ran on. One run can use a native default image and an emulated
// one, and the JSON used to give every row the architecture kibble was
// compiled for.
func TestReportJSONEvidencePlatform(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	reportJSON(&buf, []Result{{
		Step: InstallStep{Repo: "demo", Kind: "brew"}, Status: StatusVerified, Image: "homebrew/brew",
		platform: sandbox.Platform{Arch: "amd64", Host: "arm64"},
	}, {
		Step: InstallStep{Repo: "demo", Kind: "go-install"}, Status: StatusVerified,
		platform: sandbox.Platform{Arch: "arm64", Host: "arm64"},
	}})
	var rows []struct {
		Evidence struct {
			Platform string `json:"platform"`
			Emulated bool   `json:"emulated"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	type evidence struct {
		Platform string
		Emulated bool
	}
	var got []evidence
	for _, r := range rows {
		got = append(got, evidence{Platform: r.Evidence.Platform, Emulated: r.Evidence.Emulated})
	}
	want := []evidence{{Platform: "linux/amd64", Emulated: true}, {Platform: "linux/arm64"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("evidence mismatch (-want +got):\n%s", diff)
	}
	if strings.Count(buf.String(), `"emulated"`) != 1 {
		t.Errorf("a native row carries the emulated field:\n%s", buf.String())
	}
}

// TestEvidenceLinePlatforms checks that the text report names every distinct
// platform the run used, with the emulated one marked, once each.
func TestEvidenceLinePlatforms(t *testing.T) {
	t.Parallel()
	native := sandbox.Platform{Arch: "arm64", Host: "arm64"}
	emulated := sandbox.Platform{Arch: "amd64", Host: "arm64"}
	tests := []struct {
		WantLine  string
		Platforms []sandbox.Platform
	}{{ // Test 0: one native platform.
		Platforms: []sandbox.Platform{native, native},
		WantLine:  "established on linux/arm64, network enabled, no credentials",
	}, { // Test 1: a native and an emulated platform, each named once.
		Platforms: []sandbox.Platform{native, emulated, native, emulated},
		WantLine: "established on linux/arm64 and linux/amd64 (emulated on arm64), " +
			"network enabled, no credentials",
	}, { // Test 2: three platforms join as a list.
		Platforms: []sandbox.Platform{native, emulated, {Arch: "riscv64", Host: "arm64"}},
		WantLine: "established on linux/arm64, linux/amd64 (emulated on arm64), and " +
			"linux/riscv64 (emulated on arm64), network enabled, no credentials",
	}, { // Test 3: an unknown architecture claims none.
		Platforms: []sandbox.Platform{{}},
		WantLine:  "established on linux, network enabled, no credentials",
	}, { // Test 4: no results claim no architecture.
		WantLine: "established on linux, network enabled, no credentials",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			var results []Result
			for _, p := range test.Platforms {
				results = append(results, Result{Step: InstallStep{Repo: "demo"}, platform: p})
			}
			if diff := cmp.Diff(test.WantLine, evidenceLine(palette{}, results)); diff != "" {
				t.Errorf("line mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestFillPlatforms checks that a result no container ran for takes the
// daemon's platform and one that ran keeps its own.
func TestFillPlatforms(t *testing.T) {
	t.Parallel()
	host := sandbox.Platform{Arch: "arm64", Host: "arm64"}
	ran := sandbox.Platform{Arch: "amd64", Host: "arm64"}
	results := []Result{{platform: ran}, {}}
	fillPlatforms(results, host)
	got := []sandbox.Platform{results[0].platform, results[1].platform}
	if diff := cmp.Diff([]sandbox.Platform{ran, host}, got); diff != "" {
		t.Errorf("platform mismatch (-want +got):\n%s", diff)
	}
}

// TestDerivedChecksKeepPlatform checks that the flag and coverage rows, which
// read the help an install's container printed, carry that container's
// platform rather than falling back to the daemon's.
func TestDerivedChecksKeepPlatform(t *testing.T) {
	t.Parallel()
	emulated := sandbox.Platform{Arch: "amd64", Host: "arm64"}
	install := Result{
		Step:   InstallStep{Repo: "demo", Kind: "brew", Binary: "tool", Usage: &Usage{}},
		Status: StatusVerified, platform: emulated,
	}
	derived := append(flagChecks([]Result{install}), docCoverageChecks([]Result{install})...)
	if len(derived) == 0 {
		t.Fatal("no derived checks to compare")
	}
	for _, r := range derived {
		if diff := cmp.Diff(emulated, r.platform); diff != "" {
			t.Errorf("%s platform mismatch (-want +got):\n%s", r.Step.Kind, diff)
		}
	}
}
