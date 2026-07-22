// Package objectmanager is the object-store-manager — the v1b GC's Loop B (the
// byte-janitor) and the only NEW always-on component the off-graph blob plane
// needs. Byte deletion is the one off-graph side effect Weaver / Loom / the
// Processor cannot perform, so a dedicated consumer owns it.
//
// Two responsibilities, both off-graph-only (no ops submitted — the graph
// tombstone is Weaver's directOp; this just reclaims bytes):
//
//   - Loop B (the consumer): a durable consumer on core-events filtered to
//     events.object.tombstoned. For each tombstone it reads the object vertex
//     AUTHORITATIVELY from core-kv (never the lagging lens) and deletes the
//     bytes only when the vertex is gone or still tombstoned; a revived vertex
//     (re-attached) means the tombstone was superseded — skip.
//
//   - The never-attached reconcile (a low-cadence ticker): the crash-orphan
//     backstop for bytes whose AttachObject never landed. It lists the store and
//     deletes any object older than a grace window that no LIVE object vertex
//     names on its .content.storeName (the §20 exact-storeName predicate, so a
//     dedup-duplicate upload is reclaimed while the canonical bytes are spared).
//
// All NATS access is through substrate (no raw nats.go); the lens output bucket
// and Refractor-private adjacency are never read.
package objectmanager

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/operatinggraph/lattice/internal/healthkv"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	// TombstonedFilterSubject is the core-events subject the byte-janitor consumes.
	TombstonedFilterSubject = "events.object.tombstoned"
	// DefaultDurable is the byte-janitor's durable consumer name.
	DefaultDurable = "object-store-manager"

	defaultReconcileInterval = time.Hour
	// defaultReconcileGrace must exceed the worst-case AttachObject landing
	// latency (the orphan window: bytes exist, the op is in flight) AND the
	// Contract #4 24h idempotency-tracker horizon — a crash-orphaned upload whose
	// AttachObject is retried (and collapses on the tracker) within 24h must not
	// have its bytes reclaimed first. 25h clears the 24h tracker TTL with margin.
	defaultReconcileGrace = 25 * time.Hour
	redeliveryDelay       = 5 * time.Second
	heartbeatEvery        = 10 * time.Second

	// healthVersion is the object-store-manager build version reported in the
	// Contract #5 heartbeat.
	healthVersion = "0.1.0"

	severityError   = "error"
	severityWarning = "warning"
)

// Config configures the manager. CoreKVBucket / ObjectsBucket / EventsStream are
// the substrate bucket + stream names; HealthKVBucket + Instance drive the
// Contract #5 heartbeat (omit HealthKVBucket to disable it).
type Config struct {
	Conn          *substrate.Conn
	CoreKVBucket  string
	ObjectsBucket string
	EventsStream  string
	Durable       string

	// Owner-tombstone-cascade (§22). ActorKey is the object-store-manager
	// service-actor identity key (bootstrap.ObjmgrIdentityKey) the cascade
	// submits DetachObject under; empty disables the cascade (byte-janitor-only
	// mode). OpLane is the ops.<lane> to publish on (default "system");
	// CascadeDurable overrides the cascade consumer's durable name.
	ActorKey       string
	OpLane         string
	CascadeDurable string

	ReconcileInterval time.Duration
	ReconcileGrace    time.Duration

	HealthKVBucket string
	Instance       string

	Logger *slog.Logger
	// now is the clock, injectable for tests; defaults to time.Now.
	now func() time.Time
}

// Manager runs Loop B + the reconcile.
type Manager struct {
	cfg       Config
	startedAt time.Time
	issues    *issueCache
	reclaimed atomic.Int64
}

// New constructs a Manager, applying defaults for the omitted fields.
func New(cfg Config) *Manager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Durable == "" {
		cfg.Durable = DefaultDurable
	}
	if cfg.ReconcileInterval <= 0 {
		cfg.ReconcileInterval = defaultReconcileInterval
	}
	if cfg.ReconcileGrace <= 0 {
		cfg.ReconcileGrace = defaultReconcileGrace
	}
	if cfg.OpLane == "" {
		cfg.OpLane = DefaultOpLane
	}
	if cfg.CascadeDurable == "" {
		cfg.CascadeDurable = CascadeDurable
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &Manager{cfg: cfg, startedAt: cfg.now(), issues: newIssueCache()}
}

// objmgrHealthDoc is the Contract #5 §5.2 heartbeat document object-store-manager
// writes to health.object-store-manager.<instance>. Same shape as the
// Loom/Bridge/Gateway/Processor docs; component is "object-store-manager".
type objmgrHealthDoc struct {
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
	Since    string `json:"since"`
}

// issueCache holds object-store-manager's active reclaim-failure alerts (a
// stuck ObjectDelete, an unreadable object vertex) — keyed so a condition that
// resolves clears its own entry. The heartbeat surfaces the snapshot, feeding
// aggregateStatus so a heartbeat carrying an issue can never self-report
// "healthy". Mirrors the bridge/gateway issueCache. since tracks each key's
// first-arose timestamp (Contract #5 §5.5) so it persists across heartbeats
// while the issue stays open, and clears with the issue so a later
// re-occurrence gets a fresh since rather than reusing the stale one.
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

