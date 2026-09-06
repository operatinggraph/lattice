package pipeline

// The partition-scoped target diff, end to end over embedded NATS
// (anchor-partitioned-plain-lens-retraction-design.md §4's state table, §11).
//
// The lens is landlordShapeSpec — the shipped landlord shape: an (app_id,
// landlord_id) key whose second column binds a NEIGHBOUR the `manages` walk
// reached, so the read-free retraction key is underivable by construction and
// the whole-target diff is what the lens runs today. Armed, its seeded and
// licensed-neighbour events diff within one leaseapp's partition instead.
//
// EVERY assertion here is about which rows a diff may touch, so the fixture
// always carries a SECOND application whose rows no event under test names. A
// mechanism that scoped nothing would pass a single-anchor fixture.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The fixture's graph, named once so every assertion reads in the vocabulary of
// the shape rather than in NanoIDs.
//
//	app1 -appliesToUnit-> unit1 <-manages- landlordX
//	                            <-manages- landlordY   (the co-manager)
//	app2 -appliesToUnit-> unit2 <-manages- landlordX
//
// So app1 owns two rows and app2 owns one, and app2's row is the sibling every
// scoping assertion below is measured against.
const (
	partApp1      = "PARTappAAAAAAAAAAAAA"
	partApp2      = "PARTappBBBBBBBBBBBBB"
	partUnit1     = "PARTunitAAAAAAAAAAAA"
	partUnit2     = "PARTunitBBBBBBBBBBBB"
	partLandlordX = "PARTmgrXAAAAAAAAAAAA"
	partLandlordY = "PARTmgrYAAAAAAAAAAAA"
)

// partitionCountingAdapter is the real NATS-KV adapter with a tally of which LISTING each
// event ran, plus an injectable Delete failure.
//
// The tally is the finding-1 assertion's whole instrument: "the outer frame's
// whole diff did not run" is not observable from the target — the rows it would
// have deleted belong to an anchor the frame never named, so a broken
// implementation and a correct one differ by a listing rather than by a row
// until a second anchor happens to hold one. Counting the call is the direct
// question.
type partitionCountingAdapter struct {
	*adapter.NatsKVAdapter
	wholeListings     int
	partitionListings int
	// listedPartitions records the fixed value of every partition listing, so a
	// test can say WHICH anchors were listed and how many times — the only way
	// to tell "the outer frame diffed each derived anchor once" apart from
	// "each re-entry diffed its own", which produce the same total.
	listedPartitions []string
	// failDeleteOn, when non-empty, makes a Delete of that rendered key fail —
	// the transient tombstone failure whose FailClosed abort keeps a sibling
	// upsert from landing past a row that should be gone.
	failDeleteOn string
}

func (c *partitionCountingAdapter) ListKeys(ctx context.Context) ([]map[string]any, error) {
	c.wholeListings++
	return c.NatsKVAdapter.ListKeys(ctx)
}

func (c *partitionCountingAdapter) ListKeysPrefix(ctx context.Context, prefix string) ([]map[string]any, error) {
	c.wholeListings++
	return c.NatsKVAdapter.ListKeysPrefix(ctx, prefix)
}

func (c *partitionCountingAdapter) ListKeysWhere(ctx context.Context, fixed map[string]any, prefix string) ([]map[string]any, error) {
	c.partitionListings++
	c.listedPartitions = append(c.listedPartitions, fmt.Sprintf("%v", fixed))
	return c.NatsKVAdapter.ListKeysWhere(ctx, fixed, prefix)
}

func (c *partitionCountingAdapter) Delete(ctx context.Context, keys map[string]any, seq uint64) error {
	if c.failDeleteOn != "" && rowKeyOf(keys) == c.failDeleteOn {
		return fmt.Errorf("injected tombstone failure for %s", c.failDeleteOn)
	}
	return c.NatsKVAdapter.Delete(ctx, keys, seq)
}

// DeleteWithOutcome is overridden beside Delete because that is the method the
// write loop actually calls: the base adapter satisfies adapter.OutcomeDeleter,
// and a promoted DeleteWithOutcome would reach the inner implementation with the
// injected failure never consulted — a fault-injection double that injects
// nothing and a green test that proves nothing.
func (c *partitionCountingAdapter) DeleteWithOutcome(ctx context.Context, keys map[string]any, seq uint64) (adapter.DeleteOutcome, error) {
	if c.failDeleteOn != "" && rowKeyOf(keys) == c.failDeleteOn {
		return adapter.DeleteOutcome{}, fmt.Errorf("injected tombstone failure for %s", c.failDeleteOn)
	}
	return c.NatsKVAdapter.DeleteWithOutcome(ctx, keys, seq)
}

// rowKeyOf renders a (app_id, landlord_id) key map the way the adapter builds
// its target key, so a test can name one row.
func rowKeyOf(keys map[string]any) string {
	return fmt.Sprintf("%v.%v", keys["app_id"], keys["landlord_id"])
}

func partRowKey(appID, landlordID string) string { return appID + "." + landlordID }

// partitionFixture is one landlord-shaped lens over embedded NATS, with the
// graph above already written and every activation step a real deployment runs
// carried out in cmd/refractor's own order.
type partitionFixture struct {
	t        *testing.T
	p        *Pipeline
	coreKV   *substrate.KV
	adjKV    *substrate.KV
	targetKV *substrate.KV
	adpt     *partitionCountingAdapter
	seq      uint64
}

// newPartitionFixture builds the lens and, when arm is true, walks the whole
// activation chain: SetDiffRetraction, the audit's own enrolment, one real audit
// pass (so the derivation licence's verdict clock is stamped), act mode, and
// SetPartitionRetraction on the business plane.
//
// Every one of those is a licence conjunct, and none is posed by hand. A fixture
// that set p.partitionRetraction directly would establish the favourable arm
// rather than test it, and the licensed-neighbour case below has to reach
// plainDerivationDecide's ACT arm through a real adjacency index for its
// assertion to mean anything.
func newPartitionFixture(t *testing.T, arm bool) *partitionFixture {
	t.Helper()
	return newPartitionFixtureWith(t, landlordShapeSpec, []string{"app_id", "landlord_id"}, arm)
}

