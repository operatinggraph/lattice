package semanticcontracts

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// ClauseSatisfactionTarget is the §10.8 TargetID == the clauseSatisfaction
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
const ClauseSatisfactionTarget = "clauseSatisfaction"

// LeaseRentSettlementTarget is the §10.8 TargetID == the leaseRentSettlement
// lens's OutputKeyPattern prefix. It closes the bootstrap gap ahead of
// clauseSatisfaction: a signed, approved lease with an agreed rent never had
// anything mint its ledger account or its recurring rent clause (verticals.md
// "A signed lease never bills its rent") — clauseSatisfaction only ever
// converges a clause that already exists. This target opens the account,
// then the clause, mirroring cafe-domain's tabSettlement missing_account →
// directOp(CreateAccount) idiom; once the clause exists, clauseSatisfaction
// (above) owns billing it forever, so this target never dispatches
// DebitAccount itself.
const LeaseRentSettlementTarget = "leaseRentSettlement"

// Lenses returns the package's Lens declarations: `clauseSatisfaction` (§10.2
// actorAggregate covering all archetypes through Fire V3 — fixed/one-time,
// conditioned, judgment, recurring monthly, and prorated computational
// clauses) and `leaseRentSettlement` (the lease → account → clause bootstrap
// chain feeding it).
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  ClauseSatisfactionTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           clauseSatisfactionSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "clause",
				OutputKeyPattern: ClauseSatisfactionTarget + ".{actorSuffix}",
				BodyColumns: []string{"violating", "missing_charge", "missing_inspection", "entityKey", "clauseKey",
					"accountKey", "amountCents", "inspectorKey", "period", "chargeValidUntil", "freshUntil"},
				EmptyBehavior: "delete",
				KeyColumn:     "entityId",
				Freshness:     "auto",
			},
		},
		{
			CanonicalName:  LeaseRentSettlementTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           leaseRentSettlementSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "leaseapp",
				OutputKeyPattern: LeaseRentSettlementTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_account", "missing_clause", "entityKey", "leaseAppKey", "accountKey", "requestedRentCents"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
	}
}

// leaseRentSettlementSpec is the one-row-per-lease bootstrap cypher: an
// approved, signed lease application (DecideLeaseApplication's own
// approve-readiness floor already requires the signature, scripts.go) that
// carries an agreed rent needs a ledger account, then a recurring monthly
// rent clause, in two independent gap columns — mirroring cafe-domain's
// tabSettlement missing_account → missing_charge shape exactly (lenses.go),
// except the second gap here mints a CLAUSE, not a charge, because rent's
// actual recurring billing is clauseSatisfaction's job (above) once the
// clause exists:
//
//   - `missing_account` — approved + requestedRent present +
//     l.ledgerAccount.data.accountKey is null (loftspace-ledger's guard
//     aspect, the same property this package's own DebitAccount pathway
//     reads — no link walk needed, mirroring cafe-domain's
//     l.cafeLedgerAccount.data.accountKey read). Weaver dispatches
//     LoftspaceCreateAccount{leaseAppKey} (loftspace-ledger, targets.go).
//   - `missing_clause` — the account exists and no LIVE unconditioned
//     monthly clause governs this lease yet (count(DISTINCT CASE WHEN ...)
//     collapses the fan to a single existence check, the
//     clauseSatisfaction/objectLiveness idiom — lease-signing's own
//     freshBgComplete/payComplete columns use the identical
//     count(DISTINCT CASE WHEN ... THEN key ELSE null END) shape). The
//     period=monthly + conditioned<>true filter is deliberate, not
//     incidental: it is what lets this gate distinguish the auto-minted rent
//     clause from any OTHER clause a landlord might separately install on the
//     same lease (a one-time move-in fee, a conditioned pet fee) — those must
//     never suppress rent billing. Weaver dispatches
//     CreateClause{leaseAppKey, accountKey, amountCents: requestedRentCents,
//     period: "monthly", prose: <literal>} (this package).
//
// requestedRent (leaseapp .terms aspect, CreateLeaseApplication) is a plain
// DOLLAR figure, like every other rent-shaped field in LoftSpace
// (unit.listing.rentAmount, cmd/loftspace-app/web/app.js's "$"+rentAmount
// display) — but every ledger amount (CreateClause's amountCents,
// DebitAccount's amountCents) is integer CENTS. The ×100 conversion has to
// happen here, in the lens (the full engine's arithmetic BinaryOp,
// executor.go numericOp) — Weaver's GapActionSpec Params only ever
// substitute a row column verbatim or a literal (strategist.go resolveParam),
// never compute one — so requestedRentCents is the only column the
// missing_clause dispatch may template as amountCents; templating the raw
// requestedRent column would underbill by 100x.
//
// A lease with no requestedRent (an application that skipped moveInDate, or
// one created before requestedRent was captured) never projects a row — it
// simply never violates, exactly like clauseSatisfaction's own "no
// missing_account gap" precedents (clinic-ledger/cafe-domain) degrade when a
// prerequisite fact is absent, rather than dispatching a malformed op.
const leaseRentSettlementSpec = `MATCH (l:leaseapp {key: $actorKey})
OPTIONAL MATCH (l)<-[:governs]-(c:clause)
WITH
  l.key AS entityKey,
  l.decision.data.value AS decision,
  l.terms.data.requestedRent AS requestedRent,
  l.ledgerAccount.data.accountKey AS accountKey,
  count(DISTINCT CASE WHEN (c.terms.data.period = 'monthly') AND (c.terms.data.conditioned <> true) THEN c.key ELSE null END) AS rentClauseCount
WHERE (decision = 'approved') AND (requestedRent <> null)
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS leaseAppKey,
  accountKey,
  (requestedRent * 100) AS requestedRentCents,
  (accountKey = null) AS missing_account,
  ((accountKey <> null) AND (rentClauseCount = 0)) AS missing_clause,
  ((accountKey = null) OR ((accountKey <> null) AND (rentClauseCount = 0))) AS violating
`

