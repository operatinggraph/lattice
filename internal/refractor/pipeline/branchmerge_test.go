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
// anchor on a DIFFERENT anchor. Without the composition primitive here, a
// single coalesced query silently truncates the anchor set to whichever
// branch happens first (§12's defect); this asserts the UNION of all three
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
	results, err := p.executeFullForActor(ctx, p.ruleState(), identityKey, nodeProps, "")
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
	results, err := p.executeFullForActor(ctx, p.ruleState(), identityKey, nodeProps, "")
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, identityKey, results[0].Row["actorKey"])
}

// TestUseFullEngineBranches_DowngradeToSingleBranchClearsMultiWalkState pins
// that a reload (cmd/refractor/reload.go calls UseFullEngineBranches on an
// EXISTING Pipeline) which edits a lens from 2+ Walks down to 1 clears
// fullCRBranches and fullCRWalkOwnedColumns — not just skips re-setting them.
// The len==1 path must clear both explicitly: without it, a downgraded lens
// keeps evaluating (and merging) the removed Walk forever.
func TestUseFullEngineBranches_DowngradeToSingleBranchClearsMultiWalkState(t *testing.T) {
	eng := full.New()
	branch0Cr, err := eng.Parse(`MATCH (identity:identity {key: $actorKey})-[:hasService]->(svc:service) ` +
		`OPTIONAL MATCH (svc)-[:for]->(op:op) RETURN op.key AS anchor, role.key AS viaRole`)
	require.NoError(t, err)
	branch1Cr, err := eng.Parse(`MATCH (identity:identity {key: $actorKey})-[:holdsRole]->(role:role) ` +
		`MATCH (role)<-[:grantedBy]-(perm:permission)-[:forOperation]->(op:op) ` +
		`RETURN op.key AS anchor, role.key AS viaRole`)
	require.NoError(t, err)

	p := &Pipeline{}
	p.UseFullEngineBranches(eng, branch0Cr, []ruleengine.CompiledRule{branch0Cr, branch1Cr})
	require.Len(t, p.fullCRBranches, 2)
	require.Equal(t, map[string]int{"viaRole": 1}, p.fullCRWalkOwnedColumns)

	singleCr, err := eng.Parse(`MATCH (identity:identity {key: $actorKey}) RETURN identity.key AS anchor`)
	require.NoError(t, err)
	p.UseFullEngineBranches(eng, singleCr, nil)
	require.Nil(t, p.fullCRBranches, "a downgrade to 1 Walk must clear the stale multi-branch slice")
	require.Nil(t, p.fullCRWalkOwnedColumns, "a downgrade to 1 Walk must clear the stale walk-owned classification")
}