// newPartitionFixtureWith is the same fixture over an arbitrary spec — the
// hold-out cases the design excludes by a conjunct each need one: a grant
// table's plane, a closed lens's shape, a multi-position anchor. arm is
// deliberately the only knob: a caller that wants a different plane, or wants to
// arm after seeding its own graph, calls armAudit or SetPartitionRetraction
// itself, so every such case reads as the deliberate step it is.
func newPartitionFixtureWith(t *testing.T, spec string, keyCols []string, arm bool) *partitionFixture {
	t.Helper()
	coreKV, adjKV, targetKV, healthKV := newAuditKVs(t)

	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull)
	fullCR.KeyColumns = keyCols
	require.NoError(t, fullCR.ValidateKeyColumns())

	base, err := adapter.New(targetKV, keyCols, adapter.DeleteModeHard)
	require.NoError(t, err)
	adpt := &partitionCountingAdapter{NatsKVAdapter: base}

	reporter := health.New(healthKV, "partition-rule")
	p, err := New("partition-rule", "nats_kv", "CORE", adjKV, coreKV, adpt, reporter)
	require.NoError(t, err)
	require.NoError(t, p.UseFullEngine(eng, cr))
	require.NoError(t, p.SetDiffRetraction(true))

	f := &partitionFixture{t: t, p: p, coreKV: coreKV, adjKV: adjKV, targetKV: targetKV, adpt: adpt}
	f.writeGraph()

	if arm {
		f.armAudit(t)
	}
	return f
}

// armAudit walks the whole activation chain a real deployment runs: the audit's
// own enrolment, one real pass (so the derivation licence's verdict clock is
// stamped), act mode, and SetPartitionRetraction.
//
// It is a separate step because the audit pass must run against a graph that
// already holds anchors — a pass that compares nothing reaches no verdict and
// leaves LastPassAt at zero, which the licence reads as stale — so a fixture
// whose graph is written by the caller arms after it, not before.
func (f *partitionFixture) armAudit(t *testing.T) {
	t.Helper()
	p := f.p
	enrolled, refusal := p.InstallAudit(AuditOptions{})
	require.Truef(t, enrolled, "the fixture lens must enrol its divergence audit; refusal: %s", refusal)
	label, _ := p.ruleState().cr.(*full.CompiledRule).AnchorLabel()
	p.SetAuditPlan(AuditPlan{AnchorLabel: label, Batch: 10, Interval: time.Hour})
	p.Auditor().pass(context.Background())
	require.False(t, p.Auditor().Status().LastPassAt.IsZero(),
		"the audit must have reached a verdict, or the derivation licence refuses as stale and no neighbour event is ever licensed")
	p.SetAnchorDerivationMode(DerivationModeAct)
	require.NoError(t, p.SetPartitionRetraction(false))
	require.True(t, p.PartitionRetraction(), "the fixture's whole point is a lens activation actually armed")
	require.True(t, p.partitionArmed(p.ruleState()))
}

// writeGraph writes the vertices and both adjacency directions of every link in
// the shape above.
func (f *partitionFixture) writeGraph() {
	f.t.Helper()
	seedVertexBody(f.t, f.coreKV, "vtx.leaseapp."+partApp1, "leaseapp", nil)
	seedVertexBody(f.t, f.coreKV, "vtx.leaseapp."+partApp2, "leaseapp", nil)
	seedVertexBody(f.t, f.coreKV, "vtx.unit."+partUnit1, "unit", nil)
	seedVertexBody(f.t, f.coreKV, "vtx.unit."+partUnit2, "unit", nil)
	seedVertexBody(f.t, f.coreKV, "vtx.identity."+partLandlordX, "identity", nil)
	seedVertexBody(f.t, f.coreKV, "vtx.identity."+partLandlordY, "identity", nil)

	buildCollisionEdge(f.t, f.adjKV, "appliesToUnit", "leaseapp", partApp1, "unit", partUnit1)
	buildCollisionEdge(f.t, f.adjKV, "appliesToUnit", "leaseapp", partApp2, "unit", partUnit2)
	buildCollisionEdge(f.t, f.adjKV, "manages", "identity", partLandlordX, "unit", partUnit1)
	buildCollisionEdge(f.t, f.adjKV, "manages", "identity", partLandlordY, "unit", partUnit1)
	buildCollisionEdge(f.t, f.adjKV, "manages", "identity", partLandlordX, "unit", partUnit2)
}

// resetListingCounts zeroes the listing tally, so an assertion speaks about the
// event a test is about rather than the fixture's own setup.
func (f *partitionFixture) resetListingCounts() {
	f.adpt.wholeListings, f.adpt.partitionListings = 0, 0
	f.adpt.listedPartitions = nil
}

// nextSeq hands out a fresh, monotonically increasing stream sequence.
func (f *partitionFixture) nextSeq() uint64 {
	f.seq++
	return f.seq
}

// event drives one vertex-root CDC event for an already-written vertex.
func (f *partitionFixture) event(vtype, id string) {
	f.t.Helper()
	key := "vtx." + vtype + "." + id
	body := seedVertexBody(f.t, f.coreKV, key, vtype, nil)
	handleVertexEvent(f.t, f.p, key, body, f.nextSeq())
}

// deliver drives one vertex-root CDC event and hands back the pipeline's own
// disposition, for the cases whose whole assertion is that the event did NOT
// ack.
func (f *partitionFixture) deliver(vtype, id string) (substrate.Decision, error) {
	f.t.Helper()
	key := "vtx." + vtype + "." + id
	body := seedVertexBody(f.t, f.coreKV, key, vtype, nil)
	return f.p.handle(context.Background(), substrate.Message{
		Subject: "$KV.CORE." + key, Body: body, Sequence: f.nextSeq(),
	})
}

// projectAll drives both applications' own events, so every row in the shape is
// written by the lens's OWN write path.
func (f *partitionFixture) projectAll() {
	f.t.Helper()
	f.event("leaseapp", partApp1)
	f.event("leaseapp", partApp2)
	f.requireRows(
		partRowKey(partApp1, partLandlordX),
		partRowKey(partApp1, partLandlordY),
		partRowKey(partApp2, partLandlordX),
	)
}

// unmanage removes one `manages` edge from adjacency, leaving the vertices in
// place — the co-manager withdrawal the design's state table calls
// "matched-then-shrunk".
func (f *partitionFixture) unmanage(landlordID, unitID string) {
	f.t.Helper()
	ctx := context.Background()
	linkKey := "lnk.identity." + landlordID + ".manages.unit." + unitID
	edgeID := "manages:" + landlordID + ":" + unitID
	for _, evt := range []adjacency.CoreKVEvent{
		{CoreKvKey: linkKey, EdgeID: edgeID, Name: "manages", Direction: "outbound",
			NodeID: landlordID, OtherNodeID: unitID, OtherType: "unit", IsDeleted: true},
		{CoreKvKey: linkKey, EdgeID: edgeID, Name: "manages", Direction: "inbound",
			NodeID: unitID, OtherNodeID: landlordID, OtherType: "identity", IsDeleted: true},
	} {
		require.NoError(f.t, adjacency.Build(ctx, f.adjKV, evt))
	}
}

