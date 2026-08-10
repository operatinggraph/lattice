package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/rebuildgate"
	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
)

// TestTaxonomyChanged_FanOutStaysWithinTheConcurrencyBound pins the property
// the whole scheduler exists for: ONE taxonomy event over a live corpus of many
// lenses may not put more than taxonomyRebuildConcurrency rebuilds in flight at
// once. Each rebuild is a durable JetStream consumer delete-recreate, so an
// unbounded fan-out is a burst of N of them at the NATS server.
//
// Every entry starts with its rebuild already owed (taxRebuildPending) against
// an UNCHANGED expansion, which is rederiveEntry's "the answer is the same but
// the rebuild behind it never landed" arm — the real fan-out path, reached
// without needing a live pipeline per entry to publish a gate through.
//
// The bound is proven from both sides: the maximum observed concurrency never
// exceeds it, AND it is actually reached (the test waits for saturation before
// releasing anything), so the assertion cannot be satisfied by a scheduler that
// simply runs one rebuild at a time or none at all.
func TestTaxonomyChanged_FanOutStaysWithinTheConcurrencyBound(t *testing.T) {
	const lensCount = 20
	require.Greater(t, lensCount, taxonomyRebuildConcurrency,
		"the corpus must exceed the bound or there is nothing to bound")

	resolver := taxonomy.New()
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: "meta-location", CanonicalName: "location", Abstract: true},
		{ID: "meta-unit", CanonicalName: "unit", SubtypeOf: []string{"location"}},
	})
	resolver.SetArmed(true)
	labels := map[string]struct{}{"location": {}}
	expanded, _, status, _ := resolver.Expand(labels)
	require.Equal(t, taxonomy.StatusArmed, status)

	entries := make([]*pipelineEntry, 0, lensCount)
	for i := 0; i < lensCount; i++ {
		entries = append(entries, &pipelineEntry{
			// pipeline stays nil: the unchanged-but-pending arm publishes no
			// gate, and the rebuild goes through rl.rebuildPipeline below.
			rule:                 &lens.Rule{ID: fmt.Sprintf("lens-fanout-%02d", i)},
			taxExpansionLabels:   labels,
			taxExpansion:         expanded,
			taxExpansionStatus:   status,
			taxExpansionResolved: expanded,
			taxRebuildPending:    true,
		})
	}

	var inFlight, maxInFlight, completed atomic.Int64
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(lensCount)

	rl := &reloader{
		ctx:         context.Background(),
		logger:      discardLogger(),
		resolver:    resolver,
		liveEntries: func() []*pipelineEntry { return entries },
		rebuildPipeline: func(*pipelineEntry, bool) error {
			cur := inFlight.Add(1)
			for {
				peak := maxInFlight.Load()
				if cur <= peak || maxInFlight.CompareAndSwap(peak, cur) {
					break
				}
			}
			<-release
			inFlight.Add(-1)
			completed.Add(1)
			wg.Done()
			return nil
		},
	}

	// Returns while every rebuild is still blocked on `release` — the enqueue
	// cannot be allowed to apply backpressure to CoreKVSource's single dispatch
	// goroutine, which is the one calling this.
	rl.taxonomyChanged()

	mcPollUntil(t, 5*time.Second, func() bool {
		return maxInFlight.Load() >= int64(taxonomyRebuildConcurrency)
	}, "the scheduler never saturated its worker pool")

	close(release)
	mcPollUntil(t, 5*time.Second, func() bool {
		return completed.Load() == lensCount
	}, "not every queued rebuild ran")
	wg.Wait()

	assert.EqualValues(t, taxonomyRebuildConcurrency, maxInFlight.Load(),
		"one taxonomy event may never put more than %d rebuilds in flight at once", taxonomyRebuildConcurrency)
	for _, entry := range entries {
		entry.taxMu.Lock()
		pending, running := entry.taxRebuildPending, entry.taxRebuildRunning
		entry.taxMu.Unlock()
		assert.False(t, pending, "%s: a successful rebuild clears the pending flag", entry.rule.ID)
		assert.False(t, running, "%s: the single-flight latch must be handed back", entry.rule.ID)
	}
}

