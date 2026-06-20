package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// healthVersion is the bridge build version reported in the Contract #5 heartbeat.
const healthVersion = "0.1.0"

// defaultHeartbeatEvery is the Contract #5 §5.6 / NFR-O1 heartbeat cadence floor.
const defaultHeartbeatEvery = 10 * time.Second

// Health issue codes the dispatch path raises (Contract #5 §5.2). An
// unregistered adapter / unparseable envelope / a Pending outcome with no
// dispatchOp configured is errConfig (severity error, redelivery can never fix
// it); a transient adapter failure, a replyOp/dispatchOp publish failure, and a
// skip-probe Core KV failure are warnings (redelivery will re-drive them on the
// same idempotencyKey, so a sustained outage is observable without being treated
// as fatal).
const (
	codeAdapterMissing    = "BridgeAdapterMissing"
	codeAdapterFailed     = "BridgeAdapterFailed"
	codeEventUnparseable  = "BridgeEventUnparseable"
	codeReplyPublishFail  = "BridgeReplyPublishFailed"
	codeSkipProbeFailed   = "BridgeSkipProbeFailed"
	codeDispatchOpMissing = "BridgeDispatchOpMissing"
	severityError         = "error"
	severityWarning       = "warning"
)

// bridgeHealthDoc is the Contract #5 §5.2 heartbeat document the bridge writes
// to health.bridge.<instance>. Same shape as the Loom/Processor docs; component
// is "bridge".
type bridgeHealthDoc struct {
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

// issueCache holds the bridge's active dispatch-time alerts (an unregistered
// adapter, an unparseable envelope, a transient adapter failure), keyed so a
// condition that resolves clears its own entry. The heartbeater surfaces the
// snapshot as Contract #5 issues — the "never silently drop" surface (an
// errConfig event is acked, but its issue is always raised). An adapter-failure
// issue clears on the next successful dispatch of the same adapter.
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

// consumerStateCache holds the last-known pause/active state of the bridge's
// managed consumer, fed from the per-consumer HealthSink writes the supervisor
// drives. The supervisor persists state through the caller's sink but exposes no
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

// consumerHealthEntry is the bridge's minimal per-consumer pause-state document,
// stored under health.bridge.<instance>.consumer.<name> in the health-kv bucket
// — a SEPARATE, smaller shape from the Contract #5 heartbeat document. It
// carries only the fields HealthSink.Load needs to restore pause state across a
// restart.
type consumerHealthEntry struct {
	Status      string `json:"status"`                // "active" | "paused"
	PauseReason string `json:"pauseReason,omitempty"` // "infra" | "structural" | "manual"
	LastError   string `json:"lastError,omitempty"`
}

// consumerHealthSink implements substrate.HealthSink for the bridge's managed
// consumer. Every supervisor transition is funnelled through this sink: it
// persists to health-kv AND updates the engine's in-memory consumer-state cache,
// which the Contract #5 heartbeater reads to populate metrics.consumers.
type consumerHealthSink struct {
	conn   *substrate.Conn
	bucket string
	key    string
	name   string
	states *consumerStateCache
}

func newConsumerHealthSink(conn *substrate.Conn, bucket, instance, name string, states *consumerStateCache) *consumerHealthSink {
	return &consumerHealthSink{
		conn:   conn,
		bucket: bucket,
		key:    "health.bridge." + instance + ".consumer." + name,
		name:   name,
		states: states,
	}
}

func (s *consumerHealthSink) SetActive(ctx context.Context) error {
	s.states.set(s.name, consumerState(false, ""))
	return s.put(ctx, consumerHealthEntry{Status: "active"})
}

func (s *consumerHealthSink) SetPaused(ctx context.Context, reason substrate.PauseReason, lastErr string) error {
	s.states.set(s.name, consumerState(true, reason))
	return s.put(ctx, consumerHealthEntry{
		Status:      "paused",
		PauseReason: string(reason),
		LastError:   lastErr,
	})
}

// Load restores the persisted pause state at supervisor Add time. A missing or
// malformed entry resolves to (StatusActive, "", nil) per the HealthSink
// contract. It also seeds the in-memory state cache with the restored state.
func (s *consumerHealthSink) Load(ctx context.Context) (substrate.HealthStatus, substrate.PauseReason, error) {
	entry, err := s.conn.KVGet(ctx, s.bucket, s.key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			s.states.set(s.name, consumerState(false, ""))
			return substrate.StatusActive, "", nil
		}
		return substrate.StatusActive, "", err
	}
	var doc consumerHealthEntry
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		s.states.set(s.name, consumerState(false, ""))
		return substrate.StatusActive, "", nil
	}
	if doc.Status != "paused" {
		s.states.set(s.name, consumerState(false, ""))
		return substrate.StatusActive, "", nil
	}
	reason := pauseReasonFromString(doc.PauseReason)
	s.states.set(s.name, consumerState(true, reason))
	return substrate.StatusPaused, reason, nil
}

