// Package privacyworker runs the asynchronous half of crypto-shredding
// (vault-crypto-shredding-design.md §2.4, Fire 3): durable consumers that call
// Vault.ShredKey(holderKey) — the irreversible key destruction the shred
// operations only record INTENT for on their synchronous commit path
// (packages/privacy-base's shredIdentityKey / shredRetentionClassKey DDLs mark
// piiKey.shredded=true and emit an event; neither touches the Vault itself, so
// a KMS round-trip can never block or fail an operation commit).
//
// There is one consumer per HOLDER KIND, because there is one destruction verb
// per holder kind (retention-class-key-custody-design.md §4.3):
// events.privacy.keyShredded destroys a person's key on their request, and
// events.privacy.retentionClassKeyShredded destroys a retention class's key
// when the controller's obligation expires. The Vault call is identical for
// both — the backend has never cared what kind of vertex a holder key names —
// so what differs is only which finalization verb records the result, and the
// separate durables that keep one kind's failures from parking the other's.
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
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

const (
	// KeyShreddedFilterSubject is the core-events subject this worker consumes.
	KeyShreddedFilterSubject = "events.privacy.keyShredded"
	// RetentionClassKeyShreddedFilterSubject is the retention-class half's
	// core-events subject (retention-class-key-custody-design.md §4.3).
	RetentionClassKeyShreddedFilterSubject = "events.privacy.retentionClassKeyShredded"
	// DefaultDurable is the privacy-worker's durable consumer name.
	DefaultDurable = "privacy-worker"
	// DefaultRetentionClassDurable is the durable name of the retention-class
	// consumer. A SEPARATE durable, not a widened filter on the one above: two
	// durables advance two independent cursors, so a stuck retention-class
	// destruction cannot hold up an identity erasure (or the reverse), and
	// either can be reset alone.
	DefaultRetentionClassDurable = "privacy-worker-retention"
	// DefaultOpLane is the ops.<lane> RecordShredFinalization is submitted on
	// (matches Weaver's + the objectmanager cascade's default; the Processor
	// consumes ops.system).
	DefaultOpLane = "system"

	// StepVaultKeyDestroyed is the finalization step this worker records on
	// either holder kind (packages/privacy-base's shredIdentityKey and
	// shredRetentionClassKey DDLs both name it).
	StepVaultKeyDestroyed = "vaultKeyDestroyed"

	// opRecordShredFinalization / opRecordRetentionClassShredFinalization are
	// the finalization verbs of the two holder kinds. They are distinct ops
	// because each validates its own subject's vertex type and carries its own
	// step vocabulary — the identity plane finalizes a row-nullify, the class
	// plane a lens rebuild.
	// op-name: (submits) the worker submits this after destroying an identity's Vault key, recording the vaultKeyDestroyed step
	opRecordShredFinalization = "RecordShredFinalization"
	// op-name: (submits) the worker submits this after destroying a retention class's Vault key, recording the vaultKeyDestroyed step
	opRecordRetentionClassShredFinalization = "RecordRetentionClassShredFinalization"

	defaultRedeliveryDelay = 5 * time.Second

	// consumerRetryDelay paces a consumer's re-creation after a startup
	// failure. Matched to defaultRedeliveryDelay: both are "a transient
	// JetStream condition, come back shortly" waits.
	consumerRetryDelay = 5 * time.Second
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

	// RetentionClassDurable is the durable name of the retention-class
	// key-destruction consumer. Defaults to DefaultRetentionClassDurable.
	RetentionClassDurable string

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
	if cfg.RetentionClassDurable == "" {
		cfg.RetentionClassDurable = DefaultRetentionClassDurable
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

// Run drives BOTH durable key-destruction consumers — one per holder kind —
// blocking until ctx is cancelled.
//
// Two consumers rather than one widened filter, because the two destructions
// are independent events with independent failure modes: a retention-class
// destruction that keeps failing its Vault call must not park an identity's
// right-to-erasure behind it on a shared cursor.
//
// That independence has to hold at STARTUP too, which is what supervise gives
// each one. RunDurableConsumer returns non-nil only when it cannot create its
// consumer (internal/substrate/consumer.go) — a transient JetStream condition
// — and returns nil once ctx is done. So a shared "first error wins, cancel the
// sibling" shape would let one kind's momentary creation failure tear down the
// OTHER kind's healthy durable, which for the identity kind means silently
// dropping a person's right-to-erasure destruction. Each consumer instead
// retries its own creation, loudly, until ctx ends: a shred must never be
// silently dropped, and neither kind may take the other down with it.
func (m *Manager) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	supervise := func(subject, durable string, handler func(context.Context, substrate.Message) substrate.Decision) {
		defer wg.Done()
		for ctx.Err() == nil {
			err := m.cfg.Conn.RunDurableConsumer(ctx, substrate.DurableConsumerConfig{
				Stream:          m.cfg.EventsStream,
				FilterSubject:   subject,
				Durable:         durable,
				RedeliveryDelay: defaultRedeliveryDelay,
				Logger:          m.cfg.Logger,
			}, handler)
			if err == nil {
				return // ctx done — the only way the consumer loop exits cleanly.
			}
			m.cfg.Logger.Error("privacy-worker: durable consumer could not start; retrying",
				"durable", durable, "subject", subject, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(consumerRetryDelay):
			}
		}
	}
	wg.Add(2)
	go supervise(KeyShreddedFilterSubject, m.cfg.Durable, m.handleKeyShredded)
	go supervise(RetentionClassKeyShreddedFilterSubject, m.cfg.RetentionClassDurable, m.handleRetentionClassKeyShredded)
	wg.Wait()
	return ctx.Err()
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
		if err := m.submitFinalization(ctx, finalization{
			OperationType: opRecordShredFinalization,
			SubjectField:  "identityKey",
			RequestPrefix: "shredfin:",
			HolderKey:     ev.Payload.IdentityKey,
			Sequence:      msg.Sequence,
		}); err != nil {
			m.cfg.Logger.Warn("privacy-worker: RecordShredFinalization submit failed; retrying whole event",
				"identityKey", ev.Payload.IdentityKey, "error", err)
			return substrate.NakWithDelay
		}
	}
	m.cfg.Logger.Info("privacy-worker: identity key shredded", "identityKey", ev.Payload.IdentityKey)
	return substrate.Ack
}

