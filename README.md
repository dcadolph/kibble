<p align="center">
  <img src="assets/kibble-banner.png" alt="kibble" width="100%">
</p>

<h1 align="center">kibble</h1>

<p align="center">The proving ground for your docs.</p>

<p align="center">
  <a href="https://github.com/dcadolph/kibble/releases"><img
    src="https://img.shields.io/github/v/release/dcadolph/kibble" alt="Release"></a>
  <img src="https://img.shields.io/github/go-mod/go-version/dcadolph/kibble" alt="Go version">
  <a href="LICENSE"><img
    src="https://img.shields.io/badge/license-MIT-blue" alt="License"></a>
</p>

Your README tells people to run `go install ...`, then some setup, then a quickstart.
Every one of those steps rots the moment the code moves, and you are the last to know
because your machine already has everything installed. kibble eats your own dog food: it
runs the documented steps in a clean container from zero, so a broken install fails in
CI instead of in a new user's terminal.

## What it does

kibble reads a repository's README, finds the install commands, and runs each one in a
fresh container with nothing preinstalled. It smoke-tests the installed binary and
reports which steps a brand-new user could actually complete.

- Extracts install commands from fenced, inline, and indented code, so a step written
  inline in prose is not missed.
- Runs each `go install` in a clean `golang` container from zero.
- Runs documented package installs the same way, verbatim as written:
  `cargo install`, `npm install -g`, `yarn global add`, `pipx install`, and
  `uv tool install`.
- Runs each `git clone` recipe too: the clone and the build lines that follow it in the
  same code block, with GitHub SSH remotes rewritten to HTTPS for the keyless container.
- Picks the image from the toolchain the step assumes, so a Rust project builds with
  `cargo` and a Node project with `npm`, and finds the installed binary wherever that
  toolchain puts it, even when it is not named after the package.
- Never blames a document for a tool kibble lacks: a missing toolchain is a skip with a
  reason, so a red result means the documented steps are broken, not that kibble was
  short a compiler.
- Verifies each documented brew formula exists in its tap, without installing it.
- Smoke-tests the binary (`--version`, then `--help`) to confirm it runs, not just builds.
- Checks that every flag and subcommand the README cites still exists in the binary's
  help output, and reports what has drifted.
- Replays the README's quickstart and usage blocks in one clean session after install,
  so a documented example that no longer works fails in CI, not in a user's terminal.
- Prints a table or JSON, and exits non-zero when a documented install fails.

## Install

```sh
go install github.com/dcadolph/kibble@latest
```

Requires Docker, or a compatible runtime, on the host.

## Usage

Point it at one or more repository directories:

```sh
kibble ./myrepo
kibble ./repo-a ./repo-b
```

Example output:

```
REPO    KIND        STATUS  TIME  DETAIL
myrepo  brew        PASS    1s    formula exists (install not attempted)
myrepo  example     PASS    22s   15 lines ran, 9 skipped
myrepo  flag-check  PASS    0s    9 cited flags ok, 4 subcommands cited
myrepo  git-clone   PASS    41s   myrepo version 1.4.0
myrepo  go-install  PASS    28s   myrepo version 1.4.0

5 pass, 0 fail, 0 other of 5 install steps
```

| Flag        | Default       | What                                     &nbsp; |
| ----------- | ------------- | ----------------------------------------------- |
| `-image`    | `golang:1.26` | Fallback image when no toolchain is detected.   |
| `-timeout`  | `240s`        | Per-step build timeout.                         |
| `-workers`  | `3`           | Max concurrent installs.                        |
| `-json`     | `false`       | Emit results as JSON to stdout.                 |
| `-version`  | `false`       | Print the version and exit.                     |
| `-strict`   | `false`       | Also fail on timeouts and smoke-test failures.  |
| `-examples` | `true`        | Replay README example blocks in the container.  |
| `-plan`     | `false`       | Print the example plans as JSON and exit.       |

## What it checks today

kibble verifies `go install` steps end to end: the module resolves, it builds from zero,
and the binary runs. A `git clone` step runs as the documented recipe, meaning the clone
line plus the lines that follow it in the same code block, such as `cd` and
`make install`, and whatever the recipe produces is smoke-tested. A brew step
is verified against its tap, so a renamed or missing formula is caught, but nothing is
installed. A build that exceeds the timeout is reported as `TIMEOUT`, never as a failure,
so a slow network does not fail a build that would otherwise pass.

A package install runs the documented line verbatim, so what kibble verifies is the
command a reader would actually type, flags and all. The bin directory is compared before
and after, so the binary is found even when the package does not name it: a `cargo
install` of ripgrep is checked by running `rg`, and an `npm install -g` of typescript by
running `tsc`. A local build such as `cargo install --path .` is left to the clone recipe,
which already covers it.

