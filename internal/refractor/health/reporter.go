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
		slog.Warn("health: SetActive could not read existing entry, ErrorCount/ConsumerLag reset to 0",
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
		slog.Warn("health: SetPaused could not read existing entry, ErrorCount/ConsumerLag reset to 0",
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
		SweepCursor:       existing.SweepCursor,
		SweepReconciled:   existing.SweepReconciled,
		SecureRedactions:  existing.SecureRedactions,
		EvalDriftRetries:  existing.EvalDriftRetries,
		EvalDriftRequeues: existing.EvalDriftRequeues,
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
		slog.Warn("health: SetRebuilding could not read existing entry, ErrorCount/ConsumerLag reset to 0",
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
		SweepCursor:       existing.SweepCursor,
		SweepReconciled:   existing.SweepReconciled,
		SecureRedactions:  existing.SecureRedactions,
		EvalDriftRetries:  existing.EvalDriftRetries,
		EvalDriftRequeues: existing.EvalDriftRequeues,
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
