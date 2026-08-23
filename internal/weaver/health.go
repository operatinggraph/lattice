package weaver

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/healthkv"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// healthVersion is the Weaver build version reported in the Contract #5 heartbeat.
const healthVersion = "0.1.0"

// defaultHeartbeatEvery is the Contract #5 §5.6 / NFR-O1 heartbeat cadence floor.
const defaultHeartbeatEvery = 10 * time.Second

// maxHeartbeatIssues caps how many issue entries one heartbeat document lists.
//
// The whole document is a single Health-KV value, and two of the issue classes
// Weaver raises are per-ENTITY: a `surface` gap standing open (one per
// violating subject — every unrouted task past its expiresAt, every erasure
// stuck mid-flight) and GapBudgetExhausted. Both are unbounded in entity count,
// so an unbounded issues[] would grow the value without limit. The listing is
// bounded instead; the aggregate status is computed over EVERY open issue
// before the cut, so truncation never makes the heartbeat read healthier than
// it is, and the truncation entry names the total so a bounded list is never
// readable as the whole set (internal/pkgmgr's sampleWithOverflow rule, applied
// to entries rather than to a message's key sample).
const maxHeartbeatIssues = 50

// issuesTruncatedCode is the synthetic entry appended in place of the issues a
// heartbeat did not list.
const issuesTruncatedCode = "IssuesTruncated"

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
	Since    string `json:"since"`
}

// issueCache holds the engine's active config/data-error alerts (rejected
// targets, unknown gap columns, template data errors), keyed so a condition
// that resolves clears its own entry. The heartbeater surfaces the snapshot as
// Contract #5 issues — the FR29 "never silently drop" surface. since tracks
// each key's first-arose timestamp (Contract #5 §5.5) so it persists across
// heartbeats while the issue stays open, and clears with the issue so a later
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

// heartbeater writes the Contract #5 health.weaver.<instance> document on a
// ticker. Metrics carry the per-consumer state map, the registered-target
// count, the in-flight mark count (a heartbeat-cadence weaver-state scan,
// never per-message), the reconciler sweep counters, and the lane-3 temporal
// counters. Issues carry pausedStructural consumers plus the active
// config/data-error alerts.
type heartbeater struct {
	conn        *substrate.Conn
	bucket      string
	instance    string
	startedAt   time.Time
	interval    time.Duration
	states      *healthkv.ConsumerStateCache
	issues      *issueCache
	source      *targetSource
	marks       *markStore
	sweep       *sweeper
	temporal    *temporalStats
	shadow      *shadowStats
	contraction *contractionStats
	admission   *admissionScheduler
	logger      *slog.Logger

	// ttlMultiplier derives the heartbeat's Health-KV TTL (interval ×
	// ttlMultiplier, Contract #5 §5.6). Zero disables TTL.
	ttlMultiplier int

	// effectMismatchAlerted tracks the `__effect` confidence windows currently
	// carrying a standing LensEffectMismatch issue, so a window that recovers
	// (a close finally arrives, or the window ages below effectWindowSize) has
	// its issue cleared on the pass that no longer lists it. Owned solely by
	// emit, which only ever runs on the single heartbeat ticker goroutine — no
	// lock needed.
	effectMismatchAlerted map[string]struct{}

	// consumerPausedSince tracks each pausedStructural consumer's first-arose
	// timestamp (Contract #5 §5.5), since the ConsumerPaused issue is built
	// inline from live consumer state rather than through issueCache. Owned
	// solely by emit (single heartbeat ticker goroutine — no lock needed); a
	// consumer no longer paused is dropped so a later pause gets a fresh since.
	consumerPausedSince map[string]string
}

