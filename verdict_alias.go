package main

import "github.com/dcadolph/kibble/internal/verdict"

// The verdict vocabulary lives in its own package so the executor, the
// report, and the JSON cannot drift into disagreeing about what a word
// means. These aliases keep the names unqualified at the call sites, which
// read the same as they did when the taxonomy was declared here.
type (
	// Status is the outcome classification of a check.
	Status = verdict.Status
	// Bucket is the coarse grouping a status rolls up into.
	Bucket = verdict.Bucket
	// Reason is the machine-readable why behind a status that needs one.
	Reason = verdict.Reason
)

const (
	BucketBroken             = verdict.BucketBroken
	BucketDocDrift           = verdict.BucketDocDrift
	BucketInconclusive       = verdict.BucketInconclusive
	BucketNotAttempted       = verdict.BucketNotAttempted
	BucketUnverified         = verdict.BucketUnverified
	BucketWorks              = verdict.BucketWorks
	ReasonAlreadyProven      = verdict.ReasonAlreadyProven
	ReasonDependsOnSkipped   = verdict.ReasonDependsOnSkipped
	ReasonInteractive        = verdict.ReasonInteractive
	ReasonLongRunning        = verdict.ReasonLongRunning
	ReasonMissingDependency  = verdict.ReasonMissingDependency
	ReasonMissingFixture     = verdict.ReasonMissingFixture
	ReasonNeedsCredentials   = verdict.ReasonNeedsCredentials
	ReasonNoDataExpected     = verdict.ReasonNoDataExpected
	ReasonNoOutputExit1      = verdict.ReasonNoOutputExit1
	ReasonNotExecuted        = verdict.ReasonNotExecuted
	ReasonOtherPlatform      = verdict.ReasonOtherPlatform
	ReasonPlaceholder        = verdict.ReasonPlaceholder
	ReasonUnparseable        = verdict.ReasonUnparseable
	ReasonUnreachable        = verdict.ReasonUnreachable
	ReasonUnrecognizedTarget = verdict.ReasonUnrecognizedTarget
	ReasonUnsupportedMethod  = verdict.ReasonUnsupportedMethod
	StatusBlocked            = verdict.StatusBlocked
	StatusBuilt              = verdict.StatusBuilt
	StatusCrossArch          = verdict.StatusCrossArch
	StatusDrift              = verdict.StatusDrift
	StatusError              = verdict.StatusError
	StatusExists             = verdict.StatusExists
	StatusFail               = verdict.StatusFail
	StatusGap                = verdict.StatusGap
	StatusRan                = verdict.StatusRan
	StatusSkipped            = verdict.StatusSkipped
	StatusTimeout            = verdict.StatusTimeout
	StatusVerified           = verdict.StatusVerified
)