// tombstoneApp writes an application's root tombstone and drives its CDC event.
func (f *partitionFixture) tombstoneApp(id string) {
	f.t.Helper()
	key := "vtx.leaseapp." + id
	body := seedVertexBody(f.t, f.coreKV, key, "leaseapp", map[string]any{"isDeleted": true})
	handleVertexEvent(f.t, f.p, key, body, f.nextSeq())
}

// liveRows returns every target key currently holding a row.
func (f *partitionFixture) liveRows() []string {
	f.t.Helper()
	keys, err := f.targetKV.ListKeys(context.Background())
	require.NoError(f.t, err)
	return keys
}

func (f *partitionFixture) requireRows(want ...string) {
	f.t.Helper()
	require.ElementsMatch(f.t, want, f.liveRows())
}

// TestPartitionRetraction_ArmedLens walks §4's state table on the armed lens,
// row by row.
func TestPartitionRetraction_ArmedLens(t *testing.T) {
	t.Run("an anchor event seeds and writes its own partition", func(t *testing.T) {
		f := newPartitionFixture(t, true)
		f.projectAll()
		require.Zero(t, f.adpt.wholeListings,
			"an armed lens's anchor event diffs its own partition; a whole listing here would be the mechanism not engaging at all")
	})

	t.Run("a licensed neighbour event tombstones inside one partition only", func(t *testing.T) {
		f := newPartitionFixture(t, true)
		f.projectAll()
		siblingRev := targetRevision(t, f.targetKV, partRowKey(partApp2, partLandlordX))

		// The co-manager withdraws from unit1. The event is on the UNIT — a
		// neighbour type — so it reaches the derivation, which walks the real
		// adjacency index to app1 and re-enters it as a seeded evaluation.
		f.unmanage(partLandlordY, partUnit1)
		f.resetListingCounts()
		f.event("unit", partUnit1)

		f.requireRows(
			partRowKey(partApp1, partLandlordX),
			partRowKey(partApp2, partLandlordX),
		)
		require.Zero(t, f.adpt.wholeListings,
			"THE finding-1 assertion: the outer frame's results are the union of the derived anchors' rows, and a whole "+
				"listing compared against them would have tombstoned every anchor the frame did not cover")
		require.Positive(t, f.adpt.partitionListings,
			"the re-entry must have diffed its own partition — a run with no listing at all retracts nothing and would "+
				"pass the assertion above for the wrong reason")
		require.Equal(t, siblingRev, targetRevision(t, f.targetKV, partRowKey(partApp2, partLandlordX)),
			"the other application's row is in the same bucket and was never listed, so its revision cannot have moved")
	})

	t.Run("an anchor tombstone empties its whole partition", func(t *testing.T) {
		f := newPartitionFixture(t, true)
		f.projectAll()

		f.tombstoneApp(partApp1)
		f.requireRows(partRowKey(partApp2, partLandlordX))
		require.Zero(t, f.adpt.wholeListings,
			"the tombstoned anchor's key is all the predicate needs, so the partition names itself with no body and no whole listing")
	})

	t.Run("redelivering the tombstone changes nothing", func(t *testing.T) {
		f := newPartitionFixture(t, true)
		f.projectAll()
		f.tombstoneApp(partApp1)
		siblingRev := targetRevision(t, f.targetKV, partRowKey(partApp2, partLandlordX))

		f.tombstoneApp(partApp1)
		f.requireRows(partRowKey(partApp2, partLandlordX))
		require.Equal(t, siblingRev, targetRevision(t, f.targetKV, partRowKey(partApp2, partLandlordX)))
	})

	t.Run("a failing tombstone aborts the batch and the row survives", func(t *testing.T) {
		f := newPartitionFixture(t, true)
		f.projectAll()

		f.unmanage(partLandlordY, partUnit1)
		f.adpt.failDeleteOn = partRowKey(partApp1, partLandlordY)

		dec, err := f.deliver("unit", partUnit1)
		require.Error(t, err,
			"a partition tombstone carries FailClosed, so its write failure fails the whole event rather than being acked past")
		require.NotEqual(t, substrate.Ack, dec, "and the event is redelivered rather than acked with its retraction missing")
		f.requireRows(
			partRowKey(partApp1, partLandlordX),
			partRowKey(partApp1, partLandlordY),
			partRowKey(partApp2, partLandlordX),
		)

		// And the retraction lands on redelivery, which is what makes the abort
		// a deferral rather than a loss.
		f.adpt.failDeleteOn = ""
		f.event("unit", partUnit1)
		f.requireRows(
			partRowKey(partApp1, partLandlordX),
			partRowKey(partApp2, partLandlordX),
		)
	})

	t.Run("an unlicensed neighbour event still takes the whole diff", func(t *testing.T) {
		f := newPartitionFixture(t, true)
		f.projectAll()

		// The audit is suppressed, so the derivation licence refuses and the
		// neighbour event falls back to the whole-corpus rescan — whose row set
		// IS the complete truth, so the whole listing is exact for it.
		f.p.Auditor().noteSuppressed("test pause")
		licensed, refusal := f.p.plainDerivationLicence(f.p.ruleState())
		require.False(t, licensed, "the vector for this case is a lens the licence turns back")
		require.NotEmpty(t, refusal)

		f.unmanage(partLandlordY, partUnit1)
		f.resetListingCounts()
		f.event("unit", partUnit1)

		require.Positive(t, f.adpt.wholeListings,
			"an unlicensed neighbour event recomputes the whole corpus, and the whole listing is what that row set is exact against")
		require.Zero(t, f.adpt.partitionListings,
			"there is no single anchor for a whole-corpus row set to be scoped to")
		f.requireRows(
			partRowKey(partApp1, partLandlordX),
			partRowKey(partApp2, partLandlordX),
		)
	})
}

// TestPartitionRetraction_UnarmedLensKeepsTheWholeDiff is the negative half, and
// it is the one that proves the arming is what does the work: the SAME lens,
// the SAME graph, the SAME events, with SetPartitionRetraction never called.
func TestPartitionRetraction_UnarmedLensKeepsTheWholeDiff(t *testing.T) {
	f := newPartitionFixture(t, false)
	require.False(t, f.p.partitionArmed(f.p.ruleState()))
	require.Empty(t, f.p.seedAnchorFor(f.p.ruleState(), "leaseapp", "vtx.leaseapp."+partApp1),
		"an unarmed DiffRetraction lens must still recompute its whole row set — its diff retracts everything it fails to re-derive")

	f.projectAll()
	require.Positive(t, f.adpt.wholeListings, "every event on an unarmed lens lists the whole target")
	require.Zero(t, f.adpt.partitionListings)
}

