package orchestrationbase

// Rule-engine proof of the unroutedTasks convergence lens (FR29), driven
// through the `full` engine (engine:"full") against an embedded NATS
// Core/Adjacency KV — the same harness clinic-reminders/lease-signing use.
//
// What decides "expired" is a recorded FACT, not a clock: the instant an @at
// armed on the task actually fired, recorded on it in the freshnessExpiry
// marker. NO $now is supplied to ANY vector in this file, because no lens this
// package declares references one — passing one anyway would let a
// clock-reading regression pass unnoticed.
//
// Which field of the marker a lens reads follows from what the lens IS.
// unroutedTasks and staleAssignedTasks are convergence targets, so each reads
// its OWN byTarget entry (a sibling's fire is not its lapse).
// capabilityEphemeral is an observer with no entry of its own, so it reads
// `expiredAt`, the entity-wide maximum — any recorded instant at or after the
// task's deadline proves the task expired, whichever target fired. myTasks
// reads no deadline at all, by its own doc comment's reasoning.
//
//   - QUEUED, NO RECORDED LAPSE: not violating; freshUntil = expiresAt (arms the
//     @at timer) — still time to claim it.
//   - QUEUED, LAPSE RECORDED AT expiresAt (never claimed): violating;
//     missing_claim true; freshUntil null — the row itself drives dispatch.
//   - DIRECTLY ASSIGNED (no queuedFor at all): the required -[:queuedFor]->
//     match never fires — zero rows, so the task never gets a weaver-targets
//     entry in the first place.
//   - CLAIMED (assignedTo, queuedFor tombstoned): same zero-rows outcome as
//     direct assignment — ClaimTask's atomic swap makes the row disappear on
//     the next reprojection (EmptyBehavior:"delete" removes any prior row).
//   - CANCELLED while still queued: the WHERE status='open' gate excludes it —
//     zero rows.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type unrFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newUnrFixture(t *testing.T) *unrFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &unrFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *unrFixture) vtx(t *testing.T, name, typ string, data map[string]any) string {
	t.Helper()
	id := lenstest.NanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	if data == nil {
		data = map[string]any{}
	}
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *unrFixture) edge(t *testing.T, name, fromName, toName string) {
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

// aspect writes one aspect document onto a named vertex.
func (f *unrFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

// recordLapse writes the freshnessExpiry marker MarkExpired commits when a
// target's @at fires: the instant the timer fired for, recorded under that
// target's own key in byTarget, with expiredAt carrying the entity-wide maximum.
// A marker at or after a stored deadline is a recorded lapse of it; one before it
// is a fire for an earlier deadline the current one has outrun. byTarget takes
// several entries because one task can carry both this package's deadline
// targets in one marker slot.
func (f *unrFixture) recordLapse(t *testing.T, name string, byTarget map[string]string) {
	t.Helper()
	entries := map[string]any{}
	maxAt := ""
	for target, at := range byTarget {
		entries[target] = at
		if at > maxAt {
			maxAt = at
		}
	}
	f.aspect(t, name, "freshnessExpiry", "freshnessExpiry", map[string]any{
		"expiredAt": maxAt,
		"byTarget":  entries,
	})
}

// projectUnrouted runs the anchored unroutedTasks spec for one task. NO clock
// parameter is supplied — the cypher references none.
func (f *unrFixture) projectUnrouted(t *testing.T, taskName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(unroutedTasksSpec)
	require.NoError(t, err, "unroutedTasks cypher must parse on the full engine")
	taskKey := "vtx.task." + f.ids[taskName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": taskKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

func TestUnroutedTasks_QueuedNotYetExpired(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-07-01T12:00:00Z"})
	f.vtx(t, "queueRole", "role", nil)
	f.edge(t, "queuedFor", "task1", "queueRole")

	rows := f.projectUnrouted(t, "task1")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.task."+f.ids["task1"], v["entityKey"])
	require.Equal(t, false, v["missing_claim"], "no timer has fired on this task — not stale")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-01T12:00:00Z", v["freshUntil"], "freshUntil = expiresAt arms the @at timer while no lapse is recorded")
	require.Equal(t, "vtx.role."+f.ids["queueRole"], v["queuedRole"])
}

func TestUnroutedTasks_QueuedExpired(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-06-29T12:00:00Z"})
	f.vtx(t, "queueRole", "role", nil)
	f.edge(t, "queuedFor", "task1", "queueRole")
	f.recordLapse(t, "task1", map[string]string{UnroutedTasksTarget: "2026-06-29T12:00:00Z"})

	v := f.projectUnrouted(t, "task1")[0].Values
	require.Equal(t, true, v["missing_claim"], "a recorded lapse at expiresAt while still queued — never claimed")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "the lapse is recorded → nothing left to wait for → no armed timer")
	requireIntColumn(t, v, "maxretries_claim", maxClaimRetries)
}

// requireIntColumn asserts a lens-projected column is present and equals want
// as an integer. The full engine returns a numeric literal as int64 (the
// cypher parser's strconv.ParseInt path), so accept int/int64/float64 alike —
// what matters is the integer value, mirroring lease-signing's own
// requireIntColumn (lens_cypher_test.go).
func requireIntColumn(t *testing.T, v map[string]any, col string, want int) {
	t.Helper()
	got, ok := v[col]
	require.Truef(t, ok, "row must carry the %s column", col)
	switch n := got.(type) {
	case int:
		require.Equalf(t, want, n, "%s", col)
	case int64:
		require.Equalf(t, want, int(n), "%s", col)
	case float64:
		require.Equalf(t, want, int(n), "%s", col)
	default:
		t.Fatalf("%s is %T, not a numeric cap", col, got)
	}
}

func TestUnroutedTasks_DirectlyAssignedNeverMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-06-29T12:00:00Z"})
	f.vtx(t, "bob", "identity", nil)
	f.edge(t, "assignedTo", "task1", "bob")
	f.recordLapse(t, "task1", map[string]string{UnroutedTasksTarget: "2026-06-29T12:00:00Z"})

	rows := f.projectUnrouted(t, "task1")
	require.Empty(t, rows, "a direct-assigned task carries no queuedFor link, so the required MATCH never fires — even with the lapse recorded")
}

func TestUnroutedTasks_ClaimedTaskNoLongerMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	// ClaimTask's atomic swap: queuedFor is gone, assignedTo(claimant) is the
	// only relationship left — the same shape as a direct assignment.
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-06-29T12:00:00Z"})
	f.vtx(t, "claimant", "identity", nil)
	f.edge(t, "assignedTo", "task1", "claimant")

	rows := f.projectUnrouted(t, "task1")
	require.Empty(t, rows, "post-ClaimTask the queuedFor link is gone — the row disappears (EmptyBehavior:delete)")
}

func TestUnroutedTasks_CancelledWhileQueuedNeverMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "cancelled", "expiresAt": "2026-06-29T12:00:00Z"})
	f.vtx(t, "queueRole", "role", nil)
	f.edge(t, "queuedFor", "task1", "queueRole")

	rows := f.projectUnrouted(t, "task1")
	require.Empty(t, rows, "a cancelled task is excluded by the status='open' gate even if queuedFor lingers")
}

