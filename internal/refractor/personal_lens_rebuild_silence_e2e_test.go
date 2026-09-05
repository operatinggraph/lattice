// Package refractor_test — T8 of personal-lens-delta-publication-design.md
// §4.5: a personal lens's rebuild replays every Core KV entry at its ORIGINAL
// revision, and every message that replay would publish sits below the frame
// high-water mark a connected device already holds, so the device drops all of
// it. The lens therefore publishes NOTHING for the length of the rescan, and its
// completion asks the standing healer for one content cycle — the rebuilt shape
// reaches the device once, at a live revision.
package refractor_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/grantchange"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// countingContentCycleSink is the standing personal healer as a rebuilding
// pipeline sees it (pipeline.RebuildCompleteSink), counting the asks so
// "exactly once" is assertable.
//
// to is the healer the ask is passed on to, for the test that drives a real
// PersonalSweeper and needs the request to land before it sweeps; nil where the
// count alone is the observation. Forwarded FIRST, so an observed count means
// the healer already holds the request.
type countingContentCycleSink struct {
	to   pipeline.RebuildCompleteSink
	asks atomic.Int64
}

func (s *countingContentCycleSink) RequestContentCycle() {
	if s.to != nil {
		s.to.RequestContentCycle()
	}
	s.asks.Add(1)
}

// syncSubjectDepth is how many messages the SYNC stream holds for one actor's
// subject — the wire's own count, taken from the server rather than from
// anything this test published.
//
// It is what makes "the replay published nothing" an EXACT assertion rather
// than a timeout: the count is read before the rebuild and again once the
// rescan has drained, and the two must be equal.
func syncSubjectDepth(t *testing.T, h *pl2Harness, subject string) uint64 {
	t.Helper()
	stream, err := h.js.Stream(h.ctx, "SYNC")
	require.NoError(t, err)
	info, err := stream.Info(h.ctx, jetstream.WithSubjectFilter(subject))
	require.NoError(t, err)
	return info.State.Subjects[subject]
}

// replayDeliveries is how many messages this lens's durable has been handed
// since it was last created — the positive control for "the replay published
// nothing".
//
// A rebuild deletes and recreates the durable, so after one the count is exactly
// the number of Core KV entries the rescan re-delivered to the write loop. It is
// what separates a lens that received the whole replay and withheld every
// message from one that received nothing at all: a consumer filter narrowed to
// match no subject would satisfy an equal-subject-depth assertion vacuously, and
// the two are indistinguishable on the wire.
func replayDeliveries(t *testing.T, h *pl2Harness, lensID string) uint64 {
	t.Helper()
	spec := e2eSpec(lensID, "core-kv")
	cons, err := h.js.Consumer(h.ctx, spec.Stream, spec.Name)
	require.NoError(t, err)
	info, err := cons.Info(h.ctx)
	require.NoError(t, err)
	return info.Delivered.Consumer
}

// rebuildingPersonalLens activates a personal lens whose rebuild has an
// observable end. The rebuild lifecycle lives on the health entry: Rebuild
// launches its completion watcher only for a reporting pipeline, and that
// watcher is what closes the silent window.
func rebuildingPersonalLens(t *testing.T, h *pl2Harness, lensID, healthBucket string) *pipeline.Pipeline {
	t.Helper()
	_, err := h.js.CreateKeyValue(h.ctx, jetstream.KeyValueConfig{Bucket: healthBucket})
	require.NoError(t, err)
	healthKV, err := h.conn.OpenKV(h.ctx, healthBucket)
	require.NoError(t, err)

	cypher := `MATCH (identity {key: $actorKey})-[:holds]->(l:lease) ` +
		`RETURN l.key AS anchor, "lease" AS kind, l.id AS entityId, l.monthlyRent AS monthlyRent`
	p, _ := activatePersonalLensReporting(t, h, lensID, cypher, []string{"entityId"}, nil,
		health.New(healthKV, lensID))
	return p
}

// seedPersonalActorRows writes one identity holding three leases and returns the
// entity ids the lens projects for it.
//
// Three rows rather than one: a whole-actor republish and a frames-only pass are
// the same message count for a one-row actor, so a single-lease fixture could
// not tell a content cycle from an ordinary one.
func seedPersonalActorRows(t *testing.T, h *pl2Harness, identityID, seed string) []string {
	t.Helper()
	writePL2Vertex(t, h, substrate.VertexKey("identity", identityID), "identity",
		map[string]any{"name": "tenant"})
	entities := make([]string, 0, 3)
	for i, tag := range []string{"one", "two", "three"} {
		leaseID := pl2NanoID(seed + "-lease-" + tag)
		entities = append(entities, "lease-"+tag)
		writePL2Vertex(t, h, substrate.VertexKey("lease", leaseID), "lease",
			map[string]any{"id": entities[i], "monthlyRent": 1000 + i})
		writePL2Link(t, h, "identity", identityID, "holds", "lease", leaseID)
	}
	return entities
}

