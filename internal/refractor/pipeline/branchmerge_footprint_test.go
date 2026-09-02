package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// selectorFP is one node's selector record, spelled compactly so a test reads
// as the disagreement it is about.
func selectorFP(fallback bool, sel ruleengine.EdgeSelector, ids ...string) ruleengine.EdgeSelectorFootprint {
	matched := map[string]struct{}{}
	for _, id := range ids {
		matched[id] = struct{}{}
	}
	return ruleengine.EdgeSelectorFootprint{
		Fallback: fallback,
		Matched:  map[ruleengine.EdgeSelector]map[string]struct{}{sel: matched},
	}
}

var holdsRoleOut = ruleengine.EdgeSelector{RelType: "holdsRole", Direction: "out"}

// TestMergeFootprints_NodeRevisionDisagreement_IsTorn pins §9.2's first
// conflict: a multi-walk lens's branches run one after another with separate
// memos, so two branches reading one Core KV key at DIFFERENT revisions have
// already watched a write land between them. The merged map can hold only one
// of those revisions, and re-reading it later would find that survivor
// unchanged — so the disagreement has to be recorded at the merge or it is
// gone.
func TestMergeFootprints_NodeRevisionDisagreement_IsTorn(t *testing.T) {
	const key = "vtx.identity.TmfJdentityAaaaaaaaa"
	merged := mergeFootprints([]ruleengine.EvalFootprint{
		{NodeRevisions: map[string]uint64{key: 7}},
		{NodeRevisions: map[string]uint64{key: 9}},
	})
	require.True(t, merged.Torn, "two branches read %q at revisions 7 and 9", key)
	require.Equal(t, uint64(9), merged.NodeRevisions[key],
		"the merged surface stays last-wins and complete; Torn is what says it cannot be certified")
}

// TestMergeFootprints_EdgeRevisionDisagreement_IsTorn is the adjacency twin:
// one node, two whole-read fingerprints.
func TestMergeFootprints_EdgeRevisionDisagreement_IsTorn(t *testing.T) {
	const nodeID = "TmfJdentityBbbbbbbbb"
	merged := mergeFootprints([]ruleengine.EvalFootprint{
		{EdgeRevisions: map[string]uint64{nodeID: 11}},
		{EdgeRevisions: map[string]uint64{nodeID: 12}},
	})
	require.True(t, merged.Torn)
	require.Equal(t, uint64(12), merged.EdgeRevisions[nodeID])
}

// TestMergeFootprints_MatchedSetDisagreement_IsTorn pins the third conflict,
// and the one the union actively hides: two branches re-deriving one (node,
// selector) to different edge identities. The merge unions them, so the
// merged set is a set NEITHER branch saw — and a later re-read of a graph
// that has since settled on that union would compare equal and pass.
//
// Both shapes count: a swap at equal cardinality (which a size comparison
// alone would miss) and a strict superset.
func TestMergeFootprints_MatchedSetDisagreement_IsTorn(t *testing.T) {
	const nodeID = "TmfJdentityCcccccccc"

	t.Run("same size, different member", func(t *testing.T) {
		merged := mergeFootprints([]ruleengine.EvalFootprint{
			{EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
				nodeID: selectorFP(false, holdsRoleOut, "lnk.a"),
			}},
			{EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
				nodeID: selectorFP(false, holdsRoleOut, "lnk.b"),
			}},
		})
		require.True(t, merged.Torn, "a revoke-and-grant between the two branches keeps the count and moves the identity")
		require.Len(t, merged.EdgeSelectors[nodeID].Matched[holdsRoleOut], 2,
			"the union is still built — Torn is the only thing that says it is not what either branch read")
	})

	t.Run("different size", func(t *testing.T) {
		merged := mergeFootprints([]ruleengine.EvalFootprint{
			{EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
				nodeID: selectorFP(false, holdsRoleOut, "lnk.a"),
			}},
			{EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
				nodeID: selectorFP(false, holdsRoleOut, "lnk.a", "lnk.b"),
			}},
		})
		require.True(t, merged.Torn)
	})
}

