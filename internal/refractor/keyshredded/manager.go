// Package keyshredded runs the Refractor half of crypto-shredding's
// projection-nullification step (vault-crypto-shredding-design.md §2.4, Fire
// 4a): a durable consumer on events.privacy.keyShredded that removes a
// shredded identity's already-projected rows from configured lens targets —
// belt-and-suspenders in Phase A (those rows already hold now-garbage
// ciphertext; general lenses never decrypt, so nothing plaintext is exposed
// either way), load-bearing once Fire 5's Secure Lens starts holding
// plaintext.
//
// Distinct from internal/privacyworker, which destroys the Vault key itself
// (a separate, independent consumer of the SAME event — both run concurrently
// and are individually durable/idempotent, so neither's failure blocks the
// other). Runs inside the Refractor process (design §3: Refractor already
// owns row-nullification + already consumes Core-KV CDC), not a separate
// binary.
//
// Targets are configured explicitly (RuleID + the Into.Key field the
// identityKey maps to) rather than auto-discovered: Refractor has no
// registry of lenses by source-vertex-type (lens MATCH clauses are opaque
// compiled cypher, not a declared field), so an explicit, reviewable
// allowlist is the grounded mechanism — precedented by how targets are
// configured elsewhere in this codebase, not inferred by parsing queries.
// A SECURE lens (secureColumns) needs no entry here: its piiKey-CDC-triggered
// reprojection already scrubs a shredded identity's secure columns to NULL in
// place (pipeline/secure.go), the stronger guarantee. This allowlist is for
// plain lenses whose rows should be DELETED outright on a shred. A plain lens
// only ever projects a sensitive aspect as its ciphertext envelope (general
// lenses never decrypt), so an un-listed plain lens holds garbage ciphertext
// after a shred — hygiene, not a plaintext leak. No lens opts in today, so
// empty Targets makes this a harmless no-op consumer that still exercises the
// event, the counters, and the failure-tier path.
//
// On a nullification failure this raises the privacy-critical failure tier
// (failure.PrivacyCritical): the affected lens is paused via the control
// service and the event is Acked (not retried) — halting silently-wrong state
// takes priority over redelivering into a delete that is already failing, and
// the retry-elsewhere concern crash-survival worried about is covered by
// JetStream's own durable at-least-once redelivery, not a retry loop here.
package keyshredded

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	// FilterSubject is the core-events subject this listener consumes — the
	// SAME event internal/privacyworker consumes (two independent consumers).
	FilterSubject = "events.privacy.keyShredded"
	// DefaultDurable is this listener's durable consumer name.
	DefaultDurable = "refractor-keyshredded"
	// DefaultOpLane is the ops.<lane> RecordShredFinalization is submitted on
	// (the Processor consumes ops.system).
	DefaultOpLane = "system"

	// StepProjectionsNullified is the RecordShredFinalization step this
	// listener records (packages/privacy-base shredIdentityKey DDL).
	StepProjectionsNullified = "projectionsNullified"

	defaultRedeliveryDelay = 5 * time.Second

	// maxNotRegisteredDeliveries bounds how many times a target's
	// ErrRuleNotRegistered naks the whole event for redelivery before this
	// listener gives up on that target instead of retrying forever. Sized well
	// above any plausible startup race (Refractor's own lens activation is
	// seconds, not minutes) so a genuine still-starting-up target always clears
	// it, while a permanently-misconfigured RuleID stops nak-looping.
	maxNotRegisteredDeliveries = 20
)

// NullifyTarget names one lens whose projected row for a shredded identity
// must be removed. KeyField is the Into.Key field name the identityKey maps
// to for THIS lens (lenses may key their output differently), so the delete
// call can build the right keys map.
type NullifyTarget struct {
	RuleID   string
	KeyField string
}

// Config configures the Manager.
type Config struct {
	Conn         *substrate.Conn
	EventsStream string
	Durable      string
	// Control is the Refractor control service each lens's Pipeline registers
	// its Pauser/RowNullifier against (cmd/refractor's controlSvc).
	Control *control.Service
	// Targets is the explicit, reviewable list of lenses to nullify on shred.
	// Empty is valid (a no-op sweep) — see package doc.
	Targets []NullifyTarget
	Logger  *slog.Logger

	// ActorKey is the identity.system.privacy service-actor vertex key this
	// listener submits RecordShredFinalization{projectionsNullified} under
	// once EVERY configured target nullified cleanly (Fire 4b). Empty disables
	// the durable finalization record (with a startup warning) — nullification
	// itself still runs. A privacy-critical halt or a given-up target skips
	// the record, leaving the shredStatus row visibly stuck (the point); the
	// event is Acked, so after repairing the target the operator clears the
	// stuck row by re-submitting ShredIdentityKey — the re-shred resets the
	// finalization cycle and its new keyShredded event re-drives both
	// listeners end to end.
	ActorKey string
	// OpLane is the ops.<lane> for the RecordShredFinalization submit.
	// Defaults to DefaultOpLane.
	OpLane string
}

