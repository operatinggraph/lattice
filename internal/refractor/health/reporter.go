package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// PauseReason values used in health KV entries.
// Defined here so callers (pipeline) and tests share the same constants without a circular import.
const (
	PauseReasonInfra      = "infra"
	PauseReasonStructural = "structural"
	PauseReasonManual     = "manual"
)

// Entry is the full health KV value schema. All field names are camelCase per architecture convention.
// The KV key is the ruleID; the KV bucket is configured via config.HealthKVBucket.
//
// PauseReason and LastError are *string so they marshal as JSON null when inactive,
// satisfying the FR21 requirement for null (not empty string) in active entries.
type Entry struct {
	RuleID         string  `json:"ruleId"`
	Status         string  `json:"status"`         // "active" | "paused" | "rebuilding"
	PauseReason    *string `json:"pauseReason"`    // null when active; "infra", "structural", or "manual" when paused
	ActiveSequence uint64  `json:"activeSequence"` // NATS sequence of the active rule version
	ConsumerLag    uint64  `json:"consumerLag"`    // current consumer lag; updated by Story 4.2
	ErrorCount     uint64  `json:"errorCount"`     // cumulative DLQ writes; preserved across restarts
	LastError      *string `json:"lastError"`      // null when no error; non-nil with latest error message
	LastUpdated    string  `json:"lastUpdated"`    // RFC3339 UTC
	// RuleEngine is the engine name that successfully parsed this rule's match
	// body (Story 3.1a). Cached via SetRuleEngine and re-emitted on every
	// status transition. Empty string when not yet set (forward-compat).
	RuleEngine string `json:"ruleEngine,omitempty"`
	// LastProjectedAt is the wall-clock of the last successful target write
	// (lens-projection-liveness-design.md §3.2) — RFC3339 UTC; "" until the
	// lens's first projection. A freshness signal, never an alert input on its
	// own (a quiet, no-match lens naturally has an old value).
	LastProjectedAt string `json:"lastProjectedAt,omitempty"`
	// ProjectionLag is the operator-facing alias of ConsumerLag (same NumPending
	// value, named for what it means to an operator: events behind).
	ProjectionLag uint64 `json:"projectionLag"`
}

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
		Status:         "active",
		PauseReason:    nil, // JSON null
		ActiveSequence: seq,
		ConsumerLag:    existing.ConsumerLag,
		ErrorCount:     existing.ErrorCount, // preserved across restarts
		LastError:      nil,                 // JSON null
		LastUpdated:    time.Now().UTC().Format(time.RFC3339),
		RuleEngine:     eng,
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
	if lastError != "" {
		lastErrPtr = &lastError
	}
	entry := Entry{
		RuleID:         r.ruleID,
		Status:         "paused",
		PauseReason:    &reason, // non-nil *string
		ActiveSequence: seq,
		ConsumerLag:    existing.ConsumerLag,
		ErrorCount:     existing.ErrorCount, // preserved
		LastError:      lastErrPtr,          // null when no error message; non-nil otherwise
		LastUpdated:    time.Now().UTC().Format(time.RFC3339),
		RuleEngine:     r.RuleEngine(),
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
		Status:         "rebuilding",
		PauseReason:    nil, // JSON null — rebuilding is not a pause
		ActiveSequence: seq,
		ConsumerLag:    existing.ConsumerLag,
		ErrorCount:     existing.ErrorCount, // preserved
		LastError:      nil,                 // JSON null
		LastUpdated:    time.Now().UTC().Format(time.RFC3339),
		RuleEngine:     r.RuleEngine(),
	}
	if err := r.put(ctx, entry); err != nil {
		return err
	}
	slog.Info("health: rule rebuilding", "ruleId", r.ruleID)
	return nil
}

// RecordError increments ErrorCount and records the most recent error message.
// Called by pipeline after each DLQ publish (terminal failure or retry exhaustion) per AC3.
// Thread-safe; serialized via writeMu to prevent lost-update races with concurrent DLQ writes.
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
// operator-facing names) and, when lastProjectedAt is non-zero, LastProjectedAt,
// then writes (lens-projection-liveness-design.md §3.2). Called by the
// LagPoller on its existing 5s cycle in place of SetConsumerLag — one
// read-modify-write, no new goroutine. A zero lastProjectedAt (no projection
// yet this process) leaves the existing stored value untouched rather than
// blanking it.
func (r *Reporter) SetProjectionProgress(ctx context.Context, lag uint64, lastProjectedAt time.Time) error {
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
	existing.ActiveSequence = seq
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
			return Entry{Status: "active", RuleID: r.ruleID}, nil
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
