package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// TestExecuteFullForActor_MultiBranch_UnionsAnchorsAcrossWalks is the §12
// repro (refractor-shared-keyspace-arbitration-design.md §13.6's acceptance
// test): one actor, walk 0 reaching 2 real anchors, walk 1 reaching 1 real
// anchor on a DIFFERENT anchor. Before this fire's composition primitive, a
// single coalesced query silently truncated the anchor set to whichever
// branch happened first (§12's defect); this asserts the UNION of all three
// anchors projects.
func TestExecuteFullForActor_MultiBranch_UnionsAnchorsAcrossWalks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JdentityBrMerge1"
	const svcID = "Tcc3JserviceBrMerge1"
	const roleID = "Tcc3JstaffBrMerge111"
	const opAID = "Tcc3JopABrMerge11111"
	const opBID = "Tcc3JopBBrMerge11111"
	const opCID = "Tcc3JopCBrMerge11111"
	identityKey := "vtx.identity." + identityID

	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})
	writeCollisionVertex(t, coreKV, "vtx.service."+svcID, "service", map[string]any{})
	writeCollisionVertex(t, coreKV, "vtx.role."+roleID, "role", map[string]any{})
	writeCollisionVertex(t, coreKV, "vtx.op."+opAID, "op", map[string]any{"title": "A"})
	writeCollisionVertex(t, coreKV, "vtx.op."+opBID, "op", map[string]any{"title": "B"})
	writeCollisionVertex(t, coreKV, "vtx.op."+opCID, "op", map[string]any{"title": "C"})

	buildCollisionEdge(t, adjKV, "hasService", "identity", identityID, "service", svcID)
	buildCollisionEdge(t, adjKV, "for", "service", svcID, "op", opAID)
	buildCollisionEdge(t, adjKV, "for", "service", svcID, "op", opBID)
	buildCollisionEdge(t, adjKV, "hasRole", "identity", identityID, "role", roleID)
	buildCollisionEdge(t, adjKV, "for", "role", roleID, "op", opCID)

	eng := full.New()
	branch0Cr, err := eng.Parse(
		`MATCH (identity:identity {key: $actorKey})-[:hasService]->(svc:service) ` +
			`OPTIONAL MATCH (svc)-[:for]->(o:op) RETURN o.key AS anchor, o.data.title AS title`)
	require.NoError(t, err)
	branch1Cr, err := eng.Parse(
		`MATCH (identity:identity {key: $actorKey})-[:hasRole]->(role:role) ` +
			`OPTIONAL MATCH (role)-[:for]->(o:op) RETURN o.key AS anchor, o.data.title AS title`)
	require.NoError(t, err)

	p := &Pipeline{
		ruleID:         "rule-branch-union",
		coreKV:         coreKV,
		adjKV:          adjKV,
		engineKind:     ruleengine.EngineFull,
		fullEngine:     eng,
		fullCR:         branch0Cr,
		fullCRBranches: []ruleengine.CompiledRule{branch0Cr, branch1Cr},
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(ctx, identityKey, nodeProps)
	require.NoError(t, err)

	gotAnchors := map[string]string{}
	for _, r := range results {
		require.False(t, r.Delete)
		anchor, _ := r.Keys["anchor"].(string)
		title, _ := r.Row["title"].(string)
		gotAnchors[anchor] = title
	}
	require.Len(t, gotAnchors, 3, "the merged result set must carry all 3 anchors, not just branch 0's 2: %#v", gotAnchors)
	require.Equal(t, "A", gotAnchors["vtx.op."+opAID])
	require.Equal(t, "B", gotAnchors["vtx.op."+opBID])
	require.Equal(t, "C", gotAnchors["vtx.op."+opCID], "walk 1's anchor must survive the merge, not be dropped as branch 0's subset would")
}

// TestExecuteFullForActor_MultiBranch_SingleBranchIsUnaffected pins that a
// Personal lens with only ONE branch (fullCRBranches len<=1) evaluates
// exactly as it did before branch merging existed — no merge machinery
// engages.
func TestExecuteFullForActor_MultiBranch_SingleBranchIsUnaffected(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JdentityBrX9Y8Z7"
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})

	eng, cr := singleRowEngine(t)
	p := &Pipeline{
		ruleID:     "rule-branch-single",
		coreKV:     coreKV,
		adjKV:      adjKV,
		engineKind: ruleengine.EngineFull,
		fullEngine: eng,
		fullCR:     cr,
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(ctx, identityKey, nodeProps)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, identityKey, results[0].Row["actorKey"])
}

// TestExecuteFullForActor_MultiBranch_AnchorDerivedDisagreementFailsClosed
// pins §13.3's runtime backstop: if two branches' anchor-derived columns
// somehow computed different values for the SAME shared key, the merge must
// fail loudly rather than pick a winner (the classifier refuses this shape
// at compile time — internal/refractor/lens's translateSpec — so this test
// exercises the merge's own defense in depth directly).
func TestExecuteFullForActor_MultiBranch_AnchorDerivedDisagreementFailsClosed(t *testing.T) {
	out0 := []ruleengine.ProjectionResult{
		{Key: map[string]any{"anchor": "op1"}, Values: map[string]any{"anchor": "op1", "title": "left"}},
	}
	out1 := []ruleengine.ProjectionResult{
		{Key: map[string]any{"anchor": "op1"}, Values: map[string]any{"anchor": "op1", "title": "right"}},
	}
	_, err := mergeBranchRows([][]ruleengine.ProjectionResult{out0, out1})
	require.Error(t, err)
	require.ErrorContains(t, err, `column "title" disagrees across walks`)
}
