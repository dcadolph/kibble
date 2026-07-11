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
- Smoke-tests the binary (`--version`, then `--help`) to confirm it runs, not just builds.
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
myrepo  go-install  PASS    28s   myrepo version 1.4.0
myrepo  brew        SKIP    0s    not executed yet
myrepo  git-clone   SKIP    0s    not executed yet

1 pass, 0 fail, 2 other of 3 install steps
```

| Flag       | Default       | What                                     &nbsp; |
| ---------- | ------------- | ----------------------------------------------- |
| `-image`   | `golang:1.26` | Container image for clean-room installs.        |
| `-timeout` | `240s`        | Per-step build timeout.                         |
| `-workers` | `3`           | Max concurrent installs.                        |
| `-json`    | `false`       | Emit results as JSON to stdout.                 |
| `-version` | `false`       | Print the version and exit.                     |
| `-strict`  | `false`       | Also fail on timeouts and smoke-test failures.  |

## What it checks today

kibble verifies `go install` steps end to end: the module resolves, it builds from zero,
and the binary runs. Homebrew and `git clone` steps are detected and listed, but not yet
executed, so they show as `SKIP` rather than a false pass. A build that exceeds the
timeout is reported as `TIMEOUT`, never as a failure, so a slow network does not fail a
build that would otherwise pass.

## Roadmap

- Execute the Homebrew and `git clone` install paths.
- Check that flags and subcommands named in the docs still exist in the CLI.
- Run quickstart and example blocks, not just install steps.
- Ship as a GitHub Action so a stale README fails a pull request.

## Why "kibble"

Dogfooding means using your own product before you ship it. kibble is the bowl: it feeds
your docs back to a fresh machine and tells you whether they still go down.

## License

MIT. See [LICENSE](LICENSE).
