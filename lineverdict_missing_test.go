package main

import (
	"fmt"
	"strings"
	"testing"

	kplan "github.com/dcadolph/kibble/internal/plan"
)

// TestClassifyMissingNames pins the rules that read a tool naming what it
// lacked. The outputs are what the tools printed in a sweep of real
// repositories, trimmed of repeated stack frames. A name the command passed is
// the document's gap, a path it never named is blocked, a bare word is a
// program the container lacks, and git's own history errors are blocked. The
// last cases are true defects and crashes that no rule here may excuse.
func TestClassifyMissingNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Creators   map[string]string
		WantStatus Status
		WantReason Reason
		WantDetail string
		Cmd        string
		Output     string
		Cwd        string
		Code       int
		Inherited  bool
	}{{ // Test 0: A traceback naming a file passed through a flag value is the document's gap.
		Cmd: "repren --patterns=patfile --word-breaks myfile1 myfile2 myfile3",
		Output: `Traceback (most recent call last):
  File "/root/.local/bin/repren", line 10, in <module>
    sys.exit(main())
             ~~~~^^
  File "/root/.local/share/uv/tools/repren/lib/python3.14/site-packages/repren/repren.py", line 1628, in _run_cli
    with open(options.pat_file, "rb") as f:
         ~~~~^^^^^^^^^^^^^^^^^^^^^^^^
FileNotFoundError: [Errno 2] No such file or directory: 'patfile'
`,
		Code: 1, WantStatus: StatusGap, WantDetail: "references patfile,",
	}, { // Test 1: A traceback naming a path the command never named is blocked.
		Cmd: "gita context auto",
		Output: `Traceback (most recent call last):
  File "/usr/local/bin/gita", line 6, in <module>
    sys.exit(main())
             ~~~~^^
  File "/usr/local/lib/python3.14/site-packages/gita/utils.py", line 154, in replace_context
    Path(auto.with_name(f"{new}.context")).write_text("")
    ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~^^^^
  File "/usr/local/lib/python3.14/pathlib/__init__.py", line 771, in open
    return io.open(self, mode, buffering, encoding, errors, newline)
           ~~~~~~~^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
FileNotFoundError: [Errno 2] No such file or directory: '/root/.config/gita/auto.context'
`,
		Code: 1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "reads /root/.config/gita/auto.context,",
	}, { // Test 2: A traceback naming a bare word is a program the container lacks.
		Cmd: "python3 scripts/quality_bench.py --engine both --no-history-log",
		Output: `Traceback (most recent call last):
  File "/work/repo/scripts/quality_bench.py", line 3382, in <module>
    main()
    ~~~~^^
  File "/work/repo/scripts/quality_bench.py", line 49, in run
    return subprocess.run(
           ~~~~~~~~~~~~~~^
  File "/usr/lib/python3.13/subprocess.py", line 1972, in _execute_child
    raise child_exception_type(errno_num, err_msg, err_filename)
FileNotFoundError: [Errno 2] No such file or directory: 'npx'
`,
		Code: 1, WantStatus: StatusSkipped, WantReason: ReasonMissingDependency,
		WantDetail: "reported by the tool that invoked it",
	}, { // Test 3: Node's ENOENT naming a path the command never named is blocked.
		Cmd: "c8 check-coverage --lines 95 --functions 95 --branches 95",
		Output: `node:fs:1590
  const result = binding.readdir(
                         ^

Error: ENOENT: no such file or directory, scandir '/work/repo/coverage/tmp'
    at readdirSync (node:fs:1590:26)
    at Report._loadReports (/usr/local/lib/node_modules/c8/lib/report.js:450:24)
    at exports.handler (/usr/local/lib/node_modules/c8/lib/commands/check-coverage.js:35:11) {
  errno: -2,
  code: 'ENOENT',
  syscall: 'scandir',
  path: '/work/repo/coverage/tmp'
}

Node.js v22.23.2
`,
		Code: 1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "reads /work/repo/coverage/tmp,",
	}, { // Test 4: A path the command names with a trailing slash is the document's gap.
		Cmd:    "tach report my_package/",
		Output: "Report failed: The path 'my_package' does not exist.\n",
		Code:   1, WantStatus: StatusGap, WantDetail: "references my_package,",
	}, { // Test 5: A tool that could not start a program it runs names the container's gap.
		Cmd:    `lla --search "regex:^func.*\("`,
		Output: "\n✗ Error\n\n  Failed to run ripgrep: No such file or directory (os error 2)\n\n",
		Code:   1, WantStatus: StatusSkipped, WantReason: ReasonMissingDependency,
		WantDetail: "reported by the tool that invoked it",
	}, { // Test 6: Node's spawn ENOENT is the same evidence.
		Cmd: "nve 8 ava",
		Output: `spawn ava ENOENT

Invalid input syntax.

It should be:

  nve [OPTIONS...] VERSION,... [COMMAND] [ARGS...]
`,
		Code: 1, WantStatus: StatusSkipped, WantReason: ReasonMissingDependency,
		WantDetail: "reported by the tool that invoked it",
	}, { // Test 7: git saying HEAD does not exist means the session's repository has no history.
		Cmd: "commit-and-tag-version --packageFiles build.gradle --bumpFiles build.gradle",
		Output: `fatal: ambiguous argument 'HEAD': unknown revision or path not in the working tree.
Use '--' to separate paths from revisions, like this:
'git <command> [<revision>...] -- [<file>...]'
`,
		Code: 1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "needs git history",
	}, { // Test 8: git's wording for a branch with no commits is the same evidence.
		Cmd:    "tool changelog",
		Output: "fatal: your current branch 'main' does not have any commits yet\n",
		Code:   128, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "needs git history",
	}, { // Test 9: git log's wording for a missing HEAD is the same evidence.
		Cmd:    "tool log",
		Output: "fatal: bad default revision 'HEAD'\n",
		Code:   128, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "needs git history",
	}, { // Test 10: A crash that does not end in a missing name still fails.
		Cmd: "tool search x",
		Output: `no results
Traceback (most recent call last):
  File "/usr/local/bin/tool", line 8, in <module>
    sys.exit(main())
KeyError: 'x'
`,
		Code: 1, WantStatus: StatusFail, WantDetail: "crashed with exit 1",
	}, { // Test 11: A missing file earlier in a chained traceback does not excuse its last error.
		Cmd: "tool load",
		Output: `Traceback (most recent call last):
  File "/usr/local/bin/tool", line 8, in load
FileNotFoundError: [Errno 2] No such file or directory: 'cache.json'

During handling of the above exception, another exception occurred:

Traceback (most recent call last):
  File "/usr/local/bin/tool", line 12, in load
KeyError: 'x'
`,
		Code: 1, WantStatus: StatusFail, WantDetail: "crashed with exit 1",
	}, { // Test 12: An unknown flag is the document's defect, never a missing name.
		Cmd: "wait4x dns A example.com --expected-ip 93.184.216.34",
		Output: `Error: unknown flag: --expected-ip
Usage:
  wait4x dns A ADDRESS [-- command [args...]] [flags]

Aliases:
  A, a

Flags:
      --expect-ip stringArray   Expect ip address
`,
		Code: 1, WantStatus: StatusFail,
	}, { // Test 13: A module Node cannot load is a defect, never a missing name.
		Cmd: "nve 12 npm test",
		Output: `internal/modules/cjs/loader.js:818
  throw err;
  ^

Error: Cannot find module 'node:path'
Require stack:
- /usr/local/lib/node_modules/npm/lib/cli.js
- /usr/local/lib/node_modules/npm/bin/npm-cli.js
    at Function.Module._resolveFilename (internal/modules/cjs/loader.js:815:15)
    at Module.require (internal/modules/cjs/loader.js:887:19) {
  code: 'MODULE_NOT_FOUND',
  requireStack: [
    '/usr/local/lib/node_modules/npm/lib/cli.js',
    '/usr/local/lib/node_modules/npm/bin/npm-cli.js'
  ]
}
`,
		Code: 1, WantStatus: StatusFail,
	}, { // Test 14: An errno name is not a program, so ENOENT with no path settles nothing.
		Cmd:    "tool sync",
		Output: "Error: ENOENT: no such file or directory\n",
		Code:   1, WantStatus: StatusFail,
	}, { // Test 15: A remote the reader configures first is blocked, not a failure.
		Cmd: "rclone lsd remote:",
		Output: "2026/10/22 16:24:10 CRITICAL: Failed to create file system for \"remote:\": " +
			"didn't find section in config file (\"remote\")\n",
		Code: 1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "names a remote or configuration section the reader configures first: ",
	}, { // Test 16: A refusal that names no remote or section still fails.
		Cmd:    "rclone lsd remote:",
		Output: "Failed to lsd: permission denied\n",
		Code:   1, WantStatus: StatusFail,
	}, { // Test 17: uv's missing input that the command passed is the document's gap.
		Cmd:    "uv add -r requirements-docs.in -c requirements-docs.txt --group docs",
		Output: "error: File not found: requirements-docs.in\n",
		Code:   2, WantStatus: StatusGap, WantDetail: "references requirements-docs.in, which no documented step creates: ",
	}, { // Test 18: uv's missing input the command never named is blocked.
		Cmd:    "uv add fastapi",
		Output: "error: File not found: requirements-docs.in\n",
		Code:   2, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "reads requirements-docs.in, which no documented step creates: ",
	}, { // Test 19: uv's missing bare word is a program the container lacks.
		Cmd:    "uv add fastapi",
		Output: "error: File not found: helper\n",
		Code:   2, WantStatus: StatusSkipped, WantReason: ReasonMissingDependency,
	}, { // Test 20: A crash naming a passed file in an inherited directory is blocked.
		Cmd: "repren --patterns=patfile --word-breaks myfile1",
		Output: `Traceback (most recent call last):
  File "/root/.local/bin/repren", line 10, in <module>
    sys.exit(main())
FileNotFoundError: [Errno 2] No such file or directory: 'patfile'
`,
		Code: 1, Cwd: "/work/repo/example", Inherited: true,
		WantStatus: StatusBlocked, WantReason: ReasonInheritedState,
		WantDetail: "ran in /work/repo/example, which an earlier section entered",
	}, { // Test 21: A crash naming a file a failed earlier line was to create is blocked.
		Cmd: "repren --patterns=patfile --word-breaks myfile1",
		Output: `Traceback (most recent call last):
  File "/root/.local/bin/repren", line 10, in <module>
    sys.exit(main())
FileNotFoundError: [Errno 2] No such file or directory: 'patfile'
`,
		Code: 1, Creators: map[string]string{"patfile": "make-patterns > patfile"},
		WantStatus: StatusBlocked, WantReason: ReasonDependsOnSkipped,
		WantDetail: "references patfile, which an earlier line that did not pass may create: " +
			"make-patterns > patfile",
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			lr := classifyLineResult(lineResult{Cmd: test.Cmd}, kplan.PlanLine{},
				lineOutcome{code: test.Code, output: test.Output, cwd: test.Cwd, inherited: test.Inherited,
					creators: test.Creators}, false, nil, lineTimeout)
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
