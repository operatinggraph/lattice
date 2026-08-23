package pipeline

// The flip itself (auth-plane-projection-latency-design.md §17): the three
// actor-aware fan-out arms acting on the pattern-directed derivation instead of
// the ActorEnumerator BFS, and the two conjuncts that decide where they may.
//
// §9's e2e (a), tightened to what the pattern-directed derivation makes true:
// AssignRole on actor U projects U's grant and leaves every co-holder's row
// UNTOUCHED. The BFS reprojects every co-holder on every grant — identically,
// so nothing observable is wrong, which is exactly what makes that cost
// invisible. The assertion here is on the writes, so a regression to the BFS's
// breadth fails even though every row it writes is correct.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// coHolderFixture is the shape the whole increment exists for: three identities
// already holding one role, a fourth about to be granted it, and a recording
// adapter so "who was written" is the assertion rather than "what was written".
type coHolderFixture struct {
	p     *Pipeline
	adpt  *recordingAdapter
	ids   map[string]string
	types map[string]string
	t     *testing.T
	adjKV *substrate.KV
}

func newCoHolderFixture(t *testing.T) *coHolderFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	eng := full.New()
	cr, err := eng.Parse(evalDriftCypher)
	require.NoError(t, err)

	f := &coHolderFixture{
		t: t, adjKV: adjKV,
		ids: map[string]string{}, types: map[string]string{},
	}

	adpt := &recordingAdapter{}
	p := &Pipeline{
		ruleID:               "co-holder-flip",
		coreKVBucket:         "CORE",
		coreKV:               coreKV,
		adjKV:                adjKV,
		engineKind:           ruleengine.EngineFull,
		envelopeFn:           evalDriftEnvelopeFn,
		plainReprojectLabels: identityRoleLabels(),
		plainReprojectAll:    false,
		patternClosedOutput:  true,
		adpt:                 adpt,
	}
	p.UseFullEngine(eng, cr)
	p.SetActorEnumerator(NewActorEnumerator(adjKV, coreKV, "identity"))
	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})
	f.p, f.adpt = p, adpt

	for _, n := range []string{"alice", "bob", "carol", "dave"} {
		f.vertex(coreKV, n, "identity")
	}
	f.vertex(coreKV, "admin", "role")
	for _, n := range []string{"alice", "bob", "carol"} {
		f.edge("holdsRole", n, "admin")
	}
	return f
}

func (f *coHolderFixture) vertex(coreKV *substrate.KV, name, vtype string) string {
	f.t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(f.t, err)
	f.ids[name], f.types[id] = id, vtype
	key := substrate.VertexKey(vtype, id)
	writeCollisionVertex(f.t, coreKV, key, vtype, map[string]any{"name": name})
	return key
}

func (f *coHolderFixture) key(name string) string {
	return substrate.VertexKey(f.types[f.ids[name]], f.ids[name])
}

func (f *coHolderFixture) linkKey(rel, from, to string) string {
	fromID, toID := f.ids[from], f.ids[to]
	return "lnk." + f.types[fromID] + "." + fromID + "." + rel + "." + f.types[toID] + "." + toID
}

// edge seeds an adjacency edge under the SAME EdgeID the pipeline's own
// tombstone will carry — the Contract #1 link key, which is globally unique.
// buildCollisionEdge uses a different EdgeID shape, and adjacency removal is
// strictly by EdgeID, so a fixture seeded through it survives the very
// tombstone a revocation test is trying to land. That test would then pass on
// the derived set alone while the projected row silently kept its old grant.
func (f *coHolderFixture) edge(rel, from, to string) {
	f.t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[from], f.ids[to]
	fromType, toType := f.types[fromID], f.types[toID]
	linkKey := f.linkKey(rel, from, to)
	for _, evt := range []adjacency.CoreKVEvent{
		{CoreKvKey: linkKey, EdgeID: linkKey, Name: rel, Direction: "outbound",
			NodeID: fromID, OtherNodeID: toID, OtherType: toType},
		{CoreKvKey: linkKey, EdgeID: linkKey, Name: rel, Direction: "inbound",
			NodeID: toID, OtherNodeID: fromID, OtherType: fromType},
	} {
		require.NoError(f.t, adjacency.Build(ctx, f.adjKV, evt))
	}
}

