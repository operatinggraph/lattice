package pipeline

// Coverage of RebuildAndWait's two properties: it waits for a rebuild to DRAIN
// rather than returning the moment the durable is reset, and it serializes
// callers per pipeline instead of letting two rescans of one lens overlap
// (retention-class-key-custody-design.md §6.3, "two risks in the rebuild path").
//
// Both tests drive Rebuild without a supervisor, so it returns its
// no-supervisor error after the phase under test — the same window
// rebuild_force_truncate_internal_test.go inspects. Serialization is observed
// through the adapter's Truncate, which Rebuild calls inside the section being
// serialized, rather than through a hook in production code.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockingTruncAdapter parks inside Truncate until released, so a test can hold
// one rebuild in its critical section and observe whether a second enters.
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
func (a *blockingTruncAdapter) Truncate(context.Context) error {
	a.arrived <- struct{}{}
	<-a.release
	return nil
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
	p.rebuildInFlight.Store(true)

	done := make(chan error, 1)
	go func() { done <- p.RebuildAndWait(context.Background(), false) }()

	assert.Never(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, 50*time.Millisecond, 5*time.Millisecond,
		"RebuildAndWait must not start its own rescan while another is in flight")

	p.rebuildInFlight.Store(false) // the other rebuild drains

	select {
	case err := <-done:
		require.ErrorContains(t, err, "no supervisor",
			"once the in-flight rebuild drained, this caller proceeded to its OWN rebuild")
	case <-time.After(5 * time.Second):
		t.Fatal("RebuildAndWait never proceeded after the in-flight rebuild drained")
	}
}

// Concurrent callers queue rather than overlapping. Without the per-pipeline
// serialization both would reach Rebuild, and Rebuild stores rebuildInFlight
// unconditionally (not by CAS), so the second would clobber the first's window.
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
			_ = p.RebuildAndWait(context.Background(), true)
		}()
	}

	// One caller reaches Truncate and parks there.
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
	p.rebuildInFlight.Store(true) // never drains

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.RebuildAndWait(ctx, false) }()
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled context must release a RebuildAndWait caller")
	}
}
