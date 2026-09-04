package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// newActorEnumeratorAdjKV stands up an in-memory NATS server with an empty
// adjacency bucket for ActorEnumerator tests.
func newActorEnumeratorAdjKV(t *testing.T) *substrate.KV {
	t.Helper()
	return newTestKVs(t, "ADJ")[0]
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

// TestActorEnumerator_ActorTypeEventReachesTheAnchorAbove pins the union: an
// event on a vertex of the actor type must still reach the anchors that bind
// that vertex at a NON-anchor position. `capabilityEphemeral`'s
// (identity)<-[:reportsTo]-(report:identity) is exactly such a position, so a
// mutation on the report has to reproject the manager as well. The one-key
// answer returns the report alone, which on the auth plane is a stale grant.
func TestActorEnumerator_ActorTypeEventReachesTheAnchorAbove(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)

	report := f.vertex("report", "identity")
	f.vertex("manager", "identity")
	f.edge("reportsTo", "report", "manager")

	enum := NewActorEnumerator(adjKV, nil, "identity")
	actors, err := enum.Enumerate(context.Background(), report, "identity")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		substrate.VertexKey("identity", f.idByName["report"]),
		substrate.VertexKey("identity", f.idByName["manager"]),
	}, actors, "the manager binds the changed identity at a non-anchor position")
}

// TestActorEnumerator_ActorTypeEventReturnsItsOwnVertexExactlyOnce is the
// no-regression half: the changed actor is still in the answer, and the walk
// re-reaching it — here through the hierarchy hop off a subordinate that
// reportsTo it — does not put it there twice.
func TestActorEnumerator_ActorTypeEventReturnsItsOwnVertexExactlyOnce(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)

	manager := f.vertex("manager", "identity")
	f.vertex("report", "identity")
	f.edge("reportsTo", "report", "manager")

	enum := NewActorEnumerator(adjKV, nil, "identity")
	actors, err := enum.Enumerate(context.Background(), manager, "identity")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		substrate.VertexKey("identity", f.idByName["manager"]),
		substrate.VertexKey("identity", f.idByName["report"]),
	}, actors)
	seen := 0
	for _, a := range actors {
		if a == manager {
			seen++
		}
	}
	require.Equal(t, 1, seen, "the event's own actor appears once, not once per path that reaches it")
}

// TestActorEnumerator_ReportsToHopRespectsActorCap confirms the hierarchy
// hop still reaches maxActors — it is a normal addActor call, not a
// cap-bypassing side channel — and that reaching it now REFUSES. A truncated
// answer here is a silent subset of the affected anchors, indistinguishable from
// a complete one; walkToAnchors refuses the identical hazard on its own read cap.
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
	require.ErrorIs(t, err, ErrActorSetTooWide)
	require.Nil(t, actors, "a refusal returns no set at all — a short one would read as complete")
	require.Equal(t, failure.CatStructural, failure.Classify(err),
		"the event stays pending and the lens pauses; it is never acked on a subset")
}

// TestActorEnumerator_UnderTheCapStillAnswers is the positive vector the refusal
// above needs: the same two-actor graph one seat below the cap returns both,
// so the test above is pinning the cap and not merely a broken enumerator.
func TestActorEnumerator_UnderTheCapStillAnswers(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)

	task := f.vertex("task", "task")
	f.vertex("report", "identity")
	f.vertex("manager", "identity")
	f.edge("assignedTo", "report", "task")
	f.edge("reportsTo", "report", "manager")

	enum := NewActorEnumerator(adjKV, nil, "identity").WithCaps(0, 2)
	actors, err := enum.Enumerate(context.Background(), task, "task")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		substrate.VertexKey("identity", f.idByName["report"]),
		substrate.VertexKey("identity", f.idByName["manager"]),
	}, actors)
}

// reportsToSpec binds the actor type at a NON-anchor position: `report` is an
// identity whose data the anchor's own row renders. It is `capabilityEphemeral`'s
// reportsTo 2-hop reduced to the one position that makes the point.
const reportsToSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:reportsTo]-(report:identity)
RETURN identity.key AS actorKey, report.data.name AS reportName
`

// untypedRolesSpec is rolesSpec with its one hop left untyped —
// `objectAttachments`' relation dimension, with both ends labeled.
// AnchorHopIndex records that hop as a WILDCARD, so the index is complete and
// every consumer reads the empty relation name as admit-any.
const untypedRolesSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[r]->(role:role)
RETURN identity.key AS actorKey, role.key AS rk
`

