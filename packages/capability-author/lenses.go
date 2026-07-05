package capabilityauthor

import "github.com/asolgan/lattice/internal/pkgmgr"

// Lenses returns the package's Lens declarations: the single
// capabilityAuthorPending weaver-target convergence lens (Contract #10 §10.2)
// that drives the escalation dispatch (design §3.4/§8).
//
// SELF-ANCHORED, not neighbor-projected: it anchors on ONE capabilityproposal
// vertex per reprojection ($actorKey), the same single-anchor no-hop shape
// packages/augur's augurDispatchPending lens uses. missing_authoring is true
// while the proposal's OWN .claim aspect is absent (CreateAuthoringClaim
// hasn't run yet) — a null-safe `= null` presence test (the full engine's
// documented null-test form; never IS NULL, per packages/lease-signing's
// lenses.go note). Once CreateAuthoringClaim writes the create-only .claim
// aspect, the SAME row reprojects missing_authoring=false, closing the gap —
// no negative/filter-retraction primitive needed (a single-row column
// overwrite, mirroring augurDispatchPending's approved→dispatched flip).
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  "capabilityAuthorPending",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           capabilityAuthorPendingSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "capabilityproposal",
				OutputKeyPattern: "capabilityAuthorDispatch.{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_authoring", "entityKey"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
	}
}

const capabilityAuthorPendingSpec = `
MATCH (p:capabilityproposal {key: $actorKey})
RETURN
  p.key AS entityKey,
  (p.claim.data.claimedAt = null) AS missing_authoring,
  (p.claim.data.claimedAt = null) AS violating
`
