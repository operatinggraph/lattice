package orchestrationbase

// Rule-engine proof of the unroutedTasks convergence lens (FR29), driven
// through the `full` engine (engine:"full") against an embedded NATS
// Core/Adjacency KV — the same harness clinic-reminders/lease-signing use.
//
// What decides "expired" is a recorded FACT, not a clock: the instant the @at
// this lens armed actually fired, recorded on the task under this target's own
// byTarget key in the freshnessExpiry marker. No $now is supplied to the two
// converted lenses' vectors — their cyphers reference none, and passing one
// would let a clock-reading regression pass unnoticed. (myTasks and
// capabilityEphemeral, exercised further down, still read the clock and still
// take an injected one.)
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

// The injected projection instant for every case below.
const unrNow = "2026-06-30T12:00:00Z"

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
// capabilityEphemeralSpec) with an INJECTED $now, mirroring projectAt above
// for the task-anchored unroutedTasksSpec.
func (f *unrFixture) projectIdentitySpec(t *testing.T, spec, identityName, now string) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "spec must parse on the full engine")
	actorKey := "vtx.identity." + f.ids[identityName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    actorKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "the identity anchor always yields exactly one row")
	return out[0].Values
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

	v := f.projectIdentitySpec(t, myTasksSpec, "bob", unrNow)
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
// once status has moved off 'open'.
func TestCapabilityEphemeral_CompletedTask_NotProjected(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "bob", "identity", nil)
	f.vtx(t, "task1", "task", map[string]any{"status": "complete", "expiresAt": "2026-09-05T12:00:00Z"})
	f.edge(t, "assignedTo", "task1", "bob")

	v := f.projectIdentitySpec(t, capabilityEphemeralSpec, "bob", unrNow)
	grants, _ := v["ephemeralGrants"].([]any)
	for _, row := range grants {
		m, _ := row.(map[string]any)
		require.NotEqual(t, "vtx.task."+f.ids["task1"], m["taskKey"],
			"a completed task's grant must not survive its own completion")
	}
}

// TestCapabilityEphemeral_OpenAndLive_StillProjected is the happy-path
// sanity check: an open, not-yet-expired direct assignment still grants.
func TestCapabilityEphemeral_OpenAndLive_StillProjected(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newUnrFixture(t)
	f.vtx(t, "bob", "identity", nil)
	f.vtx(t, "task1", "task", map[string]any{"status": "open", "expiresAt": "2026-07-01T12:00:00Z"})
	f.edge(t, "assignedTo", "task1", "bob")

	v := f.projectIdentitySpec(t, capabilityEphemeralSpec, "bob", unrNow)
	grants, _ := v["ephemeralGrants"].([]any)
	var found bool
	for _, row := range grants {
		m, _ := row.(map[string]any)
		if m["taskKey"] == "vtx.task."+f.ids["task1"] {
			found = true
		}
	}
	require.True(t, found, "an open, not-yet-expired direct assignment must still grant")
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
// that returns $now or $projectedAt projects a clock reading the sweep's deep
// verify cannot compare, which is the divergence this conversion removes.
// capabilityEphemeral and myTasks are deliberately NOT here — they still read the
// clock, for reasons their own doc comments carry.
func TestTaskDeadlineLenses_ReferenceNoClockParameter(t *testing.T) {
	for _, tc := range []struct{ name, spec string }{
		{"unroutedTasks", unroutedTasksSpec},
		{"staleAssignedTasks", staleAssignedTasksSpec},
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
