package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/health/healthwire"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The health KV value schema is defined in internal/refractor/health/healthwire,
// a leaf package that depends on nothing but the standard library. It is
// re-exported here because it reads as health vocabulary at every call site,
// and because importing this package means linking a NATS client — which a
// browser-hosted Edge engine (edge-browser-node-design.md §3.2) must not do to
// decode a control response that embeds an Entry. Code that needs only the
// schema imports internal/refractor/health/healthwire directly.
//
// These are aliases, not new types: a healthwire.Entry IS a health.Entry.

// PauseReason values used in health KV entries.
const (
	PauseReasonInfra      = healthwire.PauseReasonInfra
	PauseReasonStructural = healthwire.PauseReasonStructural
	PauseReasonManual     = healthwire.PauseReasonManual
)

// Status values used in health KV entries.
const (
	StatusActive     = healthwire.StatusActive
	StatusPaused     = healthwire.StatusPaused
	StatusRebuilding = healthwire.StatusRebuilding
)

// FilterMode values used in health KV entries.
const (
	FilterModeNarrowedRelation = healthwire.FilterModeNarrowedRelation
	FilterModeNarrowedLabel    = healthwire.FilterModeNarrowedLabel
	FilterModeBroad            = healthwire.FilterModeBroad
)

// FilterBroadReason values used in health KV entries.
const (
	FilterBroadReasonNone                 = healthwire.FilterBroadReasonNone
	FilterBroadReasonNotEligible          = healthwire.FilterBroadReasonNotEligible
	FilterBroadReasonNonExhaustive        = healthwire.FilterBroadReasonNonExhaustive
	FilterBroadReasonLabelCap             = healthwire.FilterBroadReasonLabelCap
	FilterBroadReasonTaxonomyUnarmed      = healthwire.FilterBroadReasonTaxonomyUnarmed
	FilterBroadReasonTaxonomyUnresolvable = healthwire.FilterBroadReasonTaxonomyUnresolvable
	FilterBroadReasonInstallIncomplete    = healthwire.FilterBroadReasonInstallIncomplete
	FilterBroadReasonRegistrationFailed   = healthwire.FilterBroadReasonRegistrationFailed
)

// Entry is the full health KV value schema. The KV key is the ruleID; the KV
// bucket is configured via config.HealthKVBucket.
type Entry = healthwire.Entry

// Reporter reads and writes health KV entries for a single rule.
// It does NOT import the failure package — that dependency runs the other way.
type Reporter struct {
	kv             *substrate.KV
	ruleID         string
	mu             sync.RWMutex // protects activeSequence + ruleEngine
	activeSequence uint64       // cached rule sequence; set via SetRuleSequence
	ruleEngine     string       // cached resolved engine name; set via SetRuleEngine
	writeMu        sync.Mutex   // serializes all read-modify-write KV operations
}

// New creates a Reporter for the given rule. kv must be the health KV bucket.
func New(kv *substrate.KV, ruleID string) *Reporter {
	return &Reporter{kv: kv, ruleID: ruleID}
}

// SetRuleSequence caches the NATS sequence number of the currently-active rule version.
// Thread-safe. Does not write to KV — the cached value is included in the next health write.
// Called by the rule loader when a rule version is activated or updated.
func (r *Reporter) SetRuleSequence(seq uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activeSequence = seq
}

// SetRuleEngine caches the resolved engine name for this rule. Thread-safe.
// The cached value is included in the next health write and surfaced on the
// SetActive INFO log per Story 3.1a Decision #5.
func (r *Reporter) SetRuleEngine(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ruleEngine = name
}

// RuleEngine returns the cached resolved engine name. Thread-safe.
func (r *Reporter) RuleEngine() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ruleEngine
}

// ActiveSequence returns the cached active rule sequence. Thread-safe.
// Used by pipeline to fill the RuleSequence field of DLQ messages.
func (r *Reporter) ActiveSequence() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.activeSequence
}

