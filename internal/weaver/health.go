package weaver

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// healthVersion is the Weaver build version reported in the Contract #5 heartbeat.
const healthVersion = "0.1.0"

// defaultHeartbeatEvery is the Contract #5 §5.6 / NFR-O1 heartbeat cadence floor.
const defaultHeartbeatEvery = 10 * time.Second

// weaverHealthDoc is the Contract #5 §5.2 heartbeat document Weaver writes to
// health.weaver.<instance>. Same shape as the Processor/Refractor/Loom docs;
// component is "weaver".
type weaverHealthDoc struct {
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

// issueCache holds the engine's active config/data-error alerts (rejected
// targets, unknown gap columns, template data errors), keyed so a condition
// that resolves clears its own entry. The heartbeater surfaces the snapshot as
// Contract #5 issues — the FR29 "never silently drop" surface.
type issueCache struct {
	mu     sync.Mutex
	issues map[string]healthIssue
}

func newIssueCache() *issueCache {
	return &issueCache{issues: make(map[string]healthIssue)}
}

func (c *issueCache) set(key, severity, code, message string) {
	c.mu.Lock()
	c.issues[key] = healthIssue{Severity: severity, Code: code, Message: message}
	c.mu.Unlock()
}

func (c *issueCache) clear(key string) {
	c.mu.Lock()
	delete(c.issues, key)
	c.mu.Unlock()
}

// snapshot returns the active issues in deterministic (key) order.
func (c *issueCache) snapshot() []healthIssue {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.issues))
	for k := range c.issues {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]healthIssue, 0, len(keys))
	for _, k := range keys {
		out = append(out, c.issues[k])
	}
	return out
}

// consumerStateCache holds the last-known pause/active state of every managed
// consumer, fed from the per-consumer HealthSink writes the supervisor drives.
// The supervisor persists state through the caller's sink but exposes no
// read-back accessor, so the engine caches each transition and the heartbeater
// reads this cache to populate metrics.consumers (no supervisor re-query, no
// per-message KV scan).
type consumerStateCache struct {
	mu     sync.Mutex
	states map[string]string
}

func newConsumerStateCache() *consumerStateCache {
	return &consumerStateCache{states: make(map[string]string)}
}

func (c *consumerStateCache) set(name, state string) {
	c.mu.Lock()
	c.states[name] = state
	c.mu.Unlock()
}

func (c *consumerStateCache) delete(name string) {
	c.mu.Lock()
	delete(c.states, name)
	c.mu.Unlock()
}

func (c *consumerStateCache) snapshot() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.states))
	for k, v := range c.states {
		out[k] = v
	}
	return out
}

// consumerState renders a pause reason to the metrics.consumers state string.
func consumerState(paused bool, reason substrate.PauseReason) string {
	if !paused {
		return "running"
	}
	switch reason {
	case substrate.PauseManual:
		return "pausedManual"
	case substrate.PauseStructural:
		return "pausedStructural"
	case substrate.PauseInfra:
		return "pausedInfra"
	default:
		return "paused"
	}
}

// heartbeater writes the Contract #5 health.weaver.<instance> document on a
// ticker. Metrics carry the per-consumer state map, the registered-target
// count, the in-flight mark count (a heartbeat-cadence weaver-state scan,
// never per-message), the reconciler sweep counters, and the lane-3 temporal
// counters. Issues carry pausedStructural consumers plus the active
// config/data-error alerts.
type heartbeater struct {
	conn      *substrate.Conn
	bucket    string
	instance  string
	startedAt time.Time
	interval  time.Duration
	states    *consumerStateCache
	issues    *issueCache
	source    *targetSource
	marks     *markStore
	sweep     *sweeper
	temporal  *temporalStats
	logger    *slog.Logger
}