// unindexableRolesSpec is rolesSpec on a conjunct the completeness predicate
// still declines: a ranged hop whose LOWER bound exceeds one hop, which
// AnchorSideSeeds cannot seed without dropping an anchor. It is the vector for
// every case that needs an index refusing at the CYPHER level — nothing about
// the pattern's positions is knowable and the one-key answer cannot be proven.
// The vector's job is the refusal, not the conjunct: a WITH that keeps its
// variables and an untyped hop both index completely, so neither can serve it.
const unindexableRolesSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole*2..3]->(role:role)
RETURN identity.key AS actorKey, role.key AS rk
`

// TestEnumerateAnchors_SingleActorPositionKeepsTheOneKeyAnswer is the narrowing
// the union must not cost. capabilityRoles binds `identity` at the anchor and
// nowhere else, so a mutation on one holder cannot move a co-holder's row — and
// the control below shows the walk really would have returned all three.
func TestEnumerateAnchors_SingleActorPositionKeepsTheOneKeyAnswer(t *testing.T) {
	p, f, _ := rolesFixture(t)
	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})
	ctx := context.Background()
	rs := p.ruleState()
	alice := f.key("alice", "identity")

	require.True(t, p.oneKeyAnswerSound(rs),
		"capabilityRoles binds the actor type only at the anchor, and a sweep stands behind the narrowing")

	anchors, err := p.enumerateAnchors(ctx, rs, alice, "identity")
	require.NoError(t, err)
	require.Equal(t, []string{alice}, anchors)

	walked, err := p.actorEnumerator.Enumerate(ctx, alice, "identity")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		alice, f.key("bob", "identity"), f.key("carol", "identity"),
	}, walked, "the walk reaches every co-holder — which is what the proof lets this lens skip")
}

// TestEnumerateAnchors_NonAnchorActorPositionUnionsTheWalk is the correctness
// gap 4a-2 closes: the actor type binds at two positions, so an event on the
// report has to carry the manager with it.
func TestEnumerateAnchors_NonAnchorActorPositionUnionsTheWalk(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	report := f.vertex("report", "identity")
	f.vertex("manager", "identity")
	f.edge("reportsTo", "report", "manager")

	p := derivationPipeline(t, adjKV, reportsToSpec)
	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})
	rs := p.ruleState()

	require.True(t, ActorTypeBindsAnchorOnly(p.ruleState().anchorHops, "identity") == false,
		"`report` is a second position binding the actor type")
	require.False(t, p.oneKeyAnswerSound(rs),
		"and so the one-key answer is unsound even with a healer installed")

	anchors, err := p.enumerateAnchors(context.Background(), rs, report, "identity")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{report, f.key("manager", "identity")}, anchors)
}

// TestEnumerateAnchors_IncompleteIndexUnionsTheWalk fails the proof closed on its
// first input: a refused index never indexed the positions the count reads, so
// its answer is a floor and licenses nothing.
func TestEnumerateAnchors_IncompleteIndexUnionsTheWalk(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	alice := f.vertex("alice", "identity")
	f.vertex("bob", "identity")
	f.vertex("admin", "role")
	f.edge("holdsRole", "alice", "admin")
	f.edge("holdsRole", "bob", "admin")

	p := derivationPipeline(t, adjKV, unindexableRolesSpec)
	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})
	rs := p.ruleState()
	require.False(t, rs.anchorHops.Complete)
	require.Contains(t, rs.anchorHops.Incomplete, "lower bound exceeds one hop")
	require.False(t, p.oneKeyAnswerSound(rs))

	anchors, err := p.enumerateAnchors(context.Background(), rs, alice, "identity")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{alice, f.key("bob", "identity")}, anchors)
}

// TestEnumerateAnchors_TheHealerIsTheConjunctNotPatternClosure pins both halves
// of the adjudication.
//
// Pattern closure does NOT decide it: a lens carrying inputs outside its pattern
// still cannot have anchor Y's row moved by actor X's vertex, because every
// out-of-pattern input the tree carries is keyed on the evaluating actor.
//
// The standing healer DOES: answering with one key stops reprojecting peers the
// walk used to reach incidentally, and that accident is the only thing that
// converges a row a Capability-KV grant flip left stale. So the same pipeline —
// pattern-eligible, patternClosedOutput false either way — flips on the sweep
// plan alone. That is what holds the personal corpus on the walk, since
// projection/driver.go:417-421 gives it no plan.
func TestEnumerateAnchors_TheHealerIsTheConjunctNotPatternClosure(t *testing.T) {
	p, f, _ := rolesFixture(t)
	rs := p.ruleState()
	alice := f.key("alice", "identity")
	ctx := context.Background()

	require.False(t, p.PatternClosedOutput(), "the flag is off on both sides of this test")
	require.True(t, ActorTypeBindsAnchorOnly(rs.anchorHops, "identity"),
		"pattern-eligible: the actor type binds only at the anchor")

	require.False(t, p.oneKeyAnswerSound(rs),
		"no sweep plan — the lens must keep the accidental heal")
	walked, err := p.enumerateAnchors(ctx, rs, alice, "identity")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		alice, f.key("bob", "identity"), f.key("carol", "identity"),
	}, walked)

	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})
	require.True(t, p.oneKeyAnswerSound(p.ruleState()),
		"pattern closure never changed; the healer is what moved the verdict")
	narrowed, err := p.enumerateAnchors(ctx, p.ruleState(), alice, "identity")
	require.NoError(t, err)
	require.Equal(t, []string{alice}, narrowed)
}

// TestEnumerateAnchors_KnobOffRestoresTheOneKeyAnswer pins the operator's way
// back. The widening's cost is the co-holder population, bounded by nothing the
// design fixes, and REFRACTOR_ANCHOR_DERIVATION=off does not reach it — that
// knob routes to the enumerator, which is the walking arm.
func TestEnumerateAnchors_KnobOffRestoresTheOneKeyAnswer(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	report := f.vertex("report", "identity")
	f.vertex("manager", "identity")
	f.edge("reportsTo", "report", "manager")

	p := derivationPipeline(t, adjKV, reportsToSpec)
	rs := p.ruleState()
	ctx := context.Background()

	widened, err := p.enumerateAnchors(ctx, rs, report, "identity")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{report, f.key("manager", "identity")}, widened)

	p.SetActorPeerAnchorMode(PeerAnchorModeOff)
	narrowed, err := p.enumerateAnchors(ctx, rs, report, "identity")
	require.NoError(t, err)
	require.Equal(t, []string{report}, narrowed,
		"the knob reinstates the prior under-approximation — a containment lever, not a posture")
}
