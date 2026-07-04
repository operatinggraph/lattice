package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asolgan/lattice/internal/gateway/revocation"
	"github.com/asolgan/lattice/internal/substrate"
)

// healthVersion is the Gateway build version reported in the Contract #5 heartbeat.
const healthVersion = "0.1.0"

// DefaultHeartbeatEvery is the Contract #5 §5.6 / NFR-O1 heartbeat cadence floor.
const DefaultHeartbeatEvery = 10 * time.Second

const (
	severityError   = "error"
	severityWarning = "warning"
)

// healthDoc is the Contract #5 §5.2 heartbeat document the Gateway writes to
// health.gateway.<instance>. Same shape as the Loom/Bridge/Processor docs;
// component is "gateway" (already reserved by Contract #5 §5.1 as a Phase 2+
// component name).
type healthDoc struct {
	Key         string          `json:"key"`
	Component   string          `json:"component"`
	Instance    string          `json:"instance"`
	Version     string          `json:"version"`
	Status      string          `json:"status"`
	HeartbeatAt string          `json:"heartbeatAt"`
	StartedAt   string          `json:"startedAt"`
	Uptime      string          `json:"uptime"`
	Metrics     map[string]any  `json:"metrics"`
	Issues      []healthIssue   `json:"issues"`
	Revocation  revocationBlock `json:"revocation"`
}

// revocationBlock is the token-revocation kill-switch's live-state summary
// (gateway-token-revocation-activation-design.md §2.6) — the Loupe F11
// revoke-panel's read surface. consumerConnected reflects the materializer's
// events.gateway.> consumer; the other three fields describe the local
// token-revocation bucket it maintains.
type revocationBlock struct {
	ConsumerConnected bool   `json:"consumerConnected"`
	RevokedCount      int    `json:"revokedCount"`
	LastEventSeq      uint64 `json:"lastEventSeq"`
	LastSyncAt        string `json:"lastSyncAt"`
}

type healthIssue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// issueCache holds the Gateway's active operational alerts (e.g. the
// revocation kill-switch running disabled because its bucket never opened) —
// keyed so a condition that resolves clears its own entry. The Heartbeater
// surfaces the snapshot as Contract #5 issues, feeding aggregateStatus so a
// heartbeat carrying an issue can never self-report "healthy". Mirrors the
// bridge's issueCache.
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
	issues    *issueCache
	every     time.Duration
	startedAt time.Time
	logger    *slog.Logger
	now       func() time.Time

	// revocation* track the token-revocation materializer's live state
	// (§2.6) — set by StartRevocationMaterializer's consumer/health-sink,
	// read by emit() on every heartbeat. revocationConnected defaults to
	// true in NewHeartbeater (assumed-connected until proven otherwise);
	// revocationLastSeq/revocationLastSync default to zero ("never synced")
	// until the materializer's first fold.
	revocationConnected atomic.Bool
	revocationLastSeq   atomic.Uint64
	revocationLastSync  atomic.Int64 // UnixNano; 0 = never synced
}

// NewHeartbeater builds a Heartbeater. bucket is the Health KV bucket name
// (config.yaml health.bucketName); instance is the stable per-process
// identifier (Contract #5 §5.1 convention: "gw-<NanoID>").
func NewHeartbeater(conn *substrate.Conn, bucket, instance string, metrics *Metrics, logger *slog.Logger) *Heartbeater {
	if logger == nil {
		logger = slog.Default()
	}
	hb := &Heartbeater{
		conn:      conn,
		bucket:    bucket,
		instance:  instance,
		metrics:   metrics,
		issues:    newIssueCache(),
		every:     DefaultHeartbeatEvery,
		startedAt: time.Now(),
		logger:    logger,
		now:       time.Now,
	}
	// Assumed connected until a materializer's health sink reports otherwise
	// (mirrors heartbeatIssueSink.Load's "no persisted pause ⇒ active"
	// default) — the ConsumerSupervisor only calls SetActive/SetPaused on a
	// state *transition*, never on a steady-state healthy pump, so a
	// zero-value false default would wrongly read "disconnected" for the
	// entire life of a materializer that never once failed.
	hb.revocationConnected.Store(true)
	return hb
}

