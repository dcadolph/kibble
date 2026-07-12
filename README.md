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
- Runs each `git clone` recipe too: the clone and the build lines that follow it in the
  same code block, with GitHub SSH remotes rewritten to HTTPS for the keyless container.
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
| `-image`    | `golang:1.26` | Container image for clean-room installs.        |
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
`make install`, and whatever lands in the install directory is smoke-tested. A brew step
is verified against its tap, so a renamed or missing formula is caught, but nothing is
installed. A build that exceeds the timeout is reported as `TIMEOUT`, never as a failure,
so a slow network does not fail a build that would otherwise pass.

After a successful install, kibble compares the README against the binary itself. Every
flag cited on a line that invokes the binary, and every subcommand those lines call, is
checked against the collected `--help` output. A flag the binary no longer has, or a
subcommand it rejects, is reported as `DRIFT`. The check is conservative: it only reads
lines that invoke the binary by name, so flags shown for other tools do not count, and
`DRIFT` fails the run only under `-strict`.

## Examples

An install that builds is only half the promise. The other half is the quickstart: the
lines a new user actually types next. kibble replays them. After installing the binary,
it copies the repository into the container and runs the README's example blocks in one
session, in document order, so files and environment carry between blocks the way they do
in a real terminal. A block that no longer works, a flag that changed, a command that
prints an error where the docs promised output, all fail as `example` in CI.

The judgment of which lines to run is deterministic and conservative, because a check
that cries wolf is worse than no check. A line is skipped, never failed, when it needs
something a clean container cannot honestly provide: a placeholder the reader must fill
in (`<api-key>`, `age1bob...`), an interactive sign-in, a terminal, an API key, a local
server, a file the docs reference but never create, or a subcommand that opens a shell or
serves forever. Skips are reported with their reason, so the coverage is honest about what
it did and did not run. A command the docs say exits nonzero, such as a linter that fails
when it finds something, is recognized and passes on that exit.

When the heuristics cannot settle a call, a `.kibble.yml` at the repository root does.
It writes fixtures with real contents, exports environment, substitutes placeholder text,
installs extra packages, and forces a specific line to run, skip, or run in the
background with a readiness probe. Every choice lives in the file, so the run stays
reproducible and the engine stays the thing that decides pass or fail.

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
    - match: mytool check
      nonzeroOk: true
```

Preview the plan without running anything with `-plan`, which prints, per repository, the
exact lines kibble would run, the ones it would skip and why, and the fixtures and
packages the session needs. Turn the whole layer off with `-examples=false`.

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

## Roadmap

- Install brew formulas for real instead of only verifying they exist.
- JUnit XML output for CI annotations.

## Why "kibble"

Dogfooding means using your own product before you ship it. kibble is the bowl: it feeds
your docs back to a fresh machine and tells you whether they still go down.

## License

MIT. See [LICENSE](LICENSE).