// Manager runs the keyShredded nullification consumer.
type Manager struct {
	cfg     Config
	handled atomic.Uint64
}

// New constructs a Manager, applying defaults for the omitted fields.
// Panics if cfg.Control is nil — every code path in handleKeyShredded
// dereferences it, so a nil Control would otherwise panic the consumer
// goroutine on the first real event instead of failing at construction
// (mirrors control.Service's own nil-panic on Register*).
func New(cfg Config) *Manager {
	if cfg.Control == nil {
		panic("keyshredded: New: Control must not be nil")
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
		cfg.Logger.Warn("refractor keyshredded: no ActorKey configured; shred finalization recording disabled (shredStatus rows will stay in-flight)")
	}
	return &Manager{cfg: cfg}
}

// HandledTotal returns the count of keyShredded events this instance has
// finished handling (success or privacy-critical halt) — Contract #5 §5.4's
// keyshreddedHandledTotal.
func (m *Manager) HandledTotal() uint64 {
	return m.handled.Load()
}

// keyShreddedEvent mirrors internal/privacyworker's minimal event view.
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
		FilterSubject:   FilterSubject,
		Durable:         m.cfg.Durable,
		RedeliveryDelay: defaultRedeliveryDelay,
		Logger:          m.cfg.Logger,
	}, m.handleKeyShredded)
}

