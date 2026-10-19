package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// reREADMEPin matches a repository line of the corpus output the README quotes, and
// reREADMEMutation matches a mutation line of the same output.
var (
	reREADMEPin      = regexp.MustCompile(`^([\w.-]+): (\d+,\d+,\d+) as expected$`)
	reREADMEMutation = regexp.MustCompile(`^([\w.-]+): ([A-Z-]+=\d+) naming (\S+) as expected$`)
)

// TestREADMECorpusMatchesPins fails when the corpus output quoted in the README stops
// matching corpus/repos.tsv and corpus/mutations.tsv. The README offers those figures as
// evidence, and evidence that drifts from the pins the weekly job enforces is worse than
// none.
func TestREADMECorpusMatchesPins(t *testing.T) {
	t.Parallel()
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	var gotPins, gotMutations []string
	for _, line := range strings.Split(string(readme), "\n") {
		if m := reREADMEPin.FindStringSubmatch(line); m != nil {
			gotPins = append(gotPins, m[1]+" "+m[2])
		}
		if m := reREADMEMutation.FindStringSubmatch(line); m != nil {
			gotMutations = append(gotMutations, m[1]+" "+m[2]+" "+m[3])
		}
	}
	wantPins := corpusRows(t, "corpus/repos.tsv", 4, func(f []string) string {
		return f[0] + " " + f[3]
	})
	wantMutations := corpusRows(t, "corpus/mutations.tsv", 8, func(f []string) string {
		return f[0] + " " + f[6] + " " + f[7]
	})
	if diff := cmp.Diff(wantPins, gotPins); diff != "" {
		t.Errorf("README corpus counts differ from corpus/repos.tsv (-pins +readme):\n%s", diff)
	}
	if diff := cmp.Diff(wantMutations, gotMutations); diff != "" {
		t.Errorf("README mutation lines differ from corpus/mutations.tsv (-pins +readme):\n%s", diff)
	}
}

// corpusRows reads a tab-separated corpus file, skipping blank lines and comments, and
// renders each row of at least cols fields with render.
func corpusRows(t *testing.T, path string, cols int, render func([]string) string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < cols {
			t.Fatalf("%s: row has %d fields, want at least %d: %q", path, len(fields), cols, line)
		}
		rows = append(rows, render(fields))
	}
	return rows
}
