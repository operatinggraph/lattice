package pipeline

import (
	"context"
	"encoding/json"
	"sync"
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

// readRecorder collects the adjacency reads made under it, so a test can pin
// WHICH read a caller took rather than inferring it from a footprint's shape.
type readRecorder struct {
	mu   sync.Mutex
	seen []adjacency.ReadObservation
}

func (r *readRecorder) observe(obs adjacency.ReadObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// The observation's Relations set belongs to the caller, so copy it before
	// keeping it past the call.
	rels := map[string]struct{}{}
	for rel := range obs.Relations {
		rels[rel] = struct{}{}
	}
	if obs.Relations == nil {
		rels = nil
	}
	obs.Relations = rels
	r.seen = append(r.seen, obs)
}

// of returns every read recorded for one node.
func (r *readRecorder) of(nodeID string) []adjacency.ReadObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []adjacency.ReadObservation
	for _, obs := range r.seen {
		if obs.NodeID == nodeID {
			out = append(out, obs)
		}
	}
	return out
}

// observedCtx returns a context whose adjacency reads land in a fresh recorder.
func observedCtx() (context.Context, *readRecorder) {
	rec := &readRecorder{}
	return adjacency.WithReadObserver(context.Background(), rec.observe), rec
}

// captureHubFootprint evaluates hubScopeQuery over a marked hub with the real
// full engine at the given posture and returns the footprint, having first
// asserted the shape that posture is supposed to produce: a typed hop records
// its selector either way, and the whole-read fingerprint is present only when
// the hop actually read the hub whole.
func captureHubFootprint(t *testing.T, coreKV, adjKV *substrate.KV, hubID string, mode full.HubReadScopeMode) ruleengine.EvalFootprint {
	t.Helper()
	eng := full.New().WithHubReadScopeMode(mode)
	cr, err := eng.Parse(hubScopeQuery)
	require.NoError(t, err)

	ctx, rec := observedCtx()
	_, fp, err := eng.ExecuteWithFootprint(ctx, cr,
		ruleengine.EventContext{Parameters: map[string]any{"k": "vtx.identity." + hubID}},
		adjKV, coreKV)
	require.NoError(t, err)

	sel, ok := fp.EdgeSelectors[hubID]
	require.True(t, ok, "a typed hop records its selector at either posture")
	require.False(t, sel.Fallback)
	require.NotEmpty(t, sel.Matched)

	reads := rec.of(hubID)
	require.Len(t, reads, 1, "the hub is read exactly once per evaluation")
	if mode == full.HubReadScopeModeOn {
		require.Equal(t, map[string]struct{}{"holdsRole": {}}, reads[0].Relations,
			"with the scope on the hub is read at the hop's relation")
		require.False(t, reads[0].Whole)
		require.NotContains(t, fp.EdgeRevisions, hubID,
			"a scoped read's fingerprint is not comparable with a whole read's, so it is not recorded")
	} else {
		require.Nil(t, reads[0].Relations, "with the scope off the hub is read whole")
		require.True(t, reads[0].Whole)
		require.Contains(t, fp.EdgeRevisions, hubID,
			"a whole read does record its fingerprint")
	}
	return fp
}

