package leasesigning

import "github.com/asolgan/lattice/internal/pkgmgr"

// RenewalLenses returns the package's two renewal convergence lenses (design
// loftspace-lease-renewal-goal-authored-target-design.md §4.2/§4.3):
//
//   - leaseExpiry (Target A, frozen table): anchored on the LEASEAPP, opens the
//     renewal cycle once a signed+approved application's tenancy nears its
//     renewalOpensAt horizon.
//   - renewalComplete (Target B, mode: planned): anchored on the RENEWAL
//     vertex, walks back to its leaseapp for tenant/landlord/guarantor/bgcheck
//     facts, and is the first goal-authored gap (§10.8 Planner extension).
//
// Both share the SAME shape of walk (renewal→leaseapp→{applicant, unit}) that
// leaseApplicationCompleteSpec already established; comments here focus on
// what is NEW rather than re-explaining the shared idioms (readinessOptionalMatch
// etc. are a different target's concern and are not reused here — the renewal
// targets need only the bgcheck freshness fragment, inlined per-lens below).
func RenewalLenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  "leaseExpiry",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           leaseExpirySpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "leaseapp",
				OutputKeyPattern: "leaseExpiry.{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_renewalCycle", "entityKey", "freshUntil"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
		{
			CanonicalName:  "renewalComplete",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           renewalCompleteSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "renewal",
				OutputKeyPattern: "renewalComplete.{actorSuffix}",
				BodyColumns: []string{
					"violating", "missing_renewalComplete", "entityKey", "tenant", "landlord",
					"open", "leaseappAlive", "hasGuarantor", "bgcheckValidUntil", "freshUntil",
					"guarantorVerifiedAt", "termsSetAt", "signedAt", "maxretries_renewalComplete",
				},
				EmptyBehavior: "delete",
				KeyColumn:     "entityId",
				Freshness:     "auto",
			},
		},
	}
}

// leaseExpirySpec anchors on EVERY leaseapp (a required MATCH — actorAggregate
// re-executes per anchor) and requires: a .tenancy aspect (no backfill — a
// leaseapp approved before this shipped has none and never enters this lens,
// design §4.1/§8.2), the application decided approved AND signed (the same
// applicantApproved-adjacent facts leaseApplicationCompleteSpec already
// checks, re-derived here rather than cross-lens-referenced — each §10.2 lens
// is a self-contained projection), and an alive unit with AT LEAST ONE
// manages-landlord (an ownerless unit has no counterparty to set terms, so it
// never opens a cycle; `count` over the inbound manages fan-out, no filtering
// WHERE on the OPTIONAL so a zero-landlord unit still keeps the anchor and
// simply reads landlordCount=0 → not open).
//
// freshUntil is null-when-past (§4.2, the shipped signing-lens posture,
// re-derived here rather than shared — see leaseApplicationCompleteSpec's
// doc): a lapsed renewalOpensAt must not re-arm the @at timer on every
// delivery (a past instant fires immediately at the engine, which is correct
// EXACTLY once — the null guard prevents an infinite re-arm loop on every
// subsequent reprojection of an already-open cycle).
//
// missing_renewalCycle requires $now >= renewalOpensAt (the cycle horizon
// arrived) AND no LIVE (non-tombstoned) renewal exists whose cycleEnd equals
// THIS tenancy's leaseEnd — counting a CANCELLED renewal (status='cancelled')
// as satisfying the count (design §4.4: a landlord's recorded decline for this
// term must not be reopened by the sweep on the next sweep tick; only a
// tombstoned renewal — never produced in v1, no revive op — would fail to
// count). The renewal fan-out is walked INBOUND across the renews link
// (renewal→leaseapp), the same inbound-traversal idiom the manages link uses.
const leaseExpirySpec = `
MATCH (app:leaseapp {key: $actorKey})
OPTIONAL MATCH (app)-[:appliesToUnit]->(u:unit)
OPTIONAL MATCH (u)<-[:manages]-(landlord:identity)
OPTIONAL MATCH (app)<-[:renews]-(rn:renewal)
WITH
  app.key                          AS entityKey,
  app.tenancy.data.leaseEnd        AS leaseEnd,
  app.tenancy.data.renewalOpensAt  AS renewalOpensAt,
  app.decision.data.value          AS landlordDecision,
  app.signature.data.signedAt      AS signedAt,
  u.key                            AS unitKey,
  count(DISTINCT landlord.key)     AS landlordCount,
  count(DISTINCT CASE WHEN rn.data.cycleEnd = app.tenancy.data.leaseEnd THEN rn.key ELSE null END) AS cycleRenewalCount
RETURN
  entityKey AS actorKey,
  entityKey,
  CASE WHEN renewalOpensAt > $now THEN renewalOpensAt ELSE null END AS freshUntil,
  ((renewalOpensAt <> null) AND (landlordDecision = 'approved') AND (signedAt <> null) AND (unitKey <> null) AND (landlordCount > 0) AND ($now >= renewalOpensAt) AND (cycleRenewalCount = 0)) AS missing_renewalCycle,
  ((renewalOpensAt <> null) AND (landlordDecision = 'approved') AND (signedAt <> null) AND (unitKey <> null) AND (landlordCount > 0) AND ($now >= renewalOpensAt) AND (cycleRenewalCount = 0)) AS violating
`

