package docblock

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestPrepareLinesContinuation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantLines []string
		In        []string
	}{{ // Test 0: A prompted command split across lines keeps its continuations.
		In:        []string{`$ rg somepattern \`, `    --colors 'match:none' \`, `    --colors 'match:fg:white'`},
		WantLines: []string{`rg somepattern \`, `--colors 'match:none' \`, `--colors 'match:fg:white'`},
	}, { // Test 1: Output after a finished command is still dropped.
		In:        []string{"$ echo hi", "hi", "$ echo bye"},
		WantLines: []string{"echo hi", "echo bye"},
	}, { // Test 2: A doubled backslash ends the command, so output stays dropped.
		In:        []string{`$ printf 'a\\`, "not a continuation"},
		WantLines: []string{`printf 'a\\`},
	}, { // Test 3: A block with no prompt is returned unchanged.
		In:        []string{"make build", "make test"},
		WantLines: []string{"make build", "make test"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := PrepareLines(test.In)
			if diff := cmp.Diff(test.WantLines, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestHasPlaceholder checks that a repository URL naming a stand-in owner is a
// placeholder and one naming a real owner is not.
func TestHasPlaceholder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		In   string
		Want bool
	}{{ // Test 0: The example owner is a stand-in.
		In: `lla install --git https://github.com/example/my-lla-plugins`, Want: true,
	}, { // Test 1: A your-org owner is a stand-in.
		In: `git clone https://gitlab.com/your-org/repo`, Want: true,
	}, { // Test 2: A real owner is not.
		In: `git clone https://github.com/dcadolph/kibble`, Want: false,
	}, { // Test 3: A synopsis slot with a space is a stand-in.
		In: `aqua update <command name> [<command name> ...]`, Want: true,
	}, { // Test 4: A redirect with spaces on both sides is not a slot.
		In: `tool < in > out`, Want: false,
	}, { // Test 5: A redirect with a space before the closing bracket is not a slot.
		In: `tool <in > out`, Want: false,
	}, { // Test 6: A single-word slot still is.
		In: `tool <in> out`, Want: true,
	}, { // Test 7: A contributor owner and a fix-something ref are stand-ins.
		In:   `pipx install git+https://github.com/contributor/pipx.git@fix-something`,
		Want: true,
	}, { // Test 8: A real owner with an abc123def ref is a stand-in.
		In:   `pipx install git+https://github.com/pypa/pipx.git@abc123def`,
		Want: true,
	}, { // Test 9: A main ref on a real repository is not.
		In: `pipx install git+https://github.com/psf/black.git@main`, Want: false,
	}, { // Test 10: A version tag ref is not.
		In: `pipx install git+https://github.com/pypa/pipx.git@v1.2.3`, Want: false,
	}, { // Test 11: A latest ref is not.
		In: `pipx install git+https://github.com/pypa/pipx.git@latest`, Want: false,
	}, { // Test 12: A full 40-hex sha is not.
		In: "pip install git+https://github.com/pypa/pipx.git@" +
			"0123456789abcdef0123456789abcdef01234567",
		Want: false,
	}, { // Test 13: A 40-hex sha that begins with abc123 is not.
		In: "pip install git+https://github.com/pypa/pipx.git@" +
			"abc1230123456789abcdef0123456789abcdef01",
		Want: false,
	}, { // Test 14: A 40-hex sha that begins with deadbeef is not.
		In: "pip install git+https://github.com/pypa/pipx.git@" +
			"deadbeef23456789abcdef0123456789abcdef01",
		Want: false,
	}, { // Test 15: A your-github-username owner is a stand-in.
		In: `git clone https://github.com/your-github-username/repo`, Want: true,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := HasPlaceholder(test.In); got != test.Want {
				t.Errorf("HasPlaceholder(%q) = %v, want %v", test.In, got, test.Want)
			}
		})
	}
}

// TestCodeBlocksAdmonition checks that an indented body under an admonition
// marker is prose, while an indented block after an ordinary paragraph is code.
func TestCodeBlocksAdmonition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		In        string
		WantLines []string
		WantCount int
		WantLine  int
	}{{ // Test 0: A note admonition body is not code.
		In: "!!! note\n\n    uv does not validate the credentials, so the command succeeds " +
			"even if they are incorrect.\n",
		WantCount: 0,
	}, { // Test 1: A collapsible admonition body is not code.
		In:        "??? tip\n\n    Some advice.\n\n???+ warning\n\n    Be careful.\n",
		WantCount: 0,
	}, { // Test 2: An indented block after an ordinary paragraph is code.
		In:        "Run this command:\n\n    make build\n",
		WantCount: 1,
	}, { // Test 3: A fenced block after an admonition marker is still code.
		In:        "!!! note\n\n```sh\nmake build\n```\n",
		WantCount: 1,
	}, { // Test 4: A fenced block inside an admonition body is code at its real line.
		In: "#### Origin URL\n\n!!! note \"\"\n\n    **Type**: String  \n" +
			"    **Default**: null  \n\n    Specifies the base URL.\n\n" +
			"    ```console\n    $ st run openapi.yaml --url https://api.example.com\n    ```\n",
		WantCount: 1,
		WantLines: []string{"$ st run openapi.yaml --url https://api.example.com"},
		WantLine:  11,
	}, { // Test 5: An admonition body of prose and then a fenced block yields one block.
		In:        "!!! tip \"Hint\"\n\n    Run it.\n\n    ```sh\n    make build\n    ```\n",
		WantCount: 1,
		WantLines: []string{"make build"},
		WantLine:  6,
	}, { // Test 6: A nested admonition holding a fenced block yields one block.
		In: "!!! note\n\n    Outer prose.\n\n    !!! tip\n\n        Inner prose.\n\n" +
			"        ```sh\n        make build\n        ```\n",
		WantCount: 1,
		WantLines: []string{"make build"},
		WantLine:  10,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := CodeBlocks(test.In)
			if diff := cmp.Diff(test.WantCount, len(got)); diff != "" {
				t.Fatalf("count mismatch (-want +got):\n%s", diff)
			}
			if test.WantLines == nil {
				return
			}
			lines := strings.Split(strings.TrimSuffix(strings.Join(got[0].Lines, "\n"), "\n"), "\n")
			if diff := cmp.Diff(test.WantLines, lines); diff != "" {
				t.Errorf("lines mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantLine, got[0].Line); diff != "" {
				t.Errorf("line mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestStripFrontMatter checks that a closed front matter block is blanked line for
// line and that anything else is returned untouched.
func TestStripFrontMatter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		In   string
		Want string
	}{{ // Test 0: YAML front matter is blanked and the line count is kept.
		In:   "---\ntitle: \"Quick Start\"\nweight: 30\n---\n\nBody\n",
		Want: "\n\n\n\n\nBody\n",
	}, { // Test 1: TOML front matter is blanked and the line count is kept.
		In:   "+++\ntitle = \"Quick Start\"\n+++\nBody\n",
		Want: "\n\n\nBody\n",
	}, { // Test 2: A document without front matter is untouched.
		In:   "# Title\n\nBody\n",
		Want: "# Title\n\nBody\n",
	}, { // Test 3: Unclosed front matter is untouched.
		In:   "---\ntitle: x\n\nBody\n",
		Want: "---\ntitle: x\n\nBody\n",
	}, { // Test 4: A rule that is not on the first line is untouched.
		In:   "Intro\n---\ntitle: x\n---\n",
		Want: "Intro\n---\ntitle: x\n---\n",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := string(stripFrontMatter([]byte(test.In)))
			if diff := cmp.Diff(test.Want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestCodeBlocksFrontMatter checks that front matter is not read as a setext heading
// and that block line numbers still count the front matter lines.
func TestCodeBlocksFrontMatter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		In          string
		WantHeading string
		WantLine    int
	}{{ // Test 0: Goreleaser quick-start opening, front matter is not a heading.
		In: "---\ntitle: \"Quick Start\"\nweight: 30\n---\n\n" +
			"In this example we will build, archive and release a sample project.\n\n" +
			"Initialize your module with:\n\n" +
			"```sh\ngo mod init github.com/you/your-repo\n```\n",
		WantHeading: "",
		WantLine:    11,
	}, { // Test 1: The first real heading is used and the line is unchanged.
		In: "---\ntitle: \"Quick Start\"\nweight: 30\n---\n\n" +
			"In this example we will build, archive and release a sample project.\n\n" +
			"# Quick Start\n\nInitialize your module with:\n\n" +
			"```sh\ngo mod init github.com/you/your-repo\n```\n",
		WantHeading: "Quick Start",
		WantLine:    13,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			blocks := CodeBlocks(test.In)
			if len(blocks) == 0 {
				t.Fatal("no blocks")
			}
			if diff := cmp.Diff(test.WantHeading, blocks[0].Heading); diff != "" {
				t.Errorf("heading mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantLine, blocks[0].Line); diff != "" {
				t.Errorf("line mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestCodeBlocksTitle checks that a fence's title attribute is read from its
// info string in either quote style and is empty when the fence has none.
func TestCodeBlocksTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In        string
		WantTitle string
	}{{ // Test 0: A double-quoted title followed by another attribute.
		In:        "```python title=\"process.py\" hl_lines=\"2\"\nprint(1)\n```\n",
		WantTitle: "process.py",
	}, { // Test 1: A single-quoted title.
		In:        "```toml title='pyproject.toml'\n[project]\n```\n",
		WantTitle: "pyproject.toml",
	}, { // Test 2: A fence with no title.
		In: "```sh\nmake build\n```\n",
	}, { // Test 3: A title inside an admonition body.
		In:        "!!! note\n\n    ```bash title=\"bazel/uv-auth-helper\"\n    exec uv\n    ```\n",
		WantTitle: "bazel/uv-auth-helper",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			blocks := CodeBlocks(test.In)
			if len(blocks) != 1 {
				t.Fatalf("got %d blocks, want 1", len(blocks))
			}
			if diff := cmp.Diff(test.WantTitle, blocks[0].Title); diff != "" {
				t.Errorf("title mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
