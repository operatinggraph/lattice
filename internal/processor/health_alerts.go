// Health KV alert emitter for security-sensitive Processor events:
//
//   - `stub-auth-active` — emitted at Processor startup AND every Nth
//     Authorize call when AuthModeStub is selected
//
// Key shape (Contract #5 alert convention): health.alerts.security.<alertCode>
//
// Multiple bursts of the same alert OVERWRITE the previous entry — the alert
// represents "this is currently happening", not an audit log.
package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// ClaimAttemptEmitter surfaces ClaimIdentity outcomes to Health KV at
// health.processor.<instance>.claim-attempts.<outcome>. Separate from the
// security-alert emitter because claim attempt counters are operational metrics,
// not security alerts.
//
// Outcome enum: success, invalid-key, wrong-state, flagged, merged,
// credential-already-bound, no-target.
type ClaimAttemptEmitter interface {
	RecordClaimAttempt(ctx context.Context, outcome string)
}

// noopClaimAttemptEmitter is a no-op implementation for tests or modes that
// don't wire the health KV.
type noopClaimAttemptEmitter struct{}

func (noopClaimAttemptEmitter) RecordClaimAttempt(_ context.Context, _ string) {}

// CommitConflictInfo is the context the Processor surfaces about a same-key
// commit conflict so it can be investigated as a lane-misassignment signal: two
// writers raced the SAME Core KV key, which normally means the conflicting op
// was published on the wrong lane (legitimate only occasionally, e.g. a
// deliberate urgent-lane write). It feeds the forward Weaver/Loom lane-routing
// improvement loop (the engines learn to co-route / serialise same-key writers).
type CommitConflictInfo struct {
	ConflictingKey string // the Core KV key whose revision condition failed
	Lane           string // the lane this op was published on
	OperationType  string // this op's operationType
	Exhausted      bool   // true when retries were exhausted and the op surfaced RevisionConflict
}

// CommitConflictEmitter surfaces same-key update conflicts to Health KV at
// health.processor.<instance>.commit-conflicts. Distinct from the security-alert
// emitter (these are operational lane-misassignment signals, not security
// alerts) and from the per-tick heartbeat counters (commit_retries_total /
// commit_retry_exhausted_total) — this carries the actionable per-conflict
// context (the racing key + lane + op-kind) the counters cannot.
type CommitConflictEmitter interface {
	RecordCommitConflict(ctx context.Context, info CommitConflictInfo)
}

// noopCommitConflictEmitter is a no-op for tests or modes that don't wire the
// health KV.
type noopCommitConflictEmitter struct{}

func (noopCommitConflictEmitter) RecordCommitConflict(_ context.Context, _ CommitConflictInfo) {}