// projectIdentitySpec runs an identity-anchored spec (myTasksSpec /
// capabilityEphemeralSpec) for one actor. NO clock parameter is supplied —
// neither cypher references one.
func (f *unrFixture) projectIdentitySpec(t *testing.T, spec, identityName string) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "spec must parse on the full engine")
	actorKey := "vtx.identity." + f.ids[identityName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": actorKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "the identity anchor always yields exactly one row")
	return out[0].Values
}

// ephemeralGrantKeys returns the taskKeys capabilityEphemeral grants an actor,
// with the degenerate (null-taskKey) collect artifacts the envelope wrapper
// drops filtered out.
func (f *unrFixture) ephemeralGrantKeys(t *testing.T, identityName string) map[string]bool {
	t.Helper()
	v := f.projectIdentitySpec(t, capabilityEphemeralSpec, identityName)
	grants, _ := v["ephemeralGrants"].([]any)
	out := map[string]bool{}
	for _, row := range grants {
		m, _ := row.(map[string]any)
		if tk, ok := m["taskKey"].(string); ok && tk != "" {
			out[tk] = true
		}
	}
	return out
}

// requireGranted / requireNotGranted assert one task's grant by key, so a
// vector says which task moved rather than how many did.
func (f *unrFixture) requireGranted(t *testing.T, identityName, taskName, why string) {
	t.Helper()
	require.Truef(t, f.ephemeralGrantKeys(t, identityName)["vtx.task."+f.ids[taskName]], "%s", why)
}

func (f *unrFixture) requireNotGranted(t *testing.T, identityName, taskName, why string) {
	t.Helper()
	require.Falsef(t, f.ephemeralGrantKeys(t, identityName)["vtx.task."+f.ids[taskName]], "%s", why)
}

// TestMyTasks_ExpiredButOpen_StillProjected pins the deliberate choice
// documented on myTasksSpec: an assignedTo task past its expiresAt keeps
// rendering in the inbox, because there is no other path (no queuedFor link
// for FR29's unroutedTasks to catch, no reap/escalation mechanism) that
// would ever surface it to the assignee otherwise. Regressing this back to a
// status+expiresAt gate — the same shape capabilityEphemeral now correctly
// uses — would make the task invisible while it still blocks MergeIdentity
// and stays completable.
func TestMyTasks_ExpiredButOpen_StillProjected(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "bob", "identity", nil)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-06-29T12:00:00Z"})
	f.edge(t, "assignedTo", "task1", "bob")

	v := f.projectIdentitySpec(t, myTasksSpec, "bob")
	open, _ := v["openTasks"].([]any)
	var found bool
	for _, row := range open {
		m, _ := row.(map[string]any)
		if m["taskKey"] == "vtx.task."+f.ids["task1"] {
			found = true
		}
	}
	require.True(t, found, "an expired-but-still-open direct assignment must keep rendering — see myTasksSpec's doc comment")
}