func (s *consumerHealthSink) put(ctx context.Context, entry consumerHealthEntry) error {
	body, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = s.conn.KVPut(ctx, s.bucket, s.key, body)
	return err
}

func pauseReasonFromString(s string) substrate.PauseReason {
	switch s {
	case string(substrate.PauseManual):
		return substrate.PauseManual
	case string(substrate.PauseStructural):
		return substrate.PauseStructural
	default:
		return substrate.PauseInfra
	}
}

// heartbeater writes the Contract #5 health.bridge.<instance> document on a
// ticker. metrics carry the dispatch counters and a per-consumer state map read
// from the consumer-state cache; issues are snapshotted from the issue cache and
// the consumer pause-state.
type heartbeater struct {
	conn      *substrate.Conn
	bucket    string
	instance  string
	startedAt time.Time
	interval  time.Duration
	states    *consumerStateCache
	issues    *issueCache
	metrics   *dispatchMetrics
	logger    *slog.Logger
}

func newHeartbeater(conn *substrate.Conn, healthBucket, instance string, every time.Duration, states *consumerStateCache, issues *issueCache, metrics *dispatchMetrics, logger *slog.Logger) *heartbeater {
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
		metrics:   metrics,
		logger:    logger,
	}
}

// run blocks until ctx is cancelled, emitting one heartbeat immediately and then
// on each tick. A final "shutdown" heartbeat is emitted on ctx cancel.
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
	for k, v := range h.metrics.snapshot() {
		metrics[k] = v
	}

	issues := h.issues.snapshot()
	for name, state := range states {
		if state == "pausedStructural" {
			issues = append(issues, healthIssue{
				Severity: severityWarning,
				Code:     "ConsumerPaused",
				Message:  "consumer " + name + " paused awaiting operator resume",
			})
		}
	}

	doc := bridgeHealthDoc{
		Key:         h.key(),
		Component:   "bridge",
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
		h.logger.Error("bridge heartbeat marshal", "err", err)
		return
	}
	if _, err := h.conn.KVPut(ctx, h.bucket, h.key(), data); err != nil {
		h.logger.Warn("bridge heartbeat put", "err", err, "key", h.key())
	}
}

func (h *heartbeater) key() string {
	return "health.bridge." + h.instance
}

// dispatchMetrics holds the bridge's per-process dispatch counters surfaced on
// the Contract #5 heartbeat (metrics.dispatched / pending / skipped /
// adapterErrors). dispatched counts terminal (Resolved) replyOp posts; pending
// counts pending-marker (dispatchOp) posts for calls the vendor has not yet
// resolved.
type dispatchMetrics struct {
	mu            sync.Mutex
	dispatched    int64
	pending       int64
	skipped       int64
	adapterErrors int64
}

func newDispatchMetrics() *dispatchMetrics { return &dispatchMetrics{} }

func (m *dispatchMetrics) incDispatched()    { m.mu.Lock(); m.dispatched++; m.mu.Unlock() }
func (m *dispatchMetrics) incPending()       { m.mu.Lock(); m.pending++; m.mu.Unlock() }
func (m *dispatchMetrics) incSkipped()       { m.mu.Lock(); m.skipped++; m.mu.Unlock() }
func (m *dispatchMetrics) incAdapterErrors() { m.mu.Lock(); m.adapterErrors++; m.mu.Unlock() }

func (m *dispatchMetrics) snapshot() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return map[string]any{
		"dispatched":    m.dispatched,
		"pending":       m.pending,
		"skipped":       m.skipped,
		"adapterErrors": m.adapterErrors,
	}
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
