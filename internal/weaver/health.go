package weaver

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"strings"
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

// maxPerTargetRowIssues caps how many PER-ROW issue entries — the `data:` and
// `template:` families together — one target may hold in the issue cache at
// once. This is a bound on the CACHE, a different job from maxHeartbeatIssues'
// bound on the DOCUMENT: the document cap decides what one heartbeat lists,
// while this one decides what the in-memory map and the per-heartbeat sort over
// it can grow to. Both families carry an entity segment, so both are O(rows of
// the target) — a systemically broken 100k-row lens would otherwise grow the map
// and the sort without limit, for one repeated fault.
//
// The value sits well above maxHeartbeatIssues on purpose: for any target an
// operator can actually read, the visible truncation must stay the document
// cap's (which selects severity-first), with this cap engaging only where the
// population is already far past anything a heartbeat could list. Insertion past
// it is refused rather than evicting — an entry already standing is a fact with
// an age (Contract #5 §5.5), and dropping it to admit an identical newer one
// would restamp the fault as young.
const maxPerTargetRowIssues = 500

// issueCacheCappedCode is the synthetic per-target entry that stands in for the
// per-row issues the cache cap refused.
const issueCacheCappedCode = "IssueCacheCapped"

// issuePrefixCapped is the key family of the synthetic cap entries. It is
// distinct from every raised family so a cap entry can never be cleared as if it
// were a fact of its own — it is derived state, minted and retired by the cap
// arithmetic alone.
const issuePrefixCapped = "capped:"

// omittedCodeSampleCap bounds how many distinct codes the truncation entry
// names before it summarises the remainder.
const omittedCodeSampleCap = 8

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

// logPaceInterval bounds how long one key's re-derived fault may go without a
// loud log record. A parked gap's failure is re-derived once per
// defaultSweepInterval (a minute) for the dispatch-count TTL's whole life
// (dispatchCountTTLBackstopFactor × the lease, ~128h) — some 7,680 passes per
// parked (target, entity, gap), each writing an identical record that buries
// every genuine arrival around it. An hour turns that into ~128 loud records
// while never letting a standing fault go quiet in the log for longer than an
// hour; the passes in between log at Debug, so no record is ever dropped
// outright.
const logPaceInterval = time.Hour

// pacedLog is one key's log-pacing and arrival memory for the families
// Engine.alertPaced serves.
//
// arrivedAt is the durable answer to "how long has this fault stood". It cannot
// come from the latch's own since, because clear deletes that and the clears
// these families see are not repairs — clearClosedMarks empties the
// target-scoped gapConfig latch whenever ANY one entity's column stops being
// reported, so the stamp would reset roughly every pass and report a week-old
// playbook fault as seconds old.
//
// lastLoudAt is the pacing clock, refreshed on every loud emission.
// lastRaiseAt records every raise, loud or damped: it is what distinguishes a
// fault that never stopped being re-derived from one that ended and came back,
// and it is what bounds the map's age prune.
type pacedLog struct {
	severity    string
	code        string
	arrivedAt   time.Time
	lastLoudAt  time.Time
	lastRaiseAt time.Time
}