// sweepOneCycle drives the sweeper until it has closed a whole cycle over the
// identity population, which with a batch wider than that population is one
// Sweep. The ticks are driven from the test because a free-running sweeper
// republishes a frame for every identity on every tick, so a drain-until-quiet
// against one never goes quiet.
func sweepOneCycle(t *testing.T, h *pl2Harness, s *grantchange.PersonalSweeper) {
	t.Helper()
	before := s.CycleCompletedAt()
	for range 20 {
		s.Sweep(h.ctx)
		if s.CycleCompletedAt().After(before) {
			return
		}
	}
	t.Fatal("the sweep did not close a cycle over the identity population within 20 ticks")
}

// TestPersonalLens_RebuildPublishesNothingAndAsksForAContentCycle drives a real
// Rebuild against a live personal lens with a multi-row actor.
func TestPersonalLens_RebuildPublishesNothingAndAsksForAContentCycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	h := newPL2Harness(t)

	lensID := pl2NanoID("rebuild-silence-lens")
	identityID := pl2NanoID("rebuild-silence-identity")
	identityKey := substrate.VertexKey("identity", identityID)
	subject := "lattice.sync.user." + identityID

	p := rebuildingPersonalLens(t, h, lensID, "HEALTH-rebuild-silence")

	sink := &countingContentCycleSink{}
	p.SetRebuildCompleteSink(sink)

	cons := pl3Consumer(t, h, identityID)
	entities := seedPersonalActorRows(t, h, identityID, "rebuild-silence")

	// The barrier is the EFFECT: the lens's latest authoritative frame naming
	// all three rows, then a quiet subject.
	deltaDrainUntilFrame(t, cons, lensID, entities)
	require.Empty(t, drainBriefly(t, cons), "the fixture must be quiet before the rebuild is measured")

	depthBefore := syncSubjectDepth(t, h, subject)
	require.NotZero(t, depthBefore, "the actor's subject must hold the pre-rebuild publication, or nothing is being measured")

	// The rescan. It resets the durable to DeliverLastPerSubject, so every Core
	// KV entry this lens's filter admits — the identity, the three leases, the
	// three links — is redelivered to the write loop at its original sequence.
	require.NoError(t, p.Rebuild(context.Background(), false))

	// Completion, read from the mechanism rather than waited out: the sink is
	// invoked by the watcher that observed the consumer fully drained, so a
	// count of one IS the end of the rebuild window.
	require.Eventually(t, func() bool { return sink.asks.Load() == 1 }, 60*time.Second, 20*time.Millisecond,
		"the rebuild's completion must ask the standing healer for exactly one content cycle")
	require.False(t, p.RebuildInFlight(), "the announcement follows the flag being cleared")

	assert.Equal(t, depthBefore, syncSubjectDepth(t, h, subject),
		"the replay must have published nothing: every message it would have sent is below the device's frame high-water mark")
	assert.Empty(t, drainBriefly(t, cons), "and the device received nothing")

	// The positive control for the absence above. A lens whose recreated durable
	// admitted no subject at all would leave the subject depth equally unchanged,
	// and the two are the same observation on the wire — so the replay has to be
	// shown to have REACHED the write loop, at least once per row this actor
	// holds, and been withheld there.
	assert.GreaterOrEqual(t, replayDeliveries(t, h, lensID), uint64(len(entities)),
		"the rescan must have re-delivered the actor's entries; an equal subject depth over an empty replay proves nothing")

	// The lens is live again, and a post-rebuild event publishes normally — the
	// silence is the rebuild window's, not a latch the rebuild leaves behind.
	// Without this the assertions above would hold for a lens that had simply
	// stopped publishing.
	writePL2Vertex(t, h, identityKey, "identity", map[string]any{"name": "tenant renamed"})
	after := drainBriefly(t, cons)
	require.NotEmpty(t, after, "a live event after the rebuild must reach the device")
	assert.Greater(t, syncSubjectDepth(t, h, subject), depthBefore)
	assert.EqualValues(t, 1, sink.asks.Load(), "and a live event asks for no content cycle of its own")
}