// TestPartitionRetraction_ActivationRefusals pins SetPartitionRetraction's three
// dispositions, and the difference between them: an error is an activation
// refusal, and a bare false is a lens the transport does not apply to.
func TestPartitionRetraction_ActivationRefusals(t *testing.T) {
	eng := full.New()
	compile := func(t *testing.T, spec string, keyCols []string) ruleengine.CompiledRule {
		t.Helper()
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		fullCR := cr.(*full.CompiledRule)
		fullCR.KeyColumns = keyCols
		require.NoError(t, fullCR.ValidateKeyColumns())
		return cr
	}
	// The audit is enrolled wherever the target admits it, because
	// partitionArmed's audit half demands one: without it every case below
	// would read "not armed" for a reason none of them is about. It is
	// best-effort rather than required because one case deliberately uses a
	// target that cannot read a row back, and there the assertion is on
	// SetPartitionRetraction's own error, which no audit decides.
	newWith := func(t *testing.T, adpt adapter.Adapter, spec string, keyCols []string) *Pipeline {
		t.Helper()
		p, err := New("partition-activation", "nats_kv", "CORE", nil, nil, adpt, nil)
		require.NoError(t, err)
		require.NoError(t, p.UseFullEngine(eng, compile(t, spec, keyCols)))
		p.InstallAudit(AuditOptions{})
		return p
	}

	t.Run("a partition-only business lens whose target cannot scope a listing is REFUSED", func(t *testing.T) {
		p := newWith(t, &keyListerAdapter{}, landlordShapeSpec, []string{"app_id", "landlord_id"})
		require.NoError(t, p.SetDiffRetraction(true))
		err := p.SetPartitionRetraction(false)
		require.Error(t, err,
			"it would seed per anchor with nothing able to scope its diff — dark is the safe end of half-armed")
		require.Contains(t, err.Error(), "PartitionKeyLister")
		require.False(t, p.PartitionRetraction())
	})

	t.Run("an auth-plane lens is not armed, and that is not an error", func(t *testing.T) {
		p := newWith(t, &partitionListerAdapter{}, landlordShapeSpec, []string{"app_id", "landlord_id"})
		require.NoError(t, p.SetDiffRetraction(true))
		require.NoError(t, p.SetPartitionRetraction(true))
		require.False(t, p.PartitionRetraction(),
			"the auth plane's whole diff on every event is the only shrink path an un-truncatable target has on a rebuild")
	})

	t.Run("a lens whose rows do not partition is not armed, and that is not an error", func(t *testing.T) {
		p := newWith(t, &partitionListerAdapter{},
			`MATCH (app:leaseapp)
MATCH (app)-[:appliesToUnit]->(u:unit)
RETURN nanoIdFromKey(u.key) AS unit_id, u.status AS status`, []string{"unit_id", "status"})
		require.NoError(t, p.SetDiffRetraction(true))
		require.NoError(t, p.SetPartitionRetraction(false))
		require.False(t, p.PartitionRetraction())
	})

	t.Run("a CLOSED DiffRetraction lens is not armed", func(t *testing.T) {
		p := newWith(t, &partitionListerAdapter{},
			`MATCH (u:unit) RETURN nanoIdFromKey(u.key) AS unit_id, u.status AS status`,
			[]string{"unit_id", "status"})
		require.NoError(t, p.SetDiffRetraction(true))
		require.NoError(t, p.SetPartitionRetraction(false))
		require.False(t, p.PartitionRetraction(),
			"a closed lens retracts through the read-free presence check, and clinicPatientsRead's whole diff is a "+
				"ratified continuous healer only a whole listing can be")
	})

	t.Run("a lens with no DiffRetraction at all is not armed", func(t *testing.T) {
		p := newWith(t, &partitionListerAdapter{}, landlordShapeSpec, []string{"app_id", "landlord_id"})
		require.NoError(t, p.SetPartitionRetraction(false))
		require.False(t, p.PartitionRetraction())
	})

	t.Run("a MATCH reload onto a non-partitioning body disarms on the next event", func(t *testing.T) {
		p := newWith(t, &partitionListerAdapter{}, landlordShapeSpec, []string{"app_id", "landlord_id"})
		require.NoError(t, p.SetDiffRetraction(true))
		require.NoError(t, p.SetPartitionRetraction(false))
		require.True(t, p.partitionArmed(p.ruleState()))

		require.NoError(t, p.UseFullEngine(eng, compile(t,
			`MATCH (app:leaseapp)
MATCH (app)-[:appliesToUnit]->(u:unit)
RETURN nanoIdFromKey(u.key) AS unit_id, u.status AS status`, []string{"unit_id", "status"})))
		require.False(t, p.partitionArmed(p.ruleState()),
			"the rule half is re-derived at every install, so a reload that stops partitioning disarms with no reload refusal to write")
		require.True(t, p.PartitionRetraction(),
			"and the activation half is untouched — a reload back to a partitioning body re-arms without re-activating")
	})
}

// TestPartitionRetraction_PredicateMutationTouchesNoSibling is §9's mutation
// fixture: with the partition predicate resolving to a column that is NOT this
// lens's, the event FAILS and no row moves.
//
// The mutation is the predicate's own inputs rather than a hand-edited SQL
// string, because that is where the risk actually lives: the predicate names the
// scope a Delete is authorised within, and a scope naming the wrong column is
// either a partition nobody owns (the fail-closed answer this asserts) or, in
// the shape this design refuses, every row in the table.
func TestPartitionRetraction_PredicateMutationTouchesNoSibling(t *testing.T) {
	f := newPartitionFixture(t, true)
	f.projectAll()
	before := f.liveRows()

	rs := f.p.ruleState()
	// The unit's key stands in for the anchor's: a value that names a partition
	// this lens does not key by, reached through the real predicate call.
	_, ok := rs.engine.PartitionPredicate(rs.cr, "vtx.unit."+partUnit1, "unit")
	require.False(t, ok, "a non-anchor event names no partition of this lens")

	results, err := f.p.applyPartitionDiffRetraction(context.Background(), rs,
		[]string{"vtx.unit." + partUnit1}, nil)
	require.Error(t, err,
		"an unevaluable predicate fails the EVENT — a partition that cannot be named is never a licence to diff everything")
	require.Nil(t, results)
	require.ElementsMatch(t, before, f.liveRows(),
		"and nothing was written on the way to that error, on any anchor")

	// The same call with a well-formed anchor key that owns no rows in this
	// target: the listing is empty and nothing is tombstoned, which is the
	// difference between "cannot answer" and "answered, nothing there".
	unknown := "vtx.leaseapp.PARTappZZZZZZZZZZZZZ"
	kept, err := f.p.applyPartitionDiffRetraction(context.Background(), rs, []string{unknown}, nil)
	require.NoError(t, err)
	require.Empty(t, kept)
	require.ElementsMatch(t, before, f.liveRows())
}

