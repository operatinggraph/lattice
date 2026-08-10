package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// These tests cover the taxonomy re-derivation seam (dynamic-type-taxonomy-
// design.md §14 Fire A item 4, §17.6): reloader.taxonomyChanged and its
// helpers. main.go's actual wiring (startPipeline, the CoreKVSource taxonomy
// callbacks) is not reachable from a test — it lives inside main()'s single
// closure body — so these exercise the decision logic directly, the same way
// reload_test.go's TestReloaderUpdate_* suite exercises reloader.update
// without running cmd/refractor's real main().

// TestRederiveEntry_NoOpWhenExpansionUnchanged proves rederiveEntry's
// no-change guard runs BEFORE it ever touches entry.pipeline: with a nil
// pipeline, any actual call to UseFullEngineBranches/Rebuild would panic, so
// a clean return here is only possible if the equality check short-circuited
// first (dynamic-type-taxonomy-design.md §14 Fire A item 4: "a taxonomy
// change that does not alter a lens's expanded set triggers no
// re-derivation").
func TestRederiveEntry_NoOpWhenExpansionUnchanged(t *testing.T) {
	resolver := taxonomy.New()
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: "meta-location", CanonicalName: "location", Abstract: true},
		{ID: "meta-unit", CanonicalName: "unit", Abstract: false, SubtypeOf: []string{"location"}},
	})
	resolver.SetArmed(true)

	labels := map[string]struct{}{"location": {}}
	expanded, _, status, _ := resolver.Expand(labels)
	require.Equal(t, taxonomy.StatusArmed, status)
	require.Equal(t, map[string]map[string]struct{}{"location": {"unit": {}}}, expanded)

	entry := &pipelineEntry{
		rule:               &lens.Rule{ID: "lens-noop"},
		taxExpansionLabels: labels,
		taxExpansion:       expanded,
		taxExpansionStatus: status,
		// pipeline stays nil on purpose — see the doc above.
	}
	rl := &reloader{resolver: resolver, logger: discardLogger()}

	require.NotPanics(t, func() { rl.rederiveEntry(entry) })
}

// TestRederiveEntry_EmptyExpansionLabelsIsAlwaysANoOp proves a plain lens
// (no `*` anywhere, taxExpansionLabels empty) is never a re-derivation
// candidate, regardless of what the resolver answers — mirroring
// unionExpansionLabels' own doc.
func TestRederiveEntry_EmptyExpansionLabelsIsAlwaysANoOp(t *testing.T) {
	rl := &reloader{resolver: taxonomy.New(), logger: discardLogger()}
	entry := &pipelineEntry{rule: &lens.Rule{ID: "plain-lens"}}
	require.NotPanics(t, func() { rl.rederiveEntry(entry) })
}

// TestReloader_RetriesRefusedLensesAndClearsOnSuccess proves the
// refused-lens registry seam §17.6 named as absent: a lens recorded as
// refused for an unknown taxonomy expansion is retried on the next
// taxonomyChanged call, and a successful retry (which — mirroring main.go's
// startPipeline — clears itself) is not retried again on the call after
// that.
func TestReloader_RetriesRefusedLensesAndClearsOnSuccess(t *testing.T) {
	rl := &reloader{liveEntries: func() []*pipelineEntry { return nil }}

	var calls []string
	rl.activateForTaxonomy = func(r *lens.Rule) {
		calls = append(calls, r.ID)
		rl.clearRefusedForTaxonomy(r.ID)
	}

	rl.recordRefusedForTaxonomy(&lens.Rule{ID: "refused-lens"})

	rl.taxonomyChanged()
	assert.Equal(t, []string{"refused-lens"}, calls, "the refused lens must be retried on the next taxonomy event")

	rl.taxonomyChanged()
	assert.Equal(t, []string{"refused-lens"}, calls, "a lens that already activated must not be retried again")
}

