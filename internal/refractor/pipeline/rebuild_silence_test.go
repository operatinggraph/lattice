package pipeline

// T8 of personal-lens-delta-publication-design.md §10/§4.5: while a personal
// lens's rebuild is replaying, the CDC write loop publishes NOTHING — no row, no
// Delete, no frame — and the completion asks the standing healer for one content
// cycle so the rebuilt shape reaches the device once, at a live revision.

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// countingRebuildSink is the standing healer as the pipeline sees it, counting
// the asks so "exactly once" is assertable.
type countingRebuildSink struct{ asks atomic.Int64 }

func (s *countingRebuildSink) RequestContentCycle() { s.asks.Add(1) }

// TestEventPublishScope_ARebuildInFlightIsSilent pins the flag-read point: the
// scope producer, once per event, so an event's rows and its frame are decided
// by one observation of RebuildInFlight and can never disagree.
func TestEventPublishScope_ARebuildInFlightIsSilent(t *testing.T) {
	actorKey := substrate.VertexKey("identity", personalActorA)

	t.Run("a scopeable personal lens publishes nothing while its rebuild replays", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		p.rebuildWindows.Store(1)
		assert.Equal(t, ScopeKindSilent, p.eventPublishScope(p.ruleState(), []string{actorKey}).Kind())
	})

	t.Run("and a lens the scope REFUSES publishes nothing either", func(t *testing.T) {
		// The refusal widens to ScopeAll, which during a replay is one
		// whole-actor republish per replayed Core KV entry — the flood itself.
		// So the rebuild check has to sit ahead of it.
		p, _ := newScopeProducerFixture(t)
		p.rebuildWindows.Store(1)
		rs := p.ruleState()
		rs.personalClockRefusal = "the row references $now"
		assert.Equal(t, ScopeKindSilent, p.eventPublishScope(rs, []string{actorKey}).Kind())

		scanSeeded := p.ruleState()
		scanSeeded.anchorHops = full.HopIndex{Labels: []string{"lease"}, Anchor: -1}
		assert.Equal(t, ScopeKindSilent, p.eventPublishScope(scanSeeded, []string{actorKey}).Kind())
	})

	t.Run("a non-personal target rebuilds exactly as it always has", func(t *testing.T) {
		// A plain or auth-plane rebuild rewrites a STORED read model, and the
		// replay is what repairs it. Silencing it would leave the target
		// unrebuilt.
		p, _ := newScopeProducerFixture(t)
		p.adpt = &recordingAdapter{}
		p.rebuildWindows.Store(1)
		assert.Equal(t, ScopeKindAll, p.eventPublishScope(p.ruleState(), []string{actorKey}).Kind())
	})

	t.Run("once the rescan has drained the next event is scoped again", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		p.rebuildWindows.Store(1)
		require.Equal(t, ScopeKindSilent, p.eventPublishScope(p.ruleState(), []string{actorKey}).Kind())
		p.rebuildWindows.Store(0)
		assert.Equal(t, ScopeKindVertices, p.eventPublishScope(p.ruleState(), []string{actorKey}).Kind(),
			"the silence is the rebuild window's, not a latch")
	})
}

// TestWriteResults_SilentPublishesNothingAtAll is the write loop's half: under
// ScopeSilent nothing reaches the target — not the admitted rows, not a Delete,
// and not the frame every other scope publishes — while the message is still
// acked, so the rescan drains and the ordering token advances.
func TestWriteResults_SilentPublishesNothingAtAll(t *testing.T) {
	target := &fakePersonalTarget{}
	p, _ := newPersonalTestPipeline(t, target)
	ctx := context.Background()

	touched := substrate.VertexKey("lease", scopedLeaseIDs[1])
	results := []ruleengine.EvalResult{
		scopedRowFor(personalActorA, "lease-a", touched),
		scopedRowFor(personalActorA, "lease-b", touched),
		{Delete: true, Keys: map[string]any{adapter.PersonalActorKeyField: personalActorA, "entityId": "lease-gone"}},
	}
	writesBefore := p.ProjectionWrites()
	projectedBefore := p.Progress().LastProjectedAt

	decision, err := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 42},
		touched, results, []string{substrate.VertexKey("identity", personalActorA)}, ScopeSilent())

	require.NoError(t, err)
	assert.Equal(t, substrate.Ack, decision,
		"the replayed message is disposed of — the rescan's outstanding count is what ends the rebuild")
	assert.Empty(t, target.upsertKeys(), "no row: every one of them is below the device's frame high-water mark")
	assert.Empty(t, target.deletes,
		"no Delete either: it prunes nothing below the high-water mark, and the content cycle's frame is what retracts")
	assert.Empty(t, target.snapshot(),
		"and no frame — the one scope that withholds it, because a frame at a replayed revision is dropped too")
	assert.Zero(t, p.ProjectionWrites()-writesBefore, "a withheld row is not even an attempted write")
	assert.Equal(t, projectedBefore, p.Progress().LastProjectedAt,
		"nothing reached the target, so the freshness clock records nothing")
}

