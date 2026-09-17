package maintenancedomain

// Rule-engine proof of the workOrderQueue convergence cypher — the
// workorder-anchored gap that turns an unresolved, unqueued order into queued
// work. The spec is driven through the `full` engine directly (the engine
// selected at activation via engine:"full") against an embedded NATS
// Core/Adjacency KV, so what is pinned is the projection row itself: one row
// per anchor, strict-bool gap columns, and the gap FALSE over every state an
// arm of the op leaves (an open task, a resolution) and TRUE again over the
// one the platform re-opens (a cancelled task alone). No $now is supplied —
// the cypher reads no clock.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type lensFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string // logicalName -> bare NanoID
	types         map[string]string // bare NanoID -> key type
}

func newLensFixture(t *testing.T) *lensFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &lensFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

// vtx mints a vertex whose class is its own key type and whose root data is
// the given map (nil → {}).
func (f *lensFixture) vtx(t *testing.T, name, typ string, data map[string]any) string {
	t.Helper()
	id := lenstest.NanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	if data == nil {
		data = map[string]any{}
	}
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *lensFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *lensFixture) edge(t *testing.T, name, fromName, toName string) {
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

// seedWorkOrder mints an unresolved work order with its .report aspect — the
// shape ReportIssue commits.
func (f *lensFixture) seedWorkOrder(t *testing.T, name string) string {
	t.Helper()
	key := f.vtx(t, name, "workorder", nil)
	f.aspect(t, name, "report", "workOrderReport", map[string]any{
		"summary": "Kitchen tap is dripping", "priority": "normal",
		"reportedAt": "2026-07-21T09:00:00Z", "reportedBy": "vtx.identity." + lenstest.NanoID("reporter")})
	return key
}

// seedTask mints a task in the given status scopedTo the work order — the
// root shape orchestration-base's task DDL writes ({status, expiresAt}).
func (f *lensFixture) seedTask(t *testing.T, taskName, workOrderName, status string) {
	t.Helper()
	f.vtx(t, taskName, "task", map[string]any{"status": status, "expiresAt": "2026-08-20T00:00:00Z"})
	f.edge(t, "scopedTo", taskName, workOrderName)
}

// resolve writes the .resolution aspect ResolveWorkOrder commits.
func (f *lensFixture) resolve(t *testing.T, name string) {
	t.Helper()
	f.aspect(t, name, "resolution", "workOrderResolution", map[string]any{
		"notes": "Replaced the washer.", "resolvedAt": "2026-07-21T11:30:00Z", "resolvedBy": "vtx.identity." + lenstest.NanoID("tech")})
}

// projectWorkOrderQueue runs workOrderQueueSpec anchored on the named work order.
func (f *lensFixture) projectWorkOrderQueue(t *testing.T, name string) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(workOrderQueueSpec)
	require.NoError(t, err, "workOrderQueue cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": "vtx.workorder." + f.ids[name],
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "exactly one row per work-order anchor")
	return out[0].Values
}

// TestWorkOrderQueue_UnresolvedUntaskedOrderOpensTheGap is the gap vector: a
// reported work order nobody has queued or resolved projects missing_task =
// true, openTaskCount 0, resolvedAt null — the row Weaver dispatches on.
func TestWorkOrderQueue_UnresolvedUntaskedOrderOpensTheGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")

	v := f.projectWorkOrderQueue(t, "wo")
	require.Equal(t, true, v["missing_task"], "no task, no resolution → queue it")
	require.Equal(t, true, v["violating"])
	require.Equal(t, "vtx.workorder."+f.ids["wo"], v["entityKey"], "row.entityKey is the anchor's own key — the assignTask target")
	require.Nil(t, v["resolvedAt"])
	require.EqualValues(t, 0, v["openTaskCount"])
	_, isBool := v["missing_task"].(bool)
	require.True(t, isBool, "missing_task must be a strict bool for Weaver's openGapColumns")
}

