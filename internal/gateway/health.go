package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// healthVersion is the Gateway build version reported in the Contract #5 heartbeat.
const healthVersion = "0.1.0"

// DefaultHeartbeatEvery is the Contract #5 §5.6 / NFR-O1 heartbeat cadence floor.
const DefaultHeartbeatEvery = 10 * time.Second

// healthDoc is the Contract #5 §5.2 heartbeat document the Gateway writes to
// health.gateway.<instance>. Same shape as the Loom/Bridge/Processor docs;
// component is "gateway" (already reserved by Contract #5 §5.1 as a Phase 2+
// component name).
type healthDoc struct {
	Key         string         `json:"key"`
	Component   string         `json:"component"`
	Instance    string         `json:"instance"`
	Version     string         `json:"version"`
	Status      string         `json:"status"`
	HeartbeatAt string         `json:"heartbeatAt"`
	StartedAt   string         `json:"startedAt"`
	Uptime      string         `json:"uptime"`
	Metrics     map[string]any `json:"metrics"`
	Issues      []healthIssue  `json:"issues"`
}

type healthIssue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// Metrics tracks the Gateway's cumulative counters. Safe for concurrent use;
// the HTTP handlers increment it, the Heartbeater reads a snapshot.
type Metrics struct {
	requestsTotal     atomic.Int64
	authFailuresTotal atomic.Int64
	opsSubmittedTotal atomic.Int64
}

func (m *Metrics) snapshot() map[string]any {
	return map[string]any{
		"requests_total":      m.requestsTotal.Load(),
		"auth_failures_total": m.authFailuresTotal.Load(),
		"ops_submitted_total": m.opsSubmittedTotal.Load(),
	}
}

// Heartbeater writes the Gateway's Contract #5 heartbeat to Health KV on a
// fixed cadence. It carries no dependency on the HTTP server — a Server may
// run with or without one attached.
type Heartbeater struct {
	conn      *substrate.Conn
	bucket    string
	instance  string
	metrics   *Metrics
	every     time.Duration
	startedAt time.Time
	logger    *slog.Logger
	now       func() time.Time
}

// NewHeartbeater builds a Heartbeater. bucket is the Health KV bucket name
// (config.yaml health.bucketName); instance is the stable per-process
// identifier (Contract #5 §5.1 convention: "gw-<NanoID>").
func NewHeartbeater(conn *substrate.Conn, bucket, instance string, metrics *Metrics, logger *slog.Logger) *Heartbeater {
	if logger == nil {
		logger = slog.Default()
	}
	return &Heartbeater{
		conn:      conn,
		bucket:    bucket,
		instance:  instance,
		metrics:   metrics,
		every:     DefaultHeartbeatEvery,
		startedAt: time.Now(),
		logger:    logger,
		now:       time.Now,
	}
}

// Run blocks, emitting a heartbeat immediately and then every h.every, until
// ctx is canceled.
func (h *Heartbeater) Run(ctx context.Context) {
	h.emit(ctx, "healthy")
	t := time.NewTicker(h.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.emit(ctx, "healthy")
		}
	}
}

func (h *Heartbeater) key() string {
	return "health.gateway." + h.instance
}

func (h *Heartbeater) emit(ctx context.Context, status string) {
	now := h.now()
	doc := healthDoc{
		Key:         h.key(),
		Component:   "gateway",
		Instance:    h.instance,
		Version:     healthVersion,
		Status:      status,
		HeartbeatAt: now.UTC().Format(time.RFC3339),
		StartedAt:   h.startedAt.UTC().Format(time.RFC3339),
		Uptime:      formatUptime(now.Sub(h.startedAt)),
		Metrics:     h.metrics.snapshot(),
		Issues:      []healthIssue{},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		h.logger.Error("gateway: marshal heartbeat", "error", err)
		return
	}
	if _, err := h.conn.KVPut(ctx, h.bucket, doc.Key, raw); err != nil {
		h.logger.Warn("gateway: heartbeat write failed", "error", err)
	}
}

// formatUptime renders d as an ISO 8601 duration (Contract #5 §5.2's `uptime`
// field), e.g. "PT72H15M".
func formatUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	hours := int64(d.Hours())
	minutes := int64(d.Minutes()) % 60
	return "PT" + strconv.FormatInt(hours, 10) + "H" + strconv.FormatInt(minutes, 10) + "M"
}