// issueCache holds the engine's active config/data-error alerts (rejected
// targets, unknown gap columns, template data errors), keyed so a condition
// that resolves clears its own entry. The heartbeater surfaces the snapshot as
// Contract #5 issues — the FR29 "never silently drop" surface. since tracks
// each key's first-arose timestamp (Contract #5 §5.5) so it persists across
// heartbeats while the issue stays open, and clears with the issue so a later
// re-occurrence gets a fresh since rather than reusing the stale one.
//
// paced is the pacing-and-arrival memory behind Engine.alertPaced, and it is
// deliberately NOT the latch. A fault the engine re-derives on a cadence cannot
// be damped by the latch's presence, nor dated from it, because the clears that
// empty the latch are not evidence the fault ended: issueKeyGapConfig is
// target-scoped, so clearClosedMarks retires it as soon as ANY one entity's
// column stops being reported, while another entity's parked gap goes on
// raising it. Both the damping and the age therefore need a memory those clears
// cannot reach, so the two maps have different lifetimes:
//
//	created                | the first paced raise at a key
//	refreshed              | lastRaiseAt every raise; lastLoudAt every loud emission
//	restamped              | arrivedAt only when the fact CHANGES (severity or code) or when
//	                       | it stopped being raised for longer than one interval
//	carried                | across clear: a clear is not evidence the fact ended, which is the whole point
//	retired (subject gone) | clearPrefix, with the issue families it tears down (entity tombstone, Revoke, reconcileConsumers)
//	retired (age)          | prunePaced at the heartbeat, past twice the interval since the last RAISE —
//	                       | such an entry has already outlived the continuity window, so its next
//	                       | raise would arrive fresh anyway, and the map stays bounded by
//	                       | "keys raised in the last two intervals"
//	restart                | empty, like the latch itself; the first raise after a restart is an arrival, which is correct
//
// rowIssues and capped are the cache BOUND (maxPerTargetRowIssues), and they are
// derived state, never facts: rowIssues counts the per-row entries each target
// currently holds, and capped records the refusals once a target is at its cap.
// Their lifetime is the counted entries' own:
//
//	created   | rowIssues on the first per-row raise for a target; capped on the first raise refused
//	counted   | rowIssues on every admitted per-row insertion, never on a refresh of a standing key
//	           | (a refresh grows nothing); capped on every refused raise, so it counts RAISE
//	           | ATTEMPTS, not distinct rows — the refused keys are exactly what is not tracked
//	decounted | clear and clearPrefix, per entry actually removed
//	retired   | rowIssues when the target's count reaches zero; capped the moment the count
//	           | drops back under the cap — the refused raises are level-driven and re-arrive
//	           | on the next delivery, so a count kept past the suppression it describes would
//	           | report a suppression that is no longer happening
//	retired   | (subject gone) clearPrefix takes both with the entries it removes — an entity
//	           | tombstone, a Revoke, a registry teardown
//	restart   | empty, like the latch itself
type issueCache struct {
	mu     sync.Mutex
	issues map[string]healthIssue
	since  map[string]string
	paced  map[string]pacedLog
	// rowIssues counts the entries of the per-row families (`data:`, `template:`)
	// currently held, per targetID.
	rowIssues map[string]int
	// capped holds one overflow record per target at its cap, maintained in
	// place; snapshot renders each as a single synthetic issue.
	capped map[string]cappedRowIssues
}

// cappedRowIssues is the overflow record for one target whose per-row issue
// families have reached maxPerTargetRowIssues: how many raises have been refused
// since the cap engaged, and when it engaged.
type cappedRowIssues struct {
	refused int
	since   string
}

func newIssueCache() *issueCache {
	return &issueCache{
		issues:    make(map[string]healthIssue),
		since:     make(map[string]string),
		paced:     make(map[string]pacedLog),
		rowIssues: make(map[string]int),
		capped:    make(map[string]cappedRowIssues),
	}
}

// rowIssueTarget reports whether key belongs to a per-row (per-entity) issue
// family and, if so, which target it counts against. The target segment is the
// key's first segment below the family prefix — dot-free by construction
// (splitRowKey reads the row key's FIRST dot as the target/entity boundary, so a
// registered targetId can carry none).
func rowIssueTarget(key string) (string, bool) {
	rest := ""
	switch {
	case strings.HasPrefix(key, issuePrefixData):
		rest = key[len(issuePrefixData):]
	case strings.HasPrefix(key, issuePrefixTemplate):
		rest = key[len(issuePrefixTemplate):]
	default:
		return "", false
	}
	i := strings.IndexByte(rest, '.')
	if i <= 0 {
		return "", false
	}
	return rest[:i], true
}

// admitRowIssue decides whether a per-row insertion at key may grow the cache,
// and records the refusal when it may not. Called with c.mu held, for a key that
// is NOT already present — refreshing a standing entry grows nothing and is
// never refused.
func (c *issueCache) admitRowIssue(key string, now time.Time) bool {
	targetID, ok := rowIssueTarget(key)
	if !ok {
		return true
	}
	if c.rowIssues[targetID] < maxPerTargetRowIssues {
		c.rowIssues[targetID]++
		return true
	}
	ov, standing := c.capped[targetID]
	if !standing {
		ov.since = substrate.FormatTimestamp(now)
	}
	ov.refused++
	c.capped[targetID] = ov
	return false
}

// releaseRowIssue decounts one per-row entry that has been removed from the
// cache, retiring the target's overflow record once the population is back under
// the cap. Called with c.mu held.
func (c *issueCache) releaseRowIssue(key string) {
	targetID, ok := rowIssueTarget(key)
	if !ok {
		return
	}
	n := c.rowIssues[targetID] - 1
	if n <= 0 {
		delete(c.rowIssues, targetID)
	} else {
		c.rowIssues[targetID] = n
	}
	if n < maxPerTargetRowIssues {
		delete(c.capped, targetID)
	}
}

