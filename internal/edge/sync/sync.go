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
	"fmt"
	"log/slog"

	"github.com/asolgan/lattice/internal/edge/store"
	"github.com/asolgan/lattice/internal/edge/transport"
	"github.com/asolgan/lattice/internal/refractor/control/controlwire"
	"github.com/asolgan/lattice/internal/refractor/subjects"
)

const (
	defaultStream        = "SYNC"
	defaultSubjectPrefix = "lattice.sync.user"
)

// deltaEnvelope mirrors the wire shape a Personal Lens delta publishes to
// lattice.sync.user.<actor> (internal/refractor/adapter/natssubject.go's
// unexported deltaEnvelope; docs/components/refractor.md). Deliberately
// re-declared rather than imported: internal/refractor/adapter is a
// Refractor-internal package whose deltaEnvelope type is unexported, and the
// Edge is a separate application consuming only the documented wire contract.
type deltaEnvelope struct {
	Op            string          `json:"op"` // "upsert" | "delete" | "hydrationComplete"
	Key           string          `json:"key,omitempty"`
	Revision      uint64          `json:"revision"`
	ProjectionSeq uint64          `json:"projectionSeq"`
	Data          json.RawMessage `json:"data,omitempty"`
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
		stream = defaultStream
	}
	prefix := cfg.SubjectPrefix
	if prefix == "" {
		prefix = defaultSubjectPrefix
	}
	return &Manager{
		tr:     tr,
		store:  st,
		cfg:    cfg,
		stream: stream,
		prefix: prefix,
		// Stable per-device name (unlike Loom's per-boot-nonce pattern):
		// the Sync Manager wants JetStream's native ack-floor resume across
		// restarts, not full replay every boot.
		durable: "edge-sync-" + cfg.IdentityID + "-" + cfg.DeviceID,
		logger:  logger,
	}, nil
}

// Run drives the Sync Manager until ctx is cancelled. On cold start (no
// local cursor yet) or a detected gap (the local cursor has fallen behind
// the SYNC stream's retention window), it hydrates via the Personal-Lens
// control RPCs (§3.3) before subscribing; otherwise it subscribes directly,
// resuming the durable consumer from its own persisted ack floor (§3.2's
// "brief disconnect" case). Blocks until ctx is done; returns the durable
// consumer's terminal error, if any.
func (m *Manager) Run(ctx context.Context) error {
	if err := m.ensureFresh(ctx); err != nil {
		return fmt.Errorf("edge/sync: ensure fresh: %w", err)
	}
	return m.tr.RunDurableConsumer(ctx, transport.ConsumerConfig{
		Stream:        m.stream,
		FilterSubject: subjects.PersonalSync(m.prefix, m.cfg.IdentityID),
		Durable:       m.durable,
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
func (m *Manager) Rehydrate(ctx context.Context) error {
	return m.hydrate(ctx)
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
// (§3.2/§3.3). A warm cursor still within retention is a no-op: the
// subsequent durable consumer resumes incrementally on its own.
func (m *Manager) ensureFresh(ctx context.Context) error {
	cursor, ok, err := m.store.Cursor()
	if err != nil {
		return fmt.Errorf("read cursor: %w", err)
	}
	if ok {
		gapped, err := m.gapped(ctx, cursor)
		if err != nil {
			return fmt.Errorf("check gap: %w", err)
		}
		if !gapped {
			return nil
		}
		m.logger.Info("edge/sync: retention gap detected, re-hydrating", "cursor", cursor)
	} else {
		m.logger.Info("edge/sync: cold start, hydrating")
	}
	return m.hydrate(ctx)
}

// gapped reports whether cursor (the last stream sequence this node applied)
// has fallen behind the SYNC stream's current FirstSeq — i.e. retention has
// pruned messages between cursor and the earliest still-retained message, so
// a plain durable resume would silently skip them.
func (m *Manager) gapped(ctx context.Context, cursor uint64) (bool, error) {
	first, err := m.tr.FirstSequence(ctx, m.stream)
	if err != nil {
		return false, err
	}
	return cursor < first, nil
}

// hydrate registers the device's Interest Set, then runs the cold bulk
// projection via the "personal.hydrate" control RPC (§3.3). The bulk deltas
// and terminal hydrationComplete marker it publishes land on the same
// per-actor subject the caller's durable consumer reads next, so no local
// state beyond the registration/hydrate acknowledgement needs recording here
// — handle() advances the cursor as those messages arrive.
func (m *Manager) hydrate(ctx context.Context) error {
	if err := m.registerInterest(ctx); err != nil {
		return fmt.Errorf("personal.register: %w", err)
	}
	if _, err := m.callHydrate(ctx); err != nil {
		return fmt.Errorf("personal.hydrate: %w", err)
	}
	return nil
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

func (m *Manager) callHydrate(ctx context.Context) (revision uint64, err error) {
	resp, err := m.controlRequest(ctx, "hydrate", controlwire.ControlRequest{
		IdentityID: m.cfg.IdentityID,
		DeviceID:   m.cfg.DeviceID,
	})
	if err != nil {
		return 0, err
	}
	if resp.Error != "" {
		return 0, fmt.Errorf("%s", resp.Error)
	}
	if resp.PersonalHydrate == nil || !resp.PersonalHydrate.Hydrated {
		return 0, fmt.Errorf("control plane did not confirm hydration")
	}
	return resp.PersonalHydrate.Revision, nil
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
func (m *Manager) handle(_ context.Context, d transport.Delta) transport.Decision {
	var env deltaEnvelope
	if err := json.Unmarshal(d.Body, &env); err != nil {
		// A malformed envelope will never parse differently on redelivery —
		// terminate rather than hot-loop.
		m.logger.Error("edge/sync: malformed delta envelope, dropping", "subject", d.Subject, "err", err)
		return transport.Term
	}
	switch env.Op {
	case "upsert":
		applied, err := m.store.ApplyUpsert(env.Key, env.Revision, env.Data)
		if err != nil {
			m.logger.Error("edge/sync: apply upsert failed", "key", env.Key, "err", err)
			return transport.Nak
		}
		if applied && m.cfg.OnChange != nil {
			m.cfg.OnChange(env.Key, false)
		}
	case "delete":
		applied, err := m.store.ApplyDelete(env.Key, env.Revision)
		if err != nil {
			m.logger.Error("edge/sync: apply delete failed", "key", env.Key, "err", err)
			return transport.Nak
		}
		if applied && m.cfg.OnChange != nil {
			m.cfg.OnChange(env.Key, true)
		}
	case "hydrationComplete":
		m.logger.Info("edge/sync: hydration complete", "revision", env.Revision)
		if m.cfg.OnHydrationComplete != nil {
			m.cfg.OnHydrationComplete(env.Revision)
		}
	default:
		m.logger.Warn("edge/sync: unknown delta op, cursor still advanced", "op", env.Op)
	}
	if err := m.store.SetCursor(d.Sequence); err != nil {
		m.logger.Error("edge/sync: persist cursor failed", "err", err)
		return transport.Nak
	}
	return transport.Ack
}
