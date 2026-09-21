package main

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/dcadolph/kibble/internal/util"
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
		Reason string `json:"reason,omitempty"`
		Code   int    `json:"code"`
		Detail string `json:"detail,omitempty"`
		// Synthetic names the files kibble fabricated for this line, so a
		// consumer can tell a pass against the document's own inputs from a
		// pass against inputs kibble invented.
		Synthetic []string `json:"synthetic,omitempty"`
	}
	type stepRow struct {
		ID      string    `json:"id"`
		Heading string    `json:"heading,omitempty"`
		Lines   []lineRow `json:"lines"`
	}
	// evidenceRow records what the verdict was established against. A status
	// on its own invites the reader to hear "this documentation works", when
	// what was shown is narrower: these commands, in this container, on this
	// platform, with a network, without credentials, and against these inputs.
	// Saying so is what makes a verdict auditable rather than trusted.
	type evidenceRow struct {
		Platform    string   `json:"platform"`
		Image       string   `json:"image,omitempty"`
		Network     string   `json:"network"`
		Credentials string   `json:"credentials"`
		Synthetic   []string `json:"synthetic_inputs,omitempty"`
		RanAt       string   `json:"ran_at"`
	}
	type row struct {
		Repo     string      `json:"repo"`
		Kind     string      `json:"kind"`
		Status   string      `json:"status"`
		Bucket   string      `json:"bucket"`
		Reason   string      `json:"reason,omitempty"`
		Seconds  int         `json:"seconds"`
		Module   string      `json:"module,omitempty"`
		Image    string      `json:"image,omitempty"`
		Smoke    string      `json:"smoke,omitempty"`
		Detail   string      `json:"detail,omitempty"`
		Evidence evidenceRow `json:"evidence"`
		Steps    []stepRow   `json:"steps,omitempty"`
	}
	// One stamp for the whole report: every row came out of the same run, and
	// a per-row clock would imply they did not.
	ranAt := time.Now().UTC().Format(time.RFC3339)
	rows := make([]row, 0, len(results))
	for _, r := range results {
		out := row{
			Repo: r.Step.Repo, Kind: r.Step.Kind, Status: string(r.Status),
			Bucket: string(r.Status.Bucket()), Reason: string(r.Reason),
			Seconds: int(r.Duration.Round(time.Second).Seconds()),
			Module:  r.Step.Module, Image: r.Image,
			Smoke: r.SmokeLine, Detail: r.Detail,
		}
		var synthetic []string
		if r.example != nil {
			for _, s := range r.example.Steps {
				sr := stepRow{ID: s.ID, Heading: s.Heading}
				for _, l := range s.Lines {
					sr.Lines = append(sr.Lines, lineRow{
						Cmd: l.Cmd, Status: string(l.Status), Reason: string(l.Reason),
						Code: l.Code, Detail: l.Detail, Synthetic: l.Synthetic,
					})
					synthetic = append(synthetic, l.Synthetic...)
				}
				out.Steps = append(out.Steps, sr)
			}
		}
		out.Evidence = evidenceRow{
			Platform: "linux/" + runtime.GOARCH, Image: r.Image,
			Network: "enabled", Credentials: "none",
			Synthetic: dedupe(synthetic), RanAt: ranAt,
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
	var pass, fail, gap, other, blocked int
	var total time.Duration
	repo := ""
	// One cause can produce one row per document, and a repository with a
	// documentation tree has a lot of documents. mise returned 224 rows saying
	// the same thing, which reads as a catastrophe rather than as the single
	// problem it was. Identical rows are counted here and printed once. The
	// JSON keeps every document, since a consumer asking which documents were
	// affected deserves the list rather than a summary.
	shared := sharedCauseCounts(results)
	printed := map[string]bool{}
	for _, r := range results {
		if r.Step.Repo != repo {
			repo = r.Step.Repo
			_, _ = fmt.Fprintf(w, "\n%s\n", c.bold(repo))
		}
		key := sharedCauseKey(r)
		if n := shared[key]; n > 1 {
			if printed[key] {
				blocked += blockedLines(r)
				total += r.Duration
				countVerdict(r.Status, &pass, &fail, &gap, &other)
				continue
			}
			printed[key] = true
		}
		detail := r.SmokeLine
		if r.Detail != "" {
			detail = r.Detail
		}
		detail = strings.TrimPrefix(detail, r.Step.doc+": ")
		detail = truncate(detail, 62)
		// Appended after truncation: the count is the reason the row reads as
		// one finding instead of many, so it must not be the part cut off.
		if n := shared[sharedCauseKey(r)]; n > 1 {
			detail = fmt.Sprintf("%s %s", detail, c.dim(fmt.Sprintf("[%d documents]", n)))
		}
		if r.Image != "" {
			detail += c.dim(fmt.Sprintf("  (%s)", r.Image))
		}
		total += r.Duration
		_, _ = fmt.Fprintf(w, "  %s %s %s  %s%s\n",
			c.mark(r.Status), c.blue(fmt.Sprintf("%-13s", r.Step.Kind)),
			c.dim(fmt.Sprintf("%5s", r.Duration.Round(time.Second))),
			c.statusWord(r.Status), detail)
		writeFailure(w, c, r)
		blocked += blockedLines(r)
		countVerdict(r.Status, &pass, &fail, &gap, &other)
	}
	_, _ = fmt.Fprintf(w, "\n%s\n", verdictLine(c, fail, gap, other+blocked))
	_, _ = fmt.Fprintf(w, "%s\n", summaryLine(c, pass, fail, gap, other, len(results), total))
	_, _ = fmt.Fprintf(w, "%s\n", evidenceLine(c, results))
}

// evidenceLine names what the verdicts above were established against. Read
// without it, a column of green invites "this documentation works", which is
// wider than one Linux container with a network and no credentials can show.
// The reader who needs the narrower claim should not have to open the JSON.
func evidenceLine(c palette, results []Result) string {
	var synthetic []string
	for _, r := range results {
		if r.example == nil {
			continue
		}
		for _, s := range r.example.Steps {
			for _, l := range s.Lines {
				synthetic = append(synthetic, l.Synthetic...)
			}
		}
	}
	line := fmt.Sprintf("established on linux/%s, network enabled, no credentials",
		runtime.GOARCH)
	if n := len(dedupe(synthetic)); n > 0 {
		line += fmt.Sprintf(", %s", pluralFiles(n))
	}
	return c.dim(line)
}

// pluralFiles renders the fabricated-input count as a phrase, since a verdict
// that stood on invented files is a narrower claim than one that did not.
func pluralFiles(n int) string {
	if n == 1 {
		return "1 fabricated input"
	}
	return fmt.Sprintf("%d fabricated inputs", n)
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

// blockedLines counts the documented lines of a result that ran without
// settling. They are counted apart from the result's own status because a
// session that verified some lines is reported as verified, and the lines it
// could not read would otherwise vanish behind that green: a run that settled
// five lines of twenty has not verified the document, and the reader has to
// be told so on the verdict line rather than in a detail string.
func blockedLines(r Result) int {
	if r.example == nil {
		return 0
	}
	n := 0
	for _, s := range r.example.Steps {
		for _, l := range s.Lines {
			if l.Status == StatusBlocked {
				n++
			}
		}
	}
	return n
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
func truncate(s string, n int) string { return util.Truncate(s, n) }

// dedupe returns the unique values of a list in first-seen order. The same
// fabricated fixture can serve several lines, and listing it once per line
// would overstate how much of the run stood on invented input.
func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// sharedCauseKey identifies a result by everything except which document it
// came from, so rows that say the same thing about different documents group
// together. The document prefix is stripped from the detail, since that is the
// only part of an otherwise identical message that differs.
func sharedCauseKey(r Result) string {
	detail := r.Detail
	if r.Step.doc != "" {
		detail = strings.TrimPrefix(detail, r.Step.doc+": ")
	}
	return strings.Join([]string{r.Step.Repo, r.Step.Kind, string(r.Status), string(r.Reason), detail}, "\x00")
}

// sharedCauseCounts counts how many results share each cause.
func sharedCauseCounts(results []Result) map[string]int {
	out := map[string]int{}
	for _, r := range results {
		out[sharedCauseKey(r)]++
	}
	return out
}

// countVerdict tallies a status into the summary buckets.
func countVerdict(s Status, pass, fail, gap, other *int) {
	switch s {
	case StatusVerified:
		*pass++
	case StatusFail:
		*fail++
	case StatusGap:
		*gap++
	default:
		*other++
	}
}