// TestPartitionRetraction_ActedFrameIsUnreachableWithoutArming is the other half
// of the defensive refusal in evaluateForEntryRaw's tail.
//
// The tail fails the event outright when a frame is ACTED and the partition diff
// is not armed, because a whole diff over a partial row set is the one outcome
// this design forbids. That guard is unreachable in the shipped wiring, and this
// is the assertion that says so rather than leaving it a claim: with the
// activation half off, the derivation INDEX refuses before any re-entry is
// substituted, so no frame can be acted in the first place — and the same lens
// with the arming on does act, which is what stops the refusal above from being
// green for the wrong reason.
func TestPartitionRetraction_ActedFrameIsUnreachableWithoutArming(t *testing.T) {
	f := newPartitionFixture(t, true)
	f.projectAll()
	f.unmanage(partLandlordY, partUnit1)

	entry := ruleengine.NodeEntry{
		CoreKVKey: "vtx.unit." + partUnit1, NodeLabel: "unit",
		Properties: map[string]any{"lastModifiedAt": "2026-08-01T10:00:00Z"},
	}
	rs := f.p.ruleState()
	_, gotScope, err := f.p.evaluatePlainNeighbourEvent(context.Background(), rs, entry)
	require.NoError(t, err)
	require.Equal(t, scopeActed, gotScope.kind,
		"positive vector: an armed lens's licensed neighbour event DOES substitute per-anchor re-entries")
	require.NotEmpty(t, gotScope.anchors, "and it names the anchors the outer frame must diff")

	f.p.partitionRetraction = false
	require.NotEmpty(t, f.p.plainDerivationIndexRefusal(f.p.ruleState()),
		"with the arming off the derivation index refuses the lens outright")
	_, gotScope, err = f.p.evaluatePlainNeighbourEvent(context.Background(), f.p.ruleState(), entry)
	require.NoError(t, err)
	require.Equal(t, scopeWhole, gotScope.kind,
		"so the tail can never see an acted frame it may not diff — the guard beside it is the belt to this brace")
}

// TestPartitionRetraction_Transport pins the operator-visible verdict: an armed
// lens publishes `diffRetraction-partition`, and the same lens unarmed publishes
// the whole diff it actually runs.
func TestPartitionRetraction_Transport(t *testing.T) {
	armed := newPartitionFixture(t, true)
	require.Equal(t, RetractionTransportDiffRetractionPartition,
		armed.p.PlainRetractionTransport(false).Transport,
		"prove the payoff by the licence's POSITIVE verdict, never by a refusal's absence")

	unarmed := newPartitionFixture(t, false)
	require.Equal(t, RetractionTransportDiffRetraction,
		unarmed.p.PlainRetractionTransport(false).Transport)
}

// partitionListerAdapter is an inert target that satisfies every capability the
// partition arming requires — enough for the activation unit above, which never
// writes or reads a row.
type partitionListerAdapter struct{}

func (*partitionListerAdapter) Upsert(context.Context, map[string]any, map[string]any, uint64) error {
	return nil
}
func (*partitionListerAdapter) Delete(context.Context, map[string]any, uint64) error { return nil }
func (*partitionListerAdapter) Probe(context.Context) error                          { return nil }
func (*partitionListerAdapter) Close() error                                         { return nil }
func (*partitionListerAdapter) ListKeys(context.Context) ([]map[string]any, error)   { return nil, nil }
func (*partitionListerAdapter) ListKeysWhere(context.Context, map[string]any, string) ([]map[string]any, error) {
	return nil, nil
}
func (*partitionListerAdapter) GetRow(context.Context, map[string]any) (map[string]any, bool, error) {
	return nil, false, nil
}

var (
	_ adapter.KeyLister          = (*partitionListerAdapter)(nil)
	_ adapter.PartitionKeyLister = (*partitionListerAdapter)(nil)
	_ adapter.RowReader          = (*partitionListerAdapter)(nil)
)

// TestPartitionRetraction_AuditListsThePartition is §3.5: the should-not-exist
// direction exists for an armed lens, whose key carries a neighbour-bound column
// and so has no read-free row key for the audit to ask the target about.
//
// It is the standing detector for the failure the seeding itself could
// introduce — a per-anchor evaluation that produces FEWER rows than the whole
// one — so it ships with the seeding rather than after it. Both arms are here:
// the tombstoned anchor whose partition still holds a live key, and the live
// anchor whose partition holds a key its recompute did not produce.
func TestPartitionRetraction_AuditListsThePartition(t *testing.T) {
	// A row the lens can no longer produce, written straight to the target
	// behind the pipeline's back. That is the whole point of the vector: the
	// audit is detect-only, so the divergence it must name is one no write path
	// of this lens would ever create.
	orphan := func(f *partitionFixture, appID, landlordID string) {
		f.t.Helper()
		require.NoError(f.t, f.adpt.Upsert(context.Background(),
			map[string]any{"app_id": appID, "landlord_id": landlordID},
			map[string]any{"stale": true}, 900))
	}
	auditOnce := func(f *partitionFixture) AuditStatus {
		f.t.Helper()
		f.p.SetAuditPlan(AuditPlan{AnchorLabel: "leaseapp", Batch: 10, Interval: time.Hour})
		f.p.Auditor().pass(context.Background())
		return f.p.Auditor().Status()
	}

	t.Run("a live anchor whose partition holds a key the recompute did not produce", func(t *testing.T) {
		f := newPartitionFixture(t, true)
		f.projectAll()
		orphan(f, partApp1, "PARTmgrZAAAAAAAAAAAA")

		st := auditOnce(f)
		require.Equal(t, 1, st.Divergent[AuditClassRetained],
			"the under-production this seeding could cause is named by the standing detector rather than left silent")
		require.Zero(t, st.Unverified, "and the anchor IS checked in this direction now, rather than booked undrivable")
	})

	t.Run("a tombstoned anchor whose partition still holds a live key", func(t *testing.T) {
		f := newPartitionFixture(t, true)
		f.projectAll()
		f.tombstoneApp(partApp1)
		orphan(f, partApp1, partLandlordX)

		st := auditOnce(f)
		require.Equal(t, 1, st.Divergent[AuditClassRetained],
			"a tombstone whose retraction was lost leaves exactly this, and the partition listing is what sees it")
	})

	t.Run("a clean partition reports nothing", func(t *testing.T) {
		f := newPartitionFixture(t, true)
		f.projectAll()

		st := auditOnce(f)
		require.Empty(t, st.Divergent, "the positive vector: with no orphan the same listing must find nothing")
		require.Zero(t, st.Unverified)
	})

	t.Run("a lens that is not armed keeps the old declining direction", func(t *testing.T) {
		f := newPartitionFixture(t, false)
		f.projectAll()
		orphan(f, partApp1, "PARTmgrZAAAAAAAAAAAA")

		enrolled, refusal := f.p.InstallAudit(AuditOptions{})
		require.Truef(t, enrolled, "refusal: %s", refusal)
		st := auditOnce(f)
		require.Empty(t, st.Divergent[AuditClassRetained],
			"an unarmed lens is still not checked in this direction — the partition listing is armed with the seeding, not before it")
	})
}

