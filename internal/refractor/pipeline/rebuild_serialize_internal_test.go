package pipeline

// Coverage of RebuildAndWait's properties: it waits for a rebuild to DRAIN
// rather than returning the moment the durable is reset, it serializes callers
// per pipeline instead of letting two rescans of one lens overlap, its wait
// cannot be ended by an unrelated rebuild, and it gives up on a budget rather
// than blocking forever (retention-class-key-custody-design.md §6.3, "two risks
// in the rebuild path", and §19.1 B2/B3).
//
// Both tests drive Rebuild without a supervisor, so it returns its
// no-supervisor error after the phase under test. Serialization is observed
// through the adapter's Guarded() — the truncate-force decision, which Rebuild
// makes inside the section being serialized and before it touches the target —
// rather than through a hook in production code. It is deliberately NOT
// observed through Truncate: a rebuild refuses to truncate a target whose
// consumer the supervisor does not manage, which is exactly the state a
// supervisor-less fixture is in.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/control"
)

// blockingTruncAdapter parks inside the truncate-force decision until released,
// so a test can hold one rebuild in its critical section and observe whether a
// second enters.
type blockingTruncAdapter struct {
	arrived chan struct{}
	release chan struct{}
}

func newBlockingTruncAdapter() *blockingTruncAdapter {
	return &blockingTruncAdapter{arrived: make(chan struct{}, 8), release: make(chan struct{})}
}

func (a *blockingTruncAdapter) Upsert(context.Context, map[string]any, map[string]any, uint64) error {
	return nil
}
func (a *blockingTruncAdapter) Delete(context.Context, map[string]any, uint64) error { return nil }
func (a *blockingTruncAdapter) Probe(context.Context) error                          { return nil }
func (a *blockingTruncAdapter) Close() error                                         { return nil }
func (a *blockingTruncAdapter) Truncate(context.Context) error                       { return nil }

// Guarded is consulted by resolveTruncate, inside the serialized section and
// ahead of any write to the target.
func (a *blockingTruncAdapter) Guarded() bool {
	a.arrived <- struct{}{}
	<-a.release
	return false
}

func newRebuildWaitPipeline(t *testing.T) *Pipeline {
	t.Helper()
	p, err := New("rule-rebuild-wait", "nats_kv", "CORE", nil, nil, &guardedTruncAdapter{}, nil)
	require.NoError(t, err)
	p.rebuildPollInterval = time.Millisecond
	return p
}

// A rebuild already in flight — started by a path RebuildAndWait cannot see,
// e.g. the MATCH hot-reloader — must be waited OUT, not adopted. Its rows may
// predate the key destruction this caller is answering to, so treating it as
// this destruction's rebuild would attest to an erasure that never happened.
func TestRebuildAndWait_WaitsOutARebuildStartedElsewhere(t *testing.T) {
	p := newRebuildWaitPipeline(t)
	other := p.beginRebuild()

	done := make(chan error, 1)
	go func() { done <- p.RebuildAndWait(context.Background(), false, 0) }()

	assert.Never(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, 50*time.Millisecond, 5*time.Millisecond,
		"RebuildAndWait must not start its own rescan while another is in flight")

	other.drained.Store(true)
	p.endRebuild(other) // the other rebuild drains

	select {
	case err := <-done:
		require.ErrorContains(t, err, "no supervisor",
			"once the in-flight rebuild drained, this caller proceeded to its OWN rebuild")
	case <-time.After(5 * time.Second):
		t.Fatal("RebuildAndWait never proceeded after the in-flight rebuild drained")
	}
}

