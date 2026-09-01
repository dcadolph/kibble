# How kibble judges

This is the design behind the verdicts: what runs, what each status means, and why the
rules are shaped the way they are. The front page says what kibble does. This page says
why it can be trusted to say so.

## What runs

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
- Checks each documented brew formula against its tap, and with `-brew-install` runs the
  install for real in a Homebrew container and smoke-tests what lands.
- Smoke-tests the binary (`--version`, then `--help`) to confirm it runs, not just builds.
- Checks that every flag and subcommand the README cites still exists in the binary's
  help output, and reports what has drifted.
- Replays the README's quickstart and usage blocks in one clean session after install,
  so a documented example that no longer works fails in CI, not in a user's terminal.

The report is colored when a person is watching and plain the moment it is piped, so a
build log never fills with escape sequences. `NO_COLOR` turns it off everywhere. There
are no prompts. kibble's home is a CI job, and a tool that stops to ask a question there
either hangs on a closed pipe or needs a flag to defeat it.

## Install checks

kibble verifies `go install` steps end to end: the module resolves, it builds from zero,
and the binary runs. A `git clone` step runs as the documented recipe, meaning the clone
line plus the lines that follow it in the same code block, such as `cd` and
`make install`, and whatever the recipe produces is smoke-tested. A build that exceeds
the timeout is reported as `TIMEOUT`, never as a failure unless `-strict` promotes it, so
a slow network does not fail a build that would otherwise pass.

A package install runs the documented line verbatim, so what kibble verifies is the
command a reader would actually type, flags and all. The bin directory is compared before
and after, so the binary is found even when the package does not name it: a `cargo
install` of ripgrep is checked by running `rg`, and an `npm install -g` of typescript by
running `tsc`. A local build such as `cargo install --path .` is left to the clone recipe,
which already covers it.

## The brew doctrine

A brew step is looked up in its tap by default: the formula API, the cask index, and the
alias directories, since `brew install rich` resolves through an alias to `rich-cli` and
a name absent from the canonical list can still be exactly what a reader should type. A
lookup that finds nothing reports `GAP` and never `FAIL`, because an index can say a name
is not indexed and cannot say a documented line is broken. `-brew-install` settles it: the
line runs in Homebrew's own Linux container, the bin directory is diffed around the
install, and whatever appeared is smoke-tested. Only that run can fail a brew step. It
costs minutes per formula, so it is opt-in, and a documented cask is skipped with its
reason, since a cask installs a macOS application a Linux container cannot judge.

## The image follows the step

Commands such as `cargo`, `npm`, and `pip` name the toolchain a project builds with, and
the repository's own manifests settle what a bare `make install` leaves open, so a Rust
or Python project is built with the tools its docs were written for instead of failing in
a Go image. When nothing identifies a toolchain the run falls back to `-image`, and if
the recipe then reaches for a command that is not there, the step is a `SKIP` naming the
missing tool rather than a `FAIL`. A verdict about your docs is never a verdict about
kibble's own gaps. When kibble itself cannot run a step, because the daemon is
unreachable or an image will not pull, that is `ERROR`, kept separate from `FAIL` for the
same reason. A repository too large to stream whole is an `ERROR` too: a file kibble
failed to deliver is indistinguishable from one the docs never create, and guessing
between them is the one mistake this tool must not make. Generated directories such as
`node_modules`, `vendor`, `build`, and `target` are left out of that stream, so the
budget goes to source a document might actually name.

## SKIP, GAP, and FAIL

A `SKIP` says kibble could not judge the line. A `GAP` says it did judge it, and the
document is incomplete: the line names a file, directory, or setting that no documented
step creates. `cp skill/SKILL.md ~/.claude/skills/tool/SKILL.md` fails for every reader
when nothing creates that directory first, and a command that exits reporting a setting
the document never mentions is the same kind of hole. Both used to disappear into the
same silent skip as a placeholder the reader is meant to fill in, which is the difference
a gap draws: a placeholder is your reader's job, a gap is yours. Because a document can
also expect a reader to bring their own file, a gap reports and counts but does not fail
a run unless `-strict` is set. A brew name that no index knows lands here too: the
document may be fine and the lookup short, and the difference is settled by installing
it, not by asserting one.

