package main

import (
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

// The divergence audit's verdicts cross from the in-process Auditor to the
// heartbeat here, once per plane — the same seam sweepstatus.go closes for the
// convergence sweep, and for the identical reason: a field the audit computes
// and the heartbeat evaluates reads as a clean zero at the surface if nobody
// copies it, and the surface is the only place anyone would notice.
//
// The auth plane's own auditor is always refused (auditEnrolment's first
// conjunct), so copyCapabilityAuditStatus never carries MaskedColumns — there
// is no field for it on health.CapabilityLensStatus, and there never will be
// one while the audit's plane boundary holds.

// copyCapabilityAuditStatus transfers one auth-plane lens's audit verdicts
// onto its heartbeat snapshot. A REFUSED lens carries a non-nil auditor too —
// its refusal is a published verdict, not an absence — so Enrolled/Refusal
// are copied unconditionally; every other field describes a pass an
// unenrolled auditor never runs.
func copyCapabilityAuditStatus(snap *health.CapabilityLensStatus, status pipeline.AuditStatus, interval time.Duration) {
	snap.AuditEnrolled = status.Enrolled
	snap.AuditRefusal = status.Refusal
	snap.Audited = status.Audited
	snap.DivergentRows = status.Divergent
	snap.DivergentTotal = status.DivergentTotal
	snap.AuditUnverified = status.Unverified
	snap.AuditLastUnverified = status.LastUnverified
	snap.AuditLastPassAt = status.LastPassAt
	snap.AuditCycleCompletedAt = status.CycleCompletedAt
	snap.AuditCycleAudited = status.CycleAudited
	snap.AuditCycleDivergentTotal = status.CycleDivergentTotal
	snap.AuditCycleUnverified = status.CycleUnverified
	snap.AuditCoverageBasis = status.CoverageBasis
	snap.AuditListingSize = status.ListingSize
	snap.AuditSuppression = status.Suppression
	snap.AuditSuppressionAt = status.SuppressionAt
	snap.AuditInterval = interval
}

// copyLensAuditStatus is the business-lens twin. Every field the cap path
// carries is carried here too, plus AuditMaskedColumns (secure-plain-lens-
// retraction-and-audit-design.md §4.1): a Secure Lens is a business-plane
// lens by construction (§2.5), so only this surface ever needs it.
func copyLensAuditStatus(snap *health.LensLivenessStatus, status pipeline.AuditStatus, interval time.Duration) {
	snap.AuditEnrolled = status.Enrolled
	snap.AuditRefusal = status.Refusal
	snap.AuditMaskedColumns = status.MaskedColumns
	snap.Audited = status.Audited
	snap.DivergentRows = status.Divergent
	snap.DivergentTotal = status.DivergentTotal
	snap.AuditUnverified = status.Unverified
	snap.AuditLastUnverified = status.LastUnverified
	snap.AuditLastPassAt = status.LastPassAt
	snap.AuditCycleCompletedAt = status.CycleCompletedAt
	snap.AuditCycleAudited = status.CycleAudited
	snap.AuditCycleDivergentTotal = status.CycleDivergentTotal
	snap.AuditCycleUnverified = status.CycleUnverified
	snap.AuditCoverageBasis = status.CoverageBasis
	snap.AuditListingSize = status.ListingSize
	snap.AuditSuppression = status.Suppression
	snap.AuditSuppressionAt = status.SuppressionAt
	snap.AuditInterval = interval
}