// SetActive writes an "active" health entry. Reads the existing entry to preserve
// ErrorCount and ConsumerLag across process restarts (NFR4, AC4).
func (r *Reporter) SetActive(ctx context.Context) error {
	r.mu.RLock()
	seq := r.activeSequence
	eng := r.ruleEngine
	r.mu.RUnlock()

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		slog.Warn("health: SetActive could not read existing entry — every carried-forward field (error/redaction/drift counters, sweep progress, projection progress) resets to its zero value",
			"ruleId", r.ruleID, "err", err)
		existing = Entry{}
	}
	entry := Entry{
		RuleID:         r.ruleID,
		Status:         StatusActive,
		PauseReason:    nil, // JSON null
		ActiveSequence: seq,
		ConsumerLag:    existing.ConsumerLag,
		ErrorCount:     existing.ErrorCount, // preserved across restarts
		LastError:      nil,                 // JSON null
		LastUpdated:    time.Now().UTC().Format(time.RFC3339),
		RuleEngine:     eng,
		// The convergence sweep's round-robin position and heal count are
		// lens-lifetime state, not status-transition state: a pause/resume must
		// not silently restart the walk or zero the counter the operator reads.
		SweepCursor:     existing.SweepCursor,
		SweepReconciled: existing.SweepReconciled,
		// The divergence audit's cursor and last completed cycle carry forward
		// for the same reason, and the cycle stamp is the sharper of the two: it
		// is a COVERAGE CLAIM the lens already earned, so zeroing it on a
		// pause/resume would retract a whole-lens verdict nothing re-derived.
		AuditCursor:           existing.AuditCursor,
		AuditCycleCompletedAt: existing.AuditCycleCompletedAt,
		// The personal convergence sweep's position, its last closed cycle and
		// the grant-change drain's depth, for the same reason with one extra
		// edge: that walk is a PROCESS-level mechanism fanned out onto per-lens
		// entries, so a single lens's status transition observes nothing
		// whatever about it. Zeroing them here would make a paused-and-resumed
		// personal lens read as one with no standing healer at all, and since
		// the sweep rewrites them only once per tick the false reading stands
		// for a whole interval.
		PersonalSweepCursor:           existing.PersonalSweepCursor,
		PersonalSweepCycleCompletedAt: existing.PersonalSweepCycleCompletedAt,
		PersonalSweepQueueDepth:       existing.PersonalSweepQueueDepth,
		// Every CUMULATIVE fault counter carries forward for the same reason,
		// and SecureRedactions is the one that made the omission load-bearing:
		// a rebuild calls SetRebuilding on the way in and SetActive on the way
		// out, so a dropped counter is zeroed twice by the very operation an
		// erasure uses to reach the read models. The LensSecureRedaction issue
		// then goes quiet while the unresolvable nulls are still being served —
		// exactly the delta-signal failure the counter is cumulative to avoid.
		SecureRedactions:  existing.SecureRedactions,
		EvalDriftRetries:  existing.EvalDriftRetries,
		EvalDriftRequeues: existing.EvalDriftRequeues,
		// The projection-progress fields, for the same reason and by the same
		// rule SetProjectionProgress states about itself: they are OBSERVATIONS,
		// and a status transition observes none of them, so writing zeroes here
		// asserts something no read established. The LagPoller restores the first
		// four within a cycle; LastProjectedAt it does not, because it only ever
		// writes a NON-ZERO value — so a SetActive at activation erases a lens's
		// last-projection timestamp permanently.
		ProjectionLag:      existing.ProjectionLag,
		LagProgressAt:      existing.LagProgressAt,
		AckPending:         existing.AckPending,
		AckFloorProgressAt: existing.AckFloorProgressAt,
		LastProjectedAt:    existing.LastProjectedAt,
		// The cost gauge belongs to that same group, and is the one member
		// NOTHING restores after a transition. SetPeakBindingRows refuses to
		// write without a real sample, and samples come only from evaluations —
		// so a paused lens produces none, and a zero written here would stand for
		// the whole length of the pause. That is exactly when an operator is
		// reading the entry, and exactly the number they want: how expensive the
		// evaluation that preceded the trouble was.
		PeakBindingRows: existing.PeakBindingRows,
		// The consumer-filter footprint, by the same rule: a status transition
		// observes nothing about which subjects this lens's consumer filters
		// on, so writing zeroes here would assert something no derivation
		// established. Dropping them is worse than a reset, because the ABSENT
		// state means "never derived a filter" — a lens that pauses would read
		// as one that has not yet decided. Nothing restores them within a
		// cycle either: only a re-derivation writes them, and a rebuild calls
		// SetRebuilding on the way in and SetActive on the way out, so an
		// omission here erases the footprint on the very operation that
		// re-derives it.
		FilterMode:        existing.FilterMode,
		FilterLabelCount:  existing.FilterLabelCount,
		FilterBroadReason: existing.FilterBroadReason,
		// The last probe-driven structural recovery, by the same rule and for
		// the sharpest reason of any field here: a status transition observes
		// nothing about a recovery that already happened, and these three are
		// the ONLY record it leaves. The pause it cleared is gone from
		// PauseReason and its diagnosis is gone from LastError the moment
		// SetActive runs, so an omission here does not merely reset a value —
		// it deletes the event.
		//
		// The erasure would land exactly where the fields matter most. A
		// self-heal that does not hold re-pauses within one ProbeInterval (10s,
		// the same order as the heartbeat), so a stamp dropped by the next
		// SetPaused is never observed by any beat: StructuralAutoRecoveryAttempts
		// would be unreadable in precisely the flapping case it exists to
		// report, and a lens that flapped to its relapse latch would end
		// carrying no auto-recovery record at all. A self-heal mid-rebuild is
		// the second instance: SetRebuilding on the way in, SetActive on the way
		// out, and the stamp gone twice over.
		StructuralAutoRecoveredAt:      existing.StructuralAutoRecoveredAt,
		StructuralAutoRecoveredCause:   existing.StructuralAutoRecoveredCause,
		StructuralAutoRecoveryAttempts: existing.StructuralAutoRecoveryAttempts,
	}
	if err := r.put(ctx, entry); err != nil {
		return err
	}
	slog.Info("health: rule active",
		"ruleId", r.ruleID,
		"activeSequence", seq, "errorCount", entry.ErrorCount,
		"ruleEngine", eng)
	return nil
}

