package loom

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/asolgan/lattice/internal/healthkv"
	"github.com/asolgan/lattice/internal/substrate"
)

// healthVersion is the Loom build version reported in the Contract #5 heartbeat.
const healthVersion = "0.1.0"

// defaultHeartbeatEvery is the Contract #5 §5.6 / NFR-O1 heartbeat cadence floor.
const defaultHeartbeatEvery = 10 * time.Second

// loomHealthDoc is the Contract #5 §5.2 heartbeat document Loom writes to
// health.loom.<instance>. Same shape as the Processor/Refractor docs;
// component is "loom".
type loomHealthDoc struct {
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

// healthIssue is one Contract #5 §5.2 issue entry.
type healthIssue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// runningInstanceCounter reports how many loom-state instance.<id> entries are
// status=running, scanned on a heartbeat cadence (never per-message).
type runningInstanceCounter struct {
	conn   *substrate.Conn
	bucket string
	logger *slog.Logger
}

func (r *runningInstanceCounter) count(ctx context.Context) (int, error) {
	keys, err := r.conn.KVListKeys(ctx, r.bucket)
	if err != nil {
		return 0, err
	}
	running := 0
	for _, k := range keys {
		if !isInstanceRecordKey(k) {
			continue
		}
		entry, err := r.conn.KVGet(ctx, r.bucket, k)
		if err != nil {
			r.logger.Warn("loom heartbeat: instance key read failed", "key", k, "err", err)
			continue
		}
		var inst Instance
		if err := json.Unmarshal(entry.Value, &inst); err != nil {
			continue
		}
		if inst.Status == StatusRunning {
			running++
		}
	}
	return running, nil
}

// heartbeater writes the Contract #5 health.loom.<instance> document on a
// ticker. metrics carry the running-instance count and a per-consumer state map
// read from the consumer-state cache.
type heartbeater struct {
	conn      *substrate.Conn
	bucket    string
	instance  string
	startedAt time.Time
	interval  time.Duration
	states    *healthkv.ConsumerStateCache
	counter   *runningInstanceCounter
	logger    *slog.Logger

	// ttlMultiplier derives the heartbeat's Health-KV TTL (interval ×
	// ttlMultiplier, Contract #5 §5.6). Zero disables TTL.
	ttlMultiplier int
}

func newHeartbeater(conn *substrate.Conn, healthBucket, stateBucket, instance string, every time.Duration, states *healthkv.ConsumerStateCache, logger *slog.Logger) *heartbeater {
	if logger == nil {
		logger = slog.Default()
	}
	if every <= 0 {
		every = defaultHeartbeatEvery
	}
	return &heartbeater{
		conn:          conn,
		bucket:        healthBucket,
		instance:      instance,
		startedAt:     time.Now(),
		interval:      every,
		states:        states,
		counter:       &runningInstanceCounter{conn: conn, bucket: stateBucket, logger: logger},
		logger:        logger,
		ttlMultiplier: healthkv.DefaultTTLMultiplier,
	}
}

// SetTTLMultiplier overrides the heartbeat TTL multiplier (TTL = interval ×
// multiplier, Contract #5 §5.6). Must be called before run starts. Zero
// disables the TTL (an escape hatch for an operator who wants sticky keys); a
// negative value is clamped to 0.
func (h *heartbeater) SetTTLMultiplier(n int) {
	if n < 0 {
		n = 0
	}
	h.ttlMultiplier = n
}

// heartbeatTTL derives the current TTL from interval × ttlMultiplier
// (Contract #5 §5.6) — 0 when TTL is disabled.
func (h *heartbeater) heartbeatTTL() time.Duration {
	return h.interval * time.Duration(h.ttlMultiplier)
}

// run blocks until ctx is cancelled, emitting one heartbeat immediately and
// then on each tick. A final "shutdown" heartbeat is emitted on ctx cancel.
func (h *heartbeater) run(ctx context.Context) {
	h.emit(ctx, "starting")
	t := time.NewTicker(h.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			h.emit(detached, "shutdown")
			cancel()
			return
		case <-t.C:
			h.emit(ctx, "healthy")
		}
	}
}

func (h *heartbeater) emit(ctx context.Context, status string) {
	now := time.Now()
	states := h.states.Snapshot()

	metrics := map[string]any{
		"consumers": states,
	}
	if n, err := h.counter.count(ctx); err == nil {
		metrics["runningInstances"] = n
	} else {
		h.logger.Warn("loom heartbeat: running-instance scan failed", "err", err)
	}

	issues := []healthIssue{}
	for name, state := range states {
		if state == "pausedStructural" {
			issues = append(issues, healthIssue{
				Severity: "warning",
				Code:     "ConsumerPaused",
				Message:  "consumer " + name + " paused awaiting operator resume",
			})
		}
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Message < issues[j].Message })

	doc := loomHealthDoc{
		Key:         h.key(),
		Component:   "loom",
		Instance:    h.instance,
		Version:     healthVersion,
		Status:      aggregateStatus(status, issues),
		HeartbeatAt: substrate.FormatTimestamp(now),
		StartedAt:   substrate.FormatTimestamp(h.startedAt),
		Uptime:      formatISODuration(now.Sub(h.startedAt)),
		Metrics:     metrics,
		Issues:      issues,
	}
	data, err := json.Marshal(doc)
	if err != nil {
		h.logger.Error("loom heartbeat marshal", "err", err)
		return
	}
	if _, err := h.conn.KVPutWithTTL(ctx, h.bucket, h.key(), data, h.heartbeatTTL()); err != nil {
		h.logger.Warn("loom heartbeat put", "err", err, "key", h.key())
	}
}

func (h *heartbeater) key() string {
	return "health.loom." + h.instance
}

// aggregateStatus reconciles the reported lifecycle phase with the open issue
// set per Contract #5 §5.2/§5.3: issues are empty iff healthy, "warning" ⇒
// "degraded", "error" ⇒ "unhealthy". The "starting" and "shutdown" phases are
// returned unchanged — an initializing or draining Loom reports its lifecycle
// phase, not a steady-state health grade. Mirrors the Processor/Weaver/Refractor
// heartbeaters so a heartbeat carrying issues can never self-report "healthy".
func aggregateStatus(lifecycle string, issues []healthIssue) string {
	if lifecycle == "starting" || lifecycle == "shutdown" {
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

// formatISODuration renders a duration as an ISO 8601 duration (e.g. "PT2M30S").
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
	hrs := seconds / 3600
	rem := seconds % 3600
	return "PT" + itoa(hrs) + "H" + itoa(rem/60) + "M" + itoa(rem%60) + "S"
}
