package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// engineIdleTimeout is how long an identity's engine is kept warm after its
// last holder releases it — the design's "warm resume": a browser reload or
// a quick re-login within this window reuses the already-hydrated engine
// instead of re-dialing NATS and re-hydrating from scratch.
const engineIdleTimeout = 10 * time.Minute

const engineReapInterval = time.Minute

// engineManagerDeps is the process-wide config every engine an engineManager
// creates shares, plus the signer it mints fresh device credentials with.
type engineManagerDeps struct {
	engineConfig
	Signer *devSigner
}

// engineEntry is one identity's engine plus its holder count. refCount
// tracks live holders (an open SSE connection, or an in-flight
// /api/enqueue call) — not sessions minted, so a browser that signs out (or
// simply never reconnects its SSE stream) lets its engine idle out promptly.
// idleSince is zero while refCount > 0. pinned entries (the boot-env
// single-user fallback, engineManager.Seed) are never reaped regardless of
// refCount — main.go seeded them from a token it does not control the
// reissue of, so there's no "re-acquire on demand" for them the way a
// dev-login identity has.
type engineEntry struct {
	eng       *engine
	refCount  int
	idleSince time.Time
	pinned    bool
}

// engineManager multiplexes one engine per identity, ref-counted by active
// holders and reaped after engineIdleTimeout of disuse — the "per-session
// engines" mechanism edge-showcase-app-design.md §7.2/Inc 2 names. Facet is
// no longer per-process single-tenant: it's per-identity multi-tenant,
// bounded by however many distinct identities are actively signed in on this
// host at once, not by request volume.
type engineManager struct {
	mu      sync.Mutex
	entries map[string]*engineEntry
	deps    engineManagerDeps
	baseCtx context.Context
}

func newEngineManager(baseCtx context.Context, deps engineManagerDeps) *engineManager {
	m := &engineManager{
		entries: make(map[string]*engineEntry),
		deps:    deps,
		baseCtx: baseCtx,
	}
	go m.reapLoop(baseCtx)
	return m
}

// Seed installs identityID's engine using an already-minted credential —
// main.go's boot-env single-user fallback (EDGE_IDENTITY_ID/EDGE_TOKEN),
// whose token was minted OUTSIDE this process (e.g. `bin/gateway
// dev-token`), not by deps.Signer. The entry is pinned: reapIdle never
// closes it, and Acquire's liveness check never rebuilds it either — both
// would need to re-mint on this identity's behalf, which only deps.Signer
// can do.
func (m *engineManager) Seed(identityID, deviceID, token string) error {
	eng, err := newEngine(m.baseCtx, m.deps.engineConfig, identityID, deviceID, token, nil)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.entries[identityID] = &engineEntry{eng: eng, pinned: true}
	m.mu.Unlock()
	return nil
}

// Acquire returns identityID's engine, starting it on first use (minting a
// fresh device credential for it via deps.Signer) and incrementing its
// holder count. Callers MUST pair every successful Acquire with a Release.
//
// A cached, non-pinned entry whose NATS connection has permanently closed is
// evicted and rebuilt rather than handed back: newEngine's TokenHandler
// keeps that connection recovering across the credential's TTL on its own,
// so reaching this branch means reconnection genuinely failed for good
// (nats.go exhausted MaxReconnects, or aborted after repeated identical auth
// errors) — a last-resort backstop. Purge remains the explicit eviction
// path for revocation (see its doc).
func (m *engineManager) Acquire(identityID string) (*engine, error) {
	m.mu.Lock()
	if e, ok := m.entries[identityID]; ok {
		if e.pinned || !e.eng.conn.NATS().IsClosed() {
			e.refCount++
			e.idleSince = time.Time{}
			m.mu.Unlock()
			return e.eng, nil
		}
		delete(m.entries, identityID)
		m.mu.Unlock()
		e.eng.Close()
	} else {
		m.mu.Unlock()
	}

	if m.deps.Signer == nil {
		return nil, fmt.Errorf("no credential minter configured (FACET_DEV_AUTH not set) — cannot start %s's engine", identityID)
	}
	deviceID, err := substrate.NewNanoID()
	if err != nil {
		return nil, fmt.Errorf("generate device id: %w", err)
	}
	token, _, err := m.deps.Signer.mint(identityID)
	if err != nil {
		return nil, fmt.Errorf("mint engine credential: %w", err)
	}
	tokenSource := func() (string, error) {
		t, _, err := m.deps.Signer.mint(identityID)
		return t, err
	}
	eng, err := newEngine(m.baseCtx, m.deps.engineConfig, identityID, deviceID, token, tokenSource)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if e, ok := m.entries[identityID]; ok {
		// Lost a race with a concurrent first Acquire for the same
		// identity — keep the winner already installed, discard this one.
		e.refCount++
		e.idleSince = time.Time{}
		m.mu.Unlock()
		eng.Close()
		return e.eng, nil
	}
	m.entries[identityID] = &engineEntry{eng: eng, refCount: 1}
	m.mu.Unlock()
	return eng, nil
}

