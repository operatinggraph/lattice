package pipeline

import (
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// The retraction transports a plain lens's neighbour drop-out can travel on
// (secure-plain-lens-retraction-and-audit-design.md §4.4). A plain lens retracts
// a row on its ANCHOR's own event through the presence check; a row dropped by a
// NEIGHBOUR event — a neighbour vertex tombstoned, a link two hops out removed,
// a neighbour aspect flipping a WHERE — is named by no anchor event, and reaches
// the target only through one of these.
const (
	// RetractionTransportNone is the absence of one. On the business plane it is
	// what activation refuses; on the auth plane it is named debt, since the
	// derivation licence excludes that plane outright.
	RetractionTransportNone = ""

	// RetractionTransportDerivation is the licensed neighbour-anchor derivation
	// (T1): the neighbour event narrows to the anchors the scan-root walk
	// derives, and each is re-evaluated, so an anchor that no longer produces a
	// row receives a Delete.
	RetractionTransportDerivation = "derivation"

	// RetractionTransportDerivationAuditDisarmed is T1's shape on a deployment
	// that threw the audit's kill switch (SetAuditEnabled(false)). The lens's
	// shape supports the transport and nothing is carrying it: the licence's
	// enrolled-auditor conjunct can never hold corpus-wide while the switch is
	// down, so this reads as "declared, voided by the deployment" rather than as
	// a working transport.
	RetractionTransportDerivationAuditDisarmed = "derivation (audit disarmed)"

	// RetractionTransportDiffRetraction is the whole-target key diff (T2) on a
	// target this lens owns alone.
	RetractionTransportDiffRetraction = "diffRetraction"

	// RetractionTransportDiffRetractionPrefix is the same diff scoped to the
	// lens's own key prefix in a target it shares (T2-prefix).
	RetractionTransportDiffRetractionPrefix = "diffRetraction-prefix"

	// RetractionTransportDiffRetractionPartition is the diff scoped to the
	// ANCHORS an evaluation covered (T2-partition), on a lens whose rows
	// partition by their anchor. The neighbour event narrows to the anchors the
	// scan-root walk derives, each is re-evaluated and diffs its own partition,
	// and every other anchor's rows are never listed. It is the transport a
	// lens carries INSTEAD of the whole diff, not in addition to it — and only
	// when activation armed it (SetPartitionRetraction).
	RetractionTransportDiffRetractionPartition = "diffRetraction-partition"
)

// PlainRetractionVerdict is one plain lens's neighbour-retraction posture:
// whether its rows can be dropped by a neighbour at all, and what carries the
// retraction when they can.
type PlainRetractionVerdict struct {
	// Classified is false when this pipeline is not the shape the verdict
	// speaks about — an actor-aware or personal evaluation, whose retraction is
	// the envelope's and the sweep's, or a rule that is not a full-engine one,
	// which expresses matching differently and has no pattern graph to walk.
	// Every other field is then zero and says nothing.
	Classified bool

	// DependsOnNeighbour and Reasons are the compiled rule's own answer
	// (full.CompiledRule.ExistenceDependsOnNeighbour), and Exhaustive is
	// whether that answer could be derived at all. A non-exhaustive answer is a
	// refusal at the gate, never a pass.
	DependsOnNeighbour bool
	Reasons            []string
	Exhaustive         bool

	// Transport is one of the constants above, RetractionTransportNone when the
	// lens carries none. It is answered whether or not the lens depends on a
	// neighbour: a lens that does not need the transport can still have one, and
	// publishing the transport only for the lenses that need it is the
	// publication rule (cmd/refractor's copyLensRetractionTransport), not this
	// derivation's.
	Transport string

	// Refusal names why there is no transport, "" when there is one. It is what
	// an activation refusal and an operator both read: "no transport" with no
	// cause is indistinguishable from a gate nobody can debug.
	Refusal string
}

// PlainRetractionTransport derives this lens's neighbour-retraction posture
// from the compiled rule, the declaration, the target adapter and the plane.
//
// authPlane is passed in rather than read off p.authPlane for the reason
// InstallAudit takes it the same way: projection.IsAuthPlane is the one
// canonical derivation, and the activation gate runs BEFORE installLensPlane
// records it on the pipeline — a conjunct that depends on whether an earlier
// stage happened to run reads as satisfied for a lens it must refuse.
//
// It is re-derived on demand rather than cached, so a MATCH hot-reload that
// swaps the compiled rule (and with it the scan-root graph, the closure verdict
// and the neighbour dependency) cannot leave a stale posture published: the
// rule snapshot it reads is the copy-on-write one every other live predicate
// reads.
//
// T2 is claimed from p.diffRetraction alone, and that is exact rather than
// optimistic: activation admits DiffRetraction only after the sharing check
// (cmd/refractor) has established that the diff enumerates this lens's rows and
// no sibling's — an unshared target, a GrantSource-scoped grant table, or a
// derivable prefix threaded through SetDiffRetractionPrefix. A lens that fails
// that check never activates, so a running DiffRetraction lens is one whose
// diff is sound.
func (p *Pipeline) PlainRetractionTransport(authPlane bool) PlainRetractionVerdict {
	rs := p.ruleState()
	if p.actorEnumerator != nil || p.envelopeFn != nil || p.multiEnvelopeFn != nil {
		return PlainRetractionVerdict{}
	}
	fullCR, isFull := rs.cr.(*full.CompiledRule)
	if !isFull || fullCR == nil {
		return PlainRetractionVerdict{}
	}

	v := PlainRetractionVerdict{Classified: true}
	v.DependsOnNeighbour, v.Reasons, v.Exhaustive = fullCR.ExistenceDependsOnNeighbour()

	switch {
	case p.diffRetraction:
		// The partition arm is read FIRST because it is the transport that
		// actually runs on an armed lens: its seeded and licensed-neighbour
		// events diff their own anchors' partitions, and the whole listing
		// (scoped or not) is reached only by an unlicensed neighbour event. A
		// verdict naming the whole diff would describe the fallback rather than
		// the mechanism, and the census that pins it would then pin the wrong
		// posture for the five lenses this design moves.
		switch {
		case p.partitionArmed(rs):
			v.Transport = RetractionTransportDiffRetractionPartition
		case p.diffRetractionPrefix != "":
			v.Transport = RetractionTransportDiffRetractionPrefix
		default:
			v.Transport = RetractionTransportDiffRetraction
		}
	case authPlane:
		// The derivation licence refuses this plane outright — an auth-plane row
		// is an authorization surface, and narrowing one behind a
		// detection-only mechanism is what that refusal declines. So T1 is not
		// available here whatever the shape says, and the reason names the
		// plane rather than whichever static conjunct would have been asked
		// next.
		//
		// The one caller that reaches this arm is the corpus census, which asks
		// the verdict of every plain lens the corpus ships so the auth plane's
		// members are named debt rather than an absence. The heartbeat never
		// does: its provider loop filters !entry.authPlane before it reads a
		// transport at all, and the activation gate is scoped to the business
		// plane for the same reason. So this arm exists to give the census an
		// answer with a reason attached, not to describe a lens anything
		// publishes.
		v.Refusal = "it projects onto the auth plane, whose derivation licence is refused, so its only transport is a target diff it owns"
	default:
		eligible, refusal := p.plainDerivationStaticallyEligible(rs)
		switch {
		case !eligible:
			v.Refusal = refusal
		case !fullCR.ProjectsOneRowPerAnchor():
			// The narrowing licence and the RETRACTION are two different
			// questions, and this arm is the second one. The licence admits any
			// rule whose rows partition by anchor, because that is what makes a
			// per-anchor evaluation exact. But T1 delivers a retraction through
			// evaluateForEntryRaw's read-free presence check, which needs the
			// row's KEY derivable from the anchor alone — so on a lens that
			// partitions WITHOUT closing, the derived path re-evaluates every
			// affected anchor and emits no Delete for the row a neighbour just
			// dropped.
			//
			// A partition-only lens's retraction is the partition-scoped diff,
			// and that is reached through the p.diffRetraction case above.
			// Reaching HERE means the lens declares none, so it narrows
			// correctly and retracts nothing, and reporting T1 for it would name
			// a transport that cannot carry a row off the target.
			v.Refusal = "its rows partition by anchor but its key is not derivable from the anchor alone, so the derived " +
				"path's read-free presence check can never emit the Delete a dropped row needs — declare target-diff " +
				"retraction on a target it owns, which scopes itself to the same partitions"
		case !auditArmed:
			// The kill switch voids every T1 transport corpus-wide: the
			// licence's first conjunct is an ENROLLED auditor, and
			// auditEnrolment refuses every lens while the switch is down. The
			// shape is still there, which is why this is a transport value and
			// a warning rather than a bare refusal.
			v.Transport = RetractionTransportDerivationAuditDisarmed
		default:
			v.Transport = RetractionTransportDerivation
		}
	}
	return v
}