`FAIL` is reserved for a documented line that was executed and did not work. Nothing
kibble merely looked up can produce one. That rule exists because it was broken once, in
public: an index that knew only canonical formula names called `brew install rich` broken
when the line works, and the check had never run it.

Execution must also reproduce the reader's context before it convicts. A block whose
introducing sentence scopes it to another operating system, the FAQ that says "if you
are using BSD sed, the default on macOS, use the following", is skipped rather than run
on Linux and blamed. A failing line whose error names a file the command itself asked
for reads as a gap, the document assuming a file the reader brings, never as the tool
breaking. And a pipeline whose search fed it nothing in the fresh session settles
nothing about the document, because the same recipe on a reader's tree finds its
matches.

## Documents beyond the README

Instructions outlive the README they started in. A docs tree is where install and usage
steps go once the front page fills up, and it rots faster because nobody reads it on the
way past, so every document that walks a reader through commands is replayed in its own
session and reported under its own name. Kibble picks the README, the `docs/` tree, and
top-level guides named for the job they do, such as `GETTING_STARTED.md` or
`UPGRADING.md`. It leaves alone the documents that describe a project rather than
instruct a reader: contributing guides, changelogs, security policies, decision records,
and anything written for an agent. A document with no runnable recipe is skipped before
a container is spent on it. Settle the rest in [.kibble.yml](CONFIG.md) with `docs` to
add one and `skipDocs` to drop one.

Some documents name a file the reader is meant to supply, such as a calendar export or a
profile they write themselves. That reads as a gap because kibble cannot tell it from a
step somebody forgot. Settle it in `.kibble.yml`: give the session a fixture when a
small valid file makes the line run for real, or a step rule with a `skip` reason when
only the reader can supply the thing.

## Example replay

An install that builds is only half the promise. The other half is the quickstart: the
lines a new user actually types next. kibble replays them. After running the documented
install, whichever ecosystem it belongs to, it copies the repository into the container
and runs the README's example blocks in one session, in document order, so files and
environment carry between blocks the way they do in a real terminal. The session
verifies the documented tool actually landed on PATH before replaying anything, and a
line that calls a documented tool the install does not provide, such as a conda
alternative next to a cargo install, is skipped rather than failed. A block that no
longer works, a flag that changed, a command that prints an error where the docs
promised output, all fail as `example` in CI.

The judgment of which lines to run is deterministic and conservative, because a check
that cries wolf is worse than no check. A line is skipped, never failed, when it needs
something a clean container cannot honestly provide: a placeholder the reader must fill
in (`<api-key>`, `age1bob...`), an interactive sign-in, a terminal, an API key, a local
server, a file the docs reference but never create, a variable such as `$HISTFILE` that
only an interactive shell sets, or a subcommand that opens a shell or serves forever.
Skips are reported with their reason, so the coverage is honest about what it did and
did not run. A command the docs say exits nonzero, such as a linter that fails when it
finds something, is recognized and passes on that exit.

A tool that watches or serves is read from how its documentation introduces it. A README
opening with "nodemon is a tool that helps develop Node.js based applications by
automatically restarting the node application when file changes are detected" has said
that every invocation runs until something stops it, so kibble skips them rather than
waiting out the timeout on each one and learning nothing. Only prose counts: a code
block containing `tool serve` says the tool has a serve subcommand, which is a different
fact. Questions about the tool itself, `--version` and `--help`, still run. To have a
watcher actually verified rather than skipped, give it `background: true` and a
`readyLog`, which is the one thing kibble cannot infer.

## Drift, in both directions

After a successful install, kibble compares the documentation against the binary itself.
Every flag cited on a line that invokes the binary, every flag documented in a markdown
flag table, and every subcommand those lines call, is checked against the collected
`--help` output. A flag the binary no longer has, or a subcommand it rejects, is
reported as `DRIFT`.

