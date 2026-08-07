package pipeline

// The data half of the pattern-directed affected-anchor derivation
// (auth-plane-projection-latency-design.md §4.7), driven against a real
// adjacency graph and measured against the ActorEnumerator BFS it is destined
// to replace. Every case states which of the two answers is expected to be
// SMALLER and why — a derivation that merely agrees with the BFS everywhere
// would be sound and worthless.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// derivationPipeline builds an actor-aware pipeline over spec, sharing the
// ActorEnumerator fixtures' adjacency bucket.
func derivationPipeline(t *testing.T, adjKV *substrate.KV, spec string) *Pipeline {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	p := &Pipeline{ruleID: "testLens", adjKV: adjKV}
	p.SetActorEnumerator(NewActorEnumerator(adjKV, nil, "identity"))
	p.UseFullEngine(eng, cr)
	return p
}

// rolesSpec is the shipped capabilityRoles pattern (packages/rbac-domain).
const rolesSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)<-[:grantedBy]-(perm:permission)
RETURN identity.key AS actorKey, role.key AS r, perm.key AS p
`

// rolesFixture wires three identities onto one role, with one permission
// granted by it — the co-holder shape the whole increment exists to stop
// paying for.
func rolesFixture(t *testing.T) (*Pipeline, *enumFixture, *substrate.KV) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	f.vertex("bob", "identity")
	f.vertex("carol", "identity")
	f.vertex("admin", "role")
	f.vertex("perm1", "permission")
	// Direction follows the cypher: identity -holdsRole-> role,
	// permission -grantedBy-> role.
	f.edge("holdsRole", "alice", "admin")
	f.edge("holdsRole", "bob", "admin")
	f.edge("holdsRole", "carol", "admin")
	f.edge("grantedBy", "perm1", "admin")
	return derivationPipeline(t, adjKV, rolesSpec), f, adjKV
}

func (f *enumFixture) key(name, vtype string) string {
	return substrate.VertexKey(vtype, f.idByName[name])
}

func (f *enumFixture) link(rel, fromName, fromType, toName, toType string) string {
	return "lnk." + fromType + "." + f.idByName[fromName] + "." + rel + "." + toType + "." + f.idByName[toName]
}

// TestDeriveAnchors_HoldsRoleCostsOneActor is the headline of Increment 3. A
// grant to ONE identity is derived as that one identity, while the BFS — which
// cannot tell the changed edge from any other edge on the role — returns every
// co-holder and charges a cypher execution and a Capability-KV write for each.
func TestDeriveAnchors_HoldsRoleCostsOneActor(t *testing.T) {
	p, f, _ := rolesFixture(t)
	ctx := context.Background()
	rs := p.ruleState()
	linkKey := f.link("holdsRole", "alice", "identity", "admin", "role")

	derived, ok, err := p.deriveAnchorsForLink(ctx, rs, linkKey)
	require.NoError(t, err)
	require.True(t, ok, "the shipped capabilityRoles pattern must be derivable")
	require.ElementsMatch(t, []string{f.key("alice", "identity")}, derived)

	// The comparison that makes the assertion above a narrowing proof rather
	// than a fixture artifact: the shipped BFS really does return all three.
	bfs, err := p.actorEnumerator.Enumerate(ctx, f.key("admin", "role"), "role")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		f.key("alice", "identity"), f.key("bob", "identity"), f.key("carol", "identity"),
	}, bfs, "the BFS from the role endpoint reaches every co-holder")
}

// TestDeriveAnchors_GrantedByReachesEveryHolder is the other half of §4.7's
// worked example, and the reason "narrower" is not the derivation's goal: a
// permission newly granted to a role really does change every holder's
// projection, so here the derived set MUST match the BFS.
func TestDeriveAnchors_GrantedByReachesEveryHolder(t *testing.T) {
	p, f, _ := rolesFixture(t)
	derived, ok, err := p.deriveAnchorsForLink(context.Background(), p.ruleState(),
		f.link("grantedBy", "perm1", "permission", "admin", "role"))
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, []string{
		f.key("alice", "identity"), f.key("bob", "identity"), f.key("carol", "identity"),
	}, derived, "a permission granted to a role changes every holder — correct and necessary")
}

// TestDeriveAnchors_UntraversedRelationDerivesEmpty is the skip §4.7 licenses.
// The set is empty and ok is TRUE: on a complete index, "this link binds no hop"
// is a real answer, not a refusal.
func TestDeriveAnchors_UntraversedRelationDerivesEmpty(t *testing.T) {
	p, f, _ := rolesFixture(t)
	f.vertex("booking", "booking")
	f.edge("bookedBy", "alice", "booking")

	derived, ok, err := p.deriveAnchorsForLink(context.Background(), p.ruleState(),
		f.link("bookedBy", "alice", "identity", "booking", "booking"))
	require.NoError(t, err)
	require.True(t, ok, "an indexable lens answers rather than refusing")
	require.Empty(t, derived, "capabilityRoles never traverses bookedBy, so no anchor's output can change")
}

// TestDeriveAnchors_VertexAndAspectEventsSeedTheSamePosition covers 3b: a role
// vertex mutation (and an aspect on it) binds the role position and back-walks
// to every holder.
func TestDeriveAnchors_VertexAndAspectEventsSeedTheSamePosition(t *testing.T) {
	p, f, _ := rolesFixture(t)
	ctx := context.Background()
	rs := p.ruleState()
	want := []string{f.key("alice", "identity"), f.key("bob", "identity"), f.key("carol", "identity")}

	derived, ok, err := p.deriveAnchorsForVertex(ctx, rs, f.key("admin", "role"), "role")
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, want, derived)

	derived, ok, err = p.deriveAnchorsForAspect(ctx, rs, f.key("admin", "role")+".description")
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, want, derived, "an aspect event seeds its PARENT vertex's position")
}

// TestDeriveAnchors_AnchorVertexEventIsItself pins the fast path: an event on
// an identity derives that identity and reads no adjacency at all.
func TestDeriveAnchors_AnchorVertexEventIsItself(t *testing.T) {
	p, f, _ := rolesFixture(t)
	derived, ok, err := p.deriveAnchorsForVertex(context.Background(), p.ruleState(),
		f.key("alice", "identity"), "identity")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{f.key("alice", "identity")}, derived)
}

// TestDeriveAnchors_IncompleteIndexRefuses is the fallback contract. A lens the
// index cannot answer for must return ok == false — NOT an empty set, which the
// caller would read as a licensed skip.
func TestDeriveAnchors_IncompleteIndexRefuses(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	f.vertex("l1", "location")
	f.edge("residesIn", "alice", "l1")

	// The shipped capabilityServiceAccess shape: containedIn*0.. makes the
	// index incomplete, so every arm falls back.
	p := derivationPipeline(t, adjKV, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0)-[:containedIn*0..]->(loc)<-[:availableAt]-(svc:service)
RETURN identity.key AS actorKey, svc.key AS s
`)
	rs := p.ruleState()
	require.False(t, rs.anchorHops.Complete)

	_, ok, err := p.deriveAnchorsForVertex(context.Background(), rs, f.key("l1", "location"), "location")
	require.NoError(t, err)
	require.False(t, ok, "an incomplete index must refuse, never answer empty")

	_, ok, err = p.deriveAnchorsForLink(context.Background(), rs,
		f.link("residesIn", "alice", "identity", "l1", "location"))
	require.NoError(t, err)
	require.False(t, ok)
}

