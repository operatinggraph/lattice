package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
)

// These tests cover A1: a lens refused for an unknown taxonomy expansion
// never reaches the pipeline registry, so remover.remove's take(old.ID)
// lookup (keyed on that registry) always misses it — but CoreKVSource still
// calls remove for its tombstone (dispatchSpec sets s.known regardless of
// whether the load callback's activation attempt succeeded). Both deletion
// triggers must evict the refused-lens registry unconditionally, or a
// deleted lens definition stays queued for retryRefused to resurrect.

// TestRemover_Remove_EvictsRefusedLensEvenWhenNeverRegistered proves the
// tombstone-triggered path: a lens that was refused and never reached the
// pipeline registry (take returns !ok) must still be evicted from
// rl.refused.
func TestRemover_Remove_EvictsRefusedLensEvenWhenNeverRegistered(t *testing.T) {
	rl := &reloader{}
	rl.recordRefusedForTaxonomy(&lens.Rule{ID: "never-registered"})

	rm := &remover{
		logger:       discardLogger(),
		take:         func(string) (*pipelineEntry, bool) { return nil, false },
		unregister:   func(string) {},
		clearRefused: rl.clearRefusedForTaxonomy,
	}

	rm.remove(&lens.Rule{ID: "never-registered"})

	rl.refusedMu.Lock()
	_, stillQueued := rl.refused["never-registered"]
	rl.refusedMu.Unlock()
	assert.False(t, stillQueued, "a tombstoned lens must be evicted from refused even if it never reached the pipeline registry")
}

// TestPipelineDeleter_Delete_EvictsRefusedRegistryUnconditionally proves the
// operator-triggered path (control.Service.deleteRule calls pipelineDeleter.
// Delete directly): the refused-lens registry is evicted regardless of
// whatever else Delete does or how it ends.
func TestPipelineDeleter_Delete_EvictsRefusedRegistryUnconditionally(t *testing.T) {
	rl := &reloader{}
	rl.recordRefusedForTaxonomy(&lens.Rule{ID: "some-lens"})

	kv := startHealthKV(t)
	reporter := health.New(kv, "some-lens")
	p, err := pipeline.New("some-lens", "nats_kv", "CORE", nil, nil, newKVAdapter(t), reporter)
	require.NoError(t, err)

	_, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	close(done) // pre-closed: RunOn/Run were never called, nothing to wait for
	entry := &pipelineEntry{pipeline: p, reporter: reporter, cancel: cancel, done: done}

	d := pipelineDeleter{ruleID: "some-lens", entry: entry, clearRefused: rl.clearRefusedForTaxonomy}
	_ = d.Delete(context.Background())

	rl.refusedMu.Lock()
	_, stillQueued := rl.refused["some-lens"]
	rl.refusedMu.Unlock()
	assert.False(t, stillQueued, "an operator-triggered delete must evict the refused registry")
}

// TestPipelineDeleter_Delete_EvictsTheCapReadCensusOnlyAfterTeardown pins the
// ordering m7 asked about, and the reason the two evictions in Delete sit on
// opposite sides of the teardown.
//
// The registries evicted EARLY (refused-lens, grant-change consumer) fail in the
// direction of a mechanism continuing to drive a lens that is going away, so the
// sooner they stop naming it the better and an abandoned teardown leaves a lens
// nobody re-drives — safe. The cap-read sink census is the mirror image: an
// entry standing there REFUSES the personal derivation licence, so evicting it
// before a teardown that then fails would relicense a narrowing while a
// sink-less producer is still running and still writing grants. That is the
// fail-OPEN direction, so it waits for the run context to be cancelled.
func TestPipelineDeleter_Delete_EvictsTheCapReadCensusOnlyAfterTeardown(t *testing.T) {
	listed := func(ruleID string) bool {
		for _, id := range projection.ReadGrantProducersWithoutSink() {
			if id == ruleID {
				return true
			}
		}
		return false
	}

	newDeleter := func(t *testing.T, ruleID string, teardownFails bool) pipelineDeleter {
		t.Helper()
		kv := startHealthKV(t)
		reporter := health.New(kv, ruleID)
		p, err := pipeline.New(ruleID, "nats_kv", "CORE", nil, nil, newKVAdapter(t), reporter)
		require.NoError(t, err)
		_, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		if !teardownFails {
			close(done) // Run was never started, so there is nothing to wait for
		}
		return pipelineDeleter{ruleID: ruleID, entry: &pipelineEntry{
			pipeline: p, reporter: reporter, cancel: cancel, done: done,
		}}
	}

	t.Run("a teardown that does not complete leaves the census entry standing", func(t *testing.T) {
		const ruleID = "delete-order-stuck"
		installSinklessCapReadProducer(t, ruleID)
		require.True(t, listed(ruleID), "precondition: the producer is counted")

		// A `done` that never closes is a pipeline whose Run has not returned:
		// Delete blocks on it and gives up when the caller's context does, which
		// is the shape a teardown that fails part way leaves behind.
		d := newDeleter(t, ruleID, true)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		require.Error(t, d.Delete(ctx), "precondition: this teardown really did not complete")

		require.True(t, listed(ruleID),
			"the census entry is a REFUSAL — dropping it before the pipeline has actually stopped would relicense the narrowing while a sink-less producer is still writing grants")
	})

	t.Run("a completed teardown evicts it", func(t *testing.T) {
		const ruleID = "delete-order-clean"
		installSinklessCapReadProducer(t, ruleID)
		require.True(t, listed(ruleID))

		d := newDeleter(t, ruleID, false)
		require.NoError(t, d.Delete(context.Background()))
		require.False(t, listed(ruleID),
			"a lens that no longer runs must stop being counted against a consumer deciding what to narrow")
	})
}