func newHeartbeater(conn *substrate.Conn, healthBucket, instance string, every time.Duration,
	states *consumerStateCache, issues *issueCache, source *targetSource, marks *markStore,
	sweep *sweeper, temporal *temporalStats, logger *slog.Logger) *heartbeater {
	if logger == nil {
		logger = slog.Default()
	}
	if every <= 0 {
		every = defaultHeartbeatEvery
	}
	return &heartbeater{
		conn:      conn,
		bucket:    healthBucket,
		instance:  instance,
		startedAt: time.Now(),
		interval:  every,
		states:    states,
		issues:    issues,
		source:    source,
		marks:     marks,
		sweep:     sweep,
		temporal:  temporal,
		logger:    logger,
	}
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
	// Heartbeat-cadence sweep of the registry's pending-spec buffer: a spec
	// aspect stuck waiting for its parent vertex's class past the bound is an
	// orphaned spec (config error) and must surface, never sit silent.
	h.source.flagOrphanedSpecs()
	states := h.states.snapshot()

	metrics := map[string]any{
		"consumers": states,
		"targets":   h.source.targetCount(),
	}
	if n, err := h.marks.countInFlight(ctx); err == nil {
		metrics["marksInFlight"] = n
	} else {
		h.logger.Warn("weaver heartbeat: in-flight mark scan failed", "err", err)
	}
	if h.sweep != nil {
		reclaims, orphans, corrupt, lastRun := h.sweep.metrics()
		metrics["sweepReclaims"] = reclaims
		metrics["sweepOrphansDeleted"] = orphans
		metrics["sweepCorrupt"] = corrupt
		if !lastRun.IsZero() {
			metrics["sweepLastRunAt"] = substrate.FormatTimestamp(lastRun)
		}
	}
	if h.temporal != nil {
		metrics["timersScheduled"] = h.temporal.scheduled.Load()
		metrics["timersFired"] = h.temporal.fired.Load()
	}

	issues := h.issues.snapshot()
	for name, state := range states {
		if state == "pausedStructural" {
			issues = append(issues, healthIssue{
				Severity: "warning",
				Code:     "ConsumerPaused",
				Message:  "consumer " + name + " paused awaiting operator resume",
			})
		}
	}

	// Contract #5 §5.2/§5.3: a heartbeat carrying issues must not report
	// status:"healthy" (issues is empty iff healthy). Escalate the lifecycle
	// status to the worst issue severity — any error ⇒ unhealthy, any warning ⇒
	// degraded — so an open data/config error or a structurally-paused consumer
	// surfaces honestly instead of false-healthy. The "starting" and "shutdown"
	// lifecycle phases are reported verbatim (a draining/initializing component
	// isn't "degraded").
	status = aggregateStatus(status, issues)

	doc := weaverHealthDoc{
		Key:         h.key(),
		Component:   "weaver",
		Instance:    h.instance,
		Version:     healthVersion,
		Status:      status,
		HeartbeatAt: substrate.FormatTimestamp(now),
		StartedAt:   substrate.FormatTimestamp(h.startedAt),
		Uptime:      formatISODuration(now.Sub(h.startedAt)),
		Metrics:     metrics,
		Issues:      issues,
	}
	data, err := json.Marshal(doc)
	if err != nil {
		h.logger.Error("weaver heartbeat marshal", "err", err)
		return
	}
	if _, err := h.conn.KVPut(ctx, h.bucket, h.key(), data); err != nil {
		h.logger.Warn("weaver heartbeat put", "err", err, "key", h.key())
	}
}

func (h *heartbeater) key() string {
	return "health.weaver." + h.instance
}

// aggregateStatus reconciles the reported lifecycle status with the open issue
// set per Contract #5 §5.3: any "error" issue ⇒ "unhealthy", otherwise any
// "warning" issue ⇒ "degraded", otherwise the lifecycle status is kept. The
// "starting" and "shutdown" phases are returned unchanged — an initializing or
// draining component reports its lifecycle phase, not a steady-state health
// grade, even if transient issues are present.
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