// TestDeriveAnchors_TypelessEdgeIsKept pins the pruning rule's direction. An
// adjacency entry with no OtherType cannot confirm the pattern's label, and
// "cannot confirm" must widen the derived set, never narrow it.
func TestDeriveAnchors_TypelessEdgeIsKept(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	f.vertex("admin", "role")
	ctx := context.Background()
	// A legacy edge event carrying no OtherType, written in both directions.
	for _, e := range []adjacency.CoreKVEvent{
		{CoreKvKey: "legacy", EdgeID: "legacy", Name: "holdsRole", Direction: "outbound",
			NodeID: f.idByName["alice"], OtherNodeID: f.idByName["admin"]},
		{CoreKvKey: "legacy", EdgeID: "legacy", Name: "holdsRole", Direction: "inbound",
			NodeID: f.idByName["admin"], OtherNodeID: f.idByName["alice"]},
	} {
		require.NoError(t, adjacency.Build(ctx, adjKV, e))
	}

	p := derivationPipeline(t, adjKV, rolesSpec)
	derived, ok, err := p.deriveAnchorsForVertex(ctx, p.ruleState(), f.key("admin", "role"), "role")
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t, []string{f.key("alice", "identity")}, derived,
		"an edge whose far-end type is unknown is KEPT — the label cannot rule it out")
}