// TestReloader_RetryFailureKeepsTheLensQueued mirrors the previous test but
// has the retry keep failing — the lens must stay in refused across
// multiple taxonomy events, since nothing else will ever retry it.
func TestReloader_RetryFailureKeepsTheLensQueued(t *testing.T) {
	rl := &reloader{liveEntries: func() []*pipelineEntry { return nil }}

	var calls int
	rl.activateForTaxonomy = func(r *lens.Rule) {
		calls++
		// Deliberately does NOT clear — simulates activation refusing again.
	}

	rl.recordRefusedForTaxonomy(&lens.Rule{ID: "still-broken"})

	rl.taxonomyChanged()
	rl.taxonomyChanged()
	rl.taxonomyChanged()
	assert.Equal(t, 3, calls, "each taxonomy event retries an entry that has not cleared itself")
}

// TestReloader_ActivateForTaxonomy_MustBeGuardedAgainstDoubleActivation
// documents the contract C3 requires of whatever main.go wires into
// rl.activateForTaxonomy: it must be the SAME existence-guarded entry point
// src.SetLoadCallback uses for a first load (main.go's activateIfNotRegistered
// closure), never the bare unguarded activation function — retryRefused has
// no existence check of its own, so if activateForTaxonomy skipped that
// guard, a retry racing a concurrent first-load could register two
// pipelines for one lens ID. main() itself is not unit-testable (its wiring
// lives inside one closure body), so this pins the CONTRACT at the
// reloader level: a correctly-guarded activateForTaxonomy must be a no-op
// for an ID retryRefused's caller already considers live.
func TestReloader_ActivateForTaxonomy_MustBeGuardedAgainstDoubleActivation(t *testing.T) {
	registered := map[string]bool{"already-live": true}
	activated := 0
	guardedActivate := func(r *lens.Rule) {
		if registered[r.ID] {
			return
		}
		activated++
		registered[r.ID] = true
	}

	rl := &reloader{
		liveEntries:         func() []*pipelineEntry { return nil },
		activateForTaxonomy: guardedActivate,
	}
	rl.recordRefusedForTaxonomy(&lens.Rule{ID: "already-live"})

	rl.taxonomyChanged()

	assert.Zero(t, activated, "a correctly guarded activateForTaxonomy must skip a lens a concurrent load already registered")
}

