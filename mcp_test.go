package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestRegisterTools checks that the server offers both tools, since a client's
// picker shows only what is registered.
func TestRegisterTools(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "kibble", Version: "test"}, nil)
	registerTools(srv, config{})

	clientT, serverT := mcpsdk.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer func() { _ = serverSession.Close() }()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "probe", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer func() { _ = session.Close() }()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var got []string
	for _, tool := range listed.Tools {
		if tool.Description == "" {
			t.Errorf("tool %q has no description, so a picker cannot choose it", tool.Name)
		}
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	want := []string{"check_docs", "plan_docs"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("tools mismatch (-want +got):\n%s", diff)
	}
}

// TestPlanTool checks the plan tool against a repository on disk: it reports
// what would run, what would not, and why, without a container.
func TestPlanTool(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	readme := "# tool\n\n```sh\ngo install example.com/tool@latest\n```\n\n" +
		"```sh\ntool run notes.md\ntool add --key <api-key>\n```\n"
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	res, out, err := planTool(context.Background(), nil, planInput{Path: dir})
	if err != nil {
		t.Fatalf("planTool: %v", err)
	}
	if out.Runnable != 1 {
		t.Errorf("runnable = %d, want 1", out.Runnable)
	}
	if out.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", out.Skipped)
	}
	if len(res.Content) == 0 {
		t.Fatal("no content returned, so a caller reading only text learns nothing")
	}
	text, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok || !strings.Contains(text.Text, "would run") {
		t.Errorf("summary text = %+v, want a line saying what would run", res.Content[0])
	}
	var skipped string
	for _, doc := range out.Documents {
		for _, l := range doc.Lines {
			if l.Skip != "" {
				skipped = l.Skip
			}
		}
	}
	if !strings.Contains(skipped, "placeholder") {
		t.Errorf("skip reason = %q, want it to name the placeholder", skipped)
	}
}

// TestToolInputValidation checks that a missing path is refused by name rather
// than producing an empty result a caller would read as a clean repository.
func TestToolInputValidation(t *testing.T) {
	t.Parallel()
	check := checkToolFor(config{})
	tests := []struct {
		Name string
		Call func() error
	}{{ // Test 0: the plan tool needs a path.
		Name: "plan_docs",
		Call: func() error {
			_, _, err := planTool(context.Background(), nil, planInput{})
			return err
		},
	}, { // Test 1: the check tool needs one too.
		Name: "check_docs",
		Call: func() error {
			_, _, err := check(context.Background(), nil, checkInput{Path: "   "})
			return err
		},
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			err := test.Call()
			if err == nil {
				t.Fatalf("%s accepted an empty path", test.Name)
			}
			if !strings.Contains(err.Error(), "path is required") {
				t.Errorf("error = %v, want it to say the path is required", err)
			}
		})
	}
}
