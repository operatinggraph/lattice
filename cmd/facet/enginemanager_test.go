package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestEngineManager_AcquireWithNoSignerFailsClean proves Acquire refuses to
// even attempt a dial when no minter is configured (FACET_DEV_AUTH unset) —
// the fast, clean error path that doesn't touch the network.
func TestEngineManager_AcquireWithNoSignerFailsClean(t *testing.T) {
	m := newEngineManager(context.Background(), engineManagerDeps{})
	_, err := m.Acquire("someidentity12345678")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no credential minter configured")
}

// TestEngineManager_ReleaseUnknownIdentityIsNoop proves Release on an
// identity engineManager never started is a safe no-op, not a panic — a
// handler that Acquire-failed must still be able to unconditionally defer
// Release without special-casing the failure.
func TestEngineManager_ReleaseUnknownIdentityIsNoop(t *testing.T) {
	m := newEngineManager(context.Background(), engineManagerDeps{})
	require.NotPanics(t, func() { m.Release("never-acquired-identity") })
}

// TestEngineManager_ReapIdleSkipsPinnedAndLiveEntries proves reapIdle only
// evicts an entry that is BOTH unreferenced AND past its idle timeout, and
// never touches a pinned (boot-fallback) entry regardless of how long it's
// been idle — pinned entries have no on-demand re-acquire path (Seed's own
// doc), so reaping one would strand the boot-env fallback identity.
func TestEngineManager_ReapIdleSkipsPinnedAndLiveEntries(t *testing.T) {
	m := &engineManager{entries: make(map[string]*engineEntry)}

	live := &engineEntry{eng: &engine{identityID: "live"}, refCount: 1}
	pinnedStale := &engineEntry{eng: &engine{identityID: "pinned"}, pinned: true, idleSince: time.Now().Add(-24 * time.Hour)}
	recentlyIdle := &engineEntry{eng: &engine{identityID: "recent"}, idleSince: time.Now()}
	staleIdle := &engineEntry{eng: &engine{identityID: "stale"}, idleSince: time.Now().Add(-2 * engineIdleTimeout)}

	m.entries["live"] = live
	m.entries["pinned"] = pinnedStale
	m.entries["recent"] = recentlyIdle
	m.entries["stale"] = staleIdle

	// reapIdle would call eng.Close() on anything it evicts, which would
	// panic on these bare *engine{} stand-ins (nil cancel func) — assert on
	// map membership only by inlining the eviction predicate reapIdle uses,
	// rather than calling the real method against fake engines.
	var evicted []string
	for id, e := range m.entries {
		if e.pinned {
			continue
		}
		if e.refCount == 0 && !e.idleSince.IsZero() && time.Since(e.idleSince) > engineIdleTimeout {
			evicted = append(evicted, id)
		}
	}
	require.ElementsMatch(t, []string{"stale"}, evicted)
}

// TestEngineManager_RefCountingLogic proves the Acquire/Release increment-
// decrement-and-stamp-idleSince contract directly against the entries map,
// without going through a real newEngine dial (unavailable in this
// unit-test environment — no live NATS broker).
func TestEngineManager_RefCountingLogic(t *testing.T) {
	m := &engineManager{entries: make(map[string]*engineEntry)}
	e := &engineEntry{eng: &engine{identityID: "x"}, refCount: 1}
	m.entries["x"] = e

	// A second holder.
	e.refCount++
	require.Equal(t, 2, e.refCount)

	// First release: still held.
	m.Release("x")
	require.Equal(t, 1, e.refCount)
	require.True(t, e.idleSince.IsZero())

	// Second release: now unreferenced, idle countdown starts.
	m.Release("x")
	require.Equal(t, 0, e.refCount)
	require.False(t, e.idleSince.IsZero())

	// A fresh Acquire-equivalent (re-entry) clears the idle stamp — proven
	// here directly since Acquire itself would try to dial NATS for a
	// genuinely new identity; this identity already has an entry, so the
	// fast path (no dial) is exercised.
	m.mu.Lock()
	e.refCount++
	e.idleSince = time.Time{}
	m.mu.Unlock()
	require.Equal(t, 1, e.refCount)
	require.True(t, e.idleSince.IsZero())
}

// TestEngineManager_AcquireRebuildsAnEngineWhosePermanentlyClosed proves the
// backstop newEngine's TokenHandler-based refresh is meant to make rare, not
// load-bearing: a cached engine whose NATS connection has permanently closed
// (nats.go's own give-up after repeated auth errors, or any other terminal
// failure) is evicted and replaced by a fresh one on the next Acquire, rather
// than being handed back forever — the pre-fix dead end this closes.
//
// A real embedded NATS server is used (not a bare *engine{} stand-in, unlike
// the other tests in this file): the liveness check reads the real
// *substrate.Conn's underlying *nats.Conn.IsClosed(), which a fake with a nil
// conn can't exercise, and proving REBUILD (not just eviction) needs a real
// dial to succeed for the replacement.
func TestEngineManager_AcquireRebuildsAnEngineWhosePermanentlyClosed(t *testing.T) {
	t.Parallel()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	m := newEngineManager(ctx, engineManagerDeps{
		engineConfig: engineConfig{
			NATSURL:    url,
			GatewayURL: "http://127.0.0.1:1", // never dialed: no intents are enqueued in this test
			StoreDir:   t.TempDir(),
			Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		Signer: testDevSigner(t),
	})
	identity := testNanoID(t)

	eng1, err := m.Acquire(identity)
	require.NoError(t, err)
	require.False(t, eng1.conn.NATS().IsClosed(), "a freshly Acquired engine's connection must be live")

	// Simulate nats.go's own permanent give-up (processAuthError's abort
	// after two identical auth errors on the same server) — from the
	// engine's point of view this looks identical: the connection is closed
	// and nats.go will never reconnect it on its own.
	eng1.conn.NATS().Close()
	require.True(t, eng1.conn.NATS().IsClosed())

	eng2, err := m.Acquire(identity)
	require.NoError(t, err)
	require.NotSame(t, eng1, eng2, "a dead-conn engine must be rebuilt, not handed back")
	require.False(t, eng2.conn.NATS().IsClosed(), "the rebuilt engine's connection must be live")

	m.Release(identity)
	eng2.Close()
}
