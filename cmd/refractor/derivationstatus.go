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

// copyLensRetractionTransport transfers the lens's neighbour-retraction posture
// onto its heartbeat snapshot, and NOTHING for a lens whose rows no neighbour
// can drop.
//
// Absence is a reading, not an omission: a lens that cannot be orphaned by a
// neighbour is owed no transport, so publishing one for it — or publishing
// "none" — would put a field on the wire that reads as a verdict about a risk
// the lens does not carry.
//
// The two values that are NOT a transport are published because they are the
// two shapes activation refuses, and a lens carrying either is one that reached
// the registry anyway:
//
//   - "none": its rows depend on a neighbour and nothing retracts a neighbour
//     drop-out.
//   - "unclassified": whether its rows depend on a neighbour could not be
//     derived from its query shape. Read as "they do not" the lens activates
//     with no obligation anyone can see, which is why every gate refuses it —
//     and why the wire says so rather than falling silent, which is the reading
//     "not owed" already has.
//
// Neither should ever be published: activation refuses both shapes, and so does
// every hot-reload path that could install one. The heartbeat's own alert for
// them is the backstop for a lens that got past all of that, whose only other
// evidence would be a read model quietly keeping rows nothing will name.
//
// Business plane only. The caller's loop filters to !entry.authPlane, and that
// is the field's whole scope: an auth-plane lens publishes CapabilityLensStatus,
// which has no member for this and whose per-row verdicts belong to the
// convergence sweep and the Capability* codes. The auth-plane members carrying
// no transport are named debt in the corpus census, not an alert here.
func copyLensRetractionTransport(snap *health.LensLivenessStatus, v pipeline.PlainRetractionVerdict) {
	switch {
	case !v.Classified:
		// Not the shape the verdict speaks about — an actor-aware or personal
		// evaluation, whose retraction is the envelope's and the sweep's.
	case !v.Exhaustive:
		snap.RetractionTransport = health.RetractionTransportUnclassified
	case !v.DependsOnNeighbour:
	case v.Transport == pipeline.RetractionTransportNone:
		snap.RetractionTransport = health.RetractionTransportNone
	default:
		snap.RetractionTransport = v.Transport
	}
}
