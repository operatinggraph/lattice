// Package sync is the Edge node's Sync Manager (edge-lattice-full-design.md
// §3.2): it consumes a durable delta feed of the Personal-Lens SYNC stream,
// applies inbound delta envelopes to the Local VAL Store (internal/edge/store)
// under last-writer-wins-by-revision, persists the cursor, and — on cold
// start or a detected retention gap — calls the Personal-Lens
// "personal.register"/"personal.hydrate" control RPCs (internal/refractor/
// control) before resuming incremental delivery.
//
// The feed and the control RPCs arrive through the host-supplied Transport
// seam (internal/edge/transport), not a concrete connection: these semantics
// are identical whether the deltas come over TCP from a trusted Go host or
// over WebSocket from a browser host.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	stdsync "sync"
	"time"

	"github.com/operatinggraph/lattice/internal/edge/store"
	"github.com/operatinggraph/lattice/internal/edge/transport"
	"github.com/operatinggraph/lattice/internal/refractor/control/controlwire"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
)

// DefaultStream is the JetStream stream a Personal Lens publishes its deltas
// to, and the one a Manager's durable consumer binds to when Config.Stream is
// empty. Exported because a host that reaps its own durable outside the
// Manager's lifetime (cmd/facet's sign-out purge) must name the same stream.
const DefaultStream = "SYNC"

// DurableName is the JetStream durable-consumer name a device's delta feed
// binds to. The name is STABLE per identity+device (unlike Loom's
// per-boot-nonce pattern), and the stability is what keeps a device to ONE
// consumer: a host that hands a fresh device id to every engine it builds
// orphans one durable per build, each left holding the SYNC stream's whole
// retained subject. The delivery position across restarts comes from the
// node's own local cursor (see ensureFresh), not from the name.
//
// The format is load-bearing beyond this package: the Gateway's NATS
// auth-callout grants exactly this name's consumer subjects
// (internal/gateway/natsauth's PermissionsFor), so a host reaping its own
// durable must derive the name here rather than re-spelling it. Delegates to
// subjects.EdgeSyncDurable, the single constructor every caller across the
// tree — this package, Loupe's fleet inspector, Refractor's own
// reconciliation — shares.
func DurableName(identityID, deviceID string) string {
	return subjects.EdgeSyncDurable(identityID, deviceID)
}

const (
	defaultSubjectPrefix = "lattice.sync.user"

	// syncGapMaxAttempts bounds the warm-resume gap check's retry of the
	// personal.syncgap control RPC (edge-syncgap-control-rpc-design.md §7).
	// The control plane may briefly be unavailable at boot (Refractor still
	// starting, or the personal lens rule not yet loaded → the syncgap seam
	// nil, fail-closed), a transient window the JetStream stream-info admin
	// call this replaces did not have — the NATS server answered that directly
	// and needed no tolerance. A persistent failure still fails closed after the bound:
	// freshness is unverifiable, so the node must never resume unverified.
	syncGapMaxAttempts = 3
	// syncGapRetryBaseBackoff is the initial delay between syncgap attempts,
	// doubled each retry. Short enough not to stall a healthy boot, present so
	// a transient control-plane blip does not fail the whole session.
	syncGapRetryBaseBackoff = 100 * time.Millisecond
)

// deltaEnvelope mirrors the wire shape a Personal Lens delta publishes to
// lattice.sync.user.<actor> (internal/refractor/adapter/natssubject.go's
// unexported deltaEnvelope; docs/components/refractor.md). Deliberately
// re-declared rather than imported: internal/refractor/adapter is a
// Refractor-internal package whose deltaEnvelope type is unexported, and the
// Edge is a separate application consuming only the documented wire contract.
type deltaEnvelope struct {
	Op            string          `json:"op"` // "upsert" | "delete" | "keyset" | "hydrationComplete"
	Key           string          `json:"key,omitempty"`
	Revision      uint64          `json:"revision"`
	ProjectionSeq uint64          `json:"projectionSeq"`
	Data          json.RawMessage `json:"data,omitempty"`
	// Lens is the producing Personal Lens's rule ID (personal-lens-
	// retraction-design.md §3.1), set on "upsert" and "keyset" envelopes.
	// Empty on an "upsert" from a pre-R1 wire producer — handle() treats that
	// as an unattributed write, exactly as before this design.
	Lens string `json:"lens,omitempty"`
	// Keys carries a "keyset" envelope's complete, authoritative business-key
	// set for its actor+lens as of Revision — nil/empty is the meaningful
	// last-row-retraction case.
	Keys []string `json:"keys,omitempty"`
}