// TestCapabilityEphemeral_CompletedTask_NotProjected proves
// capabilityEphemeralSpec's liveness gate: a task's operationType/target
// grant must not survive the task's own completion, even though
// transition_task (ddls.go) carries expiresAt forward unchanged on
// CompleteTask/CancelTask — expiresAt alone is not a valid liveness proxy
// once status has moved off 'open'. The marker is present too, so the row
// proves status ALONE closes it rather than leaning on the deadline half.
func TestCapabilityEphemeral_CompletedTask_NotProjected(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "bob", "identity", nil)
	f.vtx(t, "task1", "task", map[string]any{"status": "complete", "expiresAt": "2026-09-05T12:00:00Z"})
	f.edge(t, "assignedTo", "task1", "bob")
	f.recordLapse(t, "task1", map[string]string{StaleAssignedTasksTarget: "2026-09-05T12:00:00Z"})

	f.requireNotGranted(t, "bob", "task1",
		"a completed task's grant must not survive its own completion")
}

// TestCapabilityEphemeral_OpenAndLive_StillProjected is the NEVER-LAPSED row:
// an open direct assignment that nothing has ever marked still grants.
func TestCapabilityEphemeral_OpenAndLive_StillProjected(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "bob", "identity", nil)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-07-01T12:00:00Z"})
	f.edge(t, "assignedTo", "task1", "bob")

	f.requireGranted(t, "bob", "task1",
		"an open task with NO recorded lapse must still grant — nothing has fired on it")
}

// TestCapabilityEphemeral_RecordedLapse_Retracted is the LAPSED row: the fire
// landed at the task's own deadline, so the grant is gone from the projection.
func TestCapabilityEphemeral_RecordedLapse_Retracted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	const expiresAt = "2026-06-29T12:00:00Z"
	f.vtx(t, "bob", "identity", nil)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": expiresAt})
	f.edge(t, "assignedTo", "task1", "bob")
	f.recordLapse(t, "task1", map[string]string{StaleAssignedTasksTarget: expiresAt})

	f.requireNotGranted(t, "bob", "task1",
		"a recorded lapse at the task's own deadline retracts the grant (the >= boundary counts as expired)")
}

// TestCapabilityEphemeral_LapsedButUnmarked_StaysListedAndIsDeniedAtDispatch is
// the DELIBERATE FAIL-DIRECTION, and the one row where this lens's projection
// differs from a clock reading's. A deadline can pass before the routing-shape
// target's @at fires; until MarkExpired lands, the marker hop binds nil, a nil
// ordering comparison is false, NOT(false) is true, and the grant is still
// LISTED.
//
// The two axes fail in opposite directions and both are named on purpose:
//
//   - PROJECTION axis: open. cap.ephemeral.<actor> carries the entry for the
//     length of one fire + one commit + one reprojection.
//   - DELIVERY axis: closed. The Processor re-checks every listed grant against
//     its own clock at lookup time and refuses a past expiresAt as an
//     AuthContextMismatch (internal/processor/step3_auth_capability.go:357-359,
//     pinned by TestCapabilityAuthorizer_TaskPath_Expired). Listed is not
//     authorized.
//
// A future reader tempted to "fix" this by asserting the marker's presence
// would close the projection axis at the cost of retracting every task no
// target has ever marked.
func TestCapabilityEphemeral_LapsedButUnmarked_StaysListedAndIsDeniedAtDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "bob", "identity", nil)
	// A long-past deadline with NO marker: the @at has not fired (or its
	// MarkExpired has not committed) yet.
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2020-06-01T12:00:00Z"})
	f.edge(t, "assignedTo", "task1", "bob")

	f.requireGranted(t, "bob", "task1",
		"a lapsed task with NO recorded lapse stays LISTED — the projection axis is open until the fire lands, "+
			"and the Processor's lookup-time expiresAt check is what denies the dispatch")
}

// TestCapabilityEphemeral_DeadlineMovedLater_GrantedAgain is the RE-ARM row.
// Nothing ever clears the marker, so a task re-issued with a later deadline has
// to come back off the stored comparison alone — a presence test would retract
// it forever.
func TestCapabilityEphemeral_DeadlineMovedLater_GrantedAgain(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "bob", "identity", nil)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-08-01T12:00:00Z"})
	f.edge(t, "assignedTo", "task1", "bob")
	f.recordLapse(t, "task1", map[string]string{StaleAssignedTasksTarget: "2026-06-29T12:00:00Z"})

	f.requireGranted(t, "bob", "task1",
		"a recorded instant the current deadline has outrun is not a lapse of THIS deadline — the grant returns with no clearing write")
}

