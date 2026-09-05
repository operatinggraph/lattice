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
//
// The rebuild delivers the erasure to STORED read models, and one target shape
// is outside that: a personal lens publishes to devices, and its rebuild
// publishes nothing at all while it replays. So a personal lens declaring a
// destroyed holder's type is REFUSED, loudly, and takes the attestation with it
// (ErrPersonalTargetErasureUnsupported) rather than being rebuilt into a clean
// result nobody's device saw.
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
	maxNotRegisteredDeliveries = maxNotReadyDeliveries + 20
	// maxSubmitDeliveries is the third stage on the same counter, for a failing
	// finalization publish.
	maxSubmitDeliveries = maxNotRegisteredDeliveries + 20
	// maxNotReadyDeliveries bounds the same retry for a registry that has not
	// finished loading. It answers a different question — "can this enumeration
	// be trusted at all", not "is this one lens up yet" — but both are counted
	// off the SAME msg.NumDelivered, so they are STAGED rather than independent:
	// readiness runs first and owns deliveries 1..20, and the not-registered
	// budget above starts where this one ends. Equal sizes would leave the second
	// budget dead, since a boot that spends its deliveries on readiness would
	// reach the first ErrRuleNotRegistered with no retries left and go straight to
	// the privacy-critical give-up arm. The window it needs is real: main.go
	// registers the entry in the lens registry before it registers the Rebuilder,
	// so "ready" can be true while a rebuilder is not.
	maxNotReadyDeliveries = 20

	// DefaultRebuildWait bounds how long ONE lens's rescan is waited on. It is
	// generous because a rebuild replays every key matching the lens's filter
	// and these are the lenses that carry sensitive columns, and it is BOUNDED
	// because the wait happens inside a strictly serial durable handler: an
	// unbounded one stops every later class-key destruction for the life of the
	// process. A paused lens makes that concrete — Rebuild's supervisor reset
	// requests a reopen without clearing a pause (and does not wait on a paused
	// pump, precisely because that wait could never be satisfied), so its pump
	// stays parked and its outstanding count never reaches zero. The handler
	// creates that condition itself by pausing a lens whose rebuild failed, so
	// the next event would enumerate the same lens and wedge.
	DefaultRebuildWait = 30 * time.Minute
	// DefaultHandlerBudget bounds the WHOLE handler, not one lens. RebuildWait
	// is per target inside the loop, so N targets make the handler's real upper
	// bound N × RebuildWait — unbounded in the only variable that matters, since
	// N is however many lenses declare the holder type. Every rebuild draws from
	// this one budget, and a target reached with nothing left is not attempted:
	// the attestation is withheld rather than an ack deadline blown.
	DefaultHandlerBudget = 90 * time.Minute
	// defaultAckWait must exceed DefaultHandlerBudget, or JetStream redelivers
	// the event while the rebuilds it triggered are still running and each
	// redelivery re-runs all of them. The 30s default is far below a single
	// rebuild at these lens sizes, which is the normal case, not an edge.
	defaultAckWait = 2 * time.Hour
	// classKeyPrefetch is 1 because AckWait is measured from delivery into the
	// client's prefetch buffer, not from handler entry. With nats.go's default
	// of 500 a queued destruction would sit through every preceding rebuild and
	// be redelivered before its own handler ran, no matter how large AckWait is
	// — and each redelivery re-runs every rebuild and burns a NumDelivered the
	// readiness and not-registered budgets are counting.
	classKeyPrefetch = 1
)

// ErrPersonalTargetErasureUnsupported is the refusal a personal lens draws when
// a retention-class destruction enumerates it. It is a gap in the DELIVERY
// mechanism, not a fault of the lens, and it is fatal to the attestation.
//
// A rebuild delivers an erasure by replaying every row through the
// SecureDecryptor and upserting null over the plaintext the target still holds.
// A personal lens's rebuild publishes nothing at all while it replays — every
// message would land below the frame high-water its devices already hold and be
// dropped there — so the replay upserts nothing, the rebuild reports success,
// and the plaintext stays on the SYNC stream and on every device that holds it.
// That is the shape this whole package exists to refuse: an erasure that reads
// as compliant from every surface while the sensitive value is still readable.
var ErrPersonalTargetErasureUnsupported = errors.New(
	"class-key erasure through a personal lens's rebuild publishes nothing to its devices; the attestation cannot be recorded")

