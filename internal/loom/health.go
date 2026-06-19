package loom

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

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

// consumerStateCache holds the last-known pause/active state of every managed
// consumer, fed from the per-consumer HealthSink writes the supervisor drives.
// The supervisor persists state through the caller's sink but exposes no
// read-back accessor, so Loom caches each transition and the heartbeater reads
// this cache to populate metrics.consumers (no supervisor re-query, no
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
	states    *consumerStateCache
	counter   *runningInstanceCounter
	logger    *slog.Logger
}

func newHeartbeater(conn *substrate.Conn, healthBucket, stateBucket, instance string, every time.Duration, states *consumerStateCache, logger *slog.Logger) *heartbeater {
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
		counter:   &runningInstanceCounter{conn: conn, bucket: stateBucket, logger: logger},
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
	states := h.states.snapshot()

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

	doc := loomHealthDoc{
		Key:         h.key(),
		Component:   "loom",
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
		h.logger.Error("loom heartbeat marshal", "err", err)
		return
	}
	if _, err := h.conn.KVPut(ctx, h.bucket, h.key(), data); err != nil {
		h.logger.Warn("loom heartbeat put", "err", err, "key", h.key())
	}
}

func (h *heartbeater) key() string {
	return "health.loom." + h.instance
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
