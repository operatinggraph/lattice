package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/operatinggraph/lattice/internal/appsession"
	"github.com/operatinggraph/lattice/internal/edge/store"
	edgesync "github.com/operatinggraph/lattice/internal/edge/sync"
	"github.com/operatinggraph/lattice/internal/edge/transport/natstransport"
	"github.com/operatinggraph/lattice/internal/refractor/control/controlwire"
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
	Signer *appsession.Signer
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
// no longer bound to one process-wide identity: it's per-identity,
// bounded by however many distinct identities are actively signed in on this
// host at once, not by request volume.
type engineManager struct {
	mu      sync.Mutex
	entries map[string]*engineEntry
	deps    engineManagerDeps
	baseCtx context.Context
	// sf dedupes concurrent builds for one identity: every caller arriving
	// while that identity's engine is being built shares the single in-flight
	// build and its result. Deliberately NOT m.mu — m.mu guards the entries
	// map for EVERY identity, so holding it across a build's NATS dial and
	// bbolt open would serialize unrelated identities' builds too. Keyed on
	// identityID, so different identities still build fully concurrently.
	sf singleflight.Group
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
// so reaching that case means reconnection genuinely failed for good
// (nats.go exhausted MaxReconnects, or aborted after repeated identical auth
// errors) — a last-resort backstop. The eviction itself belongs to buildEngine
// (see its doc: evicting outside the flight would let a second caller's
// newEngine start while the old engine is still releasing the mirror's file
// lock). Purge remains the explicit eviction path for revocation (see its doc).
//
// Concurrent Acquires for the SAME identity share one build (m.sf,
// buildEngine): two of them must never both reach newEngine, which opens the
// identity's single-writer bbolt mirror and starts its sync loops — the loser
// of that race either times out on the store's open lock or attaches to, and
// then tears down, the winner's durable.
func (m *engineManager) Acquire(identityID string) (*engine, error) {
	for {
		m.mu.Lock()
		if e, ok := m.entries[identityID]; ok && (e.pinned || !e.eng.conn.NATS().IsClosed()) {
			e.refCount++
			e.idleSince = time.Time{}
			m.mu.Unlock()
			return e.eng, nil
		}
		m.mu.Unlock()

		if m.deps.Signer == nil {
			return nil, fmt.Errorf("no credential minter configured (FACET_DEV_AUTH not set) — cannot start %s's engine", identityID)
		}

		built, err, _ := m.sf.Do(identityID, func() (any, error) {
			return m.buildEngine(identityID)
		})
		if err != nil {
			// Shared with every waiter on the same build: one failed build is
			// one failure for all of them, rather than each caller getting its
			// own (and differing) outcome for the same identity.
			return nil, err
		}
		eng := built.(*engine)

		// Claim this caller's hold. Everyone who shared the build runs this —
		// the caller that ran it and every caller that waited on it — so each
		// ends up owing exactly one Release, per this method's contract.
		m.mu.Lock()
		if e, ok := m.entries[identityID]; ok {
			e.refCount++
			e.idleSince = time.Time{}
			m.mu.Unlock()
			return e.eng, nil
		}
		if eng.conn.NATS().IsClosed() {
			// A Purge or a CloseAll landed between the build installing this
			// entry and this caller claiming a hold on it — taking the engine
			// with it. Handing back a corpse (or reinstalling one for the next
			// caller to find) is worse than paying for another build, so retry.
			// This terminates: a retry only happens when a fresh purge lands in
			// that window, and each pass does a whole build rather than
			// spinning — and once baseCtx is cancelled newEngine fails outright.
			m.mu.Unlock()
			continue
		}
		m.entries[identityID] = &engineEntry{eng: eng, refCount: 1}
		m.mu.Unlock()
		return eng, nil
	}
}

// buildEngine returns identityID's engine, building one when the manager holds
// none that is usable: it evicts a cached-but-dead entry, mints a fresh device
// credential, starts the engine, and installs the entry — holder-less, since
// each caller claims its own hold back in Acquire.
//
// It runs ONLY under m.sf, and that is what makes a second, mirror-racing
// newEngine call for one identity impossible. A concurrent Acquire either
// joins the in-flight build, or — arriving after it settles — starts a fresh
// one that finds the entry installed here and returns it without opening the
// store at all: singleflight forgets a key only after the function returns, so
// the install always precedes the forget, leaving no window in which a later
// build can begin while this one's entry is still uninstalled.
//
// The dead-entry eviction lives here, inside the flight, for the same reason:
// eng.Close() is what releases the mirror's bbolt lock, so evicting from
// Acquire — outside the flight — would let a second caller's newEngine start
// its store.Open against a lock the old engine has not finished dropping,
// which is the very open-lock timeout this serialization exists to prevent.
func (m *engineManager) buildEngine(identityID string) (*engine, error) {
	m.mu.Lock()
	if e, ok := m.entries[identityID]; ok {
		if e.pinned || !e.eng.conn.NATS().IsClosed() {
			m.mu.Unlock()
			return e.eng, nil
		}
		delete(m.entries, identityID)
		m.mu.Unlock()
		e.eng.Close()
	} else {
		m.mu.Unlock()
	}

	token, _, err := m.deps.Signer.Mint(identityID)
	if err != nil {
		return nil, fmt.Errorf("mint engine credential: %w", err)
	}
	tokenSource := func() (string, error) {
		t, _, err := m.deps.Signer.Mint(identityID)
		return t, err
	}
	// Empty device id: newEngine resolves this host's stable, store-persisted
	// one for identityID, so a rebuild after an idle reap (or a process
	// restart) re-binds the durable the last engine left rather than
	// orphaning it and starting a new one.
	eng, err := newEngine(m.baseCtx, m.deps.engineConfig, identityID, "", token, tokenSource)
	if err != nil {
		return nil, err
	}

	// Defer to an entry that appeared mid-build only if it is actually usable.
	// The one that can appear here is a CORPSE: a Purge landing between this
	// flight installing an entry and an earlier caller claiming its hold makes
	// that caller reinstall its now-closed engine (Acquire's claim step), and
	// this build — started against the freed mirror — is the live replacement.
	// Deferring to it blindly would close a good engine and hand back a dead
	// one to everybody. Superseding the map entry leaks nothing: an entry
	// reachable here is always backed by an already-closed engine, since a
	// still-open one would still hold the mirror's file lock and newEngine
	// above could not have returned (internal/edge/store/bolt.go's Open — the
	// lock contends within this process too). The pinned/live arm is kept as
	// defense in depth for a future second writer of entries; today's only
	// other one, Seed, cannot reach it for that same file-lock reason.
	m.mu.Lock()
	if e, ok := m.entries[identityID]; ok && (e.pinned || !e.eng.conn.NATS().IsClosed()) {
		m.mu.Unlock()
		eng.Close()
		return e.eng, nil
	}
	m.entries[identityID] = &engineEntry{eng: eng}
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

	// A live engine already carries the id; taking it from there means the
	// common sign-out (an SSE holder keeps the engine up) never contends for
	// the mirror's file lock at all.
	deviceID := ""
	if ok {
		deviceID = e.eng.deviceID
		e.eng.Close()
	}
	// Delete the mirror even when no engine was live: an already-reaped
	// engine, or a prior process lifetime, can have left the file behind —
	// the point of the purge is that nothing of this identity survives
	// locally, not merely that the running copy stops.
	storePath := filepath.Join(m.deps.StoreDir, identityID+".db")
	// The device id dies with the mirror, so the durable it names would
	// outlive its last reader — reap it while the id is still known. Runs
	// after the engine is closed (bbolt is single-writer) and before the
	// file is removed.
	m.reapSyncDurable(identityID, deviceID, storePath)
	if err := os.Remove(storePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("purge local mirror %q: %w", storePath, err)
	}
	return nil
}

// reapDurableTimeout bounds the whole connect-and-delete round trip a purge
// makes. Sign-out is a synchronous user action, so an unreachable NATS must
// not hold it open.
const reapDurableTimeout = 5 * time.Second

// reapSyncDurable deletes the SYNC durable consumer identityID was feeding
// and deregisters its personal-lens-interest registration, over a
// short-lived connection of its own (the engine's is already closed by the
// time a purge reaches here). An empty deviceID is read back from the mirror
// at storePath — the no-engine-was-live path.
//
// Every failure is logged and swallowed: a purge must never fail the
// sign-out it is part of, and its load-bearing half — the local mirror —
// is deleted by the caller regardless. A revoked credential in particular
// CANNOT complete this — the auth callout refuses the connection, which is
// the correct outcome for revocation. What that leaves behind is one
// durable and one registration, unreachable from this host ever after: the
// id naming them is in the mirror the caller is about to delete.
func (m *engineManager) reapSyncDurable(identityID, deviceID, storePath string) {
	if m.deps.Signer == nil {
		return // boot-env fallback: no minter, so no connection to reap over
	}
	logger := m.deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if deviceID == "" {
		st, err := store.Open(storePath)
		if err != nil {
			logger.Warn("facet purge: local mirror unreadable; leaving the sync durable in place", "identityId", identityID, "err", err)
			return
		}
		id, ok, err := readDeviceID(st)
		_ = st.Close()
		if err != nil {
			logger.Warn("facet purge: device id unreadable; leaving the sync durable in place", "identityId", identityID, "err", err)
			return
		}
		if !ok {
			return // never opened an engine on this host — nothing named, nothing to reap
		}
		deviceID = id
	}

	token, _, err := m.deps.Signer.Mint(identityID)
	if err != nil {
		logger.Warn("facet purge: mint failed; leaving the sync durable in place", "identityId", identityID, "err", err)
		return
	}
	base := m.baseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, reapDurableTimeout)
	defer cancel()
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL: m.deps.NATSURL,
		// The same bare device id the engine connected under: natsauth
		// grants $JS.API.CONSUMER.DELETE.SYNC.<durable> for exactly the
		// durable this CONNECT name spells (natsauth's PermissionsFor), so
		// any other name here would be denied.
		Name: deviceID,
		// reapDurableTimeout already bounds this whole round trip to 5s, so a
		// reconnect budget adds no safety — it only exists to opt OUT of
		// nats.go's own default (60 attempts, ~2s apart). 1 is the smallest
		// value substrate.Connect can actually express as "don't linger":
		// substrate only threads MaxReconnects into nats.go's options when
		// the field is nonzero (internal/substrate/conn.go), so a literal 0
		// would silently fall back to that same 60-attempt default.
		MaxReconnects: 1,
		Token:         token,
		InboxPrefix:   "_INBOX.edge." + identityID,
	})
	if err != nil {
		logger.Warn("facet purge: connect failed; leaving the sync durable in place", "identityId", identityID, "err", err)
		return
	}
	defer conn.Close()
	durable := edgesync.DurableName(identityID, deviceID)
	if err := conn.DeleteStreamConsumer(ctx, edgesync.DefaultStream, durable); err != nil {
		logger.Warn("facet purge: sync durable not reaped", "identityId", identityID, "durable", durable, "err", err)
	} else {
		logger.Info("facet purge: sync durable reaped", "identityId", identityID, "durable", durable)
	}

	// The durable and the personal-lens-interest registration are two
	// independent artifacts of the same device (edge-sync-orphan-expiry-
	// design.md §1.2/§5 Inc 3a): attempted regardless of whether the delete
	// above succeeded, over the SAME connection, in the SAME
	// swallow-every-failure posture — a purge must never fail the
	// sign-out/revocation it is part of.
	deregisterInterest(ctx, conn, identityID, deviceID, logger)
}