// TestWorkOrderQueue_OpenTaskClosesTheGap: an open task scopedTo the order IS
// the work — the gap closes and stays closed, so a re-dispatch never races the
// task already queued.
func TestWorkOrderQueue_OpenTaskClosesTheGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.seedTask(t, "task", "wo", "open")

	v := f.projectWorkOrderQueue(t, "wo")
	require.Equal(t, false, v["missing_task"], "an open task is the work; nothing to queue")
	require.Equal(t, false, v["violating"])
	require.EqualValues(t, 1, v["openTaskCount"])
}

// TestWorkOrderQueue_ResolutionClosesTheGap: a .resolution closes the gap
// whether or not a task ever existed — a standing-path resolve with no task
// is the case this vector seeds, since the task-path one always carries a
// complete task beside it.
func TestWorkOrderQueue_ResolutionClosesTheGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.resolve(t, "wo")

	v := f.projectWorkOrderQueue(t, "wo")
	require.Equal(t, false, v["missing_task"], "a resolved order has no work left to queue")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-21T11:30:00Z", v["resolvedAt"])
}

// TestWorkOrderQueue_ResolvedWithCompleteTaskStaysClosed is the task-path
// shape: ResolveWorkOrder under the task grant writes .resolution and the
// §10.6 auto-complete flips the task to complete on the same commit. Neither
// fact alone is what the gap reads — the resolution is.
func TestWorkOrderQueue_ResolvedWithCompleteTaskStaysClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.seedTask(t, "task", "wo", "complete")
	f.resolve(t, "wo")

	v := f.projectWorkOrderQueue(t, "wo")
	require.Equal(t, false, v["missing_task"])
	require.EqualValues(t, 0, v["openTaskCount"], "a complete task is not open")
}

// TestWorkOrderQueue_CompleteTaskWithoutResolutionReopensTheGap: a task
// closed by orchestration-base's CompleteTask carries status = complete and
// no resolution behind it. The order is still unresolved and nobody holds it,
// so the gap re-opens — level-triggered, one re-queue per human completion.
// Its sibling TestWorkOrderQueue_ResolvedWithCompleteTaskStaysClosed is the
// same task state WITH the resolution, closed.
func TestWorkOrderQueue_CompleteTaskWithoutResolutionReopensTheGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.seedTask(t, "task", "wo", "complete")

	v := f.projectWorkOrderQueue(t, "wo")
	require.Equal(t, true, v["missing_task"], "a completed task with no resolution leaves the order unresolved and unheld; queue it again")
	require.Equal(t, true, v["violating"])
	require.EqualValues(t, 0, v["openTaskCount"])
	require.Nil(t, v["resolvedAt"])
}

// TestWorkOrderQueue_CancelledTaskAloneReopensTheGap: CancelTask is the
// platform's "this is not going to be done"; the order is still unresolved, so
// the gap re-opens and Weaver queues it again.
func TestWorkOrderQueue_CancelledTaskAloneReopensTheGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.seedTask(t, "task", "wo", "cancelled")

	v := f.projectWorkOrderQueue(t, "wo")
	require.Equal(t, true, v["missing_task"], "a cancelled task is not the work; queue it again")
	require.Equal(t, true, v["violating"])
	require.EqualValues(t, 0, v["openTaskCount"])
}

// TestWorkOrderQueue_OpenBesideCancelledStaysClosed pins the count over a fan:
// a cancelled task and an open one on the same order count exactly one open
// task, one row — the fan neither multiplies the anchor nor lets the cancelled
// sibling re-open a gap the open task closes.
func TestWorkOrderQueue_OpenBesideCancelledStaysClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.seedTask(t, "cancelled", "wo", "cancelled")
	f.seedTask(t, "open", "wo", "open")

	v := f.projectWorkOrderQueue(t, "wo")
	require.Equal(t, false, v["missing_task"])
	require.Equal(t, false, v["violating"])
	require.EqualValues(t, 1, v["openTaskCount"], "count(DISTINCT CASE …) over the fan's keys: one open, one cancelled → 1")
}