// handleKeyShredded nullifies every configured target's row for the shredded
// identity, attempting ALL targets before disposing the message (one target's
// outcome must not skip the rest). A target lens that has not registered yet
// (ErrRuleNotRegistered — e.g. still starting up) makes the whole event
// NAK'd for redelivery, since a retry will re-attempt every target including
// the ones that already succeeded (Delete is idempotent) — bounded by
// maxNotRegisteredDeliveries so a permanently-misconfigured RuleID does not
// nak-loop forever. A real Delete failure raises the privacy-critical tier
// for that target's lens (paused, alerted) but does not stop the remaining
// targets from being attempted; once every target has been attempted the
// event is Acked regardless — privacy-critical failures are never retried
// (see package doc).
func (m *Manager) handleKeyShredded(ctx context.Context, msg substrate.Message) substrate.Decision {
	if len(msg.Body) == 0 {
		return substrate.Ack
	}
	var ev keyShreddedEvent
	if err := json.Unmarshal(msg.Body, &ev); err != nil {
		m.cfg.Logger.Warn("refractor keyshredded: unparseable privacy.keyShredded event; dropping", "error", err)
		return substrate.Term
	}
	if ev.Payload.IdentityKey == "" {
		m.cfg.Logger.Warn("refractor keyshredded: privacy.keyShredded missing identityKey; dropping")
		return substrate.Term
	}

	notRegistered := false
	allClean := true
	for _, target := range m.cfg.Targets {
		keys := map[string]any{target.KeyField: ev.Payload.IdentityKey}
		// projectionSeq: a GUARDED nats_kv target (adapter/natskv.go's H4
		// no-resurrect guard, opted in per-lens via SetGuarded — e.g.
		// capabilityEphemeral/myTasks) drops a write whose projectionSeq is <=
		// the row's stored watermark as a stale replay. A shred is authoritative
		// and terminal: it must always win over whatever CDC-driven projectionSeq
		// the row was last upserted with, so the delete is stamped with the
		// maximum watermark rather than any stream-relative sequence number (this
		// event isn't on the row's own CDC stream, so no naturally-comparable
		// sequence exists). No effect on an unguarded target (the common case),
		// which ignores projectionSeq entirely. math.MaxInt64, not MaxUint64: a
		// guarded Postgres grant-table/protected target (rls.go) binds this value
		// as a signed int64 column parameter — MaxUint64 wraps to -1 there, which
		// would always LOSE the guard comparison instead of always winning it.
		// MaxInt64 is still unreachably far above any real CDC/stream sequence, so
		// the "always wins" property holds on both the uint64 NATS-KV path and the
		// int64 Postgres path.
		//
		// KNOWN LIMITATION (observed empirically against a live full-engine
		// harness, cause not fully isolated): a deleted row can reappear shortly
		// after this call returns. The identity vertex stays alive (not
		// tombstoned) after a shred, and Refractor's projection pipeline can
		// re-upsert this lens's row from a later CDC delivery for that vertex — a
		// fresh, later write with its own real projectionSeq legitimately beats
		// any watermark this listener stamps, guarded target or not. This is
		// consistent with the design's "belt-and-suspenders in Phase A" framing
		// (Phase A rows hold only ciphertext, so a resurrected row is not a new
		// leak) but means this nullification is best-effort/transient, not a
		// permanent guarantee, until a lens-side shredded-identity filter
		// (mirroring the negative/filter-retraction projection pattern) or Fire
		// 5's Secure Lens closes the gap. Flagged on the board as a Fire 4a
		// residual, not silently swept under "done."
		err := m.cfg.Control.NullifyRow(ctx, target.RuleID, keys, math.MaxInt64)
		if err == nil {
			continue
		}
		if errors.Is(err, control.ErrRuleNotRegistered) {
			if msg.NumDelivered < maxNotRegisteredDeliveries {
				m.cfg.Logger.Warn("refractor keyshredded: target lens not yet registered; will retry whole event",
					"identityKey", ev.Payload.IdentityKey, "ruleId", target.RuleID, "error", err,
					"numDelivered", msg.NumDelivered)
				notRegistered = true
				continue
			}
			// A target that is STILL not registered after many redeliveries is a
			// permanent misconfiguration (a typo'd/decommissioned RuleID), not a
			// startup race — naking forever would spam redelivery indefinitely with
			// no path to resolution. Give up loudly instead of looping: this is an
			// operator-visible defect (fix the Targets config), not a transient one.
			m.cfg.Logger.Error("refractor keyshredded: target lens still not registered after max redeliveries; giving up on this target",
				"identityKey", ev.Payload.IdentityKey, "ruleId", target.RuleID, "error", err,
				"numDelivered", msg.NumDelivered)
			allClean = false
			continue
		}
		// A real Delete failure: privacy-critical — halt this lens, alert, no retry
		// (the remaining targets are still attempted below).
		allClean = false
		pcErr := failure.PrivacyCritical(err)
		m.cfg.Logger.Error("refractor keyshredded: nullification failed; pausing lens (privacy-critical, no retry)",
			"identityKey", ev.Payload.IdentityKey, "ruleId", target.RuleID, "error", pcErr)
		if pauseErr := m.cfg.Control.PauseRule(ctx, target.RuleID); pauseErr != nil {
			m.cfg.Logger.Error("refractor keyshredded: pause after nullification failure also failed",
				"ruleId", target.RuleID, "error", pauseErr)
		}
	}
	if notRegistered {
		return substrate.NakWithDelay
	}

	// Durably record the finalization ONLY when every configured target
	// nullified cleanly this delivery (an empty Targets list is vacuously
	// clean). A privacy-critical halt or a given-up target skips the record —
	// the shredStatus row stays visibly stuck, which is the observability this
	// exists for. Publish-then-ack: a failed submit naks the whole event;
	// NullifyRow is idempotent, so the redelivery re-attempts targets and
	// retries the submit with the same deterministic requestId (Contract #4
	// tracker collapse).
	if allClean && m.cfg.ActorKey != "" {
		if err := m.submitFinalization(ctx, ev.Payload.IdentityKey, msg.Sequence); err != nil {
			m.cfg.Logger.Warn("refractor keyshredded: RecordShredFinalization submit failed; retrying whole event",
				"identityKey", ev.Payload.IdentityKey, "error", err)
			return substrate.NakWithDelay
		}
	}

	m.cfg.Logger.Info("refractor keyshredded: identity's projected rows nullified",
		"identityKey", ev.Payload.IdentityKey, "targets", len(m.cfg.Targets))
	m.handled.Add(1)
	return substrate.Ack
}

// finalizationOpEnvelope is the Contract #2 §2.1 op wire format this listener
// publishes to ops.<lane> — the same shape internal/processor reads; carried
// as a private copy to keep the module boundary clean (the
// weaver/objectmanager/privacyworker idiom).
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

// submitFinalization publishes one RecordShredFinalization
// {projectionsNullified} op. ContextHint.Reads declares the piiKey aspect so
// the record is hydrated + OCC-conditioned (the sibling vaultKeyDestroyed
// record can race this one on the system lane's concurrent workers;
// conditioning turns a would-be lost-update into a transparent commit-path
// retry). Class-less (the Processor's operationType→class reverse index
// resolves it). The requestId is keyed on the triggering event's
// backing-stream sequence, so a redelivery of the SAME event collapses on the
// Contract #4 tracker while a new shred event derives a fresh id.
func (m *Manager) submitFinalization(ctx context.Context, identityKey string, seq uint64) error {
	payload, err := json.Marshal(map[string]any{
		"identityKey": identityKey,
		"step":        StepProjectionsNullified,
	})
	if err != nil {
		return fmt.Errorf("keyshredded: marshal payload: %w", err)
	}
	env := finalizationOpEnvelope{
		RequestID: substrate.DeriveNanoID("shredfin:"+StepProjectionsNullified+":",
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
		return fmt.Errorf("keyshredded: marshal op envelope: %w", err)
	}
	return m.cfg.Conn.Publish(ctx, "ops."+m.cfg.OpLane, data, nil)
}