// TestCapabilityEphemeral_DeadlineMovedEarlier_Retracted is the
// DEADLINE-PULLED-IN row, asserted deliberately so a later reader does not
// "fix" it: a grant shortened below an instant a timer already fired at reads
// expired at once, and that is correct — a fire did happen at or after the new
// deadline.
func TestCapabilityEphemeral_DeadlineMovedEarlier_Retracted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "bob", "identity", nil)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-06-20T12:00:00Z"})
	f.edge(t, "assignedTo", "task1", "bob")
	f.recordLapse(t, "task1", map[string]string{StaleAssignedTasksTarget: "2026-06-29T12:00:00Z"})

	f.requireNotGranted(t, "bob", "task1",
		"the recorded fire is after the new deadline, so it IS a lapse of it")
}

// TestCapabilityEphemeral_ClaimedAfterTheLapse_ForeignTargetEntryRetracts is the
// window the entity-wide maximum closes. ClaimTask admits a lapsed queued task
// (ddls.go checks status='open' only), so a task can carry the unroutedTasks
// fire from its time in the queue and then become a direct assignment whose own
// staleAssignedTasks @at has not come round yet. The grant must be retracted on
// the instant that IS recorded: this lens is nobody's target, so a fire under
// any target id is a lapse it must honour.
//
// Reading a per-arm byTarget entry instead would leave the grant listed for
// that whole interval.
func TestCapabilityEphemeral_ClaimedAfterTheLapse_ForeignTargetEntryRetracts(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	const expiresAt = "2026-06-29T12:00:00Z"
	f.vtx(t, "claimant", "identity", nil)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": expiresAt})
	// Post-ClaimTask shape: queuedFor is gone, assignedTo(claimant) is all that
	// is left, and only the queue target's fire was ever recorded.
	f.edge(t, "assignedTo", "task1", "claimant")
	f.recordLapse(t, "task1", map[string]string{UnroutedTasksTarget: expiresAt})

	f.requireNotGranted(t, "claimant", "task1",
		"the entity-wide maximum honours the queue target's recorded fire, so the claimed task's grant is "+
			"retracted at once rather than waiting for the assignment target's own overdue @at")
}

// TestCapabilityEphemeral_MarkerWithEmptyByTarget_Retracted pins the shape a
// marker written before byTarget existed carries: `expiredAt` alone, naming no
// target. An observer reads exactly that field, so the marker still decides —
// and it must, because the instant in it was a fire instant then too.
//
// It is the other half of the absence gate: the no-marker row above keeps the
// grant, this one retracts it, and neither is inferable from the other.
func TestCapabilityEphemeral_MarkerWithEmptyByTarget_Retracted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "bob", "identity", nil)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-06-20T12:00:00Z"})
	f.edge(t, "assignedTo", "task1", "bob")
	// Written directly rather than through recordLapse: that helper derives
	// expiredAt from the byTarget entries, and this shape has none.
	f.aspect(t, "task1", "freshnessExpiry", "freshnessExpiry", map[string]any{
		"expiredAt": "2026-06-29T12:00:00Z",
		"byTarget":  map[string]any{},
	})

	f.requireNotGranted(t, "bob", "task1",
		"a marker carrying only expiredAt still records a fire at or after the deadline — the observer honours it")
}

// TestCapabilityEphemeral_ExpiresAtNeverWritten_Granted is the NEVER-WRITTEN
// row. CreateTask requires expiresAt (ddls.go "required"), so this task shape
// is unreachable through the op path; it is pinned anyway because the engine's
// answer to it is a property of the predicate, not of the population. The
// deadline hop binds nil, `E >= nil` is false, NOT(false) is true — granted,
// and the Processor's lookup-time check has no window to compare against
// either.
func TestCapabilityEphemeral_ExpiresAtNeverWritten_Granted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "bob", "identity", nil)
	f.vtx(t, "task1", "task", map[string]any{"status": "open"})
	f.edge(t, "assignedTo", "task1", "bob")

	f.requireGranted(t, "bob", "task1",
		"a task with no expiresAt at all binds nil at the deadline hop, which no recorded instant can be at or after")
}

