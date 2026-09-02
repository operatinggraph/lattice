package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// hubScopeQuery walks one typed relation off a marked identity hub.
const hubScopeQuery = `MATCH (i:identity {key: $k})-[:holdsRole]->(r:role) RETURN r.key AS roleKey`

// seedSoftDeletedHubLink rewrites a link envelope with isDeleted set, the way
// a retraction reaches a marked node: the edge leaves the node's list while
// the KV entry remains.
func seedSoftDeletedHubLink(t *testing.T, coreKV *substrate.KV, linkKey string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"key": linkKey, "isDeleted": true})
	require.NoError(t, err)
	_, err = coreKV.Put(context.Background(), linkKey, body)
	require.NoError(t, err)
}

// captureHubScopedFootprint evaluates hubScopeQuery over a marked hub with the
// real full engine and returns the footprint, having first asserted the shape
// this file is about: the hub was read at the hop's relation, so it carries a
// selector record and NO whole-read fingerprint.
func captureHubScopedFootprint(t *testing.T, coreKV, adjKV *substrate.KV, hubID string) ruleengine.EvalFootprint {
	t.Helper()
	eng := full.New().WithHubReadScopeMode(full.HubReadScopeModeOn)
	cr, err := eng.Parse(hubScopeQuery)
	require.NoError(t, err)

	_, fp, err := eng.ExecuteWithFootprint(context.Background(), cr,
		ruleengine.EventContext{Parameters: map[string]any{"k": "vtx.identity." + hubID}},
		adjKV, coreKV)
	require.NoError(t, err)

	require.NotContains(t, fp.EdgeRevisions, hubID,
		"a typed hop over a marked hub records no whole-read fingerprint for it")
	sel, ok := fp.EdgeSelectors[hubID]
	require.True(t, ok, "and is footprinted by the selector it consulted")
	require.False(t, sel.Fallback)
	require.NotEmpty(t, sel.Matched)
	return fp
}

// TestFootprintValid_HubScopedFootprint_ValidatesAtTheRelationScope pins §9.1
// rule 4's selector path against the footprint shape rule 3 produces: a marked
// hub carried by its Matched sets alone, with no fingerprint to fall back on.
// The validator re-reads the hub at exactly the relations the footprint names,
// so a write to a relation the walk never followed is not drift — and every
// change to the relation it DID follow is: an addition, a retraction, and a
// swap that leaves the count untouched.
func TestFootprintValid_HubScopedFootprint_ValidatesAtTheRelationScope(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}

	// setup seeds a marked identity hub holding one role, and returns the
	// pipeline, the KVs, the hub id and the footprint of a holdsRole walk.
	setup := func(t *testing.T, tag string) (*Pipeline, *substrate.KV, *substrate.KV, string, string, ruleengine.EvalFootprint) {
		t.Helper()
		hubID := hubNanoID(t, tag)
		p, coreKV, adjKV := markedHubKV(t, hubID)
		writeCollisionVertex(t, coreKV, "vtx.identity."+hubID, "identity", nil)

		roleID := hubNanoID(t, "rr"+tag)
		writeCollisionVertex(t, coreKV, "vtx.role."+roleID, "role", nil)
		held := seedHubLink(t, coreKV, "identity", hubID, "holdsRole", "role", roleID)

		fp := captureHubScopedFootprint(t, coreKV, adjKV, hubID)
		valid, verr := p.footprintValid(context.Background(), fp)
		require.NoError(t, verr)
		require.True(t, valid, "an untouched hub must validate")
		return p, coreKV, adjKV, hubID, held, fp
	}

	t.Run("unrelated relation is not drift", func(t *testing.T) {
		p, coreKV, _, hubID, _, fp := setup(t, "hs1")
		seedHubLink(t, coreKV, "identity", hubID, "worksAt", "org", hubNanoID(t, "og1"))

		valid, verr := p.footprintValid(context.Background(), fp)
		require.NoError(t, verr)
		require.True(t, valid,
			"a write to a relation the footprint never named must not read as drift on a hub-scoped footprint")
	})

	t.Run("same relation added is drift", func(t *testing.T) {
		p, coreKV, _, hubID, _, fp := setup(t, "hs2")
		seedHubLink(t, coreKV, "identity", hubID, "holdsRole", "role", hubNanoID(t, "rz2"))

		valid, verr := p.footprintValid(context.Background(), fp)
		require.NoError(t, verr)
		require.False(t, valid, "a second holdsRole link changes the selector's matched set")
	})

	t.Run("same relation retracted is drift", func(t *testing.T) {
		p, coreKV, _, _, held, fp := setup(t, "hs3")
		seedSoftDeletedHubLink(t, coreKV, held)

		valid, verr := p.footprintValid(context.Background(), fp)
		require.NoError(t, verr)
		require.False(t, valid, "a soft-tombstoned holdsRole link leaves the matched set")
	})

	t.Run("same relation swapped is drift", func(t *testing.T) {
		p, coreKV, _, hubID, held, fp := setup(t, "hs4")
		// A revoke-and-grant landing together: the same COUNT of holdsRole
		// links, different identities.
		seedSoftDeletedHubLink(t, coreKV, held)
		seedHubLink(t, coreKV, "identity", hubID, "holdsRole", "role", hubNanoID(t, "rz4"))

		valid, verr := p.footprintValid(context.Background(), fp)
		require.NoError(t, verr)
		require.False(t, valid, "a same-count swap of holdsRole links is still drift")
	})
}