// TestRederiveEntry_GrowThenShrink_GoesThroughRebuildBothWays runs a real
// pipeline (embedded NATS) against a real *taxonomy.Resolver and proves both
// directions of §6.2-§6.4: a grow publishes a widened client gate (the
// consumer filter narrows to include the new concrete type) and a shrink
// publishes the narrower client gate too — going through the SAME
// UseFullEngineBranches-then-Rebuild sequence, never an in-place filter
// mutation. p.ConsumerFilter() is a pure function of the published rule
// state, so it can be asserted synchronously right after rederiveEntry
// returns, without waiting for the async Rebuild goroutine — proving the
// CLIENT GATE update landed first, exactly as §6.2 requires.
func TestRederiveEntry_GrowThenShrink_GoesThroughRebuildBothWays(t *testing.T) {
	env := startMatchChangeEnv(t)
	ctx := context.Background()

	fullEngine := full.New()
	cr := mcCompile(t, fullEngine, "MATCH (l:location*) RETURN l.key AS locKey", []string{"locKey"})

	adpt, err := adapter.New(env.target, []string{"locKey"}, adapter.DeleteModeHard)
	require.NoError(t, err)

	healthKV, err := env.conn.OpenKV(ctx, "HEALTH-lens-mc")
	require.NoError(t, err)
	reporter := health.New(healthKV, "lens-tax")

	p, err := pipeline.New("lens-tax", "nats_kv", "CORE", env.adjKV, env.coreKV, adpt, reporter)
	require.NoError(t, err)

	resolver := taxonomy.New()
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: "meta-location", CanonicalName: "location", Abstract: true},
	})
	resolver.SetArmed(true)
	p.SetTaxonomyResolver(resolver)

	labels := map[string]struct{}{"location": {}}
	initialExpand, _, initialStatus, _ := resolver.Expand(labels)
	require.Equal(t, taxonomy.StatusArmed, initialStatus)
	require.NoError(t, p.UseFullEngineBranches(fullEngine, cr, nil))

	p.RunOn(env.conn, substrate.ConsumerSpec{
		Name:          "refractor-lens-tax",
		Stream:        subjects.CoreKVStream("CORE"),
		FilterSubject: subjects.CoreKVFilter("CORE"),
		DeliverPolicy: substrate.DeliverLastPerSubject,
		DeliverGroup:  "refractor-lens-tax",
		AckWait:       2 * time.Second,
	})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// Rebuild's supervisor.UpdateSpec requires the consumer already
	// registered — Run does that asynchronously, so a rederiveEntry call
	// racing straight past RunOn/Run would hit "not managed". Pending
	// returns an error until that registration lands; wait for it.
	mcPollUntil(t, 3*time.Second, func() bool {
		_, err := p.Pending(ctx)
		return err == nil
	})

	// At activation: "location" resolves to a KNOWN but EMPTY set (no
	// concrete descendants yet) — §4.2's zero-length-set rule forces the
	// broad filter, never a stale narrow one.
	filterSubjects, filterSubject, _ := p.ConsumerFilter()
	assert.Nil(t, filterSubjects)
	assert.Equal(t, subjects.CoreKVFilter("CORE"), filterSubject)

	entry := &pipelineEntry{
		pipeline:           p,
		reporter:           reporter,
		rule:               &lens.Rule{ID: "lens-tax", ResolvedEngine: ruleengine.EngineFull, CompiledRule: cr},
		taxExpansionLabels: labels,
		taxExpansion:       initialExpand,
		taxExpansionStatus: initialStatus,
	}
	rl := &reloader{
		ctx:        ctx,
		logger:     discardLogger(),
		fullEngine: fullEngine,
		resolver:   resolver,
	}

	// GROW: location gains a concrete child, "unit".
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: "meta-location", CanonicalName: "location", Abstract: true},
		{ID: "meta-unit", CanonicalName: "unit", Abstract: false, SubtypeOf: []string{"location"}},
	})
	resolver.SetArmed(true)
	rl.rederiveEntry(entry)

	filterSubjects, filterSubject, _ = p.ConsumerFilter()
	require.NotNil(t, filterSubjects, "the client gate must publish the widened rule state — the filter narrows, never stays broad")
	assert.Contains(t, filterSubjects, subjects.CoreKVVertexFilter("CORE", "unit"))
	assert.Empty(t, filterSubject)

	// The server filter + cursor reset is fire-and-forget (rederiveEntry's
	// own doc): wait for a full in-flight cycle (rises then falls) rather
	// than polling health status, which can read a STALE "active" left over
	// from before this cycle even started and falsely look "done".
	waitForRebuildCycle(t, p, 3*time.Second)

	// SHRINK: the subtypeOf edge is retracted — location has no concrete
	// children again.
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: "meta-location", CanonicalName: "location", Abstract: true},
	})
	resolver.SetArmed(true)
	rl.rederiveEntry(entry)

	filterSubjects, filterSubject, _ = p.ConsumerFilter()
	assert.Nil(t, filterSubjects, "a shrink goes through the SAME Rebuild path as a grow — never an in-place narrowing that leaves the OLD (now over-wide) filter's complement unexamined")
	assert.Equal(t, subjects.CoreKVFilter("CORE"), filterSubject)

	waitForRebuildCycle(t, p, 3*time.Second)

	status, err := reporter.GetStatus(ctx)
	require.NoError(t, err)
	assert.Zero(t, status.ErrorCount, "neither re-derivation should have recorded a refusal")
}