// SetPaused writes a "paused" health entry with the given pause reason and last error.
// reason must be "infra", "structural", or "manual". Reads the existing entry to preserve
// ErrorCount and ConsumerLag.
func (r *Reporter) SetPaused(ctx context.Context, reason, lastError string) error {
	r.mu.RLock()
	seq := r.activeSequence
	r.mu.RUnlock()

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		slog.Warn("health: SetPaused could not read existing entry — every carried-forward field (error/redaction/drift counters, sweep progress, projection progress) resets to its zero value",
			"ruleId", r.ruleID, "err", err)
		existing = Entry{}
	}
	var lastErrPtr *string
	switch {
	case lastError != "":
		lastErrPtr = &lastError
	case existing.Status == StatusPaused && existing.LastError != nil:
		// An empty cause means "no new cause", not "forget the old one". A lens
		// already paused carries the diagnosis of whatever put it there, and a
		// second pause raised over it — an operator Pause on a structurally
		// paused lens, say — has no cause of its own to record.
		lastErrPtr = existing.LastError
	}
	entry := Entry{
		RuleID:         r.ruleID,
		Status:         StatusPaused,
		PauseReason:    &reason, // non-nil *string
		ActiveSequence: seq,
		ConsumerLag:    existing.ConsumerLag,
		ErrorCount:     existing.ErrorCount, // preserved
		LastError:      lastErrPtr,          // null when no error message; non-nil otherwise
		LastUpdated:    time.Now().UTC().Format(time.RFC3339),
		RuleEngine:     r.RuleEngine(),
		// Preserved for the same reason as in SetActive: sweep progress and the
		// cumulative fault counters are lens-lifetime state, not
		// status-transition state.
		SweepCursor:           existing.SweepCursor,
		SweepReconciled:       existing.SweepReconciled,
		AuditCursor:           existing.AuditCursor,
		AuditCycleCompletedAt: existing.AuditCycleCompletedAt,
		// The personal convergence sweep's state, preserved for the reason
		// SetActive states: it is written by a process-level walk fanned out
		// onto per-lens entries, and this transition observes nothing about it.
		PersonalSweepCursor:           existing.PersonalSweepCursor,
		PersonalSweepCycleCompletedAt: existing.PersonalSweepCycleCompletedAt,
		PersonalSweepQueueDepth:       existing.PersonalSweepQueueDepth,
		SecureRedactions:              existing.SecureRedactions,
		EvalDriftRetries:              existing.EvalDriftRetries,
		EvalDriftRequeues:             existing.EvalDriftRequeues,
		// The projection-progress fields, for the same reason and by the same
		// rule SetProjectionProgress states about itself: they are OBSERVATIONS,
		// and a status transition observes none of them, so writing zeroes here
		// asserts something no read established. The LagPoller restores the first
		// four within a cycle; LastProjectedAt it does not, because it only ever
		// writes a NON-ZERO value — so a SetActive at activation erases a lens's
		// last-projection timestamp permanently.
		ProjectionLag:      existing.ProjectionLag,
		LagProgressAt:      existing.LagProgressAt,
		AckPending:         existing.AckPending,
		AckFloorProgressAt: existing.AckFloorProgressAt,
		LastProjectedAt:    existing.LastProjectedAt,
		// The cost gauge, for the reason SetActive states: nothing restores it
		// after a transition, because samples come only from evaluations.
		PeakBindingRows: existing.PeakBindingRows,
		// The consumer-filter footprint, by the same rule: a status transition
		// observes nothing about which subjects this lens's consumer filters
		// on, so writing zeroes here would assert something no derivation
		// established. Dropping them is worse than a reset, because the ABSENT
		// state means "never derived a filter" — a lens that pauses would read
		// as one that has not yet decided. Nothing restores them within a
		// cycle either: only a re-derivation writes them, and a rebuild calls
		// SetRebuilding on the way in and SetActive on the way out, so an
		// omission here erases the footprint on the very operation that
		// re-derives it.
		FilterMode:        existing.FilterMode,
		FilterLabelCount:  existing.FilterLabelCount,
		FilterBroadReason: existing.FilterBroadReason,
		// The last probe-driven structural recovery, preserved for the reason
		// SetActive states: a status transition observes nothing about a
		// recovery that already happened, and these three are the only record it
		// leaves once the pause and its diagnosis are cleared.
		StructuralAutoRecoveredAt:      existing.StructuralAutoRecoveredAt,
		StructuralAutoRecoveredCause:   existing.StructuralAutoRecoveredCause,
		StructuralAutoRecoveryAttempts: existing.StructuralAutoRecoveryAttempts,
	}
	if err := r.put(ctx, entry); err != nil {
		return err
	}
	slog.Info("health: rule paused",
		"ruleId", r.ruleID,
		"pauseReason", reason, "lastError", lastError)
	return nil
}

