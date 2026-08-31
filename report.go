package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
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
		Image   string    `json:"image,omitempty"`
		Smoke   string    `json:"smoke,omitempty"`
		Detail  string    `json:"detail,omitempty"`
		Steps   []stepRow `json:"steps,omitempty"`
	}
	rows := make([]row, 0, len(results))
	for _, r := range results {
		out := row{
			Repo: r.Step.Repo, Kind: r.Step.Kind, Status: string(r.Status),
			Seconds: int(r.Duration.Round(time.Second).Seconds()),
			Module:  r.Step.Module, Image: r.Image,
			Smoke: r.SmokeLine, Detail: r.Detail,
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
	c := newPalette(w)
	var pass, fail, gap, other int
	var total time.Duration
	repo := ""
	for _, r := range results {
		if r.Step.Repo != repo {
			repo = r.Step.Repo
			_, _ = fmt.Fprintf(w, "\n%s\n", c.bold(repo))
		}
		detail := r.SmokeLine
		if r.Detail != "" {
			detail = r.Detail
		}
		detail = truncate(detail, 62)
		if r.Image != "" {
			detail += c.dim(fmt.Sprintf("  (%s)", r.Image))
		}
		total += r.Duration
		_, _ = fmt.Fprintf(w, "  %s %s %s  %s%s\n",
			c.mark(r.Status), c.blue(fmt.Sprintf("%-13s", r.Step.Kind)),
			c.dim(fmt.Sprintf("%5s", r.Duration.Round(time.Second))),
			c.statusWord(r.Status), detail)
		switch r.Status {
		case StatusPass:
			pass++
		case StatusFail:
			fail++
		case StatusGap:
			gap++
		default:
			other++
		}
	}
	_, _ = fmt.Fprintf(w, "\n%s\n", summaryLine(c, pass, fail, gap, other, len(results), total))
}

// summaryLine counts the run. A failure or a gap is colored so the eye lands
// on it, and a count of zero stays plain so nothing shouts about nothing.
func summaryLine(c palette, pass, fail, gap, other, checks int, total time.Duration) string {
	parts := []string{c.green(fmt.Sprintf("%d passed", pass))}
	if fail > 0 {
		parts = append(parts, c.red(fmt.Sprintf("%d failed", fail)))
	}
	if gap > 0 {
		parts = append(parts, c.yellow(fmt.Sprintf("%d gap", gap)))
	}
	if other > 0 {
		parts = append(parts, c.dim(fmt.Sprintf("%d other", other)))
	}
	return fmt.Sprintf("%s  %s", strings.Join(parts, "  "),
		c.dim(fmt.Sprintf("%d checks in %s", checks, total.Round(time.Second))))
}

// truncate shortens s to n runes, adding an ellipsis when it cuts.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