// ephemeralArms are the three walks capabilityEphemeral binds a task through.
// Each seeds its own actor + task and returns their fixture names, so one
// predicate edit applied to two arms and not the third fails here BY ARM NAME
// — the shape a single shared vector would hide.
var ephemeralArms = []struct {
	name string
	seed func(t *testing.T, f *unrFixture, expiresAt string) (actor, task string)
}{
	{
		name: "directAssignment",
		seed: func(t *testing.T, f *unrFixture, expiresAt string) (string, string) {
			f.vtx(t, "arm1actor", "identity", nil)
			f.vtx(t, "arm1task", "task", map[string]any{"status": "open", "expiresAt": expiresAt})
			f.edge(t, "assignedTo", "arm1task", "arm1actor")
			return "arm1actor", "arm1task"
		},
	},
	{
		// The actor is the MANAGER: the report reports to it, and it inherits
		// the task assigned to the report (downward delegation).
		name: "managerDelegation",
		seed: func(t *testing.T, f *unrFixture, expiresAt string) (string, string) {
			f.vtx(t, "arm2manager", "identity", nil)
			f.vtx(t, "arm2report", "identity", nil)
			f.vtx(t, "arm2task", "task", map[string]any{"status": "open", "expiresAt": expiresAt})
			f.edge(t, "reportsTo", "arm2report", "arm2manager")
			f.edge(t, "assignedTo", "arm2task", "arm2report")
			return "arm2manager", "arm2task"
		},
	},
	{
		name: "roleQueueFanOut",
		seed: func(t *testing.T, f *unrFixture, expiresAt string) (string, string) {
			f.vtx(t, "arm3actor", "identity", nil)
			f.vtx(t, "arm3role", "role", nil)
			f.vtx(t, "arm3task", "task", map[string]any{"status": "open", "expiresAt": expiresAt})
			f.edge(t, "holdsRole", "arm3actor", "arm3role")
			f.edge(t, "queuedFor", "arm3task", "arm3role")
			return "arm3actor", "arm3task"
		},
	},
}

// TestCapabilityEphemeral_EveryArmReadsTheRecordedLapse runs the
// unlapsed/lapsed pair on each of the three arms. The predicate is written out
// three times in the cypher, once per arm, so one clause converted and another
// missed is exactly the drift this covers.
func TestCapabilityEphemeral_EveryArmReadsTheRecordedLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const expiresAt = "2026-06-29T12:00:00Z"
	for _, arm := range ephemeralArms {
		t.Run(arm.name, func(t *testing.T) {
			f := newUnrFixture(t)
			actor, task := arm.seed(t, f, expiresAt)

			f.requireGranted(t, actor, task,
				arm.name+": with no lapse recorded the task must grant")

			f.recordLapse(t, task, map[string]string{StaleAssignedTasksTarget: expiresAt})
			f.requireNotGranted(t, actor, task,
				arm.name+": a recorded lapse at the deadline must retract the grant — this arm's WHERE still reads a clock, or reads nothing")
		})
	}
}

// TestCapabilityEphemeral_EveryArmsTaskIsCoveredByARoutingShapeTarget is the
// POPULATION-COVERAGE pin, and it is what makes the recorded-fact read sound:
// this lens does not arm its own timer, so every task it projects has to be in
// the population of a target that does.
//
// An open task carries exactly one routing link — assignedTo (arms 1 and 2) or
// queuedFor (arm 3) — so staleAssignedTasks and unroutedTasks between them
// anchor the whole set, and each projects freshUntil = expiresAt, which is what
// arms the @at at the task's own deadline. A future WHERE edit on either target
// that strands one of these arms fails here, by arm name, instead of silently
// leaving that arm's grants un-retractable.
func TestCapabilityEphemeral_EveryArmsTaskIsCoveredByARoutingShapeTarget(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const expiresAt = "2026-06-29T12:00:00Z"
	for _, arm := range ephemeralArms {
		t.Run(arm.name, func(t *testing.T) {
			f := newUnrFixture(t)
			actor, task := arm.seed(t, f, expiresAt)
			f.requireGranted(t, actor, task, arm.name+": the fixture task must be granted, or the coverage claim is about nothing")

			var rows []ruleengine.ProjectionResult
			var target string
			if arm.name == "roleQueueFanOut" {
				rows, target = f.projectUnrouted(t, task), UnroutedTasksTarget
			} else {
				rows, target = f.projectStaleAssigned(t, task), StaleAssignedTasksTarget
			}
			require.Lenf(t, rows, 1,
				"%s: the task capabilityEphemeral grants must be anchored by %s, or nothing ever arms an @at at its deadline "+
					"and its grant can never be retracted", arm.name, target)
			require.Equalf(t, expiresAt, rows[0].Values["freshUntil"],
				"%s: %s must project freshUntil = expiresAt, which is the @at that records the lapse this lens reads",
				arm.name, target)
		})
	}
}

