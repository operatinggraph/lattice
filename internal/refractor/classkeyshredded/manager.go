// Package classkeyshredded delivers a RETENTION-CLASS key destruction to the
// read models, by rebuilding every Secure Lens whose columns declare the
// destroyed holder's type (retention-class-key-custody-design.md §6.3).
//
// It exists because the identity kind's in-band scrub cannot be generalized.
// For custody kind `identity` the holder vertex IS the vertex hosting the
// ciphertext, so an identity-anchored sensitive aspect is reachable in cypher
// only through a node bound to that identity: either the lens binds
// (id:identity) and `identity` enters its referenced-label set, or it binds an
// unlabeled node and clears `exhaustive`. In both branches the lens reacts to
// the piiKey CDC event a shred commits, and the decryptor re-runs and projects
// null. That guarantee is derived from the KEY SHAPE, not from the current
// corpus — which is exactly what a class holder cannot have. A retention class
// is not the ciphertext's host, so nothing forces any lens to bind it:
// `vtx.retentionclass.<H>.piiKey` matches no subject in the consumer filter of
// a lens labelled {appointment, provider}, and the plain aspect arm Ack-drops
// it for the same reason. No sweep catches it either — a Secure Lens is a plain
// projection lens and never enrols one. The row would keep rendering the full
// plaintext note forever while the destruction reported success, which is
// strictly worse than un-projected plaintext because it reads as compliant from
// every surface.
//
// So delivery here is a scoped REBUILD rather than a delta. A rebuild re-runs
// the SecureDecryptor over every row through the single choke point every plain
// -lens evaluation flows through; a destroyed key yields ErrKeyShredded, which
// projects null. It needs no truncate and deletes nothing — the row is
// RETAINED, only its plaintext is gone, so upsert-with-null is the correct
// semantics and there is no TRUNCATE blast radius. Decisively, a rebuild
// re-delivers the last value of every key matching the lens's own filter, so it
// re-evaluates every row whether or not the holder type is in the lens's
// reprojection labels — it does not inherit the label-narrowing blindness that
// broke the CDC path, which is the property that makes it a guarantee rather
// than a hope. Its cost is bounded by rarity: a class-key destruction is an
// operator-scheduled, per-class event.
//
// Enumeration is by holder TYPE, not holder instance, so destroying one class's
// key rebuilds lenses that also carry other classes' rows. That over-rebuild is
// the fail-closed direction and the event is rare; narrowing it would require
// per-instance declaration and buys nothing.
package classkeyshredded

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

const (
	// FilterSubject is the core-events subject this listener consumes — the
	// SAME event internal/privacyworker consumes on its own durable (that one
	// destroys the key material; this one delivers the consequence to the read
	// models).
	FilterSubject = "events.privacy.retentionClassKeyShredded"
	// DefaultDurable is this listener's durable consumer name. Distinct from
	// privacy-worker's `privacy-worker-retention` and from the identity-plane
	// `refractor-keyshredded`: all three consume independently.
	DefaultDurable = "refractor-classkeyshredded"
	// DefaultOpLane is the ops.<lane> the finalization is submitted on.
	DefaultOpLane = "system"

	// StepProjectionsRebuilt is the RecordRetentionClassShredFinalization step
	// this listener produces — and the ONLY producer of it, which is why the
	// packages/privacy-base script refused the step until this package existed.
	StepProjectionsRebuilt = "projectionsRebuilt"

	defaultRedeliveryDelay = 5 * time.Second
	// maxNotRegisteredDeliveries bounds the redelivery of an event naming a
	// lens that has not registered yet (a Refractor still starting up), so a
	// permanently-misconfigured RuleID cannot nak-loop forever.
	maxNotRegisteredDeliveries = 20
)

// RebuildTarget names one lens whose projection must be rebuilt.
type RebuildTarget struct {
	RuleID string
}

// Config configures the Manager.
type Config struct {
	Conn         *substrate.Conn
	EventsStream string
	Durable      string
	// Control is the Refractor control service each lens's Pipeline registers
	// its Rebuilder against (cmd/refractor's controlSvc).
	Control *control.Service
	Logger  *slog.Logger

	// ActorKey is the identity.system.privacy service-actor vertex key the
	// RecordRetentionClassShredFinalization{projectionsRebuilt} op is submitted
	// under. Empty disables the durable attestation (with a startup warning) —
	// the rebuilds still run, but the retentionKeyStatus row stays visibly
	// in-flight, which is the honest reading: nothing attested.
	ActorKey string
	// OpLane is the ops.<lane> for the finalization submit. Defaults to
	// DefaultOpLane.
	OpLane string
}

