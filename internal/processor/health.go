package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync/atomic"
	"time"

	"github.com/asolgan/lattice/internal/healthkv"
	"github.com/asolgan/lattice/internal/substrate"
)

// LaneBacklogReader reads a durable consumer's pending backlog by durable name.
// The substrate ConsumerSupervisor satisfies it (PendingForConsumer), letting the
// heartbeater report the real lane_lag without holding a jetstream.Consumer
// handle — keeping the sole Core-KV writer on the same substrate boundary as
// every other component.
type LaneBacklogReader interface {
	PendingForConsumer(ctx context.Context, name string) (uint64, error)
}

// defaultLaneLagThreshold is the consumer-backlog count above which the
// heartbeat raises a ProcessorLaneLagging warning (status ⇒ degraded). Mirrors
// the Refractor capability-lens lag default; overridable via SetLagThreshold.
const defaultLaneLagThreshold = 100

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
	// CommitRetries counts same-key revision conflicts the Processor absorbed by
	// re-hydrating + re-executing in-process (Contract #3 §3.2 OCC + the bounded
	// internal retry). CommitRetryExhausted counts conflicts that survived the
	// retry budget and surfaced RevisionConflict to the client — a genuinely hot
	// key. Mirrors Weaver's sweepReclaims / sweepReclaimsSuppressed split.
	CommitRetries        atomic.Uint64
	CommitRetryExhausted atomic.Uint64
}

// healthIssue is one Contract #5 §5.5 issue record. since persists across
// heartbeats while the issue stays open and is dropped when it resolves.
type healthIssue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Since    string `json:"since"`
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
	Issues      []healthIssue  `json:"issues"`
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

	// ttlMultiplier derives the heartbeat's Health-KV TTL (interval ×
	// ttlMultiplier, Contract #5 §5.6). Zero disables TTL (falls back to a
	// plain, non-expiring KVPut) — an operator escape hatch; default is on.
	ttlMultiplier int

	// diagnosticTTL bounds sparse per-instance diagnostic breadcrumbs
	// (malformed-operation markers) — a fixed, not re-armed, window (unlike
	// the heartbeat's interval-derived TTL) since these are write-once
	// records, not a liveness signal. Zero disables TTL (plain KVPut).
	diagnosticTTL time.Duration

	// Per-tick step-3 capability auth signal. The CapabilityAuthorizer is
	// wired by MakePipeline when AuthMode resolves to capability. step3-latency
	// always emits.
	capAuthorizer *CapabilityAuthorizer

	// Lane-backlog reader for real per-lane lane_lag reporting, plus the
	// lane→durable map it queries. Attached by the cmd wiring once the
	// ConsumerSupervisor has all four lane consumers registered, before Run
	// starts (so no concurrent access with the emit loop). A nil reader — or a
	// lane absent from the map — ⇒ that lane reported as null, never a
	// fabricated zero.
	backlog      LaneBacklogReader
	laneDurables map[string]string

	// lagThreshold is the backlog count above which ProcessorLaneLagging fires.
	lagThreshold uint64

	// openIssues tracks an internal issue key → since-timestamp for currently-open
	// issues so the §5.5 since field persists across heartbeats and drops on
	// resolve. The key is per-lane (ProcessorLaneLagging:<lane>) so two lanes
	// lagging at once track their since independently.
	openIssues map[string]string
}