// projectStaleAssigned runs staleAssignedTasksSpec (task-anchored, like
// unroutedTasksSpec), with no clock parameter — that cypher references none.
func (f *unrFixture) projectStaleAssigned(t *testing.T, taskName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(staleAssignedTasksSpec)
	require.NoError(t, err, "staleAssignedTasks cypher must parse on the full engine")
	taskKey := "vtx.task." + f.ids[taskName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": taskKey,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

func TestStaleAssignedTasks_AssignedNotYetExpired(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-07-01T12:00:00Z"})
	f.vtx(t, "bob", "identity", nil)
	f.edge(t, "assignedTo", "task1", "bob")

	rows := f.projectStaleAssigned(t, "task1")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_completion"], "no timer has fired on this task — not stale")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-07-01T12:00:00Z", v["freshUntil"])
	require.Equal(t, "vtx.identity."+f.ids["bob"], v["assignee"])
}

func TestStaleAssignedTasks_AssignedExpired(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-06-29T12:00:00Z"})
	f.vtx(t, "bob", "identity", nil)
	f.edge(t, "assignedTo", "task1", "bob")
	f.recordLapse(t, "task1", map[string]string{StaleAssignedTasksTarget: "2026-06-29T12:00:00Z"})

	v := f.projectStaleAssigned(t, "task1")[0].Values
	require.Equal(t, true, v["missing_completion"], "a recorded lapse at expiresAt while still open and unfinished")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"])
	requireIntColumn(t, v, "maxretries_completion", maxCompletionRetries)
}

func TestStaleAssignedTasks_QueuedNeverMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-06-29T12:00:00Z"})
	f.vtx(t, "queueRole", "role", nil)
	f.edge(t, "queuedFor", "task1", "queueRole")

	rows := f.projectStaleAssigned(t, "task1")
	require.Empty(t, rows, "a role-queued task carries no assignedTo link, so the required MATCH never fires — unroutedTasksSpec covers this case")
}

func TestStaleAssignedTasks_CompletedNeverMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "complete", "expiresAt": "2026-06-29T12:00:00Z"})
	f.vtx(t, "bob", "identity", nil)
	f.edge(t, "assignedTo", "task1", "bob")

	rows := f.projectStaleAssigned(t, "task1")
	require.Empty(t, rows, "a completed task is excluded by the status='open' gate")
}

// --- the recorded-lapse vectors both task targets share ---------------------

// TestUnroutedTasks_PastExpiresAtProjectedVerbatim is the
// PAST-DEADLINE-AT-FIRST-PROJECTION vector, and the one place a "null when the
// deadline is already past" guard would be tempting. A grant that lapsed while no
// target was watching carries no marker, so nothing has recorded it; the only
// thing that records it is this row projecting the past instant, Weaver
// publishing the overdue @at, and NATS releasing it immediately. Nulling it here
// arms nothing and the task never surfaces at all.
func TestUnroutedTasks_PastExpiresAtProjectedVerbatim(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	const longPast = "2020-06-01T12:00:00Z"
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": longPast})
	f.vtx(t, "queueRole", "role", nil)
	f.edge(t, "queuedFor", "task1", "queueRole")

	v := f.projectUnrouted(t, "task1")[0].Values
	require.Equal(t, longPast, v["freshUntil"],
		"an already-past expiresAt with no recorded lapse projects VERBATIM — the overdue @at is the only path to recording it")
	require.Equal(t, false, v["missing_claim"], "nothing has fired yet, so the gap is not open until the marker lands")

	f.recordLapse(t, "task1", map[string]string{UnroutedTasksTarget: longPast})
	v = f.projectUnrouted(t, "task1")[0].Values
	require.Equal(t, true, v["missing_claim"], "the recorded lapse opens the gap")
	require.Nil(t, v["freshUntil"])
}

// TestUnroutedTasks_ExtendedPastTheRecordedLapse is the RE-ARM vector. Nothing
// clears the marker — MarkExpired never tombstones it — so a task whose grant was
// re-issued with a later expiresAt must arm again off the stored comparison
// alone. A presence test would leave it permanently surfaced.
func TestUnroutedTasks_ExtendedPastTheRecordedLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	const extended = "2026-08-01T12:00:00Z"
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": extended})
	f.vtx(t, "queueRole", "role", nil)
	f.edge(t, "queuedFor", "task1", "queueRole")
	f.recordLapse(t, "task1", map[string]string{UnroutedTasksTarget: "2026-06-29T12:00:00Z"})

	v := f.projectUnrouted(t, "task1")[0].Values
	require.Equal(t, false, v["missing_claim"], "a lapse the current expiresAt has outrun is not a lapse of THIS deadline")
	require.Equal(t, extended, v["freshUntil"], "and the @at re-arms with no clearing write")
}

