package main

import (
	"strings"
	"testing"
)

// TestNpmFailureLine pins which line of an npm failure a reader is shown. npm
// prefixes every line it prints, so the word error never starts one and the
// old declaration rules matched nothing. Every npm failure therefore fell
// through to the last line, which npm reserves for the path of a debug log
// inside a container that no longer exists. The corpus surfaced this on fd:
// the reported cause was "A complete log of this run can be found in", and the
// actual cause was two lines above it.
func TestNpmFailureLine(t *testing.T) {
	t.Parallel()

	// Captured verbatim from `npm install -g fd-find` on node:22-slim.
	out := `npm error code 1
npm error path /usr/local/lib/node_modules/fd-find
npm error command failed
npm error command sh -c node download.js
npm error Missing required commands: wget.
npm notice
npm notice New major version of npm available! 10.9.8 -> 12.0.2
npm notice To update run: npm install -g npm@12.0.2
npm error A complete log of this run can be found in: /root/.npm/_logs/2026-09-09T02_53_06_649Z-debug-0.log`

	got := buildFailLine(strings.Split(out, "\n"))
	if !strings.Contains(got, "Missing required commands: wget") {
		t.Errorf("fail line = %q, want the sentence naming the cause", got)
	}
	for _, never := range []string{"A complete log", "npm notice", "npm error code", "npm error path"} {
		if strings.Contains(got, never) {
			t.Errorf("fail line = %q, want it to skip %q", got, never)
		}
	}
}

// TestNpmFailureLineFallsBack checks an npm failure whose only lines are the
// structural ones, so the picker still says something rather than nothing.
func TestNpmFailureLineFallsBack(t *testing.T) {
	t.Parallel()

	out := `npm error code EBADPLATFORM
npm error notsup Unsupported platform for fd-find@10.3.0: wanted {"os":"linux","arch":"x64"}
npm error A complete log of this run can be found in: /root/.npm/_logs/x-debug-0.log`

	got := buildFailLine(strings.Split(out, "\n"))
	if !strings.Contains(got, "Unsupported platform") {
		t.Errorf("fail line = %q, want the notsup sentence", got)
	}
}
