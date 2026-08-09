package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
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