// TestUnroutedTasks_ShortenedBelowTheRecordedLapse is the
// DEADLINE-MOVED-EARLIER row of the state table, asserted deliberately so a later
// reader does not "fix" it: a grant pulled in below an instant this target
// already fired at reads expired at once. Correct — a timer did fire at or after
// the new deadline.
func TestUnroutedTasks_ShortenedBelowTheRecordedLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-06-20T12:00:00Z"})
	f.vtx(t, "queueRole", "role", nil)
	f.edge(t, "queuedFor", "task1", "queueRole")
	f.recordLapse(t, "task1", map[string]string{UnroutedTasksTarget: "2026-06-29T12:00:00Z"})

	v := f.projectUnrouted(t, "task1")[0].Values
	require.Equal(t, true, v["missing_claim"], "the recorded fire is after the new expiresAt, so it IS a lapse of it")
	require.Nil(t, v["freshUntil"])
}

// TestUnroutedTasks_BoundaryMarkerEqualsExpiresAt pins which side of the `>=` the
// equal instant falls on, and it is the one boundary this conversion MOVED: the
// clock form asked `$now > expiresAt`, strictly after. The timer fires AT the
// deadline and records that instant, so equality must count as the lapse —
// otherwise the ordinary fire would leave the row armed forever and nothing else
// would ever advance it.
func TestUnroutedTasks_BoundaryMarkerEqualsExpiresAt(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	const expiresAt = "2026-07-01T12:00:00Z"
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": expiresAt})
	f.vtx(t, "queueRole", "role", nil)
	f.edge(t, "queuedFor", "task1", "queueRole")
	f.recordLapse(t, "task1", map[string]string{UnroutedTasksTarget: expiresAt})

	v := f.projectUnrouted(t, "task1")[0].Values
	require.Equal(t, true, v["missing_claim"], "marker == expiresAt is a lapse (>= boundary)")
	require.Nil(t, v["freshUntil"])
}

// TestUnroutedTasks_SiblingTargetLapseDoesNotOpenThisGap is the isolation vector.
// A task is the anchor of BOTH unroutedTasks and staleAssignedTasks (and
// orphanedTaskGrants), all sharing one marker aspect, so reading the aspect's
// presence — or its entity-wide expiredAt maximum — would let one target's fire
// surface the other's row.
func TestUnroutedTasks_SiblingTargetLapseDoesNotOpenThisGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-07-01T12:00:00Z"})
	f.vtx(t, "queueRole", "role", nil)
	f.edge(t, "queuedFor", "task1", "queueRole")
	f.recordLapse(t, "task1", map[string]string{StaleAssignedTasksTarget: "2099-01-01T00:00:00Z"})

	v := f.projectUnrouted(t, "task1")[0].Values
	require.Equal(t, false, v["missing_claim"], "another target's recorded fire is not this target's lapse")
	require.Equal(t, "2026-07-01T12:00:00Z", v["freshUntil"], "and it does not disarm this target's timer either")
}

// TestUnroutedTasks_MarkerWithNoByTargetMapReadsUnlapsed pins the shape a marker
// written before byTarget existed carries. `expiredAt` alone says which entity
// last lapsed, never which target, so a lens that read it would answer for a
// sibling's fire. The four-hop read resolves to nil and compareAny answers false:
// unlapsed, and the timer stays armed.
func TestUnroutedTasks_MarkerWithNoByTargetMapReadsUnlapsed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-07-01T12:00:00Z"})
	f.vtx(t, "queueRole", "role", nil)
	f.edge(t, "queuedFor", "task1", "queueRole")
	f.aspect(t, "task1", "freshnessExpiry", "freshnessExpiry", map[string]any{"expiredAt": "2099-01-01T00:00:00Z"})

	v := f.projectUnrouted(t, "task1")[0].Values
	require.Equal(t, false, v["missing_claim"], "a marker with no byTarget map names no target and lapses nothing here")
	require.Equal(t, "2026-07-01T12:00:00Z", v["freshUntil"])
}

// TestStaleAssignedTasks_PastExpiresAtProjectedVerbatim — the same
// past-deadline vector on the direct-assignment half. It is asserted separately
// rather than inferred from unroutedTasks: the two cyphers are hand-copied
// mirrors, so a conversion applied to one and not the other is exactly the shape
// a shared assertion would hide.
func TestStaleAssignedTasks_PastExpiresAtProjectedVerbatim(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	const longPast = "2020-06-01T12:00:00Z"
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": longPast})
	f.vtx(t, "bob", "identity", nil)
	f.edge(t, "assignedTo", "task1", "bob")

	v := f.projectStaleAssigned(t, "task1")[0].Values
	require.Equal(t, longPast, v["freshUntil"],
		"an already-past expiresAt with no recorded lapse projects VERBATIM — the overdue @at is the only path to recording it")
	require.Equal(t, false, v["missing_completion"], "nothing has fired yet, so the gap is not open until the marker lands")

	f.recordLapse(t, "task1", map[string]string{StaleAssignedTasksTarget: longPast})
	v = f.projectStaleAssigned(t, "task1")[0].Values
	require.Equal(t, true, v["missing_completion"], "the recorded lapse opens the gap")
	require.Nil(t, v["freshUntil"])
}