// Release decrements identityID's holder count. A count reaching zero starts
// its idle countdown (reapLoop) rather than closing the engine synchronously
// — a fast release/reacquire must not pay a full re-hydration.
func (m *engineManager) Release(identityID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[identityID]
	if !ok {
		return
	}
	e.refCount--
	if e.refCount <= 0 {
		e.refCount = 0
		e.idleSince = time.Now()
	}
}

func (m *engineManager) reapLoop(ctx context.Context) {
	ticker := time.NewTicker(engineReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reapIdle()
		}
	}
}

func (m *engineManager) reapIdle() {
	m.mu.Lock()
	var toClose []*engine
	for id, e := range m.entries {
		if e.pinned {
			continue
		}
		if e.refCount == 0 && !e.idleSince.IsZero() && time.Since(e.idleSince) > engineIdleTimeout {
			toClose = append(toClose, e.eng)
			delete(m.entries, id)
		}
	}
	m.mu.Unlock()
	for _, eng := range toClose {
		eng.Close()
	}
}

// Purge stops identityID's engine and DELETES its local mirror (the bbolt
// store file under StoreDir) — design §4.4's "on confirmed revocation/
// sign-out the local mirror is purged" (documented residual: host-level
// storage until the media is reused).
//
// This is where two ratified sentences that conflict for exactly this case
// are reconciled. Inc 2's "warm resume" (§7.2) serves a tab close or a
// reload — the user never said to forget them. An EXPLICIT sign-out, or a
// credential the platform has revoked, is the opposite instruction. So a
// reload still resumes warm (the engine merely idles), while sign-out and
// revocation purge. Inc 2's green bar — "sign out and back in re-enters the
// same identity" — still holds: re-entry re-hydrates instead of resuming,
// which is a latency property, not that bar.
//
// Purging is also what makes the §4.4 sign-out flow RECOVERABLE rather than
// a dead end for a REVOKED credential specifically. Revocation denies future
// Gateway calls and reconnects without necessarily closing an already-open
// NATS connection, so Acquire's own liveness check (conn.NATS().IsClosed())
// does not catch it — an engine keeps serving a revoked identity's cached
// local reads until something evicts it. Dropping the entry here forces the
// next Acquire to build a new engine with a freshly minted credential; a
// merely-expired-then-recovered engine has newEngine's TokenHandler for
// that instead and needs no explicit purge.
func (m *engineManager) Purge(identityID string) error {
	// Defense in depth against a path escape: identityID is splice into a
	// filename below, and filepath.Join CLEANS its result rather than
	// neutralizing "..", so a non-NanoID id like "../../x" would resolve to
	// a delete OUTSIDE StoreDir entirely. The login path already refuses a
	// non-NanoID subject; this is the sink refusing independently, so no
	// future caller can reintroduce the traversal.
	if !substrate.IsValidNanoID(identityID) {
		return fmt.Errorf("purge: refusing a non-NanoID identity %q", identityID)
	}
	m.mu.Lock()
	e, ok := m.entries[identityID]
	if ok {
		delete(m.entries, identityID)
	}
	m.mu.Unlock()

	if ok {
		e.eng.Close()
	}
	// Delete the mirror even when no engine was live: an already-reaped
	// engine, or a prior process lifetime, can have left the file behind —
	// the point of the purge is that nothing of this identity survives
	// locally, not merely that the running copy stops.
	storePath := filepath.Join(m.deps.StoreDir, identityID+".db")
	if err := os.Remove(storePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("purge local mirror %q: %w", storePath, err)
	}
	return nil
}

// CloseAll stops every running engine — process shutdown.
func (m *engineManager) CloseAll() {
	m.mu.Lock()
	all := make([]*engine, 0, len(m.entries))
	for id, e := range m.entries {
		all = append(all, e.eng)
		delete(m.entries, id)
	}
	m.mu.Unlock()
	for _, eng := range all {
		eng.Close()
	}
}
