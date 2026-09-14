package leasesigning

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TenancyEndTarget is the §10.8 TargetID == the tenancyEnd lens's
// OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads, and the key
// the lens reads its own recorded lapse under in the leaseapp's freshnessExpiry
// marker. Both the target spec and the cypher are built from it so the timer
// that fires and the entry the row compares cannot drift apart (the
// LeaseExpiryTarget idiom).
const TenancyEndTarget = "tenancyEnd"

// TenancyEndLenses returns the tenancyEnd lens (design
// loftspace-lease-term-and-tenancy-end-design.md §2.2): the leaseapp-anchored
// frozen-table lens that turns a lease term's end into a RECORDED fact
// (EndTenancy writes .tenancy.endedAt) and then frees the unit the ended
// tenancy held (SetListingStatus available). It is the sibling of leaseExpiry
// — the same recorded-lapse timer shape on the same anchor, one horizon later:
// leaseExpiry watches renewalOpensAt and opens the renewal cycle, this one
// watches leaseEnd itself and ends the term the cycle did not extend.
func TenancyEndLenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  TenancyEndTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           tenancyEndSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "leaseapp",
				OutputKeyPattern: TenancyEndTarget + ".{actorSuffix}",
				BodyColumns: []string{
					"violating", "missing_tenancyEnded", "missing_relist", "entityKey", "unitKey", "freshUntil",
					"leaseEnd", "endedAt", "unitStatus",
				},
				EmptyBehavior: "delete",
				KeyColumn:     "entityId",
				Freshness:     "auto",
			},
		},
	}
}

