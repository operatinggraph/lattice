// Package health: Lattice-side heartbeater per Contract #5 §5.2.
// Emits health.refractor.<instance> and per-lens lag every 10s.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// defaultCapabilityLensLagThreshold is the consumer-lag (pending message count)
// above which an active capability lens is flagged CapabilityLensLagging
// (severity warning ⇒ degraded). Deployment-overridable via the heartbeater's
// CapabilityLensLagThreshold field. A warning, not a halt: it self-resolves on
// the next heartbeat once the projector drains its backlog.
const defaultCapabilityLensLagThreshold = 100

// Issue codes for capability-lens anomalies (Contract #5 §5.5; component-defined,
// PascalCase).
const (
	issueCapabilityLensPaused  = "CapabilityLensPaused"
	issueCapabilityLensLagging = "CapabilityLensLagging"
)

// CapabilityLensStatus is one auth-plane (capability-kv) lens's liveness snapshot,
// supplied by CapabilityLensProvider for the per-heartbeat threshold evaluation.
// The provider reads it from the lens's health Reporter (status / pauseReason) and
// supervised consumer (consumerLag); it never touches the authorization decision
// path, Core KV, or the projection itself.
type CapabilityLensStatus struct {
	CanonicalName string
	RuleID        string
	Status        string // "active" | "paused" | "rebuilding"
	PauseReason   string // "" when not paused
	ConsumerLag   uint64
}

// issueRecord is one entry of the Health-KV `issues` array (Contract #5 §5.5).
type issueRecord struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // "warning" (degraded) | "error" (unhealthy)
	Message  string `json:"message"`
	Since    string `json:"since"` // ISO 8601; persists across heartbeats while open
}

// capIssue is the in-flight (severity, message) for an active issue code this cycle.
type capIssue struct {
	severity string
	message  string
}

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

	// LensLatencyProvider optionally returns per-Lens projection latency
	// stats (Contract #5 §5.2 / NFR-P3). The map is keyed by Lens canonical
	// name (e.g. "capability", "capabilityRoleIndex") and produces
	// {mean,p95,p99,count} as nanosecond-precision durations. May be nil
	// before any lens activates with a latency buffer installed.
	LensLatencyProvider func() map[string]LensLatencySnapshot

	// CapabilityLensProvider optionally returns liveness snapshots for the
	// auth-plane (capability-kv) lenses — the authorization read-model the
	// Processor's capability check reads. When a snapshot crosses a threshold the
	// heartbeat raises a Contract #5 §5.5 issue and degrades status, the
	// operational backstop for the Processor's absent per-op freshness gate. nil
	// before any capability lens activates.
	CapabilityLensProvider func() []CapabilityLensStatus

	// CapabilityLensLagThreshold is the consumer-lag (pending count) above which
	// an active capability lens is flagged CapabilityLensLagging (warning).
	// Zero selects defaultCapabilityLensLagThreshold.
	CapabilityLensLagThreshold uint64

	// issuesMu guards openCapIssues.
	issuesMu sync.Mutex
	// openCapIssues tracks the `since` timestamp of each currently-open
	// capability-lens issue code (Contract #5 §5.5: components hold open issues in
	// memory so `since` persists across heartbeats; a resolved issue is dropped).
	openCapIssues map[string]string
}

