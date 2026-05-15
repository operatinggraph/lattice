// Package health: Lattice-side heartbeater per Contract #5 §5.2.
// Emits health.refractor.<instance> and per-lens lag every 10s.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// LatticeHealthDoc mirrors Contract #5 §5.2 (same shape as Processor).
type LatticeHealthDoc struct {
	Key         string         `json:"key"`
	Component   string         `json:"component"`
	Instance    string         `json:"instance"`
	Version     string         `json:"version"`
	Status      string         `json:"status"`
	HeartbeatAt string         `json:"heartbeatAt"`
	StartedAt   string         `json:"startedAt"`
	Uptime      string         `json:"uptime"`
	Metrics     map[string]any `json:"metrics"`
	Issues      []any          `json:"issues"`
}

// LatticeHeartbeater periodically writes the Refractor instance's
// heartbeat to Health KV at `health.refractor.<instance>`. NFR-O1
// floor: 10s interval.
type LatticeHeartbeater struct {
	conn      *substrate.Conn
	bucket    string
	instance  string
	startedAt time.Time
	interval  time.Duration
	logger    *slog.Logger

	// LagProvider optionally returns per-lens lag (stream_last_seq -
	// consumer_acked_seq) values for inclusion in the heartbeat metrics.
	// May be nil before any lens is active.
	LagProvider func() map[string]uint64
}

// NewLatticeHeartbeater wires the heartbeater. instance must be stable
// across the lifetime of the process (Contract #5 §5.1 convention:
// rfx-<NanoID>).
func NewLatticeHeartbeater(conn *substrate.Conn, bucket, instance string, interval time.Duration, logger *slog.Logger) *LatticeHeartbeater {
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &LatticeHeartbeater{
		conn:      conn,
		bucket:    bucket,
		instance:  instance,
		startedAt: time.Now(),
		interval:  interval,
		logger:    logger,
	}
}

// Run blocks until ctx is cancelled, emitting heartbeats on a ticker.
// One initial heartbeat fires immediately so observers see a fresh
// document within 10s of startup (AC #6).
func (h *LatticeHeartbeater) Run(ctx context.Context) {
	h.emit(ctx, "starting")
	t := time.NewTicker(h.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			detached, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			h.emit(detached, "shutdown")
			cancel()
			return
		case <-t.C:
			h.emit(ctx, "healthy")
		}
	}
}

func (h *LatticeHeartbeater) emit(ctx context.Context, status string) {
	now := time.Now()
	metrics := map[string]any{}
	if h.LagProvider != nil {
		lags := h.LagProvider()
		if len(lags) > 0 {
			lensLags := make(map[string]uint64, len(lags))
			for k, v := range lags {
				lensLags[k] = v
			}
			metrics["lensLags"] = lensLags
		}
	}
	doc := LatticeHealthDoc{
		Key:         h.healthKey(),
		Component:   "refractor",
		Instance:    h.instance,
		Version:     "0.1.0",
		Status:      status,
		HeartbeatAt: substrate.FormatTimestamp(now),
		StartedAt:   substrate.FormatTimestamp(h.startedAt),
		Uptime:      formatISODuration(now.Sub(h.startedAt)),
		Metrics:     metrics,
		Issues:      []any{},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		h.logger.Error("refractor heartbeat marshal", "err", err)
		return
	}
	if _, err := h.conn.KVPut(ctx, h.bucket, h.healthKey(), data); err != nil {
		h.logger.Warn("refractor heartbeat put", "err", err, "key", h.healthKey())
	}
}

func (h *LatticeHeartbeater) healthKey() string {
	return "health.refractor." + h.instance
}

// formatISODuration is duplicated from internal/processor/health.go.
// Story 2.2 may consolidate into substrate.
func formatISODuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int64(d.Seconds())
	if seconds < 60 {
		return "PT" + itoa(seconds) + "S"
	}
	if seconds < 3600 {
		return "PT" + itoa(seconds/60) + "M" + itoa(seconds%60) + "S"
	}
	h := seconds / 3600
	rem := seconds % 3600
	return "PT" + itoa(h) + "H" + itoa(rem/60) + "M" + itoa(rem%60) + "S"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 20)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
