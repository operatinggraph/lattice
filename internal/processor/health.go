package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// Metrics holds the running counters surfaced through the heartbeat per
// Contract #5 §5.4 (recommended Phase 1 Processor baseline). Counters are
// atomically incremented from the commit path; the heartbeater snapshots
// them for emission.
type Metrics struct {
	OpsConsumed   atomic.Uint64
	OpsCommitted  atomic.Uint64
	OpsRejected   atomic.Uint64
	OpsDuplicates atomic.Uint64
	OpsMalformed  atomic.Uint64
}

// HealthDoc mirrors the Contract #5 §5.2 shape for a heartbeat write.
type HealthDoc struct {
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

// HealthHeartbeater periodically writes the Processor instance's health
// document to Health KV at `health.processor.<instance>`. Per NFR-O1 the
// interval is 10s minimum.
type HealthHeartbeater struct {
	conn      *substrate.Conn
	bucket    string
	instance  string
	startedAt time.Time
	interval  time.Duration
	metrics   *Metrics
	logger    *slog.Logger

	// Per-tick step-3 capability auth signals. The CapabilityAuthorizer is
	// wired by MakePipeline when AuthMode resolves to capability. step3-latency
	// always emits; cap-staleness skips emission when zero samples in the window.
	capAuthorizer *CapabilityAuthorizer
}

// NewHealthHeartbeater wires the heartbeater. instance must be a stable
// per-process identifier (Contract #5 §5.1 convention: proc-<NanoID>).
func NewHealthHeartbeater(conn *substrate.Conn, bucket, instance string, interval time.Duration, metrics *Metrics, logger *slog.Logger) *HealthHeartbeater {
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	return &HealthHeartbeater{
		conn:      conn,
		bucket:    bucket,
		instance:  instance,
		startedAt: time.Now(),
		interval:  interval,
		metrics:   metrics,
		logger:    logger,
	}
}

// Run blocks until ctx is cancelled, emitting heartbeats on a ticker.
// One initial heartbeat is emitted immediately so observers see a fresh
// document without waiting a full interval.
func (h *HealthHeartbeater) Run(ctx context.Context) {
	h.emit(ctx, "starting")
	t := time.NewTicker(h.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Use a short detached context for the final heartbeat so
			// the shuttingDown marker actually lands (the just-cancelled
			// ctx would error out the KV put).
			shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			h.emit(shutCtx, "shuttingDown")
			cancel()
			return
		case <-t.C:
			h.emit(ctx, "healthy")
		}
	}
}

// SetInterval adjusts the heartbeat ticker interval before Run is called.
// Must not be called after Run starts. Enforces the NFR-O1 minimum of 10s.
func (h *HealthHeartbeater) SetInterval(d time.Duration) {
	if d < 10*time.Second {
		d = 10 * time.Second
	}
	h.interval = d
}

// AttachCapabilityAuthorizer wires the step-3 latency + staleness ring buffers
// so each heartbeat tick emits the two derived keys. Idempotent: calling twice
// replaces the prior authorizer.
func (h *HealthHeartbeater) AttachCapabilityAuthorizer(ca *CapabilityAuthorizer) {
	h.capAuthorizer = ca
}

// EmitMalformedOperation writes the per-malformed-envelope marker into
// Health KV. Key form: `health.processor.<instance>.malformed-operation.<requestId>`.
// Called inline from step 1 when an envelope fails to parse but a
// requestId is recoverable.
func (h *HealthHeartbeater) EmitMalformedOperation(ctx context.Context, requestID, reason string) {
	if requestID == "" {
		return
	}
	key := fmt.Sprintf("health.processor.%s.malformed-operation.%s", h.instance, requestID)
	doc := map[string]any{
		"key":          key,
		"component":    "processor",
		"instance":     h.instance,
		"event":        "MalformedOperation",
		"requestId":    requestID,
		"reason":       reason,
		"observedAt":   substrate.FormatTimestamp(time.Now()),
	}
	b, _ := json.Marshal(doc)
	if _, err := h.conn.KVPut(ctx, h.bucket, key, b); err != nil {
		h.logger.Warn("health: failed to write malformed-operation marker",
			"key", key, "error", err)
	}
}

