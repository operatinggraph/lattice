package loftspacedomain

// Rule-engine proof of the landlordWorkOrdersRead cypher
// (docs/reviews/loftspace-maintenance-loop-2026-09-17.md decision 5). Same harness
// landlord_units_lens_test.go / lens_cypher_test.go use.
//
//   - TestLandlordWorkOrdersRead_ProjectsOrderAtManagedUnit: a work order
//     locatedAt a unit the landlord manages projects one row, summary/priority
//     from .report, open_task_count 0 (no task), resolved_at null.
//   - TestLandlordWorkOrdersRead_OpenTaskCountsOne: a task scopedTo the order
//     with status 'open' counts once.
//   - TestLandlordWorkOrdersRead_CancelledTaskCountsZero: only a cancelled task
//     scoped to it reads open_task_count 0.
//   - TestLandlordWorkOrdersRead_ResolvedAtProjectsFromResolutionAspect: a
//     .resolution aspect projects resolved_at + resolution_notes.
//   - TestLandlordWorkOrdersRead_ExcludesUnmanagedUnit: a work order at a unit
//     with no manages link projects nothing.
//   - TestLandlordWorkOrdersRead_FansOutPerCoLandlord: a unit managed by two
//     landlords fans one order out to two rows.
//   - TestLandlordWorkOrdersRead_ExcludesBuildingLocatedOrder: a work order
//     locatedAt a building (staff-reported) has no manages walk to anchor on
//     and projects nothing.
//   - TestLandlordWorkOrdersRead_ReportedByResidentTrue: a reportedBy identity
//     who also residesIn the order's own unit reads reported_by_resident true.
//   - TestLandlordWorkOrdersRead_ReportedByStaffFalse: a reportedBy identity
//     with no residesIn link at all (staff) reads reported_by_resident false.
//   - TestLandlordWorkOrdersRead_NoReporterLinkFalse: an order with no
//     reportedBy link (the pre-backfill legacy shape) reads reported_by_resident
//     false, never null.
//   - TestLandlordWorkOrdersRead_ReporterResidesInDifferentUnitFalse: a
//     reportedBy identity who residesIn a UNIT OTHER than the order's own
//     locatedAt unit reads reported_by_resident false — the constrained-target
//     walk closes back on THIS order's unit, never any unit the reporter
//     happens to live in.
//   - TestLandlordWorkOrdersRead_OpenTaskAndResidentReporterCoexist: an order
//     carrying BOTH an open task and a resident reportedBy link still
//     projects exactly one row (no branch-decomposition duplication across
//     the two independent OPTIONAL MATCH fans), with open_task_count and
//     reported_by_resident both correct on it.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// taskVtx mints a task vertex carrying {status}, the shape orchestration-base's
// task DDL writes (vtx.task.<id>, root data {status, expiresAt}). Distinct from
// luFixture's own vtx() (always data: {}), reusing its types map.
func (f *luFixture) taskVtx(t *testing.T, name, status string) string {
	t.Helper()
	id := lenstest.NanoID(name)
	f.ids[name] = id
	f.types[id] = "task"
	key := "vtx.task." + id
	body := map[string]any{"key": key, "class": "task", "isDeleted": false, "data": map[string]any{"status": status}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

// link mints a generic named directed link between two already-minted
// vertices, mirroring manages()/containedIn()'s own two-adjacency-record shape
// (one outbound record off fromName, one inbound off toName).
func (f *luFixture) link(t *testing.T, name, fromName, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[fromName], f.ids[toName]
	fromType, toType := f.types[fromID], f.types[toID]
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType}))
}

func (f *luFixture) projectWorkOrders(t *testing.T) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(landlordWorkOrdersReadSpec)
	require.NoError(t, err, "landlordWorkOrdersRead cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

func TestLandlordWorkOrdersRead_ProjectsOrderAtManagedUnit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	f.vtx(t, "u1", "unit")
	f.manages(t, "larry", "u1")
	woKey := f.vtx(t, "wo1", "workorder")
	f.link(t, "locatedAt", "wo1", "u1")
	f.unitAspect(t, "wo1", "report", "workOrderReport", map[string]any{
		"summary": "Kitchen tap is dripping", "priority": "normal",
		"reportedAt": "2026-07-29T09:00:00Z", "reportedBy": "vtx.identity." + f.ids["larry"],
	})

	rows := f.projectWorkOrders(t)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, woKey, v["work_order_key"])
	require.Equal(t, "Kitchen tap is dripping", v["summary"])
	require.Equal(t, "normal", v["priority"])
	require.EqualValues(t, 0, v["open_task_count"])
	require.Nil(t, v["resolved_at"])
	require.Nil(t, v["resolution_notes"])
	anchors, ok := v["authz_anchors"].([]any)
	require.True(t, ok, "authz_anchors must be a list, got %T", v["authz_anchors"])
	require.Equal(t, []any{f.ids["larry"]}, anchors)
}