// retentionClassKeyShreddedEvent is the minimal view of a
// privacy.retentionClassKeyShredded core-events message.
type retentionClassKeyShreddedEvent struct {
	Payload struct {
		RetentionClassKey string `json:"retentionClassKey"`
	} `json:"payload"`
}

// handleRetentionClassKeyShredded destroys a retention class's Vault key —
// the erase-on-expiry half of custody. Identical in shape and disposition to
// handleKeyShredded (ShredKey is idempotent, so a redelivery re-runs safely),
// and identical in consequence: after it returns, every record custodied on
// this class is unrecoverable, whether or not its subject was ever erased.
//
// The Vault call is the SAME Vault.ShredKey with the SAME semantics — the
// backend has never cared what kind of vertex a holder key names, which is
// exactly what made class custody a subtraction on the read path rather than a
// second crypto plane.
func (m *Manager) handleRetentionClassKeyShredded(ctx context.Context, msg substrate.Message) substrate.Decision {
	if len(msg.Body) == 0 {
		return substrate.Ack
	}
	var ev retentionClassKeyShreddedEvent
	if err := json.Unmarshal(msg.Body, &ev); err != nil {
		m.cfg.Logger.Warn("privacy-worker: unparseable privacy.retentionClassKeyShredded event; dropping", "error", err)
		return substrate.Term
	}
	if ev.Payload.RetentionClassKey == "" {
		m.cfg.Logger.Warn("privacy-worker: privacy.retentionClassKeyShredded missing retentionClassKey; dropping")
		return substrate.Term
	}
	if err := m.cfg.Vault.ShredKey(ctx, ev.Payload.RetentionClassKey); err != nil {
		m.cfg.Logger.Warn("privacy-worker: retention-class ShredKey failed; retrying",
			"retentionClassKey", ev.Payload.RetentionClassKey, "error", err)
		return substrate.NakWithDelay
	}
	if m.cfg.ActorKey != "" {
		if err := m.submitFinalization(ctx, finalization{
			OperationType: opRecordRetentionClassShredFinalization,
			SubjectField:  "retentionClassKey",
			RequestPrefix: "rcshredfin:",
			HolderKey:     ev.Payload.RetentionClassKey,
			Sequence:      msg.Sequence,
		}); err != nil {
			m.cfg.Logger.Warn("privacy-worker: RecordRetentionClassShredFinalization submit failed; retrying whole event",
				"retentionClassKey", ev.Payload.RetentionClassKey, "error", err)
			return substrate.NakWithDelay
		}
	}
	m.cfg.Logger.Info("privacy-worker: retention-class key shredded", "retentionClassKey", ev.Payload.RetentionClassKey)
	return substrate.Ack
}