// Config configures a Manager. IdentityID and DeviceID are required;
// SubjectPrefix and Stream default to the platform convention
// ("lattice.sync.user" / "SYNC") when empty.
type Config struct {
	SubjectPrefix string
	Stream        string
	IdentityID    string
	DeviceID      string
	// ActorHeader is stamped as the Lattice-Actor header on every
	// personal.register/personal.hydrate control-plane request (trusted
	// posture, EDGE.1 — no JWT yet; EDGE.3 replaces this with the Gateway
	// path). Empty sends no header, matching the control plane's
	// self-asserted-actor default.
	ActorHeader string
	// Types/Anchors seed the device's Interest Set registration
	// (personal.register). Both empty registers an unfiltered device (the
	// full authorized slice — personalinterest.Register's documented
	// behavior), which is EDGE.1's posture.
	Types   []string
	Anchors []string
	Logger  *slog.Logger
	// OnChange, if set, is invoked from handle() after a delivered upsert or
	// delete actually lands in the Local VAL Store (a stale/reordered delta
	// dropped under last-writer-wins-by-revision does not fire this). key is
	// the Contract #1 key that changed; deleted reports which store method
	// applied it. A UI host uses this to react to deltas instead of polling
	// overlay.Read per key (edge-showcase-app-design.md §7 Fire 0, G3).
	OnChange func(key string, deleted bool)
	// OnHydrationComplete, if set, is invoked from handle() when the
	// terminal "hydrationComplete" delta for the cold bulk projection
	// arrives — the signal a UI host uses to stop showing a loading state
	// (facet-app-ux.md §2/§3.0: "nothing today tells a host process the
	// initial catch-up is done").
	OnHydrationComplete func(revision uint64)
	// OnRunEstablished, if set, is invoked once per Run call, after
	// ensureFresh succeeds and before the durable consumer starts — on the
	// cold-hydrate and warm-resume paths alike (OnHydrationComplete fires
	// only on the former). A host whose restart loop marked sync degraded
	// when a prior Run exited uses this, not a delta callback, to clear the
	// indicator: a quiet world delivers no deltas, so waiting on one would
	// leave a recovered engine flagged degraded indefinitely.
	OnRunEstablished func()
}

// Transport is the host-supplied seam a Manager drives: the durable delta
// feed it applies, and the control request-reply it registers/hydrates over.
// A trusted Go host satisfies it with transport.NewSubstrate; a browser host
// satisfies it over a JS NATS client on WebSocket.
type Transport interface {
	transport.DeltaSource
	transport.ControlClient
}

// Manager is the Edge node's Sync Manager.
type Manager struct {
	tr      Transport
	store   store.Store
	cfg     Config
	stream  string
	prefix  string
	durable string
	logger  *slog.Logger

	// gateMu guards hydrateTarget/hydrateArmed (see armHydrateGate /
	// hydrationGateReady): hydrate() arms it from Run's/Rehydrate's calling
	// goroutine while handle() reads it from the durable consumer's delivery
	// goroutine.
	gateMu        stdsync.Mutex
	hydrateTarget uint64
	hydrateArmed  bool

	// floor keeps the persisted cursor a contiguous resume floor rather than a
	// high-water mark. See deliveryFloor.
	floor deliveryFloor
}

