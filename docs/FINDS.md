# Fixes found by kibble

This page lists merged fixes for documented commands that failed when kibble ran them, in repositories kibble had never run on before. Each failure was replayed in a clean container, and each fix was filed by kibble's author after reading the project's contributing rules. Most fixes change the documentation. Two change the code so the documented command works. Open reports join the list when they merge.

Entries are ordered by merge date, newest first.

## dundee/gdu

- Project: [dundee/gdu](https://github.com/dundee/gdu)
- Documented at: [`README.md`, line 382](https://github.com/dundee/gdu/blob/14907d977e7682b82d1e2f5811d5ded1f01cbc91/README.md#L382) and [`CONTRIBUTING.md`, line 16](https://github.com/dundee/gdu/blob/14907d977e7682b82d1e2f5811d5ded1f01cbc91/CONTRIBUTING.md#L16), tested at `14907d9`
- Command as written: `make install-dev-dependencies`
- In a clean container: exit 2 with `GOBIN` exported, `cannot install, GOBIN must be an absolute path`, and exit 2 without it, a gotraceui build error on Go 1.24 and later
- Fix: Renames the Makefile's go command variable from `GOBIN` to `GO` and drops the gotraceui install, which no `go install` form can build today.
- Merged PR: [dundee/gdu#704](https://github.com/dundee/gdu/pull/704)
- Merged: 2026-11-03

## mufeedvh/code2prompt

- Project: [mufeedvh/code2prompt](https://github.com/mufeedvh/code2prompt)
- Documented at: [`website/src/content/docs/docs/tutorials/learn_filters.mdx`, line 188](https://github.com/mufeedvh/code2prompt/blob/66585136062c04275ddd5a01c5518a4d78c4aa76/website/src/content/docs/docs/tutorials/learn_filters.mdx#L188), tested at `6658513`
- Command as written: `code2prompt test_dir --include="*.py" --exclude="**/uppercase/*" --include-priority=true`
- In a clean container: exit 2, the removed `--include-priority` flag is rejected as an unexpected argument
- Fix: Drops the removed flag from the filter tutorial in all seven languages. Without it the command already includes the three files the tutorial describes.
- Merged PR: [mufeedvh/code2prompt#346](https://github.com/mufeedvh/code2prompt/pull/346)
- Merged: 2026-11-03

## goreleaser/goreleaser

- Project: [goreleaser/goreleaser](https://github.com/goreleaser/goreleaser)
- Documented at: [`www/content/getting-started/quick-start.md`, line 138](https://github.com/goreleaser/goreleaser/blob/a9fb415389a2e55906ae457d9f76bb77b3cb6056/www/content/getting-started/quick-start.md#L138) ([lines 135 to 196](https://github.com/goreleaser/goreleaser/blob/a9fb415389a2e55906ae457d9f76bb77b3cb6056/www/content/getting-started/quick-start.md#L135-L196)), tested at `a9fb415`
- Command as written: `goreleaser build --single-target --clean`
- In a clean container: exit 1, `git doesn't contain any tags - either add a tag or use --snapshot`
- Fix: Adds `--snapshot` to the quick start's single-target build lines, which run before the guide creates a tag.
- Merged PR: [goreleaser/goreleaser#7232](https://github.com/goreleaser/goreleaser/pull/7232)
- Merged: 2026-11-01

## tach-org/tach

- Project: [tach-org/tach](https://github.com/tach-org/tach)
- Documented at: [`docs/getting-started/getting-started.md`, line 76](https://github.com/tach-org/tach/blob/bc92d71520a1acdb9aacd1f2362ca49b58259dda/docs/getting-started/getting-started.md#L76), swept at `bc92d71`
- Command as written: `tach install --pre-commit`
- In a clean container: exit 2, `tach install: error: the following arguments are required: target`
- Fix: Changes the command to `tach install pre-commit`, matching the commands reference.
- Merged PR: [tach-org/tach#961](https://github.com/tach-org/tach/pull/961)
- Merged: 2026-10-29

## pypa/pipx

- Project: [pypa/pipx](https://github.com/pypa/pipx)
- Documented at: [`docs/how-to/install-pipx.rst`, line 198](https://github.com/pypa/pipx/blob/9a49f1957fad930860189421f2cd29d5a5604fd4/docs/how-to/install-pipx.rst#L198), swept at `9a49f19`
- Command as written: `pipx install git+https://github.com/psf/black.git@ce14fa8b497bae2b50ec48b3bd7022573a59cdb1`
- In a clean container: exit 1 on Python 3.15.0, `error: unknown type name 'PyFutureFeatures'` (a typed-ast build failure)
- Fix: Replaces the pinned commit with the commit of a current black release, which installs on Python 3.15.
- Merged PR: [pypa/pipx#2181](https://github.com/pypa/pipx/pull/2181)
- Merged: 2026-10-28

## jqlang/jq

- Project: [jqlang/jq](https://github.com/jqlang/jq)
- Documented at: [`README.md`, line 56](https://github.com/jqlang/jq/blob/94b00aa807a51a8f97bc9345c03e32982e344678/README.md#L56) ([lines 47 to 61](https://github.com/jqlang/jq/blob/94b00aa807a51a8f97bc9345c03e32982e344678/README.md#L47-L61)), swept at `94b00aa`
- Command as written: `sudo make install`
- In a clean container: exit 127 from `jq --version` afterward, `jq: error while loading shared libraries: libjq.so.1`
- Fix: Adds `sudo ldconfig` after the install and `make clean` before the static build step.
- Merged PR: [jqlang/jq#3692](https://github.com/jqlang/jq/pull/3692)
- Merged: 2026-10-24

## wait4x/wait4x

- Project: [wait4x/wait4x](https://github.com/wait4x/wait4x)
- Documented at: [`README.md`, lines 253 to 274](https://github.com/wait4x/wait4x/blob/6e9c8413fd45da50b4ec65e97effb7e27550708a/README.md#L253-L274), swept at `6e9c841`
- Command as written: `wait4x dns A example.com --expected-ip 93.184.216.34`
- In a clean container: exit 1, `Error: unknown flag: --expected-ip`
- Fix: Renames the `--expected-*` flags to the `--expect-*` flags the commands define and moves the examples to hosts that resolve today.
- Merged PR: [wait4x/wait4x#577](https://github.com/wait4x/wait4x/pull/577)
- Merged: 2026-10-23

## johnkerl/miller (prepipe examples)

- Project: [johnkerl/miller](https://github.com/johnkerl/miller)
- Documented at: [`pkg/cli/option_parse.go`, lines 3522 and 3523](https://github.com/johnkerl/miller/blob/cb7f080a734c72a80b86244df65bdc5375a57bae/pkg/cli/option_parse.go#L3522-L3523) (the `mlr help flags` text), swept at `cb7f080`
- Command as written: `mlr --prepipe xz -cd`
- In a clean container: exit 1, `option "-d" not recognized` (the `zcat -cf` form exits 0 and prints the function list instead of the data)
- Fix: Quotes both prepipe examples, as the compressed-data docs already say to do, in the source and its generated copies.
- Merged PR: [johnkerl/miller#2275](https://github.com/johnkerl/miller/pull/2275)
- Merged: 2026-10-23

## johnkerl/miller (library setup)

- Project: [johnkerl/miller](https://github.com/johnkerl/miller)
- Documented at: [`docs/src/miller-as-library.md.in`, line 16](https://github.com/johnkerl/miller/blob/cb7f080a734c72a80b86244df65bdc5375a57bae/docs/src/miller-as-library.md.in#L16) (generated [`docs/src/miller-as-library.md`, line 32](https://github.com/johnkerl/miller/blob/cb7f080a734c72a80b86244df65bdc5375a57bae/docs/src/miller-as-library.md#L32)), checked at `cb7f080`
- Command as written: `cd cd use-mlr`
- In a clean container: exit 1, `bash: cd: too many arguments`
- Fix: Removes the doubled `cd` in the setup steps.
- Merged PR: [johnkerl/miller#2276](https://github.com/johnkerl/miller/pull/2276)
- Merged: 2026-10-23

## ducaale/xh

- Project: [ducaale/xh](https://github.com/ducaale/xh)
- Documented at: [`doc/md-template.md`, line 128](https://github.com/ducaale/xh/blob/319d39c2561d2a8697acb3eb9800abcc33bbad98/doc/md-template.md#L128) (generated [`doc/xh.1.md`, line 372](https://github.com/ducaale/xh/blob/319d39c2561d2a8697acb3eb9800abcc33bbad98/doc/xh.1.md#L372)), swept at `319d39c`
- Command as written: `xh get Ihttpbin.org/json x-api-key:12345`
- In a clean container: exit 1, `dns error ... http://ihttpbin.org/json`
- Fix: Removes the stray `I` from the host in the template and the generated man page.
- Merged PR: [ducaale/xh#497](https://github.com/ducaale/xh/pull/497)
- Merged: 2026-10-23

## pyenv/pyenv

- Project: [pyenv/pyenv](https://github.com/pyenv/pyenv)
- Documented at: `README.md`, lines [187](https://github.com/pyenv/pyenv/blob/7d65a4683c91b29f7501da4c8737d4e786f8cad2/README.md#L187), [725](https://github.com/pyenv/pyenv/blob/7d65a4683c91b29f7501da4c8737d4e786f8cad2/README.md#L725) and [728](https://github.com/pyenv/pyenv/blob/7d65a4683c91b29f7501da4c8737d4e786f8cad2/README.md#L728), swept at `7d65a46`
- Command as written: `path/to/pyenv --install <shell executable name>`
- In a clean container: exit 1, ``pyenv: no such command `--install'``
- Fix: Changes the three lines to `pyenv init --install`, the form the neighboring lines already use.
- Merged PR: [pyenv/pyenv#3603](https://github.com/pyenv/pyenv/pull/3603)
- Merged: 2026-10-23

## ehmicky/nve

- Project: [ehmicky/nve](https://github.com/ehmicky/nve)
- Documented at: [`README.md`, line 47](https://github.com/ehmicky/nve/blob/269ad0e6665f81c9ed2f7d2edae5973b3ebe904b/README.md#L47), with the related compatibility note at [lines 193 to 194](https://github.com/ehmicky/nve/blob/269ad0e6665f81c9ed2f7d2edae5973b3ebe904b/README.md#L193-L194), swept at `269ad0e`
- Command as written: `nve 12 npm test`
- In a clean container: exit 1, `Error: Cannot find module 'node:path'`
- Fix: Replaces the note that npm works with Node 6 and later with a link to npm's current Node requirements.
- Merged PR: [ehmicky/nve#82](https://github.com/ehmicky/nve/pull/82)
- Merged: 2026-10-21

## nosarthur/gita

- Project: [nosarthur/gita](https://github.com/nosarthur/gita)
- Documented at: [`README.md`, line 258](https://github.com/nosarthur/gita/blob/062a5e1ed56cad9b5f81c63791cd4ab130f175d9/README.md#L258), swept at `062a5e1`
- Command as written: `gita context auto`
- In a clean container: exit 1, `FileNotFoundError: [Errno 2] No such file or directory: '/root/.config/gita/auto.context'`
- Fix: Makes gita create its config directory before it writes a context file, with a test.
- Merged PR: [nosarthur/gita#316](https://github.com/nosarthur/gita/pull/316)
- Merged: 2026-10-21

Last refreshed: 2026-11-01.

