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
// Fire 3 scope ends at ShredKey (destroy the key + evict the cache — one
// atomic call in the local backend). Row nullification of already-projected
// ciphertext rows, the privacy-critical failure tier, health counters, and
// the Weaver shred-finalization convergence marker (the crash-after-commit-
// before-destroy guarantee) are Fire 4.
package privacyworker

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/vault"
)

const (
	// KeyShreddedFilterSubject is the core-events subject this worker consumes.
	KeyShreddedFilterSubject = "events.privacy.keyShredded"
	// DefaultDurable is the privacy-worker's durable consumer name.
	DefaultDurable = "privacy-worker"

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
		// backend (local or a future KMS) confirms destruction. Fire 4's
		// Weaver convergence marker is the crash-survival backstop; this
		// redelivery loop is the in-process one.
		m.cfg.Logger.Warn("privacy-worker: ShredKey failed; retrying",
			"identityKey", ev.Payload.IdentityKey, "error", err)
		return substrate.NakWithDelay
	}
	m.cfg.Logger.Info("privacy-worker: identity key shredded", "identityKey", ev.Payload.IdentityKey)
	return substrate.Ack
}