// TestPersonalLens_TheHealersCyclesAreSilentInsideTheRebuildWindow is the
// window's OTHER publisher.
//
// The CDC write loop goes quiet for the length of a rescan, but the standing
// healer is not on that loop: it drives every registered personal lens through
// ReprojectPersonalActor on its own clock — ScopeAll on a content cycle,
// ScopeNone on every other — and it knows nothing about which of those lenses is
// currently replaying. Measured on the dev stack: one boot content cycle reached
// the widest actor and published that actor's whole row set plus its frame
// through the one rebuilding pipeline of fifteen, at a revision three orders of
// magnitude below the live head, every message of it dropped on the devices' own
// frameHW and resurrection guards. So the window has the last word at that entry
// point too, and this drives a REAL PersonalSweeper across a real rebuild to
// prove it on the wire.
//
// The window is held open by the completion watcher's own poll clock, which is
// captured at pipeline construction: a poll interval longer than this test means
// the first poll — the only thing that can observe the rescan drained and close
// the window — never comes. That the window really was open across the whole
// measurement is asserted at both ends, with the un-asked content cycle as a
// third witness.
//
// What happens once the window CLOSES is the test below: the rows withheld here
// reach the device on the cycle that close asks for, at a live revision.
func TestPersonalLens_TheHealersCyclesAreSilentInsideTheRebuildWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	origPoll := pipeline.RebuildPollInterval
	pipeline.RebuildPollInterval = 10 * time.Minute
	t.Cleanup(func() { pipeline.RebuildPollInterval = origPoll })

	h := newPL2Harness(t)

	lensID := pl2NanoID("rebuild-heal-silence-lens")
	identityID := pl2NanoID("rebuild-heal-silence-identity")
	subject := "lattice.sync.user." + identityID

	p := rebuildingPersonalLens(t, h, lensID, "HEALTH-rebuild-heal-silence")

	// The healer as cmd/refractor assembles it, the same wiring the delivery arm
	// below uses.
	reprojector := grantchange.New()
	reprojector.RegisterPersonal(lensID, p)
	sweeper := grantchange.NewPersonalSweeper(reprojector, h.coreKV, nil)
	sweeper.SetBounds(100, 0)
	sink := &countingContentCycleSink{to: sweeper}
	p.SetRebuildCompleteSink(sink)

	cons := pl3Consumer(t, h, identityID)
	entities := seedPersonalActorRows(t, h, identityID, "rebuild-heal-silence")
	deltaDrainUntilFrame(t, cons, lensID, entities)
	require.Empty(t, drainBriefly(t, cons), "the fixture must be quiet before the cycles are measured")

	// The positive control, taken FIRST: cycle 1 is the boot content cycle, which
	// is exactly the pass the live measurement caught republishing a whole actor
	// into a rewound cursor. Without seeing it do its job here, every silence
	// below would be satisfied by a sweeper that reaches this lens never.
	sweepOneCycle(t, h, sweeper)
	healed := drainUntilQuiet(t, cons)
	assert.ElementsMatch(t, entities, upsertKeys(healed, lensID),
		"the healer's content cycle republishes the whole actor while the lens is itself")
	require.Len(t, frames(healed, lensID), 1, "under its authoritative frame")

	depthBefore := syncSubjectDepth(t, h, subject)
	require.NotZero(t, depthBefore, "the actor's subject must hold the pre-rebuild publication, or nothing is being measured")

	require.NoError(t, p.Rebuild(context.Background(), false))
	require.True(t, p.RebuildInFlight(), "the rescan's window is open, which is the condition under test")

	// Both cadences, inside the window: the content cycle a request buys, and the
	// ordinary frames-only pass that follows it. ScopeNone already withholds the
	// rows; what the window adds is the frame, which at a replayed revision
	// retracts nothing on any device and costs one message per swept actor.
	sweeper.RequestContentCycle()
	sweepOneCycle(t, h, sweeper)
	sweepOneCycle(t, h, sweeper)

	assert.Equal(t, depthBefore, syncSubjectDepth(t, h, subject),
		"neither cycle put a message on the actor's subject: both would have published at the replay's rewound revision")
	assert.Empty(t, drainBriefly(t, cons), "and the device received nothing")
	require.True(t, p.RebuildInFlight(),
		"the window was open across the whole measurement, so the silence is the window's rather than the lens having died")
	assert.EqualValues(t, 0, sink.asks.Load(),
		"a window that never closed asks for nothing — the third witness that it stayed open")

	// The replay has to be shown to have REACHED the write loop and been withheld
	// there: a recreated durable admitting no subject at all would leave the
	// subject depth equally unchanged, and the two are the same observation on
	// the wire.
	assert.GreaterOrEqual(t, replayDeliveries(t, h, lensID), uint64(len(entities)),
		"the rescan must have re-delivered the actor's entries; an equal subject depth over an empty replay proves nothing")
}