// edgeActorHeader returns the Lattice-Actor header value facet stamps on
// every control-plane request it self-asserts for identityID — the
// vtx.identity.<id> vertex-key shape internal/refractor/control/service.go's
// self-asserted-body fallback binds to. Shared by every self-asserting call
// site in this package so there is exactly one spelling of it.
func edgeActorHeader(identityID string) string {
	return "vtx.identity." + identityID
}

// deregisterInterest issues the "personal.deregister" control op so a
// device's Interest Set registration does not outlive the durable it names —
// the caller Deregister has been waiting for since it shipped
// (personalinterest.Deregister). The Lattice-Actor header carries facet's
// self-asserted actor key (edgeActorHeader) — exactly what this connection's
// own "personal.register" already sends on the sync Manager's behalf
// (engine.go's ActorHeader). With no ActorVerifier configured on the control
// plane the header is trusted as asserted; with one configured it is instead
// authenticated as a signed token (internal/controlauth/verified_actor.go:
// 58-70's ResolveActor passes it straight to Authenticate), so a bare
// vtx.identity.<id> string is refused there — the same way register's
// identical header is refused on the same connection. The two ops fail
// alike under that posture, which is why swallowing the failure here costs
// nothing beyond what an unregistered device already costs.
func deregisterInterest(ctx context.Context, conn *substrate.Conn, identityID, deviceID string, logger *slog.Logger) {
	actorHeader := edgeActorHeader(identityID)
	data, err := json.Marshal(controlwire.ControlRequest{IdentityID: identityID, DeviceID: deviceID})
	if err != nil {
		logger.Warn("facet purge: marshal deregister request failed; leaving the interest registration in place", "identityId", identityID, "deviceId", deviceID, "err", err)
		return
	}
	reply, err := natstransport.New(conn).Request(ctx, controlwire.ControlSubject("personal", "deregister"), data, actorHeader)
	if err != nil {
		logger.Warn("facet purge: deregister request failed; leaving the interest registration in place", "identityId", identityID, "deviceId", deviceID, "err", err)
		return
	}
	var resp controlwire.ControlResponse
	if err := json.Unmarshal(reply, &resp); err != nil {
		logger.Warn("facet purge: deregister response undecodable; leaving the interest registration in place", "identityId", identityID, "deviceId", deviceID, "err", err)
		return
	}
	if resp.Error != "" {
		logger.Warn("facet purge: deregister refused", "identityId", identityID, "deviceId", deviceID, "err", resp.Error)
		return
	}
	logger.Info("facet purge: interest registration deregistered", "identityId", identityID, "deviceId", deviceID)
}

// engineFleetSnapshot is a read-only point-in-time summary of every engine
// this manager currently holds — the health probe's dependency signal for
// the crash-looping-engine axis (facet-host-health-emission-design.md §4.3).
type engineFleetSnapshot struct {
	Total            int
	Pinned           int
	SyncDegraded     int
	NatsDisconnected int
}

// healthSnapshot folds every entry's feed.connectivityState() (the sticky
// sync-manager-in-restart-backoff bit) and its NATS connection's live
// IsConnected() — two distinct axes (ce050a7 deliberately separated
// crash-looping-sync-manager from mere connectivity) — into aggregate counts
// only, never per-identity detail (design §8.2/§9: no identity id may appear
// in a marshaled heartbeat).
func (m *engineManager) healthSnapshot() engineFleetSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := engineFleetSnapshot{Total: len(m.entries)}
	for _, e := range m.entries {
		if e.pinned {
			s.Pinned++
		}
		if _, syncDegraded := e.eng.feed.connectivityState(); syncDegraded {
			s.SyncDegraded++
		}
		if !e.eng.conn.NATS().IsConnected() {
			s.NatsDisconnected++
		}
	}
	return s
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