// TestRederiveEntry_RebuildFailureLeavesTheRebuildPendingSoTheNextEventRetries
// covers C1: a failed Rebuild must not let the NEXT taxonomy event compare
// equal and skip. No p.RunOn is called, so Rebuild fails immediately and
// deterministically ("no supervisor configured") — isolating the fix from any
// real NATS I/O timing.
//
// The baseline itself DOES advance: it describes the client gate, which the
// synchronous publish already put in force. What records the unfinished half
// is entry.taxRebuildPending, and it is what makes the second rederiveEntry
// call (the resolver unchanged) re-drive the rebuild instead of returning
// early on an answer that has not changed.
func TestRederiveEntry_RebuildFailureLeavesTheRebuildPendingSoTheNextEventRetries(t *testing.T) {
	kv := startHealthKV(t)
	reporter := health.New(kv, "lens-tax-rebuildfail")

	adpt, err := adapter.New(nil, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := pipeline.New("lens-tax-rebuildfail", "nats_kv", "CORE", nil, nil, adpt, reporter)
	require.NoError(t, err)

	fullEngine := full.New()
	cr := mcCompile(t, fullEngine, "MATCH (l:location*) RETURN l.key AS key", []string{"key"})

	resolver := taxonomy.New()
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: "meta-location", CanonicalName: "location", Abstract: true},
		{ID: "meta-unit", CanonicalName: "unit", Abstract: false, SubtypeOf: []string{"location"}},
	})
	resolver.SetArmed(true)
	p.SetTaxonomyResolver(resolver)

	labels := map[string]struct{}{"location": {}}
	entry := &pipelineEntry{
		pipeline:           p,
		reporter:           reporter,
		rule:               &lens.Rule{ID: "lens-tax-rebuildfail", ResolvedEngine: ruleengine.EngineFull, CompiledRule: cr},
		taxExpansionLabels: labels,
		// taxExpansion/taxExpansionStatus start at their zero value.
	}
	rl := &reloader{ctx: context.Background(), logger: discardLogger(), fullEngine: fullEngine, resolver: resolver}

	rl.rederiveEntry(entry)

	require.Eventually(t, func() bool {
		return errorCount(t, reporter) > 0
	}, 3*time.Second, 20*time.Millisecond, "the Rebuild failure must be recorded on health")

	entry.taxMu.Lock()
	baseline, baselineStatus, pending := entry.taxExpansion, entry.taxExpansionStatus, entry.taxRebuildPending
	entry.taxMu.Unlock()
	require.Equal(t, map[string]map[string]struct{}{"location": {"unit": {}}}, baseline,
		"the baseline describes the client gate, which the synchronous publish put in force")
	require.Equal(t, taxonomy.StatusArmed, baselineStatus)
	require.True(t, pending, "the gate's Rebuild never succeeded, and that is what must be recorded")

	before := errorCount(t, reporter)
	rl.rederiveEntry(entry)
	require.Eventually(t, func() bool {
		return errorCount(t, reporter) > before
	}, 3*time.Second, 20*time.Millisecond, "an outstanding rebuild must make the next event retry (and fail again), not compare equal and skip")
}

func errorCount(t *testing.T, reporter *health.Reporter) uint64 {
	t.Helper()
	status, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	return status.ErrorCount
}