// NewCommitConflictEmitter constructs a CommitConflictEmitter wired to Health
// KV. Returns a noop when conn is nil or bucket/instance are empty.
func NewCommitConflictEmitter(conn *substrate.Conn, bucket, instance string, logger *slog.Logger) CommitConflictEmitter {
	if conn == nil || bucket == "" || instance == "" {
		return noopCommitConflictEmitter{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &HealthAlertEmitter{conn: conn, bucket: bucket, instance: instance, logger: logger}
}

// RecordCommitConflict implements CommitConflictEmitter. Writes a best-effort
// rolling record to health.processor.<instance>.commit-conflicts with the
// most-recent conflict context plus a cumulative count, so the condition is
// operator-visible (Lamplighter-readable) rather than silent. The record is
// "this is currently happening" (last writer wins), not an audit log — bounded
// to a single key, never one-per-racing-key (that would be unbounded
// cardinality). The `count` is a non-atomic read-modify-write (concurrent
// recorders on the same instance can lose an increment — Phase-1 acceptable, the
// same posture as claim-attempts); the AUTHORITATIVE totals are the atomic
// heartbeat counters commit_retries_total / commit_retry_exhausted_total. This
// marker carries the actionable context (which key, which lane) the counters
// cannot.
func (e *HealthAlertEmitter) RecordCommitConflict(ctx context.Context, info CommitConflictInfo) {
	if e.instance == "" {
		return
	}
	key := fmt.Sprintf("health.processor.%s.commit-conflicts", e.instance)

	var currentCount int64
	if entry, err := e.conn.KVGet(ctx, e.bucket, key); err == nil {
		var existing map[string]any
		if json.Unmarshal(entry.Value, &existing) == nil {
			if c, ok := existing["count"].(float64); ok {
				currentCount = int64(c)
			}
		}
	}
	currentCount++

	body := map[string]any{
		"key":            key,
		"instance":       e.instance,
		"count":          currentCount,
		"conflictingKey": info.ConflictingKey,
		"lane":           info.Lane,
		"operationType":  info.OperationType,
		"exhausted":      info.Exhausted,
		"lastAt":         substrate.FormatTimestamp(time.Now()),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		e.logger.Warn("commit-conflicts: marshal failed", "key", key, "error", err)
		return
	}
	if _, err := e.conn.KVPut(ctx, e.bucket, key, raw); err != nil {
		e.logger.Warn("commit-conflicts: KV write failed", "key", key, "error", err)
	}
}

// HealthAlertEmitter writes Contract #5 alert entries to Health KV.
type HealthAlertEmitter struct {
	conn     *substrate.Conn
	bucket   string
	instance string
	logger   *slog.Logger
}

// NewHealthAlertEmitter constructs the emitter. Nil conn returns a noop
// — useful in unit tests that don't wire a substrate connection.
func NewHealthAlertEmitter(conn *substrate.Conn, bucket string, logger *slog.Logger) AuthAlertEmitter {
	if conn == nil || bucket == "" {
		return noopAlertEmitter{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &HealthAlertEmitter{conn: conn, bucket: bucket, logger: logger}
}

// NewClaimAttemptEmitter constructs a ClaimAttemptEmitter wired to Health KV.
// Returns a noop when conn is nil or bucket/instance are empty.
func NewClaimAttemptEmitter(conn *substrate.Conn, bucket, instance string, logger *slog.Logger) ClaimAttemptEmitter {
	if conn == nil || bucket == "" || instance == "" {
		return noopClaimAttemptEmitter{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &HealthAlertEmitter{conn: conn, bucket: bucket, instance: instance, logger: logger}
}

// RecordClaimAttempt implements ClaimAttemptEmitter. Writes a best-effort
// counter to health.processor.<instance>.claim-attempts.<outcome> with shape
// {count: N, lastAt: <RFC3339>}. Counter is read-modify-write; under
// contention last writer wins (Phase 1 acceptable; precise counting is Phase 2).
func (e *HealthAlertEmitter) RecordClaimAttempt(ctx context.Context, outcome string) {
	if e.instance == "" {
		return
	}
	key := fmt.Sprintf("health.processor.%s.claim-attempts.%s", e.instance, outcome)

	// Read existing count.
	var currentCount int64
	if entry, err := e.conn.KVGet(ctx, e.bucket, key); err == nil {
		var existing map[string]any
		if json.Unmarshal(entry.Value, &existing) == nil {
			if c, ok := existing["count"].(float64); ok {
				currentCount = int64(c)
			}
		}
	}
	currentCount++

	body := map[string]any{
		"key":      key,
		"instance": e.instance,
		"outcome":  outcome,
		"count":    currentCount,
		"lastAt":   substrate.FormatTimestamp(time.Now()),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		e.logger.Warn("claim-attempts: marshal failed", "outcome", outcome, "error", err)
		return
	}
	if _, err := e.conn.KVPut(ctx, e.bucket, key, raw); err != nil {
		e.logger.Warn("claim-attempts: KV write failed", "outcome", outcome, "key", key, "error", err)
	}
}

// EmitAlert implements AuthAlertEmitter.
func (e *HealthAlertEmitter) EmitAlert(ctx context.Context, code string, details map[string]any) {
	key := "health.alerts.security." + code
	body := map[string]any{
		"key":        key,
		"alertCode":  code,
		"severity":   alertSeverity(code),
		"observedAt": substrate.FormatTimestamp(time.Now()),
		"details":    details,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		e.logger.Warn("alert: marshal failed", "code", code, "error", err)
		return
	}
	if _, err := e.conn.KVPut(ctx, e.bucket, key, raw); err != nil {
		e.logger.Warn("alert: KV write failed", "code", code, "key", key, "error", err)
	}
}

// alertSeverity returns the Contract #5 severity for known alert codes.
// Unknown codes fall back to "warning".
func alertSeverity(code string) string {
	switch code {
	case "stub-auth-active":
		// Stub mode in production is a security risk; "warning" is the
		// least-noisy enum that still surfaces in dashboards.
		return "warning"
	default:
		return "warning"
	}
}