// Concurrent callers queue rather than overlapping. Without the per-pipeline
// serialization both would reach Rebuild, and each rebuild resets the consumer
// under the other, so the second would replay over a rescan the first is still
// attesting to.
func TestRebuildAndWait_SerializesConcurrentCallers(t *testing.T) {
	ad := newBlockingTruncAdapter()
	p, err := New("rule-rebuild-serial", "nats_kv", "CORE", nil, nil, ad, nil)
	require.NoError(t, err)
	p.rebuildPollInterval = time.Millisecond

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each errors on the missing supervisor; the ORDERING is the assertion.
			// truncate=false so the force rule actually consults Guarded() — a
			// requested truncate short-circuits that decision.
			_ = p.RebuildAndWait(context.Background(), false, 0)
		}()
	}

	// One caller reaches the truncate decision and parks there.
	select {
	case <-ad.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("no rebuild reached the adapter")
	}

	// While it is held, the second must not enter — that is the serialization.
	assert.Never(t, func() bool {
		select {
		case <-ad.arrived:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, 5*time.Millisecond,
		"a second rebuild of the same lens entered while the first was still in its critical section")

	close(ad.release)
	wg.Wait()
}

// A cancelled context releases the caller instead of pinning it to an unbounded
// drain — the reason the serialization is a channel and not a sync.Mutex.
func TestRebuildAndWait_HonorsContextCancellation(t *testing.T) {
	p := newRebuildWaitPipeline(t)
	p.beginRebuild() // never drains

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.RebuildAndWait(ctx, false, 0) }()
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled context must release a RebuildAndWait caller")
	}
}

