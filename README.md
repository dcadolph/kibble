<p align="center">
  <img src="assets/kibble-banner.png" alt="kibble" width="100%">
</p>

<h1 align="center">kibble</h1>

<p align="center">Dogfood your docs.</p>

<p align="center">
  <a href="https://github.com/dcadolph/kibble/releases"><img
    src="https://img.shields.io/github/v/release/dcadolph/kibble" alt="Release"></a>
  <img src="https://img.shields.io/github/go-mod/go-version/dcadolph/kibble" alt="Go version">
  <a href="LICENSE"><img
    src="https://img.shields.io/badge/license-MIT-blue" alt="License"></a>
</p>

Eating your own dog food means using what you ship the way a stranger would. Nobody does
it for documentation, because your machine already has everything installed and the
instructions pass by inspection. kibble is the bowl: it runs your documented steps in a
clean container from zero, as a reader with nothing would, so a broken install fails in
CI instead of in their terminal.

Your README tells people to run `go install ...`, then some setup, then a quickstart.
Every one of those rots the moment the code moves, and you are the last to know.

The stranger is not always a person now. Coding agents install tools by doing what the
README says, and they fail differently than people do. Someone who follows a broken
instruction knows they followed it correctly, reads the error, and works around the
document. An agent cannot tell a stale command from its own mistake, so it retries,
invents variants, and reports success it did not have. A line that has been wrong for six
months gets run all day by something that will never complain about it.

## Install

```sh
go install github.com/dcadolph/kibble@latest
```

Requires Docker on the host. A drop-in replacement that speaks docker's command line,
such as Podman, works through `KIBBLE_DOCKER=podman`.

## Usage

Point it at one or more repository directories, or none at all:

```sh
kibble
kibble ./myrepo
kibble ./repo-a ./repo-b
```

With no path it checks the directory you are standing in, so `cd` into a project and run
it. There are no prompts. kibble's home is a CI job, and a tool that stops to ask a
question there either hangs on a closed pipe or needs a flag to defeat it.

Example output:

```
REPO    KIND        STATUS  TIME  DETAIL
myrepo  brew        PASS    1s    formula exists (install not attempted)
myrepo  example     PASS    22s   15 lines ran, 9 skipped
myrepo  flag-check  PASS    0s    9 cited flags ok, 4 subcommands cited
myrepo  git-clone   PASS    41s   myrepo version 1.4.0
myrepo  go-install  PASS    28s   myrepo version 1.4.0

5 pass, 0 fail, 0 other of 5 checks
```

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
      - uses: dcadolph/kibble@v0.19.0
        with:
          repo: .
          # args: -strict   # fail on timeouts, smoke failures, drift, and gaps too
```

The runner already has Docker. A failed install or example is annotated on the exact
README line that broke, so it shows up inline in the pull request the way a failing test
does; doc drift becomes a warning annotation, and the job summary gets the full results
table. The action downloads the released binary for the pinned version and verifies its
checksum before running it. kibble runs on its own README this way on every commit.

## Flags

| Flag        | Default       | What                                     &nbsp; |
| ----------- | ------------- | ----------------------------------------------- |
| `-image`    | `golang:1.26` | Fallback image when no toolchain is detected.   |
| `-timeout`  | `240s`        | Per-step build timeout.                         |
| `-workers`  | `3`           | Max concurrent installs.                        |
| `-json`     | `false`       | Emit results as JSON to stdout.                 |
| `-version`  | `false`       | Print the version and exit.                     |
| `-strict`   | `false`       | Also fail on timeouts, smoke failures, drift, and gaps. |
| `-examples` | `true`        | Replay each document's example blocks in the container. |
| `-plan`     | `false`       | Print the example plans as JSON and exit.       |
| `-suggest`  | `false`       | Propose a `.kibble.yml` using a model and exit. |
| `-mcp`      | `false`       | Serve the Model Context Protocol over stdio.    |
| `-brew-install` | `false`   | Run documented brew installs for real instead of checking the formula exists. |

One dash or two, either works: `-strict` and `--strict` name the same flag.

## What the verdicts mean

kibble runs installs from zero, smoke-tests what lands, replays quickstarts in one
session, and checks cited flags and subcommands against the binary's own help. Every
result is one of seven verdicts, and the boundaries between them are the product:

- `PASS` ran and worked. `FAIL` ran and did not; nothing kibble merely looked up can
  produce one.
- `SKIP` means kibble could not judge the line, with the reason.
- `GAP` means the document is incomplete: a file, directory, or setting nothing creates.
- `DRIFT` means the docs cite a flag or subcommand the binary no longer has.
- `TIMEOUT` and `ERROR` keep slow networks and kibble's own trouble out of your verdict.

A gap, a drift, or a timeout never fails a default run; `-strict` promotes them. The
full reasoning, including why a check that cries wolf is worse than no check, is in
[docs/DESIGN.md](docs/DESIGN.md).

## Configuration

Most repositories need none. When a heuristic cannot settle a call, a `.kibble.yml` at
the repository root does: fixtures, environment, substitutions, background services with
readiness probes, and per-line run or skip rules, so the run stays reproducible and the
engine stays the thing that decides pass or fail. `-suggest` has a model draft the file
for you to review; `-mcp` serves the same engine to an agent. All of it is in
[docs/CONFIG.md](docs/CONFIG.md).

## Security

kibble executes commands it read out of documentation, which means **a README is
untrusted input**: anyone who can change the docs can change what runs. Every command
runs in a fresh, unprivileged, capability-dropped container with nothing mounted from
the host, and the network stays open because verifying an install is fetching it. Treat
a kibble run the way you treat a build script, and read [docs/SECURITY.md](docs/SECURITY.md)
before pointing it at a repository you do not trust.

## Proof

`corpus/repos.tsv` pins real repositories to hand-verified verdict counts, and a
scheduled run fails when kibble's judgment moves. `corpus/mutations.tsv` holds the other
direction: corrupt one documented line of a pinned repository and kibble must catch it,
in a finding that names the damage. Correct documentation passes and one edit of rot
flips, which is the pair a verifier has to hold. Details in
[docs/DESIGN.md](docs/DESIGN.md).

## Roadmap

- JUnit XML output for CI systems that are not GitHub.

## Why "kibble"

Dogfooding means using your own product before you ship it. kibble is the bowl: it feeds
your docs back to a fresh machine and tells you whether they still go down.

## License

MIT. See [LICENSE](LICENSE).