Flags and subcommands are read from the whole documentation set, since a reference page
cites more of a tool than its front page does and drifts faster. Two things keep that
from inventing failures. A flag is judged against the screen of the subcommand it was
cited on, so one cited on a subcommand whose help never arrived is reported as
unverified rather than missing. And a rejection has to be a parser naming that
subcommand as the one it does not have. The exit code is not enough on its own: a
subcommand that takes arguments rather than flags answers `--help` by complaining about
the argument and exiting nonzero, while plainly existing. So the name in the message is
what decides, and a probe that settles nothing leaves the subcommand alone.

The comparison needs a name to attribute cited flags to, so it covers the installs that
name their binary up front: every `go install`, whose binary follows from the module
path, and every package install whose package names the binary it provides. The two
cases it does not cover are honest gaps rather than silent ones. A `git clone` recipe
names no binary in advance, and a package that installs a differently named binary, as
ripgrep provides `rg`, gives the docs nothing to key on; both are still installed and
smoke-tested, they are just not compared against the README. The check is conservative
in the other direction too: command lines only count when they invoke the binary by
name, so flags shown for other tools do not count, and `DRIFT` fails the run only under
`-strict`. kibble runs this check on its own README, so its flag table rots loudly, not
silently.

The reverse question, whether every command the binary advertises appears in the docs,
is `doc-coverage`, and it catches the more common rot: a feature ships and nobody writes
it down. The public surface is whatever `--help` prints, so a command a parser hides is
deliberately private and never counted, and no allowlist is needed to say so. A command
missing from every document is a `GAP`. A command documented somewhere but not in the
README is reported as a count beside the total, never as a failure, because which
commands earn README space is an editorial call and one finding per command would bury
the one worth reading.

A repository kibble cannot read is a failure, not a quiet skip. A path with no README, a
path that does not exist, and a malformed `.kibble.yml` each report `ERROR` and exit
non-zero, so a typo in a workflow cannot pass as a green check that verified nothing.

## Where the model sits

Default kibble never consults a model. `-suggest` is the one place a model appears: it
sends the lines the engine could not settle, and only those, to a model you configure,
then prints a `.kibble.yml` for you to review and commit. The model never decides
whether anything passed. It classifies documented lines, once, into a file you read and
commit; after that the run is deterministic again and no model is consulted. kibble with
no key configured verifies exactly as much as kibble with one.

The MCP server follows the same line. `plan_docs` answers what kibble would run and why
it would leave the rest alone, takes milliseconds, and needs no Docker. `check_docs`
runs the documented steps for real, which costs minutes and belongs where a CI run does.
There is deliberately no tool for writing a `.kibble.yml`: `plan_docs` returns every
skip with its reason, and the caller is already a model, so it can write a better config
than kibble's own prompt would. Wrapping a model call inside a tool call for a model is
a layer that only adds cost.

## The corpus and the mutations

`corpus/repos.tsv` pins real repositories, ripgrep and nodemon and friends, each to a
commit and to the verdict counts a person verified by hand. A scheduled workflow runs
kibble against all of them and fails when any count moves, because a moved count means
kibble's judgment changed, not the repository. This is the regression suite for the part
unit tests cannot reach: the rules can all do exactly what they say and still be wrong
about a repository nobody anticipated. Adding an entry means reading that repository's
documentation and verifying the expected counts first.

The corpus proves kibble does not cry wolf. `corpus/mutations.tsv` proves the opposite
half: rot cannot pass silently. Each row corrupts one documented line of a pinned
repository, a typo in a crate name, a brew formula that no longer resolves, and asserts
kibble reaches the named verdict on the damage, in a finding that names the mutated
token. The same property holds below the integration layer: a unit suite corrupts a
healthy document one edit at a time, a flag typo, a renamed subcommand, a command whose
only mention was deleted, and checks that the untouched tree is green and every single
edit flips it. A verifier is only worth trusting when both directions are pinned:
correct documentation passes, and one edit of rot is caught.