// aggregateStatus reconciles the reported lifecycle phase with the open issue
// set per Contract #5 §5.2/§5.3: issues are empty iff healthy, "warning" ⇒
// "degraded", "error" ⇒ "unhealthy". Mirrors the Loom/Weaver/Bridge/Gateway/
// Processor heartbeaters so a heartbeat carrying issues can never self-report
// "healthy".
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

// tombstonedEvent is the minimal view of an object.tombstoned core-events
// message — the business data lives under payload (read-from-body discipline).
type tombstonedEvent struct {
	Payload struct {
		ObjectKey string `json:"objectKey"`
		StoreName string `json:"storeName"`
	} `json:"payload"`
}

// Run starts the heartbeat + the reconcile ticker + the owner-tombstone-cascade
// (§22, when an ActorKey is configured), then drives the byte-janitor consumer
// (Loop B), blocking until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	go m.reconcileLoop(ctx)
	if m.cfg.HealthKVBucket != "" {
		go m.heartbeatLoop(ctx)
	}
	if m.cfg.ActorKey != "" {
		go func() {
			if err := m.runCascade(ctx); err != nil && ctx.Err() == nil {
				m.cfg.Logger.Error("object-store-manager: owner-tombstone-cascade exited", "error", err)
			}
		}()
	}
	return m.cfg.Conn.RunDurableConsumer(ctx, substrate.DurableConsumerConfig{
		Stream:          m.cfg.EventsStream,
		FilterSubject:   TombstonedFilterSubject,
		Durable:         m.cfg.Durable,
		RedeliveryDelay: redeliveryDelay,
		Logger:          m.cfg.Logger,
	}, m.handleTombstoned)
}

// handleTombstoned reclaims a tombstoned object's bytes. It reads the object
// vertex authoritatively from core-kv: only a gone-or-still-tombstoned vertex
// triggers the irreversible ObjectDelete. A revived vertex carries FRESH bytes
// under a different storeName, so the event's storeName is the abandoned one —
// skipping it here is safe (the reconcile reclaims it). The handler is
// idempotent: a redelivered tombstone re-checks and re-deletes (ObjectDelete is
// a no-op on an absent object).
func (m *Manager) handleTombstoned(ctx context.Context, msg substrate.Message) substrate.Decision {
	if len(msg.Body) == 0 {
		return substrate.Ack
	}
	var ev tombstonedEvent
	if err := json.Unmarshal(msg.Body, &ev); err != nil {
		m.cfg.Logger.Warn("object-store-manager: unparseable object.tombstoned event; dropping", "error", err)
		return substrate.Term
	}
	if ev.Payload.ObjectKey == "" || ev.Payload.StoreName == "" {
		m.cfg.Logger.Warn("object-store-manager: object.tombstoned missing objectKey/storeName; dropping",
			"objectKey", ev.Payload.ObjectKey, "storeName", ev.Payload.StoreName)
		return substrate.Term
	}

	entry, err := m.cfg.Conn.KVGet(ctx, m.cfg.CoreKVBucket, ev.Payload.ObjectKey)
	gone := errors.Is(err, substrate.ErrKeyNotFound)
	if err != nil && !gone {
		// Infra error reading the authoritative state — retry, never guess.
		return substrate.NakWithDelay
	}
	stillTombstoned := gone
	if !gone {
		var doc struct {
			IsDeleted bool `json:"isDeleted"`
		}
		if json.Unmarshal(entry.Value, &doc) == nil {
			stillTombstoned = doc.IsDeleted
		}
	}
	if !stillTombstoned {
		// Re-attached since the tombstone (fresh bytes under a new storeName) —
		// the tombstone was superseded; leave the event's storeName to the reconcile.
		m.cfg.Logger.Info("object-store-manager: object revived since tombstone; skipping byte delete",
			"objectKey", ev.Payload.ObjectKey)
		return substrate.Ack
	}
	issueKey := "tombstone-reclaim:" + ev.Payload.StoreName
	if err := m.cfg.Conn.ObjectDelete(ctx, m.cfg.ObjectsBucket, ev.Payload.StoreName); err != nil {
		if substrate.IsConnectionError(err) {
			return substrate.NakWithDelay
		}
		m.cfg.Logger.Warn("object-store-manager: ObjectDelete failed; retrying", "storeName", ev.Payload.StoreName, "error", err)
		m.issues.set(issueKey, severityWarning, "ObjectDeleteFailed",
			"tombstone reclaim stuck on "+ev.Payload.StoreName+": "+err.Error())
		return substrate.NakWithDelay
	}
	m.issues.clear(issueKey)
	m.reclaimed.Add(1)
	m.cfg.Logger.Info("object-store-manager: reclaimed object bytes",
		"objectKey", ev.Payload.ObjectKey, "storeName", ev.Payload.StoreName)
	return substrate.Ack
}

