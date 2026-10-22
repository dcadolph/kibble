package plan

import (
	"fmt"
	"testing"

	"github.com/dcadolph/kibble/internal/shell"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestPlannerReadsQuotedArguments checks that the planner reads a documented
// line the way a shell reads it, rather than by splitting on spaces. A quoted
// path is one argument, and a flag written inside a quoted argument is not a
// flag the line passes. Splitting on whitespace gets both wrong: it reports a
// fragment of a path as the missing file, and it finds flags in prose.
func TestPlannerReadsQuotedArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Line     string
		WantFile string
	}{{ // Test 0: a quoted path is one file, not the word before its space.
		Line: `tool validate "my config.json"`, WantFile: "my config.json",
	}, { // Test 1: the same shape unquoted is missing for the same reason.
		Line: `tool validate config.json`, WantFile: "config.json",
	}, { // Test 2: single quotes bind as tightly as double ones.
		Line: `tool validate 'my data.csv'`, WantFile: "my data.csv",
	}, { // Test 3: a path the repo ships is not missing, spaces and all.
		Line: `tool validate "have space.json"`, WantFile: "",
	}, { // Test 4: an extension kibble can fabricate becomes a fixture rather
		// than a missing file, and the space does not change that.
		Line: `tool read "my notes.md"`, WantFile: "",
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			pl := &planner{
				plan:     &Plan{},
				binaries: map[string]bool{"tool": true},
				tree:     map[string]bool{"have space.json": true},
				created:  map[string]bool{},
				fixed:    map[string]bool{},
			}
			got := pl.missingFile(test.Line)
			if diff := cmp.Diff(test.WantFile, got); diff != "" {
				t.Errorf("missingFile mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestInteractiveFlagIgnoresQuotedText checks that --interactive counts only
// when the line passes it. A document explaining the flag inside a quoted
// argument is describing it, not using it, and skipping that line would leave
// a working example unverified.
func TestInteractiveFlagIgnoresQuotedText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Line string
		Want bool
	}{{ // Test 0: the flag is passed, so the line is interactive.
		Line: `tool run --interactive`, Want: true,
	}, { // Test 1: the flag sits inside a quoted argument, so it is text.
		Line: `tool run --message "pass --interactive to prompt"`, Want: false,
	}, { // Test 2: no flag at all.
		Line: `tool run`, Want: false,
	}, { // Test 3: the short form never counts, quoted or not.
		Line: `tool run -i`, Want: false,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := interactiveFlag(test.Line)
			if diff := cmp.Diff(test.Want, got); diff != "" {
				t.Errorf("interactiveFlag mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestShellFirstWord checks the program name a line runs, including the cases
// that made indexing a whitespace split wrong: a blank line has no program and
// must not panic, and a quoted program name is one word.
func TestShellFirstWord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Line string
		Want string
	}{{ // Test 0: an ordinary invocation.
		Line: "tool build", Want: "tool",
	}, { // Test 1: a blank line runs nothing and returns nothing.
		Line: "", Want: "",
	}, { // Test 2: whitespace only is still nothing.
		Line: "   ", Want: "",
	}, { // Test 3: an assignment prefix is not the program.
		Line: "FOO=1 tool build", Want: "tool",
	}, { // Test 4: a quoted program name holding a space is one word.
		Line: `"my tool" build`, Want: "my tool",
	}, { // Test 5: a line no bash parser accepts still yields its first word.
		Line: `tool "unclosed`, Want: "tool",
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := shell.FirstWord(test.Line)
			if diff := cmp.Diff(test.Want, got); diff != "" {
				t.Errorf("shell.FirstWord mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestMissingFileReaderInputs checks the file rules for names the reader
// supplies: archive and diagram extensions count as files, and a name that
// says it holds checksums or a signature is reported rather than fabricated.
func TestMissingFileReaderInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Line         string
		WantFile     string
		WantFixtures []string
	}{{ // Test 0: a diagram source is a file.
		Line: `mmdr -i diagram.mmd -o out.svg`, WantFile: "diagram.mmd",
	}, { // Test 1: a tarball is a file.
		Line: `lla source.tar.gz -l`, WantFile: "source.tar.gz",
	}, { // Test 2: a checksum list is missing and never fabricated.
		Line:     `cat aqua_2.34.0_checksums.txt | sha256sum -c -`,
		WantFile: "aqua_2.34.0_checksums.txt",
	}, { // Test 3: a sums file is never fabricated.
		Line: `cat sha256sums.txt`, WantFile: "sha256sums.txt",
	}, { // Test 4: an ordinary prose file still becomes a fixture.
		Line: `cat notes.txt`, WantFixtures: []string{"notes.txt"},
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			pl := &planner{
				plan:     &Plan{},
				binaries: map[string]bool{"mmdr": true, "lla": true},
				tree:     map[string]bool{},
				created:  map[string]bool{},
				fixed:    map[string]bool{},
			}
			got := pl.missingFile(test.Line)
			if diff := cmp.Diff(test.WantFile, got); diff != "" {
				t.Errorf("missingFile mismatch (-want +got):\n%s", diff)
			}
			var fixtures []string
			for _, f := range pl.plan.Fixtures {
				fixtures = append(fixtures, f.Path)
			}
			if diff := cmp.Diff(test.WantFixtures, fixtures, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("fixtures mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestDownloadedNames checks which file names a curl or wget line creates.
func TestDownloadedNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Fields     []string
		WantResult []string
	}{{ // Test 0: curl -LO saves under the URL's last segment.
		Fields:     []string{"curl", "-LO", "https://h/archive/0.7.1.zip"},
		WantResult: []string{"0.7.1.zip"},
	}, { // Test 1: curl without -O writes to stdout.
		Fields: []string{"curl", "https://h/0.7.1.zip"},
	}, { // Test 2: curl -o is handled elsewhere and adds nothing here.
		Fields: []string{"curl", "-o", "out.zip", "https://h/0.7.1.zip"},
	}, { // Test 3: the query is dropped.
		Fields:     []string{"curl", "-LO", "https://h/x.zip?v=1"},
		WantResult: []string{"x.zip"},
	}, { // Test 4: the fragment is dropped.
		Fields:     []string{"curl", "-fsSLO", "https://h/x.zip#frag"},
		WantResult: []string{"x.zip"},
	}, { // Test 5: --remote-name counts.
		Fields:     []string{"curl", "--remote-name", "ftp://h/y.tgz"},
		WantResult: []string{"y.tgz"},
	}, { // Test 6: every URL operand is saved.
		Fields:     []string{"curl", "-O", "https://h/a.zip", "https://h/b.zip"},
		WantResult: []string{"a.zip", "b.zip"},
	}, { // Test 7: wget saves under the last segment.
		Fields:     []string{"wget", "https://h/a.tar.gz"},
		WantResult: []string{"a.tar.gz"},
	}, { // Test 8: wget -O names the file.
		Fields:     []string{"wget", "-O", "b.tgz", "https://h/a.tar.gz"},
		WantResult: []string{"b.tgz"},
	}, { // Test 9: another command downloads nothing.
		Fields: []string{"git", "clone", "https://h/a.git"},
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := downloadedNames(test.Fields)
			if diff := cmp.Diff(test.WantResult, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("downloadedNames mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestMissingFileAfterDownload checks that a download line makes the file it
// saves available to a later line, and that a stdout fetch does not.
func TestMissingFileAfterDownload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Download string
		Line     string
		WantFile string
	}{{ // Test 0: curl -LO provides the archive.
		Download: `curl -LO https://h/archive/0.7.1.zip`, Line: `unzip 0.7.1.zip`,
	}, { // Test 1: curl without -O provides nothing.
		Download: `curl https://h/0.7.1.zip`, Line: `unzip 0.7.1.zip`, WantFile: "0.7.1.zip",
	}, { // Test 2: curl -o provides the named file.
		Download: `curl -o out.zip https://h/0.7.1.zip`, Line: `unzip out.zip`,
	}, { // Test 3: wget provides the URL's file.
		Download: `wget https://h/a.tar.gz`, Line: `tar xzf a.tar.gz`,
	}, { // Test 4: wget -O provides the named file.
		Download: `wget -O b.tgz https://h/a.tar.gz`, Line: `tar xzf b.tgz`,
	}, { // Test 5: a quoted URL with a query provides the bare name.
		Download: `curl -LO 'https://h/x.zip?v=1'`, Line: `unzip x.zip`,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			pl := &planner{
				plan:    &Plan{},
				tree:    map[string]bool{},
				created: map[string]bool{},
				fixed:   map[string]bool{},
			}
			pl.recordCreated(test.Download)
			if diff := cmp.Diff(test.WantFile, pl.missingFile(test.Line)); diff != "" {
				t.Errorf("missingFile mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