The image follows the step. Commands such as `cargo`, `npm`, and `pip` name the
toolchain a project builds with, and the repository's own manifests settle what a bare
`make install` leaves open, so a Rust or Python project is built with the tools its docs
were written for instead of failing in a Go image. When nothing identifies a toolchain
the run falls back to `-image`, and if the recipe then reaches for a command that is not
there, the step is a `SKIP` naming the missing tool rather than a `FAIL`. A verdict about
your docs is never a verdict about kibble's own gaps. When kibble itself cannot run a
step, because the daemon is unreachable or an image will not pull, that is `ERROR`, kept
separate from `FAIL` for the same reason.

After a successful install, kibble compares the README against the binary itself. Every
flag cited on a line that invokes the binary, every flag documented in a markdown flag
table, and every subcommand those lines call, is checked against the collected `--help`
output. A flag the binary no longer has, or a subcommand it rejects, is reported as
`DRIFT`. The check is conservative: command lines only count when they invoke the binary
by name, so flags shown for other tools do not count, and `DRIFT` fails the run only
under `-strict`. kibble runs this check on its own README, so the flag table above rots
loudly, not silently.

## Examples

An install that builds is only half the promise. The other half is the quickstart: the
lines a new user actually types next. kibble replays them. After running the documented
install, whichever ecosystem it belongs to, it copies the repository into the container
and runs the README's example blocks in one session, in document order, so files and
environment carry between blocks the way they do in a real terminal. The session verifies
the documented tool actually landed on PATH before replaying anything, and a line that
calls a documented tool the install does not provide, such as a conda alternative next to
a cargo install, is skipped rather than failed. A block that no longer works, a flag that changed, a command that
prints an error where the docs promised output, all fail as `example` in CI.

The judgment of which lines to run is deterministic and conservative, because a check
that cries wolf is worse than no check. A line is skipped, never failed, when it needs
something a clean container cannot honestly provide: a placeholder the reader must fill
in (`<api-key>`, `age1bob...`), an interactive sign-in, a terminal, an API key, a local
server, a file the docs reference but never create, a variable such as `$HISTFILE` that
only an interactive shell sets, or a subcommand that opens a shell or serves forever.
Skips are reported with their reason, so the coverage is honest about what it did and
did not run. A command the docs say exits nonzero, such as a linter that fails when it
finds something, is recognized and passes on that exit.

When the heuristics cannot settle a call, a `.kibble.yml` at the repository root does.
It writes fixtures with real contents, exports environment, substitutes placeholder text,
installs extra packages, and forces a specific line to run, skip, or run in the
background with a readiness probe. Every choice lives in the file, so the run stays
reproducible and the engine stays the thing that decides pass or fail.

The most common use is a scanner or linter whose whole job is to exit nonzero when it
finds something. Point it at real code and it does exactly that, which is the tool working,
not the docs breaking. One `nonzeroOk` line settles it.

```yaml
version: 1
examples:
  packages: [age]
  substitutions:
    "<api-key>": test-key-1234
  fixtures:
    - path: config.yaml
      contents: |
        setting: value
  steps:
    - match: mytool serve
      background: true
      readyLog: listening on
    - match: mytool scan          # a scanner exits nonzero on findings by design
      nonzeroOk: true
    - match: mytool *.go          # a documented form the clean session cannot run
      skip: file-glob loading is unreliable here
    - match: mytool demo          # force a line the planner would skip
      run: true
```

Preview the plan without running anything with `-plan`, which prints, per repository, the
exact lines kibble would run, the ones it would skip and why, and the fixtures and
packages the session needs. The preview is the floor, not the ceiling: at run time the
session may downgrade more lines to skips for reasons only execution can see, such as a
command that turns out to need a terminal. Turn the whole layer off with
`-examples=false`.

## Use it in CI

Add a workflow that fails a pull request when a documented install breaks:

```yaml
name: docs
on: pull_request
jobs:
  kibble:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: dcadolph/kibble@v1
        with:
          repo: .
          # version: v0.3.0   # pin a version, or leave for latest
          # args: -strict      # fail on timeouts and smoke failures too
```

The runner already has Docker, so kibble spins its clean-room containers there.

In a workflow, kibble speaks GitHub natively. A failed install or example is annotated on
the exact README line that broke, so it shows up inline in the pull request the way a
failing test does. Doc drift becomes a warning annotation, and the job summary gets the
full results table, readable without opening a log.

## Roadmap

- Install brew formulas for real instead of only verifying they exist.
- JUnit XML output for CI systems that are not GitHub.
- Optional LLM assist for the calls the heuristics cannot settle, such as classifying an
  ambiguous block as command or illustration. Strictly additive: the engine stays
  deterministic, decides every pass and fail itself, and is fully useful with zero keys.

## Why "kibble"

Dogfooding means using your own product before you ship it. kibble is the bowl: it feeds
your docs back to a fresh machine and tells you whether they still go down.

## More tools

- [preen](https://github.com/dcadolph/preen), split a messy working tree into clean, atomic git commits
- [slop-chop](https://github.com/dcadolph/slop-chop), strip the AI tells out of your writing
- [vamoose](https://github.com/dcadolph/vamoose), route time off through approval, then tell the team
- [whodar](https://github.com/dcadolph/whodar), find who to talk to about X across your work tools

## License

MIT. See [LICENSE](LICENSE).
