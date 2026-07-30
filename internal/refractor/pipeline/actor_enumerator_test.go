package pipeline

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// newActorEnumeratorAdjKV stands up an in-memory NATS server with an empty
// adjacency bucket for ActorEnumerator tests.
func newActorEnumeratorAdjKV(t *testing.T) *substrate.KV {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	_, nc := natsfixture.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "ADJ"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "ADJ")
	require.NoError(t, err)
	return adjKV
}

// enumFixture is a small named-vertex registry so tests can wire edges by
// logical name (mirrors ruleengine/full's fixtureRegistry, kept local here
// to avoid a cross-package test dependency).
type enumFixture struct {
	t        *testing.T
	adjKV    *substrate.KV
	idByName map[string]string
	typeByID map[string]string
}

func newEnumFixture(t *testing.T, adjKV *substrate.KV) *enumFixture {
	return &enumFixture{t: t, adjKV: adjKV, idByName: map[string]string{}, typeByID: map[string]string{}}
}

// vertex registers a logical name for a freshly generated NanoID of the
// given Contract #1 type and returns its full vertex key.
func (f *enumFixture) vertex(name, vtype string) string {
	f.t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(f.t, err)
	f.idByName[name] = id
	f.typeByID[id] = vtype
	return substrate.VertexKey(vtype, id)
}

// edge writes both adjacency directions for a named link between two
// registered vertices, mirroring evaluateLinkFanOut's own idempotent build.
func (f *enumFixture) edge(name, fromName, toName string) {
	f.t.Helper()
	ctx := context.Background()
	fromID, toID := f.idByName[fromName], f.idByName[toName]
	require.NotEmpty(f.t, fromID, "fixture: %q not registered", fromName)
	require.NotEmpty(f.t, toID, "fixture: %q not registered", toName)
	fromType, toType := f.typeByID[fromID], f.typeByID[toID]
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(f.t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID,
		EdgeID:    edgeID, Name: name,
		Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType,
	}))
	require.NoError(f.t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID,
		EdgeID:    edgeID, Name: name,
		Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType,
	}))
}

// TestActorEnumerator_ReportsToContinuesToManager pins the fix: a mutation
// reaching the report (via a non-hierarchy edge, e.g. assignedTo) must also
// reach the manager the report reportsTo, since capabilityEphemeral's 2-hop
// branch means the manager's own projection depends on the report's tasks.
func TestActorEnumerator_ReportsToContinuesToManager(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)

	task := f.vertex("task", "task")
	f.vertex("report", "identity")
	f.vertex("manager", "identity")
	f.edge("assignedTo", "report", "task")
	f.edge("reportsTo", "report", "manager")

	enum := NewActorEnumerator(adjKV, nil, "identity")
	actors, err := enum.Enumerate(context.Background(), task, "task")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		substrate.VertexKey("identity", f.idByName["report"]),
		substrate.VertexKey("identity", f.idByName["manager"]),
	}, actors)
}

// TestActorEnumerator_StopsAtActorWithoutHierarchyEdge is the pre-existing
// (still correct) behavior: with no reportsTo edge at all, only the
// directly-reached actor comes back.
func TestActorEnumerator_StopsAtActorWithoutHierarchyEdge(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)

	task := f.vertex("task", "task")
	f.vertex("report", "identity")
	f.edge("assignedTo", "report", "task")

	enum := NewActorEnumerator(adjKV, nil, "identity")
	actors, err := enum.Enumerate(context.Background(), task, "task")
	require.NoError(t, err)
	require.Equal(t, []string{substrate.VertexKey("identity", f.idByName["report"])}, actors)
}