// Manager runs the retentionClassKeyShredded rebuild consumer.
type Manager struct {
	cfg     Config
	handled atomic.Uint64

	mu           sync.Mutex
	targetLister func(holderType string) []RebuildTarget
}

// New constructs a Manager, applying defaults for the omitted fields. Panics if
// cfg.Control is nil — every path in the handler dereferences it, so a nil
// Control would otherwise panic the consumer goroutine on the first real event
// rather than at construction (mirrors keyshredded and control.Service).
func New(cfg Config) *Manager {
	if cfg.Control == nil {
		panic("classkeyshredded: New: Control must not be nil")
	}
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
		cfg.Logger.Warn("refractor classkeyshredded: no ActorKey configured; retention-class shred attestation disabled (retentionKeyStatus rows will stay in-flight)")
	}
	return &Manager{cfg: cfg}
}

// SetTargetLister registers the function that answers "which live lenses
// declare holder type T?", evaluated fresh on every destruction event rather
// than fixed at construction. That freshness is load-bearing: lenses arrive by
// package install at runtime with install-time NanoID RuleIDs, so a set fixed
// at construction would silently miss every lens installed since boot — and
// missing one means its plaintext survives a destruction that reported success.
// Thread-safe; a nil fn clears it.
func (m *Manager) SetTargetLister(fn func(holderType string) []RebuildTarget) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.targetLister = fn
}

func (m *Manager) targetsFor(holderType string) []RebuildTarget {
	m.mu.Lock()
	lister := m.targetLister
	m.mu.Unlock()
	if lister == nil {
		return nil
	}
	return lister(holderType)
}

// HandledTotal returns the count of destruction events this instance has
// finished handling.
func (m *Manager) HandledTotal() uint64 {
	return m.handled.Load()
}

// classKeyShreddedEvent mirrors internal/privacyworker's minimal event view.
type classKeyShreddedEvent struct {
	Payload struct {
		RetentionClassKey string `json:"retentionClassKey"`
	} `json:"payload"`
}

// Run drives the durable consumer, blocking until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	return m.cfg.Conn.RunDurableConsumer(ctx, substrate.DurableConsumerConfig{
		Stream:          m.cfg.EventsStream,
		FilterSubject:   FilterSubject,
		Durable:         m.cfg.Durable,
		RedeliveryDelay: defaultRedeliveryDelay,
		Logger:          m.cfg.Logger,
	}, m.handleClassKeyShredded)
}

