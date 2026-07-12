package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"
)

// report writes results to w, as a table or JSON.
func report(w io.Writer, results []Result, asJSON bool) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Step.Repo != results[j].Step.Repo {
			return results[i].Step.Repo < results[j].Step.Repo
		}
		return results[i].Step.Kind < results[j].Step.Kind
	})
	if asJSON {
		reportJSON(w, results)
		return
	}
	reportTable(w, results)
}

// reportJSON writes results as an indented JSON array. Example rows carry
// their per-line outcomes, so CI logs show exactly which documented line
// failed or why one was skipped.
func reportJSON(w io.Writer, results []Result) {
	type lineRow struct {
		Cmd    string `json:"cmd"`
		Status string `json:"status"`
		Code   int    `json:"code"`
		Detail string `json:"detail,omitempty"`
	}
	type stepRow struct {
		ID      string    `json:"id"`
		Heading string    `json:"heading,omitempty"`
		Lines   []lineRow `json:"lines"`
	}
	type row struct {
		Repo    string    `json:"repo"`
		Kind    string    `json:"kind"`
		Status  string    `json:"status"`
		Seconds int       `json:"seconds"`
		Module  string    `json:"module,omitempty"`
		Smoke   string    `json:"smoke,omitempty"`
		Detail  string    `json:"detail,omitempty"`
		Steps   []stepRow `json:"steps,omitempty"`
	}
	rows := make([]row, 0, len(results))
	for _, r := range results {
		out := row{
			Repo: r.Step.Repo, Kind: r.Step.Kind, Status: string(r.Status),
			Seconds: int(r.Duration.Round(time.Second).Seconds()),
			Module:  r.Step.Module, Smoke: r.SmokeLine, Detail: r.Detail,
		}
		if r.example != nil {
			for _, s := range r.example.Steps {
				sr := stepRow{ID: s.ID, Heading: s.Heading}
				for _, l := range s.Lines {
					sr.Lines = append(sr.Lines, lineRow{
						Cmd: l.Cmd, Status: string(l.Status), Code: l.Code, Detail: l.Detail,
					})
				}
				out.Steps = append(out.Steps, sr)
			}
		}
		rows = append(rows, out)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rows)
}

// reportTable writes a compact aligned table and a summary line.
func reportTable(w io.Writer, results []Result) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tKIND\tSTATUS\tTIME\tDETAIL")
	var pass, fail, other int
	for _, r := range results {
		detail := r.SmokeLine
		if r.Detail != "" {
			detail = r.Detail
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			r.Step.Repo, r.Step.Kind, r.Status,
			r.Duration.Round(time.Second), truncate(detail, 54))
		switch r.Status {
		case StatusPass:
			pass++
		case StatusFail:
			fail++
		default:
			other++
		}
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "\n%d pass, %d fail, %d other of %d install steps\n",
		pass, fail, other, len(results))
}

// truncate shortens s to n runes, adding an ellipsis when it cuts.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