func newHeartbeater(conn *substrate.Conn, healthBucket, instance string, every time.Duration,
	states *healthkv.ConsumerStateCache, issues *issueCache, source *targetSource, marks *markStore,
	sweep *sweeper, temporal *temporalStats, shadow *shadowStats, contraction *contractionStats,
	admission *admissionScheduler, logger *slog.Logger) *heartbeater {
	if logger == nil {
		logger = slog.Default()
	}
	if every <= 0 {
		every = defaultHeartbeatEvery
	}
	return &heartbeater{
		conn:                  conn,
		bucket:                healthBucket,
		instance:              instance,
		startedAt:             time.Now(),
		interval:              every,
		states:                states,
		issues:                issues,
		source:                source,
		marks:                 marks,
		sweep:                 sweep,
		temporal:              temporal,
		shadow:                shadow,
		contraction:           contraction,
		admission:             admission,
		logger:                logger,
		ttlMultiplier:         healthkv.DefaultTTLMultiplier,
		effectMismatchAlerted: make(map[string]struct{}),
		consumerPausedSince:   make(map[string]string),
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
	// Heartbeat-cadence sweep of the registry's pending-spec buffer: a spec
	// aspect stuck waiting for its parent vertex's class past the bound is an
	// orphaned spec (config error) and must surface, never sit silent.
	h.source.flagOrphanedSpecs()
	states := h.states.Snapshot()

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
		reclaims, reclaimsSuppressed, orphans, corrupt, lastRun := h.sweep.metrics()
		metrics["sweepReclaims"] = reclaims
		metrics["sweepReclaimsSuppressed"] = reclaimsSuppressed
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
	h.flagEffectMismatches(ctx, metrics)
	if h.shadow != nil {
		if snap := h.shadow.snapshot(); len(snap) > 0 {
			metrics["plannerShadow"] = snap
		}
	}
	if h.contraction != nil {
		if snap := h.contraction.snapshot(); len(snap) > 0 {
			metrics["contractionTrajectory"] = snap
		}
	}
	if h.admission != nil {
		admitted, deferred := h.admission.metrics()
		if admitted > 0 || deferred > 0 {
			metrics["admissionAdmitted"] = admitted
			metrics["admissionDeferred"] = deferred
		}
	}

	issues := append(h.issues.snapshot(), h.pausedIssues(states, now)...)

	// Contract #5 §5.2/§5.3: a heartbeat carrying issues must not report
	// status:"healthy" (issues is empty iff healthy). Escalate the lifecycle
	// status to the worst issue severity — any error ⇒ unhealthy, any warning ⇒
	// degraded — so an open data/config error or a structurally-paused consumer
	// surfaces honestly instead of false-healthy. The "starting" and "shutdown"
	// lifecycle phases are reported verbatim (a draining/initializing component
	// isn't "degraded").
	//
	// Computed over the FULL issue set, before the listing is bounded: an issue
	// too far down the list to be named still decides the reported status.
	status = aggregateStatus(status, issues)
	issues = boundIssues(issues, maxHeartbeatIssues)

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
	if _, err := h.conn.KVPutWithTTL(ctx, h.bucket, h.key(), data, h.heartbeatTTL()); err != nil {
		h.logger.Warn("weaver heartbeat put", "err", err, "key", h.key())
	}
}

func (h *heartbeater) key() string {
	return "health.weaver." + h.instance
}

// pausedIssues builds the ConsumerPaused issue for each pausedStructural
// consumer, stamping+persisting each one's since (Contract #5 §5.5) in
// h.consumerPausedSince across ticks and dropping a consumer that is no longer
// paused so a later pause gets a fresh since. Pure apart from that persisted
// map — takes states/now as params so it's testable without a live conn.
func (h *heartbeater) pausedIssues(states map[string]string, now time.Time) []healthIssue {
	var issues []healthIssue
	pausedNow := make(map[string]struct{})
	for name, state := range states {
		if state == "pausedStructural" {
			pausedNow[name] = struct{}{}
			since, ok := h.consumerPausedSince[name]
			if !ok {
				since = substrate.FormatTimestamp(now)
				h.consumerPausedSince[name] = since
			}
			issues = append(issues, healthIssue{
				Severity: "warning",
				Code:     "ConsumerPaused",
				Message:  "consumer " + name + " paused awaiting operator resume",
				Since:    since,
			})
		}
	}
	for name := range h.consumerPausedSince {
		if _, ok := pausedNow[name]; !ok {
			delete(h.consumerPausedSince, name)
		}
	}
	return issues
}

// flagEffectMismatches scans every `__effect` confidence window (heartbeat
// cadence, never per-message) and raises a LensEffectMismatch issue for each
// one whose last effectWindowSize dispatch episodes recorded zero observed
// closes — the loud surface for "dispatches commit but closes never arrive"
// (design weaver-planner-mandate-design.md §3.4): a package's declared
// remediation keeps firing but the lens gap it targets never flips, which
// points at a stale/wrong guard, a lens projecting the wrong column, or a
// remediation that silently no-ops. A window that recovers (a close finally
// lands, or it ages back below the full-window threshold) has its issue
// cleared on the first pass that no longer lists it — mirrors the sweep's
// corruptAlerted reconciliation.
func (h *heartbeater) flagEffectMismatches(ctx context.Context, metrics map[string]any) {
	mismatches, err := h.marks.scanEffectMismatches(ctx)
	if err != nil {
		h.logger.Warn("weaver heartbeat: effect mismatch scan failed", "err", err)
		return
	}
	current := make(map[string]struct{}, len(mismatches))
	for _, mm := range mismatches {
		key := issueKeyEffect(mm.TargetID, mm.GapColumn, mm.ActionRef)
		current[key] = struct{}{}
		h.effectMismatchAlerted[key] = struct{}{}
		h.issues.set(key, "warning", "LensEffectMismatch",
			"target "+mm.TargetID+" gap "+mm.GapColumn+" action "+mm.ActionRef+": last "+
				strconv.Itoa(effectWindowSize)+" dispatches recorded zero observed closes")
	}
	for key := range h.effectMismatchAlerted {
		if _, ok := current[key]; !ok {
			delete(h.effectMismatchAlerted, key)
			h.issues.clear(key)
		}
	}
	metrics["effectMismatches"] = len(mismatches)
}

// boundIssues lists at most limit entries of an already deterministically
// ordered issue set and appends ONE synthetic entry standing for the rest, so a
// bounded list is never readable as the whole set. Returns issues unchanged
// when it already fits (and when limit is non-positive — a disabled cap).
//
// The synthetic entry describes the issues it replaces: its severity is theirs
// (an `error` hiding in the unlisted tail must not present as a warning), its
// since is the oldest of theirs, and its message carries both the unlisted
// count and the true total. It never substitutes for the status computation —
// callers aggregate over the full set first.
func boundIssues(issues []healthIssue, limit int) []healthIssue {
	if limit <= 0 || len(issues) <= limit {
		return issues
	}
	omitted := issues[limit:]
	out := make([]healthIssue, 0, limit+1)
	out = append(out, issues[:limit]...)
	out = append(out, healthIssue{
		Severity: worstSeverity(omitted),
		Code:     issuesTruncatedCode,
		Message: strconv.Itoa(len(omitted)) + " further open issues are not listed in this heartbeat (" +
			strconv.Itoa(len(issues)) + " open in total, " + strconv.Itoa(limit) + " listed)",
		Since: oldestSince(omitted),
	})
	return out
}

// worstSeverity collapses an issue set to the single severity that describes it,
// on the same two-way split aggregateStatus applies to the whole document: any
// "error" ⇒ "error", anything else (including a severity string this build does
// not recognise, which aggregateStatus already reads as at-least-degraded) ⇒
// "warning". Empty for an empty set.
func worstSeverity(issues []healthIssue) string {
	if len(issues) == 0 {
		return ""
	}
	for _, is := range issues {
		if is.Severity == "error" {
			return "error"
		}
	}
	return "warning"
}

// oldestSince returns the earliest first-arose stamp in the set (Contract #5
// §5.5). RFC3339Nano trims trailing zeros in the fractional part, so the stamps
// are compared as instants, not as strings; a set whose stamps are all
// unparseable falls back to the first entry's stamp verbatim rather than
// inventing one.
func oldestSince(issues []healthIssue) string {
	if len(issues) == 0 {
		return ""
	}
	since := issues[0].Since
	var oldest time.Time
	found := false
	for _, is := range issues {
		t, err := time.Parse(time.RFC3339Nano, is.Since)
		if err != nil {
			continue
		}
		if !found || t.Before(oldest) {
			oldest, found, since = t, true, is.Since
		}
	}
	return since
}

// aggregateStatus reconciles the reported lifecycle status with the open issue
// set per Contract #5 §5.3: any "error" issue ⇒ "unhealthy", otherwise any
// "warning" (or any other unrecognized non-empty severity) issue ⇒ "degraded",
// otherwise the lifecycle status is kept. Treating an unknown severity as at
// least "degraded" keeps §5.3's honesty invariant (issues empty iff healthy):
// an open issue can never leave the heartbeat reporting clean merely because its
// severity string is one this switch does not name. The "starting" and
// "shutdown" phases are returned unchanged — an initializing or draining
// component reports its lifecycle phase, not a steady-state health grade, even
// if transient issues are present.
func aggregateStatus(lifecycle string, issues []healthIssue) string {
	if lifecycle == "starting" || lifecycle == "shutdown" {
		return lifecycle
	}
	worst := lifecycle
	for _, is := range issues {
		switch is.Severity {
		case "error":
			return "unhealthy"
		default:
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
