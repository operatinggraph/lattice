package main

import (
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

// The convergence sweep's verdicts cross from the in-process Sweeper to the
// heartbeat here, once per plane. The two planes carry deliberately separate
// status structs, so the copy cannot be one function — but it is two NAMED ones
// rather than two anonymous blocks inside the provider closures, because this is
// a seam that is green at each layer and broken across it: a field the sweep
// computes and the heartbeat evaluates reads as a clean zero at the surface if
// nobody copies it, and the surface is the only place anyone would notice.
//
// The counters are read from the sweeper rather than from the persisted health
// entry: the streaks are what escalate the issues, and they are per-run state
// the entry deliberately does not carry — a restart opens a fresh escalation
// window and re-discovers a live verdict on its first pass.

// copyCapabilitySweepStatus transfers one auth-plane lens's sweep verdicts onto
// its heartbeat snapshot.
func copyCapabilitySweepStatus(snap *health.CapabilityLensStatus, status pipeline.SweepStatus, interval time.Duration) {
	snap.SweepReconciled = status.Reconciled
	snap.SweepDivergentStreak = status.DivergentStreak
	snap.SweepFailingActors = status.FailingActors
	snap.SweepFailedStreak = status.FailedStreak
	snap.SweepLastFailure = status.LastFailure
	snap.SweepUnverified = status.Unverified
	snap.SweepUnverifiedStreak = status.UnverifiedStreak
	snap.SweepLastUnverified = status.LastUnverified
	snap.SweepBlocked = status.Blocked
	snap.SweepBlockedStreak = status.BlockedStreak
	snap.SweepLastBlocked = status.LastBlocked
	// The blocked total split by class. Without it the heartbeat's severity rule
	// sees four zeros and reads every blocked row as the unclassifiable one, so
	// the whole classification is inert at the only surface that publishes it.
	snap.SweepBlockedRetraction = status.BlockedByClass[pipeline.BlockedRetraction]
	snap.SweepBlockedContent = status.BlockedByClass[pipeline.BlockedContent]
	snap.SweepBlockedUnknown = status.BlockedByClass[pipeline.BlockedUnknown]
	snap.SweepBlockedProvenance = status.BlockedByClass[pipeline.BlockedProvenance]
	snap.SweepWorstBlocked = status.WorstBlockedClass
	snap.SweepLastPassAt = status.LastPassAt
	snap.SweepSuppression = status.Suppression
	snap.SweepSuppressionAt = status.SuppressionAt
	snap.SweepInterval = interval
}

// copyLensSweepStatus is the business-lens twin. Every field the cap path
// carries is carried here too: the two surfaces publish the same sweep, and a
// split that exists on one of them only is a lens whose class census silently
// reads as zero.
func copyLensSweepStatus(snap *health.LensLivenessStatus, status pipeline.SweepStatus, interval time.Duration) {
	snap.SweepReconciled = status.Reconciled
	snap.SweepDivergentStreak = status.DivergentStreak
	snap.SweepFailingActors = status.FailingActors
	snap.SweepFailedStreak = status.FailedStreak
	snap.SweepLastFailure = status.LastFailure
	snap.SweepUnverified = status.Unverified
	snap.SweepUnverifiedStreak = status.UnverifiedStreak
	snap.SweepLastUnverified = status.LastUnverified
	snap.SweepBlocked = status.Blocked
	snap.SweepBlockedStreak = status.BlockedStreak
	snap.SweepLastBlocked = status.LastBlocked
	snap.SweepBlockedRetraction = status.BlockedByClass[pipeline.BlockedRetraction]
	snap.SweepBlockedContent = status.BlockedByClass[pipeline.BlockedContent]
	snap.SweepBlockedUnknown = status.BlockedByClass[pipeline.BlockedUnknown]
	snap.SweepBlockedProvenance = status.BlockedByClass[pipeline.BlockedProvenance]
	snap.SweepWorstBlocked = status.WorstBlockedClass
	snap.SweepLastPassAt = status.LastPassAt
	snap.SweepSuppression = status.Suppression
	snap.SweepSuppressionAt = status.SuppressionAt
	snap.SweepInterval = interval
}