func (h *HealthHeartbeater) emit(ctx context.Context, status string) {
	now := time.Now()
	doc := HealthDoc{
		Key:         h.healthKey(),
		Component:   "processor",
		Instance:    h.instance,
		Version:     "1.0",
		Status:      status,
		HeartbeatAt: substrate.FormatTimestamp(now),
		StartedAt:   substrate.FormatTimestamp(h.startedAt),
		Uptime:      formatISODuration(now.Sub(h.startedAt)),
		Metrics: map[string]any{
			"ops_consumed_total":   h.metrics.OpsConsumed.Load(),
			"ops_committed_total":  h.metrics.OpsCommitted.Load(),
			"ops_rejected_total":   h.metrics.OpsRejected.Load(),
			"ops_duplicates_total": h.metrics.OpsDuplicates.Load(),
			"ops_malformed_total":  h.metrics.OpsMalformed.Load(),
			"lane_lag":             map[string]int{"default": 0, "meta": 0, "urgent": 0, "system": 0},
		},
		Issues: []any{},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		h.logger.Warn("health: marshal heartbeat", "error", err)
		return
	}
	if _, err := h.conn.KVPut(ctx, h.bucket, h.healthKey(), b); err != nil {
		h.logger.Warn("health: write heartbeat", "key", h.healthKey(), "error", err)
	}

	// Per-tick capability-auth signals.
	h.emitCapabilityAuthSignals(ctx)
}

// emitCapabilityAuthSignals writes the step3-latency (always) and
// cap-staleness (only when non-empty window) signals derived from the
// CapabilityAuthorizer's ring buffers. No-op when no authorizer is
// attached (stub mode).
func (h *HealthHeartbeater) emitCapabilityAuthSignals(ctx context.Context) {
	if h.capAuthorizer == nil {
		return
	}
	// step3-latency — always emit (Decision #5: zero-sample emission is
	// itself a live signal of "Processor saw zero ops this tick").
	latency := h.capAuthorizer.LatencyStats()
	latencyKey := "health.processor." + h.instance + ".step3-latency"
	latencyDoc := map[string]any{
		"key":         latencyKey,
		"component":   "processor",
		"instance":    h.instance,
		"observedAt":  substrate.FormatTimestamp(time.Now()),
		"count":       latency.Count,
		"meanNs":      latency.Mean.Nanoseconds(),
		"p95Ns":       latency.P95.Nanoseconds(),
		"p99Ns":       latency.P99.Nanoseconds(),
	}
	if raw, err := json.Marshal(latencyDoc); err == nil {
		if _, err := h.conn.KVPut(ctx, h.bucket, latencyKey, raw); err != nil {
			h.logger.Warn("health: write step3-latency", "key", latencyKey, "error", err)
		}
	}

	// cap-staleness — skip when no samples in window (Decision #4: no
	// misleading zeros).
	staleness := h.capAuthorizer.StalenessStats()
	if staleness.Count == 0 {
		return
	}
	stalenessKey := "health.processor." + h.instance + ".cap-staleness"
	stalenessDoc := map[string]any{
		"key":             stalenessKey,
		"component":       "processor",
		"instance":        h.instance,
		"observedAt":      substrate.FormatTimestamp(time.Now()),
		"count":           staleness.Count,
		"meanMs":          staleness.Mean.Milliseconds(),
		"p95Ms":           staleness.P95.Milliseconds(),
		"p99Ms":           staleness.P99.Milliseconds(),
		"exceedingNFRP3":  h.capAuthorizer.StalenessExceedingNFRP3(),
	}
	if raw, err := json.Marshal(stalenessDoc); err == nil {
		if _, err := h.conn.KVPut(ctx, h.bucket, stalenessKey, raw); err != nil {
			h.logger.Warn("health: write cap-staleness", "key", stalenessKey, "error", err)
		}
	}
}

func (h *HealthHeartbeater) healthKey() string {
	return "health.processor." + h.instance
}

// formatISODuration renders a Go duration as the ISO 8601 PT…S form used
// by the refractor-stub heartbeat and recommended by Contract #5 §5.2.
func formatISODuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int64(d.Seconds())
	hours := secs / 3600
	mins := (secs % 3600) / 60
	rem := secs % 60
	if hours > 0 {
		return fmt.Sprintf("PT%dH%dM%dS", hours, mins, rem)
	}
	if mins > 0 {
		return fmt.Sprintf("PT%dM%dS", mins, rem)
	}
	return fmt.Sprintf("PT%dS", rem)
}