// TestStaleAssignedTasks_ExtendedPastTheRecordedLapse — the re-arm vector on the
// direct-assignment half.
func TestStaleAssignedTasks_ExtendedPastTheRecordedLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	const extended = "2026-08-01T12:00:00Z"
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": extended})
	f.vtx(t, "bob", "identity", nil)
	f.edge(t, "assignedTo", "task1", "bob")
	f.recordLapse(t, "task1", map[string]string{StaleAssignedTasksTarget: "2026-06-29T12:00:00Z"})

	v := f.projectStaleAssigned(t, "task1")[0].Values
	require.Equal(t, false, v["missing_completion"], "a lapse the current expiresAt has outrun is not a lapse of THIS deadline")
	require.Equal(t, extended, v["freshUntil"], "and the @at re-arms with no clearing write")
}

// TestStaleAssignedTasks_SiblingTargetLapseDoesNotOpenThisGap — the isolation
// vector from the other direction: unroutedTasks' fire must not surface a direct
// assignment.
func TestStaleAssignedTasks_SiblingTargetLapseDoesNotOpenThisGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-07-01T12:00:00Z"})
	f.vtx(t, "bob", "identity", nil)
	f.edge(t, "assignedTo", "task1", "bob")
	f.recordLapse(t, "task1", map[string]string{UnroutedTasksTarget: "2099-01-01T00:00:00Z"})

	v := f.projectStaleAssigned(t, "task1")[0].Values
	require.Equal(t, false, v["missing_completion"], "another target's recorded fire is not this target's lapse")
	require.Equal(t, "2026-07-01T12:00:00Z", v["freshUntil"], "and it does not disarm this target's timer either")
}

// TestTaskDeadlineLenses_ReferenceNoClockParameter is the structural half of the
// conversion, asserted on the compiled cyphers rather than on any one row: a lens
// that reads $now or $projectedAt projects a clock reading the sweep's deep
// verify cannot compare, which is the divergence this conversion removes. On
// capabilityEphemeral that divergence is the one in the corpus whose escalation
// reaches `error`, since its rows are the auth plane's.
//
// Every lens this package declares is here: the two task-deadline convergence
// targets, the auth-plane observer, and myTasks, which projects a deadline
// column but compares it to nothing.
func TestTaskDeadlineLenses_ReferenceNoClockParameter(t *testing.T) {
	for _, tc := range []struct{ name, spec string }{
		{"unroutedTasks", unroutedTasksSpec},
		{"staleAssignedTasks", staleAssignedTasksSpec},
		{"capabilityEphemeral", capabilityEphemeralSpec},
		{"myTasks", myTasksSpec},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := full.New()
			cr, err := eng.Parse(tc.spec)
			require.NoError(t, err)
			fullCR, isFull := cr.(*full.CompiledRule)
			require.True(t, isFull, "must compile to the full engine")
			for _, param := range []string{"now", "projectedAt"} {
				referenced, exhaustive := fullCR.ReferencesParam(param)
				require.Truef(t, exhaustive, "%s: the query shape must be provably free of $%s", tc.name, param)
				require.Falsef(t, referenced,
					"%s must reference no $%s — expiry is a recorded fact, not a clock reading", tc.name, param)
			}
		})
	}
}

// TestTaskDeadlineLenses_ReadTheirOwnTargetsMarkerEntry binds the two halves that
// can silently drift apart: the §10.8 TargetID Weaver fires a timer under, and
// the byTarget key the lens compares against its deadline. Both cyphers are built
// with fmt.Sprintf from the same constant the target spec carries, so this pins
// that the splice actually happened — a lens reading an entry nothing writes has
// a gap that can never open, with every row still projecting and every
// seeded-marker test still passing.
func TestTaskDeadlineLenses_ReadTheirOwnTargetsMarkerEntry(t *testing.T) {
	specs := map[string]string{}
	for _, l := range Lenses() {
		specs[l.CanonicalName] = l.Spec
	}
	var checked int
	for _, tgt := range WeaverTargets() {
		spec, ok := specs[tgt.LensRef]
		require.Truef(t, ok, "target %s names lens %s, which this package must declare", tgt.TargetID, tgt.LensRef)
		if !strings.Contains(spec, "freshnessExpiry") {
			continue
		}
		require.Containsf(t, spec, "byTarget."+tgt.TargetID,
			"lens %s reads a freshness marker but not under its own target id %q — the timer that fires writes an entry this cypher never reads",
			tgt.LensRef, tgt.TargetID)
		checked++
	}
	require.Equal(t, 2, checked,
		"unroutedTasks and staleAssignedTasks each read a recorded lapse; a drop here is a lens that went back to a clock")
}