// SetRebuilding writes a "rebuilding" health entry. Reads the existing entry to
// preserve ErrorCount and ConsumerLag. PauseReason and LastError are null —
// rebuilding is not a pause or error state. Status returns to "active" when
// consumer lag reaches zero after the rebuild rescan completes.
func (r *Reporter) SetRebuilding(ctx context.Context) error {
	r.mu.RLock()
	seq := r.activeSequence
	r.mu.RUnlock()

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		slog.Warn("health: SetRebuilding could not read existing entry — every carried-forward field (error/redaction/drift counters, sweep progress, projection progress) resets to its zero value",
			"ruleId", r.ruleID, "err", err)
		existing = Entry{}
	}
	entry := Entry{
		RuleID:         r.ruleID,
		Status:         StatusRebuilding,
		PauseReason:    nil, // JSON null — rebuilding is not a pause
		ActiveSequence: seq,
		ConsumerLag:    existing.ConsumerLag,
		ErrorCount:     existing.ErrorCount, // preserved
		LastError:      nil,                 // JSON null
		LastUpdated:    time.Now().UTC().Format(time.RFC3339),
		RuleEngine:     r.RuleEngine(),
		// Preserved for the same reason as in SetActive: sweep progress and the
		// cumulative fault counters are lens-lifetime state, not
		// status-transition state.
		SweepCursor:           existing.SweepCursor,
		SweepReconciled:       existing.SweepReconciled,
		AuditCursor:           existing.AuditCursor,
		AuditCycleCompletedAt: existing.AuditCycleCompletedAt,
		// The personal convergence sweep's state, preserved for the reason
		// SetActive states: it is written by a process-level walk fanned out
		// onto per-lens entries, and this transition observes nothing about it.
		PersonalSweepCursor:           existing.PersonalSweepCursor,
		PersonalSweepCycleCompletedAt: existing.PersonalSweepCycleCompletedAt,
		PersonalSweepQueueDepth:       existing.PersonalSweepQueueDepth,
		SecureRedactions:              existing.SecureRedactions,
		EvalDriftRetries:              existing.EvalDriftRetries,
		EvalDriftRequeues:             existing.EvalDriftRequeues,
		// The projection-progress fields, for the same reason and by the same
		// rule SetProjectionProgress states about itself: they are OBSERVATIONS,
		// and a status transition observes none of them, so writing zeroes here
		// asserts something no read established. The LagPoller restores the first
		// four within a cycle; LastProjectedAt it does not, because it only ever
		// writes a NON-ZERO value — so a SetActive at activation erases a lens's
		// last-projection timestamp permanently.
		ProjectionLag:      existing.ProjectionLag,
		LagProgressAt:      existing.LagProgressAt,
		AckPending:         existing.AckPending,
		AckFloorProgressAt: existing.AckFloorProgressAt,
		LastProjectedAt:    existing.LastProjectedAt,
		// The cost gauge, for the reason SetActive states: nothing restores it
		// after a transition, because samples come only from evaluations.
		PeakBindingRows: existing.PeakBindingRows,
		// The consumer-filter footprint, by the same rule: a status transition
		// observes nothing about which subjects this lens's consumer filters
		// on, so writing zeroes here would assert something no derivation
		// established. Dropping them is worse than a reset, because the ABSENT
		// state means "never derived a filter" — a lens that pauses would read
		// as one that has not yet decided. Nothing restores them within a
		// cycle either: only a re-derivation writes them, and a rebuild calls
		// SetRebuilding on the way in and SetActive on the way out, so an
		// omission here erases the footprint on the very operation that
		// re-derives it.
		FilterMode:        existing.FilterMode,
		FilterLabelCount:  existing.FilterLabelCount,
		FilterBroadReason: existing.FilterBroadReason,
		// The last probe-driven structural recovery, preserved for the reason
		// SetActive states: a status transition observes nothing about a
		// recovery that already happened, and these three are the only record it
		// leaves once the pause and its diagnosis are cleared. A rebuild is the
		// second place the omission would bite — SetRebuilding on the way in and
		// SetActive on the way out erase it twice over, on the very operation an
		// operator runs BECAUSE the recovery told them a rebuild was owed.
		StructuralAutoRecoveredAt:      existing.StructuralAutoRecoveredAt,
		StructuralAutoRecoveredCause:   existing.StructuralAutoRecoveredCause,
		StructuralAutoRecoveryAttempts: existing.StructuralAutoRecoveryAttempts,
	}
	if err := r.put(ctx, entry); err != nil {
		return err
	}
	slog.Info("health: rule rebuilding", "ruleId", r.ruleID)
	return nil
}

