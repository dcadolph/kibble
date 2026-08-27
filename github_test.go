package main

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestReadmePath checks the annotation path, which must name the README file
// the repository actually has rather than assuming README.md.
func TestReadmePath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Dir  string
		Name string
		Want string
	}{{ // Test 0: the checkout root with the usual name.
		Dir: ".", Name: "README.md", Want: "README.md",
	}, { // Test 1: a lowercase readme keeps its own name.
		Dir: ".", Name: "readme.md", Want: "readme.md",
	}, { // Test 2: a subdirectory carries the name with it.
		Dir: "tools/kibble", Name: "README.MD", Want: "tools/kibble/README.MD",
	}, { // Test 3: an absolute path falls back to the checkout root.
		Dir: "/abs/path", Name: "readme.md", Want: "readme.md",
	}, { // Test 4: an unknown name falls back to the conventional one.
		Dir: ".", Name: "", Want: "README.md",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, readmePath(test.Dir, test.Name)); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