// deliveryFloor tracks, for one attach, which delivered sequences are still
// unresolved, so the persisted cursor never advances past a message the node
// has asked to be given again.
//
// It exists because the cursor is the resume authority. Delivery is serial but
// a Nak'd message is redelivered LATER, so higher sequences resolve first: a
// cursor written as "the sequence that just succeeded" would sit above a hole,
// and the next attach — which deletes the durable, discarding the server-side
// ack floor that was holding that hole open — would start above it and never
// see it again. A `delete` or `keyset` frame lost that way leaves the Local VAL
// Store holding a key the actor no longer has, and the warm path runs no
// keyset heal to notice. Gap detection cannot catch it either: `personal.syncgap`
// tests `cursor < firstSeq`, and a cursor that is too HIGH is not a gap.
//
// # Lifetime
//
// Per attach, in memory only. Nothing about it is persisted; the store holds
// one number, the floor itself.
//
//	| boundary | pending | highest | persisted |
//	|---|---|---|---|
//	| created — at each attach, in Run, before the consumer starts | emptied | 0 | seeded from the store's cursor |
//	| a delivered message Naks | + its sequence | — | — |
//	| a delivered message Acks or Terms | − its sequence | raised to it | raised to the floor once the store write lands |
//	| an Ack whose cursor write fails (returned as Nak) | + its sequence again | — | unchanged — the write is retried by a later resolve |
//	| a Term whose cursor write fails | — | raised | unchanged — the server will not redeliver, so the position is passed regardless |
//	| the iterator reopens mid-attach (transient receive error) | carried | carried | carried — the abandoned iterator is drained through this handler first (internal/substrate's drainBuffered), so every message the server already delivered resolves into this same floor before higher sequences arrive on the reopened one |
//	| the process crashes | discarded | discarded | whatever last landed in the store, which is by construction at or below every hole |
//
// The floor is `highest`, capped at one below the lowest pending sequence, and
// is only ever written when it exceeds what is already persisted. That
// monotonicity is what makes a late redelivery harmless: resolving sequence 100
// after 105 has resolved raises nothing, because `highest` is a maximum and the
// store write is skipped when the computed floor does not exceed the last one.
// A blind `SetCursor(seq)` would instead rewind the cursor to 100.
//
// It is deliberately conservative: while a hole is open the floor stops at the
// hole and no later progress is recorded, so a crash re-delivers a little.
// Under-advancing over-delivers; over-advancing loses data.
type deliveryFloor struct {
	mu        stdsync.Mutex
	pending   map[uint64]struct{}
	highest   uint64
	persisted uint64
}

// reset starts a new attach's bookkeeping from the cursor already in the store.
func (f *deliveryFloor) reset(persisted uint64) {
	f.mu.Lock()
	f.pending = make(map[uint64]struct{})
	f.highest = 0
	f.persisted = persisted
	f.mu.Unlock()
}

// hold records that seq is outstanding: it has been asked for again, so the
// floor must not pass it. Sequence 0 means "no position available" (delivery
// metadata was missing) and is not a hole anything could resume from.
func (f *deliveryFloor) hold(seq uint64) {
	if seq == 0 {
		return
	}
	f.mu.Lock()
	if f.pending == nil {
		f.pending = make(map[uint64]struct{})
	}
	f.pending[seq] = struct{}{}
	f.mu.Unlock()
}

// release records that seq is permanently resolved and reports the floor to
// persist, or advanced=false when the floor has not moved past what is already
// stored.
func (f *deliveryFloor) release(seq uint64) (floor uint64, advanced bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pending, seq)
	if seq > f.highest {
		f.highest = seq
	}
	floor = f.highest
	for outstanding := range f.pending {
		if outstanding-1 < floor {
			floor = outstanding - 1
		}
	}
	if floor <= f.persisted {
		return 0, false
	}
	return floor, true
}

// commit records that floor reached the store.
func (f *deliveryFloor) commit(floor uint64) {
	f.mu.Lock()
	if floor > f.persisted {
		f.persisted = floor
	}
	f.mu.Unlock()
}

