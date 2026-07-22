// Package privacyworker runs the asynchronous half of crypto-shredding
// (vault-crypto-shredding-design.md §2.4, Fire 3): a durable consumer on
// events.privacy.keyShredded that calls Vault.ShredKey(identityKey) — the
// irreversible key destruction the ShredIdentityKey operation only records
// INTENT for on its synchronous commit path (packages/privacy-base's
// shredIdentityKey DDL marks piiKey.shredded=true and emits the event; it
// never touches the Vault itself, so a KMS round-trip can never block or fail
// an operation commit).
//
// Co-located in the SAME process as the Processor (wired from cmd/processor,
// not a separate binary — design §3's "fewer moving parts"), sharing the
// Processor's own *vault.LocalBackend instance. This placement is load-
// bearing, not just convenient: the local backend's shredded-set and DEK
// cache (internal/vault/local.go) are per-instance in-memory state — a
// SEPARATE Vault instance constructed from the same master KEK would NOT
// observe a shred recorded by this listener, since decrypt-on-read (step 4)
// and encrypt-on-write (step 6.5) both run against the Processor's instance.
// Refractor never needs a Vault at all (§2.3 — it projects ciphertext as-is),
// so hosting this listener there instead would mean wiring master-KEK access
// into a component the design deliberately keeps Vault-blind; the Processor
// already holds the Vault, so this is the minimal-surface-area placement.
//
// After a successful ShredKey the worker durably records the destruction by
// submitting RecordShredFinalization{step: vaultKeyDestroyed} under the
// identity.system.privacy service actor (Fire 4b) — the state the shredStatus
// lens projects so an operator can see in-flight/stuck shreds. The submit is
// one fire-and-forget publish to ops.<OpLane> with a deterministic requestId
// (the objectmanager cascade idiom), published BEFORE the event is Acked so a
// crash retries both halves; ShredKey is idempotent and the record op
// collapses on the Contract #4 tracker. An empty ActorKey disables recording
// (a pre-v15 kernel with no privacy actor) without disabling the shred.
package privacyworker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

const (
	// KeyShreddedFilterSubject is the core-events subject this worker consumes.
	KeyShreddedFilterSubject = "events.privacy.keyShredded"
	// DefaultDurable is the privacy-worker's durable consumer name.
	DefaultDurable = "privacy-worker"
	// DefaultOpLane is the ops.<lane> RecordShredFinalization is submitted on
	// (matches Weaver's + the objectmanager cascade's default; the Processor
	// consumes ops.system).
	DefaultOpLane = "system"

	// StepVaultKeyDestroyed is the RecordShredFinalization step this worker
	// records (packages/privacy-base shredIdentityKey DDL).
	StepVaultKeyDestroyed = "vaultKeyDestroyed"

	defaultRedeliveryDelay = 5 * time.Second
)

// Config configures the Manager. Conn / EventsStream are the substrate
// connection + core-events stream name (bootstrap.CoreEventsStreamName).
// Vault MUST be the same instance the Processor's commit path decrypts /
// encrypts through (see package doc) — a differently-constructed instance,
// even from the same master KEK, will not observe the shred.
type Config struct {
	Conn         *substrate.Conn
	EventsStream string
	Durable      string
	Vault        vault.Vault
	Logger       *slog.Logger

	// ActorKey is the identity.system.privacy service-actor vertex key the
	// worker submits RecordShredFinalization under (Fire 4b). Empty disables
	// the durable finalization record (with a startup warning) — the shred
	// itself still runs.
	ActorKey string
	// OpLane is the ops.<lane> for the RecordShredFinalization submit.
	// Defaults to DefaultOpLane.
	OpLane string
}

// Manager runs the keyShredded consumer.
type Manager struct {
	cfg Config
}

// New constructs a Manager, applying defaults for the omitted fields.
func New(cfg Config) *Manager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Durable == "" {
		cfg.Durable = DefaultDurable
	}
	if cfg.OpLane == "" {
		cfg.OpLane = DefaultOpLane
	}
	if cfg.ActorKey == "" {
		cfg.Logger.Warn("privacy-worker: no ActorKey configured; shred finalization recording disabled (shredStatus rows will stay in-flight)")
	}
	return &Manager{cfg: cfg}
}

// keyShreddedEvent is the minimal view of a privacy.keyShredded core-events
// message — the business data lives under payload (read-from-body discipline,
// mirroring internal/objectmanager's tombstonedEvent).
type keyShreddedEvent struct {
	Payload struct {
		IdentityKey string `json:"identityKey"`
	} `json:"payload"`
}

// Run drives the durable events.privacy.keyShredded consumer, blocking until
// ctx is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	return m.cfg.Conn.RunDurableConsumer(ctx, substrate.DurableConsumerConfig{
		Stream:          m.cfg.EventsStream,
		FilterSubject:   KeyShreddedFilterSubject,
		Durable:         m.cfg.Durable,
		RedeliveryDelay: defaultRedeliveryDelay,
		Logger:          m.cfg.Logger,
	}, m.handleKeyShredded)
}

