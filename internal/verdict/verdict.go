// Package verdict holds kibble's outcome taxonomy: the statuses a check can
// reach, the coarse buckets they roll up into, and the machine-readable
// reasons behind the ones that need explaining. It is the one place the
// vocabulary is defined, so the executor, the report, the JSON, and strict
// mode cannot drift into disagreeing about what a word means.
package verdict

// Status is the outcome classification of an install attempt.
type Status string

const (
	// StatusVerified means the tool built or installed and the binary
	// responded to a smoke test. It is the only status that claims the
	// documented step actually works.
	StatusVerified Status = "VERIFIED"
	// StatusBuilt means it built or installed but the smoke test exited
	// non-zero, so the artifact exists without proof it runs.
	StatusBuilt Status = "BUILT"
	// StatusRan means the recipe completed with a zero exit but produced no
	// binary to smoke-test, so nothing was verified. It covers both a recipe
	// that should have produced a tool and did not, and one that legitimately
	// installs no binary, since kibble cannot tell the two apart.
	StatusRan Status = "RAN"
	// StatusExists means a documented package or formula was confirmed present
	// in its index but never installed, so its existence is known and its
	// installation is not.
	StatusExists Status = "EXISTS"
	// StatusCrossArch means the tool installed but the binary targets another
	// architecture, so it could not be smoke-tested on this host.
	StatusCrossArch Status = "CROSS-ARCH"
	// StatusTimeout means the build exceeded the timeout, so the result is unknown.
	StatusTimeout Status = "TIMEOUT"
	// StatusFail means the documented install failed to build or named
	// something that does not exist.
	StatusFail Status = "FAIL"
	// StatusSkipped means kibble intentionally did not run this step. The
	// Reason field carries why in a machine-readable form. A skip is a
	// decision made before execution, on evidence kibble holds: a placeholder
	// token, another platform, a file no documented step creates.
	StatusSkipped Status = "SKIP"
	// StatusBlocked means kibble ran the step and could not establish whether
	// the documented behavior works. It is the honest answer whenever the only
	// evidence is that the output resembled a condition kibble excuses: a
	// status code that reads as missing credentials, a refusal that reads as a
	// missing service, a failure that names a command which did not run. Such
	// output is consistent with a working document and with a broken one, and
	// a skip would claim the first. Blocked claims neither.
	StatusBlocked Status = "BLOCKED"
	// StatusGap means the documentation is incomplete: a documented line
	// names a file, directory, or variable that no documented step creates,
	// so a reader following the document cannot run it. Unlike a skip, the
	// gap is the document's, not the container's.
	StatusGap Status = "GAP"
	// StatusDrift means the docs cite a flag or subcommand the binary lacks.
	StatusDrift Status = "DRIFT"
	// StatusError means kibble itself could not run the step, so the result
	// says nothing about the document.
	StatusError Status = "ERROR"
)

// Bucket is a coarse grouping of statuses, so a consumer can condense the
// fine-grained verdicts into a handful of categories without hard-coding the
// full status set.
type Bucket string

const (
	// BucketWorks holds the statuses that prove the documented step works.
	BucketWorks Bucket = "works"
	// BucketUnverified holds the statuses where a step ran or a package
	// exists but nothing confirmed it works.
	BucketUnverified Bucket = "unverified"
	// BucketBroken holds the statuses where a documented step failed.
	BucketBroken Bucket = "broken"
	// BucketDocDrift holds the statuses where the documentation itself is
	// wrong even if commands ran.
	BucketDocDrift Bucket = "doc-drift"
	// BucketNotAttempted holds steps kibble chose not to run.
	BucketNotAttempted Bucket = "not-attempted"
	// BucketInconclusive holds steps whose outcome kibble could not determine.
	BucketInconclusive Bucket = "inconclusive"
)

// Bucket maps a status to its coarse category. It is the one place the
// rollup is defined, so the report, the JSON, and strict mode all agree.
func (s Status) Bucket() Bucket {
	switch s {
	case StatusVerified:
		return BucketWorks
	case StatusBuilt, StatusRan, StatusExists, StatusCrossArch:
		return BucketUnverified
	case StatusFail:
		return BucketBroken
	case StatusGap, StatusDrift:
		return BucketDocDrift
	case StatusSkipped:
		return BucketNotAttempted
	case StatusBlocked:
		return BucketInconclusive
	default:
		return BucketInconclusive
	}
}

// FailsUnderStrict reports whether strict mode treats this status as a
// failure. Strict promotes everything that is not a clean pass and not a
// deliberate skip: an unverified step, a drifted doc, or an inconclusive run
// all become failures when the caller demands proof.
func (s Status) FailsUnderStrict() bool {
	switch s.Bucket() {
	case BucketWorks, BucketNotAttempted:
		return false
	default:
		return true
	}
}

// Reason is a machine-readable code for why a step landed on its status,
// chiefly why a step was skipped, so a consumer can filter and audit the
// reasons rather than parsing the human-facing detail string.
type Reason string

const (
	// ReasonInteractive marks a command that waits for input.
	ReasonInteractive Reason = "interactive"
	// ReasonLongRunning marks a watcher, server, or daemon that does not return.
	ReasonLongRunning Reason = "long-running"
	// ReasonPlaceholder marks a command holding a template token to fill in.
	ReasonPlaceholder Reason = "placeholder"
	// ReasonMissingFixture marks a command needing a file the docs never create.
	ReasonMissingFixture Reason = "missing-fixture"
	// ReasonOtherPlatform marks a command scoped to another operating system.
	ReasonOtherPlatform Reason = "other-platform"
	// ReasonNeedsCredentials marks a command that needs authentication.
	ReasonNeedsCredentials Reason = "needs-credentials"
	// ReasonNoDataExpected marks a command whose empty result is normal.
	ReasonNoDataExpected Reason = "no-data-expected"
	// ReasonMissingDependency marks a command needing a tool the docs assume.
	ReasonMissingDependency Reason = "missing-dependency"
	// ReasonDependsOnSkipped marks a command that follows a skipped one.
	ReasonDependsOnSkipped Reason = "depends-on-skipped"
	// ReasonAlreadyProven marks a command the install smoke test already covered.
	ReasonAlreadyProven Reason = "already-proven"
	// ReasonNoOutputExit1 marks a quiet exit-1, as a search does on no match.
	ReasonNoOutputExit1 Reason = "no-output-exit1"
	// ReasonUnrecognizedTarget marks an install target kibble cannot parse.
	ReasonUnrecognizedTarget Reason = "unrecognized-target"
	// ReasonUnreachable marks a lookup kibble could not complete over the network.
	ReasonUnreachable Reason = "unreachable"
	// ReasonNotExecuted marks a step kibble has not run in this kind of pass.
	ReasonNotExecuted Reason = "not-executed"
	// ReasonUnparseable marks a documented line no shell parser accepts, so
	// kibble declines to guess at what it would have run.
	ReasonUnparseable Reason = "unparseable"
	// ReasonUnsupportedMethod marks a documented install method kibble sees but
	// does not run, such as a piped shell installer or a system package.
	ReasonUnsupportedMethod Reason = "unsupported-method"
)