// TestMergeFootprints_NoDisagreement_IsNotTorn pins the negative side, which
// is what keeps the flag from being a blanket refusal of every multi-walk
// lens: branches that agree, branches that touched disjoint keys, and — the
// case that looks like a conflict and is not — one node read WHOLE by a
// branch whose untyped hop set Fallback and read only at a relation scope by
// another. Those are two different units, each validated on its own terms,
// not two answers to one question.
//
// It also pins that the merged surface is exactly what the union has always
// produced, so nothing but the flag changed.
func TestMergeFootprints_NoDisagreement_IsNotTorn(t *testing.T) {
	sharedKey := "vtx.identity." + hubNanoID(t, "mfd")
	branch0Key := "vtx.role." + hubNanoID(t, "mfra")
	branch1Key := "vtx.role." + hubNanoID(t, "mfrb")
	agreeNode := hubNanoID(t, "mfe")
	splitNode := hubNanoID(t, "mff")

	merged := mergeFootprints([]ruleengine.EvalFootprint{
		{
			NodeRevisions: map[string]uint64{sharedKey: 4, branch0Key: 1},
			EdgeRevisions: map[string]uint64{agreeNode: 21},
			EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
				agreeNode: selectorFP(false, holdsRoleOut, "lnk.a"),
				// A typed hop read this node at a relation scope, so it has
				// no fingerprint of its own.
				splitNode: selectorFP(false, holdsRoleOut, "lnk.c"),
			},
		},
		{
			NodeRevisions: map[string]uint64{sharedKey: 4, branch1Key: 2},
			EdgeRevisions: map[string]uint64{agreeNode: 21, splitNode: 33},
			EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
				agreeNode: selectorFP(false, holdsRoleOut, "lnk.a"),
				// The other branch crossed splitNode with an untyped hop:
				// whole read, Fallback, and no Matched set of its own.
				splitNode: {Fallback: true},
			},
		},
	})

	require.False(t, merged.Torn,
		"agreement, disjoint keys, and a scoped-only/whole+Fallback pair on one node are none of them disagreements")

	require.Equal(t, map[string]uint64{sharedKey: 4, branch0Key: 1, branch1Key: 2}, merged.NodeRevisions)
	require.Equal(t, map[string]uint64{agreeNode: 21, splitNode: 33}, merged.EdgeRevisions)
	require.Equal(t, map[string]ruleengine.EdgeSelectorFootprint{
		agreeNode: {Matched: map[ruleengine.EdgeSelector]map[string]struct{}{
			holdsRoleOut: {"lnk.a": {}},
		}},
		splitNode: {Fallback: true, Matched: map[ruleengine.EdgeSelector]map[string]struct{}{
			holdsRoleOut: {"lnk.c": {}},
		}},
	}, merged.EdgeSelectors, "the merged surface is exactly the union it has always been")
}

// TestMergeFootprints_TornInputCarriesThrough pins that a merge cannot
// launder a torn input: a branch footprint already carrying the flag keeps it
// even when every key it holds agrees with its siblings.
func TestMergeFootprints_TornInputCarriesThrough(t *testing.T) {
	const key = "vtx.identity.TmfJdentityGggggggg1"
	merged := mergeFootprints([]ruleengine.EvalFootprint{
		{NodeRevisions: map[string]uint64{key: 3}, Torn: true},
		{NodeRevisions: map[string]uint64{key: 3}},
	})
	require.True(t, merged.Torn, "a torn input stays torn however well its keys agree")
}

// TestMergeFootprints_SingleBranch_IsNeverTorn pins the floor: one memo
// cannot disagree with itself, so the merge of one footprint is that
// footprint plus a false flag. (executeBranches does not even call the merge
// for a single-branch lens, but the merge must not invent a conflict if it
// ever is.)
func TestMergeFootprints_SingleBranch_IsNeverTorn(t *testing.T) {
	nodeID := hubNanoID(t, "mfh")
	merged := mergeFootprints([]ruleengine.EvalFootprint{{
		NodeRevisions: map[string]uint64{"vtx.role." + hubNanoID(t, "mfrc"): 5},
		EdgeRevisions: map[string]uint64{nodeID: 6},
		EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
			nodeID: selectorFP(false, holdsRoleOut, "lnk.a", "lnk.b"),
		},
	}})
	require.False(t, merged.Torn)
}