// The completion signal belongs to ONE rebuild. A different rebuild failing —
// which is what abandonRebuild does, unconditionally, on any error — must not
// release a caller waiting on an earlier one. Before the per-rebuild channel,
// waiters polled a single pipeline-wide flag that abandonRebuild cleared as its
// first statement, so a concurrent hot-reload or operator rebuild ended the
// wait of a caller whose own rescan was still running: it returned nil and, for
// the retention-class consumer, attested to an erasure that had not landed.
func TestRebuildAndWait_AnotherRebuildsFailureDoesNotEndThisWait(t *testing.T) {
	p := newRebuildWaitPipeline(t)
	mine := p.beginRebuild()

	released := make(chan struct{})
	go func() {
		defer close(released)
		_ = p.waitRebuildSignal(context.Background(), context.Background(), mine)
	}()

	// A second rebuild starts and immediately fails, exactly as a hot-reload or
	// an operator rebuild does when its own setup errors.
	theirs := p.beginRebuild()
	require.Error(t, p.abandonRebuild(context.Background(), theirs, assert.AnError))

	assert.Never(t, func() bool {
		select {
		case <-released:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, 5*time.Millisecond,
		"an unrelated rebuild's failure released a caller waiting on a different rebuild")

	p.endRebuild(mine)
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter was not released when its OWN rebuild ended")
	}
}

// An expired wait budget is a failure with its own error, never a completion —
// a caller that attests must be able to tell "the rescan drained" from "I
// stopped waiting". The rebuild itself keeps running: the budget bounds the
// wait only, so the completion watcher survives to clear the "rebuilding"
// status and un-suppress the convergence sweep.
func TestRebuildAndWait_BudgetExpiryIsAFailureNotACompletion(t *testing.T) {
	p := newRebuildWaitPipeline(t)
	stillRunning := p.beginRebuild()

	done := make(chan error, 1)
	go func() { done <- p.RebuildAndWait(context.Background(), false, 50*time.Millisecond) }()

	select {
	case err := <-done:
		require.ErrorIs(t, err, control.ErrRebuildWaitTimeout)
	case <-time.After(5 * time.Second):
		t.Fatal("an unbounded wait: the budget never expired the caller")
	}

	select {
	case <-stillRunning.done:
		t.Fatal("the budget ended the REBUILD, not just the wait — its watcher would be gone and the status latched")
	default:
	}
}

// A cancelled watcher closes the signal too — it is the same `defer` every exit
// runs — so a waiter that reads the CLOSE as completion returns success for a
// rescan that was killed mid-drain. And because both select arms are then ready,
// Go picks uniformly at random, so it did so about half the time. Success is the
// `drained` flag, which only the watcher that saw zero outstanding ever sets.
func TestWaitRebuildSignal_ACancelledWatcherIsNotACompletion(t *testing.T) {
	p := newRebuildWaitPipeline(t)
	sig := p.beginRebuild()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.endRebuild(sig) // exactly what watchRebuildCompletion's ctx-cancel exit does

	// Repeated because the defect was a 50/50 select race: one pass proves little.
	for range 50 {
		err := p.waitRebuildSignal(ctx, ctx, sig)
		require.Error(t, err, "a signal closed without a drain must never read as success")
		require.ErrorIs(t, err, context.Canceled,
			"a shutdown must surface as the caller's own cancellation, so the handler naks instead of attesting")
	}
}

// The same close, with the caller's context still live: not a shutdown, not a
// timeout — a rebuild that ended without anyone observing it drain. It gets its
// own sentinel because the caller's response differs from both.
func TestWaitRebuildSignal_AnEndWithoutADrainIsItsOwnError(t *testing.T) {
	p := newRebuildWaitPipeline(t)
	sig := p.beginRebuild()
	p.endRebuild(sig)

	err := p.waitRebuildSignal(context.Background(), context.Background(), sig)
	require.ErrorIs(t, err, control.ErrRebuildNotDrained)
}

// The success path, so the fix is not "never report completion".
func TestWaitRebuildSignal_ADrainedRescanSucceeds(t *testing.T) {
	p := newRebuildWaitPipeline(t)
	sig := p.beginRebuild()
	sig.drained.Store(true)
	p.endRebuild(sig)

	require.NoError(t, p.waitRebuildSignal(context.Background(), context.Background(), sig))
}

// abandonRebuild clears PIPELINE-WIDE state — the in-flight flag and the health
// status — and an older rebuild failing must not clear a newer one's. The flag
// is what Sweeper.suppressed reads, so clearing it under a live rescan
// un-suppresses the convergence sweep and announces a lens that is still
// draining: the exact condition the suppression exists to create.
func TestAbandonRebuild_DoesNotClearANewerRebuildsInFlightState(t *testing.T) {
	p := newRebuildWaitPipeline(t)
	older := p.beginRebuild()
	newer := p.beginRebuild() // the newer rebuild now owns the pipeline-wide state

	require.Error(t, p.abandonRebuild(context.Background(), older, assert.AnError))

	assert.True(t, p.RebuildInFlight(),
		"an older rebuild's failure must not un-suppress the sweep under a newer rescan")
	select {
	case <-newer.done:
		t.Fatal("an older rebuild's failure released the NEWER rebuild's waiters")
	default:
	}
}

// The same rule at its source. Every exit funnels through endRebuild — the
// abandon path, the watcher's deferred exit, its drained arm, and the unwatched
// branch — so the ownership test belongs to it rather than to any one caller,
// and the watcher's two gates have no coverage of their own without this. A
// non-owner releases only its OWN waiters and closes only its OWN window: the
// newer rebuild's window stays open, and that is what keeps the convergence
// sweep suppressed while the newer rescan drains.
func TestEndRebuild_ANonOwnerReleasesItsWaitersWithoutClearingTheFlag(t *testing.T) {
	p := newRebuildWaitPipeline(t)
	older := p.beginRebuild()
	newer := p.beginRebuild()

	assert.False(t, p.endRebuild(older), "the older rebuild no longer owns the pipeline-wide state")
	assert.True(t, p.RebuildInFlight(),
		"a non-owner must not un-suppress the sweep under a newer rescan")
	select {
	case <-older.done:
	default:
		t.Fatal("endRebuild must release its own waiters even when it does not own the flag")
	}
	select {
	case <-newer.done:
		t.Fatal("a non-owner released the NEWER rebuild's waiters")
	default:
	}

	// The owner does clear it, and clears it before releasing its waiters — the
	// ordering that stops a woken waiter's own rebuild from being cleared out
	// from under it by the goroutine that just ended.
	assert.True(t, p.endRebuild(newer), "the newest rebuild owns the pipeline-wide state")
	assert.False(t, p.RebuildInFlight(), "the owner clears the flag as it ends")
}