func TestLandlordWorkOrdersRead_OpenTaskCountsOne(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	f.vtx(t, "u1", "unit")
	f.manages(t, "larry", "u1")
	f.vtx(t, "wo1", "workorder")
	f.link(t, "locatedAt", "wo1", "u1")
	f.unitAspect(t, "wo1", "report", "workOrderReport", map[string]any{"summary": "leak", "priority": "urgent"})
	f.taskVtx(t, "t1", "open")
	f.link(t, "scopedTo", "t1", "wo1")

	rows := f.projectWorkOrders(t)
	require.Len(t, rows, 1)
	require.EqualValues(t, 1, rows[0].Values["open_task_count"])
}

func TestLandlordWorkOrdersRead_CancelledTaskCountsZero(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	f.vtx(t, "u1", "unit")
	f.manages(t, "larry", "u1")
	f.vtx(t, "wo1", "workorder")
	f.link(t, "locatedAt", "wo1", "u1")
	f.unitAspect(t, "wo1", "report", "workOrderReport", map[string]any{"summary": "leak", "priority": "urgent"})
	f.taskVtx(t, "t1", "cancelled")
	f.link(t, "scopedTo", "t1", "wo1")

	rows := f.projectWorkOrders(t)
	require.Len(t, rows, 1, "an order with only a cancelled task still projects")
	require.EqualValues(t, 0, rows[0].Values["open_task_count"], "a cancelled task must not count as open")
}

func TestLandlordWorkOrdersRead_ResolvedAtProjectsFromResolutionAspect(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	f.vtx(t, "u1", "unit")
	f.manages(t, "larry", "u1")
	f.vtx(t, "wo1", "workorder")
	f.link(t, "locatedAt", "wo1", "u1")
	f.unitAspect(t, "wo1", "report", "workOrderReport", map[string]any{"summary": "leak", "priority": "urgent"})
	f.unitAspect(t, "wo1", "resolution", "workOrderResolution", map[string]any{
		"notes": "Replaced the washer.", "resolvedAt": "2026-07-30T11:00:00Z", "resolvedBy": "vtx.identity." + f.ids["larry"],
	})

	rows := f.projectWorkOrders(t)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "2026-07-30T11:00:00Z", v["resolved_at"])
	require.Equal(t, "Replaced the washer.", v["resolution_notes"])
}

func TestLandlordWorkOrdersRead_ExcludesUnmanagedUnit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "u1", "unit")
	f.vtx(t, "wo1", "workorder")
	f.link(t, "locatedAt", "wo1", "u1")
	f.unitAspect(t, "wo1", "report", "workOrderReport", map[string]any{"summary": "leak", "priority": "urgent"})
	// No manages link written.

	rows := f.projectWorkOrders(t)
	require.Empty(t, rows, "a unit with no manages link has no landlord to anchor the row on")
}

func TestLandlordWorkOrdersRead_FansOutPerCoLandlord(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	f.vtx(t, "linda", "identity")
	f.vtx(t, "u1", "unit")
	f.manages(t, "larry", "u1")
	f.manages(t, "linda", "u1")
	f.vtx(t, "wo1", "workorder")
	f.link(t, "locatedAt", "wo1", "u1")
	f.unitAspect(t, "wo1", "report", "workOrderReport", map[string]any{"summary": "leak", "priority": "urgent"})

	rows := f.projectWorkOrders(t)
	require.Len(t, rows, 2, "a co-managed unit's order fans out to one row per landlord")
	byLandlord := map[string]bool{}
	for _, r := range rows {
		byLandlord[r.Values["landlord_key"].(string)] = true
	}
	require.True(t, byLandlord["vtx.identity."+f.ids["larry"]])
	require.True(t, byLandlord["vtx.identity."+f.ids["linda"]])
}