// TestRederiveEntry_LiveStatusUnknownDegradesToBroadRatherThanRefusing covers
// C2: a LIVE re-derivation whose Expand answers StatusUnknown must degrade
// the running pipeline to the broad filter (never refuse, unlike
// activation) and must NOT record a health error — going StatusUnknown is
// itself not a failure on the live path, only a rebuild I/O failure is.
func TestRederiveEntry_LiveStatusUnknownDegradesToBroadRatherThanRefusing(t *testing.T) {
	env := startMatchChangeEnv(t)
	ctx := context.Background()

	fullEngine := full.New()
	cr := mcCompile(t, fullEngine, "MATCH (l:location*) RETURN l.key AS locKey", []string{"locKey"})

	adpt, err := adapter.New(env.target, []string{"locKey"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	healthKV, err := env.conn.OpenKV(ctx, "HEALTH-lens-mc")
	require.NoError(t, err)
	reporter := health.New(healthKV, "lens-tax-unknown")

	p, err := pipeline.New("lens-tax-unknown", "nats_kv", "CORE", env.adjKV, env.coreKV, adpt, reporter)
	require.NoError(t, err)

	resolver := taxonomy.New()
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: "meta-location", CanonicalName: "location", Abstract: true},
		{ID: "meta-unit", CanonicalName: "unit", Abstract: false, SubtypeOf: []string{"location"}},
	})
	resolver.SetArmed(true)
	p.SetTaxonomyResolver(resolver)

	labels := map[string]struct{}{"location": {}}
	initialExpand, _, initialStatus, _ := resolver.Expand(labels)
	require.Equal(t, taxonomy.StatusArmed, initialStatus)
	require.NoError(t, p.UseFullEngineBranches(fullEngine, cr, nil))

	p.RunOn(env.conn, substrate.ConsumerSpec{
		Name:          "refractor-lens-tax-unknown",
		Stream:        subjects.CoreKVStream("CORE"),
		FilterSubject: subjects.CoreKVFilter("CORE"),
		DeliverPolicy: substrate.DeliverLastPerSubject,
		DeliverGroup:  "refractor-lens-tax-unknown",
		AckWait:       2 * time.Second,
	})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	mcPollUntil(t, 3*time.Second, func() bool {
		_, err := p.Pending(ctx)
		return err == nil
	})

	// Precondition: narrowed and live.
	filterSubjects, _, _ := p.ConsumerFilter()
	require.NotNil(t, filterSubjects, "precondition: the lens starts narrowed")

	entry := &pipelineEntry{
		pipeline:           p,
		reporter:           reporter,
		rule:               &lens.Rule{ID: "lens-tax-unknown", ResolvedEngine: ruleengine.EngineFull, CompiledRule: cr},
		taxExpansionLabels: labels,
		taxExpansion:       initialExpand,
		taxExpansionStatus: initialStatus,
	}
	rl := &reloader{ctx: ctx, logger: discardLogger(), fullEngine: fullEngine, resolver: resolver}

	// The taxonomy no longer declares "location" AT ALL — Expand("location")
	// now answers (nil, StatusUnknown), simulating an unresolvable label
	// (or, structurally identically, a resolver whose snapshot is missing
	// it after some other install-order fault).
	resolver.InstallSnapshot([]taxonomy.TypeSnapshot{
		{ID: "meta-other", CanonicalName: "somethingElse", Abstract: false},
	})
	resolver.SetArmed(true)

	rl.rederiveEntry(entry)

	filterSubjects, filterSubject, _ := p.ConsumerFilter()
	assert.Nil(t, filterSubjects, "StatusUnknown on a LIVE re-derivation must degrade to the broad filter, not keep the stale narrow one")
	assert.Equal(t, subjects.CoreKVFilter("CORE"), filterSubject)

	waitForRebuildCycle(t, p, 3*time.Second)
	assert.Zero(t, errorCount(t, reporter), "a live StatusUnknown re-derivation is not itself a refusal — only a Rebuild I/O failure would be")
}