// RecordError increments ErrorCount and records the most recent error
// message — the lens's general-purpose fault counter, surfaced in
// `lattice health summary` / Lamplighter's Health KV read
// (docs/observability/health-kv-schema.md's per-lens reporter-status entry)
// without a status transition, so a lens that is still running correctly
// stays "active" rather than reading as paused. Callers: pipeline after each
// DLQ publish (terminal failure or retry exhaustion, per AC3), a refused
// hot-reload edit, and a narrowed Core KV consumer registration falling back
// to the broad filter. Thread-safe; serialized via writeMu to prevent
// lost-update races with concurrent callers.
func (r *Reporter) RecordError(ctx context.Context, errMsg string) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		return fmt.Errorf("health: RecordError read: %w", err)
	}
	existing.ErrorCount++
	existing.LastError = &errMsg
	existing.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	existing.RuleID = r.ruleID
	if writeErr := r.put(ctx, existing); writeErr != nil {
		return writeErr
	}
	slog.Info("health: error recorded",
		"ruleId", r.ruleID,
		"errorCount", existing.ErrorCount, "lastError", errMsg)
	return nil
}

// RecordGrantReprojectIssue records a fault in the D1 grant-change
// reprojection path for this lens: a coalescing dirty-actor set that overflowed
// its bound and dropped signals, or a per-actor reprojection that failed
// (personal-lens-grant-change-trigger-design.md §5).
//
// Both are the same operator-facing fact — the prompt path is no longer
// covering some set of actors, so the standing healer is the only thing
// converging them — and both have to be loud, because the degraded state is
// otherwise invisible: the drain simply does less, correctly, forever. kind
// names which (overflow, reproject); detail carries the actor or the count.
//
// Deliberately routed through the existing ErrorCount/LastError pair rather
// than a new wire field. Those two already have readers — `lattice health
// summary`, Loupe's fault rendering, Lamplighter's Health KV read — and a
// counter nobody reads is an issue nobody sees, which is the exact failure this
// method exists to prevent. Unlike SetFilterState, which is an observation of a
// correct decision, this is a fault and belongs in the fault bucket.
func (r *Reporter) RecordGrantReprojectIssue(ctx context.Context, kind, detail string) error {
	return r.RecordError(ctx, fmt.Sprintf("grant-change reprojection: %s: %s", kind, detail))
}

// SetFilterState records which Core KV consumer filter this lens's derivation
// chose — the footprint triple FilterMode / FilterLabelCount /
// FilterBroadReason. It is an OBSERVATION of a decision the pipeline has
// already made: nothing here influences which subjects the lens filters on.
//
// Deliberately NOT a RecordError. A lens falling back to the broad filter is
// projecting every row it should, only reading more of the stream than it
// needs — a footprint regression, not a fault — so routing it through
// errorCount/lastError would put a correct lens in the same bucket as a DLQ
// write. The one broad reason that IS a fault (registration-failed) keeps its
// own separate RecordError call at the site that decides it.
//
// Written wholesale, never merged: the three fields are one decision, and a
// later derivation replaces all three. Callers pass labelCount 0 and a
// non-empty reason for broad, and a positive labelCount with reason "" for
// either narrowed mode. Thread-safe; serialized via writeMu against the other
// read-modify-write callers.
func (r *Reporter) SetFilterState(ctx context.Context, mode string, labelCount int, broadReason string) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		return fmt.Errorf("health: SetFilterState read: %w", err)
	}
	existing.FilterMode = mode
	existing.FilterLabelCount = labelCount
	existing.FilterBroadReason = broadReason
	existing.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	existing.RuleID = r.ruleID
	if writeErr := r.put(ctx, existing); writeErr != nil {
		return writeErr
	}
	slog.Info("health: consumer filter state recorded",
		"ruleId", r.ruleID,
		"filterMode", mode, "filterLabelCount", labelCount, "filterBroadReason", broadReason)
	return nil
}