// TestFootprintValid_CoarsePath_StaleMatchedSetUnderCurrentFingerprint pins the
// coarse path's set comparison — the arm §9.1 rule 4 adds, and the ONLY thing
// that can see this class of drift.
//
// The shape is typed-then-untyped on one marked hub. The typed hop reads the
// hub at its own relation at t1 and records a Matched set; a holdsRole link
// then commits; the untyped hop reads the hub whole at t2 and records the
// fingerprint. Nothing moves after t2, so the fingerprint still compares EQUAL
// at validation — the control case below proves that directly — and only
// re-deriving the earlier-instant Matched set catches the write that landed
// between the two hops.
func TestFootprintValid_CoarsePath_StaleMatchedSetUnderCurrentFingerprint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	hubID := hubNanoID(t, "cp1")
	p, coreKV, adjKV := markedHubKV(t, hubID)

	seedHubLink(t, coreKV, "identity", hubID, "holdsRole", "role", hubNanoID(t, "rza"))

	// t1 — the typed hop's relation-scoped read of the hub.
	scoped, _, err := adjacency.NeighborsByRelation(ctx, adjKV, coreKV, hubID,
		map[string]struct{}{"holdsRole": {}})
	require.NoError(t, err)
	require.Len(t, scoped, 1)
	selector := ruleengine.EdgeSelector{RelType: "holdsRole", Direction: "out"}
	matched := map[string]struct{}{scoped[0].EdgeID: {}}

	// A second role is granted between the two hops.
	seedHubLink(t, coreKV, "identity", hubID, "holdsRole", "role", hubNanoID(t, "rzb"))

	// t2 — the untyped hop reads the hub whole and records the fingerprint.
	_, rev, err := adjacency.Neighbors(ctx, adjKV, coreKV, hubID)
	require.NoError(t, err)
	require.NotZero(t, rev)

	// Control: the fingerprint ALONE cannot see it. Nothing has moved since
	// t2, so a footprint carrying only the whole read validates — which is
	// exactly why the coarse path re-derives the sets as well.
	fingerprintOnly := ruleengine.EvalFootprint{EdgeRevisions: map[string]uint64{hubID: rev}}
	valid, verr := p.footprintValid(ctx, fingerprintOnly)
	require.NoError(t, verr)
	require.True(t, valid, "the whole-read fingerprint is unchanged since t2 — it cannot catch this")

	fp := ruleengine.EvalFootprint{
		EdgeRevisions: map[string]uint64{hubID: rev},
		EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
			hubID: {
				Fallback: true,
				Matched:  map[ruleengine.EdgeSelector]map[string]struct{}{selector: matched},
			},
		},
	}
	valid, verr = p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.False(t, valid,
		"a Matched set observed before the untyped hop's whole read must be re-derived, or the write between the two hops goes undetected")
}

// TestFootprintValid_CoarsePath_NoFingerprintIsMalformed pins the fail-closed
// direction of the coarse path: an untyped hop always reads its node whole, so
// a Fallback entry with no EdgeRevisions fingerprint cannot have come from an
// evaluation. There is nothing to compare, and the validator reports drift
// rather than validating a node it never checked.
func TestFootprintValid_CoarsePath_NoFingerprintIsMalformed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	hubID := hubNanoID(t, "cp2")
	p, coreKV, _ := markedHubKV(t, hubID)
	seedHubLink(t, coreKV, "identity", hubID, "holdsRole", "role", hubNanoID(t, "rzc"))

	fp := ruleengine.EvalFootprint{
		EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
			hubID: {Fallback: true},
		},
	}
	valid, verr := p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.False(t, valid, "a coarse node with no fingerprint is malformed and must fail closed")
}