// closedDiffRetractionSpec is a leaseapp lens whose single key column IS its
// anchor's identity — closed, so the read-free presence check retracts it and
// the partition-ONLY conjunct excludes it. It stands here for
// clinicPatientsRead, whose whole diff is a ratified continuous healer of the
// lost-anchor-event channel and which only a whole listing can be.
const closedDiffRetractionSpec = `
MATCH (app:leaseapp)
MATCH (app)-[:appliesToUnit]->(u:unit)
RETURN nanoIdFromKey(app.key) AS app_id, u.key AS unit_key
`

// TestPartitionRetraction_HoldOutsKeepTheWholeDiff is the behavioural half of
// §3.7: the two families the design deliberately leaves on the whole,
// unscoped-by-anchor diff still seed nothing and still list everything.
//
// Both are pinned by BEHAVIOUR rather than by the flag, because the flag is what
// the adversarial pass found a rule-only conjunct silently getting wrong: a
// seeded row set met against a whole listing on a grant table is a mass revoke,
// and against clinicPatientsRead's healer it is the ratified whole diff quietly
// removed.
func TestPartitionRetraction_HoldOutsKeepTheWholeDiff(t *testing.T) {
	t.Run("an auth-plane lens on a target with no partition listing", func(t *testing.T) {
		// The grant tables' shape: the rule partitions, and three independent
		// exclusions keep it off this transport. This fixture poses the plane —
		// the one the activation gate passes in — and asserts the outcome.
		f := newPartitionFixtureWith(t, landlordShapeSpec, []string{"app_id", "landlord_id"}, false)
		require.NoError(t, f.p.SetPartitionRetraction(true))
		require.False(t, f.p.PartitionRetraction(),
			"the whole diff on every event is the only shrink path an un-truncatable grant table has on a rebuild")

		require.Empty(t, f.p.seedAnchorFor(f.p.ruleState(), "leaseapp", "vtx.leaseapp."+partApp1),
			"so it must still recompute its whole row set — a per-anchor row set met against a source-wide listing is a mass revoke")
		f.projectAll()
		require.Positive(t, f.adpt.wholeListings)
		require.Zero(t, f.adpt.partitionListings)
	})

	t.Run("the shared grant writer implements no partition listing", func(t *testing.T) {
		// §3.7's SECOND exclusion, asserted against the real type rather than
		// argued: the grant tables are held off this transport by their plane,
		// by the gate, AND by their target, and each is meant to hold on its
		// own. A GrantWriterAdapter that quietly acquired the method would
		// remove one of the three without anything failing.
		var g any = &adapter.GrantWriterAdapter{}
		_, ok := g.(adapter.PartitionKeyLister)
		require.False(t, ok,
			"the shared grant table's writer must not be able to list a partition — a per-anchor row set met against "+
				"ListGrantsBySource's whole source is a mass revoke on actor_read_grants")
		_, ok = g.(adapter.KeyLister)
		require.True(t, ok,
			"positive vector: it CAN list its own source-scoped key set, which is the whole diff it keeps")
	})

	t.Run("a closed DiffRetraction lens", func(t *testing.T) {
		f := newPartitionFixtureWith(t, closedDiffRetractionSpec, []string{"app_id"}, false)
		require.NoError(t, f.p.SetPartitionRetraction(false))
		require.False(t, f.p.PartitionRetraction(),
			"the partition-ONLY conjunct excludes a lens that already closes; its retraction is the read-free presence check")
		require.False(t, f.p.ruleState().partition.only)

		require.Empty(t, f.p.seedAnchorFor(f.p.ruleState(), "leaseapp", "vtx.leaseapp."+partApp1),
			"and its whole diff — the continuous healer the secure design kept on purpose — needs the whole row set")
		f.event("leaseapp", partApp1)
		require.Zero(t, f.adpt.partitionListings,
			"a closed lens never lists a partition: its own key resolves read-free, so the presence check is what "+
				"retracts it and the diff is only reached where that derivation declines")
	})
}

// dupCandidatesShapeSpec is identity-hygiene's duplicateCandidates shape: ONE
// label bound at BOTH pattern positions, keyed on the pair, with the identifying
// column naming the anchor `b`.
//
// Only this shape can carry the under-coverage below: an
// event on an `identity` seeds (its type IS the anchor label) AND
// seedMultiPosition reports true (the same label binds `a`), so the event routes
// through evaluateSeededMultiPosition rather than the plain seeded call.
const dupCandidatesShapeSpec = `
MATCH (b:identity)-[:duplicateOf]->(a:identity)
WHERE b.state.data.value = 'claimed' AND a.state.data.value = 'claimed'
RETURN nanoIdFromKey(b.key) AS secondaryId, nanoIdFromKey(a.key) AS primaryId
`

// The pair fixture's three identities. X is the interesting one: it is the
// SECONDARY of a row keyed (X,P) — where X sits at the anchor position — and the
// PRIMARY of a row keyed (Q,X), where it sits at the OTHER position.
const (
	pairIdentityX = "DUPidentityXAAAAAAAA"
	pairIdentityP = "DUPidentityPAAAAAAAA"
	pairIdentityQ = "DUPidentityQAAAAAAAA"
)

// newPairFixture stands the duplicateCandidates shape up over embedded NATS,
// armed or not, with both rows already projected.
func newPairFixture(t *testing.T, arm bool) *partitionFixture {
	t.Helper()
	f := newPartitionFixtureWith(t, dupCandidatesShapeSpec, []string{"secondaryId", "primaryId"}, false)

	claimed := map[string]any{"data": map[string]any{"value": "claimed"}}
	for _, id := range []string{pairIdentityX, pairIdentityP, pairIdentityQ} {
		key := "vtx.identity." + id
		seedVertexBody(t, f.coreKV, key, "identity", nil)
		putBody(t, f.coreKV, key+".state", aspectBody(key, "state", claimed["data"].(map[string]any), false))
	}
	buildCollisionEdge(t, f.adjKV, "duplicateOf", "identity", pairIdentityX, "identity", pairIdentityP)
	buildCollisionEdge(t, f.adjKV, "duplicateOf", "identity", pairIdentityQ, "identity", pairIdentityX)

	if arm {
		f.armAudit(t)
	}
	// Both rows, written by the lens's own write path.
	f.event("identity", pairIdentityX)
	f.event("identity", pairIdentityQ)
	f.requireRows(
		partRowKey(pairIdentityX, pairIdentityP),
		partRowKey(pairIdentityQ, pairIdentityX),
	)
	f.resetListingCounts()
	return f
}