// TestActorEnumerator_DoesNotFanOutViaSharedNonHierarchyNeighbor guards the
// original concern the no-traverse-through-actors rule was written against:
// two actors sharing an unrelated neighbor (e.g. a location) must NOT pull
// each other in. Only the reportsTo relation continues the walk past an
// actor.
func TestActorEnumerator_DoesNotFanOutViaSharedNonHierarchyNeighbor(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)

	task := f.vertex("task", "task")
	f.vertex("report", "identity")
	f.vertex("colleague", "identity")
	loc := f.vertex("loc", "location")
	f.edge("assignedTo", "report", "task")
	f.edge("locatedAt", "report", "loc")
	f.edge("locatedAt", "colleague", "loc")

	enum := NewActorEnumerator(adjKV, nil, "identity")
	actors, err := enum.Enumerate(context.Background(), task, "task")
	require.NoError(t, err)
	require.Equal(t, []string{substrate.VertexKey("identity", f.idByName["report"])}, actors)
	_ = loc
}

// TestActorEnumerator_ReportsToHopIsNotTransitive confirms the hierarchy
// hop is exactly one fixed step, matching capabilityEphemeral's cypher
// (which has no variable-length reportsTo* — only one 2-hop branch): a
// report's OWN manager (director, here) must never be pulled in on a
// report's task mutation, even with the default (generous) depth cap.
func TestActorEnumerator_ReportsToHopIsNotTransitive(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)

	task := f.vertex("task", "task")
	f.vertex("report", "identity")
	f.vertex("manager", "identity")
	f.vertex("director", "identity")
	f.edge("assignedTo", "report", "task")
	f.edge("reportsTo", "report", "manager")
	f.edge("reportsTo", "manager", "director")

	enum := NewActorEnumerator(adjKV, nil, "identity")
	actors, err := enum.Enumerate(context.Background(), task, "task")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		substrate.VertexKey("identity", f.idByName["report"]),
		substrate.VertexKey("identity", f.idByName["manager"]),
	}, actors)
}

// TestActorEnumerator_ReportsToHopIsDirectional confirms the hop only
// follows the outbound (subordinate→manager) direction: a manager's own
// report set must never be pulled in when the manager itself is the actor
// directly reached (a colleague reportsTo the same manager must not
// appear).
func TestActorEnumerator_ReportsToHopIsDirectional(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)

	task := f.vertex("task", "task")
	f.vertex("manager", "identity")
	f.vertex("colleague", "identity")
	f.edge("assignedTo", "manager", "task")
	f.edge("reportsTo", "colleague", "manager")

	enum := NewActorEnumerator(adjKV, nil, "identity")
	actors, err := enum.Enumerate(context.Background(), task, "task")
	require.NoError(t, err)
	require.Equal(t, []string{substrate.VertexKey("identity", f.idByName["manager"])}, actors)
}

// TestActorEnumerator_ReportsToHopAppliesToDirectAssignment shows the fix's
// generality: a task assigned DIRECTLY to a manager (no intervening report)
// still reaches the manager's own manager, since capabilityEphemeral's
// 2-hop branch binds identity=grand-manager, report=manager on manager's
// own direct assignments too — the hop applies to whichever actor is
// found, not only ones reached via a non-hierarchy edge first.
func TestActorEnumerator_ReportsToHopAppliesToDirectAssignment(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)

	task := f.vertex("task", "task")
	f.vertex("manager", "identity")
	f.vertex("grandManager", "identity")
	f.edge("assignedTo", "manager", "task")
	f.edge("reportsTo", "manager", "grandManager")

	enum := NewActorEnumerator(adjKV, nil, "identity")
	actors, err := enum.Enumerate(context.Background(), task, "task")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		substrate.VertexKey("identity", f.idByName["manager"]),
		substrate.VertexKey("identity", f.idByName["grandManager"]),
	}, actors)
}

// TestActorEnumerator_ReportsToHopRespectsActorCap confirms the hierarchy
// hop still honors maxActors — it is a normal addActor call, not a
// cap-bypassing side channel.
func TestActorEnumerator_ReportsToHopRespectsActorCap(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)

	task := f.vertex("task", "task")
	f.vertex("report", "identity")
	f.vertex("manager", "identity")
	f.edge("assignedTo", "report", "task")
	f.edge("reportsTo", "report", "manager")

	enum := NewActorEnumerator(adjKV, nil, "identity").WithCaps(0, 1)
	actors, err := enum.Enumerate(context.Background(), task, "task")
	require.NoError(t, err)
	require.Len(t, actors, 1)
}