// ClearLastError nils LastError while preserving ErrorCount and every other
// field on the entry — Status and PauseReason included, which this method
// never writes. Its one caller is pipeline.registerWithFilterFallback: a
// lens's Core KV consumer (re)registration that succeeds cleanly (no
// narrowed-to-broad fallback) is this process proving the lens healthy right
// now, while a LastError an earlier boot's fallback recorded has no other
// retirement path — a restart alone does not touch health KV, and RecordError
// only ever appends — so without this call the message outlives every process
// that could still explain it and keeps the lens rendering "fault"
// (cmd/loupe/renderedstate.go's fault conjunct requires a live LastError, not
// just a nonzero cumulative ErrorCount) long after the condition is gone.
//
// A structurally paused lens is the one exception, and the one status this
// method does read for a decision: there LastError is not a stale artifact but
// the pause's diagnosis — the whole of what an operator has to work from,
// since a structural pause is held until a human reconciles it. Registration
// succeeds regardless of the pause (supervisor.Add creates the durable and
// spawns the pump; the pause lives in the pump), so without this guard every
// Pipeline.Run and every Rebuild would erase the cause of a pause it did
// nothing to resolve. Infra and manual pauses are not exempted: an infra pause
// at activation is the "not yet provisioned" state its probe resolves on its
// own, and a manual pause never carries a cause of its own, so for both the
// only thing LastError can hold is exactly the stale fallback message this
// method exists to retire.
//
// Deliberately narrower than SetActive, which also writes Status: "active"
// and PauseReason: nil unconditionally — exactly the pair
// substrate.ConsumerSupervisor's restoreState reads at startup, from the pump
// goroutine Add spawns concurrently with registerWithFilterFallback's caller,
// to decide whether to honor a persisted pause. A durable's registration can
// succeed while the lens is manually or structurally paused, so a setter that
// touches Status/PauseReason here would race that restore and risk
// clobbering the pause open; nil-ing only LastError cannot, whichever
// goroutine runs first. Thread-safe, serialized via writeMu like every other
// setter. A no-op (no KV write) when LastError is already nil.
func (r *Reporter) ClearLastError(ctx context.Context) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		return fmt.Errorf("health: ClearLastError read: %w", err)
	}
	if existing.PauseReason != nil && *existing.PauseReason == PauseReasonStructural {
		return nil
	}
	if existing.LastError == nil {
		return nil
	}
	existing.LastError = nil
	existing.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	existing.RuleID = r.ruleID
	if err := r.put(ctx, existing); err != nil {
		return err
	}
	slog.Info("health: stale error cleared", "ruleId", r.ruleID, "errorCount", existing.ErrorCount)
	return nil
}

// RecordEvalDriftRetry increments EvalDriftRetries. Called once per inline
// footprint-validation re-execution attempt an auth-plane evaluation's
// drifted read surface triggers (refractor-evaluation-consistency-design.md
// §4.6). Thread-safe; serialized via writeMu to prevent lost-update races
// with concurrent RecordError/RecordEvalDriftRequeue calls.
func (r *Reporter) RecordEvalDriftRetry(ctx context.Context) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		return fmt.Errorf("health: RecordEvalDriftRetry read: %w", err)
	}
	existing.EvalDriftRetries++
	existing.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	existing.RuleID = r.ruleID
	return r.put(ctx, existing)
}

// RecordEvalDriftRequeue increments EvalDriftRequeues. Called once when an
// auth-plane evaluation's read surface still diverges after the inline
// re-execution and the pipeline requeues it as failure.ErrEvalDrift instead
// of landing a possibly-torn row. Thread-safe; serialized via writeMu.
func (r *Reporter) RecordEvalDriftRequeue(ctx context.Context) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		return fmt.Errorf("health: RecordEvalDriftRequeue read: %w", err)
	}
	existing.EvalDriftRequeues++
	existing.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	existing.RuleID = r.ruleID
	return r.put(ctx, existing)
}

// RecordSecureRedactions adds n to SecureRedactions — the count of secure
// columns an evaluation projected as null because it could not resolve them
// (retention-class-key-custody-design.md §6.2, fork F2). Takes a COUNT rather
// than incrementing by one because a single evaluation redacts per column per
// row: a lens-wide misdeclaration would otherwise turn one event into a KV
// write storm. n == 0 is a no-op, so callers can call it unconditionally.
// Thread-safe; serialized via writeMu against the other counter writers.
func (r *Reporter) RecordSecureRedactions(ctx context.Context, n uint64) error {
	if n == 0 {
		return nil
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		return fmt.Errorf("health: RecordSecureRedactions read: %w", err)
	}
	existing.SecureRedactions += n
	existing.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	existing.RuleID = r.ruleID
	return r.put(ctx, existing)
}

// RecordStructuralAutoRecovery stamps a structural pause that cleared with no
// operator involved: the consumer's own probe re-verified the condition the
// pause was raised on, every remaining gate passed, and the lens is about to
// project again. cause is the diagnosis the pause carried; attempts is which
// self-heal attempt lifted it, from 1.
//
// It exists because the recovery is otherwise INVISIBLE. The entry it writes
// reads `active`, exactly like a lens that never faulted and like one a human
// repaired, and the pause's cause is gone from LastError the moment the pause
// clears — so without these three fields the only trace of a lens having been
// dark, and of what was wrong with it, is a log line nobody is reading. The
// pause's own backlog does replay on resume — the failing message was never
// acked — but a condition cleared by re-provisioning or restoring the target
// leaves everything written BEFORE the pause unreplayable, and only the cause
// says which of the two happened.
//
// It records the recovery only — Status and PauseReason are the supervisor's to
// write, and are already correct by the time this is called. Read-modify-write
// through put under writeMu like every other setter, so it cannot lose an
// update to a concurrent RecordError.
func (r *Reporter) RecordStructuralAutoRecovery(ctx context.Context, cause string, attempts int) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		return fmt.Errorf("health: RecordStructuralAutoRecovery read: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	existing.StructuralAutoRecoveredAt = now
	existing.StructuralAutoRecoveredCause = cause
	existing.StructuralAutoRecoveryAttempts = attempts
	existing.LastUpdated = now
	existing.RuleID = r.ruleID
	if err := r.put(ctx, existing); err != nil {
		return err
	}
	slog.Info("health: structural pause recovered without operator action",
		"ruleId", r.ruleID, "attempts", attempts, "cause", cause)
	return nil
}

