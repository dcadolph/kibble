package main

import (
	"fmt"
	"strings"
	"testing"

	kplan "github.com/dcadolph/kibble/internal/plan"
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

// TestNpmLocalInstall checks that a local npm install of a named package runs
// in a scratch project, where its binaries are looked for, while a global or
// bare install is left as documented. zx's README says `npm install zx`, and
// run in zx's own checkout it failed with `Tracker "idealTree" already
// exists`.
func TestNpmLocalInstall(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Raw       string
		WantLocal bool
	}{{ // Test 0: Zx's documented local install.
		Raw: "npm install zx", WantLocal: true,
	}, { // Test 1: The short form with a dev flag is local too.
		Raw: "npm i -D chalk", WantLocal: true,
	}, { // Test 2: A global install is unchanged.
		Raw: "npm install -g zx", WantLocal: false,
	}, { // Test 3: The long global flag is unchanged.
		Raw: "npm install --global zx", WantLocal: false,
	}, { // Test 4: A bare install is the checkout's own dependencies.
		Raw: "npm install", WantLocal: false,
	}, { // Test 5: A compound line is not rewritten.
		Raw: "npm install zx && zx --version", WantLocal: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			script := pkgScriptFor(InstallStep{Kind: "npm-install", Raw: test.Raw, Module: "zx", Binary: "zx"}, 60)
			hasInit := strings.Contains(script, "npm init -y")
			hasBin := strings.Contains(script, `BINDIR="$HOME/kibble-npm/node_modules/.bin"`)
			if hasInit != test.WantLocal || hasBin != test.WantLocal {
				t.Errorf("npm init %v, local bin dir %v, want both %v:\n%s", hasInit, hasBin, test.WantLocal, script)
			}
			if !test.WantLocal && !strings.Contains(script, `BINDIR="$(npm prefix -g)/bin"`) {
				t.Errorf("non-local install lost the global bin dir:\n%s", script)
			}
		})
	}
}

// TestSessionNpmLocalInstall checks that the example session runs a local npm
// install in the scratch project and can find its binary on PATH.
func TestSessionNpmLocalInstall(t *testing.T) {
	t.Parallel()
	plan := &kplan.Plan{
		Installs: []kplan.PlanInstall{{Cmd: "npm install zx", Ecosystem: "node"}},
		Binaries: []string{"zx"},
		Steps:    []kplan.PlanStep{{ID: "b1", Lines: []kplan.PlanLine{{Cmd: "zx --version"}}}},
	}
	script, _ := sessionScript(plan, 60, 30)
	for _, want := range []string{"npm init -y", `cd "$HOME/kibble-npm"`, ":$HOME/kibble-npm/node_modules/.bin"} {
		if !strings.Contains(script, want) {
			t.Errorf("session script lacks %q", want)
		}
	}
}