// handleClassKeyShredded rebuilds every lens declaring the destroyed holder's
// type, attempting ALL of them before disposing the message, then attests.
//
// The rebuild is safe to run the moment this event arrives, and that is a
// property of the write path rather than of timing: ShredRetentionClassKey
// writes `shredded: true` to <holder>.piiKey on the SAME commit that emits this
// event, and the SecureDecryptor reads that document straight off Core KV. So
// the durable flag is already visible to every decrypt the rebuild triggers,
// and no handshake with the privacy-worker's asynchronous Vault.ShredKey is
// needed — that destruction is a redundant second path to the same refusal, not
// a precondition for it.
func (m *Manager) handleClassKeyShredded(ctx context.Context, msg substrate.Message) substrate.Decision {
	if len(msg.Body) == 0 {
		return substrate.Ack
	}
	var ev classKeyShreddedEvent
	if err := json.Unmarshal(msg.Body, &ev); err != nil {
		m.cfg.Logger.Error("refractor classkeyshredded: undecodable event; dropping",
			"error", err)
		return substrate.Term
	}
	holderKey := ev.Payload.RetentionClassKey
	if holderKey == "" {
		m.cfg.Logger.Error("refractor classkeyshredded: event carries no retentionClassKey; dropping")
		return substrate.Term
	}
	holderType := vault.KeyHolderType(holderKey)
	if holderType == "" {
		m.cfg.Logger.Error("refractor classkeyshredded: retentionClassKey names no holder type; dropping",
			"retentionClassKey", holderKey)
		return substrate.Term
	}

	targets := m.targetsFor(holderType)
	allClean := true
	notRegistered := false

	for _, t := range targets {
		// truncate=false deliberately: the row is RETAINED and only its
		// plaintext is destroyed, so the rebuild must upsert null over it, not
		// delete it. Truncating would also take out every other class's rows
		// this lens carries.
		err := m.cfg.Control.RebuildRule(ctx, t.RuleID, false)
		if err == nil {
			continue
		}
		allClean = false
		if errors.Is(err, control.ErrRuleNotRegistered) {
			// The lens exists in the corpus but its pipeline has not registered
			// yet (a Refractor still starting). Retry the whole event: a rebuild
			// is idempotent, so re-attempting the ones that already succeeded
			// costs a rescan, not correctness.
			if msg.NumDelivered <= maxNotRegisteredDeliveries {
				notRegistered = true
				continue
			}
			m.cfg.Logger.Error("refractor classkeyshredded: lens never registered after the redelivery budget; giving up (privacy-critical, its rows may still hold plaintext)",
				"retentionClassKey", holderKey, "ruleId", t.RuleID,
				"deliveries", msg.NumDelivered)
			continue
		}
		// A real rebuild failure: this lens may still be rendering plaintext
		// whose key no longer exists, which is the same hazard a failed
		// nullification raises on the identity plane. Halt it and alert; the
		// remaining targets are still attempted below.
		pcErr := failure.PrivacyCritical(err)
		m.cfg.Logger.Error("refractor classkeyshredded: rebuild failed; pausing lens (privacy-critical, no retry)",
			"retentionClassKey", holderKey, "ruleId", t.RuleID, "error", pcErr)
		if pauseErr := m.cfg.Control.PauseRule(ctx, t.RuleID); pauseErr != nil {
			m.cfg.Logger.Error("refractor classkeyshredded: pause after a failed rebuild also failed",
				"ruleId", t.RuleID, "error", pauseErr)
		}
	}

	if notRegistered {
		return substrate.NakWithDelay
	}

	// Attest ONLY when every affected lens rebuilt cleanly this delivery. An
	// empty target set is vacuously clean and MUST still attest: if no live lens
	// declares this holder type then no read model holds the plaintext, so the
	// erasure is complete — leaving it unattested would park the operator's
	// retentionKeyStatus row forever on the strength of there being nothing to
	// do. Publish-then-ack: a failed submit naks the whole event, and the
	// deterministic requestId collapses the redelivery on the Contract #4
	// tracker.
	if allClean && m.cfg.ActorKey != "" {
		if err := m.submitFinalization(ctx, holderKey, msg.Sequence); err != nil {
			m.cfg.Logger.Warn("refractor classkeyshredded: finalization submit failed; retrying whole event",
				"retentionClassKey", holderKey, "error", err)
			return substrate.NakWithDelay
		}
	}

	m.cfg.Logger.Info("refractor classkeyshredded: retention-class key destruction delivered to the read models",
		"retentionClassKey", holderKey, "holderType", holderType,
		"lensesRebuilt", len(targets), "attested", allClean)
	m.handled.Add(1)
	return substrate.Ack
}

// finalizationOpEnvelope is the Contract #2 §2.1 op wire format this listener
// publishes to ops.<lane>, carried as a private copy to keep the module
// boundary clean (the weaver/objectmanager/privacyworker idiom).
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

// submitFinalization publishes one
// RecordRetentionClassShredFinalization{projectionsRebuilt} op.
// ContextHint.Reads declares the piiKey aspect so the record is hydrated and
// OCC-conditioned — the sibling vaultKeyDestroyed record the privacy-worker
// submits races this one on the system lane, and conditioning turns a would-be
// lost update into a transparent commit-path retry. The requestId is keyed on
// the triggering event's backing-stream sequence, so a redelivery of the SAME
// event collapses on the Contract #4 tracker while a fresh destruction derives
// a new id.
func (m *Manager) submitFinalization(ctx context.Context, holderKey string, seq uint64) error {
	payload, err := json.Marshal(map[string]any{
		"retentionClassKey": holderKey,
		"step":              StepProjectionsRebuilt,
	})
	if err != nil {
		return fmt.Errorf("classkeyshredded: marshal payload: %w", err)
	}
	env := finalizationOpEnvelope{
		RequestID: substrate.DeriveNanoID("rcshredfin:"+StepProjectionsRebuilt+":",
			holderKey+"\x00"+strconv.FormatUint(seq, 10)),
		Lane:          m.cfg.OpLane,
		OperationType: "RecordRetentionClassShredFinalization",
		Actor:         m.cfg.ActorKey,
		SubmittedAt:   substrate.FormatTimestamp(time.Now()),
		Payload:       payload,
		ContextHint:   &finalizationContextHint{Reads: []string{holderKey + ".piiKey"}},
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("classkeyshredded: marshal op envelope: %w", err)
	}
	return m.cfg.Conn.Publish(ctx, "ops."+m.cfg.OpLane, data, nil)
}
