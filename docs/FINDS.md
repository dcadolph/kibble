# Fixes found by kibble

This page lists merged fixes for documented commands that failed when kibble ran them, in repositories kibble had never run on before. Each failure was replayed in a clean container, and each fix was filed by kibble's author after reading the project's contributing rules. Most fixes change the documentation. One changes the code so the documented command works. Open reports join the list when they merge.

Entries are ordered by merge date, newest first.

## goreleaser/goreleaser

- Project: [goreleaser/goreleaser](https://github.com/goreleaser/goreleaser)
- Documented at: `www/content/getting-started/quick-start.md`, line 138 (lines 135 to 196), tested at `a9fb415`
- Command as written: `goreleaser build --single-target --clean`
- In a clean container: exit 1, `git doesn't contain any tags - either add a tag or use --snapshot`
- Fix: Adds `--snapshot` to the quick start's single-target build lines, which run before the guide creates a tag.
- Merged PR: [goreleaser/goreleaser#7232](https://github.com/goreleaser/goreleaser/pull/7232)
- Merged: 2026-11-01

## tach-org/tach

- Project: [tach-org/tach](https://github.com/tach-org/tach)
- Documented at: `docs/getting-started/getting-started.md`, line 76, swept at `bc92d71`
- Command as written: `tach install --pre-commit`
- In a clean container: exit 2, `tach install: error: the following arguments are required: target`
- Fix: Changes the command to `tach install pre-commit`, matching the commands reference.
- Merged PR: [tach-org/tach#961](https://github.com/tach-org/tach/pull/961)
- Merged: 2026-10-29

## pypa/pipx

- Project: [pypa/pipx](https://github.com/pypa/pipx)
- Documented at: `docs/how-to/install-pipx.rst`, line 198, swept at `9a49f19`
- Command as written: `pipx install git+https://github.com/psf/black.git@ce14fa8b497bae2b50ec48b3bd7022573a59cdb1`
- In a clean container: exit 1 on Python 3.15.0, `error: unknown type name 'PyFutureFeatures'` (a typed-ast build failure)
- Fix: Replaces the pinned commit with the commit of a current black release, which installs on Python 3.15.
- Merged PR: [pypa/pipx#2181](https://github.com/pypa/pipx/pull/2181)
- Merged: 2026-10-28

## jqlang/jq

- Project: [jqlang/jq](https://github.com/jqlang/jq)
- Documented at: `README.md`, line 56 (lines 47 to 61), swept at `94b00aa`
- Command as written: `sudo make install`
- In a clean container: exit 127 from `jq --version` afterward, `jq: error while loading shared libraries: libjq.so.1`
- Fix: Adds `sudo ldconfig` after the install and `make clean` before the static build step.
- Merged PR: [jqlang/jq#3692](https://github.com/jqlang/jq/pull/3692)
- Merged: 2026-10-24

## wait4x/wait4x

- Project: [wait4x/wait4x](https://github.com/wait4x/wait4x)
- Documented at: `README.md`, lines 253 to 274, swept at `6e9c841`
- Command as written: `wait4x dns A example.com --expected-ip 93.184.216.34`
- In a clean container: exit 1, `Error: unknown flag: --expected-ip`
- Fix: Renames the `--expected-*` flags to the `--expect-*` flags the commands define and moves the examples to hosts that resolve today.
- Merged PR: [wait4x/wait4x#577](https://github.com/wait4x/wait4x/pull/577)
- Merged: 2026-10-23

## johnkerl/miller (prepipe examples)

- Project: [johnkerl/miller](https://github.com/johnkerl/miller)
- Documented at: `pkg/cli/option_parse.go`, lines 3522 and 3523 (the `mlr help flags` text), swept at `cb7f080`
- Command as written: `mlr --prepipe xz -cd`
- In a clean container: exit 1, `option "-d" not recognized` (the `zcat -cf` form exits 0 and prints the function list instead of the data)
- Fix: Quotes both prepipe examples, as the compressed-data docs already say to do, in the source and its generated copies.
- Merged PR: [johnkerl/miller#2275](https://github.com/johnkerl/miller/pull/2275)
- Merged: 2026-10-23

## johnkerl/miller (library setup)

- Project: [johnkerl/miller](https://github.com/johnkerl/miller)
- Documented at: `docs/src/miller-as-library.md.in`, line 16 (generated `docs/src/miller-as-library.md`, line 32), checked at `cb7f080`
- Command as written: `cd cd use-mlr`
- In a clean container: exit 1, `bash: cd: too many arguments`
- Fix: Removes the doubled `cd` in the setup steps.
- Merged PR: [johnkerl/miller#2276](https://github.com/johnkerl/miller/pull/2276)
- Merged: 2026-10-23

## ducaale/xh

- Project: [ducaale/xh](https://github.com/ducaale/xh)
- Documented at: `doc/md-template.md`, line 128 (generated `doc/xh.1.md`, line 372), swept at `319d39c`
- Command as written: `xh get Ihttpbin.org/json x-api-key:12345`
- In a clean container: exit 1, `dns error ... http://ihttpbin.org/json`
- Fix: Removes the stray `I` from the host in the template and the generated man page.
- Merged PR: [ducaale/xh#497](https://github.com/ducaale/xh/pull/497)
- Merged: 2026-10-23

## pyenv/pyenv

- Project: [pyenv/pyenv](https://github.com/pyenv/pyenv)
- Documented at: `README.md`, lines 187, 725 and 728, swept at `7d65a46`
- Command as written: `path/to/pyenv --install <shell executable name>`
- In a clean container: exit 1, ``pyenv: no such command `--install'``
- Fix: Changes the three lines to `pyenv init --install`, the form the neighboring lines already use.
- Merged PR: [pyenv/pyenv#3603](https://github.com/pyenv/pyenv/pull/3603)
- Merged: 2026-10-23

## ehmicky/nve

- Project: [ehmicky/nve](https://github.com/ehmicky/nve)
- Documented at: `README.md`, line 47, with the related compatibility note at lines 193 to 194, swept at `269ad0e`
- Command as written: `nve 12 npm test`
- In a clean container: exit 1, `Error: Cannot find module 'node:path'`
- Fix: Replaces the note that npm works with Node 6 and later with a link to npm's current Node requirements.
- Merged PR: [ehmicky/nve#82](https://github.com/ehmicky/nve/pull/82)
- Merged: 2026-10-21

## nosarthur/gita

- Project: [nosarthur/gita](https://github.com/nosarthur/gita)
- Documented at: `README.md`, line 258, swept at `062a5e1`
- Command as written: `gita context auto`
- In a clean container: exit 1, `FileNotFoundError: [Errno 2] No such file or directory: '/root/.config/gita/auto.context'`
- Fix: Makes gita create its config directory before it writes a context file, with a test.
- Merged PR: [nosarthur/gita#316](https://github.com/nosarthur/gita/pull/316)
- Merged: 2026-10-21

Last refreshed: 2026-11-01.