// set raises or refreshes the issue at key, stamping a key that carries no
// since yet with the current instant.
func (c *issueCache) set(key, severity, code, message string) {
	c.setSince(key, severity, code, message, time.Now())
}

// setSince is set with the caller supplying the first-arose instant to stamp a
// key that has none — for a caller holding better evidence of when the fault
// arose than "now". A key that already carries a since keeps it: a standing
// issue's age is never overwritten, exactly as under set.
func (c *issueCache) setSince(key, severity, code, message string, arrivedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, standing := c.issues[key]; !standing && !c.admitRowIssue(key, arrivedAt) {
		// The target is at its per-row cache cap: this raise is counted on its
		// overflow record and not stored. Nothing is lost that the next delivery
		// cannot re-raise — every family this covers is level-driven — and the
		// operator sees the suppression rather than silence.
		return
	}
	since, ok := c.since[key]
	if !ok {
		since = substrate.FormatTimestamp(arrivedAt)
		c.since[key] = since
	}
	c.issues[key] = healthIssue{Severity: severity, Code: code, Message: message, Since: since}
}

// standingAs reports whether an issue with exactly this severity and code is
// already active at key. It answers one question only — "is this fact already
// on the board" — for a caller that re-raises the same fact on a fixed cadence
// and needs to tell the fact's ARRIVAL from its continuation (Engine.alertStanding).
//
// Message is deliberately NOT compared. Several issue families raise the same
// (key, severity, code) with a different message per occurrence — a per-drop
// TimerDataError names the timer it dropped — and for those the message is the
// only thing distinguishing two genuinely distinct faults. Comparing it would
// make every such raise an arrival, which is right for them and is exactly why
// they use alert rather than this seam; comparing it HERE would instead make an
// embedded varying value (a count, a timestamp) re-arrive on every pass and
// bring the flood back. So the seam is narrow by construction: only a fact
// whose message is a pure function of its key belongs on it.
func (c *issueCache) standingAs(key, severity, code string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	is, ok := c.issues[key]
	return ok && is.Severity == severity && is.Code == code
}

// clear retires the issue at key. The pace memory at that key is deliberately
// left in place: a clear says the latch is empty, not that the fault ended, and
// for the families alertPaced serves it is routinely neither (a target-scoped
// config fact is cleared by one entity's column closing while another entity's
// parked gap still raises it). Erasing the clock here would make the very next
// re-derivation an arrival and hand back the flood the pacing exists to stop.
func (c *issueCache) clear(key string) {
	c.mu.Lock()
	if _, standing := c.issues[key]; standing {
		c.releaseRowIssue(key)
	}
	delete(c.issues, key)
	delete(c.since, key)
	c.mu.Unlock()
}

// pacedRaise records a raise at key on the pace clock and reports how it must
// be surfaced: whether the log record may be LOUD, and the instant this fact
// first arose. now is a parameter so the whole decision is a pure function of
// the caller's clock, mirroring tokenBucket.refill.
//
// A raise is loud on the fact's arrival (no entry), on a severity or code change
// at that key (a different fact, which must never wait out another fact's
// window), after a silence longer than one interval (nothing was re-deriving
// this fault, so its return is an arrival), and once logPaceInterval has passed
// since the last loud record. The interval is measured from the last LOUD
// record, not the last raise, so a damped pass does not push the next loud one
// further away.
//
// The returned instant is the durable arrival stamp the latch cannot keep. It
// survives clear, which is the point — for these families a clear is routinely
// another entity's close rather than a repair. What it cannot distinguish is a
// fault that genuinely ended and re-arose within a single interval: that is
// reported as one continuous fault, dated from the earlier arrival. A gap in the
// raises longer than one interval starts a fresh stamp, so the bridging is
// bounded by logPaceInterval — and the alternative it replaces is a stamp that
// reset on essentially every pass.
//
// Message is not compared, for the reason standingAs gives: a message carrying
// an embedded count or timestamp would re-arrive on every pass and bring the
// flood straight back.
func (c *issueCache) pacedRaise(key, severity, code string, now time.Time) (loud bool, arrivedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	last, ok := c.paced[key]
	if !ok || last.severity != severity || last.code != code ||
		now.Sub(last.lastRaiseAt) > logPaceInterval {
		c.paced[key] = pacedLog{
			severity: severity, code: code,
			arrivedAt: now, lastLoudAt: now, lastRaiseAt: now,
		}
		return true, now
	}
	loud = now.Sub(last.lastLoudAt) >= logPaceInterval
	if loud {
		last.lastLoudAt = now
	}
	last.lastRaiseAt = now
	c.paced[key] = last
	return loud, last.arrivedAt
}