// finalizationOpEnvelope is the Contract #2 §2.1 op wire format the worker
// publishes to ops.<lane> — the same shape internal/processor reads; the
// worker carries its own copy to keep the module boundary clean (the
// weaver/objectmanager idiom — substrate-only, no internal/processor import).
type finalizationOpEnvelope struct {
	RequestID     string                   `json:"requestId"`
	Lane          string                   `json:"lane"`
	OperationType string                   `json:"operationType"`
	Actor         string                   `json:"actor"`
	SubmittedAt   string                   `json:"submittedAt"`
	Payload       json.RawMessage          `json:"payload"`
	ContextHint   *finalizationContextHint `json:"contextHint,omitempty"`
}

type finalizationContextHint struct {
	Reads []string `json:"reads,omitempty"`
}

// finalization names the one holder kind's worth of variation in the
// otherwise-identical finalization submit: which verb, what the verb calls its
// subject, and the requestId namespace. Everything else — the lane, the actor,
// the declared read, the step — is the same for both kinds, which is the point:
// the two planes differ in vocabulary, not in mechanism.
type finalization struct {
	OperationType string
	SubjectField  string
	RequestPrefix string
	HolderKey     string
	Sequence      uint64
}

// submitFinalization publishes one Record*ShredFinalization{vaultKeyDestroyed}
// op for the given holder kind. ContextHint.Reads declares the piiKey aspect so
// the record is hydrated + OCC-conditioned (the sibling projection-side record
// can race this one on the system lane's concurrent workers; conditioning turns
// a would-be lost-update into a transparent commit-path retry). Class-less
// (the Processor's operationType→class reverse index resolves each verb to its
// owning DDL). The requestId is keyed on the triggering event's backing-stream
// sequence: a redelivery of the SAME event derives the same id (tracker
// collapse); a genuinely new shred of the same holder is a new event → a new id
// → a fresh (idempotent-by-value) record. The two kinds carry DIFFERENT
// requestId prefixes so neither can ever collapse onto the other's tracker
// entry. A prefix is part of the derivation, so it is frozen per kind: an
// in-flight event redelivered across a deploy must derive the same id on both
// sides of it, or the tracker sees a duplicate rather than a retry.
//
// ContextHint.Reads also declares the ACTOR's own vertex: the script pins these
// attestations to the identity.system.privacy service actor by reading
// state[op.actor].class, and an undeclared actor fails the op closed. So the
// declaration is a correctness requirement of the submit, not an optimization.
func (m *Manager) submitFinalization(ctx context.Context, f finalization) error {
	payload, err := json.Marshal(map[string]any{
		f.SubjectField: f.HolderKey,
		"step":         StepVaultKeyDestroyed,
	})
	if err != nil {
		return fmt.Errorf("privacyworker: marshal payload: %w", err)
	}
	env := finalizationOpEnvelope{
		RequestID: substrate.DeriveNanoID(f.RequestPrefix+StepVaultKeyDestroyed+":",
			f.HolderKey+"\x00"+strconv.FormatUint(f.Sequence, 10)),
		Lane:          m.cfg.OpLane,
		OperationType: f.OperationType,
		Actor:         m.cfg.ActorKey,
		SubmittedAt:   substrate.FormatTimestamp(time.Now()),
		Payload:       payload,
		ContextHint: &finalizationContextHint{
			Reads: []string{f.HolderKey + ".piiKey", m.cfg.ActorKey},
		},
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("privacyworker: marshal op envelope: %w", err)
	}
	return m.cfg.Conn.Publish(ctx, "ops."+m.cfg.OpLane, data, nil)
}