// TestRebuildGate_TaxonomyAndControlPathsShareOneBound pins the reason the gate
// is a shared instance rather than one per path.
//
// A rebuild is a durable JetStream delete-recreate whichever path asked for it,
// so a bound held separately by the reload scheduler and by the operator control
// plane leaves their SUM unbounded. Both are wired to one gate in main.go, and
// this drives both ends for real — a taxonomy rebuild held open while the
// "rebuild" control op is issued over NATS for the same lens — to prove the
// control path waits rather than reproducing the burst the scheduler exists to
// prevent.
func TestRebuildGate_TaxonomyAndControlPathsShareOneBound(t *testing.T) {
	const ruleID = "lens-shared-gate"

	_, nc := natsfixture.Server(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	gate := rebuildgate.New(taxonomyRebuildConcurrency)

	var mu sync.Mutex
	var inFlight, peak int
	var order []string
	release := make(chan struct{})

	enter := func(who string) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		order = append(order, who)
		mu.Unlock()
	}
	leave := func() {
		mu.Lock()
		inFlight--
		mu.Unlock()
	}
	ran := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), order...)
	}

	rl := &reloader{
		ctx:    ctx,
		logger: discardLogger(),
		rebuildPipeline: func(*pipelineEntry, bool) error {
			enter("taxonomy")
			<-release
			leave()
			return nil
		},
	}
	rl.taxRebuild.settle = time.Millisecond
	rl.taxRebuild.setGate(gate)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetRebuildGate(gate)
	svc.RegisterRebuilder(ruleID, sharedGateRebuilder{enter: enter, leave: leave})
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	entry := &pipelineEntry{rule: &lens.Rule{ID: ruleID}}
	markTaxonomyRebuildPending(entry, false)
	rl.startTaxonomyRebuild(entry, ruleID, "shared-gate")
	mcPollUntil(t, 5*time.Second, func() bool { return len(ran()) == 1 },
		"the taxonomy rebuild never started")

	body, err := json.Marshal(map[string]any{})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject(ruleID, "rebuild"), body, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	require.Empty(t, resp.Error)
	require.NotNil(t, resp.Rebuild)
	require.True(t, resp.Rebuild.Started, "the control op still acks immediately — it queues, it does not refuse")

	require.Never(t, func() bool { return len(ran()) > 1 }, 200*time.Millisecond, 5*time.Millisecond,
		"the control plane must not rebuild a lens the reload path is already rebuilding")

	close(release)
	mcPollUntil(t, 5*time.Second, func() bool { return len(ran()) == 2 },
		"the control rebuild never ran once the taxonomy rebuild released the lens")

	assert.Equal(t, []string{"taxonomy", "control"}, ran(),
		"the control rebuild runs AFTER the taxonomy one, not instead of it — the gate serializes, it never coalesces")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, peak, "one lens may never be rebuilt by two paths at once")
}

// sharedGateRebuilder is the control-plane end of the shared-gate test: a
// Rebuilder that records its call against the same counters the reload path
// writes, so "at once" is measured across both paths rather than within one.
type sharedGateRebuilder struct {
	enter func(string)
	leave func()
}

func (r sharedGateRebuilder) Rebuild(context.Context, bool) error {
	r.enter("control")
	r.leave()
	return nil
}

func (r sharedGateRebuilder) RebuildAndWait(context.Context, bool, time.Duration) error {
	r.enter("control")
	r.leave()
	return nil
}

// TestTaxonomyRebuildScheduler_ShutdownAbandonsQueuedJobs pins the drain half
// of the scheduler's state table: on ctx cancellation a queued job is never
// run, but its entry's single-flight latch IS handed back — leaving
// taxRebuildPending set, which is the truthful record that the rebuild is still
// owed and the next taxonomy event must drive it.
func TestTaxonomyRebuildScheduler_ShutdownAbandonsQueuedJobs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queued := &pipelineEntry{rule: &lens.Rule{ID: "lens-queued"}}

	release := make(chan struct{})
	var started, ranQueued atomic.Int64

	rl := &reloader{
		ctx:    ctx,
		logger: discardLogger(),
		rebuildPipeline: func(entry *pipelineEntry, _ bool) error {
			if entry == queued {
				ranQueued.Add(1)
				return nil
			}
			started.Add(1)
			<-release
			return nil
		},
	}

	// Occupy every worker slot, so the entry enqueued next can only be sitting
	// in the queue when the cancellation arrives.
	for i := 0; i < taxonomyRebuildConcurrency; i++ {
		blocker := &pipelineEntry{rule: &lens.Rule{ID: fmt.Sprintf("lens-blocker-%d", i)}}
		markTaxonomyRebuildPending(blocker, false)
		rl.startTaxonomyRebuild(blocker, blocker.rule.ID, "blocker")
	}
	mcPollUntil(t, 5*time.Second, func() bool {
		return started.Load() == int64(taxonomyRebuildConcurrency)
	}, "the worker pool never filled")

	markTaxonomyRebuildPending(queued, false)
	rl.startTaxonomyRebuild(queued, queued.rule.ID, "queued")

	// The cancellation reaches the scheduler on its own watcher goroutine, so
	// the workers are only unblocked once it has actually landed — otherwise a
	// worker could pick the queued job up before the drain began, and what this
	// test pins is the behaviour of a job still queued at that moment.
	cancel()
	mcPollUntil(t, 5*time.Second, func() bool {
		rl.taxRebuild.mu.Lock()
		defer rl.taxRebuild.mu.Unlock()
		return rl.taxRebuild.stopped
	}, "the context cancellation never reached the scheduler")
	close(release)

	mcPollUntil(t, 5*time.Second, func() bool {
		queued.taxMu.Lock()
		defer queued.taxMu.Unlock()
		return !queued.taxRebuildRunning
	}, "an abandoned job must hand back its single-flight latch")

	queued.taxMu.Lock()
	pending := queued.taxRebuildPending
	queued.taxMu.Unlock()
	assert.True(t, pending, "the abandoned rebuild is still owed, and that must stay recorded")
	assert.Zero(t, ranQueued.Load(), "a job queued when the scheduler drains must not run")
}

