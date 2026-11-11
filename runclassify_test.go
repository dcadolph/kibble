package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestFailureLineSkipsLocationTail checks that a failure ending on a stack
// frame or a bare path is explained by the line that says what went wrong,
// and that an ordinary last line is still the one reported.
func TestFailureLineSkipsLocationTail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Output   string
		WantLine string
	}{{ // Test 0: lux's Go panic ends on a runtime source location.
		Output: "Downloading https://www.youtube.com/watch?v=x error:\nunable to find basejs URL in playerConfig\n" +
			"main.main\n\t/go/pkg/mod/github.com/iawia002/lux@v0.24.1/main.go:13\n" +
			"runtime.goexit\n\t/usr/local/go/src/runtime/asm_arm64.s:1447",
		WantLine: "Downloading https://www.youtube.com/watch?v=x error: unable to find basejs URL in playerConfig",
	}, { // Test 1: rtk ends on the path it looked in.
		Output: "rtk: rtk-proxy not found.\nThe daemon ships as its own release archive.\n" +
			"Looked in:\n  /root/.local/bin/rtk-proxy",
		WantLine: "rtk: rtk-proxy not found.",
	}, { // Test 2: an error declaration wins over an earlier plain line.
		Output:   "starting\nError: config missing\n  at load (/app/x.js:3:1)",
		WantLine: "Error: config missing",
	}, { // Test 3: a plain two-line error keeps its last line.
		Output:   "reading config\ncannot parse key 'x'",
		WantLine: "cannot parse key 'x'",
	}, { // Test 4: a Go test log line that names a location and then the cause keeps the cause.
		Output:   "go: downloading github.com/google/go-cmp v0.7.0\npostgres.go:26: PostgreSQL is not installed",
		WantLine: "postgres.go:26: PostgreSQL is not installed",
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := failureLine(strings.Split(test.Output, "\n"))
			if diff := cmp.Diff(test.WantLine, got); diff != "" {
				t.Errorf("line mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
