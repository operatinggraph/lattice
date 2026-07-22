package controlauth

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// AuthAlertEmitter is the minimal Health KV alert surface. Implementations
// write to `health.alerts.security.<code>` per Contract #5's alert
// convention — the same shape internal/processor's HealthAlertEmitter
// produces for `stub-auth-active`, so a dashboard reads both with one query.
type AuthAlertEmitter interface {
	EmitAlert(ctx context.Context, code string, details map[string]any)
}

// noopAlertEmitter is the default when a caller doesn't wire an emitter.
type noopAlertEmitter struct{}

func (noopAlertEmitter) EmitAlert(context.Context, string, map[string]any) {}

// kvPutter is the minimal NATS KV write surface EmitAlert needs.
type kvPutter interface {
	KVPut(ctx context.Context, bucket, key string, value []byte) (uint64, error)
}

// HealthAlertEmitter writes `health.alerts.security.<code>` to the health-kv
// bucket. Mirrors internal/processor's HealthAlertEmitter so the two engines'
// alert entries are indistinguishable in shape to an operator dashboard.
type HealthAlertEmitter struct {
	conn   kvPutter
	bucket string
	logger *slog.Logger
}

// NewHealthAlertEmitter constructs a HealthAlertEmitter. Nil logger uses
// slog.Default().
func NewHealthAlertEmitter(conn kvPutter, bucket string, logger *slog.Logger) *HealthAlertEmitter {
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
		"severity":   "warning",
		"observedAt": substrate.FormatTimestamp(time.Now()),
		"details":    details,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		e.logger.Warn("controlauth: alert marshal failed", "code", code, "error", err)
		return
	}
	if _, err := e.conn.KVPut(ctx, e.bucket, key, raw); err != nil {
		e.logger.Warn("controlauth: alert KV write failed", "code", code, "key", key, "error", err)
	}
}
