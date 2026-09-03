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
		writeFailure(w, c, r)
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
	_, _ = fmt.Fprintf(w, "\n%s\n", verdictLine(c, fail, gap, other))
	_, _ = fmt.Fprintf(w, "%s\n", summaryLine(c, pass, fail, gap, other, len(results), total))
}

// verdictLine states what the run established, which is not the same as what
// it counted. A run with no failures can still be a run that could not check
// several documented lines, and printing only green counts invites a reader to
// hear "your docs are fine" when kibble said "nothing I could execute broke".
// VERIFIED is claimed only when every check reached a verdict.
func verdictLine(c palette, fail, gap, other int) string {
	switch {
	case fail > 0:
		return c.strong(ansiRed, "FAILED") + c.dim("  a documented line ran and did not work")
	case gap > 0 || other > 0:
		return c.strong(ansiYellow, "INCOMPLETE") +
			c.dim("  nothing broke, and some documented lines were not settled")
	default:
		return c.strong(ansiGreen, "VERIFIED") + c.dim("  every documented line ran and worked")
	}
}

// writeFailure prints the documented line that broke, where it is written,
// and what the tool said. A verdict a reader has to go find in a log is a
// verdict that costs them a search: the file, the line number, and the command
// are what turns a red row into an edit, and kibble already knows all three
// because the CI annotation is built from them. Nothing is printed for a
// result that is not a failure.
func writeFailure(w io.Writer, c palette, r Result) {
	type broken struct {
		line int
		cmd  string
		why  string
	}
	var found []broken
	switch {
	case r.example != nil:
		for _, s := range r.example.Steps {
			for _, l := range s.Lines {
				if l.Status == StatusFail {
					found = append(found, broken{l.Line, flatten(l.Cmd), l.Detail})
				}
			}
		}
	case r.Status == StatusFail:
		found = append(found, broken{r.Step.Line, strings.TrimSpace(r.Step.Raw), r.Detail})
	}
	if len(found) == 0 {
		return
	}
	file := readmePath(r.Step.dir, r.Step.readme)
	for _, b := range found {
		where := file
		if b.line > 0 {
			where = fmt.Sprintf("%s:%d", file, b.line)
		}
		_, _ = fmt.Fprintf(w, "      %s\n", c.dim(where))
		if b.cmd != "" {
			_, _ = fmt.Fprintf(w, "      %s %s\n", c.dim("$"), b.cmd)
		}
		for _, ln := range wrapDetail(b.why, 66) {
			_, _ = fmt.Fprintf(w, "        %s\n", c.red(ln))
		}
	}
}

// wrapDetail breaks an error across lines at width, so a long message stays
// readable under the command it belongs to instead of running off the screen
// or being truncated into uselessness.
func wrapDetail(s string, width int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	cur := ""
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case len(cur)+1+len(word) <= width:
			cur += " " + word
		default:
			out = append(out, cur)
			cur = word
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	// Four lines is enough to say what broke. A long tail is elided rather
	// than printed whole, since the full output is what -json is for.
	if len(out) > 4 {
		out = out[:4]
		out[3] += " …"
	}
	return out
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