// SetPeakBindingRows persists the lens's peak-binding-rows gauge — the largest
// binding set its recent evaluations materialized, over the pipeline's rolling
// observation window (pipeline.PeakRowsRingBuffer). Read-modify-write under the
// same writeMu as every other setter, so it cannot lose an update to a
// concurrent RecordError.
//
// It takes the window's CURRENT maximum rather than one evaluation's peak, and
// therefore overwrites: the value is allowed to fall as a spike ages out, which
// is the whole point of a gauge over a counter. Callers publish it on a poll
// cycle, not per evaluation — a per-evaluation write would turn every event
// into a health KV write.
//
// A caller with no sample must not call this at all: writing a fabricated zero
// over a real prior observation would erase the one number an operator
// diagnosing a refusal is looking for.
func (r *Reporter) SetPeakBindingRows(ctx context.Context, rows uint64) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		return fmt.Errorf("health: SetPeakBindingRows read: %w", err)
	}
	existing.PeakBindingRows = rows
	existing.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	existing.RuleID = r.ruleID
	return r.put(ctx, existing)
}

// Delete removes the health KV entry for this rule (FR39 — rule deletion cleanup).
// After Delete, subsequent GetStatus calls return the default active zero Entry
// (ErrKeyNotFound path in readExisting). Safe to call when no entry exists —
// substrate.ErrKeyNotFound is silently ignored.
func (r *Reporter) Delete(ctx context.Context) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if err := r.kv.Delete(ctx, r.ruleID); err != nil && !errors.Is(err, substrate.ErrKeyNotFound) {
		return fmt.Errorf("health: delete entry %s: %w", r.ruleID, err)
	}
	slog.Info("health: rule deleted", "ruleId", r.ruleID)
	return nil
}

// SetConsumerLag reads the existing entry, updates ConsumerLag, and writes.
// Used by Story 4.2's lag metric publisher to keep the health entry current.
// Thread-safe; serialized via writeMu to prevent lost-update races with concurrent RecordError calls.
func (r *Reporter) SetConsumerLag(ctx context.Context, lag uint64) error {
	r.mu.RLock()
	seq := r.activeSequence
	r.mu.RUnlock()

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		return fmt.Errorf("health: SetConsumerLag read: %w", err)
	}
	existing.ConsumerLag = lag
	existing.ActiveSequence = seq
	existing.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	existing.RuleID = r.ruleID
	return r.put(ctx, existing)
}

// SetProjectionProgress reads the existing entry, updates ConsumerLag/
// ProjectionLag (the same NumPending value under both the legacy and
// operator-facing names), LagProgressAt (when non-zero), and, when
// lastProjectedAt is non-zero, LastProjectedAt, then writes
// (lens-projection-liveness-design.md §3.2). Called by the LagPoller on its
// existing 5s cycle in place of SetConsumerLag — one read-modify-write, no
// new goroutine. A zero lastProjectedAt (no projection yet this process)
// leaves the existing stored value untouched rather than blanking it; a zero
// lagProgressAt does the same (the LagPoller stamps it from its first poll
// onward, so zero here only happens before that poller has run at all).
//
// AckPending and AckFloorProgressAt land together, gated on a non-zero
// ackFloorProgressAt: they are the pair that separates a consumer that is
// caught up from one that has been handed everything and cannot finish it, and
// half the pair is worse than neither.
func (r *Reporter) SetProjectionProgress(ctx context.Context, lag uint64, lastProjectedAt, lagProgressAt time.Time, ackPending uint64, ackFloorProgressAt time.Time) error {
	r.mu.RLock()
	seq := r.activeSequence
	r.mu.RUnlock()

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		return fmt.Errorf("health: SetProjectionProgress read: %w", err)
	}
	existing.ConsumerLag = lag
	existing.ProjectionLag = lag
	if !lastProjectedAt.IsZero() {
		existing.LastProjectedAt = lastProjectedAt.UTC().Format(time.RFC3339)
	}
	if !lagProgressAt.IsZero() {
		existing.LagProgressAt = lagProgressAt.UTC().Format(time.RFC3339)
	}
	// A zero ackFloorProgressAt means the poller has no ack-stats source or its
	// read failed this cycle. Leave BOTH fields alone in that case: writing
	// ackPending=0 over a real nonzero observation would erase the one signal
	// that separates a wedged consumer from a drained one.
	if !ackFloorProgressAt.IsZero() {
		existing.AckPending = ackPending
		existing.AckFloorProgressAt = ackFloorProgressAt.UTC().Format(time.RFC3339)
	}
	existing.ActiveSequence = seq
	existing.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	existing.RuleID = r.ruleID
	return r.put(ctx, existing)
}

