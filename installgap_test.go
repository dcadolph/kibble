package main

import (
	"strings"
	"testing"
)

// TestInstallEnvironmentIsNotTheDocument checks the two ways a clean container
// fails an install that the document got right. kibble's doctrine is that it
// never blames a document for a tool kibble lacks, and both of these were
// reaching FAIL anyway. The corpus caught it: fd failed one scheduled run and
// passed the next two with nothing changed in between, which is not something
// documentation does.
func TestInstallEnvironmentIsNotTheDocument(t *testing.T) {
	t.Parallel()

	// Captured verbatim from `npm install -g fd-find` on node:22-slim, where
	// the package's own installer names what the image is missing.
	missing := `npm error code 1
npm error command sh -c node download.js
npm error Missing required commands: wget.
npm error A complete log of this run can be found in: /root/.npm/_logs/x-debug-0.log`

	name, found := missingCommand(missing)
	if !found || name != "wget" {
		t.Errorf("missingCommand = (%q, %v), want (\"wget\", true)", name, found)
	}

	// Captured verbatim from the same image with the network removed, which is
	// what a registry blip looks like from inside the container.
	blip := `npm error code EAI_AGAIN
npm error syscall getaddrinfo
npm error errno EAI_AGAIN
npm error request to https://registry.npmjs.org/left-pad failed, reason: getaddrinfo EAI_AGAIN registry.npmjs.org
npm error A complete log of this run can be found in: /root/.npm/_logs/x-debug-0.log`

	if !reNetworkError.MatchString(blip) {
		t.Error("a failed npm fetch does not read as a network error, so it convicts the document")
	}
	// The fetch failure must not be mistaken for a missing program, since the
	// two lead to different verdicts and only one of them is right here.
	if _, found := missingCommand(blip); found {
		t.Error("a network failure was read as a missing command")
	}
}

// TestRealBreakStillFails guards the other direction. Loosening these rules
// must not turn a genuinely broken install into an excuse, so an ordinary
// compile error stays a failure and names nothing about the environment.
func TestRealBreakStillFails(t *testing.T) {
	t.Parallel()

	broken := `error[E0432]: unresolved import ` + "`serde::Deserialize`" + `
error: could not compile ` + "`mytool`" + ` (lib) due to 1 previous error`

	if _, found := missingCommand(broken); found {
		t.Error("a compile error was read as a missing command")
	}
	if reNetworkError.MatchString(broken) {
		t.Error("a compile error was read as a network failure")
	}
	if got := buildFailLine(strings.Split(broken, "\n")); !strings.Contains(got, "unresolved import") {
		t.Errorf("fail line = %q, want the compiler's own error", got)
	}
}