// renewalCompleteSpec anchors on EVERY renewal vertex, unfiltered by status
// (design §4.3: an actorAggregate lens has no filter-retraction transport, so
// completed/cancelled rows linger benignly with false columns — the shipped
// signing-lens posture; status-gating `open`/`missing_renewalComplete` keeps a
// closed cycle from ever re-violating). It walks BACK to the renewal's
// leaseapp (the renews link is OPTIONAL only in cypher SHAPE — every live
// renewal has exactly one by construction, OpenRenewal always creates it in
// the same batch as the vertex), then fans out exactly as
// leaseApplicationCompleteSpec does: applicationFor→identity(→.profile,
// →providedTo service instances) and appliesToUnit→unit(<-manages-landlord).
//
//   - tenant / landlord are the deterministic MIN-key picks across their
//     respective (single tenant; possibly-multi landlord) sets — landlord's
//     min-key pick is the one that matters (design §4.3 B5): many-landlords is
//     legal, and an engine-arbitrary pick would break one-row-per-anchor
//     determinism, so `min(landlord.key)` is the canonical manager, v1
//     semantic.
//   - hasGuarantor is the applicant's raw .profile flag (no freshness).
//   - bgcheckValidUntil is the freshest COMPLETED bgcheck's validUntil,
//     null-when-stale (the SAME freshness posture leaseApplicationCompleteSpec
//     uses on its own providedTo fan, re-derived here since this lens walks a
//     different anchor and cannot cross-reference that lens's row).
//   - freshUntil re-arms ONLY while the cycle is open (a completed/cancelled
//     cycle's bgcheck lapsing later must not resurrect a closed row's timer).
//   - guarantorVerifiedAt / termsSetAt / signedAt are the renewal's own
//     aspect-real facts (goalColumns-bridged in the target, not root-mapped).
//   - leaseappAlive gates a withdrawn/tombstoned leaseapp's renewal from
//     staying an immortal violating row (design §4.3).
//   - open ≡ status='open'; missing_renewalComplete requires open AND
//     leaseappAlive AND the goal NOT yet met — the goal's own boolean is
//     re-derived here (rather than shared with the gap's guard-grammar Goal)
//     because the ANCHOR PROJECTION and the PLANNER GOAL are two independent
//     evaluations over the same facts by design (§10.8): the lens computes
//     "is there work to do" (a plain boolean the engine drives dispatch from),
//     while Goal is the grammar the planner SEARCHES against — they must
//     agree, and TestRenewalComplete_MissingGoalAgreement pins that they do.
const renewalCompleteSpec = `
MATCH (rn:renewal {key: $actorKey})
OPTIONAL MATCH (rn)-[:renews]->(app:leaseapp)
OPTIONAL MATCH (app)-[:applicationFor]->(id:identity)
OPTIONAL MATCH (app)-[:appliesToUnit]->(u:unit)
OPTIONAL MATCH (u)<-[:manages]-(landlord:identity)
OPTIONAL MATCH (id)<-[:providedTo]-(inst:service)
WITH
  rn.key                                  AS entityKey,
  rn.data.status                          AS status,
  app.key                                 AS leaseAppKey,
  (app.key <> null AND app.isDeleted <> True) AS leaseappAlive,
  id.key                                  AS tenant,
  min(DISTINCT landlord.key)              AS landlordMin,
  id.profile.data.hasGuarantor            AS hasGuarantor,
  rn.guarantorVerification.data.verifiedAt AS guarantorVerifiedAt,
  rn.terms.data.setAt                     AS termsSetAt,
  rn.terms.data.termMonths                AS termsTermMonths,
  rn.renewalSignature.data.signedAt       AS signedAt,
  max(CASE WHEN inst.class = 'service.backgroundCheck.instance' AND inst.outcome.data.status = 'completed' AND inst.outcome.data.validUntil > $now THEN inst.outcome.data.validUntil ELSE null END) AS bgcheckValidUntil
RETURN
  entityKey AS actorKey,
  entityKey,
  leaseAppKey                             AS leaseApp,
  tenant,
  landlordMin                             AS landlord,
  leaseappAlive,
  (status = 'open')                       AS open,
  hasGuarantor,
  bgcheckValidUntil,
  CASE WHEN (status = 'open') THEN bgcheckValidUntil ELSE null END AS freshUntil,
  guarantorVerifiedAt,
  termsSetAt,
  signedAt,
  6                                       AS maxretries_renewalComplete,
  ((status = 'open') AND leaseappAlive AND NOT (
     (bgcheckValidUntil <> null) AND
     ((hasGuarantor = False) OR (guarantorVerifiedAt <> null)) AND
     (termsSetAt <> null) AND
     (signedAt <> null)
   )) AS missing_renewalComplete,
  ((status = 'open') AND leaseappAlive AND NOT (
     (bgcheckValidUntil <> null) AND
     ((hasGuarantor = False) OR (guarantorVerifiedAt <> null)) AND
     (termsSetAt <> null) AND
     (signedAt <> null)
   )) AS violating
`