// TestTaxonomyRebuildScheduler_SettlesBeforeRunning pins the coalescing window.
//
// One logical taxonomy change arrives as a burst — a re-parent is a link delete
// followed by a link create, and between them the subtype is detached from
// everything. A rebuild that ran on that intermediate state would truncate and
// re-derive against a taxonomy nobody intended, then do it again for the real
// one. So a queued job waits for the queue to go quiet, and a publication
// landing inside that window is absorbed into the SAME pass.
func TestTaxonomyRebuildScheduler_SettlesBeforeRunning(t *testing.T) {
	const settle = 80 * time.Millisecond

	entry := &pipelineEntry{rule: &lens.Rule{ID: "lens-settle"}}
	started := make(chan time.Time, 4)
	var calls atomic.Int64

	rl := &reloader{
		ctx:    context.Background(),
		logger: discardLogger(),
		rebuildPipeline: func(*pipelineEntry, bool) error {
			calls.Add(1)
			started <- time.Now()
			return nil
		},
	}
	rl.taxRebuild.settle = settle

	// The first half of the change.
	markTaxonomyRebuildPending(entry, false)
	rl.startTaxonomyRebuild(entry, entry.rule.ID, "settle")
	// The second half, inside the window: the entry is already latched, so this
	// republishes the gate rather than queueing a second job.
	markTaxonomyRebuildPending(entry, false)
	enqueuedAt := time.Now()

	var ranAt time.Time
	select {
	case ranAt = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the settled rebuild never ran")
	}

	assert.GreaterOrEqual(t, ranAt.Sub(enqueuedAt), settle,
		"a job must not run before the queue has been quiet for the settle window — otherwise it rebuilds against the burst's intermediate state")

	mcPollUntil(t, 5*time.Second, func() bool {
		entry.taxMu.Lock()
		defer entry.taxMu.Unlock()
		return !entry.taxRebuildRunning
	}, "the settled rebuild never handed back its latch")
	assert.EqualValues(t, 1, calls.Load(),
		"both halves of the burst must be absorbed into one pass, not rebuilt once each")
}

// TestDriveTaxonomyRebuild_ShrinkLandingMidRebuildStillTruncates pins that the
// truncate a shrink owes travels with the gate generation it belongs to.
//
// The flag and the taxGen bump are committed by ONE critical section
// (markTaxonomyRebuildPending) precisely so this sequence cannot lose it: a
// rebuild is already in flight for generation G when a shrink publishes G+1 and
// asks for a truncate. If the worker could observe the advanced generation while
// the flag was still uncommitted, it would compute "not superseded", take the
// success arm, and clear both pending and truncate — retiring a truncate that
// never ran, so the shrink silently fails to retract.
func TestDriveTaxonomyRebuild_ShrinkLandingMidRebuildStillTruncates(t *testing.T) {
	entry := &pipelineEntry{rule: &lens.Rule{ID: "lens-midflight"}}

	arrived := make(chan struct{}, 4)
	release := make(chan struct{})
	var mu sync.Mutex
	var sawTruncate []bool

	rl := &reloader{
		ctx:    context.Background(),
		logger: discardLogger(),
		rebuildPipeline: func(_ *pipelineEntry, truncate bool) error {
			mu.Lock()
			sawTruncate = append(sawTruncate, truncate)
			first := len(sawTruncate) == 1
			mu.Unlock()
			arrived <- struct{}{}
			if first {
				<-release
			}
			return nil
		},
	}
	rl.taxRebuild.settle = time.Millisecond

	// The gate in force when the rebuild starts: no shrink, no truncate.
	markTaxonomyRebuildPending(entry, false)
	rl.startTaxonomyRebuild(entry, entry.rule.ID, "midflight")
	<-arrived

	// The shrink lands while that rebuild is still running.
	markTaxonomyRebuildPending(entry, true)
	close(release)

	mcPollUntil(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(sawTruncate) == 2
	}, "the superseded rebuild never made a second pass for the newer gate")

	mu.Lock()
	got := append([]bool(nil), sawTruncate...)
	mu.Unlock()
	assert.Equal(t, []bool{false, true}, got,
		"the pass for the shrink's gate must truncate — the first pass answered a gate that predates it")

	mcPollUntil(t, 5*time.Second, func() bool {
		entry.taxMu.Lock()
		defer entry.taxMu.Unlock()
		return !entry.taxRebuildRunning
	}, "the entry never handed back its single-flight latch")
	entry.taxMu.Lock()
	defer entry.taxMu.Unlock()
	assert.False(t, entry.taxRebuildPending)
	assert.False(t, entry.taxRebuildTruncate, "only a rebuild that actually truncated may retire the flag")
}
