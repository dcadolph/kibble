// Package util holds the small helpers more than one package needs. It exists
// so a second copy of one never appears: a truncation that rounds differently
// in the table than in an annotation is the kind of difference nobody notices
// until two reports disagree about the same run.
package util

// Truncate shortens s to at most n runes, marking the cut with an ellipsis.
// Runes rather than bytes, since a report that splits a multi-byte character
// prints a replacement glyph where the document had a word.
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
