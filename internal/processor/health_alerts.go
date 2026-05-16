// Story 3.3 — Health KV alert emitter for security-sensitive Processor
// events. Two alerts ship in 3.3:
//
//   - `stub-auth-active` — emitted at Processor startup AND every Nth
//     Authorize call when AuthModeStub is selected (Decision #7)
//   - `auth-freshness-exceeded` — emitted alongside the AuthFreshnessExceeded
//     denial when a Capability KV projection is staler than 5×NFR-P3
//
// Key shape (Contract #5 alert convention):
//   health.alerts.security.<alertCode>
//
// Multiple bursts of the same alert OVERWRITE the previous entry — the
// alert is "this is currently happening" not an audit log. Per-alert
// audit history is Stream 7 territory (Phase 2+).
package processor

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// HealthAlertEmitter writes Contract #5 alert entries to Health KV.
type HealthAlertEmitter struct {
	conn   *substrate.Conn
	bucket string
	logger *slog.Logger
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
