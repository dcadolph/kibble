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
clean Linux container from zero, as a reader with nothing would, so a broken install
fails in CI instead of in their terminal.

That container is one environment, not every environment. kibble answers whether your
documentation works from zero in a reproducible Linux container, which is a narrower
claim than whether it works for every reader: a macOS path, an ARM host, musl, a
corporate proxy, and a private registry are all outside what it sees. The narrow claim
is the one worth making, because it is the one kibble can actually settle.

Your README tells people to run `go install ...`, then some setup, then a quickstart.
Every one of those rots the moment the code moves, and you are the last to know. kibble
reads those steps from the README and from the install documents beside it, an INSTALL
file or an installation guide under a docs tree, since that is where they go once the
front page fills up. A piped shell installer and a system package install are recorded
but not run, and reported as skips with a reason, so an install kibble declines to
execute is still visible rather than silently missed.

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

A run that finds something says where it is:

```
myrepo
  ! brew             0s  GAP  no formula, cask, or alias named "acme/tap/nope" in the c…
  – doc-coverage     0s  SKIP  no command list in the help output to check against
  ✗ example         22s  FAIL  b1 "mytool --nosuchflag" exited 2: flag provided but not d…
      myrepo/README.md:17
      $ mytool --nosuchflag
        exited 2: flag provided but not defined: -nosuchflag
  ✓ flag-check       0s  2 cited flags ok, 0 subcommands cited
  ✓ go-install      22s  v1.4.0

FAILED  a documented line ran and did not work
2 passed  1 failed  1 gap  1 other  5 checks in 45s
```

The file and line under a failure are the point. A verdict a reader has to go hunting for
in a log costs them the search, so the broken line, where it is written, and what the tool
said are printed together. In CI the same three become an annotation on that README line.

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
      - uses: dcadolph/kibble@v0.20.0
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
result is one verdict, and the boundaries between them are the product. Only one
verdict claims the documented step works:

- `VERIFIED` built or installed and the binary answered a smoke test. It is the only
  green verdict.
- `BUILT`, `RAN`, `EXISTS`, and `CROSS-ARCH` all mean a step ran or a package exists
  but nothing proved it works: `BUILT` compiled but the smoke test failed, `RAN`
  finished but produced no binary to test, `EXISTS` confirms a package is in its index
  without installing it, and `CROSS-ARCH` installed a binary for another architecture
  this host cannot run. None of them is a pass, and none is an accusation.
- `FAIL` ran and did not work, or named a package that does not exist.
- `SKIP` means kibble chose not to run the line; a machine-readable reason says why.
- `GAP` means the document is incomplete: a file, directory, or setting nothing creates.
- `DRIFT` means the docs cite a flag or subcommand the binary no longer has.
- `TIMEOUT` and `ERROR` keep slow networks and kibble's own trouble out of your verdict.

Every verdict also carries a coarse `bucket` (`works`, `unverified`, `broken`,
`doc-drift`, `not-attempted`, `inconclusive`) so a consumer can condense the fine
verdicts without hard-coding them. By default only `FAIL` and `ERROR` fail a run;
`-strict` also fails everything outside the works and not-attempted buckets, so an
unverified step, a drift, or a gap all count when you demand proof. The full reasoning,
including why a check that cries wolf is worse than no check, is in
[docs/DESIGN.md](docs/DESIGN.md).

## Why believe any of it

A verifier is worth exactly what its false positives and false negatives are worth, so
both are pinned and enforced weekly against real repositories.

`corpus/repos.tsv` holds ripgrep, fd, glow, nodemon, httpie, and rich-cli, each at a
fixed commit with the verdict counts a person read the documentation and verified by
hand. A scheduled run fails when any count moves, because a moved count means kibble's
judgment changed and the repository did not.

`corpus/mutations.tsv` runs the other direction. Each row corrupts exactly one documented
line of a pinned repository, a crate name typo, a brew formula that no longer resolves,
and requires kibble to reach a named verdict on the damage in a finding that quotes the
mutated token. A count alone would not do, since a coincidence can produce a count.

```
ripgrep   5,0,1  as expected      ripgrep mutation:  FAIL=1 naming ripgreppp
fd        7,0,0  as expected      rich-cli mutation: GAP=2  naming richhh
rich-cli  3,0,1  as expected
```

Correct documentation passes and one edit of rot flips it. Both halves are the claim; a
tool that only holds one of them is either a rubber stamp or a nuisance. The same
property is pinned below the integration layer by a unit suite that mutates a healthy
document one edit at a time and requires the untouched copy to come back clean first.
Reasoning in [docs/DESIGN.md](docs/DESIGN.md).

## What kibble does not check

The container is Linux, x86 or ARM depending on the runner, with a network and no
credentials. Documentation that only breaks on macOS or Windows, only under musl, only
behind a proxy or a corporate CA, or only with a private registry configured, is
documentation kibble cannot speak to. A block whose own prose scopes it to another
operating system is skipped and said so, rather than run and blamed. Nothing here is a
verdict about your reader's laptop; it is a verdict about a clean machine.

## Security

kibble executes commands it read out of documentation, which means **a README is
untrusted input**: anyone who can change the docs can change what runs. Every command
runs in a fresh, unprivileged, capability-dropped container with nothing mounted from
the host, and the network stays open because verifying an install is fetching it. Treat
a kibble run the way you treat a build script, and read [docs/SECURITY.md](docs/SECURITY.md)
before pointing it at a repository you do not trust.


## Configuration

Most repositories need none. When a heuristic cannot settle a call, a `.kibble.yml` at
the repository root does: fixtures, environment, substitutions, background services with
readiness probes, and per-line run or skip rules, so the run stays reproducible and the
engine stays the thing that decides pass or fail. `-suggest` has a model draft the file
for you to review; `-mcp` serves the same engine to an agent. All of it is in
[docs/CONFIG.md](docs/CONFIG.md).
## Roadmap

- JUnit XML output for CI systems that are not GitHub.

## Why "kibble"

Dogfooding means using your own product before you ship it. kibble is the bowl: it feeds
your docs back to a fresh machine and tells you whether they still go down.

## License

MIT. See [LICENSE](LICENSE).
