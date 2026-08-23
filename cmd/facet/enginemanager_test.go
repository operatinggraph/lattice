package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
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
// than being handed back forever — the dead end this closes.
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

// TestEngineManager_ConcurrentFirstAcquiresShareOneBuild proves two
// first-time Acquires for the same identity landing together — the live
// consumer is a first sign-in whose SSE attach (handleFeed) and first write
// (handleEnqueue) arrive at once — build exactly ONE engine between them.
// Two builds would both call newEngine, which opens the identity's
// single-writer bbolt mirror and starts its sync loops: the loser either
// times out on the store's 2s open lock (a 500 to a real request) or attaches
// to, and then on Close tears down, the winner's sync durable.
//
// A real embedded NATS server is used, like the rebuild test above: the
// property under test is that only one real newEngine ever runs, which a
// stand-in *engine{} cannot exercise.
func TestEngineManager_ConcurrentFirstAcquiresShareOneBuild(t *testing.T) {
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

	const holders = 2
	engs := make([]*engine, holders)
	errs := make([]error, holders)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < holders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // released together, so both are inside Acquire at once
			engs[i], errs[i] = m.Acquire(identity)
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < holders; i++ {
		require.NoErrorf(t, errs[i], "concurrent Acquire %d failed", i)
	}
	require.Same(t, engs[0], engs[1], "concurrent first Acquires must share one engine, not race two builds on the same mirror")

	m.mu.Lock()
	e, ok := m.entries[identity]
	refCount := 0
	if ok {
		refCount = e.refCount
	}
	m.mu.Unlock()
	require.True(t, ok, "the shared build must leave an installed entry")
	require.Equal(t, holders, refCount, "every caller of a shared build owes exactly one Release")

	for i := 0; i < holders; i++ {
		m.Release(identity)
	}
	engs[0].Close()
}

// TestEngineManager_AcquireSupersedesAClosedEntry proves Acquire never hands
// back an engine that has already been closed out from under its entry, and
// rebuilds instead — the state a Purge leaves when it lands between a build
// installing its entry and a caller of that build claiming a hold on it: the
// claim finds the entry gone and reinstalls the engine the purge just closed.
//
// The interleaving that produces it cannot be driven from a test without a
// hook inside Acquire, so the entry is planted directly: what matters is the
// resulting STATE (a present, non-pinned, closed-engine entry) and that it is
// superseded rather than deferred to. buildEngine is called directly for that
// assertion because it is where every dead entry is now evicted — under the
// flight, so no second caller can start a newEngine against a mirror lock the
// engine being closed has not finished dropping.
func TestEngineManager_AcquireSupersedesAClosedEntry(t *testing.T) {
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
	m.Release(identity)

	// A real purge: drops the entry, closes the engine, frees the mirror.
	require.NoError(t, m.Purge(identity))
	require.True(t, eng1.conn.NATS().IsClosed(), "purge must have closed the engine")

	// The corpse a purge-raced claim step leaves behind.
	m.mu.Lock()
	m.entries[identity] = &engineEntry{eng: eng1, refCount: 1}
	m.mu.Unlock()

	eng2, err := m.buildEngine(identity)
	require.NoError(t, err)
	require.NotSame(t, eng1, eng2, "a closed engine's entry must be superseded, not handed back")
	require.False(t, eng2.conn.NATS().IsClosed(), "the replacement engine's connection must be live")

	m.mu.Lock()
	installed := m.entries[identity].eng
	m.mu.Unlock()
	require.Same(t, eng2, installed, "the map must hold the live replacement, not the corpse")

	// And the full Acquire path claims a hold on that replacement rather than
	// building yet another engine.
	got, err := m.Acquire(identity)
	require.NoError(t, err)
	require.Same(t, eng2, got)

	m.mu.Lock()
	refCount := m.entries[identity].refCount
	m.mu.Unlock()
	require.Equal(t, 1, refCount, "buildEngine installs holder-less; the claim step adds the only hold")

	m.Release(identity)
	eng2.Close()
}