// TestPersonalLens_TheRequestedContentCycleDeliversTheRebuiltShape is T8's
// DELIVERY arm: the same seam, driven through a REAL standing healer, asserted
// on what the device receives rather than on the request being made.
//
// The counting sink above proves the ask happens. It cannot prove the ask is
// worth anything — a request the sweeper consumed on a cycle that published
// nothing, or one spent on a cycle the clock had already made a content cycle,
// counts exactly the same. So this wires the pipeline to a live PersonalSweeper
// the way cmd/refractor's registerPersonalHealer does and measures the actor's
// SYNC subject: the rows the device stopped receiving during the replay arrive
// on the cycle after the rebuild, at a live revision, with the authoritative
// frame that makes them safe to keep.
//
// The frames-only baseline cycle is what makes the content attributable. The
// first cycle after a sweeper is built is a content cycle on the clock's own
// rule, so a test that swept once and then rebuilt would be measuring the boot
// rule; the cycle in between establishes the quiet the request breaks.
func TestPersonalLens_TheRequestedContentCycleDeliversTheRebuiltShape(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	h := newPL2Harness(t)

	lensID := pl2NanoID("rebuild-delivery-lens")
	identityID := pl2NanoID("rebuild-delivery-identity")
	subject := "lattice.sync.user." + identityID

	p := rebuildingPersonalLens(t, h, lensID, "HEALTH-rebuild-delivery")

	// The healer as cmd/refractor assembles it: the Reprojector's personal
	// registry is the population the sweep re-drives, and the sweeper is the
	// sink the lens's rebuild talks back to.
	reprojector := grantchange.New()
	reprojector.RegisterPersonal(lensID, p)
	sweeper := grantchange.NewPersonalSweeper(reprojector, h.coreKV, nil)
	sweeper.SetBounds(100, 0)
	sink := &countingContentCycleSink{to: sweeper}
	p.SetRebuildCompleteSink(sink)

	cons := pl3Consumer(t, h, identityID)
	entities := seedPersonalActorRows(t, h, identityID, "rebuild-delivery")
	deltaDrainUntilFrame(t, cons, lensID, entities)
	require.Empty(t, drainBriefly(t, cons), "the fixture must be quiet before the cycles are measured")

	// Cycle 1 is the boot content cycle.
	sweepOneCycle(t, h, sweeper)
	drainUntilQuiet(t, cons)

	// Cycle 2 is the baseline: inside the content-heal interval, so the frame
	// alone. Both halves are asserted, because a cycle that published nothing at
	// all would also read as "no rows".
	depthBeforeBaseline := syncSubjectDepth(t, h, subject)
	sweepOneCycle(t, h, sweeper)
	baseline := drainUntilQuiet(t, cons)
	assert.Empty(t, upsertKeys(baseline, lensID), "the cycle before the rebuild republishes no row")
	require.Len(t, frames(baseline, lensID), 1, "and does publish its authoritative frame")
	require.Equal(t, depthBeforeBaseline+1, syncSubjectDepth(t, h, subject),
		"one frame, no rows — the quiet the request has to break")

	depthBeforeRebuild := syncSubjectDepth(t, h, subject)
	revisionBeforeRebuild := p.Progress().LastAppliedSeq
	require.NotZero(t, revisionBeforeRebuild, "the lens holds an ordering token, or no frame it publishes can be applied")

	require.NoError(t, p.Rebuild(context.Background(), false))
	require.Eventually(t, func() bool { return sink.asks.Load() == 1 }, 60*time.Second, 20*time.Millisecond,
		"the rebuild's window must close with exactly one ask on the standing healer")
	require.Equal(t, depthBeforeRebuild, syncSubjectDepth(t, h, subject),
		"and the replay itself published nothing, which is what leaves the device owing")

	// The delivery. Nothing here asks for a content cycle — the request the
	// rebuild made is the only thing that can make this one carry rows.
	sweepOneCycle(t, h, sweeper)
	delivered := drainUntilQuiet(t, cons)

	assert.ElementsMatch(t, entities, upsertKeys(delivered, lensID),
		"every row the replay withheld reaches the device on the requested cycle")
	got := frames(delivered, lensID)
	require.Len(t, got, 1)
	assert.ElementsMatch(t, entities, got[0],
		"with the authoritative frame naming them, or the device prunes what it was just sent")
	assert.Equal(t, depthBeforeRebuild+uint64(len(entities))+1, syncSubjectDepth(t, h, subject),
		"the actor's rows and one frame, and nothing else")

	for _, env := range delivered {
		revision, ok := env["revision"].(float64)
		require.True(t, ok, "every published envelope carries the revision the client applies it at")
		assert.GreaterOrEqual(t, uint64(revision), revisionBeforeRebuild,
			"published at a LIVE revision — a message below the device's frame high-water mark is dropped there, which is the whole reason the replay published nothing")
	}
}
