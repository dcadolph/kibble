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

## Cloud metadata, and what is not blocked

A CI runner is usually a cloud instance, and cloud instances answer on a metadata
address that hands out credentials to anything that asks from inside. A documented line
kibble runs is untrusted input running inside that instance, so the well-known metadata
hostnames, `metadata.google.internal` and friends, are pointed at an address that answers
nothing.

That closes the named path and not the numbered one. `169.254.169.254` and the other
literal addresses stay reachable, because Docker offers no portable way to drop a route
to a single address, and both alternatives are worse: an internal network breaks every
install kibble exists to run, and host firewall rules are not kibble's to install.

So this is a speed bump, not a wall. If you run kibble against documentation you do not
control, on a runner whose instance role can reach anything you care about, scope that
role down. The container boundary is the thing protecting you, and it was never a
guarantee.