// SetIssue records an active Contract #5 issue under key, surfaced on every
// heartbeat until ClearIssue(key) is called. Safe for concurrent use with Run.
func (h *Heartbeater) SetIssue(key, severity, code, message string) {
	h.issues.set(key, severity, code, message)
}

// ClearIssue removes a previously-set issue; a no-op if key isn't set.
func (h *Heartbeater) ClearIssue(key string) {
	h.issues.clear(key)
}

// SetRevocationConnected records the token-revocation materializer's
// events.gateway.> consumer connectivity, surfaced in the next heartbeat's
// revocation.consumerConnected field. Safe for concurrent use with Run.
func (h *Heartbeater) SetRevocationConnected(connected bool) {
	h.revocationConnected.Store(connected)
}

// RecordRevocationSync marks a successful revoke/unrevoke fold at the given
// backing-stream sequence, surfaced in the next heartbeat's
// revocation.lastEventSeq/lastSyncAt fields. Safe for concurrent use with Run.
func (h *Heartbeater) RecordRevocationSync(seq uint64, at time.Time) {
	h.revocationLastSeq.Store(seq)
	h.revocationLastSync.Store(at.UnixNano())
}

// revocationSnapshot builds the current revocation heartbeat block.
// revokedCount is scanned live off the token-revocation bucket (a compacting,
// latest-per-actor set — cheap even at heartbeat cadence, mirroring Loom's
// running-instance heartbeat counter). A scan failure (e.g. the bucket isn't
// open in a test/degraded process) reports 0 rather than failing the
// heartbeat — this is an observability field, not the fail-closed check
// (that stays revocation.Checker's per-request read).
func (h *Heartbeater) revocationSnapshot(ctx context.Context) revocationBlock {
	count := 0
	if keys, err := h.conn.KVListKeys(ctx, revocation.BucketName); err == nil {
		count = len(keys)
	}
	lastSync := ""
	if ns := h.revocationLastSync.Load(); ns != 0 {
		lastSync = time.Unix(0, ns).UTC().Format(time.RFC3339)
	}
	return revocationBlock{
		ConsumerConnected: h.revocationConnected.Load(),
		RevokedCount:      count,
		LastEventSeq:      h.revocationLastSeq.Load(),
		LastSyncAt:        lastSync,
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
	issues := h.issues.snapshot()
	doc := healthDoc{
		Key:         h.key(),
		Component:   "gateway",
		Instance:    h.instance,
		Version:     healthVersion,
		Status:      aggregateStatus(status, issues),
		HeartbeatAt: now.UTC().Format(time.RFC3339),
		StartedAt:   h.startedAt.UTC().Format(time.RFC3339),
		Uptime:      formatUptime(now.Sub(h.startedAt)),
		Metrics:     h.metrics.snapshot(),
		Issues:      issues,
		Revocation:  h.revocationSnapshot(ctx),
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

// aggregateStatus reconciles the reported lifecycle phase with the open issue
// set per Contract #5 §5.2/§5.3: issues are empty iff healthy, "warning" ⇒
// "degraded", "error" ⇒ "unhealthy". Mirrors the Loom/Weaver/Bridge/Processor
// heartbeaters so a heartbeat carrying issues can never self-report "healthy".
func aggregateStatus(lifecycle string, issues []healthIssue) string {
	if lifecycle == "starting" || lifecycle == "shutdown" {
		return lifecycle
	}
	worst := lifecycle
	for _, is := range issues {
		switch is.Severity {
		case severityError:
			return "unhealthy"
		case severityWarning:
			worst = "degraded"
		}
	}
	return worst
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
