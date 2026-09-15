package main

import (
	"bytes"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
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

	if diff := cmp.Diff("linux/"+runtime.GOARCH, got.Platform); diff != "" {
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
