package main

import (
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

// The plain arm's neighbour-anchor derivation crosses from the in-process
// Pipeline to the heartbeat here — the seam sweepstatus.go and auditstatus.go
// close for the sweep and the audit, and for the identical reason: a value the
// pipeline computes and the heartbeat publishes reads as a clean zero at the
// surface if nobody copies it, and the surface is the only place anyone would
// notice.
//
// There is no capability-plane twin. The derivation's own licence refuses the
// auth plane outright (plainDerivationLicence's first conjunct), so an
// auth-plane lens can never carry a tally, and health.CapabilityLensStatus has
// no field for one.

// copyLensDerivationStatus transfers one business lens's derivation posture and
// act-mode fall-back tally onto its heartbeat snapshot. An INELIGIBLE lens
// carries the zero status, and Eligible false is what stops everything in this
// group being published at all (health.addPlainDerivationMetrics) — so the
// copy is unconditional and the posture, not the caller, decides what an
// operator sees.
func copyLensDerivationStatus(snap *health.LensLivenessStatus, status pipeline.PlainDerivationStatus) {
	snap.DerivationEligible = status.Eligible
	snap.DerivationArmed = status.Armed
	snap.DerivationFellBack = status.FellBack
	snap.DerivationOverCapSize = status.OverCapSize
}