// TestFootprintValid_HubScopedFootprint_ValidatesAtTheRelationScope pins §9.1
// rule 4's selector path against the footprint shape rule 3 produces: a marked
// hub carried by its Matched sets alone, with no fingerprint to fall back on.
// The validator re-reads the hub at exactly the relations the footprint names,
// so a write to a relation the walk never followed is not drift — and every
// change to the relation it DID follow is: an addition, a retraction, and a
// swap that leaves the count untouched.
//
// Every case runs at BOTH postures and asserts the same verdict. That is the
// load-bearing half of "REFRACTOR_HUB_READ_SCOPE=off is the way back": the
// footprint's SHAPE differs between them (captureHubFootprint pins that), but
// what counts as drift must not, or the switch would be a correctness knob
// rather than a containment lever.
func TestFootprintValid_HubScopedFootprint_ValidatesAtTheRelationScope(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}

	// setup seeds a marked identity hub holding one role, and returns the
	// pipeline, the KVs, the hub id and the footprint of a holdsRole walk.
	setup := func(t *testing.T, tag string, mode full.HubReadScopeMode) (*Pipeline, *substrate.KV, string, string, ruleengine.EvalFootprint) {
		t.Helper()
		hubID := hubNanoID(t, tag)
		p, coreKV, adjKV := markedHubKV(t, hubID)
		writeCollisionVertex(t, coreKV, "vtx.identity."+hubID, "identity", nil)

		roleID := hubNanoID(t, "rr"+tag)
		writeCollisionVertex(t, coreKV, "vtx.role."+roleID, "role", nil)
		held := seedHubLink(t, coreKV, "identity", hubID, "holdsRole", "role", roleID)

		fp := captureHubFootprint(t, coreKV, adjKV, hubID, mode)

		// The validator's own re-read takes the scope the footprint names, at
		// either posture: every hop on this hub was typed, so validation is
		// the selector path, which re-reads by relation rather than whole.
		ctx, rec := observedCtx()
		valid, verr := p.footprintValid(ctx, fp)
		require.NoError(t, verr)
		require.True(t, valid, "an untouched hub must validate")

		reads := rec.of(hubID)
		require.Len(t, reads, 1, "one scoped re-read covers every selector on the node")
		require.Equal(t, map[string]struct{}{"holdsRole": {}}, reads[0].Relations,
			"the validator re-reads at the relation the footprint names, not whole")
		require.True(t, reads[0].Marked)
		return p, coreKV, hubID, held, fp
	}

	for _, mode := range []full.HubReadScopeMode{full.HubReadScopeModeOn, full.HubReadScopeModeOff} {
		t.Run("scope "+mode.String(), func(t *testing.T) {
			tag := "n" + mode.String()[:1]

			t.Run("unrelated relation is not drift", func(t *testing.T) {
				p, coreKV, hubID, _, fp := setup(t, tag+"1", mode)
				seedHubLink(t, coreKV, "identity", hubID, "worksAt", "org", hubNanoID(t, "og1"))

				valid, verr := p.footprintValid(context.Background(), fp)
				require.NoError(t, verr)
				require.True(t, valid,
					"a write to a relation the footprint never named must not read as drift")
			})

			t.Run("same relation added is drift", func(t *testing.T) {
				p, coreKV, hubID, _, fp := setup(t, tag+"2", mode)
				seedHubLink(t, coreKV, "identity", hubID, "holdsRole", "role", hubNanoID(t, "rz2"))

				valid, verr := p.footprintValid(context.Background(), fp)
				require.NoError(t, verr)
				require.False(t, valid, "a second holdsRole link changes the selector's matched set")
			})

			t.Run("same relation retracted is drift", func(t *testing.T) {
				p, coreKV, _, held, fp := setup(t, tag+"3", mode)
				seedSoftDeletedHubLink(t, coreKV, held)

				valid, verr := p.footprintValid(context.Background(), fp)
				require.NoError(t, verr)
				require.False(t, valid, "a soft-tombstoned holdsRole link leaves the matched set")
			})

			t.Run("same relation swapped is drift", func(t *testing.T) {
				p, coreKV, hubID, held, fp := setup(t, tag+"4", mode)
				// A revoke-and-grant landing together: the same COUNT of
				// holdsRole links, different identities.
				seedSoftDeletedHubLink(t, coreKV, held)
				seedHubLink(t, coreKV, "identity", hubID, "holdsRole", "role", hubNanoID(t, "rz4"))

				valid, verr := p.footprintValid(context.Background(), fp)
				require.NoError(t, verr)
				require.False(t, valid, "a same-count swap of holdsRole links is still drift")
			})
		})
	}
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
	obsCtx, rec := observedCtx()
	valid, verr = p.footprintValid(obsCtx, fp)
	require.NoError(t, verr)
	require.False(t, valid,
		"a Matched set observed before the untyped hop's whole read must be re-derived, or the write between the two hops goes undetected")

	// The coarse path re-reads WHOLE — the fingerprint it compares is a whole
	// read's, so the re-read has to be one too, and the Matched sets are then
	// re-derived from the same edges rather than from a second read.
	reads := rec.of(hubID)
	require.Len(t, reads, 1, "the coarse path takes one read, not one per selector")
	require.Nil(t, reads[0].Relations, "the coarse path re-reads the node whole")
	require.True(t, reads[0].Whole)
}

// TestFootprintValid_CoarsePath_NoFingerprintIsMalformed pins the fail-closed
// direction of the coarse path: an untyped hop always reads its node whole, so
// a Fallback entry with no EdgeRevisions fingerprint cannot have come from an
// evaluation. There is nothing to compare, and the validator reports drift
// rather than validating a node it never checked.
//
// The node is UNMARKED and has no adjacency document, which is what makes the
// test able to fail. Its fresh fingerprint is 0, and a missing EdgeRevisions
// entry also reads as 0 — so without the guard the fingerprint comparison
// succeeds (0 == 0) and the footprint validates. On a marked node the fresh
// fingerprint is a non-zero hash and the comparison would report drift on its
// own, whether or not the guard existed.
func TestFootprintValid_CoarsePath_NoFingerprintIsMalformed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	coreKV, adjKV, _ := newCollisionKVs(t)
	p := &Pipeline{coreKV: coreKV, adjKV: adjKV}
	nodeID := hubNanoID(t, "cp2")

	_, freshRev, err := adjacency.Neighbors(ctx, adjKV, coreKV, nodeID)
	require.NoError(t, err)
	require.Zero(t, freshRev,
		"the fixture only bites while this node reads back at fingerprint 0 — the value an absent EdgeRevisions entry also has")

	fp := ruleengine.EvalFootprint{
		EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
			nodeID: {Fallback: true},
		},
	}
	valid, verr := p.footprintValid(ctx, fp)
	require.NoError(t, verr)
	require.False(t, valid, "a coarse node with no fingerprint is malformed and must fail closed")
}