// TestWriteResults_SilentIgnoresTheHydrateExemption pins that §4.6's guard 1
// does not re-open the flood for one actor.
//
// The guard publishes a hydrating actor WHOLE because a scoped publish would
// advance the device's frame high-water past the hydrate's. A silent pass
// publishes no frame at all, so it advances nothing and the hazard cannot
// arise — while granting the exemption would put every replayed row of exactly
// the actor least able to use them back on the wire.
func TestWriteResults_SilentIgnoresTheHydrateExemption(t *testing.T) {
	p, target := newScopeProducerFixture(t)
	ctx := context.Background()
	p.recordAppliedSeq(3)

	unlock, err := p.lockPersonalActor(ctx, personalActorA)
	require.NoError(t, err)
	unmark := p.markHydrating(personalActorA)
	require.True(t, p.hydrateInFlight(personalActorA))

	touched := substrate.VertexKey("lease", scopedLeaseIDs[1])
	_, err = p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: 12},
		touched, []ruleengine.EvalResult{scopedRowFor(personalActorA, "lease-a", touched)},
		[]string{substrate.VertexKey("identity", personalActorA)}, ScopeSilent())
	require.NoError(t, err)

	unmark()
	unlock()

	assert.Empty(t, target.upsertKeys(), "a hydrate in flight does not un-silence a replayed event")
	assert.Empty(t, target.snapshot())
}

// TestReprojectPersonalActor_SilentPublishesNothingAndStampsNothing is the
// non-CDC entry point's half of the same rule.
//
// ReprojectPersonalActor frames unconditionally — a row it withholds is one the
// device already holds, and the frame naming it is the only thing that keeps the
// device from pruning it. ScopeSilent is where that stops being true: the frame
// would carry a replayed revision, which the device drops like every other
// message of the replay, so the pass is a publication that reaches nobody. And
// because nothing reached the target, nothing may stamp the read model's
// last-touch clock — a stamped non-publication is exactly what silences
// LensProjectionStalled.
func TestReprojectPersonalActor_SilentPublishesNothingAndStampsNothing(t *testing.T) {
	p, target := newScopedPersonalFixture(t)
	require.True(t, p.Progress().LastProjectedAt.IsZero(), "nothing has been published yet")

	require.NoError(t, p.ReprojectPersonalActor(context.Background(), personalActorA, ScopeSilent()))

	assert.Empty(t, target.upsertKeys(), "no row: every one of them is below the device's frame high-water mark")
	assert.Empty(t, target.snapshot(),
		"and no frame — the one scope that withholds it, because a frame at a replayed revision is dropped too")
	assert.True(t, p.Progress().LastProjectedAt.IsZero(),
		"a pass that reached nobody is not output, and stamping it would report freshness the device never got")
}

