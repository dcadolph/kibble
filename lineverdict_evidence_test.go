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
	}, { // Test 2: A preset the command never named came from defaults or configuration.
		Cmd:    "lla --preset other",
		Output: "Error: Filter(\"Unknown preset 'source-files'\")\n",
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "reached for a preset the reader defines first, which the command never named",
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
	}, { // Test 12: No named form reads a name after a colon, so the line falls through to FAIL.
		Cmd:    "tool run",
		Output: "Error: unknown profile: dev\n",
		Code:   1, WantStatus: StatusFail,
	}, { // Test 13: Told to define an API in the config, the reader writes that first.
		Cmd: "redocly bundle",
		Output: "No APIs were provided. Specify an API via the command argument " +
			"or define one in your config.\n",
		Code: 1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "points to a configuration entry the reader writes first",
	}, { // Test 14: An entry told to be added under a key is configuration the reader writes.
		Cmd: "redocly generate-client",
		Output: "No API to generate. Add a `client` block or `clientOutput` under an " +
			"`apis:` entry, or pass <api>\n",
		Code: 1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "points to a configuration entry the reader writes first",
	}, { // Test 15: A bare word where a path was wanted is a name defined in configuration.
		Cmd:    "redocly stats core@v1",
		Output: "Please provide a valid path.\n",
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "passed core@v1 where the tool wanted a path",
	}, { // Test 16: A file-shaped operand that is refused stays a failure.
		Cmd:    "redocly stats spec.yaml",
		Output: "Please provide a valid path.\n",
		Code:   1, WantStatus: StatusFail,
	}, { // Test 17: No operand at all stays a failure.
		Cmd:    "redocly stats",
		Output: "Please provide a valid path.\n",
		Code:   1, WantStatus: StatusFail,
	}, { // Test 18: A configuration file that will not import is the reader's project to supply.
		Cmd: `mdlint "**/*.md"`,
		Output: "Error: Unable to use configuration file '/work/repo/test/x/.lintrc.cjs'; " +
			"Unable to import module\n",
		Code: 2, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "could not load a configuration",
	}, { // Test 19: A relative path the tool says does not exist is a file the reader lacks.
		Cmd: "redocly eject-generator php --update",
		Output: "Nothing to update: generators/php/index.ts does not exist. " +
			"Eject first.\n",
		Code: 1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "generators/php/index.ts",
	}, { // Test 20: An unknown flag named config is the document's defect.
		Cmd:    "redocly bundle --config",
		Output: "Unknown argument: --config\n",
		Code:   1, WantStatus: StatusFail,
	}, { // Test 21: Nothing to do for this profile is a profile the reader defines first.
		Cmd:    "dotdrop install",
		Output: "[WARN] no dotfile to install for this profile (\"87271e6263ea\")\n",
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "reached for a profile the reader defines first",
	}, { // Test 22: A profile only the tool names is one the reader defines first.
		Cmd:    "dotdrop update --key f_vimrc",
		Output: "[ERR] no such profile \"87271e6263ea\"\n",
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "reached for a profile the reader defines first, which the command never named",
	}, { // Test 23: No such profile the command passed names one the reader defines first.
		Cmd:    "dotdrop install -p work",
		Output: "[ERR] no such profile \"work\"\n",
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "names a profile the reader defines first",
	}, { // Test 24: No plugins installed is a plugin the reader installs first.
		Cmd:    "lla update",
		Output: "\n  No plugins are currently installed. Run 'lla install' to add one.\n",
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "reached for a plugin the reader defines first",
	}, { // Test 25: A missing file is not a registered name.
		Cmd:    "tool load config.yaml",
		Output: "Error: open config.yaml: no such file or directory\n",
		Code:   1, WantStatus: StatusGap,
		WantDetail: "references config.yaml, which no documented step creates",
	}, { // Test 26: An unknown flag named profile is the document's defect.
		Cmd:    "tool run --profile dev",
		Output: "Error: unknown flag: --profile\n",
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

// TestClassifyShownOutput checks that a nonzero exit is documented behavior
// only when the tool printed the output the document shows under the command.
func TestClassifyShownOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantStatus Status
		WantDetail string
		Shown      []string
		Output     string
		Code       int
	}{{ // Test 0: The refusal the document shows, printed under the tool's own name.
		Shown:      []string{"rm: target '/' skipped, unsafe directory scope"},
		Output:     "safe-rm: target '/' skipped, unsafe directory scope\n",
		Code:       1,
		WantStatus: StatusVerified,
		WantDetail: "exit 1 is documented behavior: the document shows this output",
	}, { // Test 1: A shown line that the tool did not print leaves the failure.
		Shown:      []string{"all done"},
		Output:     "safe-rm: target '/' skipped, unsafe directory scope\n",
		Code:       1,
		WantStatus: StatusFail,
	}, { // Test 2: A matching line on exit 127 is not an ordinary exit.
		Shown:      []string{"rm: target '/' skipped, unsafe directory scope"},
		Output:     "safe-rm: target '/' skipped, unsafe directory scope\n",
		Code:       127,
		WantStatus: StatusSkipped,
	}, { // Test 3: A shown line under 12 characters is too short to be evidence.
		Shown:      []string{"error"},
		Output:     "error\n",
		Code:       1,
		WantStatus: StatusFail,
	}, { // Test 4: A prose label is not a program name, so an echoed command proves nothing.
		Shown:      []string{"Or: tool schema generate"},
		Output:     "tool schema generate\nmake: *** [schema] Error 1\n",
		Code:       2,
		WantStatus: StatusFail,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			lr := classifyLineResult(lineResult{Cmd: `SAFE_RM_SCOPE="$HOME" safe-rm -rf /`},
				kplan.PlanLine{Shown: test.Shown},
				lineOutcome{code: test.Code, output: test.Output}, false, nil, lineTimeout)
			if lr.Status != test.WantStatus {
				t.Errorf("verdict = %s %q, want %s (detail %q)",
					lr.Status, lr.Reason, test.WantStatus, lr.Detail)
			}
			if !strings.Contains(lr.Detail, test.WantDetail) {
				t.Errorf("detail = %q, want it to contain %q", lr.Detail, test.WantDetail)
			}
		})
	}
}

// TestClassifyCheckModeCrash checks that a check-mode line which crashed is
// not a documented finding, while one that reported findings is.
func TestClassifyCheckModeCrash(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantStatus Status
		Output     string
	}{{ // Test 0: A traceback on a check-mode line stays a failure.
		Output: "Traceback (most recent call last):\n  File \"/usr/bin/tool\", line 8, in <module>\n" +
			"    sys.exit(main())\nKeyError: 'rules'\n",
		WantStatus: StatusFail,
	}, { // Test 1: A plain finding report is the documented exit.
		Output:     "docs/a.md:3: MD001 heading levels should only increment by one\n1 problem\n",
		WantStatus: StatusVerified,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			lr := classifyLineResult(lineResult{Cmd: "tool lint ."}, kplan.PlanLine{NonzeroOK: true},
				lineOutcome{code: 1, output: test.Output}, false, nil, lineTimeout)
			if lr.Status != test.WantStatus {
				t.Errorf("verdict = %s %q, want %s (detail %q)",
					lr.Status, lr.Reason, test.WantStatus, lr.Detail)
			}
		})
	}
}