// handleKeyShredded destroys the shredded identity's Vault key. Idempotent:
// vault.Vault.ShredKey is documented idempotent (shredding an already-
// shredded, or never-created, identity key is not an error), so a redelivery
// of the same event is safe to re-run in full.
func (m *Manager) handleKeyShredded(ctx context.Context, msg substrate.Message) substrate.Decision {
	if len(msg.Body) == 0 {
		return substrate.Ack
	}
	var ev keyShreddedEvent
	if err := json.Unmarshal(msg.Body, &ev); err != nil {
		m.cfg.Logger.Warn("privacy-worker: unparseable privacy.keyShredded event; dropping", "error", err)
		return substrate.Term
	}
	if ev.Payload.IdentityKey == "" {
		m.cfg.Logger.Warn("privacy-worker: privacy.keyShredded missing identityKey; dropping")
		return substrate.Term
	}
	if err := m.cfg.Vault.ShredKey(ctx, ev.Payload.IdentityKey); err != nil {
		// A shred must never be silently dropped — retry until the Vault
		// backend (local or a future KMS) confirms destruction. JetStream's
		// durable at-least-once redelivery is the crash-survival backstop;
		// this redelivery loop is the in-process one.
		m.cfg.Logger.Warn("privacy-worker: ShredKey failed; retrying",
			"identityKey", ev.Payload.IdentityKey, "error", err)
		return substrate.NakWithDelay
	}
	// Publish-then-ack (the cascade idiom): the durable finalization record is
	// submitted before the event is Acked, so a crash between ShredKey and the
	// submit redelivers the event — ShredKey re-runs idempotently and the
	// deterministic requestId collapses a duplicate record on the Contract #4
	// tracker.
	if m.cfg.ActorKey != "" {
		if err := m.submitFinalization(ctx, ev.Payload.IdentityKey, msg.Sequence); err != nil {
			m.cfg.Logger.Warn("privacy-worker: RecordShredFinalization submit failed; retrying whole event",
				"identityKey", ev.Payload.IdentityKey, "error", err)
			return substrate.NakWithDelay
		}
	}
	m.cfg.Logger.Info("privacy-worker: identity key shredded", "identityKey", ev.Payload.IdentityKey)
	return substrate.Ack
}

// finalizationOpEnvelope is the Contract #2 §2.1 op wire format the worker
// publishes to ops.<lane> — the same shape internal/processor reads; the
// worker carries its own copy to keep the module boundary clean (the
// weaver/objectmanager idiom — substrate-only, no internal/processor import).
type finalizationOpEnvelope struct {
	RequestID     string                  `json:"requestId"`
	Lane          string                  `json:"lane"`
	OperationType string                  `json:"operationType"`
	Actor         string                  `json:"actor"`
	SubmittedAt   string                  `json:"submittedAt"`
	Payload       json.RawMessage         `json:"payload"`
	ContextHint   *finalizationContextHint `json:"contextHint,omitempty"`
}

type finalizationContextHint struct {
	Reads []string `json:"reads,omitempty"`
}

// submitFinalization publishes one RecordShredFinalization{vaultKeyDestroyed}
// op. ContextHint.Reads declares the piiKey aspect so the record is
// hydrated + OCC-conditioned (the sibling projectionsNullified record can
// race this one on the system lane's concurrent workers; conditioning turns
// a would-be lost-update into a transparent commit-path retry). Class-less
// (the Processor's operationType→class reverse index resolves it to the
// shredIdentityKey DDL). The requestId is keyed on the triggering event's
// backing-stream sequence: a redelivery of the SAME event derives the same id
// (tracker collapse); a genuinely new shred of the same identity is a new
// event → a new id → a fresh (idempotent-by-value) record.
func (m *Manager) submitFinalization(ctx context.Context, identityKey string, seq uint64) error {
	payload, err := json.Marshal(map[string]any{
		"identityKey": identityKey,
		"step":        StepVaultKeyDestroyed,
	})
	if err != nil {
		return fmt.Errorf("privacyworker: marshal payload: %w", err)
	}
	env := finalizationOpEnvelope{
		RequestID: substrate.DeriveNanoID("shredfin:"+StepVaultKeyDestroyed+":",
			identityKey+"\x00"+strconv.FormatUint(seq, 10)),
		Lane:          m.cfg.OpLane,
		OperationType: "RecordShredFinalization",
		Actor:         m.cfg.ActorKey,
		SubmittedAt:   substrate.FormatTimestamp(time.Now()),
		Payload:       payload,
		ContextHint:   &finalizationContextHint{Reads: []string{identityKey + ".piiKey"}},
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("privacyworker: marshal op envelope: %w", err)
	}
	return m.cfg.Conn.Publish(ctx, "ops."+m.cfg.OpLane, data, nil)
}