// prunePaced drops every pace entry not raised for at least twice
// logPaceInterval, returning how many it dropped. Such an entry has already
// outlived the continuity window pacedRaise bridges, so its next raise would be
// treated as a fresh arrival with or without it — pruning is behaviour-neutral
// and bounds the map by "keys raised within the last two intervals" rather than
// by "keys raised since this process started". Called from the heartbeat, which
// already walks the cache on its own cadence.
func (c *issueCache) prunePaced(now time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for k, p := range c.paced {
		if now.Sub(p.lastRaiseAt) >= 2*logPaceInterval {
			delete(c.paced, k)
			n++
		}
	}
	return n
}

// clearPrefix retires every issue whose key starts with prefix, returning how
// many it retired. It is the teardown counterpart of clear, for the issue
// families whose keys carry a per-entity (or per-gap) segment: a target being
// revoked has no single key to name, and one entry per (entity, column) would
// otherwise stand until the process restarts.
//
// The caller supplies a prefix ending in the family's separator — see
// issueKeyTargetPrefixes, which builds them from the same constants the key
// constructors use, so a key shape and its teardown cannot drift apart. That
// trailing separator is what keeps "t1." from matching a key under "t10."
// (markStore.deleteByTargetPrefix's rule, applied to the issue keyspace).
//
// Unlike clear, this takes the pace memory with it. A prefix clear is the
// SUBJECT leaving — a deleted entity, a revoked or unregistered target — and a
// subject that is gone raises nothing more, so there is no cadence left to pace
// and no standing age to carry; if it ever comes back, its first raise is a
// genuine arrival and belongs loud, dated from then.
func (c *issueCache) clearPrefix(prefix string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for k := range c.paced {
		if strings.HasPrefix(k, prefix) {
			delete(c.paced, k)
		}
	}
	for k := range c.issues {
		if strings.HasPrefix(k, prefix) {
			c.releaseRowIssue(k)
			delete(c.issues, k)
			delete(c.since, k)
			n++
		}
	}
	return n
}