// rowFor returns the row last written for one identity, or nil if none was.
func (f *coHolderFixture) rowFor(name string) map[string]any {
	want := capKeyFor(f.key(name))
	for i := len(f.adpt.upserts) - 1; i >= 0; i-- {
		if k, _ := f.adpt.upserts[i].keys["key"].(string); k == want {
			return f.adpt.upserts[i].row
		}
	}
	return nil
}

// handleLink drives a link CDC event through the real message path, so the
// arm's own adjacency.Build, its choice of answer, and the reprojection all run
// exactly as they do in production.
func (f *coHolderFixture) handleLink(rel, from, to string, deleted bool, seq uint64) {
	f.t.Helper()
	linkKey := f.linkKey(rel, from, to)
	body, err := json.Marshal(map[string]any{"key": linkKey, "isDeleted": deleted})
	require.NoError(f.t, err)
	dec, err := f.p.handle(context.Background(), substrate.Message{
		Subject: "$KV.CORE." + linkKey, Body: body, Sequence: seq})
	require.NoError(f.t, err)
	require.Equal(f.t, substrate.Ack, dec)
}

// writtenActors renders the adapter's upserts as the set of output keys they
// touched — the co-holder question stated in the terms the target sees.
func (f *coHolderFixture) writtenActors() []string {
	out := make([]string, 0, len(f.adpt.upserts))
	for _, w := range f.adpt.upserts {
		k, _ := w.keys["key"].(string)
		out = append(out, k)
	}
	return out
}

func capKeyFor(vertexKey string) string {
	return "cap." + vertexKey[len("vtx."):]
}

// TestFlip_LinkGrant_ReprojectsOnlyTheGrantee is §9's e2e (a), tightened. One
// grant, one write — and the sibling assertion below proves the fixture really
// does contain co-holders the old path would have written.
func TestFlip_LinkGrant_ReprojectsOnlyTheGrantee(t *testing.T) {
	f := newCoHolderFixture(t)
	f.handleLink("holdsRole", "dave", "admin", false, 1)

	require.Equal(t, []string{capKeyFor(f.key("dave"))}, f.writtenActors(),
		"a grant to one identity must reproject that identity and nobody else")
	require.Empty(t, f.adpt.deletes)
}

// TestFlip_ShadowModeKeepsTheBFSBreadth is the control. The same fixture and the
// same event under `shadow` writes every co-holder, which is what the arms did
// before the flip. Without it, the assertion above could be satisfied by a
// fixture whose graph simply had no co-holders to reach.
func TestFlip_ShadowModeKeepsTheBFSBreadth(t *testing.T) {
	f := newCoHolderFixture(t)
	f.p.SetAnchorDerivationMode(DerivationModeShadow)

	f.handleLink("holdsRole", "dave", "admin", false, 1)

	require.ElementsMatch(t, []string{
		capKeyFor(f.key("alice")), capKeyFor(f.key("bob")),
		capKeyFor(f.key("carol")), capKeyFor(f.key("dave")),
	}, f.writtenActors(),
		"the enumerator reaches every co-holder through the role — the breadth the flip removes")
}

// TestFlip_LinkRevocationStillRetracts is the direction that matters. A
// narrowing that dropped a revocation would leave a grant live, so the
// revocation must reach the revoked identity's row on the derived path too.
func TestFlip_LinkRevocationStillRetracts(t *testing.T) {
	f := newCoHolderFixture(t)
	f.handleLink("holdsRole", "alice", "admin", true, 1)

	require.Contains(t, f.writtenActors(), capKeyFor(f.key("alice")),
		"the revoked identity's row must be recomputed on the very event that revoked it")
	// Naming the identity is not the assertion — the RETRACTION is. Asserting
	// only on membership of the derived set would pass just as happily if the
	// recomputed row still carried the grant.
	row := f.rowFor("alice")
	require.NotNil(t, row)
	require.Nil(t, row["roleName"],
		"the recomputed row must no longer carry the revoked role")

	for _, other := range []string{"bob", "carol"} {
		require.NotContains(t, f.writtenActors(), capKeyFor(f.key(other)),
			"a revocation from one identity is not news for a co-holder")
	}
}