// TestShadow_CountsNarrowingWithoutChangingTheAnswer is the increment's whole
// posture in one test: the shadow observes, and the pipeline still acts on the
// enumerator's set.
func TestShadow_CountsNarrowingWithoutChangingTheAnswer(t *testing.T) {
	p, f, _ := rolesFixture(t)
	p.SetAnchorDerivationSampling(1)
	ctx := context.Background()
	rs := p.ruleState()

	bfs, err := p.actorEnumerator.Enumerate(ctx, f.key("admin", "role"), "role")
	require.NoError(t, err)
	require.Len(t, bfs, 3)

	p.shadowAnchorDerivation(rs, f.link("holdsRole", "alice", "identity", "admin", "role"), bfs,
		func() ([]string, bool, error) {
			return p.deriveAnchorsForLink(ctx, rs, f.link("holdsRole", "alice", "identity", "admin", "role"))
		})

	st := p.AnchorDerivationShadow()
	require.Equal(t, int64(1), st.Sampled)
	require.Equal(t, int64(1), st.NarrowedEvents)
	require.Equal(t, int64(2), st.NarrowedAnchors, "two co-holders the derivation would not reproject")
	require.Zero(t, st.DivergentEvents, "the derived set must stay inside the enumerator's")
	require.Zero(t, st.Declined)
	require.Equal(t, int64(3), st.BFSAnchors)
	require.Equal(t, int64(1), st.DerivedAnchors)
}