// snapshot returns the active issues in deterministic (key) order, with one
// synthetic entry per target whose per-row families are at the cache cap. The
// synthetic entries are rendered here rather than stored, so the cap's own
// bookkeeping can never be mistaken for — or cleared as — a raised fact, and
// they are ordered on the same key sort as everything else.
func (c *issueCache) snapshot() []healthIssue {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.issues)+len(c.capped))
	for k := range c.issues {
		keys = append(keys, k)
	}
	overflow := make(map[string]healthIssue, len(c.capped))
	for targetID, ov := range c.capped {
		key := issuePrefixCapped + targetID
		overflow[key] = healthIssue{
			// The refused families are all `warning`s (a RowDataError, a
			// TemplateDataError), so the entry standing in for them is one too.
			Severity: "warning",
			Code:     issueCacheCappedCode,
			Message: "target " + targetID + ": per-row issue cache is at its " +
				strconv.Itoa(maxPerTargetRowIssues) + "-entry cap; " + strconv.Itoa(ov.refused) +
				" further per-row raises for this target have been suppressed since (raise attempts, " +
				"not distinct rows — the suppressed keys are exactly what is not tracked)",
			Since: ov.since,
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]healthIssue, 0, len(keys))
	for _, k := range keys {
		if is, standing := c.issues[k]; standing {
			out = append(out, is)
			continue
		}
		out = append(out, overflow[k])
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
		reclaims, reclaimsSuppressed, orphans, corrupt, reArms, lastRun := h.sweep.metrics()
		metrics["sweepReclaims"] = reclaims
		metrics["sweepReclaimsSuppressed"] = reclaimsSuppressed
		metrics["sweepOrphansDeleted"] = orphans
		metrics["sweepCorrupt"] = corrupt
		metrics["sweepReArms"] = reArms
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

	// The log-pacing clock is bounded on the same walk that reads the cache: an
	// entry past twice its interval would log loudly on its next raise anyway,
	// so dropping it here costs nothing and keeps the map sized to what is
	// actually being re-derived.
	h.issues.prunePaced(now)
	issues := append(h.issues.snapshot(), h.pausedIssues(states, now)...)

	// Contract #5 §5.2/§5.3: a heartbeat carrying issues must not report
	// status:"healthy" (issues is empty iff healthy). Escalate the lifecycle
	// status to the worst issue severity — any error ⇒ unhealthy, any warning ⇒
	// degraded — so an open data/config error or a structurally-paused consumer
	// surfaces honestly instead of false-healthy. The "starting" and "shutdown"
	// lifecycle phases are reported verbatim (a draining/initializing component
	// isn't "degraded").
	//
	// Computed over the FULL issue set: the status describes every open issue,
	// not the sample the document happens to carry. boundIssues then selects
	// severity-first, so the listing keeps the worst severity present and would
	// reach the same verdict — but the two are computed from different things
	// on purpose, and this one is the definition.
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

// boundIssues lists at most limit entries of an issue set and appends ONE
// synthetic entry standing for the rest, so a bounded list is never readable as
// the whole set. Returns issues unchanged when it already fits (and when limit
// is non-positive — a disabled cap).
//
// SELECTION IS BY SEVERITY FIRST, ties broken on the caller's incoming order
// (the deterministic key order), because what a cap must not do is evict the
// entry that explains the fault. The per-entity families are the ones that grow
// without bound — one entry per violating subject — and they are all
// `warning`s; the entries an operator needs in front of them (a
// PlaybookConfigError, a LensEffectMismatch, a paused consumer) are single
// `error`s that key order would sort behind fifty identical warnings. Sorting
// by severity means a document can be truncated to fifty instances of one
// warning only after every error has been listed.
//
// The synthetic entry describes the issues it replaces: its severity is theirs
// (an `error` in the unlisted tail must not present as a warning), its since is
// the oldest of theirs, and its message carries the unlisted count, the true
// total, and the distinct CODES that went unlisted — an operator who cannot see
// every instance must still be able to see what kind of thing is missing.
func boundIssues(issues []healthIssue, limit int) []healthIssue {
	if limit <= 0 || len(issues) <= limit {
		return issues
	}
	ranked := make([]healthIssue, len(issues))
	copy(ranked, issues)
	sort.SliceStable(ranked, func(i, j int) bool {
		return severityRank(ranked[i].Severity) < severityRank(ranked[j].Severity)
	})
	omitted := ranked[limit:]
	out := make([]healthIssue, 0, limit+1)
	out = append(out, ranked[:limit]...)
	out = append(out, healthIssue{
		Severity: worstSeverity(omitted),
		Code:     issuesTruncatedCode,
		Message: strconv.Itoa(len(omitted)) + " further open issues are not listed in this heartbeat (" +
			strconv.Itoa(len(issues)) + " open in total, " + strconv.Itoa(limit) + " listed): " +
			omittedCodes(omitted),
		Since: oldestSince(omitted),
	})
	return out
}

// severityRank orders issues for the listing cut: `error` ahead of everything
// else, on the same two-way split aggregateStatus and worstSeverity apply. An
// unrecognised severity ranks with the warnings rather than ahead of the
// errors — it is already treated as at-least-degraded, and a severity string
// this build does not know must not be able to displace a known error.
func severityRank(severity string) int {
	if severity == "error" {
		return 0
	}
	return 1
}

// omittedCodes renders the distinct codes in an unlisted set with their counts,
// most-numerous first and ties broken alphabetically so the same set always
// renders identically. The list itself is bounded: a `surface` gap's issueCode
// is package-declared, so the vocabulary is open-ended and a truncation message
// must not become the thing that needs truncating.
func omittedCodes(issues []healthIssue) string {
	counts := make(map[string]int, len(issues))
	for _, is := range issues {
		counts[is.Code]++
	}
	codes := make([]string, 0, len(counts))
	for code := range counts {
		codes = append(codes, code)
	}
	sort.Slice(codes, func(i, j int) bool {
		if counts[codes[i]] != counts[codes[j]] {
			return counts[codes[i]] > counts[codes[j]]
		}
		return codes[i] < codes[j]
	})
	rendered := make([]string, 0, len(codes))
	for _, code := range codes {
		rendered = append(rendered, code+" ×"+strconv.Itoa(counts[code]))
	}
	if len(rendered) > omittedCodeSampleCap {
		head := append([]string{}, rendered[:omittedCodeSampleCap]...)
		head = append(head, "+"+strconv.Itoa(len(rendered)-omittedCodeSampleCap)+" more codes")
		rendered = head
	}
	return strings.Join(rendered, ", ")
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