// TestWorkOrderQueue_ExpiredOpenTaskStaysClosed pins decision 1's non-goal: an
// open task past its expiresAt is still 'open' (expiry is not a status
// transition), so the gap stays closed — that lapse is orchestration-base's
// unroutedTasks surfaced issue, never a re-queue from here.
func TestWorkOrderQueue_ExpiredOpenTaskStaysClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.vtx(t, "task", "task", map[string]any{"status": "open", "expiresAt": "2020-01-01T00:00:00Z"})
	f.edge(t, "scopedTo", "task", "wo")

	v := f.projectWorkOrderQueue(t, "wo")
	require.Equal(t, false, v["missing_task"], "an expired-but-open task is unroutedTasks' issue, not a re-queue")
	require.EqualValues(t, 1, v["openTaskCount"])
}

// TestWorkOrderQueue_TaskOnAnotherOrderDoesNotCount: the scopedTo walk is the
// anchor's own — a task queued for a sibling order leaves this one's gap open.
func TestWorkOrderQueue_TaskOnAnotherOrderDoesNotCount(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	f.seedWorkOrder(t, "other")
	f.seedTask(t, "task", "other", "open")

	v := f.projectWorkOrderQueue(t, "wo")
	require.Equal(t, true, v["missing_task"])
	require.EqualValues(t, 0, v["openTaskCount"])
}

// TestWorkOrderQueue_PlaybookColumnsMatchLens is the §10.2↔§10.8 seam pin
// (lease-signing's TestLeaseSigning_PlaybookColumnsMatchLens shape): the
// target's LensRef resolves to the lens, its TargetID is the lens's
// OutputKeyPattern prefix, every row.<col> the playbook templates is a
// BodyColumn, and every missing_* column has a gap entry AND every gap entry
// names a missing_* column — the bijection that keeps Weaver from holding a
// row on the long redelivery floor for a column nothing declares.
func TestWorkOrderQueue_PlaybookColumnsMatchLens(t *testing.T) {
	targets := WeaverTargets()
	require.Len(t, targets, 1)
	target := targets[0]

	var lens *pkgmgr.LensSpec
	for i := range Lenses() {
		if l := Lenses()[i]; l.CanonicalName == target.LensRef {
			lens = &l
		}
	}
	require.NotNil(t, lens, "target %q: LensRef %q resolves to no lens this package declares", target.TargetID, target.LensRef)
	require.NotNil(t, lens.Output)
	require.Equal(t, target.TargetID, strings.TrimSuffix(lens.Output.OutputKeyPattern, ".{actorSuffix}"),
		"TargetID must be the lens OutputKeyPattern prefix (the §10.2↔§10.8 binding)")
	require.Equal(t, "actorAggregate", lens.ProjectionKind)
	require.Equal(t, "weaver-targets", lens.Bucket)

	cols := map[string]bool{}
	for _, c := range append(append([]string{}, lens.Output.BodyColumns...), lens.Output.StaticEmptyColumns...) {
		cols[c] = true
	}
	require.True(t, cols["violating"], "Weaver dispatches only violating rows")
	for col := range cols {
		if strings.HasPrefix(col, "missing_") {
			_, ok := target.Gaps[col]
			require.True(t, ok, "lens projects gap column %q with no Gaps entry", col)
		}
	}
	for col, ga := range target.Gaps {
		require.True(t, strings.HasPrefix(col, "missing_"), "gap %q is not a missing_* column", col)
		require.True(t, cols[col], "gap %q is declared but the lens projects no such column", col)
		for _, tmpl := range []string{ga.Subject, ga.Pattern, ga.Operation, ga.Assignee, ga.Queue, ga.Target} {
			if strings.HasPrefix(tmpl, "row.") {
				require.True(t, cols[strings.TrimPrefix(tmpl, "row.")], "gap %q templates %q, not a lens column", col, tmpl)
			}
		}
	}
}

// TestWorkOrderQueue_SpecProjectsEveryBodyColumn: every declared BodyColumn is
// a RETURN alias of the cypher, so no envelope key is silently null.
func TestWorkOrderQueue_SpecProjectsEveryBodyColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedWorkOrder(t, "wo")
	v := f.projectWorkOrderQueue(t, "wo")
	for _, col := range Lenses()[0].Output.BodyColumns {
		_, ok := v[col]
		require.True(t, ok, "BodyColumn %q is not projected by the cypher", col)
	}
}
