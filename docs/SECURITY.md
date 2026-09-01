# Security model

kibble executes commands it read out of a repository's documentation. That is the
product, and it means **a README is untrusted input**: anyone who can change the docs can
change what runs. Treat a kibble run the way you treat a build script.

Every command runs inside a fresh container that is removed afterward, with bounded
memory and process counts, `no-new-privileges`, and every Linux capability dropped except
the handful `apt` needs to install the packages the docs depend on. Nothing from the host
is mounted in; the repository is streamed in as a tar of its working tree.

The boundary that stays open is the network, because verifying an install *is* fetching
it: `go install`, `cargo install`, and `npm install -g` are network operations. A
malicious documented command can therefore reach out from inside the container. The
container is disposable and unprivileged, but Docker isolation is a wall, not a
guarantee. So:

- Running kibble on your own repository in CI is the designed case: anyone who can edit
  your README can already edit your workflows.
- Running kibble against a repository you do not trust is running that repository's
  chosen commands on your Docker daemon. Do it the way you would run their Makefile:
  in a sandbox you are prepared to lose.
- Default kibble never contacts a model provider. Only `-suggest` does, it sends only
  the documented lines the engine could not settle, and the Ollama route keeps even
  that on your own machine.

In CI, the GitHub Action downloads the released binary for the version you pinned and
verifies its checksum against the release's `checksums.txt` before running it, so a CI
log can say which kibble produced its verdict.