// tenancyEndSpec anchors on EVERY leaseapp (a required MATCH — actorAggregate
// re-executes per anchor) and projects two gaps, one per half of "a lease that
// ends frees its unit":
//
//   - missing_tenancyEnded — the term has ended and nothing has recorded it:
//     the application is decided approved AND signed (the tenancy is a real
//     lease — the same two facts leaseExpirySpec requires, re-derived here
//     since each §10.2 lens is a self-contained projection), it carries a
//     .tenancy with a leaseEnd, that .tenancy carries NO endedAt yet, a timer
//     this target armed has RECORDED a fire at or after leaseEnd, and NO OPEN
//     renewal covers this term. → directOp EndTenancy{leaseAppKey}.
//   - missing_relist — the term is recorded as ended and the unit is still
//     marked leased with nobody else living in it: endedAt is set, the
//     appliesToUnit unit is live (unitKey <> null), its .listing.status is
//     'leased', and no OTHER approved application on that unit holds a live
//     (not-ended) tenancy. → directOp SetListingStatus{unit, status: available}.
//   - violating is the explicit OR of the two (Contract #10 §10.2 — Weaver
//     dispatches only violating rows).
//
// freshUntil is null-once-LAPSED, not null-when-past, exactly as leaseExpiry's
// is: it carries leaseEnd until the marker under this target's own byTarget key
// reaches it, or until the term is recorded as ended (an ended term has nothing
// left to wait for, and a still-armed @at on it would fire into a row that
// dispatches nothing). A leaseEnd already in the past projects VERBATIM, Weaver
// publishes the overdue @at and NATS releases it at once — the only path that
// records the lapse. The marker read rides the aggregating WITH as a scalar,
// the same way leaseEnd does, so the RETURN compares two carried values and
// the cypher references no clock parameter at all (Andrew, 2026-09-01: time
// facts are recorded on the entity, never $now in a lens). What re-arms the
// timer is the same comparison: SignRenewal extends leaseEnd past the recorded
// fire, lapsedAt >= leaseEnd goes false, and freshUntil projects the new end.
//
// Why an OPEN renewal holds the tenancy (openRenewalCount): a renewal with
// status 'open' whose cycleEnd equals THIS tenancy's leaseEnd means the parties
// are mid-negotiation on this very term — SetRenewalTerms / VerifyGuarantor /
// SignRenewal are still in flight, and SignRenewal's commit extends leaseEnd,
// which re-arms this timer on the new end. Ending the term underneath them
// would relist the unit out from under a tenant who is about to sign. A
// CANCELLED renewal (the landlord's recorded decline) or a never-opened one
// holds nothing: the term ends on its own date. Only 'open' counts; a
// 'complete' renewal has already moved leaseEnd and no longer matches
// cycleEnd, so it drops out of the count by construction. The renewal fan is
// walked INBOUND across renews (renewal→leaseapp), the leaseExpiry idiom.
//
// This open-renewal conjunct is DISPATCH-GATED ONLY: EndTenancy does not walk
// renewals — it reads the leaseapp + its .tenancy and refuses only NotYetEnded
// / NoTenancy (the SignRenewal-bgcheck-freshness posture, renewal_scripts.go:
// the lens decides when to dispatch, the op re-proves what it can from its own
// declared reads). An operator ending a term by hand while a renewal is open
// is admitted; SignRenewal then refuses TenancyEnded rather than dropping the
// endedAt its whole-aspect rewrite would otherwise lose.
//
// Why otherLiveTenancyCount is load-bearing: the relist gap reads a UNIT fact
// (status = 'leased') off a LEASEAPP anchor, and the unit outlives the
// tenancy. Once this ended application's unit re-leases to a new tenant, the
// unit reads 'leased' again — and this row still carries endedAt, so without
// the conjunct missing_relist re-opens on the OLD row and Weaver flips the
// NEW tenant's unit back to available, forever, on every redelivery. The
// conjunct counts OTHER approved applications on the same unit whose .tenancy
// exists (leaseStart <> null — an approved row with no tenancy was decided
// before .tenancy shipped and holds nothing) and is not ended; one such row
// means the unit is somebody else's now and this row is terminal. The
// `other.key <> app.key` term is what keeps the anchor's own tenancy out of
// its own count: the OPTIONAL MATCH back across appliesToUnit binds the anchor
// itself as one of the unit's applications. A landlord double-approval (two
// approved, un-ended tenancies on one unit — design §2.2) reads as
// otherLiveTenancyCount = 1 on each and holds the automatic relist; the
// landlord's manual Relist breaks the tie.
//
// A null unitKey (the appliesToUnit target tombstoned — the OPTIONAL MATCH
// drops it and unitKey / unitStatus both project null) must never open
// missing_relist: the playbook binds SetListingStatus's `unit` param to
// row.unitKey, and a Params template bound to a missed OPTIONAL hop is a
// dispatch refusal, not a no-op (the missing_listingLeased precedent in
// leaseApplicationCompleteSpec conjoins the same unitKey <> null).
//
// '= null' / '<> null' are the full engine's null tests (ruleengine/full
// values.go equalsAny: null = null is true, any value = null is false; `<>`
// is its negation). compareAny answers FALSE when either side of an ordering
// operator is nil, so an unmarked leaseapp reads (null >= leaseEnd) = false
// and stays armed; a leaseapp with no .tenancy reads leaseEnd = null, opens
// nothing, and projects a null freshUntil. Both aggregates are count(DISTINCT
// CASE ...) over their own fan's keys, so the renewal × other-application
// cross product never inflates either.
//
// Built with fmt.Sprintf so the target id comes from the constant the
// WeaverTargetSpec uses, which puts this Spec out of lint-lens-anchors'
// static reach; its advisory asks for a hand check for a narrowing range
// bound inside a NEGATED pattern, and there is none — the cypher has no
// negated relationship pattern at all.
var tenancyEndSpec = fmt.Sprintf(`
MATCH (app:leaseapp {key: $actorKey})
OPTIONAL MATCH (app)-[:appliesToUnit]->(u:unit)
OPTIONAL MATCH (app)<-[:renews]-(rn:renewal)
OPTIONAL MATCH (u)<-[:appliesToUnit]-(other:leaseapp)
WITH
  app.key                          AS entityKey,
  app.tenancy.data.leaseEnd        AS leaseEnd,
  app.tenancy.data.endedAt         AS endedAt,
  app.decision.data.value          AS landlordDecision,
  app.signature.data.signedAt      AS signedAt,
  u.key                            AS unitKey,
  u.listing.data.status            AS unitStatus,
  app.freshnessExpiry.data.byTarget.%[1]s AS lapsedAt,
  count(DISTINCT CASE WHEN rn.data.status = 'open' AND rn.data.cycleEnd = app.tenancy.data.leaseEnd THEN rn.key ELSE null END) AS openRenewalCount,
  count(DISTINCT CASE WHEN other.key <> app.key AND other.decision.data.value = 'approved' AND other.tenancy.data.leaseStart <> null AND other.tenancy.data.endedAt = null THEN other.key ELSE null END) AS otherLiveTenancyCount
RETURN
  entityKey AS actorKey,
  entityKey,
  unitKey,
  leaseEnd,
  endedAt,
  unitStatus,
  CASE WHEN (endedAt <> null) OR (lapsedAt >= leaseEnd) THEN null ELSE leaseEnd END AS freshUntil,
  ((landlordDecision = 'approved') AND (signedAt <> null) AND (leaseEnd <> null) AND (endedAt = null) AND (lapsedAt >= leaseEnd) AND (openRenewalCount = 0)) AS missing_tenancyEnded,
  ((endedAt <> null) AND (unitKey <> null) AND (unitStatus = 'leased') AND (otherLiveTenancyCount = 0)) AS missing_relist,
  (((landlordDecision = 'approved') AND (signedAt <> null) AND (leaseEnd <> null) AND (endedAt = null) AND (lapsedAt >= leaseEnd) AND (openRenewalCount = 0)) OR ((endedAt <> null) AND (unitKey <> null) AND (unitStatus = 'leased') AND (otherLiveTenancyCount = 0))) AS violating
`, TenancyEndTarget)
