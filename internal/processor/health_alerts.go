// Health KV alert emitter for security-sensitive Processor events:
//
//   - `stub-auth-active` — emitted at Processor startup AND every Nth
//     Authorize call when AuthModeStub is selected
//   - `auth-freshness-exceeded` — emitted alongside the AuthFreshnessExceeded
//     denial when a Capability KV projection is staler than 5×NFR-P3
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
	case "auth-freshness-exceeded":
		return "error"
	case "stub-auth-active":
		// Stub mode in production is a security risk; "warning" is the
		// least-noisy enum that still surfaces in dashboards.
		return "warning"
	default:
		return "warning"
	}
}