// New creates a Manager. Returns an error if cfg.IdentityID or cfg.DeviceID
// is empty.
func New(tr Transport, st store.Store, cfg Config) (*Manager, error) {
	if cfg.IdentityID == "" || cfg.DeviceID == "" {
		return nil, fmt.Errorf("edge/sync: IdentityID and DeviceID are both required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	stream := cfg.Stream
	if stream == "" {
		stream = DefaultStream
	}
	prefix := cfg.SubjectPrefix
	if prefix == "" {
		prefix = defaultSubjectPrefix
	}
	return &Manager{
		tr:      tr,
		store:   st,
		cfg:     cfg,
		stream:  stream,
		prefix:  prefix,
		durable: DurableName(cfg.IdentityID, cfg.DeviceID),
		logger:  logger,
	}, nil
}

// Run drives the Sync Manager until ctx is cancelled. On cold start (no
// local cursor yet) or a detected gap (the local cursor has fallen behind
// the SYNC stream's retention window), it hydrates via the Personal-Lens
// control RPCs (§3.3) before subscribing; otherwise it subscribes directly.
// Either way the feed is positioned at the sequence ensureFresh resolves, so
// the consumer starts where this node's knowledge ends instead of at the
// subject's retained beginning. Blocks until ctx is done; returns the durable
// consumer's terminal error, if any.
//
// Nothing about the position is resolved until the transport says its host may
// attach (transport.AttachGate — a no-op for a transport that does not
// implement it). The gap check, the hydrate it may trigger and the delivery
// floor all read state that goes stale while a host waits for its turn: a
// browser tab can compute a position at page boot and then park on the Web Lock
// behind another tab for days, by which time the SYNC stream's retention window
// may have passed that position — which JetStream clamps UP, skipping the span
// in between, exactly the direction the cursor exists to prevent.
func (m *Manager) Run(ctx context.Context) error {
	if gate, ok := m.tr.(transport.AttachGate); ok {
		if err := gate.AwaitAttachReady(ctx); err != nil {
			return fmt.Errorf("edge/sync: await attach readiness: %w", err)
		}
	}
	startSeq, err := m.ensureFresh(ctx)
	if err != nil {
		return fmt.Errorf("edge/sync: ensure fresh: %w", err)
	}
	// Seed the attach's floor bookkeeping from what is actually in the store,
	// which is where the next attach will read it from too.
	stored, _, err := m.store.Cursor()
	if err != nil {
		return fmt.Errorf("edge/sync: read cursor: %w", err)
	}
	m.floor.reset(stored)
	if m.cfg.OnRunEstablished != nil {
		m.cfg.OnRunEstablished()
	}
	return m.tr.RunDurableConsumer(ctx, transport.ConsumerConfig{
		Stream:        m.stream,
		FilterSubject: subjects.PersonalSync(m.prefix, m.cfg.IdentityID),
		Durable:       m.durable,
		StartSeq:      startSeq,
		Logger:        m.logger,
	}, m.handle)
}

// Rehydrate runs a fresh cold bulk projection unconditionally — the
// internal/edge/agent package's conflict re-audit (edge-lattice-full-
// design.md §3.5): a RevisionConflict means the cloud state moved under an
// offline edit, so the mirror needs to catch up before the user re-decides.
// No anchor-scoped hydrate RPC ships yet, so this reuses the same full
// personal.hydrate call ensureFresh makes on cold start/gap, rather than
// inventing a narrower primitive.
//
// It discards the hydrate's delivery position. Repositioning is a property of
// ATTACHING a feed, and this runs mid-session against a consumer that is
// already attached and already caught up: there is no backlog in front of it
// to skip, and moving it would mean tearing down a live consumer to buy
// nothing (edge-cold-signin-delivery-position-design.md §3.5).
func (m *Manager) Rehydrate(ctx context.Context) error {
	_, err := m.hydrate(ctx)
	return err
}

// UpdateInterest re-registers the device's Interest Set with new types/
// anchors via the "personal.register" control RPC alone — no cold
// personal.hydrate call. Use this when a host changes what the user is
// watching (edge-showcase-app-design.md §7 Fire 0, G4): registration is
// additive server-side (personalinterest.Register widens/narrows the
// server's push filter), and the already-hydrated store keeps whatever it
// holds for keys no longer in scope until GC reclaims them — this call does
// not retroactively hydrate a newly-widened scope's backlog. Callers that
// need the newly-in-scope data populated immediately should follow with
// Rehydrate. cfg.Types/cfg.Anchors are updated so a later reconnect/hydrate
// re-registers with the same interest.
func (m *Manager) UpdateInterest(ctx context.Context, types, anchors []string) error {
	m.cfg.Types = types
	m.cfg.Anchors = anchors
	return m.registerInterest(ctx)
}

// ensureFresh hydrates when the local store has never been hydrated (no
// cursor) or when the stored cursor has fallen behind the SYNC stream's
// current retention window (a long disconnect pruned messages the node
// never saw) — the vault's "ephemerality: re-hydrate, don't backlog-replay"
// (§3.2/§3.3). A warm cursor still within retention hydrates nothing; the
// subsequent durable consumer just resumes incrementally.
//
// It returns the SYNC stream sequence that consumer must begin delivery at:
// the last position this node has accounted for, plus one. A hydrate accounts
// for everything at or below the snapshot it was taken from, because the burst
// carries that whole world; a warm resume accounts for everything at or below
// the persisted cursor.
//
// # The resume position — lifetime
//
// The position is DERIVED per attach and never stored. Its only persisted
// input is the cursor, which handle() keeps as a contiguous floor at or below
// every sequence still awaiting redelivery (see deliveryFloor). Nothing
// carries the position between calls; nothing needs resetting.
//
//	| boundary                                    | position         | reasoning |
//	|---------------------------------------------|------------------|-----------|
//	| cold start (no cursor) → hydrate            | syncStartSeq + 1 | the burst is the world as of syncStartSeq; everything below it is already in the burst |
//	| gap / operator-requested hydration          | syncStartSeq + 1 | a gap is a cold start holding a stale cursor — the burst supersedes it |
//	| warm restart, no gap                        | cursor + 1       | the cursor is a contiguous floor — every sequence at or below it is resolved |
//	| warm restart, persisted cursor of 0         | 0 → DeliverAll   | zero is not a position: nothing has been applied, so the node asserts none |
//	| any path, control plane answers 0           | 0 → DeliverAll   | a control host with no position seam; delivering the whole subject is what shipped before there was a position at all |
//	| crash between the durable's delete and its create | unchanged  | the position is recomputed from the cursor on the next attach; a durable that no longer exists is simply created |
//	| upgrade from an existing DeliverAll durable | as above         | a non-zero position recreates the durable, so the old policy does not survive to conflict |
//
// Every fallback is the same direction: assert no position and receive MORE of
// the node's own subject. A message can only be skipped by resuming ABOVE it,
// and neither input allows that — a hydrate's snapshot sequence is a position
// the burst has already accounted for, and the cursor is a floor no unresolved
// sequence sits below.
func (m *Manager) ensureFresh(ctx context.Context) (startSeq uint64, err error) {
	cursor, ok, err := m.store.Cursor()
	if err != nil {
		return 0, fmt.Errorf("read cursor: %w", err)
	}
	if ok {
		gapped, hydrationRequested, err := m.gapped(ctx, cursor)
		if err != nil {
			return 0, fmt.Errorf("check gap: %w", err)
		}
		if !gapped && !hydrationRequested {
			// A warm resume hydrates nothing, so nothing else on this path
			// would touch the server-side registration — and the attach that
			// follows DELETES this device's durable before recreating it
			// (natstransport.RunDurableConsumer, and the browser shell does
			// the same), so the registration is the only artifact that
			// vouches for the device across that window.
			//
			// Registering here, before the attach, is the same order hydrate()
			// uses. It buys two things: registeredAt becomes a real
			// "last attached" instant rather than a one-time birth stamp, and
			// a registration that was legitimately reaped while this device
			// was away comes back when it returns — which is the normal case
			// for the browser host, whose durable carries a 30-minute
			// InactiveThreshold, so a tab closed overnight loses its durable
			// by design and must not lose its roster identity with it.
			//
			// A failure here does not fail the attach. The Interest Set is a
			// bandwidth filter whose absence admits everything
			// (personalinterest: "absence is never a denial"), so a device
			// that could not refresh it still syncs correctly, just
			// unfiltered; trading a working resume for a roster row would be
			// the wrong direction, and the next attach retries.
			if rerr := m.registerInterest(ctx); rerr != nil {
				m.logger.Warn("edge/sync: warm resume could not refresh its Interest Set registration", "err", rerr)
			}
			if cursor == 0 {
				// A persisted zero records no applied message, so cursor+1
				// would assert the node had passed sequence 1 without ever
				// seeing it. Assert nothing instead.
				//
				// Reaching here needs the control plane to answer NOT gapped
				// for a zero cursor, and its predicate is `cursor < firstSeq`
				// — so this fires when the SYNC stream retains nothing at all
				// (firstSeq is 0 too), or when a control host answers
				// not-gapped on some other basis. A non-empty stream sends a
				// zero cursor down the gapped/hydrate path instead.
				return 0, nil
			}
			return cursor + 1, nil
		}
		if gapped {
			m.logger.Info("edge/sync: retention gap detected, re-hydrating", "cursor", cursor)
		} else {
			// An operator marked this device for hydration
			// (loupe-flows-edge-depth-ux.md §3.2) — the platform cannot push
			// to a device it cannot see, so this is consumed here, on the
			// device's own next attach.
			m.logger.Info("edge/sync: operator-requested hydration pending, re-hydrating", "cursor", cursor)
		}
	} else {
		m.logger.Info("edge/sync: cold start, hydrating")
	}
	syncStartSeq, err := m.hydrate(ctx)
	if err != nil {
		return 0, err
	}
	if syncStartSeq == 0 {
		m.logger.Warn("edge/sync: hydrate returned no sync position, delivering the full retained subject")
		return 0, nil
	}
	return syncStartSeq + 1, nil
}

// gapped reports whether cursor (the last stream sequence this node applied)
// has fallen behind the SYNC stream's current retention window — i.e.
// retention pruned messages between cursor and the earliest still-retained
// message, so a plain durable resume would silently skip them — and whether
// an operator has separately marked this device for hydration
// (loupe-flows-edge-depth-ux.md §3.2). Answered by the identity-bound
// personal.syncgap control RPC (edge-syncgap-control-rpc-design.md): the Edge
// grant no longer speaks any $JS.API.STREAM.* verb, so the earliest-retained
// sequence is compared to the cursor server-side by the control host on its
// own full-grant read, which also carries the hydration-request bit as a
// hitchhiker on the same round trip. A transient control-plane
// unavailability (boot-order window, §7) is retried with bounded backoff; a
// persistent failure fails closed (never resume unverified).
func (m *Manager) gapped(ctx context.Context, cursor uint64) (gapped bool, hydrationRequested bool, err error) {
	var lastErr error
	backoff := syncGapRetryBaseBackoff
	for attempt := 1; attempt <= syncGapMaxAttempts; attempt++ {
		g, hr, err := m.syncGapOnce(ctx, cursor)
		if err == nil {
			return g, hr, nil
		}
		lastErr = err
		if attempt == syncGapMaxAttempts {
			break
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, false, ctx.Err()
		case <-timer.C:
		}
		backoff *= 2
	}
	return false, false, fmt.Errorf("after %d attempts: %w", syncGapMaxAttempts, lastErr)
}

// syncGapOnce issues one personal.syncgap control RPC and decodes its answer.
// The response handling is deliberately stricter than the sibling ops'
// nil-checks: gapped=false is this op's COMMON, legitimate answer, so an
// absent PersonalSyncGap result must be an error, never defaulted to false —
// nil→false is the silent-data-loss direction (a warm node that should
// re-hydrate would instead resume its durable and skip the pruned deltas
// forever, edge-syncgap-control-rpc-design.md §3.3). hydrationRequested has
// no such stakes — it is a bandwidth hint, not a correctness gate — so it
// defaults to false with the rest of the tuple on any error path.
func (m *Manager) syncGapOnce(ctx context.Context, cursor uint64) (gapped bool, hydrationRequested bool, err error) {
	resp, err := m.controlRequest(ctx, "syncgap", controlwire.ControlRequest{
		IdentityID: m.cfg.IdentityID,
		DeviceID:   m.cfg.DeviceID,
		Cursor:     cursor,
	})
	if err != nil {
		return false, false, err
	}
	if resp.Error != "" {
		return false, false, fmt.Errorf("%s", resp.Error)
	}
	if resp.PersonalSyncGap == nil {
		return false, false, fmt.Errorf("control plane returned no syncgap result")
	}
	return resp.PersonalSyncGap.Gapped, resp.PersonalSyncGap.HydrationRequested, nil
}

// hydrate registers the device's Interest Set, then runs the cold bulk
// projection via the "personal.hydrate" control RPC (§3.3). The bulk deltas
// and terminal hydrationComplete marker it publishes land on the same
// per-actor subject the caller's durable consumer reads next, so no local
// state beyond the registration/hydrate acknowledgement needs recording here
// — handle() advances the cursor as those messages arrive.
//
// It returns the SYNC stream sequence the control plane captured immediately
// before publishing the burst. The burst is a projection of the whole world as
// of that sequence, so everything at or below it is already accounted for and
// a caller attaching a feed starts one past it. Zero means the control host
// could not name a position (an older control plane, or a failed read); the
// caller then asserts none and receives the subject's full retained history,
// which is what shipped before the position existed.
//
// The per-actor subject retains every delta ever published to it, including
// every prior hydrate cycle's own terminal marker. A caller that discards the
// returned position — or one attaching to a control plane that named none —
// replays them all before reaching the fresh burst this call just triggered,
// so armHydrateGate records the revision this call targets and handle() can
// tell that fresh marker apart from a stale one rather than releasing the
// caller's boot gate early.
func (m *Manager) hydrate(ctx context.Context) (syncStartSeq uint64, err error) {
	if err := m.registerInterest(ctx); err != nil {
		return 0, fmt.Errorf("personal.register: %w", err)
	}
	revision, lenses, syncStartSeq, _, err := m.callHydrate(ctx)
	if err != nil {
		return 0, fmt.Errorf("personal.hydrate: %w", err)
	}
	m.armHydrateGate(revision)
	// A lens dropped from the DDL, or re-minted under a new rule ID, has no
	// emitter left to heal its stranded attributions (personal-lens-
	// retraction-design.md §3.4) — a completed hydrate is what closes that
	// gap. Best-effort: a failure here does not undo the hydrate the control
	// plane already confirmed.
	pruned, pruneErr := m.store.PruneDeadLensAttributions(lenses)
	if pruneErr != nil {
		m.logger.Error("edge/sync: prune dead lens attributions failed", "err", pruneErr)
	} else if m.cfg.OnChange != nil {
		for _, k := range pruned {
			m.cfg.OnChange(k, true)
		}
	}
	return syncStartSeq, nil
}

// armHydrateGate records the revision the personal.hydrate RPC just
// targeted, so hydrationGateReady can hold OnHydrationComplete off any
// hydrationComplete marker replayed from before that point.
func (m *Manager) armHydrateGate(target uint64) {
	m.gateMu.Lock()
	m.hydrateTarget = target
	m.hydrateArmed = true
	m.gateMu.Unlock()
}

// hydrationGateReady reports whether a hydrationComplete marker at revision
// satisfies the currently armed gate, disarming it if so, and the target it
// was checked against (for logging — reading m.hydrateTarget outside gateMu
// would race armHydrateGate). No gate armed — a warm-resume live tail never
// called hydrate(), or this Run's gate already cleared — always reports
// ready, matching handle()'s pre-gate behavior.
func (m *Manager) hydrationGateReady(revision uint64) (ready bool, target uint64) {
	m.gateMu.Lock()
	defer m.gateMu.Unlock()
	if !m.hydrateArmed {
		return true, 0
	}
	if revision < m.hydrateTarget {
		return false, m.hydrateTarget
	}
	m.hydrateArmed = false
	return true, m.hydrateTarget
}

func (m *Manager) registerInterest(ctx context.Context) error {
	resp, err := m.controlRequest(ctx, "register", controlwire.ControlRequest{
		IdentityID: m.cfg.IdentityID,
		DeviceID:   m.cfg.DeviceID,
		Types:      m.cfg.Types,
		Anchors:    m.cfg.Anchors,
	})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	if resp.PersonalRegister == nil || !resp.PersonalRegister.Registered {
		return fmt.Errorf("control plane did not confirm registration")
	}
	return nil
}

func (m *Manager) callHydrate(ctx context.Context) (revision uint64, lenses []string, syncStartSeq uint64, syncEndSeq uint64, err error) {
	resp, err := m.controlRequest(ctx, "hydrate", controlwire.ControlRequest{
		IdentityID: m.cfg.IdentityID,
		DeviceID:   m.cfg.DeviceID,
	})
	if err != nil {
		return 0, nil, 0, 0, err
	}
	if resp.Error != "" {
		return 0, nil, 0, 0, fmt.Errorf("%s", resp.Error)
	}
	if resp.PersonalHydrate == nil || !resp.PersonalHydrate.Hydrated {
		return 0, nil, 0, 0, fmt.Errorf("control plane did not confirm hydration")
	}
	return resp.PersonalHydrate.Revision, resp.PersonalHydrate.Lenses, resp.PersonalHydrate.SyncStartSeq, resp.PersonalHydrate.SyncEndSeq, nil
}

// controlRequest issues one request-reply against the "personal" pseudo-lens
// op, carrying cfg.ActorHeader as the actor the control plane authorizes.
func (m *Manager) controlRequest(ctx context.Context, op string, body controlwire.ControlRequest) (controlwire.ControlResponse, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return controlwire.ControlResponse{}, fmt.Errorf("marshal %s request: %w", op, err)
	}
	reply, err := m.tr.Request(ctx, controlwire.ControlSubject("personal", op), data, m.cfg.ActorHeader)
	if err != nil {
		return controlwire.ControlResponse{}, fmt.Errorf("%s request: %w", op, err)
	}
	var resp controlwire.ControlResponse
	if err := json.Unmarshal(reply, &resp); err != nil {
		return controlwire.ControlResponse{}, fmt.Errorf("decode %s response: %w", op, err)
	}
	return resp, nil
}

// handle applies one delivered delta to the Local VAL Store and advances the
// persisted cursor. Must be idempotent (transport.Handler contract): a
// redelivered delta re-applies harmlessly under last-writer-wins-by-revision.
//
// The persisted cursor is a contiguous FLOOR, not the sequence that happens to
// have just succeeded: deliveryFloor holds it at or below every sequence still
// awaiting redelivery. Both dispositions that end a message's life advance it —
// an Ack because the delta is applied, a Term because the disposal is permanent
// and the server will never offer it again, so leaving it behind the floor
// would resurrect the same poison frame on every boot. A Nak holds the floor,
// because a Nak is a request for the message to come back.
func (m *Manager) handle(_ context.Context, d transport.Delta) transport.Decision {
	decision := m.apply(d)
	if decision == transport.Nak {
		m.floor.hold(d.Sequence)
		return decision
	}
	floor, advanced := m.floor.release(d.Sequence)
	if !advanced {
		return decision
	}
	if err := m.store.SetCursor(floor); err != nil {
		m.logger.Error("edge/sync: persist cursor failed", "floor", floor, "err", err)
		if decision == transport.Term {
			// The message is already gone server-side, so asking for it again
			// would only spin: this consumer sets no delivery bound, and Term
			// exists here precisely to escape a hot loop. The floor stays
			// released and the next resolve carries the write forward.
			return transport.Term
		}
		// An applied delta whose position could not be recorded must be asked
		// for again, and must go back to holding the floor until it is.
		m.floor.hold(d.Sequence)
		return transport.Nak
	}
	m.floor.commit(floor)
	return decision
}

// apply runs one delivered delta against the Local VAL Store and returns the
// disposition alone: Ack for a message the node is finished with, Term for one
// it will never be able to process, Nak for one worth redelivering. The cursor
// is handle()'s business, not this function's.
func (m *Manager) apply(d transport.Delta) transport.Decision {
	var env deltaEnvelope
	if err := json.Unmarshal(d.Body, &env); err != nil {
		// A malformed envelope will never parse differently on redelivery —
		// terminate rather than hot-loop.
		m.logger.Error("edge/sync: malformed delta envelope, dropping", "subject", d.Subject, "err", err)
		return transport.Term
	}
	switch env.Op {
	case "upsert":
		applied, err := m.store.ApplyUpsert(env.Key, env.Lens, env.Revision, env.Data)
		if err != nil {
			if decision, terminal := m.classifyApplyError("upsert", env.Key, err); terminal {
				return decision
			}
			m.logger.Error("edge/sync: apply upsert failed", "key", env.Key, "err", err)
			return transport.Nak
		}
		if applied && m.cfg.OnChange != nil {
			m.cfg.OnChange(env.Key, false)
		}
	case "delete":
		applied, err := m.store.ApplyDelete(env.Key, env.Revision)
		if err != nil {
			if decision, terminal := m.classifyApplyError("delete", env.Key, err); terminal {
				return decision
			}
			m.logger.Error("edge/sync: apply delete failed", "key", env.Key, "err", err)
			return transport.Nak
		}
		if applied && m.cfg.OnChange != nil {
			m.cfg.OnChange(env.Key, true)
		}
	case "keyset":
		if env.Lens == "" {
			m.logger.Warn("edge/sync: keyset envelope missing lens, ignoring", "subject", d.Subject)
			break
		}
		pruned, _, err := m.store.ApplyKeySet(env.Lens, env.Revision, env.Keys)
		if err != nil {
			m.logger.Error("edge/sync: apply keyset failed", "lens", env.Lens, "err", err)
			return transport.Nak
		}
		if m.cfg.OnChange != nil {
			for _, key := range pruned {
				m.cfg.OnChange(key, true)
			}
		}
	case "hydrationComplete":
		if ready, target := m.hydrationGateReady(env.Revision); !ready {
			m.logger.Info("edge/sync: stale hydrationComplete marker replayed, ignoring", "revision", env.Revision, "target", target)
			break
		}
		m.logger.Info("edge/sync: hydration complete", "revision", env.Revision)
		if m.cfg.OnHydrationComplete != nil {
			m.cfg.OnHydrationComplete(env.Revision)
		}
	default:
		m.logger.Warn("edge/sync: unknown delta op, cursor still advanced", "op", env.Op)
	}
	return transport.Ack
}

// classifyApplyError decides whether an ApplyUpsert/ApplyDelete error is
// deterministic (terminal=true) — the store will reject this key identically on
// every redelivery — or transient. An unstorable key (store.ErrUnstorableKey:
// a lens `ns` typo, or a future non-manifest personal lens) is terminal and
// must Term rather than Nak into an infinite redelivery hot-loop, exactly as
// handle() Terms a malformed envelope for the same reason. Any other apply
// error is treated as transient (terminal=false) and left to the caller to Nak.
func (m *Manager) classifyApplyError(op, key string, err error) (transport.Decision, bool) {
	if errors.Is(err, store.ErrUnstorableKey) {
		m.logger.Error("edge/sync: unstorable delta key, dropping", "op", op, "key", key, "err", err)
		return transport.Term, true
	}
	return transport.Nak, false
}
