package leasesigning

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// RenewalLenses returns the package's renewal lenses (design
// loftspace-lease-renewal-goal-authored-target-design.md §4.2/§4.3/§4.5):
//
//   - leaseExpiry (Target A, frozen table): anchored on the LEASEAPP, opens the
//     renewal cycle once a signed+approved application's tenancy nears its
//     renewalOpensAt horizon.
//   - renewalComplete (Target B, mode: planned): anchored on the RENEWAL
//     vertex, walks back to its leaseapp for tenant/landlord/guarantor/bgcheck
//     facts, and is the first goal-authored gap (§10.8 Planner extension).
//   - renewalsRead (R3): the FE-facing, dual-anchored (tenant + landlord)
//     protected Postgres read model — the sibling of the two Weaver-internal
//     lenses above, but for display rather than dispatch.
//
// All three share the SAME shape of walk (renewal→leaseapp→{applicant, unit})
// that leaseApplicationCompleteSpec already established; comments here focus on
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
		{
			// renewalsRead — the protected Postgres FE read model (design §4.5,
			// R3). Sibling of leaseApplicationsRead/landlordLeaseApplicationsRead,
			// but DUAL-anchored: one row per renewal cycle, readable by EITHER
			// the tenant OR the managing landlord (§6.14's authz_anchors is a SET
			// with any-match RLS semantics — no new machinery, just two elements
			// instead of one). This is the FE-facing sibling of the Weaver-internal
			// renewalComplete lens above: same walk, same facts, but a plain
			// (non-actorAggregate) projection into an RLS table instead of the
			// weaver-targets orchestration bucket, so the two can never be
			// conflated at the read boundary (P5: apps read this, never
			// weaver-targets).
			//
			// Every MATCH is REQUIRED (the leaseApplicationsRead/
			// landlordLeaseApplicationsRead fail-closed convention): a renewal
			// missing its leaseapp/tenant/unit/landlord link projects no row
			// rather than a null-anchored one. In practice this is a no-op
			// exclusion — OpenRenewal never fires until leaseExpiry's own gate
			// already required a live unit with >=1 manager (leaseExpirySpec
			// below), so every renewal this lens ever sees already has all four
			// links.
			CanonicalName: "renewalsRead",
			Class:         "meta.lens",
			Adapter:       "postgres",
			Table:         "read_renewals",
			Engine:        "full",
			Spec:          renewalsReadSpec,
			Protected:     true,
			IntoKey:       []string{"renewal_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "entity_key", Type: "text"},
				{Name: "lease_app", Type: "text"},
				{Name: "tenant", Type: "text"},
				{Name: "landlord", Type: "text"},
				{Name: "status", Type: "text"},
				{Name: "cycle_end", Type: "text"},
				{Name: "unit_address", Type: "text"},
				{Name: "rent_amount", Type: "double precision"},
				{Name: "term_months", Type: "double precision"},
				{Name: "terms_set_at", Type: "text"},
				{Name: "has_guarantor", Type: "boolean"},
				{Name: "guarantor_verified_at", Type: "text"},
				{Name: "guarantor_method", Type: "text"},
				{Name: "signed_at", Type: "text"},
				{Name: "cancel_reason", Type: "text"},
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

// renewalsReadSpec is renewalsRead's cypher — the FE-facing dual-anchor sibling
// of renewalCompleteSpec above. It mirrors that lens's walk (renewal→leaseapp→
// {applicant, unit→landlord}) verbatim rather than cross-referencing it (each
// §10.2-adjacent lens is a self-contained projection, the same posture
// renewalCompleteSpec's own doc comment states), but projects DISPLAY columns
// for the FE (unit address, terms, verification/signature timestamps) instead
// of the engine's boolean gap columns, and closes with an authz_anchors set
// instead of the weaver-targets envelope.
//
//   - landlord (the single display/action-target field) is the SAME
//     deterministic MIN-key pick renewalCompleteSpec uses — the planner
//     assigns SetRenewalTerms/VerifyGuarantor tasks to exactly ONE canonical
//     manager, so the engine-dispatch semantics need a single winner.
//     authz_anchors is a DIFFERENT question — READ access — and must NOT
//     collapse to that same single winner: landlordLeaseApplicationsRead
//     (this file's sibling) already establishes that every co-manager gets a
//     row (there, by fanning out one row per co-manager); here, since the
//     read model stays one-row-per-renewal (matching renewalCompleteSpec's
//     shape, not the fan-out shape), the fix is to fold ALL managing
//     landlords' NanoIDs into the anchor SET instead. A min-key-only anchor
//     set would silently deny read access to a legitimate co-manager who
//     isn't the canonical one — caught in review, not by accident.
//   - hasGuarantor is the tenant's raw .profile flag, read here (not bridged
//     from renewalComplete) so this lens has no runtime dependency on the
//     Weaver-internal target.
//   - authz_anchors = tenant + EVERY managing landlord's bare NanoID (§6.14's
//     set is exactly this: any-match over an arbitrary-size set, not a pair).
//     Both the tenant and every co-manager may read their own renewal row; a
//     third party sees nothing (the primordial cap-read self-grant already
//     grants every identity its own NanoID, so no new grant-lens is needed
//     for any of them).
const renewalsReadSpec = `
MATCH (rn:renewal)
MATCH (rn)-[:renews]->(app:leaseapp)
MATCH (app)-[:applicationFor]->(tenant:identity)
MATCH (app)-[:appliesToUnit]->(u:unit)
MATCH (u)<-[:manages]-(landlord:identity)
WITH
  rn.key                                   AS entityKey,
  rn.data.status                           AS status,
  rn.data.cycleEnd                         AS cycleEnd,
  rn.data.reason                           AS cancelReason,
  app.key                                  AS leaseAppKey,
  tenant.key                               AS tenantKey,
  min(DISTINCT landlord.key)               AS landlordKey,
  collect(DISTINCT nanoIdFromKey(landlord.key)) AS landlordAnchors,
  u.address.data.line1                     AS unitAddress,
  tenant.profile.data.hasGuarantor         AS hasGuarantor,
  rn.terms.data.rentAmount                 AS rentAmount,
  rn.terms.data.termMonths                 AS termMonths,
  rn.terms.data.setAt                      AS termsSetAt,
  rn.guarantorVerification.data.verifiedAt AS guarantorVerifiedAt,
  rn.guarantorVerification.data.method     AS guarantorMethod,
  rn.renewalSignature.data.signedAt        AS signedAt
RETURN
  nanoIdFromKey(entityKey)                 AS renewal_id,
  entityKey                                AS entity_key,
  leaseAppKey                              AS lease_app,
  tenantKey                                AS tenant,
  landlordKey                              AS landlord,
  status,
  cycleEnd                                 AS cycle_end,
  unitAddress                              AS unit_address,
  rentAmount                               AS rent_amount,
  termMonths                               AS term_months,
  termsSetAt                               AS terms_set_at,
  hasGuarantor                             AS has_guarantor,
  guarantorVerifiedAt                      AS guarantor_verified_at,
  guarantorMethod                          AS guarantor_method,
  signedAt                                 AS signed_at,
  cancelReason                             AS cancel_reason,
  [nanoIdFromKey(tenantKey)] + landlordAnchors AS authz_anchors
`