// TestShadow_SamplingAndDeclineAreBothCounted keeps the tally honest: a lens the
// derivation refuses is still a sampled event, so a board of all-zero counters
// cannot be mistaken for agreement.
func TestShadow_SamplingAndDeclineAreBothCounted(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	f := newEnumFixture(t, adjKV)
	f.vertex("alice", "identity")
	p := derivationPipeline(t, adjKV, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(l0)-[:containedIn*0..]->(l)
RETURN identity.key AS actorKey, l.key AS lk
`)
	p.SetAnchorDerivationSampling(1)
	p.shadowAnchorDerivation(p.ruleState(), "vtx.location.x", nil, func() ([]string, bool, error) {
		t.Fatal("an incomplete index must be refused before the walk is attempted")
		return nil, false, nil
	})
	st := p.AnchorDerivationShadow()
	require.Equal(t, int64(1), st.Sampled)
	require.Equal(t, int64(1), st.Declined)

	// Sampling off means no observation at all, not a zero-valued one.
	p.SetAnchorDerivationSampling(-1)
	p.shadowAnchorDerivation(p.ruleState(), "vtx.location.x", nil, func() ([]string, bool, error) {
		return nil, true, nil
	})
	require.Equal(t, int64(1), p.AnchorDerivationShadow().Sampled)
}

// TestDeriveAnchors_ReadCapFallsBackRatherThanTruncating is the unit's own
// stated safety net, and it guards the one mutation that would silently break
// the superset invariant: returning the partial set with ok == true. The cap is
// lowered rather than the graph enlarged, so the test states the property
// instead of approximating it with thousands of vertices.
//
// Seeded at the role, the walk reads the role's edges (reaching all three
// holders AND the permission), then the permission's — two documents. So two is
// exactly enough and one is exactly not, which is what makes the refusal below
// attributable to the budget rather than to the graph.
func TestDeriveAnchors_ReadCapFallsBackRatherThanTruncating(t *testing.T) {
	p, f, _ := rolesFixture(t)
	ctx := context.Background()
	rs := p.ruleState()

	p.SetAnchorDerivationReadCap(2)
	derived, ok, err := p.deriveAnchorsForVertex(ctx, rs, f.key("admin", "role"), "role")
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, derived, 3, "two reads are enough for this walk")

	p.SetAnchorDerivationReadCap(1)
	derived, ok, err = p.deriveAnchorsForVertex(ctx, rs, f.key("admin", "role"), "role")
	require.NoError(t, err)
	require.False(t, ok, "an exhausted budget must fall back, never return the partial set")
	require.Nil(t, derived, "a refusal carries no anchors at all — not the three it had already found")
}

// TestDeriveAnchors_ReadCapZeroMeansUnset pins the two knobs' zero-value
// semantics, which are the opposite of each other and easy to get wrong: a
// non-positive READ CAP restores the default budget, while a NEGATIVE sampling
// rate switches the shadow off and zero leaves it at the default.
func TestDeriveAnchors_ReadCapZeroMeansUnset(t *testing.T) {
	p, _, _ := rolesFixture(t)
	require.Equal(t, DefaultDerivationReadCap, p.derivationReadCap())
	p.SetAnchorDerivationReadCap(-1)
	require.Equal(t, DefaultDerivationReadCap, p.derivationReadCap())
	p.SetAnchorDerivationReadCap(0)
	require.Equal(t, DefaultDerivationReadCap, p.derivationReadCap())

	// Sampling 0 is UNSET: it restores the default 1-in-N rate rather than
	// disabling, because 0 is the atomic's own zero value and cannot mean two
	// things. Only a negative rate switches the shadow off.
	p.SetAnchorDerivationSampling(0)
	sampled := 0
	for i := 0; i < defaultDerivationShadowSampling; i++ {
		if p.derivShadow.shouldSample() {
			sampled++
		}
	}
	require.Equal(t, 1, sampled, "one event in %d, not zero", defaultDerivationShadowSampling)

	p.SetAnchorDerivationSampling(-1)
	for i := 0; i < defaultDerivationShadowSampling*2; i++ {
		require.False(t, p.derivShadow.shouldSample(), "a negative rate is the off switch")
	}
}

// TestAnchorHops_ReloadLifetime mirrors the seedAnchorLabel reload tests: the
// graph is republished on every rule swap, so a lens edited down to multiple
// walks disarms the derivation and one edited back re-arms it. A stale graph
// would direct the walk under a pattern the lens no longer has.
func TestAnchorHops_ReloadLifetime(t *testing.T) {
	adjKV := newActorEnumeratorAdjKV(t)
	p := derivationPipeline(t, adjKV, rolesSpec)
	require.True(t, p.ruleState().anchorHops.Complete)

	eng := full.New()
	one, err := eng.Parse(rolesSpec)
	require.NoError(t, err)
	two, err := eng.Parse(`MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role) RETURN i.key AS actorKey, r.key AS rk`)
	require.NoError(t, err)

	p.UseFullEngineBranches(eng, one, []ruleengine.CompiledRule{one, two})
	require.False(t, p.ruleState().anchorHops.Complete,
		"a multi-walk lens carries one anchor per branch, so no single graph may speak for it")

	p.UseFullEngine(eng, one)
	require.True(t, p.ruleState().anchorHops.Complete, "a reload back to one walk re-arms the derivation")
}