// RebuildTarget names one lens whose projection must be rebuilt.
type RebuildTarget struct {
	RuleID string
	// Personal marks a lens whose target is a per-actor subject stream devices
	// subscribe to, rather than a stored read model. Such a target is REFUSED
	// rather than rebuilt (ErrPersonalTargetErasureUnsupported), and it is
	// carried as a label rather than filtered out at the enumeration precisely
	// so the refusal is visible: a target dropped from the set would read as
	// "no live lens holds this plaintext" and attest.
	Personal bool
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

	// RebuildWait bounds the wait on ONE lens's rescan. Defaults to
	// DefaultRebuildWait.
	RebuildWait time.Duration
	// HandlerBudget bounds every rebuild of one destruction together. Defaults
	// to DefaultHandlerBudget.
	HandlerBudget time.Duration
	// AckWait is the durable's ack deadline. Defaults to defaultAckWait.
	AckWait time.Duration
}

// Manager runs the retentionClassKeyShredded rebuild consumer.
type Manager struct {
	cfg     Config
	handled atomic.Uint64

	mu            sync.Mutex
	targetLister  func(holderType string) []RebuildTarget
	registryReady func(ctx context.Context, holderType string) error
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
	if cfg.RebuildWait <= 0 {
		cfg.RebuildWait = DefaultRebuildWait
	}
	if cfg.HandlerBudget <= 0 {
		cfg.HandlerBudget = DefaultHandlerBudget
	}
	if cfg.AckWait <= 0 {
		cfg.AckWait = defaultAckWait
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

// SetRegistryReady registers the check that answers "is the lens registry
// complete enough for an enumeration over THIS holder type to mean anything?".
// It returns nil when it is, and an error naming the reason when it is not or
// cannot be determined.
//
// It takes the holder type because the corpus-global form answers a question
// nobody asked: any single lens that never registers, for any reason, would
// withhold every attestation for every holder type forever.
//
// This is the difference between the two readings of an under-populated target
// set, which the set itself cannot tell apart: "no live lens declares this
// holder type" (legitimately clean — nothing holds the plaintext, so the
// erasure is complete) and "the registry has not loaded" (a rebuilt-nothing
// attestation over lenses still serving plaintext). The identity half solves
// the same problem with a static floor target that hits ErrRuleNotRegistered
// until the registry loads; a retention class cannot have one, because nothing
// obliges any lens to bind a class, so the signal is explicit here instead.
//
// Absence is NOT readiness. Until this is set — which includes the whole of
// boot, since the check is wired from the registry probe constructed late in
// the startup sequence — every destruction event is retried rather than
// attested. That is deliberate: a cold-boot durable replays from the start of
// the subject history (DeliverAllPolicy), so the un-set window is exactly when
// a vacuous attestation would be most likely and most wrong.
func (m *Manager) SetRegistryReady(fn func(ctx context.Context, holderType string) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registryReady = fn
}

func (m *Manager) checkRegistryReady(ctx context.Context, holderType string) error {
	m.mu.Lock()
	fn := m.registryReady
	m.mu.Unlock()
	if fn == nil {
		return errors.New("lens registry readiness check not wired yet (Refractor still starting)")
	}
	return fn(ctx, holderType)
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
		// The handler blocks on real rescans, so the default 30s ack deadline
		// would redeliver the event mid-flight and re-run every rebuild — and
		// the prefetch cap is half of that fix, since the deadline is measured
		// from delivery into the client's buffer rather than from handler entry.
		AckWait:     m.cfg.AckWait,
		MaxPrefetch: classKeyPrefetch,
		Logger:      m.cfg.Logger,
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

	// Readiness FIRST: the enumeration below is only as trustworthy as the
	// registry it reads, and an under-populated registry produces a SHORT target
	// set, not just an empty one — so this gates the whole delivery, not merely
	// the vacuous case.
	readyErr := m.checkRegistryReady(ctx, holderType)
	if readyErr != nil {
		if msg.NumDelivered <= maxNotReadyDeliveries {
			m.cfg.Logger.Warn("refractor classkeyshredded: lens registry not reconciled; retrying the whole event rather than attesting over a partial enumeration",
				"retentionClassKey", holderKey, "deliveries", msg.NumDelivered, "reason", readyErr)
			return substrate.NakWithDelay
		}
		// Past the budget, proceed WITHOUT attesting: rebuild what is visible so
		// the destruction reaches whatever loaded, and leave the operator's
		// retentionKeyStatus row visibly in-flight. That is the honest state —
		// something is wrong with the registry itself (a live
		// LensRegistryIncomplete condition), and an attestation here would be a
		// claim about lenses this process cannot see.
		// Ack-and-alarm rather than Nak-forever, decided rather than defaulted.
		// Naking indefinitely would wedge this strictly-serial consumer on one
		// holder and stop every later destruction — the wedge the wait budget was
		// added to remove. What makes the Ack safe is that the destruction is
		// REDRIVABLE by an operator: re-submitting ShredRetentionClassKey for the
		// same holder is idempotent, clears the prior cycle's finalization
		// progress, and re-emits this event. The in-flight retentionKeyStatus row
		// is the signal to do it, so the log names the action rather than only the
		// fault.
		m.cfg.Logger.Error("refractor classkeyshredded: lens registry never reconciled for this holder type within the redelivery budget; delivering to what is registered and NOT attesting (privacy-critical) — once the missing lenses register, re-drive by re-submitting ShredRetentionClassKey for this holder",
			"retentionClassKey", holderKey, "holderType", holderType,
			"deliveries", msg.NumDelivered, "reason", readyErr)
	}

	targets := m.targetsFor(holderType)
	allClean := readyErr == nil
	notRegistered := false

	// One budget for every rebuild this destruction triggers. Per-lens alone
	// leaves the handler's real bound at N × RebuildWait, which is what the ack
	// deadline has to sit above — and N is however many lenses declare the
	// holder type, so it is not a constant anyone can size against.
	deadline := time.Now().Add(m.cfg.HandlerBudget)

	for _, t := range targets {
		// Ahead of the budget, so a target that must never be rebuilt cannot be
		// reported as one the clock ran out on — and ahead of RebuildRule, which
		// makes the rebuild unreachable for it rather than merely discouraged.
		// The attestation goes with it: a personal lens reached here is holding
		// plaintext this destruction cannot take away, and the operator's
		// in-flight retentionKeyStatus row is the honest reading.
		if t.Personal {
			allClean = false
			m.cfg.Logger.Error("refractor classkeyshredded: a personal lens declares this holder type; its rebuild cannot deliver the erasure (privacy-critical, its devices still hold the plaintext) — NOT attesting",
				"retentionClassKey", holderKey, "holderType", holderType, "ruleId", t.RuleID,
				"error", failure.PrivacyCritical(ErrPersonalTargetErasureUnsupported))
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			allClean = false
			m.cfg.Logger.Error("refractor classkeyshredded: handler budget exhausted before this lens was rebuilt; NOT attesting (privacy-critical)",
				"retentionClassKey", holderKey, "ruleId", t.RuleID)
			continue
		}
		wait := min(m.cfg.RebuildWait, remaining)
		// truncate=false deliberately: the row is RETAINED and only its
		// plaintext is destroyed, so the rebuild must upsert null over it, not
		// delete it. Truncating would also take out every other class's rows
		// this lens carries.
		err := m.cfg.Control.RebuildRule(ctx, t.RuleID, false, wait)
		if err == nil {
			continue
		}
		allClean = false
		// A cancelled context is a shutdown, not a lens fault: pausing the lens
		// would blame it for a rolling deploy, and Acking below would advance
		// the durable cursor past a destruction no lens was rebuilt for and
		// nothing would ever redrive. Nak so the next process redelivers it.
		if ctx.Err() != nil {
			m.cfg.Logger.Warn("refractor classkeyshredded: shutting down mid-rebuild; retrying the whole event on the next process",
				"retentionClassKey", holderKey, "ruleId", t.RuleID)
			return substrate.NakWithDelay
		}
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
		// The two "not known to be rebuilt, but not the lens's fault" classes.
		// They must be told apart from a rebuild failure BEFORE the pause arm
		// below, because pausing here is self-perpetuating: a paused lens cannot
		// drain a rebuild (the supervisor's reset requests a reopen without
		// clearing a pause), so a lens whose rescan legitimately outran the
		// budget once would burn the whole budget, time out and re-pause on every
		// later destruction — while serving its pre-destruction rows the entire
		// time. That is the wedge the budget was added to remove, relocated. The
		// attestation is still withheld (allClean is already false): the lens is
		// left running, and the operator's row stays visibly in-flight.
		if errors.Is(err, control.ErrRebuildWaitTimeout) || errors.Is(err, control.ErrRebuildNotDrained) {
			m.cfg.Logger.Error("refractor classkeyshredded: rebuild not confirmed drained; NOT attesting and leaving the lens running (privacy-critical)",
				"retentionClassKey", holderKey, "ruleId", t.RuleID, "error", failure.PrivacyCritical(err))
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
			// Bounded like every other Nak arm here, and for a sharper reason: a
			// retry re-runs every rebuild this destruction triggered, so an
			// unbounded loop against a persistently unpublishable op is not a
			// retry, it is a rescan storm. Past the budget the rebuilds have
			// still landed; only the attestation is missing, which is what the
			// operator's in-flight row already says.
			if msg.NumDelivered <= maxSubmitDeliveries {
				m.cfg.Logger.Warn("refractor classkeyshredded: finalization submit failed; retrying whole event",
					"retentionClassKey", holderKey, "deliveries", msg.NumDelivered, "error", err)
				return substrate.NakWithDelay
			}
			m.cfg.Logger.Error("refractor classkeyshredded: finalization submit still failing past the redelivery budget; the rebuilds landed but the attestation did not (privacy-critical)",
				"retentionClassKey", holderKey, "deliveries", msg.NumDelivered, "error", err)
		}
	}

	m.cfg.Logger.Info("refractor classkeyshredded: retention-class key destruction delivered to the read models",
		"retentionClassKey", holderKey, "holderType", holderType,
		"lensesRebuilt", len(targets),
		// What was actually done, not what was eligible: with attestation
		// disabled (no ActorKey) the rebuilds still run but nothing is recorded,
		// and a log line reading attested=true over a submit that never happened
		// is how the documented disabled configuration reads as compliant.
		"attested", allClean && m.cfg.ActorKey != "")
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
		OperationType: "RecordRetentionClassShredFinalization", // op-name: (submits) this manager submits its own finalization op after rebuilding lens projections for a shredded retention class; a separate, independent consumer of the same event from privacyworker's own RecordRetentionClassShredFinalization submit — not a duplicate to consolidate
		Actor:         m.cfg.ActorKey,
		SubmittedAt:   substrate.FormatTimestamp(time.Now()),
		Payload:       payload,
		ContextHint: &finalizationContextHint{
			Reads: []string{holderKey + ".piiKey", m.cfg.ActorKey},
		},
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("classkeyshredded: marshal op envelope: %w", err)
	}
	return m.cfg.Conn.Publish(ctx, "ops."+m.cfg.OpLane, data, nil)
}