// TestFlip_RoleVertexEventStillReachesEveryHolder is the anti-vacuity case for
// the node-seeded arm: the derivation is not "narrower everywhere". A change to
// the role itself changes every holder's row, and the derived set must say so.
func TestFlip_RoleVertexEventStillReachesEveryHolder(t *testing.T) {
	f := newCoHolderFixture(t)
	roleKey := f.key("admin")
	body, err := json.Marshal(map[string]any{
		"key": roleKey, "class": "role", "isDeleted": false,
		"createdAt": "2026-05-15T10:00:00Z", "lastModifiedAt": "2026-05-15T10:00:00Z",
		"data": map[string]any{"name": "admin-renamed"},
	})
	require.NoError(t, err)
	_, err = f.p.coreKV.Put(context.Background(), roleKey, body)
	require.NoError(t, err)

	dec, err := f.p.handle(context.Background(), substrate.Message{
		Subject: "$KV.CORE." + roleKey, Body: body, Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)

	require.ElementsMatch(t, []string{
		capKeyFor(f.key("alice")), capKeyFor(f.key("bob")), capKeyFor(f.key("carol")),
	}, f.writtenActors(), "renaming a role changes every holder's row — correct and necessary")
}

// TestFlip_ActRefusedWithoutSweepPlan and its sibling below pin §17.2's two
// conjuncts. Both are stated as behaviour of the fan-out, not of the predicate,
// so a refactor that keeps the predicate and stops consulting it still fails.
func TestFlip_ActRefusedWithoutSweepPlan(t *testing.T) {
	f := newCoHolderFixture(t)
	f.p.sweeper = nil

	_, ready := f.p.derivationIndexForAct(f.p.ruleState())
	require.False(t, ready, "no standing healer, no acting")

	f.handleLink("holdsRole", "dave", "admin", false, 1)
	require.Len(t, f.writtenActors(), 4,
		"a lens with no sweep plan keeps the enumerator's breadth, incidental heals included")
}

func TestFlip_ActRefusedWithoutPatternClosure(t *testing.T) {
	f := newCoHolderFixture(t)
	f.p.SetPatternClosedOutput(false)

	_, ready := f.p.derivationIndexForAct(f.p.ruleState())
	require.False(t, ready,
		"a row fed by an input outside the pattern can change with no pattern edge changing")

	// The shipped three-conjunct index is untouched: shadow mode must keep
	// observing exactly the lenses acting refuses.
	_, shadowReady := f.p.derivationIndex(f.p.ruleState())
	require.True(t, shadowReady)

	f.handleLink("holdsRole", "dave", "admin", false, 1)
	require.Len(t, f.writtenActors(), 4)
}

// TestFlip_TallyCountsActedAndFellBack proves the measurement has a reader on
// the acting path — the counters move, per lens, and distinguish the two
// outcomes. A lens falling back on every event is the failure mode the tally
// exists to make visible, and it looks identical to success without this.
func TestFlip_TallyCountsActedAndFellBack(t *testing.T) {
	f := newCoHolderFixture(t)
	f.handleLink("holdsRole", "dave", "admin", false, 1)
	require.Equal(t, int64(1), f.p.AnchorDerivationShadow().Acted)
	require.Equal(t, int64(1), f.p.AnchorDerivationShadow().ActedAnchors)
	require.Zero(t, f.p.AnchorDerivationShadow().FellBack)

	// A STATIC refusal — a conjunct this lens can never clear — is deliberately
	// not counted: it would report a fall-back on every event forever and drown
	// the ratio the tally exists to show. Only a per-event decline counts.
	f.p.sweeper = nil
	f.handleLink("holdsRole", "carol", "admin", true, 2)
	require.Equal(t, int64(1), f.p.AnchorDerivationShadow().Acted)
	require.Zero(t, f.p.AnchorDerivationShadow().FellBack,
		"a lens that can never act is logged once, not tallied per event")
}

// TestFlip_ReadCapFallsBackRatherThanTruncating pins the one path where the
// derivation gives up mid-walk. Truncating would return a subset and the arm
// would act on it, which is the single failure this whole unit exists to avoid.
//
// It needs a chain the walk actually reads along, which capabilityRoles does not
// give: a `holdsRole` event seeds AT the anchor position and answers with zero
// adjacency reads, so no cap can be reached on it. The ephemeral shape's
// booking → task → identity back-chain costs two reads and a cap of one refuses
// on the second.
func TestFlip_ReadCapFallsBackRatherThanTruncating(t *testing.T) {
	f := newDiffFixture(t, ephemeralDiffSpec)
	f.vertex("alice", "identity", map[string]any{"name": "alice"})
	f.vertex("t1", "task", map[string]any{"expiresAt": "2030-01-01T00:00:00Z"})
	f.vertex("bk1", "booking", map[string]any{"ref": "b1"})
	f.applyLink("assignedTo", "t1", "alice", false)
	scoped := f.applyLink("scopedTo", "t1", "bk1", false)

	ctx, rs := context.Background(), f.p.ruleState()
	derived, ok, err := f.p.deriveAnchorsForLink(ctx, rs, scoped)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{f.key("alice")}, derived,
		"the two-read chain resolves before the cap is lowered")

	f.p.SetAnchorDerivationReadCap(1)
	_, ok, err = f.p.deriveAnchorsForLink(ctx, rs, scoped)
	require.NoError(t, err)
	require.False(t, ok,
		"a walk that reaches the read cap must DECLINE — a truncated set would be acted on")

	// And a decline on the act path spends the enumerator, not a partial answer.
	f.p.patternClosedOutput = true
	f.p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})
	anchors, err := f.p.affectedAnchors(ctx, rs, scoped,
		func() ([]string, bool, error) { return f.p.deriveAnchorsForLink(ctx, rs, scoped) },
		func() ([]string, error) { return f.p.actorEnumerator.Enumerate(ctx, f.key("bk1"), "booking") })
	require.NoError(t, err)
	require.Equal(t, []string{f.key("alice")}, anchors)
	require.Equal(t, int64(1), f.p.AnchorDerivationShadow().FellBack)
	require.Zero(t, f.p.AnchorDerivationShadow().Acted)
}