// unclaim flips an identity's state aspect so the WHERE stops matching every row
// that binds it — at EITHER pattern position.
func (f *partitionFixture) unclaim(id string) {
	f.t.Helper()
	key := "vtx.identity." + id
	putBody(f.t, f.coreKV, key+".state", aspectBody(key, "state", map[string]any{"value": "merged"}, false))
}

// TestPartitionRetraction_MultiPositionAnchorIsNotUnderCovered pins that a
// partition-armed lens whose anchor label binds a SECOND pattern position
// covers the rows at that position on every path.
//
// `duplicateCandidates` binds `identity` at BOTH pattern positions, so an
// anchor-typed event on it routes through evaluateSeededMultiPosition. The
// narrow single-position seed that producer declines to on an UNARMED lens
// misses every row where the vertex sits at the other position — and on an ARMED
// one the tail would then diff only that one anchor's partition, so such a row
// would be neither recomputed nor retracted. The whole rescan and whole diff
// cover it, and on an armed lens's declined path they are what must run.
//
// The declined state driven here is DERIVATION MODE OFF, and it has to be a mode
// rather than an audit fault: the arming's own audit half means a suppressed or
// un-enrolled audit disarms the lens outright, and an unarmed lens never seeds,
// so it reaches this producer at all only while it is armed. Mode off (and
// shadow) is exactly that: armed, seeding, and declining to act.
func TestPartitionRetraction_MultiPositionAnchorIsNotUnderCovered(t *testing.T) {
	t.Run("declined: the whole rescan and whole diff still cover the other position", func(t *testing.T) {
		f := newPairFixture(t, true)
		require.True(t, f.p.seedMultiPosition(f.p.ruleState(), "identity"),
			"precondition: this lens's anchor label binds a second pattern position, which is what routes the event here")
		require.True(t, f.p.partitionArmed(f.p.ruleState()),
			"precondition: armed, so the event seeds and reaches evaluateSeededMultiPosition rather than the neighbour path")

		// The operator has the derivation switched off, so the producer answers
		// with its DECLINED evaluation.
		f.p.SetAnchorDerivationMode(DerivationModeOff)

		// P stops being claimed. P is bound at the OTHER position of the (X,P)
		// row — a seed at P narrows to P-as-anchor, which produces nothing and
		// says nothing about the row X anchors; and (X,P) is in X's partition,
		// not P's, so a partition diff scoped to the seed would never list it.
		f.unclaim(pairIdentityP)
		f.event("identity", pairIdentityP)

		f.requireRows(partRowKey(pairIdentityQ, pairIdentityX))
		require.GreaterOrEqual(t, f.adpt.wholeListings, 1,
			"the declined answer on an armed lens must be the WHOLE rescan and the WHOLE diff — a partition diff over the "+
				"narrow seed's row set would leave the row this event dropped, in exactly the state an operator runs in")
	})

	t.Run("licensed: the same drop is retracted with no whole listing", func(t *testing.T) {
		f := newPairFixture(t, true)
		f.unclaim(pairIdentityP)
		f.event("identity", pairIdentityP)

		f.requireRows(partRowKey(pairIdentityQ, pairIdentityX))
		require.Zero(t, f.adpt.wholeListings,
			"only the licensed act path improves: the derivation names the anchors P reaches and each diffs its own partition")
		require.Positive(t, f.adpt.partitionListings)
	})

	t.Run("an UNARMED multi-position lens keeps the narrow seed", func(t *testing.T) {
		// The pre-existing posture, unchanged: a lens with no partition arming
		// pays exactly what it paid before, including its own pre-existing
		// other-position gap.
		f := newPairFixture(t, false)
		require.False(t, f.p.partitionArmed(f.p.ruleState()))

		rs := f.p.ruleState()
		entry := ruleengine.NodeEntry{
			CoreKVKey: "vtx.identity." + pairIdentityP, NodeLabel: "identity",
			Properties: map[string]any{"lastModifiedAt": "2026-08-01T10:00:00Z"},
		}
		f.p.SetAnchorDerivationMode(DerivationModeOff)
		_, gotScope, err := f.p.evaluateSeededMultiPosition(context.Background(), rs, entry)
		require.NoError(t, err)
		require.Equal(t, scopeSeeded, gotScope.kind,
			"an unarmed lens's declined answer is the narrow single-seed call — today's shipped cost, never a rescan it never asked for")
	})
}

// TestPartitionRetraction_DerivedAnchorWithNoReentryIsStillDiffed pins that a
// derived anchor is diffed even when its own re-entry produces nothing.
//
// evaluatePlainDerivedAnchors re-enters through plainEntryForVertex, which
// returns NOTHING for a derived anchor whose vertex is missing or tombstoned. If
// each re-entry owned its own diff, that anchor's partition would be listed by
// nobody — and the outer frame is forbidden from listing the whole target — so
// its rows would linger with no event left to name them.
//
// The scenario is a lost anchor event: an application is tombstoned but its own
// CDC event never arrives (a purge, a filter that dropped it, a consumer gap).
// Any later NEIGHBOUR event that derives that anchor must heal it.
func TestPartitionRetraction_DerivedAnchorWithNoReentryIsStillDiffed(t *testing.T) {
	heal := func(t *testing.T, arm bool) *partitionFixture {
		t.Helper()
		f := newPartitionFixture(t, arm)
		f.projectAll()

		// app1 is tombstoned in Core KV and its own event is LOST — nothing is
		// delivered for it, so its rows are still on the target.
		putBody(t, f.coreKV, "vtx.leaseapp."+partApp1, map[string]any{
			"key": "vtx.leaseapp." + partApp1, "class": "leaseapp", "isDeleted": true,
			"createdAt": "2026-08-01T10:00:00Z", "lastModifiedAt": "2026-08-01T10:00:00Z",
			"data": map[string]any{},
		})
		f.requireRows(
			partRowKey(partApp1, partLandlordX),
			partRowKey(partApp1, partLandlordY),
			partRowKey(partApp2, partLandlordX),
		)
		f.resetListingCounts()

		// A neighbour event that derives app1: the unit it applies to.
		f.event("unit", partUnit1)
		return f
	}

	t.Run("armed: the outer frame diffs the derived partitions and heals it", func(t *testing.T) {
		f := heal(t, true)
		f.requireRows(partRowKey(partApp2, partLandlordX))
		require.Positive(t, f.adpt.partitionListings,
			"the outer frame lists the K derived partitions — the re-entry that produced no results still had its partition listed")
		require.Zero(t, f.adpt.wholeListings,
			"and it does so without the whole listing the finding-1 assertion forbids")
	})

	t.Run("unarmed: the whole diff heals it, as it always did", func(t *testing.T) {
		f := heal(t, false)
		f.requireRows(partRowKey(partApp2, partLandlordX))
		require.Positive(t, f.adpt.wholeListings)
	})
}

