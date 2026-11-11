package docblock

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	// reExitHeading matches a heading that opens a section about exit codes.
	reExitHeading = regexp.MustCompile(`(?i)^#{1,6}\s.*\bexit (codes?|status(es)?)\b`)
	// reAnyHeading matches any markdown heading line.
	reAnyHeading = regexp.MustCompile(`^#{1,6}\s`)
	// reExitLead matches prose that starts a run of exit codes, such as witr's
	// "Exit codes: 0 found, 1 found with warnings, 2 not found".
	reExitLead = regexp.MustCompile(`(?i)\bexit (codes?|status(es)?)\s*:`)
	// reExitRow matches one documented code and its meaning, as a table row,
	// a list item, or one entry of a comma-separated run.
	reExitRow = regexp.MustCompile("^\\s*[|*-]?\\s*`?(\\d{1,3})`?\\s*[|:=)–—-]?\\s*(.+)$")
	// reNotFoundMeaning matches the meanings that say a lookup came back empty.
	reNotFoundMeaning = regexp.MustCompile(`(?i)\b(not found|no match(es)?|nothing found)\b`)
)

// NotFoundCodes returns the exit codes a document says mean that nothing was
// found, from a section headed as exit codes or a prose run that starts with
// "Exit codes:". Only a meaning of not found, no match, or nothing found
// counts. Every other documented code, including any failure a project
// documents, is left out, so a table cannot excuse a real error. Codes are
// nonzero ordinary exits only, in ascending order.
func NotFoundCodes(markdown string) []int {
	var spans []string
	lines := strings.Split(ProseOnly(markdown), "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case reExitHeading.MatchString(line):
			var sec []string
			for i++; i < len(lines) && !reAnyHeading.MatchString(lines[i]); i++ {
				sec = append(sec, lines[i])
			}
			i--
			spans = append(spans, strings.Join(sec, "\n"))
		case reExitLead.MatchString(line):
			para := []string{line[reExitLead.FindStringIndex(line)[1]:]}
			for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" && !reAnyHeading.MatchString(lines[i+1]) {
				i++
				para = append(para, lines[i])
			}
			spans = append(spans, strings.Join(para, " "))
		}
	}
	var codes []int
	for _, span := range spans {
		for _, piece := range strings.FieldsFunc(span, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n'
		}) {
			m := reExitRow.FindStringSubmatch(piece)
			if m == nil || !reNotFoundMeaning.MatchString(m[2]) {
				continue
			}
			code, err := strconv.Atoi(m[1])
			if err != nil || code < 1 || code > 125 || code == 124 || slices.Contains(codes, code) {
				continue
			}
			codes = append(codes, code)
		}
	}
	slices.Sort(codes)
	return codes
}