// TestFlip_OffModeIsTheShippedBehaviour is the rollback the operator knob buys:
// one setting, and every arm is back to the enumerator on the next event.
func TestFlip_OffModeIsTheShippedBehaviour(t *testing.T) {
	f := newCoHolderFixture(t)
	f.p.SetAnchorDerivationMode(DerivationModeOff)

	f.handleLink("holdsRole", "dave", "admin", false, 1)
	require.Len(t, f.writtenActors(), 4)
	require.Zero(t, f.p.AnchorDerivationShadow().Acted)
	require.Zero(t, f.p.AnchorDerivationShadow().FellBack,
		"off runs neither arm's bookkeeping — there is no derivation to account for")
}

func TestParseDerivationMode(t *testing.T) {
	for in, want := range map[string]DerivationMode{
		"off": DerivationModeOff, "shadow": DerivationModeShadow, "act": DerivationModeAct,
	} {
		got, err := ParseDerivationMode(in)
		require.NoError(t, err)
		require.Equal(t, want, got)
		require.Equal(t, in, got.String())
	}
	// Rejected rather than defaulted: a typo silently resolving to `off` would
	// disable the derivation on a lens someone was watching, and say nothing.
	_, err := ParseDerivationMode("ACT")
	require.Error(t, err)
	_, err = ParseDerivationMode("")
	require.Error(t, err)
}

// TestDefaultDerivationMode_IsAct pins the built-in default, since every
// assertion above that does NOT set a mode depends on it.
func TestDefaultDerivationMode_IsAct(t *testing.T) {
	p := &Pipeline{}
	require.Equal(t, DerivationModeAct, p.derivationMode())

	SetDefaultAnchorDerivationMode(DerivationModeShadow)
	t.Cleanup(func() { SetDefaultAnchorDerivationMode(DerivationModeUnset) })
	require.Equal(t, DerivationModeShadow, p.derivationMode())

	p.SetAnchorDerivationMode(DerivationModeAct)
	require.Equal(t, DerivationModeAct, p.derivationMode(),
		"a per-pipeline override wins over the package default")
}

// TestFlip_AspectEventDerivesThroughItsParent covers the third arm end to end:
// an aspect mutation on the role reaches every holder, an aspect on one
// identity reaches only that identity.
func TestFlip_AspectEventDerivesThroughItsParent(t *testing.T) {
	f := newCoHolderFixture(t)
	ctx := context.Background()

	dec, err := f.p.handle(ctx, substrate.Message{
		Subject: "$KV.CORE." + f.key("admin") + ".detail",
		Body:    []byte(`{"data":{}}`), Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	require.ElementsMatch(t, []string{
		capKeyFor(f.key("alice")), capKeyFor(f.key("bob")), capKeyFor(f.key("carol")),
	}, f.writtenActors())

	f.adpt.upserts = nil
	dec, err = f.p.handle(ctx, substrate.Message{
		Subject: "$KV.CORE." + f.key("bob") + ".state",
		Body:    []byte(`{"data":{}}`), Sequence: 2})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	require.Equal(t, []string{capKeyFor(f.key("bob"))}, f.writtenActors(),
		"an aspect on one identity is that identity's business alone")
}