// clauseSatisfactionSpec is the one-row-per-clause satisfaction cypher (§3.2
// of the design). Two independent gaps, never both live on the same clause
// (CreateClause writes exactly one of accountKey/amountCents (computational)
// or an inspector link (judgment)):
//
//   - `missing_charge` — true while the clause charges an account, is either
//     unconditioned or its conditionedOn target is still live, and no
//     transaction `authorizedBy` it exists yet. count(t.key) collapses the
//     fan to a single existence check (the objectLiveness liveOwners idiom).
//     "Conditioned" is a `terms.conditioned` data flag set at CreateClause
//     time (not inferred from link/target liveness — a tombstoned
//     conditionedOn TARGET makes condKey resolve null exactly like "never
//     conditioned" would, so only an explicit flag can tell them apart; the
//     flag is true only when CreateClause received a conditionedOnKey). The
//     gate reads `conditioned <> true`, not `conditioned = false`: a
//     pre-this-fire clause's `.terms` aspect has no `conditioned` key at all
//     (Fire V1's shape), so `conditioned` resolves to null — `null = false`
//     is false (equalsAny only equals nil to nil), which would wrongly
//     collapse the whole OR to false and permanently suppress the charge for
//     every legacy clause. `<> true` correctly treats both `false` and
//     absent (null) as "not conditioned."
//   - `missing_inspection` — true while the clause has an assigned inspector
//     (judgment) and no .inspection aspect has been written yet.
//
// Null comparisons use the shipped `= null` / `<> null` idiom (lease-signing
// precedent), not `IS NULL`/`IS NOT NULL`: this grammar's
// oC_StringListNullOperatorExpression visitor deliberately passes those
// suffixes through unevaluated (full/visitor.go), so `IS NOT NULL` silently
// no-ops to the bare operand rather than a boolean. Every null-tested column
// here is itself a `.key`/aspect PROPERTY access (never a bare MATCH node
// variable): resolveProperty converts an unmatched OPTIONAL MATCH node's
// typed-nil `*nodeRef` to a clean interface nil via a direct pointer check,
// so `= null`/`<> null` sees a real nil — a bare node variable would still be
// a non-nil interface (Go's typed-nil-in-interface trap) and compare unequal
// to null even when unmatched.
//
// Deliberately does NOT gate the oneTime archetype on `.status.data.state =
// 'active'`: per the design's R3, a status-flip that removes the anchor from
// a WHERE-filtered match is the deferred negative/filter-retraction primitive
// (Fires 1+2 shipped the plain-lens retraction transport 2026-07-02, but
// wiring it into actorAggregate lenses like this one is a later target-diff
// increment — see the design's R3 v1 constraint). A oneTime
// clause instead relies purely on the upsert-safe signal — once the
// authorizing transaction exists, the gap flips false and STAYS false (the
// row lingers non-violating, which is harmless).
//
// Fire V3 (recurring + proration):
//
//   - `period` (c.terms.data.period, always present — every CreateClause
//     stamps it) branches missing_charge's gate in two mutually exclusive
//     ways. period<>'monthly' (oneTime, the default) keeps the exact Fire
//     V1/V2 chargeCount=0 check above. period='monthly' instead mirrors
//     lease-signing's bgcheck-freshness pattern: the gate is
//     `chargeValidUntil = null OR a recorded lapse reaching chargeValidUntil`
//     — a freshness decay read off c.status.data.chargeValidUntil (DebitAccount
//     re-stamps it on every recurring charge), not a transaction count. This
//     is why a monthly clause's .status aspect is NOT purely audit like the
//     oneTime case (see clauseStatusAspectTypeDDL) — chargeValidUntil is the
//     actual convergence signal for that archetype.
//   - The lapse is a FACT on the clause, not a clock reading: when the @at this
//     lens arms fires, MarkExpired records the instant in the clause's
//     freshnessExpiry marker under this target's own key, and `lapsedAt` is
//     that entry, carried through the aggregating WITH as a scalar beside
//     chargeValidUntil. Both operands of the comparison are stored graph data,
//     so the row is a pure function of the subgraph and two projections at
//     different wall-clock instants over the same graph agree. compareAny
//     answers false when either operand is nil, so a clause no timer has fired
//     on reads unlapsed — and the explicit `chargeValidUntil = null` arm is
//     what still opens the gap for a monthly clause that has never been
//     charged and so has no window at all.
//   - `freshUntil` arms Weaver's temporal lane (internal/weaver/temporal.go)
//     the same way lease-signing's bgcheck does: while no recorded lapse
//     reaches a monthly clause's chargeValidUntil, freshUntil projects that
//     same instant so an @at fires right when it lapses (nothing else would
//     CDC-trigger a re-read at that moment); once the lapse is recorded (or
//     for a oneTime clause, always) freshUntil is null — no timer armed,
//     chargeCount/gap-driven dispatch owns it instead. A chargeValidUntil
//     already in the past is projected VERBATIM, so the overdue @at fires at
//     once and records the lapse that opens the gap.
//   - Proration needs NO lens change at all: a prorated clause's amountCents
//     was computed ONCE by CreateClause (exact Starlark bignum integer
//     arithmetic, ddls.go) and stored like any flat fee, so it flows through
//     the existing oneTime chargeCount=0 gate unchanged.
const clauseSatisfactionSpec = `
MATCH (c:clause {key: $actorKey})
OPTIONAL MATCH (c)-[:chargesTo]->(a:account)
OPTIONAL MATCH (c)-[:conditionedOn]->(cond)
OPTIONAL MATCH (c)-[:requiresInspectionBy]->(insp:identity)
OPTIONAL MATCH (c)<-[:authorizedBy]-(t:transaction)
WITH
  c.key AS entityKey,
  a.key AS accountKey,
  cond.key AS condKey,
  insp.key AS inspectorKey,
  c.terms.data.amountCents AS amountCents,
  c.terms.data.conditioned AS conditioned,
  c.terms.data.period AS period,
  c.status.data.chargeValidUntil AS chargeValidUntil,
  c.freshnessExpiry.data.byTarget.clauseSatisfaction AS lapsedAt,
  c.inspection.data.completed AS inspectionCompleted,
  count(t.key) AS chargeCount
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS clauseKey,
  accountKey,
  amountCents,
  inspectorKey,
  period,
  chargeValidUntil,
  ((accountKey <> null) AND ((conditioned <> true) OR (condKey <> null)) AND
   (((period <> 'monthly') AND (chargeCount = 0))
    OR ((period = 'monthly') AND ((chargeValidUntil = null) OR (lapsedAt >= chargeValidUntil))))
  ) AS missing_charge,
  ((inspectorKey <> null) AND (inspectionCompleted = null)) AS missing_inspection,
  CASE WHEN (period = 'monthly') AND (chargeValidUntil <> null) AND NOT (lapsedAt >= chargeValidUntil)
       THEN chargeValidUntil ELSE null END AS freshUntil,
  (
    ((accountKey <> null) AND ((conditioned <> true) OR (condKey <> null)) AND
     (((period <> 'monthly') AND (chargeCount = 0))
      OR ((period = 'monthly') AND ((chargeValidUntil = null) OR (lapsedAt >= chargeValidUntil)))))
    OR ((inspectorKey <> null) AND (inspectionCompleted = null))
  ) AS violating
`