// SetSweepProgress persists the auth-plane convergence sweep's round-robin
// cursor and cumulative heal count onto the lens's existing health entry
// (capability-projection-reconciliation-design.md §3.2). Read-modify-write under
// the same writeMu as every other setter, so it cannot lose an update to a
// concurrent RecordError. Called once per sweep tick — the sweep's only health
// write.
func (r *Reporter) SetSweepProgress(ctx context.Context, cursor string, reconciled uint64) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		return fmt.Errorf("health: SetSweepProgress read: %w", err)
	}
	existing.SweepCursor = cursor
	existing.SweepReconciled = reconciled
	existing.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	existing.RuleID = r.ruleID
	return r.put(ctx, existing)
}

// SetAuditProgress persists the plain-lens divergence audit's round-robin
// cursor and the time its walk last completed a full cycle onto the lens's
// existing health entry (lens-projection-divergence-audit-design.md §4.3).
// Read-modify-write under the same writeMu as every other setter, so it cannot
// lose an update to a concurrent RecordError. Called once per audit pass — and
// it is the audit's ONLY write anywhere: nothing in the audit path touches the
// lens's target, and nothing touches Core KV.
//
// A zero cycleCompletedAt leaves the stored value alone rather than clearing
// it. The audit stamps that field only when a walk reaches the END of the anchor
// listing, so writing a zero on every intermediate pass would erase the one
// field that says what a clean verdict covers — the same rule
// SetProjectionProgress states about its own timestamps.
func (r *Reporter) SetAuditProgress(ctx context.Context, cursor string, cycleCompletedAt time.Time) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		return fmt.Errorf("health: SetAuditProgress read: %w", err)
	}
	existing.AuditCursor = cursor
	if !cycleCompletedAt.IsZero() {
		existing.AuditCycleCompletedAt = cycleCompletedAt.UTC().Format(time.RFC3339)
	}
	existing.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	existing.RuleID = r.ruleID
	return r.put(ctx, existing)
}

// SetPersonalSweepProgress persists the personal convergence sweep's
// round-robin cursor, the time its walk last closed a full cycle, and the
// grant-change drain's queue depth onto the lens's existing health entry
// (personal-lens-grant-change-trigger-design.md §4.3). Read-modify-write under
// the same writeMu as every other setter, so it cannot lose an update to a
// concurrent RecordError.
//
// Unlike SetSweepProgress and SetAuditProgress this is written by ONE
// process-level walk fanned out across every personal lens, not by a per-lens
// mechanism. It is the same fan-out RecordGrantReprojectIssue takes and for the
// same reason: the mechanism is shared, the fact is per-lens.
//
// A zero cycleCompletedAt leaves the stored value alone rather than clearing
// it, exactly as SetAuditProgress does: the sweep stamps that field only on the
// tick that reaches the END of the identity population, so writing a zero on
// every intermediate tick would erase the one field that says what the walk has
// actually covered. queueDepth always overwrites — it is a live gauge, not a
// milestone, and a stale depth is worse than a zero one.
func (r *Reporter) SetPersonalSweepProgress(ctx context.Context, cursor string, cycleCompletedAt time.Time, queueDepth uint64) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	existing, err := r.readExisting(ctx)
	if err != nil {
		return fmt.Errorf("health: SetPersonalSweepProgress read: %w", err)
	}
	existing.PersonalSweepCursor = cursor
	if !cycleCompletedAt.IsZero() {
		existing.PersonalSweepCycleCompletedAt = cycleCompletedAt.UTC().Format(time.RFC3339)
	}
	existing.PersonalSweepQueueDepth = queueDepth
	existing.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	existing.RuleID = r.ruleID
	return r.put(ctx, existing)
}

// GetStatus reads the current health entry for this rule.
// If no entry exists (ErrKeyNotFound), returns a zero Entry with status "active" and nil error —
// absence of a health entry is treated as active (new rule, never written).
func (r *Reporter) GetStatus(ctx context.Context) (Entry, error) {
	return r.readExisting(ctx)
}

// readExisting reads and unmarshals the current health KV entry for this rule.
// Returns a zero Entry (status="active", ruleId set) on ErrKeyNotFound.
// Returns an error only for unexpected read or unmarshal failures.
func (r *Reporter) readExisting(ctx context.Context) (Entry, error) {
	e, err := r.kv.Get(ctx, r.ruleID)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return Entry{Status: StatusActive, RuleID: r.ruleID}, nil
		}
		return Entry{}, fmt.Errorf("health: read existing %s: %w", r.ruleID, err)
	}
	var entry Entry
	if err := json.Unmarshal(e.Value, &entry); err != nil {
		return Entry{}, fmt.Errorf("health: unmarshal entry %s: %w", r.ruleID, err)
	}
	return entry, nil
}

func (r *Reporter) put(ctx context.Context, entry Entry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("health: marshal entry: %w", err)
	}
	if _, err := r.kv.Put(ctx, r.ruleID, data); err != nil {
		return fmt.Errorf("health: put entry %s: %w", r.ruleID, err)
	}
	return nil
}