// waitForRebuildCycle waits for p.RebuildInFlight() to rise then fall — a
// full rebuild cycle actually ran, as opposed to reading "not in flight"
// before the fire-and-forget goroutine (rederiveEntry's `go func(){...}()`)
// has even had a chance to start (mirrors mcPollUntil's polling style;
// CLAUDE.md: deterministic sync, never a fixed sleep in place of one).
func waitForRebuildCycle(t *testing.T, p *pipeline.Pipeline, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	sawInFlight := false
	for time.Now().Before(deadline) {
		if p.RebuildInFlight() {
			sawInFlight = true
		} else if sawInFlight {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("rebuild did not complete a full in-flight cycle within %s", timeout)
}

// TestRederiveEntry_TaxonomyReturnsToTheBaseline_RepublishesTheGateItLeft is
// the A→B→A sequence, and it needs no race: every step is a synchronous call.
//
// The client gate and the server-side consumer filter are moved by different
// halves of the same sequence — the gate by the synchronous publish, the
// filter by the Rebuild after it — so a change-detection baseline that
// advances only when the Rebuild SUCCEEDS describes neither. E0 is in force
// on both. (1) The taxonomy loses `desk`, so the gate is published as E1 and
// the Rebuild fails on an ordinary blip, leaving the consumer's filter on E0.
// (2) The package is reinstalled and the answer returns to E0 — which, against
// a baseline that never left E0, compares EQUAL. Returning early there strands
// a client gate on E1 over a server filter on E0: every `vtx.desk.*` event is
// delivered and then dropped by the gate, with no other write path to either
// side. §6.5's one unacceptable state.
//
// So the assertion is that the gate comes back to the set the server side
// never left, and that the rebuild it is still owed is driven again.
func TestRederiveEntry_TaxonomyReturnsToTheBaseline_RepublishesTheGateItLeft(t *testing.T) {
	kv := startHealthKV(t)
	reporter := health.New(kv, "lens-tax-aba")

	adpt, err := adapter.New(nil, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	// No RunOn: every Rebuild fails immediately and deterministically ("no
	// supervisor configured"), which is this test's stand-in for the NATS blip.
	p, err := pipeline.New("lens-tax-aba", "nats_kv", "CORE", nil, nil, adpt, reporter)
	require.NoError(t, err)

	fullEngine := full.New()
	cr := mcCompile(t, fullEngine, "MATCH (l:location*) RETURN l.key AS key", []string{"key"})

	resolver := taxonomy.New()
	installLocationTaxonomy := func(leaves ...string) {
		snap := []taxonomy.TypeSnapshot{{ID: "meta-location", CanonicalName: "location", Abstract: true}}
		for _, leaf := range leaves {
			snap = append(snap, taxonomy.TypeSnapshot{ID: "meta-" + leaf, CanonicalName: leaf, SubtypeOf: []string{"location"}})
		}
		resolver.InstallSnapshot(snap)
		resolver.SetArmed(true)
	}

	// E0 — the state both the gate and the (never-rebuilt-since) consumer
	// filter are in when the sequence starts.
	installLocationTaxonomy("room", "desk")
	p.SetTaxonomyResolver(resolver)
	require.NoError(t, p.UseFullEngineBranches(fullEngine, cr, nil))
	e0Subjects, _, _ := p.ConsumerFilter()
	require.Contains(t, e0Subjects, subjects.CoreKVVertexFilter("CORE", "desk"), "precondition: E0 admits desk")

	labels := map[string]struct{}{"location": {}}
	e0, _, e0Status, _ := resolver.Expand(labels)
	entry := &pipelineEntry{
		pipeline:           p,
		reporter:           reporter,
		rule:               &lens.Rule{ID: "lens-tax-aba", ResolvedEngine: ruleengine.EngineFull, CompiledRule: cr},
		taxExpansionLabels: labels,
		taxExpansion:       e0,
		taxExpansionStatus: e0Status,
	}
	rl := &reloader{ctx: context.Background(), logger: discardLogger(), fullEngine: fullEngine, resolver: resolver}

	// (1) desk's subtypeOf is tombstoned. The gate moves to E1; the Rebuild
	// behind it fails, so the consumer filter stays on E0.
	installLocationTaxonomy("room")
	rl.rederiveEntry(entry)
	require.Eventually(t, func() bool {
		return errorCount(t, reporter) > 0
	}, 3*time.Second, 20*time.Millisecond, "the Rebuild failure must be recorded on health")

	e1Subjects, _, _ := p.ConsumerFilter()
	require.NotContains(t, e1Subjects, subjects.CoreKVVertexFilter("CORE", "desk"),
		"the gate narrowed to E1 while the consumer filter it never rebuilt still admits desk")

	// (2) The package is reinstalled: the answer returns to E0.
	installLocationTaxonomy("room", "desk")
	before := errorCount(t, reporter)
	rl.rederiveEntry(entry)

	backSubjects, _, _ := p.ConsumerFilter()
	require.Contains(t, backSubjects, subjects.CoreKVVertexFilter("CORE", "desk"),
		"the gate must return to the set the server side never left — otherwise every desk event is "+
			"delivered by the filter and silently dropped by the gate, with nothing left to repair it")
	require.Contains(t, backSubjects, subjects.CoreKVVertexFilter("CORE", "room"))
	require.ElementsMatch(t, e0Subjects, backSubjects, "gate and filter agree again, on E0")

	require.Eventually(t, func() bool {
		return errorCount(t, reporter) > before
	}, 3*time.Second, 20*time.Millisecond, "the rebuild the gate is still owed must be driven again")
}