// LensLatencySnapshot is the per-Lens summary the heartbeater emits
// under `health.refractor.<instance>.lens.<canonicalName>.*` (or as a
// sub-map of the main heartbeat document — see emit()).
// Count is the number of samples behind the mean/p95/p99 figures.
type LensLatencySnapshot struct {
	Count int           `json:"count"`
	Mean  time.Duration `json:"mean"`
	P95   time.Duration `json:"p95"`
	P99   time.Duration `json:"p99"`
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
	// Per-Lens projection latency under metrics.lensLatency (Contract #5 §5.2).
	// Each entry carries {count, mean, p95, p99} expressed in nanoseconds.
	// Only Lenses with at least one sample are emitted to avoid misleading
	// zeros on quiet instances.
	if h.LensLatencyProvider != nil {
		stats := h.LensLatencyProvider()
		if len(stats) > 0 {
			out := make(map[string]map[string]any, len(stats))
			for name, s := range stats {
				if s.Count == 0 {
					continue
				}
				out[name] = map[string]any{
					"count":  s.Count,
					"meanNs": s.Mean.Nanoseconds(),
					"p95Ns":  s.P95.Nanoseconds(),
					"p99Ns":  s.P99.Nanoseconds(),
				}
			}
			if len(out) > 0 {
				metrics["lensLatency"] = out
			}
		}
	}
	// Capability-lens liveness backstop: surface a §5.5 issue (and degrade status)
	// when an auth-plane lens is paused or lagging beyond threshold. The
	// metrics.capabilityLens sub-map is emitted on every cycle (including healthy
	// alert:"ok") so observers can render the green state, not only anomalies.
	capMetric, capIssues := h.evalCapabilityLenses(now)
	if len(capMetric) > 0 {
		metrics["capabilityLens"] = capMetric
	}
	issues := make([]any, 0, len(capIssues))
	for _, is := range capIssues {
		issues = append(issues, is)
	}
	// Elevate to the §5.4 degraded/unhealthy status while a capability issue is
	// open — at startup too, so a paused-at-boot capability lens is visible
	// immediately. A "shutdown" beat is left as-is (the instance is tearing down),
	// and a clean cycle keeps its lifecycle status ("starting"/"healthy").
	effectiveStatus := status
	if status != "shutdown" && len(capIssues) > 0 {
		effectiveStatus = aggregateStatus(capIssues)
	}
	doc := LatticeHealthDoc{
		Key:         h.healthKey(),
		Component:   "refractor",
		Instance:    h.instance,
		Version:     "0.1.0",
		Status:      effectiveStatus,
		HeartbeatAt: substrate.FormatTimestamp(now),
		StartedAt:   substrate.FormatTimestamp(h.startedAt),
		Uptime:      formatISODuration(now.Sub(h.startedAt)),
		Metrics:     metrics,
		Issues:      issues,
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

// evalCapabilityLenses applies the §5.5 threshold model to the capability-lens
// snapshots, returning the metrics.capabilityLens sub-map and the open issue
// records. It reconciles the in-memory open-issue set so each issue's `since`
// persists across heartbeats and a resolved issue is dropped on the next cycle.
// Returns (nil, nil) when no provider is wired.
func (h *LatticeHeartbeater) evalCapabilityLenses(now time.Time) (map[string]map[string]any, []issueRecord) {
	if h.CapabilityLensProvider == nil {
		return nil, nil
	}
	threshold := h.CapabilityLensLagThreshold
	if threshold == 0 {
		threshold = defaultCapabilityLensLagThreshold
	}

	snaps := h.CapabilityLensProvider()
	metric := make(map[string]map[string]any, len(snaps))
	var paused, lagging []string
	for _, s := range snaps {
		alert := "ok"
		switch {
		case s.Status == "paused":
			alert = "paused"
			reason := s.PauseReason
			if reason == "" {
				reason = "unknown"
			}
			paused = append(paused, fmt.Sprintf("%s (%s)", capLensName(s), reason))
		case s.Status == "active" && s.ConsumerLag > threshold:
			alert = "lagging"
			lagging = append(lagging, fmt.Sprintf("%s (lag %d)", capLensName(s), s.ConsumerLag))
		}
		metric[capLensName(s)] = map[string]any{
			"status":      s.Status,
			"consumerLag": s.ConsumerLag,
			"alert":       alert,
		}
	}

	active := map[string]capIssue{}
	if len(paused) > 0 {
		active[issueCapabilityLensPaused] = capIssue{
			severity: "error",
			message: "capability lens paused; authorization read-model is frozen — grants/revocations will not project: " +
				strings.Join(paused, ", "),
		}
	}
	if len(lagging) > 0 {
		active[issueCapabilityLensLagging] = capIssue{
			severity: "warning",
			message: fmt.Sprintf("capability lens consumer lag exceeds threshold %d; authorization reads may be stale: %s",
				threshold, strings.Join(lagging, ", ")),
		}
	}
	return metric, h.reconcileCapIssues(active, now)
}

// reconcileCapIssues merges this cycle's active capability issues with the
// in-memory open set (Contract #5 §5.5): a newly-active code is stamped with
// `since=now`; an already-open code keeps its original `since`; a code no longer
// active is dropped. Output is sorted by code for deterministic heartbeats.
func (h *LatticeHeartbeater) reconcileCapIssues(active map[string]capIssue, now time.Time) []issueRecord {
	h.issuesMu.Lock()
	defer h.issuesMu.Unlock()
	if h.openCapIssues == nil {
		h.openCapIssues = map[string]string{}
	}
	for code := range h.openCapIssues {
		if _, ok := active[code]; !ok {
			delete(h.openCapIssues, code)
		}
	}
	out := make([]issueRecord, 0, len(active))
	for code, ci := range active {
		since, ok := h.openCapIssues[code]
		if !ok {
			since = substrate.FormatTimestamp(now)
			h.openCapIssues[code] = since
		}
		out = append(out, issueRecord{Code: code, Severity: ci.severity, Message: ci.message, Since: since})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// aggregateStatus maps the open issues to a Contract #5 §5.4 status: any error ⇒
// unhealthy, any warning ⇒ degraded, otherwise healthy.
func aggregateStatus(issues []issueRecord) string {
	worst := "healthy"
	for _, is := range issues {
		if is.Severity == "error" {
			return "unhealthy"
		}
		if is.Severity == "warning" {
			worst = "degraded"
		}
	}
	return worst
}

// capLensName prefers the canonical name, falling back to the rule ID.
func capLensName(s CapabilityLensStatus) string {
	if s.CanonicalName != "" {
		return s.CanonicalName
	}
	return s.RuleID
}

func (h *LatticeHeartbeater) healthKey() string {
	return "health.refractor." + h.instance
}

// formatISODuration converts a duration to an ISO 8601 duration string (e.g. "PT2M30S").
func formatISODuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	itoa := func(n int64) string { return strconv.FormatInt(n, 10) }
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
