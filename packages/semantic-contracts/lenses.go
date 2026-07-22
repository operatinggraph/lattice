package semanticcontracts

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// ClauseSatisfactionTarget is the §10.8 TargetID == the clauseSatisfaction
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
const ClauseSatisfactionTarget = "clauseSatisfaction"

// Lenses returns the package's Lens declarations: the single
// `clauseSatisfaction` actorAggregate convergence lens (§10.2) covering all
// archetypes through Fire V3 (fixed/one-time, conditioned, judgment,
// recurring monthly, and prorated computational clauses).
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
	}
}

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
// increment, not this fire — see the design's R3 v1 constraint). A oneTime
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
//     `chargeValidUntil = null OR chargeValidUntil <= $now` — a plain
//     freshness decay read off c.status.data.chargeValidUntil (DebitAccount
//     re-stamps it on every recurring charge), not a transaction count. This
//     is why a monthly clause's .status aspect is NOT purely audit like the
//     oneTime case (see clauseStatusAspectTypeDDL) — chargeValidUntil is the
//     actual convergence signal for that archetype.
//   - `freshUntil` arms Weaver's temporal lane (internal/weaver/temporal.go)
//     the same way lease-signing's bgcheck does: while a monthly clause's
//     chargeValidUntil is still in the future, freshUntil projects that same
//     instant so an @at timer forces re-projection right when it lapses
//     (nothing else would CDC-trigger a re-read at that exact moment); once
//     it lapses (or for a oneTime clause, always) freshUntil is null — no
//     timer armed, chargeCount/gap-driven dispatch owns it instead.
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
    OR ((period = 'monthly') AND ((chargeValidUntil = null) OR (chargeValidUntil <= $now))))
  ) AS missing_charge,
  ((inspectorKey <> null) AND (inspectionCompleted = null)) AS missing_inspection,
  CASE WHEN (period = 'monthly') AND (chargeValidUntil <> null) AND (chargeValidUntil > $now)
       THEN chargeValidUntil ELSE null END AS freshUntil,
  (
    ((accountKey <> null) AND ((conditioned <> true) OR (condKey <> null)) AND
     (((period <> 'monthly') AND (chargeCount = 0))
      OR ((period = 'monthly') AND ((chargeValidUntil = null) OR (chargeValidUntil <= $now)))))
    OR ((inspectorKey <> null) AND (inspectionCompleted = null))
  ) AS violating
`
