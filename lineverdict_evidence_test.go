package main

import (
	"fmt"
	"strings"
	"testing"

	kplan "github.com/dcadolph/kibble/internal/plan"
)

// TestClassifyRegisteredAndEnvironment pins the rules for a name the reader
// registers, a configuration the reader supplies, a terminal, an architecture,
// and a rate limit. The outputs are what the tools printed in a sweep of real
// repositories, trimmed of repeated lines. The last cases are true defects that
// none of these rules may excuse.
func TestClassifyRegisteredAndEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		WantStatus Status
		WantReason Reason
		WantDetail string
		Cmd        string
		Output     string
		Code       int
	}{{ // Test 0: A preset the command names is one the reader defines first.
		Cmd:    "lla --preset source-files",
		Output: "Error: Filter(\"Unknown preset 'source-files'\")\n",
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "names a preset the reader defines first",
	}, { // Test 1: A plugin the command names is one the reader defines first.
		Cmd:    "lla update my_plugin",
		Output: "\n✗ Plugin Error\n\n  Plugin 'my_plugin' not found\n\n",
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "names a plugin the reader defines first",
	}, { // Test 2: The same preset text for a command that never names it is a failure.
		Cmd:    "lla --preset other",
		Output: "Error: Filter(\"Unknown preset 'source-files'\")\n",
		Code:   1, WantStatus: StatusFail,
	}, { // Test 3: A configuration that would not resolve is the reader's project to supply.
		Cmd: "cspell .",
		Output: `Failed to process "/work/repo/packages/cspell-trie-lib/api/README.md" Configuration Loader Error: Failed to resolve configuration file: "@cspell/dict-es-es/cspell-ext.json" referenced from "./packages/cspell-trie-lib/cspell.json"
CSpell: Files checked: 1494 (1747 from cache), skipped: 282, Issues found: 0 in 253 files with 257 errors.
`,
		Code: 1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "could not load a configuration",
	}, { // Test 4: ENXIO is a terminal the container lacks.
		Cmd:    "lla -F",
		Output: `Error: Io(Os { code: 6, kind: Uncategorized, message: "No such device or address" })` + "\n",
		Code:   1, WantStatus: StatusSkipped, WantReason: ReasonInteractive,
	}, { // Test 5: An emulator missing its loader is an architecture the container lacks.
		Cmd:    "nve --arch=x32 8 npm test",
		Output: "qemu-i386: Could not open '/lib/ld-linux.so.2': No such file or directory\n",
		Code:   255, WantStatus: StatusSkipped, WantReason: ReasonOtherPlatform,
		WantDetail: "needs an architecture the container does not have",
	}, { // Test 6: A rate limit is a refusal that needs credentials.
		Cmd:    "gh api /user",
		Output: "API rate limit exceeded",
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonNeedsCredentials,
	}, { // Test 7: An unknown flag is the document's defect, not a registered name.
		Cmd: "wait4x dns A example.com --expected-ip 93.184.216.34",
		Output: `Error: unknown flag: --expected-ip
Usage:
  wait4x dns A ADDRESS [-- command [args...]] [flags]

Aliases:
  A, a

Flags:
      --expect-ip stringArray   Expected IPv4 addresses to match against A records
  -h, --help                    help for A
`,
		Code: 1, WantStatus: StatusFail,
	}, { // Test 8: An unknown argument is the document's defect.
		Cmd: "redocly --version",
		Output: `Unknown argument: version
                            [choices: "basename", "title"] [default: "basename"]
`,
		Code: 1, WantStatus: StatusFail,
	}, { // Test 9: A missing module is a defect that no configuration rule may excuse.
		Cmd: "nve 12 npm test",
		Output: `internal/modules/cjs/loader.js:818
  throw err;
  ^

Error: Cannot find module 'node:path'
Require stack:
- /usr/local/lib/node_modules/npm/lib/cli.js
    at Function.Module._resolveFilename (internal/modules/cjs/loader.js:815:15)
  code: 'MODULE_NOT_FOUND',
}
`,
		Code: 1, WantStatus: StatusFail,
	}, { // Test 10: A registry the tool names no name for is one the reader defines first.
		Cmd: "aqua rm foo,suzuki-shunsuke/foo",
		Output: `Oct 20 15:50:23.812 ERR aqua failed program=aqua version=v2.64.0 ` +
			`env=linux/arm64 package_name=foo,suzuki-shunsuke/foo ` +
			`error="find a package from registries: unknown registry"` + "\n",
		Code: 1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "reached for a registry the reader defines first: ",
	}, { // Test 11: An unknown command is the document's defect, not a registered name.
		Cmd:    "tool frob",
		Output: "unknown command \"frob\"\n",
		Code:   1, WantStatus: StatusFail,
	}, { // Test 12: A name after a colon is a name, so only the named form decides.
		Cmd:    "tool run",
		Output: "Error: unknown profile: dev\n",
		Code:   1, WantStatus: StatusFail,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			lr := classifyLineResult(lineResult{Cmd: test.Cmd}, kplan.PlanLine{},
				lineOutcome{code: test.Code, output: test.Output}, false, nil, lineTimeout)
			if lr.Status != test.WantStatus || lr.Reason != test.WantReason {
				t.Errorf("verdict = %s %q, want %s %q (detail %q)",
					lr.Status, lr.Reason, test.WantStatus, test.WantReason, lr.Detail)
			}
			if !strings.Contains(lr.Detail, test.WantDetail) {
				t.Errorf("detail = %q, want it to contain %q", lr.Detail, test.WantDetail)
			}
		})
	}
}
