package chronicler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/substrate"
)

const defaultRedeliveryDelay = 5 * time.Second

// ManagerConfig configures a Manager — one durable consumer per eventStream
// lens definition.
type ManagerConfig struct {
	Conn         *substrate.Conn
	EventsStream string // the JetStream stream backing core-events
	Subject      string // the definition's single source.subjects entry
	Durable      string // durable consumer name, "chronicler-"+lensID by convention
	KeyField     string // the target adapter's sole key column
	Project      *EventProjection
	// Adapter must also implement adapter.RowReader — v1 targets nats_kv only
	// (translateDefinition enforces this at load time), and NatsKVAdapter
	// implements both.
	Adapter adapter.Adapter
	Logger  *slog.Logger
}

// Manager runs one eventStream lens's durable consumer: decode each
// core-events delivery into the canonical Event envelope, apply the
// definition's declarative EventProjection, merge the result onto the
// previously stored row, and upsert through the guarded adapter using the
// event's own backing-stream sequence as the monotonic ordering token
// (design §2.4).
type Manager struct {
	cfg    ManagerConfig
	reader adapter.RowReader
}

// NewManager validates cfg and constructs a Manager. Requires cfg.Adapter to
// also implement adapter.RowReader (carry-forward merge needs to read the row
// a lifecycle event doesn't fully restate).
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.Conn == nil {
		return nil, fmt.Errorf("chronicler: Conn required")
	}
	if cfg.EventsStream == "" {
		return nil, fmt.Errorf("chronicler: EventsStream required")
	}
	if cfg.Subject == "" {
		return nil, fmt.Errorf("chronicler: Subject required")
	}
	if cfg.Durable == "" {
		return nil, fmt.Errorf("chronicler: Durable required")
	}
	if cfg.KeyField == "" {
		return nil, fmt.Errorf("chronicler: KeyField required")
	}
	if cfg.Project == nil {
		return nil, fmt.Errorf("chronicler: Project required")
	}
	if cfg.Adapter == nil {
		return nil, fmt.Errorf("chronicler: Adapter required")
	}
	reader, ok := cfg.Adapter.(adapter.RowReader)
	if !ok {
		return nil, fmt.Errorf("chronicler: Adapter %T must implement adapter.RowReader", cfg.Adapter)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Manager{cfg: cfg, reader: reader}, nil
}

// Run drives the durable consumer, blocking until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	return m.cfg.Conn.RunDurableConsumer(ctx, substrate.DurableConsumerConfig{
		Stream:          m.cfg.EventsStream,
		FilterSubject:   m.cfg.Subject,
		Durable:         m.cfg.Durable,
		RedeliveryDelay: defaultRedeliveryDelay,
		Logger:          m.cfg.Logger,
	}, m.handle)
}

// handle projects one event, merges it onto the previously stored row (carry-
// forward for every column this event didn't touch — see ProjectEvent), and
// writes the merged row through the guarded adapter with the event's own
// backing-stream sequence as the ordering token.
//
// The read-then-write here is NOT transactional — it is safe only because a
// single durable JetStream pull-consumer processes one message at a time
// (invariant: no concurrent writer of the same definition's rows). The
// adapter's own guarded CAS loop is a second, independent line of defense
// against a stale write actually landing (a lower-seq write is rejected even
// if this read happened to race one).
func (m *Manager) handle(ctx context.Context, msg substrate.Message) substrate.Decision {
	if len(msg.Body) == 0 {
		return substrate.Ack
	}
	if msg.Sequence == 0 {
		// A guarded Upsert fail-closed-drops a seq-0 write as "no ordering
		// token" and returns nil — which would otherwise Ack here with
		// NOTHING written. Harmless for a coreKV lens (Core-KV stays the
		// source of truth; a rebuild re-derives the row), but an eventStream
		// lens's event IS the only copy of its contribution (bounded by
		// core-events' 7-day retention) — silently Acking a seq-0 delivery
		// would permanently lose it. Sequence 0 only happens when
		// msg.Metadata() itself failed, which is a transient JetStream-client
		// condition, so retry rather than accept the loss.
		m.cfg.Logger.Warn("chronicler: message carries no stream sequence (metadata read failure); retrying",
			"subject", msg.Subject)
		return substrate.NakWithDelay
	}
	var ev Event
	if err := json.Unmarshal(msg.Body, &ev); err != nil {
		m.cfg.Logger.Error("chronicler: unparseable event; dropping", "subject", msg.Subject, "error", err)
		return substrate.Term
	}
	key, partial, err := ProjectEvent(m.cfg.Project, ev, msg.Sequence)
	if err != nil {
		m.cfg.Logger.Error("chronicler: projection failed; dropping poison event",
			"subject", msg.Subject, "eventType", ev.EventType, "error", err)
		return substrate.Term
	}
	keys := map[string]any{m.cfg.KeyField: key}
	existing, _, err := m.reader.GetRow(ctx, keys)
	if err != nil {
		m.cfg.Logger.Warn("chronicler: read existing row failed; retrying", "key", key, "error", err)
		return substrate.NakWithDelay
	}
	merged := make(map[string]any, len(existing)+len(partial))
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range partial {
		merged[k] = v
	}
	if err := m.cfg.Adapter.Upsert(ctx, keys, merged, msg.Sequence); err != nil {
		m.cfg.Logger.Warn("chronicler: upsert failed; retrying", "key", key, "error", err)
		return substrate.NakWithDelay
	}
	return substrate.Ack
}