// TestFootprintValid_TornFootprint_RejectsWithoutReading pins the validator's
// side of §9.2. The proof that NO read happened is structural: the pipeline
// carries nil Core and Adjacency KV handles, so every read path in
// footprintValid would panic on a nil dereference. The footprint names keys
// and nodes that do not exist in any bucket, which under a real handle would
// also invalidate it — the nil handles are what make "returned false without
// reading" the only explanation for a clean false.
func TestFootprintValid_TornFootprint_RejectsWithoutReading(t *testing.T) {
	p := &Pipeline{coreKV: nil, adjKV: nil}

	fp := ruleengine.EvalFootprint{
		Torn:          true,
		NodeRevisions: map[string]uint64{"vtx.identity.TmfJdentityJjjjjjjjj": 1},
		EdgeRevisions: map[string]uint64{"TmfJdentityKkkkkkkkk": 2},
		EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
			"TmfJdentityMmmmmmmmm": selectorFP(false, holdsRoleOut, "lnk.a"),
		},
	}

	valid, err := p.footprintValid(context.Background(), fp)
	require.NoError(t, err)
	require.False(t, valid, "a torn footprint is rejected outright")
}

// TestFootprintValid_UntornFootprintOverNilKVs_WouldRead is the control that
// makes the test above mean something: the SAME footprint with Torn cleared
// does reach a read, and the nil handles turn that read into a panic. Without
// this, a false from the torn case could equally have come from a validator
// that never reads anything at all.
func TestFootprintValid_UntornFootprintOverNilKVs_WouldRead(t *testing.T) {
	p := &Pipeline{coreKV: nil, adjKV: nil}

	fp := ruleengine.EvalFootprint{
		NodeRevisions: map[string]uint64{"vtx.identity.TmfJdentityJjjjjjjjj": 1},
	}

	require.Panics(t, func() {
		_, _ = p.footprintValid(context.Background(), fp)
	}, "an untorn footprint must reach a KV read — otherwise the torn case proves nothing")
}

// TestFootprintValid_SelectorPathWithNoSelectors_IsMalformed pins the selector
// path's own fail-closed guard, the twin of the coarse path's missing-
// fingerprint one. An entry that is not Fallback and names no selector has
// nothing to compare: the whole fingerprint is deliberately not compared on
// this path, and a scoped re-read of an empty relation set reads nothing at
// all — so validating would confirm a node the pass never looked at.
//
// recordEdgeSelector never produces the shape, but mergeFootprints mints an
// empty Matched map for every node it folds, so it is one nil-Matched branch
// input away.
//
// Nil Core and Adjacency KV handles prove no read happened: any read path
// would nil-dereference, so a clean false can only mean the guard returned
// first (TestFootprintValid_UntornFootprintOverNilKVs_WouldRead is the control
// that a footprint reaching a read does panic).
func TestFootprintValid_SelectorPathWithNoSelectors_IsMalformed(t *testing.T) {
	p := &Pipeline{coreKV: nil, adjKV: nil}
	nodeID := hubNanoID(t, "mfz")

	for _, tc := range []struct {
		name    string
		matched map[ruleengine.EdgeSelector]map[string]struct{}
	}{
		{"empty Matched, the shape mergeFootprints mints", map[ruleengine.EdgeSelector]map[string]struct{}{}},
		{"nil Matched", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := ruleengine.EvalFootprint{
				EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{
					nodeID: {Fallback: false, Matched: tc.matched},
				},
			}
			valid, err := p.footprintValid(context.Background(), fp)
			require.NoError(t, err)
			require.False(t, valid, "a selector entry naming no selector is malformed and must fail closed")
		})
	}
}
