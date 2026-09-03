package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestPaletteOff checks that anything but a terminal gets plain text. An
// escape sequence in a build log is noise the reader cannot remove.
func TestPaletteOff(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	c := newPalette(&buf)
	if c.on {
		t.Fatal("a buffer is not a terminal, so color should be off")
	}
	for _, got := range []string{c.green("ok"), c.red("no"), c.dim("q"), c.bold("b")} {
		if strings.Contains(got, "\x1b") {
			t.Errorf("painted %q into a non-terminal", got)
		}
	}
	if got := c.statusWord(StatusFail); got != "FAIL  " {
		t.Errorf("statusWord = %q, want the plain word", got)
	}
}

// TestPaletteRespectsNoColor checks the environment override, which is the
// switch a person reaches for when a terminal renders escapes badly.
func TestPaletteRespectsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "1")
	if newPalette(nil).on {
		t.Error("NO_COLOR should win over CLICOLOR_FORCE")
	}
}

// TestMarks checks that every status has a symbol, so a reader without color
// still sees which rows need attention.
func TestMarks(t *testing.T) {
	t.Parallel()
	c := palette{}
	tests := []struct {
		Status Status
		Want   string
	}{{ // Test 0: a pass is a tick.
		Status: StatusVerified, Want: "✓",
	}, { // Test 1: a failure is a cross.
		Status: StatusFail, Want: "✗",
	}, { // Test 2: an error kibble caused reads like a failure to the eye.
		Status: StatusError, Want: "✗",
	}, { // Test 3: a gap wants attention without claiming breakage.
		Status: StatusGap, Want: "!",
	}, { // Test 4: drift is the same weight as a gap.
		Status: StatusDrift, Want: "!",
	}, { // Test 5: a skip is quiet.
		Status: StatusSkipped, Want: "–",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := c.mark(test.Status); got != test.Want {
				t.Errorf("mark(%s) = %q, want %q", test.Status, got, test.Want)
			}
		})
	}
}

// TestSummaryLineQuiet checks that a clean run says only what happened, so a
// count of zero failures does not shout about nothing.
func TestSummaryLineQuiet(t *testing.T) {
	t.Parallel()
	got := summaryLine(palette{}, 4, 0, 0, 0, 4, 0)
	if strings.Contains(got, "failed") || strings.Contains(got, "gap") {
		t.Errorf("clean summary = %q, want no mention of failures or gaps", got)
	}
	if !strings.Contains(got, "4 passed") {
		t.Errorf("summary = %q, want the pass count", got)
	}
}