func (m *Manager) reconcileLoop(ctx context.Context) {
	t := time.NewTicker(m.cfg.ReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.Reconcile(ctx); err != nil {
				m.cfg.Logger.Warn("object-store-manager: reconcile pass failed", "error", err)
				m.issues.set("reconcile-pass", severityWarning, "ReconcileListFailed", "reconcile ObjectList failed: "+err.Error())
			} else {
				m.issues.clear("reconcile-pass")
			}
		}
	}
}

// Reconcile is the never-attached backstop: it lists the store and deletes any
// object older than the grace window that no LIVE object vertex names on its
// .content.storeName. Exported so tests can drive a single pass deterministically.
func (m *Manager) Reconcile(ctx context.Context) error {
	infos, err := m.cfg.Conn.ObjectList(ctx, m.cfg.ObjectsBucket)
	if err != nil {
		return err
	}
	cutoff := m.cfg.now().Add(-m.cfg.ReconcileGrace)
	reclaimed := 0
	for _, info := range infos {
		if info.ModTime.After(cutoff) {
			continue // within the orphan window — an AttachObject may still be in flight
		}
		if m.referencedByLiveVertex(ctx, info) {
			continue
		}
		issueKey := "reconcile-reclaim:" + info.Name
		if err := m.cfg.Conn.ObjectDelete(ctx, m.cfg.ObjectsBucket, info.Name); err != nil {
			m.cfg.Logger.Warn("object-store-manager: reconcile ObjectDelete failed", "storeName", info.Name, "error", err)
			m.issues.set(issueKey, severityWarning, "ObjectDeleteFailed", "reconcile reclaim stuck on "+info.Name+": "+err.Error())
			continue
		}
		m.issues.clear(issueKey)
		reclaimed++
	}
	m.reclaimed.Add(int64(reclaimed))
	if reclaimed > 0 {
		m.cfg.Logger.Info("object-store-manager: reconcile reclaimed never-attached bytes",
			"count", reclaimed, "scanned", len(infos))
	}
	return nil
}

// referencedByLiveVertex reports whether a LIVE object vertex names EXACTLY this
// storeName on its .content aspect (§20 C-e). The content-addressed oid is
// derived from the store object's own digest, so a dedup-duplicate upload (whose
// storeName the canonical vertex does NOT name) reads as unreferenced and is
// reclaimed, while the canonical bytes are spared. An infra read error is
// treated as "referenced" — never delete on uncertainty (the next pass retries).
func (m *Manager) referencedByLiveVertex(ctx context.Context, info substrate.ObjectInfo) bool {
	oid := substrate.SHA256NanoID("object:" + info.Digest)
	contentKey := "vtx.object." + oid + ".content"
	entry, err := m.cfg.Conn.KVGet(ctx, m.cfg.CoreKVBucket, contentKey)
	if errors.Is(err, substrate.ErrKeyNotFound) {
		return false // no vertex → unreferenced
	}
	if err != nil {
		return true // infra error — be conservative, do NOT delete this round
	}
	var doc struct {
		IsDeleted bool `json:"isDeleted"`
		Data      struct {
			StoreName string `json:"storeName"`
		} `json:"data"`
	}
	if json.Unmarshal(entry.Value, &doc) != nil {
		return true // unreadable envelope — conservative, do not delete
	}
	if doc.IsDeleted {
		return false // the vertex is tombstoned → unreferenced
	}
	return doc.Data.StoreName == info.Name
}

func (m *Manager) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	m.emitHeartbeat(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.emitHeartbeat(ctx)
		}
	}
}

// emitHeartbeat writes the Contract #5 health entry directly to Health KV (the
// sanctioned direct-write plane, Decision #4). TTL = heartbeatEvery ×
// healthkv.DefaultTTLMultiplier (§5.6) so a crashed instance's key self-expires
// instead of orphaning forever.
func (m *Manager) emitHeartbeat(ctx context.Context) {
	now := m.cfg.now()
	issues := m.issues.snapshot()
	key := "health.object-store-manager." + m.cfg.Instance
	body := objmgrHealthDoc{
		Key:         key,
		Component:   "object-store-manager",
		Instance:    m.cfg.Instance,
		Version:     healthVersion,
		Status:      aggregateStatus("healthy", issues),
		HeartbeatAt: substrate.FormatTimestamp(now),
		StartedAt:   substrate.FormatTimestamp(m.startedAt),
		Uptime:      formatISODuration(now.Sub(m.startedAt)),
		Metrics:     map[string]any{"reclaimed_total": m.reclaimed.Load()},
		Issues:      issues,
	}
	doc, err := json.Marshal(body)
	if err != nil {
		m.cfg.Logger.Error("object-store-manager: heartbeat marshal", "error", err)
		return
	}
	ttl := heartbeatEvery * healthkv.DefaultTTLMultiplier
	if _, err := m.cfg.Conn.KVPutWithTTL(ctx, m.cfg.HealthKVBucket, key, doc, ttl); err != nil {
		m.cfg.Logger.Warn("object-store-manager: heartbeat write failed", "error", err)
	}
}