// TestRebuildWindow_TheLastWindowToCloseAsksThePersonalHealerOnce pins the ask
// to the end of the SILENT SPAN rather than to a drained rescan, through the
// real window accounting every ending runs.
//
// The window opens before the health write, the registration wait, the truncate
// and the consumer reset, and every one of those can abandon the rebuild several
// seconds later. A personal lens has been publishing nothing for that whole
// span, so an abandoned window leaves exactly the rows a drained one would have
// left — stale on every connected device, with no event to correct them and the
// clock's own content cycle a day away. What the ask answers is the silence.
func TestRebuildWindow_TheLastWindowToCloseAsksThePersonalHealerOnce(t *testing.T) {
	t.Run("a rebuild abandoned before its consumer reset asks", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		sink := &countingRebuildSink{}
		p.SetRebuildCompleteSink(sink)

		// A supervisor-less pipeline opens the window and then abandons it at
		// the reset, which is the furthest an abandon gets from the open.
		require.ErrorContains(t, p.Rebuild(context.Background(), false), "no supervisor")

		require.False(t, p.RebuildInFlight(), "the window is shut, so events publish normally again")
		assert.EqualValues(t, 1, sink.asks.Load(),
			"the rebuild did not happen, but the silence did — the devices are owed the content cycle either way")
	})

	t.Run("a drained rebuild asks once, though its signal is ended twice", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		sink := &countingRebuildSink{}
		p.SetRebuildCompleteSink(sink)

		sig := p.beginRebuild()
		sig.drained.Store(true)

		require.True(t, p.endRebuild(sig), "watchRebuildCompletion's drained branch owns the window")
		require.False(t, p.endRebuild(sig), "and its deferred end runs again on the way out")

		assert.EqualValues(t, 1, sink.asks.Load(),
			"one window, one ask — a second would cost the whole population a republish it is not owed")
	})

	t.Run("a superseded finisher asks not at all", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		sink := &countingRebuildSink{}
		p.SetRebuildCompleteSink(sink)

		older := p.beginRebuild()
		p.beginRebuild() // a newer rebuild now owns the window

		require.False(t, p.endRebuild(older))
		assert.EqualValues(t, 0, sink.asks.Load(),
			"the lens is still silent under the newer rescan; that rescan's own end is what will ask")
	})

	t.Run("a non-personal target's rebuild asks nothing", func(t *testing.T) {
		// Its replay rewrites a stored read model, so the replay itself is the
		// repair — and the cycle it would buy republishes every personal lens
		// for every actor, none of it on this rebuild's account.
		p, _ := newScopeProducerFixture(t)
		p.adpt = &recordingAdapter{}
		sink := &countingRebuildSink{}
		p.SetRebuildCompleteSink(sink)

		require.ErrorContains(t, p.Rebuild(context.Background(), false), "no supervisor")

		assert.EqualValues(t, 0, sink.asks.Load())
	})

	t.Run("a deployment with no healer rebuilds anyway", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		p.SetRebuildCompleteSink(nil)

		assert.ErrorContains(t, p.Rebuild(context.Background(), false), "no supervisor",
			"fail-SLOW: with nowhere to record the ask the window still closes")
	})
}

// TestRebuildWindow_ASecondRebuildThatAbandonsLeavesTheFirstsWindowOpen is the
// overlap the pipeline cannot serialize away.
//
// Two paths begin a rebuild without passing through rebuildSerial — the operator
// control op and the taxonomy fan-out — so a second rebuild can begin while an
// earlier rescan is still replaying, and fail at the registration wait, the
// truncate or the reset moments later. Its end must not speak for the lens: the
// earlier replay is still running, and for a personal lens the consequence is
// not a suppressed healer but a resumed FLOOD, every replayed event publishing
// at a revision every device drops. The content cycle it would announce runs at
// the same rewound revision and is dropped too, and the surviving rescan's own
// end is then not the installed rebuild and announces nothing — so the silence
// ends with the devices holding the pre-rebuild shape and no cycle coming.
func TestRebuildWindow_ASecondRebuildThatAbandonsLeavesTheFirstsWindowOpen(t *testing.T) {
	p, _ := newScopeProducerFixture(t)
	sink := &countingRebuildSink{}
	p.SetRebuildCompleteSink(sink)

	live := p.beginRebuild()
	require.True(t, p.RebuildInFlight())

	// The second rebuild opens its own window and abandons it at the reset — the
	// furthest an abandon gets from the open, with no supervisor to reach.
	require.ErrorContains(t, p.Rebuild(context.Background(), false), "no supervisor")

	assert.True(t, p.RebuildInFlight(),
		"the first rescan is still replaying, so the lens is still silent")
	assert.EqualValues(t, 0, sink.asks.Load(),
		"a cycle announced now would run at the replay's rewound revision and reach nobody")

	live.drained.Store(true)
	p.endRebuild(live)

	assert.False(t, p.RebuildInFlight(), "the last window is shut, so events publish normally again")
	assert.EqualValues(t, 1, sink.asks.Load(),
		"one span of silence, one cycle — asked by the rescan that actually ended it")
}
