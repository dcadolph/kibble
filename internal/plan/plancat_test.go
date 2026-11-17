package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/kibble/internal/docblock"
)

// TestCatTranscript checks that a prompted block opening with `$ cat <file>`
// and the file's lines is planned as the document writing that file. trivy's
// filtering guide shows .trivyignore that way and then runs a scan reading it,
// and kibble reported the file as a gap the document never filled.
func TestCatTranscript(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		Markdown  string
		WantFiles []string
		WantCmds  []string
	}{{ // Test 0: a shown file is written.
		Markdown:  "```bash\n$ cat .trivyignore\n# Accept the risk\nCVE-2018-14618\n```\n",
		WantFiles: []string{".trivyignore"},
	}, { // Test 1: the commands after the shown file still run.
		Markdown:  "```bash\n$ cat app.yaml\nkind: Pod\n\n$ tool scan app.yaml\n```\n",
		WantFiles: []string{"app.yaml"}, WantCmds: []string{"tool scan app.yaml"},
	}, { // Test 2: a trailing comment on the cat is allowed.
		Markdown:  "```bash\n$ cat requirements.txt # it will only find requests\nrequests==2.28.2\n```\n",
		WantFiles: []string{"requirements.txt"},
	}, { // Test 3: a file the checkout holds is never overwritten, and the cat runs.
		Markdown: "```bash\n$ cat Dockerfile\nFROM alpine:3.12\n```\n",
		WantCmds: []string{"cat Dockerfile"},
	}, { // Test 4: a cat piped into another command shows that command's output.
		Markdown: "```bash\n$ cat result.json | jq .\n{\n}\n```\n",
		WantCmds: []string{"cat result.json | jq ."},
	}, { // Test 5: a cat after another command shows what that command made.
		Markdown: "```bash\n$ tool scan -o out.json\n$ cat out.json\n{}\n```\n",
		WantCmds: []string{"tool scan -o out.json", "cat out.json"},
	}, { // Test 6: a file an earlier line wrote is not written again.
		Markdown: "```bash\necho hi > note.txt\n```\n\n```bash\n$ cat note.txt\nhi\n```\n",
		WantCmds: []string{"echo hi > note.txt", "cat note.txt"},
	}, { // Test 7: a cat with no lines under it is a plain command.
		Markdown: "```bash\n$ cat notes.txt\n```\n",
		WantCmds: []string{"cat notes.txt"},
	}, { // Test 8: right after a block of the tool, under its intro, the cat shows what it wrote.
		Markdown: "## Store\n\nThe flag writes each response.\n\n```console\ntool -u x -store-response\n```\n\n" +
			"```bash\n$ cat tool_response/index.txt\n\ntool_response/a.txt https://example.com\n```\n",
		WantCmds: []string{"tool -u x -store-response", "cat tool_response/index.txt"},
	}, { // Test 9: a new paragraph between them makes the cat a file to write.
		Markdown: "## Lists\n\nScan a list.\n\n```console\ntool -u x\n```\n\nGiven this list:\n\n" +
			"```bash\n$ cat url_list.txt\nhttps://example.com\n```\n",
		WantFiles: []string{"url_list.txt"}, WantCmds: []string{"tool -u x"},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := BuildPlan("repo", dir, test.Markdown, []string{"tool"},
				[]PlanInstall{{Cmd: "go install example.com/tool@latest", Ecosystem: "go"}}, nil, testRecognizer())
			var files, cmds []string
			for _, s := range plan.Steps {
				for _, l := range s.Lines {
					if l.File != "" {
						files = append(files, l.File)
						continue
					}
					cmds = append(cmds, strings.TrimSpace(docblock.StripComment(docblock.Flatten(l.Cmd))))
				}
			}
			if diff := cmp.Diff(test.WantFiles, files); diff != "" {
				t.Errorf("files mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantCmds, cmds); diff != "" {
				t.Errorf("commands mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
