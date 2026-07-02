package bespokecontracts

import "github.com/asolgan/lattice/internal/pkgmgr"

// ClauseSatisfactionTarget is the §10.8 TargetID == the clauseSatisfaction
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
const ClauseSatisfactionTarget = "clauseSatisfaction"

// Lenses returns the package's Lens declarations: the single
// `clauseSatisfaction` actorAggregate convergence lens (§10.2), Fire V1's
// fixed/one-time computational archetype.
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
				BodyColumns:      []string{"violating", "missing_charge", "entityKey", "clauseKey", "accountKey", "amountCents"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
	}
}

// clauseSatisfactionSpec is the one-row-per-clause satisfaction cypher (§3.2
// of the design). `missing_charge` is true until a transaction
// `authorizedBy` this clause exists — count(t.key) collapses the fan to a
// single existence check (the objectLiveness liveOwners idiom), so the query
// needs no filtering WHERE and stays one row per anchor even before any
// charge posts.
//
// Deliberately does NOT gate on `.status.data.state = 'active'`: per the
// design's R3, a status-flip that removes the anchor from a WHERE-filtered
// match is the deferred negative/filter-retraction primitive this platform
// has not built (upsert alone does not retract a dropped composite key). Fire
// V1 instead relies purely on the upsert-safe signal — once the authorizing
// transaction exists, `missing_charge`/`violating` flip false and STAY false
// (the row lingers non-violating, which is harmless — see the design's R3 v1
// constraint). The .status aspect DebitAccount writes is audit/display
// bookkeeping only, never the convergence gate.
const clauseSatisfactionSpec = `
MATCH (c:clause {key: $actorKey})
OPTIONAL MATCH (c)-[:chargesTo]->(a:account)
OPTIONAL MATCH (c)<-[:authorizedBy]-(t:transaction)
WITH
  c.key AS entityKey,
  a.key AS accountKey,
  c.terms.data.amountCents AS amountCents,
  count(t.key) AS chargeCount
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS clauseKey,
  accountKey,
  amountCents,
  (chargeCount = 0) AS missing_charge,
  (chargeCount = 0) AS violating
`
