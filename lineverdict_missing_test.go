package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

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

	// entryModule is what vite printed in a sweep when the file it builds from
	// was absent, color codes included, around the name it gives.
	entryModule := func(name string) string {
		const dist = "file:///root/kibble-npm/node_modules/"
		return "transforming...\nBuild failed with 1 error:\n\n" +
			"\x1b[31m[UNRESOLVED_ENTRY] \x1b[0mCannot resolve entry module " + name + ".\n\n" +
			"    at #build (" + dist + "rolldown/dist/shared/rolldown-AohEFLrh.mjs:146:34)\n" +
			"    at async CAC.<anonymous> (" + dist + "vite/dist/node/cli.js:789:3) {\n" +
			"  errors: [Getter/Setter]\n}\n"
	}

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
	}, { // Test 22: A traceback header after a progress line still reads as the crash it is.
		Cmd: "MMD_CLI_JOBS=1 python3 scripts/bench_compare.py",
		Output: "   flowchart_small... Traceback (most recent call last):\n" +
			"  File \"/work/repo/scripts/bench_compare.py\", line 10, in <module>\n" +
			"    main()\n" +
			"  File \"/usr/lib/python3.13/subprocess.py\", line 1972, in _execute_child\n" +
			"FileNotFoundError: [Errno 2] No such file or directory: 'npx'\n",
		Code: 1, WantStatus: StatusSkipped, WantReason: ReasonMissingDependency,
		WantDetail: "reported by the tool that invoked it",
	}, { // Test 23: A traceback ending at a closed stdin is interactive.
		Cmd: "dotdrop import ~/.zshrc",
		Output: "Apply these changes? [y/N] ? Traceback (most recent call last):\n" +
			"  File \"/usr/bin/dotdrop\", line 8, in <module>\n" +
			"    sys.exit(main())\n" +
			"  File \"/usr/lib/python3/dotdrop/dotdrop.py\", line 90, in ask\n" +
			"    resp = input(qmsg)\n" +
			"EOFError: EOF when reading a line\n",
		Code: 1, WantStatus: StatusSkipped, WantReason: ReasonInteractive,
		WantDetail: "waited for input",
	}, { // Test 24: A line-start traceback ending in another error stays a failure.
		Cmd: "python3 tool.py",
		Output: "Traceback (most recent call last):\n" +
			"  File \"tool.py\", line 3, in <module>\n" +
			"KeyError: 'x'\n",
		Code: 1, WantStatus: StatusFail, WantDetail: "crashed with exit 1",
	}, { // Test 25: A Node module error with no traceback stays a failure.
		Cmd:    "nve 18 node app.js",
		Output: "Error: Cannot find module 'node:path'\n",
		Code:   1, WantStatus: StatusFail,
	}, { // Test 26: A bundler's entry file the command never named is blocked.
		Cmd:    "vite build",
		Output: entryModule("index.html"),
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "reads index.html, which no documented step creates: ",
	}, { // Test 27: An entry file the command passed is the document's gap.
		Cmd:    "vite build --ssr src/entry-server.js",
		Output: entryModule("src/entry-server.js"),
		Code:   1, WantStatus: StatusGap,
		WantDetail: "references src/entry-server.js, which no documented step creates: ",
	}, { // Test 28: A name with several dots is captured whole, without the period.
		Cmd:    "vite build",
		Output: entryModule("vite.config.ts"),
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "reads vite.config.ts, which no documented step creates: ",
	}, { // Test 29: A quoted entry path is read without its quotes.
		Cmd:    "vite build",
		Output: entryModule(`"src/main.ts"`),
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "reads src/main.ts, which no documented step creates: ",
	}, { // Test 30: An entry with no extension names no file, so the failure stands.
		Cmd:    "vite build",
		Output: entryModule("main"),
		Code:   1, WantStatus: StatusFail,
	}, { // Test 31: Rollup's wording for the same absent entry file reads the same.
		Cmd:    "vite build",
		Output: "error during build:\nCould not resolve entry module \"index.html\".\n",
		Code:   1, WantStatus: StatusBlocked, WantReason: ReasonMissingFixture,
		WantDetail: "reads index.html, which no documented step creates: ",
	}, { // Test 32: Older Rollup wraps the name in parentheses, and it reads the same.
		Cmd:    "rollup src/main.js -f cjs",
		Output: "[!] Error: Could not resolve entry module (src/main.js).\n",
		Code:   1, WantStatus: StatusGap,
		WantDetail: "references src/main.js, which no documented step creates: ",
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

// TestClassifyMissingScriptPackage pins the rule that reads Node failing to
// find a package the script on the command line imports. The first output is
// what zx printed in a sweep. A package missing under a file the command never
// named, under installed code, or under a file elsewhere that only ends like
// the argument stays a failure, and so does a run that printed the loader
// error and then died of something else.
func TestClassifyMissingScriptPackage(t *testing.T) {
	t.Parallel()

	const (
		stack = "    at packageResolve (node:internal/modules/esm/resolve:772:81)\n" +
			"    at ModuleLoader.import (node:internal/modules/esm/loader:679:21) {\n" +
			"  code: 'ERR_MODULE_NOT_FOUND'\n}\n"
		typeError = "TypeError: Cannot read properties of undefined (reading 'port')\n" +
			"    at file:///work/repo/opt.mjs:3:1\n\nNode.js v26.13.0\n"
	)
	noPackage := func(pkg, importer string) string {
		return "Error [ERR_MODULE_NOT_FOUND]: Cannot find package '" + pkg +
			"' imported from " + importer + "\n" + stack
	}

	tests := []struct {
		WantStatus Status
		WantReason Reason
		WantDetail string
		Cmd        string
		Cwd        string
		Output     string
		Code       int
		Truncated  bool
	}{{ // Test 0: A script a runner compiled from the named document is blocked.
		Cmd:        "zx docs/markdown.md",
		Cwd:        "/work/repo",
		Output:     noPackage("chalk", "/work/repo/docs/markdown.md.mjs"),
		WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
		WantDetail: "imports chalk, which Node could not find",
	}, { // Test 1: A script named outright is blocked, with no directory on the marker.
		Cmd:        "node app.js",
		Output:     noPackage("express", "/work/repo/app.js"),
		WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
		WantDetail: "imports express, which Node could not find",
	}, { // Test 2: A leading ./ on the named script does not hide it.
		Cmd:        "node ./src/app.mjs --port 8080",
		Cwd:        "/work/repo",
		Output:     noPackage("@scope/pkg", "/work/repo/src/app.mjs"),
		WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
		WantDetail: "imports @scope/pkg, which Node could not find",
	}, { // Test 3: A package the tool's installed code lacks is the tool's defect.
		Cmd:        "tool build site.config.js",
		Output:     noPackage("chalk", "/usr/local/lib/node_modules/tool/dist/index.js"),
		WantStatus: StatusFail,
	}, { // Test 4: A named script under node_modules is installed code all the same.
		Cmd:        "node node_modules/tool/cli.js",
		Output:     noPackage("chalk", "/work/repo/node_modules/tool/cli.js"),
		WantStatus: StatusFail,
	}, { // Test 5: A file the command never named settles nothing in the line's favor.
		Cmd:        "node app.js",
		Output:     noPackage("chalk", "/work/repo/lib/util.js"),
		WantStatus: StatusFail,
	}, { // Test 6: A name that only ends like the named script is another file.
		Cmd:        "node app.js",
		Output:     noPackage("chalk", "/work/repo/myapp.js"),
		WantStatus: StatusFail,
	}, { // Test 7: A file inside a directory named like the script is another file.
		Cmd:        "zx docs/markdown.md",
		Output:     noPackage("chalk", "/work/repo/docs/markdown.md.d/run.mjs"),
		WantStatus: StatusFail,
	}, { // Test 8: A command naming no script has no importer to match.
		Cmd:        "tool serve",
		Output:     noPackage("chalk", "/work/repo/serve.js"),
		WantStatus: StatusFail,
	}, { // Test 9: A crash before the loader error still convicts.
		Cmd:        "node app.js",
		Output:     "fatal error: out of memory\n" + noPackage("chalk", "/work/repo/app.js"),
		WantStatus: StatusFail, WantDetail: "crashed with exit 1",
	}, { // Test 10: A workspace tool's own source ends like the argument and is not it.
		Cmd:        "tool build src/index.js",
		Cwd:        "/work/repo",
		Output:     noPackage("chalk", "/work/repo/packages/tool/src/index.js"),
		WantStatus: StatusFail,
	}, { // Test 11: The same holds for a tool installed outside node_modules.
		Cmd:        "tool lint index.js",
		Cwd:        "/work/repo",
		Output:     noPackage("chalk", "/opt/tool/index.js"),
		WantStatus: StatusFail,
	}, { // Test 12: A caught optional import followed by the real error convicts.
		Cmd:        "node opt.mjs",
		Cwd:        "/work/repo",
		Output:     noPackage("fsevents", "/work/repo/opt.mjs") + typeError,
		WantStatus: StatusFail,
	}, { // Test 13: A caught optional import does not hide the file the document never made.
		Cmd: "node opt2.mjs data.json",
		Cwd: "/work/repo",
		Output: noPackage("fsevents", "/work/repo/opt2.mjs") +
			"Error: ENOENT: no such file or directory, open 'data.json'\n",
		WantStatus: StatusGap, WantDetail: "references data.json",
	}, { // Test 14: Of two scripts, the one that ended the line decides.
		Cmd:        "node a.mjs; node b.mjs",
		Cwd:        "/work/repo",
		Output:     noPackage("chalk", "/work/repo/a.mjs") + typeError,
		WantStatus: StatusFail,
	}, { // Test 15: A script named through the parent directory is resolved first.
		Cmd:        "node ../app.mjs",
		Cwd:        "/work/repo/sub",
		Output:     noPackage("chalk", "/work/repo/app.mjs"),
		WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
	}, { // Test 16: A directory named like the script does not hide the script inside it.
		Cmd:        "node demo.mjs",
		Cwd:        "/work/repo/demo.mjs.org",
		Output:     noPackage("chalk", "/work/repo/demo.mjs.org/demo.mjs"),
		WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
	}, { // Test 17: A script passed as a flag's value is named all the same.
		Cmd:        "tool --config=app.mjs",
		Cwd:        "/work/repo",
		Output:     noPackage("chalk", "/work/repo/app.mjs"),
		WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
	}, { // Test 18: A script named by its full path is named.
		Cmd:        "node /work/repo/src/./app.js",
		Cwd:        "/tmp",
		Output:     noPackage("chalk", "/work/repo/src/app.js"),
		WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
	}, { // Test 19: Two added extensions make another file, such as a test beside the script.
		Cmd:        "tool src/app",
		Cwd:        "/work/repo",
		Output:     noPackage("chalk", "/work/repo/src/app.test.js"),
		WantStatus: StatusFail,
	}, { // Test 20: A process a signal killed did not end on the loader error.
		Cmd:        "node app.js",
		Cwd:        "/work/repo",
		Output:     noPackage("chalk", "/work/repo/app.js"),
		Code:       137,
		WantStatus: StatusFail,
	}, { // Test 21: An uncaught loader error carries Node's header and version banner.
		Cmd: "node app.mjs",
		Cwd: "/work/repo",
		Output: "node:internal/modules/esm/resolve:873\n" +
			"  throw new ERR_MODULE_NOT_FOUND(packageName, fileURLToPath(base), null);\n" +
			"        ^\n\n" + noPackage("chalk", "/work/repo/app.mjs") + "\nNode.js v22.11.0\n",
		WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
	}, { // Test 22: A program the tool could not start is still the container's gap.
		Cmd:        "node run.mjs",
		Cwd:        "/work/repo",
		Output:     "Error: spawn ava ENOENT\n" + noPackage("chalk", "/work/repo/run.mjs"),
		WantStatus: StatusSkipped, WantReason: ReasonMissingDependency,
	}, { // Test 23: The directory the line ran in decides which file a relative name means.
		Cmd:        "node app.js",
		Cwd:        "/work/repo/sub",
		Output:     noPackage("chalk", "/work/repo/app.js"),
		WantStatus: StatusFail,
	}, { // Test 24: A directory the line itself enters first is where its script is.
		Cmd:        "cd examples && node demo.mjs",
		Cwd:        "/work/repo",
		Output:     noPackage("chalk", "/work/repo/examples/demo.mjs"),
		WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
	}, { // Test 25: A pushd and a runner in front of the script read the same way.
		Cmd:        "pushd examples && npx tsx src/main.ts",
		Cwd:        "/work/repo",
		Output:     noPackage("chalk", "/work/repo/examples/src/main.ts"),
		WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
	}, { // Test 26: After the line changes directory the old one no longer names the script.
		Cmd:        "cd examples && node demo.mjs",
		Cwd:        "/work/repo",
		Output:     noPackage("chalk", "/work/repo/demo.mjs"),
		WantStatus: StatusFail,
	}, { // Test 27: A directory only a variable names leaves a relative script unplaced.
		Cmd:        "cd \"$DEMO_DIR\" && node demo.mjs",
		Cwd:        "/work/repo",
		Output:     noPackage("chalk", "/work/repo/demo.mjs"),
		WantStatus: StatusFail,
	}, { // Test 28: An indented message of the script's own after the stack is not Node's.
		Cmd:        "node opt.mjs",
		Cwd:        "/work/repo",
		Output:     noPackage("fsevents", "/work/repo/opt.mjs") + "  error: port must be a number\n",
		WantStatus: StatusFail,
	}, { // Test 29: An indented sentence that starts with "at" is not a stack frame.
		Cmd:        "node opt.mjs",
		Cwd:        "/work/repo",
		Output:     noPackage("fsevents", "/work/repo/opt.mjs") + "  at least one entry is required\n",
		WantStatus: StatusFail,
	}, { // Test 30: Output the session cut short cannot show what the line ended on.
		Cmd:        "node app.js",
		Cwd:        "/work/repo",
		Output:     noPackage("chalk", "/work/repo/app.js"),
		Truncated:  true,
		WantStatus: StatusFail,
	}, { // Test 31: A script named by ./ and no extension is still a path.
		Cmd:        "node ./server",
		Cwd:        "/work/repo",
		Output:     noPackage("express", "/work/repo/server.js"),
		WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
	}, { // Test 32: A bare frame, a url property, and a pre-release banner are Node's own.
		Cmd: "node app.mjs",
		Cwd: "/work/repo",
		Output: "Error [ERR_MODULE_NOT_FOUND]: Cannot find package 'chalk' imported from " +
			"/work/repo/app.mjs\n    at packageResolve (node:internal/modules/esm/resolve:772:81)\n" +
			"    at async file:///work/repo/app.mjs:1:1 {\n  code: 'ERR_MODULE_NOT_FOUND',\n" +
			"  url: 'file:///work/repo/node_modules/chalk'\n}\n\nNode.js v23.0.0-pre\n",
		WantStatus: StatusBlocked, WantReason: ReasonMissingDependency,
	}}

	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			code := 1
			if test.Code != 0 {
				code = test.Code
			}
			lr := classifyLineResult(lineResult{Cmd: test.Cmd}, kplan.PlanLine{},
				lineOutcome{code: code, cwd: test.Cwd, output: test.Output, truncated: test.Truncated},
				false, nil, lineTimeout)
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

// TestClassifyExampleLongOutput pins that a line whose output passed the
// session's cap is not read as ending where the kept lines end. A script that
// printed a caught loader error and then died of something else past the cap
// stays a failure, and the same loader error inside the cap is still blocked.
func TestClassifyExampleLongOutput(t *testing.T) {
	t.Parallel()

	const (
		loader = "Error [ERR_MODULE_NOT_FOUND]: Cannot find package 'chalk' imported from " +
			"/work/repo/build.mjs\n" +
			"    at packageResolve (node:internal/modules/esm/resolve:772:81)\n" +
			"    at ModuleLoader.import (node:internal/modules/esm/loader:679:21) {\n" +
			"  code: 'ERR_MODULE_NOT_FOUND'\n}\n"
		typeError = "TypeError: Cannot read properties of undefined (reading 'port')\n" +
			"    at file:///work/repo/build.mjs:9:1\n\nNode.js v22.11.0\n"
	)
	tests := []struct {
		WantStatus Status
		Progress   int
		After      string
	}{{ // Test 0: A loader error that ends short output is blocked.
		Progress: 20, WantStatus: StatusBlocked,
	}, { // Test 1: A later error inside the cap is read and convicts.
		Progress: 20, After: typeError, WantStatus: StatusFail,
	}, { // Test 2: A later error the cap dropped convicts all the same.
		Progress: 195, After: typeError, WantStatus: StatusFail,
	}, { // Test 3: Output that fills the cap exactly was not cut.
		Progress: 195, WantStatus: StatusBlocked,
	}, { // Test 4: One kept line past the cap was cut, whatever it said.
		Progress: 195, After: "done\n", WantStatus: StatusFail,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			plan := &kplan.Plan{
				Repo: "repo", Binaries: []string{"zx"},
				Steps: []kplan.PlanStep{{ID: "b1", Lines: []kplan.PlanLine{{Cmd: "zx build.mjs"}}}},
			}
			out := "KIBBLE-BUILD CODE=0\nKIBBLE-STEP b1 START\n" +
				strings.Repeat("built one more page\n", test.Progress) + loader + test.After +
				"KIBBLE-LINE b1:0 CODE=1 CWD=/work/repo\nKIBBLE-DONE\n"
			res := classifyExample(InstallStep{Repo: "repo", Kind: "example"}, plan, out, nil, 0,
				lineTimeout, sessionEnd{})
			var got []Status
			for _, s := range res.example.Steps {
				for _, l := range s.Lines {
					got = append(got, l.Status)
				}
			}
			if diff := cmp.Diff([]Status{test.WantStatus}, got); diff != "" {
				t.Errorf("line statuses mismatch (-want +got):\n%s\ndetail %q", diff, res.Detail)
			}
		})
	}
}