// TestPartitionRetraction_ReentrantFrameRunsNoDiff states, where it is decided
// rather than inferred from the outcome above, that the diff is the OUTER
// frame's — once, over every derived anchor.
//
// Without it the K partitions would each be listed twice — once by their own
// re-entry and once by the outer frame — which is not merely waste: it is the
// shape in which a derived anchor that produced no re-entry results at all gets
// no listing from anyone.
func TestPartitionRetraction_ReentrantFrameRunsNoDiff(t *testing.T) {
	f := newPartitionFixture(t, true)
	f.projectAll()
	f.unmanage(partLandlordY, partUnit1)
	f.resetListingCounts()

	rs := f.p.ruleState()
	anchors, ok, err := f.p.deriveAnchorsForPlainVertex(context.Background(), rs,
		"vtx.unit."+partUnit1, "unit")
	require.NoError(t, err)
	require.True(t, ok)

	f.event("unit", partUnit1)

	// EACH derived anchor listed EXACTLY ONCE. A bare count cannot tell the two
	// implementations apart — K re-entries each diffing their own partition and
	// one outer frame diffing all K both total K — so the assertion is the
	// multiset: a re-entry that also diffed would list its own anchor twice.
	require.Len(t, f.adpt.listedPartitions, len(anchors),
		"the diff is the outer frame's, once per derived anchor; a re-entrant frame that diffed too would double a listing")
	seen := map[string]int{}
	for _, fixed := range f.adpt.listedPartitions {
		seen[fixed]++
	}
	for fixed, n := range seen {
		require.Equalf(t, 1, n, "partition %s was listed %d times", fixed, n)
	}
	f.requireRows(
		partRowKey(partApp1, partLandlordX),
		partRowKey(partApp2, partLandlordX),
	)
}

// TestPartitionRetraction_AuditHalfDisarms pins the arming's audit half. The
// transport authorises Deletes on RLS-protected tables from a SEEDED evaluation,
// and the standing detector for a seeded evaluation that under-produces is the
// divergence audit's own should-not-exist direction. Without that audit the
// mechanism has no observer, so it does not arm.
func TestPartitionRetraction_AuditHalfDisarms(t *testing.T) {
	t.Run("no auditor installed: not armed, whole diff, transport diffRetraction", func(t *testing.T) {
		f := newPartitionFixtureWith(t, landlordShapeSpec, []string{"app_id", "landlord_id"}, false)
		f.p.SetAnchorDerivationMode(DerivationModeAct)
		require.NoError(t, f.p.SetPartitionRetraction(false))
		require.True(t, f.p.PartitionRetraction(), "the activation half binds — it does not read the audit")
		require.True(t, f.p.ruleState().partition.only, "and the rule half holds")
		require.False(t, f.p.partitionArmed(f.p.ruleState()),
			"but with nothing standing to re-test a row a seeded evaluation left behind, the whole is not armed")

		require.Empty(t, f.p.seedAnchorFor(f.p.ruleState(), "leaseapp", "vtx.leaseapp."+partApp1))
		require.Equal(t, RetractionTransportDiffRetraction, f.p.PlainRetractionTransport(false).Transport,
			"and the operator is told what actually runs, not what the adapter would allow")

		f.projectAll()
		require.Positive(t, f.adpt.wholeListings)
		require.Zero(t, f.adpt.partitionListings)
	})

	t.Run("the deployment kill switch disarms on the next event and re-arms", func(t *testing.T) {
		f := newPartitionFixture(t, true)
		require.True(t, f.p.partitionArmed(f.p.ruleState()), "positive vector: armed while the switch is up")

		SetAuditEnabled(false)
		t.Cleanup(func() { SetAuditEnabled(true) })
		require.False(t, f.p.partitionArmed(f.p.ruleState()),
			"the switch is read LIVE — AuditStatus.Enrolled is the install-time verdict and would keep this armed until some later pass")
		require.Equal(t, RetractionTransportDiffRetraction, f.p.PlainRetractionTransport(false).Transport)
		require.Contains(t, f.p.plainDerivationIndexRefusal(f.p.ruleState()), "kill switch",
			"and the refusal names the condition that governs rather than the declaration")

		f.projectAll()
		require.Positive(t, f.adpt.wholeListings, "so the event pays exactly today's cost: whole rescan, whole diff")
		require.Zero(t, f.adpt.partitionListings)

		SetAuditEnabled(true)
		require.True(t, f.p.partitionArmed(f.p.ruleState()), "and it re-arms with nothing to re-activate")
	})

	t.Run("a suppressed audit disarms it", func(t *testing.T) {
		f := newPartitionFixture(t, true)
		f.p.Auditor().noteSuppressed("test pause")
		require.False(t, f.p.partitionArmed(f.p.ruleState()),
			"Enrolled alone is fail-open — an operator pause leaves it true while nothing is re-testing anything")
		require.Equal(t, RetractionTransportDiffRetraction, f.p.PlainRetractionTransport(false).Transport)
	})
}

// TestPartitionRetraction_MultiWalkLensIsNeverArmed holds the multi-walk
// exclusion. Branch merging
// evaluates N independent queries and drops the seed for all of them
// (evaluateBranches), so a multi-branch lens armed off its head rule would
// evaluate its WHOLE corpus and diff that row set against ONE anchor's
// partition — every other anchor's rows in that partition reading as dropped.
func TestPartitionRetraction_MultiWalkLensIsNeverArmed(t *testing.T) {
	eng := full.New()
	compile := func(t *testing.T) ruleengine.CompiledRule {
		t.Helper()
		cr, err := eng.Parse(landlordShapeSpec)
		require.NoError(t, err)
		fullCR := cr.(*full.CompiledRule)
		fullCR.KeyColumns = []string{"app_id", "landlord_id"}
		require.NoError(t, fullCR.ValidateKeyColumns())
		return cr
	}
	p, err := New("partition-multiwalk", "nats_kv", "CORE", nil, nil, &partitionListerAdapter{}, nil)
	require.NoError(t, err)

	head := compile(t)
	require.NoError(t, p.UseFullEngine(eng, head))
	require.True(t, p.ruleState().partition.only, "positive vector: as a SINGLE-walk lens this body partitions")

	branches := []ruleengine.CompiledRule{compile(t), compile(t)}
	require.NoError(t, p.UseFullEngineBranches(eng, branches[0], branches))
	require.False(t, p.ruleState().partition.only,
		"as a multi-walk lens it must not: the merge drops the seed, so the evaluation is whole and only the whole diff is exact against it")
	require.Empty(t, p.seedAnchorLabels,
		"which is the same reason seeding itself is refused for a multi-walk lens")
}