// NewHealthHeartbeater wires the heartbeater. instance must be a stable
// per-process identifier (Contract #5 §5.1 convention: proc-<NanoID>).
func NewHealthHeartbeater(conn *substrate.Conn, bucket, instance string, interval time.Duration, metrics *Metrics, logger *slog.Logger) *HealthHeartbeater {
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	return &HealthHeartbeater{
		conn:          conn,
		bucket:        bucket,
		instance:      instance,
		startedAt:     time.Now(),
		interval:      interval,
		metrics:       metrics,
		logger:        logger,
		lagThreshold:  defaultLaneLagThreshold,
		openIssues:    map[string]string{},
		ttlMultiplier: healthkv.DefaultTTLMultiplier,
		diagnosticTTL: healthkv.DefaultDiagnosticTTL,
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

// AttachCapabilityAuthorizer wires the step-3 latency ring buffer so each
// heartbeat tick emits the derived step3-latency key. Idempotent: calling
// twice replaces the prior authorizer.
func (h *HealthHeartbeater) AttachCapabilityAuthorizer(ca *CapabilityAuthorizer) {
	h.capAuthorizer = ca
}

// AttachBacklogReader wires the lane-backlog reader (the ConsumerSupervisor) and
// the lane→durable map to query so each heartbeat reports the real per-lane
// backlog (lane_lag). Must be called before Run starts — the emit loop reads the
// handle without synchronization.
func (h *HealthHeartbeater) AttachBacklogReader(r LaneBacklogReader, laneDurables map[string]string) {
	h.backlog = r
	h.laneDurables = laneDurables
}

// SetLagThreshold overrides the backlog count above which ProcessorLaneLagging
// fires. Must be called before Run starts. A zero value is ignored (keeps the
// default) — a 0 threshold would flag every non-empty backlog as degraded.
func (h *HealthHeartbeater) SetLagThreshold(n uint64) {
	if n > 0 {
		h.lagThreshold = n
	}
}

// SetTTLMultiplier overrides the heartbeat TTL multiplier (TTL = interval ×
// multiplier, Contract #5 §5.6). Must be called before Run starts. Zero
// disables the TTL (an escape hatch for an operator who wants sticky keys); a
// negative value is clamped to 0.
func (h *HealthHeartbeater) SetTTLMultiplier(n int) {
	if n < 0 {
		n = 0
	}
	h.ttlMultiplier = n
}

// heartbeatTTL derives the current TTL from interval × ttlMultiplier
// (Contract #5 §5.6) — 0 when TTL is disabled.
func (h *HealthHeartbeater) heartbeatTTL() time.Duration {
	return h.interval * time.Duration(h.ttlMultiplier)
}

// SetDiagnosticTTL overrides the fixed TTL applied to sparse per-instance
// diagnostic breadcrumbs (malformed-operation markers). Zero disables the TTL
// (sticky keys); a negative value is clamped to 0.
func (h *HealthHeartbeater) SetDiagnosticTTL(d time.Duration) {
	if d < 0 {
		d = 0
	}
	h.diagnosticTTL = d
}

// EmitMalformedOperation writes the per-malformed-envelope marker into
// Health KV. Key form: `health.processor.<instance>.malformed-operation.<requestId>`.
// Called inline from step 1 when an envelope fails to parse but a
// requestId is recoverable. Carries a fixed diagnosticTTL (not re-armed) so a
// dead instance's malformed-operation breadcrumbs clear within the window
// instead of accruing forever.
func (h *HealthHeartbeater) EmitMalformedOperation(ctx context.Context, requestID, reason string) {
	if requestID == "" {
		return
	}
	key := fmt.Sprintf("health.processor.%s.malformed-operation.%s", h.instance, requestID)
	doc := map[string]any{
		"key":        key,
		"component":  "processor",
		"instance":   h.instance,
		"event":      "MalformedOperation",
		"requestId":  requestID,
		"reason":     reason,
		"observedAt": substrate.FormatTimestamp(time.Now()),
	}
	b, _ := json.Marshal(doc)
	if _, err := h.conn.KVPutWithTTL(ctx, h.bucket, key, b, h.diagnosticTTL); err != nil {
		h.logger.Warn("health: failed to write malformed-operation marker",
			"key", key, "error", err)
	}
}

func (h *HealthHeartbeater) emit(ctx context.Context, lifecycle string) {
	doc := h.buildHealthDoc(ctx, lifecycle, time.Now())
	b, err := json.Marshal(doc)
	if err != nil {
		h.logger.Warn("health: marshal heartbeat", "error", err)
		return
	}
	if _, err := h.conn.KVPutWithTTL(ctx, h.bucket, h.healthKey(), b, h.heartbeatTTL()); err != nil {
		h.logger.Warn("health: write heartbeat", "key", h.healthKey(), "error", err)
	}

	// Per-tick capability-auth signals.
	h.emitCapabilityAuthSignals(ctx)
}

// buildHealthDoc assembles the §5.2 heartbeat document for the given lifecycle
// phase: snapshots the counters, reads the real consumer backlog into lane_lag,
// derives the open issue set, and reconciles the lifecycle status against it.
// Pure with respect to KV (no writes) so it is unit-testable with a fake
// consumer; emit handles marshalling and the KV put.
func (h *HealthHeartbeater) buildHealthDoc(ctx context.Context, lifecycle string, now time.Time) HealthDoc {
	metrics := map[string]any{
		"ops_consumed_total":   h.metrics.OpsConsumed.Load(),
		"ops_committed_total":  h.metrics.OpsCommitted.Load(),
		"ops_rejected_total":   h.metrics.OpsRejected.Load(),
		"ops_duplicates_total": h.metrics.OpsDuplicates.Load(),
		"ops_malformed_total":  h.metrics.OpsMalformed.Load(),
		// §3.2 OCC commit-conflict absorption (Contract #5 §5.4): retries the
		// Processor absorbed in-process vs. those that exhausted the budget and
		// surfaced RevisionConflict (a genuinely hot key).
		"commit_retries_total":         h.metrics.CommitRetries.Load(),
		"commit_retry_exhausted_total": h.metrics.CommitRetryExhausted.Load(),
	}

	// Real per-lane consumer backlog (Contract #5 §5.4 lane_lag). Each lane has
	// its own durable consumer, so each lane's NumPending is read independently:
	// lane_lag.<lane> carries that lane's real backlog, and lane_lag_total sums
	// the readable lanes. A lane whose consumer can't be read this tick (no
	// reader attached, or a transient Info error) is reported null — never a
	// fabricated zero, which a watcher would trust as healthy; lane_lag_total is
	// null only when NO lane is readable.
	laneLag := make(map[string]any, len(laneOrder))
	var total uint64
	anyReadable := false
	active := map[string]activeIssue{}
	for _, lane := range laneOrder {
		n, ok := h.lanePending(ctx, h.laneDurables[lane])
		if !ok {
			laneLag[lane] = nil
			continue
		}
		laneLag[lane] = n
		total += n
		anyReadable = true
		if n > h.lagThreshold {
			active["ProcessorLaneLagging:"+lane] = activeIssue{
				code:     "ProcessorLaneLagging",
				severity: "warning",
				message:  fmt.Sprintf("lane %q backlog %d exceeds threshold %d (consumer %s)", lane, n, h.lagThreshold, h.laneDurables[lane]),
			}
		}
	}
	metrics["lane_lag"] = laneLag
	if anyReadable {
		metrics["lane_lag_total"] = total
	} else {
		metrics["lane_lag_total"] = nil
	}

	issues := h.reconcileIssues(active, now)
	status := aggregateStatus(lifecycle, issues)

	return HealthDoc{
		Key:         h.healthKey(),
		Component:   "processor",
		Instance:    h.instance,
		Version:     "1.0",
		Status:      status,
		HeartbeatAt: substrate.FormatTimestamp(now),
		StartedAt:   substrate.FormatTimestamp(h.startedAt),
		Uptime:      formatISODuration(now.Sub(h.startedAt)),
		Metrics:     metrics,
		Issues:      issues,
	}
}

// lanePending returns one lane durable's current backlog (NumPending) and
// whether it could be read. A nil reader, an empty durable name (lane absent
// from the map), or a transient read error yields (0, false) so the caller
// reports null rather than a fabricated zero.
func (h *HealthHeartbeater) lanePending(ctx context.Context, durable string) (uint64, bool) {
	if h.backlog == nil || durable == "" {
		return 0, false
	}
	pending, err := h.backlog.PendingForConsumer(ctx, durable)
	if err != nil {
		h.logger.Debug("health: lane backlog unavailable", "durable", durable, "error", err)
		return 0, false
	}
	return pending, true
}

// activeIssue is a transient issue open this tick: the emitted §5.5 code plus
// its severity and message. The map key under which it is tracked may differ
// from code (e.g. ProcessorLaneLagging:<lane>) so per-lane issues sharing one
// code track their since independently. reconcileIssues stamps it with a
// persisted since timestamp.
type activeIssue struct {
	code     string
	severity string
	message  string
}

// reconcileIssues converts the issue keys open this tick into §5.5 issue
// records, carrying each key's since timestamp across heartbeats (first-seen →
// now) and dropping keys that resolved. Output is sorted by (code, message) for
// stable heartbeats across per-lane issues that share a code.
func (h *HealthHeartbeater) reconcileIssues(active map[string]activeIssue, now time.Time) []healthIssue {
	out := make([]healthIssue, 0, len(active))
	next := make(map[string]string, len(active))
	for key, ai := range active {
		since, ok := h.openIssues[key]
		if !ok {
			since = substrate.FormatTimestamp(now)
		}
		next[key] = since
		out = append(out, healthIssue{Code: ai.code, Severity: ai.severity, Message: ai.message, Since: since})
	}
	h.openIssues = next
	sort.Slice(out, func(i, j int) bool {
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		return out[i].Message < out[j].Message
	})
	return out
}

// aggregateStatus reconciles the lifecycle phase with the open issue set per
// Contract #5 §5.3: any "error" ⇒ "unhealthy", else any "warning" ⇒ "degraded",
// else the lifecycle status is kept. The "starting" and "shuttingDown" phases
// are returned unchanged — an initializing or draining Processor reports its
// lifecycle phase, not a steady-state health grade.
func aggregateStatus(lifecycle string, issues []healthIssue) string {
	if lifecycle == "starting" || lifecycle == "shuttingDown" {
		return lifecycle
	}
	worst := lifecycle
	for _, is := range issues {
		switch is.Severity {
		case "error":
			return "unhealthy"
		case "warning":
			worst = "degraded"
		}
	}
	return worst
}

// emitCapabilityAuthSignals writes the step3-latency signal derived from the
// CapabilityAuthorizer's latency ring buffer. No-op when no authorizer is
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
		"key":        latencyKey,
		"component":  "processor",
		"instance":   h.instance,
		"observedAt": substrate.FormatTimestamp(time.Now()),
		"count":      latency.Count,
		"meanNs":     latency.Mean.Nanoseconds(),
		"p95Ns":      latency.P95.Nanoseconds(),
		"p99Ns":      latency.P99.Nanoseconds(),
	}
	if raw, err := json.Marshal(latencyDoc); err == nil {
		if _, err := h.conn.KVPutWithTTL(ctx, h.bucket, latencyKey, raw, h.heartbeatTTL()); err != nil {
			h.logger.Warn("health: write step3-latency", "key", latencyKey, "error", err)
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