// TestExecuteFullForActor_MultiBranch_WalkOwnedFanOutResolvesDeterministically
// is the edgeCatalog live repro (2026-07-29T05:51:52, filed to lattice.md):
// a multi-hat actor holding 2 roles that each `grantedBy` a DIFFERENT
// permission for the SAME op makes that role-walk's own cypher execution
// yield 2 rows for one op anchor with 2 different `viaRole` values. viaRole
// is ColumnWalkOwned (only the role-walk branch ever binds `role`), so the
// merge must resolve it deterministically instead of treating it as a
// cross-walk disagreement and refusing the whole row. Also pins the behavior
// for the two other shapes a real multi-walk lens produces: a walk-owned
// column merges fine across branches when only one role reaches a
// dual-reachable op (existing nil-vs-non-nil case), and an anchor-only
// column (title) still requires agreement.
func TestExecuteFullForActor_MultiBranch_WalkOwnedFanOutResolvesDeterministically(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JidentHatMergeXY"
	const svcID = "Tcc3JsvcHatMergeXYZA"
	const role1ID = "Tcc3JhatAforMergeXYZ"
	const role2ID = "Tcc3JhatBforMergeXYZ"
	const perm1ID = "Tcc3JpermAforMrgXYZA" // role1 -> opMany
	const perm2ID = "Tcc3JpermBforMrgXYZA" // role2 -> opMany (same op, different role)
	const perm3ID = "Tcc3JpermCforMrgXYZA" // role1 -> opShared
	const opManyID = "Tcc3JopManyMergeXYZA"
	const opSharedID = "Tcc3JopSharedMrgXYZA"
	const opBaseOnlyID = "Tcc3JopBaseJustMrgXY"
	identityKey := "vtx.identity." + identityID

	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})
	writeCollisionVertex(t, coreKV, "vtx.service."+svcID, "service", map[string]any{})
	// role1's label deliberately sorts LAST and role2's FIRST — a merge that
	// picked viaRole and viaRoleName independently (rather than as a unit
	// from one winning row) would tear role1's (winning) key away from its
	// own label and pair it with role2's, so this ordering is bait: only a
	// row-level pick lands on role1's key WITH role1's own label.
	writeCollisionVertex(t, coreKV, "vtx.role."+role1ID, "role", map[string]any{"label": "Zzzlast"})
	writeCollisionVertex(t, coreKV, "vtx.role."+role2ID, "role", map[string]any{"label": "Aaafirst"})
	writeCollisionVertex(t, coreKV, "vtx.permission."+perm1ID, "permission", map[string]any{})
	writeCollisionVertex(t, coreKV, "vtx.permission."+perm2ID, "permission", map[string]any{})
	writeCollisionVertex(t, coreKV, "vtx.permission."+perm3ID, "permission", map[string]any{})
	writeCollisionVertex(t, coreKV, "vtx.op."+opManyID, "op", map[string]any{"title": "Many"})
	writeCollisionVertex(t, coreKV, "vtx.op."+opSharedID, "op", map[string]any{"title": "Shared"})
	writeCollisionVertex(t, coreKV, "vtx.op."+opBaseOnlyID, "op", map[string]any{"title": "Base"})

	buildCollisionEdge(t, adjKV, "hasService", "identity", identityID, "service", svcID)
	buildCollisionEdge(t, adjKV, "for", "service", svcID, "op", opSharedID)
	buildCollisionEdge(t, adjKV, "for", "service", svcID, "op", opBaseOnlyID)
	buildCollisionEdge(t, adjKV, "holdsRole", "identity", identityID, "role", role1ID)
	buildCollisionEdge(t, adjKV, "holdsRole", "identity", identityID, "role", role2ID)
	buildCollisionEdge(t, adjKV, "grantedBy", "permission", perm1ID, "role", role1ID)
	buildCollisionEdge(t, adjKV, "grantedBy", "permission", perm2ID, "role", role2ID)
	buildCollisionEdge(t, adjKV, "grantedBy", "permission", perm3ID, "role", role1ID)
	buildCollisionEdge(t, adjKV, "forOperation", "permission", perm1ID, "op", opManyID)
	buildCollisionEdge(t, adjKV, "forOperation", "permission", perm2ID, "op", opManyID)
	buildCollisionEdge(t, adjKV, "forOperation", "permission", perm3ID, "op", opSharedID)

	eng := full.New()
	// viaService (svc.key) is walk-owned by branch0 alone — a DIFFERENT
	// owner than viaRole/viaRoleName's branch1 — so opShared (reachable via
	// BOTH walks) exercises the cross-owner case: each owner's columns must
	// resolve independently, never coupled into one shared winning row.
	branch0Cr, err := eng.Parse(
		`MATCH (identity:identity {key: $actorKey})-[:hasService]->(svc:service) ` +
			`OPTIONAL MATCH (svc)-[:for]->(op:op) ` +
			`RETURN op.key AS anchor, op.data.title AS title, svc.key AS viaService, ` +
			`role.key AS viaRole, role.data.label AS viaRoleName`)
	require.NoError(t, err)
	branch1Cr, err := eng.Parse(
		`MATCH (identity:identity {key: $actorKey})-[:holdsRole]->(role:role) ` +
			`MATCH (role)<-[:grantedBy]-(perm:permission)-[:forOperation]->(op:op) ` +
			`RETURN op.key AS anchor, op.data.title AS title, svc.key AS viaService, ` +
			`role.key AS viaRole, role.data.label AS viaRoleName`)
	require.NoError(t, err)

	p := &Pipeline{
		ruleID: "rule-branch-walk-owned-fanout",
		coreKV: coreKV,
		adjKV:  adjKV,
	}
	p.UseFullEngineBranches(eng, branch0Cr, []ruleengine.CompiledRule{branch0Cr, branch1Cr})
	require.Equal(t, map[string]int{"viaService": 0, "viaRole": 1, "viaRoleName": 1}, p.fullCRWalkOwnedColumns,
		"viaService is owned by branch0 (svc), viaRole/viaRoleName by branch1 (role) — 2 distinct owners")

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(ctx, p.ruleState(), identityKey, nodeProps, "")
	require.NoError(t, err, "a walk-owned column's own multi-role fan-out must resolve, not fail the merge")

	byAnchor := map[string]map[string]any{}
	for _, r := range results {
		anchor, _ := r.Keys["anchor"].(string)
		byAnchor[anchor] = r.Row
	}
	require.Len(t, byAnchor, 3)

	many := byAnchor["vtx.op."+opManyID]
	require.Equal(t, "Many", many["title"])
	require.Equal(t, "vtx.role."+role1ID, many["viaRole"],
		"2 roles reaching the same op must resolve to a stable pick, not error or flap")
	require.Equal(t, "Zzzlast", many["viaRoleName"],
		"viaRoleName must come from the SAME winning row as viaRole (role1), never torn from role2's — "+
			"role2's 'Aaafirst' would win if the two columns were picked independently")
	require.Nil(t, many["viaService"], "opMany is reached only through the role walk — no service")

	shared := byAnchor["vtx.op."+opSharedID]
	require.Equal(t, "Shared", shared["title"])
	require.Equal(t, "vtx.role."+role1ID, shared["viaRole"],
		"a dual-reachable op (base walk nil, role walk's one role) must still merge to the role walk's value")
	require.Equal(t, "Zzzlast", shared["viaRoleName"])
	require.Equal(t, "vtx.service."+svcID, shared["viaService"],
		"viaService (branch0-owned) must survive alongside viaRole/viaRoleName (branch1-owned) — "+
			"resolving one owner's columns must never null out a DIFFERENT owner's real value")

	base := byAnchor["vtx.op."+opBaseOnlyID]
	require.Equal(t, "Base", base["title"])
	require.Nil(t, base["viaRole"], "an op reached only through the base walk carries no role")
	require.Nil(t, base["viaRoleName"])
	require.Equal(t, "vtx.service."+svcID, base["viaService"],
		"an op reached only through the base walk still carries its own owned column")
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
	_, err := mergeBranchRows([][]ruleengine.ProjectionResult{out0, out1}, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, `column "title" disagrees across walks`)
}
