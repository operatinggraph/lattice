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

	"github.com/operatinggraph/lattice/internal/gateway/auth"
	"github.com/operatinggraph/lattice/internal/gateway/revocation"
	"github.com/operatinggraph/lattice/internal/healthkv"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// healthVersion is the Gateway build version reported in the Contract #5 heartbeat.
const healthVersion = "0.1.0"

// DefaultHeartbeatEvery is the Contract #5 §5.6 / NFR-O1 heartbeat cadence floor.
const DefaultHeartbeatEvery = 10 * time.Second

// heartbeatTTL is the per-write NATS-TTL the heartbeat key is re-armed with:
// the Contract #5 §5.6 interval-derived convention every other emitter already
// shares (healthkv.DefaultTTLMultiplier), so a Gateway instance that stops
// heartbeating ages out of Health KV instead of lingering as a stale component.
func (h *Heartbeater) heartbeatTTL() time.Duration {
	return h.every * healthkv.DefaultTTLMultiplier
}

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
	Jwks        jwksBlock       `json:"jwks"`
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

// jwksBlock is the JWT trust-key set's live-state summary — the Loupe F11
// JWKS panel's read surface, mirroring the revocation block. keys lists the
// current trusted set's provenance; lastPoll/swaps describe the background
// JWKSPoller's activity (both zero-valued when no JWKS URL is configured —
// a static/dev-only Gateway still reports its trusted keys, just with no
// poller behind them).
type jwksBlock struct {
	Keys     []jwksKeyEntry `json:"keys"`
	LastPoll string         `json:"lastPoll"`
	Swaps    uint64         `json:"swaps"`
}

type jwksKeyEntry struct {
	Kid     string `json:"kid"`
	Source  string `json:"source"`
	Alg     string `json:"alg"`
	AddedAt string `json:"addedAt"`
}

type healthIssue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Since    string `json:"since"`
}

// issueCache holds the Gateway's active operational alerts (e.g. the
// revocation kill-switch running disabled because its bucket never opened) —
// keyed so a condition that resolves clears its own entry. The Heartbeater
// surfaces the snapshot as Contract #5 issues, feeding aggregateStatus so a
// heartbeat carrying an issue can never self-report "healthy". Mirrors the
// bridge's issueCache. since tracks each key's first-arose timestamp
// (Contract #5 §5.5) so it persists across heartbeats while the issue stays
// open, and clears with the issue so a later re-occurrence gets a fresh since
// rather than reusing the stale one.
type issueCache struct {
	mu     sync.Mutex
	issues map[string]healthIssue
	since  map[string]string
}

func newIssueCache() *issueCache {
	return &issueCache{issues: make(map[string]healthIssue), since: make(map[string]string)}
}

func (c *issueCache) set(key, severity, code, message string) {
	c.mu.Lock()
	since, ok := c.since[key]
	if !ok {
		since = substrate.FormatTimestamp(time.Now())
		c.since[key] = since
	}
	c.issues[key] = healthIssue{Severity: severity, Code: code, Message: message, Since: since}
	c.mu.Unlock()
}

func (c *issueCache) clear(key string) {
	c.mu.Lock()
	delete(c.issues, key)
	delete(c.since, key)
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
	readsTotal        atomic.Int64
	readFailuresTotal atomic.Int64
}

func (m *Metrics) snapshot() map[string]any {
	return map[string]any{
		"requests_total":      m.requestsTotal.Load(),
		"auth_failures_total": m.authFailuresTotal.Load(),
		"ops_submitted_total": m.opsSubmittedTotal.Load(),
		"reads_total":         m.readsTotal.Load(),
		"read_failures_total": m.readFailuresTotal.Load(),
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

	// jwks* back the jwks heartbeat block — set once via SetJWKSSource after
	// both the Verifier and (if a JWKS URL is configured) the JWKSPoller
	// exist. jwksVerifier nil means jwksSnapshot reports an empty block (no
	// JWT auth wired at all, e.g. some test harnesses); jwksPoller nil means
	// "no JWKS URL configured" — keys still reflect the Verifier's
	// static/dev-only trusted set, with lastPoll/swaps left at zero.
	jwksVerifier *auth.Verifier
	jwksPoller   *auth.JWKSPoller
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

// SetJWKSSource attaches the JWT Verifier (and, if JWKS polling is
// configured, its JWKSPoller) whose state jwksSnapshot reports. poller may be
// nil (static/dev-only trust, no live polling). Call once, before Run — not
// safe to change concurrently with emit().
func (h *Heartbeater) SetJWKSSource(verifier *auth.Verifier, poller *auth.JWKSPoller) {
	h.jwksVerifier = verifier
	h.jwksPoller = poller
}

// jwksSnapshot builds the current jwks heartbeat block from the attached
// Verifier/JWKSPoller (see SetJWKSSource). Returns a zero-value block if no
// Verifier is attached.
func (h *Heartbeater) jwksSnapshot() jwksBlock {
	if h.jwksVerifier == nil {
		return jwksBlock{}
	}
	info := h.jwksVerifier.Info()
	kids := make([]string, 0, len(info))
	for kid := range info {
		kids = append(kids, kid)
	}
	sort.Strings(kids)
	entries := make([]jwksKeyEntry, 0, len(kids))
	for _, kid := range kids {
		ki := info[kid]
		addedAt := ""
		if !ki.AddedAt.IsZero() {
			addedAt = ki.AddedAt.UTC().Format(time.RFC3339)
		}
		entries = append(entries, jwksKeyEntry{Kid: kid, Source: ki.Source, Alg: ki.Alg, AddedAt: addedAt})
	}
	block := jwksBlock{Keys: entries}
	if h.jwksPoller != nil {
		block.Swaps = h.jwksPoller.Swaps()
		if lp := h.jwksPoller.LastPollAt(); !lp.IsZero() {
			block.LastPoll = lp.UTC().Format(time.RFC3339)
		}
	}
	return block
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
		Jwks:        h.jwksSnapshot(),
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		h.logger.Error("gateway: marshal heartbeat", "error", err)
		return
	}
	// TTL-armed like every other emitter (healthkv.Reporter, the Weaver
	// heartbeater): the Contract #5 §5.6 derived TTL = interval ×
	// DefaultTTLMultiplier, re-armed on each write. An untimed KVPut leaks one
	// permanent health.gateway.<instance> key per restart, and the rollup counts
	// every leaked key as a stale component — enough of them and overall health
	// can never read green again.
	if _, err := h.conn.KVPutWithTTL(ctx, h.bucket, doc.Key, raw, h.heartbeatTTL()); err != nil {
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