func TestLandlordWorkOrdersRead_ReportedByResidentTrue(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	f.vtx(t, "jordan", "identity")
	f.vtx(t, "u1", "unit")
	f.manages(t, "larry", "u1")
	f.link(t, "residesIn", "jordan", "u1")
	f.vtx(t, "wo1", "workorder")
	f.link(t, "locatedAt", "wo1", "u1")
	f.link(t, "reportedBy", "wo1", "jordan")
	f.unitAspect(t, "wo1", "report", "workOrderReport", map[string]any{"summary": "leak", "priority": "urgent"})

	rows := f.projectWorkOrders(t)
	require.Len(t, rows, 1)
	require.Equal(t, true, rows[0].Values["reported_by_resident"], "the reporter resides in this order's own unit")
}

func TestLandlordWorkOrdersRead_ReportedByStaffFalse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	f.vtx(t, "priya", "identity")
	f.vtx(t, "u1", "unit")
	f.manages(t, "larry", "u1")
	f.vtx(t, "wo1", "workorder")
	f.link(t, "locatedAt", "wo1", "u1")
	f.link(t, "reportedBy", "wo1", "priya")
	f.unitAspect(t, "wo1", "report", "workOrderReport", map[string]any{"summary": "leak", "priority": "urgent"})
	// No residesIn link at all — a staff reporter.

	rows := f.projectWorkOrders(t)
	require.Len(t, rows, 1)
	require.Equal(t, false, rows[0].Values["reported_by_resident"], "a reporter with no residesIn link is staff")
}

func TestLandlordWorkOrdersRead_NoReporterLinkFalse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	f.vtx(t, "u1", "unit")
	f.manages(t, "larry", "u1")
	f.vtx(t, "wo1", "workorder")
	f.link(t, "locatedAt", "wo1", "u1")
	f.unitAspect(t, "wo1", "report", "workOrderReport", map[string]any{"summary": "leak", "priority": "urgent"})
	// No reportedBy link — the pre-backfill legacy shape.

	rows := f.projectWorkOrders(t)
	require.Len(t, rows, 1)
	require.Equal(t, false, rows[0].Values["reported_by_resident"], "no reportedBy link reads false, never null")
}

func TestLandlordWorkOrdersRead_ReporterResidesInDifferentUnitFalse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	f.vtx(t, "jordan", "identity")
	f.vtx(t, "u1", "unit")
	f.vtx(t, "u2", "unit")
	f.manages(t, "larry", "u1")
	f.link(t, "residesIn", "jordan", "u2")
	f.vtx(t, "wo1", "workorder")
	f.link(t, "locatedAt", "wo1", "u1")
	f.link(t, "reportedBy", "wo1", "jordan")
	f.unitAspect(t, "wo1", "report", "workOrderReport", map[string]any{"summary": "leak", "priority": "urgent"})

	rows := f.projectWorkOrders(t)
	require.Len(t, rows, 1)
	require.Equal(t, false, rows[0].Values["reported_by_resident"],
		"the reporter resides at a DIFFERENT unit than this order — the constrained-target walk must not admit it")
}

func TestLandlordWorkOrdersRead_OpenTaskAndResidentReporterCoexist(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	f.vtx(t, "jordan", "identity")
	f.vtx(t, "u1", "unit")
	f.manages(t, "larry", "u1")
	f.link(t, "residesIn", "jordan", "u1")
	f.vtx(t, "wo1", "workorder")
	f.link(t, "locatedAt", "wo1", "u1")
	f.link(t, "reportedBy", "wo1", "jordan")
	f.unitAspect(t, "wo1", "report", "workOrderReport", map[string]any{"summary": "leak", "priority": "urgent"})
	f.taskVtx(t, "t1", "open")
	f.link(t, "scopedTo", "t1", "wo1")

	rows := f.projectWorkOrders(t)
	require.Len(t, rows, 1, "two independent OPTIONAL MATCH fans off the same order must not fan the row out")
	v := rows[0].Values
	require.EqualValues(t, 1, v["open_task_count"])
	require.Equal(t, true, v["reported_by_resident"])
}

func TestLandlordWorkOrdersRead_ExcludesBuildingLocatedOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	f.vtx(t, "tower", "building")
	f.vtx(t, "wo1", "workorder")
	f.link(t, "locatedAt", "wo1", "tower")
	f.unitAspect(t, "wo1", "report", "workOrderReport", map[string]any{"summary": "lobby light out", "priority": "low"})
	// No manages link on a building — landlordWorkOrdersRead is units only.

	rows := f.projectWorkOrders(t)
	require.Empty(t, rows, "a building-located order has no manages walk to anchor a landlord row on")
}
