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
// stuck mid-flight) and the spent-budget pair (GapBudgetExhausted, or
// GapEscalatedToAugur where a policy escalates). Both are unbounded in entity count,
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

// omittedCodeSampleCap bounds how many distinct codes the truncation entry
// names before it summarises the remainder.
const omittedCodeSampleCap = 8

// rowIssueCapPerTarget bounds how many PER-ROW issue entries — the `data:` and
// `template:` families together, one entry per (entity, column) — the issue
// cache tracks for one target.
//
// maxHeartbeatIssues bounds the DOCUMENT and is unaffected by this: the
// heartbeat still computes its aggregate status over every entry the cache holds
// and still lists severity-first with an honest truncation entry. What this
// bounds is the in-memory map and snapshot's per-heartbeat sort over it, which
// the document cap never touched — those grow with the LENS (one entry per
// malformed row), so a systemically-broken large target would grow them without
// limit while the document stayed a tidy fifty entries.
//
// Sized an order of magnitude above the document cap: the tracked set is a
// sample either way once it exceeds fifty, so the extra entries buy nothing an
// operator reads, and the value's real job is to keep a pathological target from
// making every heartbeat's sort proportional to its row count.
const rowIssueCapPerTarget = 500

// rowIssuesCappedCode is the synthetic per-target entry standing in for the
// per-row issues the cache refused to track once rowIssueCapPerTarget was
// reached.
const rowIssuesCappedCode = "RowIssuesCapped"

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
// The remaining maps are the per-target bound on the two PER-ROW families
// (`data:` and `template:`), whose entry count is one per (entity, column) and
// therefore grows with the LENS, not with the deployment: a systemically-broken
// 100k-row target would otherwise grow both this map and snapshot's
// per-heartbeat sort without limit. rowIssues counts a target's currently-tracked
// per-row latch entries; rowPaced counts its tracked per-row PACE entries, which
// need their own budget because prunePaced can never reach them once a stuck row
// re-raises inside every staleness window; refused counts the latch raises turned
// away, and refusedWorst remembers the worst severity among them so the one
// surfaced overflow entry (issueKeyRowIssuesCapped) can carry it. Their lifetimes:
//
//	created                | lazily, at the target's first per-row raise
//	incremented            | rowIssues / rowPaced on each newly-tracked per-row key; refused on each
//	                       | latch raise past the cap, which also folds its severity into refusedWorst
//	decremented            | rowIssues on each per-row key's clear / clearPrefix removal; rowPaced on
//	                       | prunePaced and clearPrefix (clear deliberately leaves the pace entry)
//	retired (drained)      | refused, refusedWorst and the overflow entry when rowIssues reaches ZERO —
//	                       | NOT merely when it falls back under the cap. A refused `data:` fact is not
//	                       | re-derivable on demand: those exits Ack, and lane 1 is DeliverLastPerSubject
//	                       | on a stable durable, so the row is never delivered again until it
//	                       | re-projects. Retiring the count at the boundary would delete the only
//	                       | record that those rows are broken
//	retired (subject gone) | with the target's `data:`/`template:` prefix clears — an entity tombstone frees
//	                       | that entity's slots, a Revoke or registry removal frees the target's whole set
//	restart                | empty, like the latch itself: the cache is rebuilt from redeliveries, so the cap
//	                       | re-fills in delivery order and the overflow entry re-arrives when it is re-reached
//	ordering               | none is promised — the cap admits whichever keys are raised first, so the tracked
//	                       | set is a SAMPLE, never a ranking; the overflow entry is what states that
type issueCache struct {
	mu           sync.Mutex
	issues       map[string]healthIssue
	since        map[string]string
	paced        map[string]pacedLog
	rowIssues    map[string]int
	rowPaced     map[string]int
	refused      map[string]int
	refusedWorst map[string]string
}

func newIssueCache() *issueCache {
	return &issueCache{
		issues:       make(map[string]healthIssue),
		since:        make(map[string]string),
		paced:        make(map[string]pacedLog),
		rowIssues:    make(map[string]int),
		rowPaced:     make(map[string]int),
		refused:      make(map[string]int),
		refusedWorst: make(map[string]string),
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
//
// A NEW key in one of the per-ROW families is admitted only while its target is
// under rowIssueCapPerTarget; past the cap the insertion is refused and folded
// into the target's one overflow entry instead. Refreshing a key the cache
// ALREADY tracks is never refused — the cap bounds how many distinct facts are
// tracked, never how current a tracked one is.
//
// The refusal carries the raise's SEVERITY into the overflow entry. Without
// that, aggregateStatus — which only ever sees c.issues — would never learn that
// an `error` was raised at all, and a component that cannot fulfil its
// responsibility would report `degraded`. The two per-row families are
// warning-dominated today, but nothing constrains them: temporal.go's
// timer-payload marshal failure already raises `error` at a per-ENTITY
// issueKeyDataEntity key, so "errors here are O(1)" is not an invariant this
// cache may rest on.
func (c *issueCache) setSince(key, severity, code, message string, arrivedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, tracked := c.issues[key]; !tracked {
		if target, perRow := rowIssueTarget(key); perRow {
			if c.rowIssues[target] >= rowIssueCapPerTarget {
				c.refused[target]++
				if severityRank(severity) < severityRank(c.refusedWorst[target]) || c.refusedWorst[target] == "" {
					c.refusedWorst[target] = severity
				}
				c.setLocked(issueKeyRowIssuesCapped(target), c.refusedWorst[target], rowIssuesCappedCode,
					"target "+target+": per-row issue tracking reached its cap of "+
						strconv.Itoa(rowIssueCapPerTarget)+" entries; "+strconv.Itoa(c.refused[target])+
						" further raises for rows outside the tracked set were not recorded, and are not "+
						"re-derivable until those rows project again", arrivedAt)
				return
			}
			c.rowIssues[target]++
		}
	}
	c.setLocked(key, severity, code, message, arrivedAt)
}

// setLocked is setSince's write, past the cap decision and with c.mu held.
func (c *issueCache) setLocked(key, severity, code, message string, arrivedAt time.Time) {
	since, ok := c.since[key]
	if !ok {
		since = substrate.FormatTimestamp(arrivedAt)
		c.since[key] = since
	}
	c.issues[key] = healthIssue{Severity: severity, Code: code, Message: message, Since: since}
}

// releaseRowIssueLocked gives one per-row latch slot back to target and, once
// the target holds NO per-row entries at all, retires its overflow entry and the
// refusal accounting behind it.
//
// The retirement waits for zero rather than for the cap boundary, and the reason
// is the shape of the facts that were refused. A `template:` fault is re-derived
// on the long redelivery floor, so it would come back; a `data:` fault is not —
// those exits Ack, and lane 1 is DeliverLastPerSubject on a stable durable, so
// the row is never delivered again until something writes its key. Retiring the
// overflow entry the moment one tracked row repairs would therefore delete the
// only surviving record that N further rows are broken, and nothing would ever
// re-create it. Waiting for the target's per-row set to drain also stops the
// entry's `since` flapping around the boundary.
func (c *issueCache) releaseRowIssueLocked(target string) {
	if n := c.rowIssues[target]; n > 1 {
		c.rowIssues[target] = n - 1
	} else {
		delete(c.rowIssues, target)
	}
	if c.rowIssues[target] == 0 {
		c.retireRowIssueOverflowLocked(target)
	}
}

// retireRowIssueOverflowLocked drops a target's overflow entry and its refusal
// accounting. Called when the target's per-row set drains to nothing — by
// repair, by an entity tombstone, or by the target's own teardown.
func (c *issueCache) retireRowIssueOverflowLocked(target string) {
	delete(c.refused, target)
	delete(c.refusedWorst, target)
	overflow := issueKeyRowIssuesCapped(target)
	delete(c.issues, overflow)
	delete(c.since, overflow)
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
	defer c.mu.Unlock()
	_, tracked := c.issues[key]
	delete(c.issues, key)
	delete(c.since, key)
	if !tracked {
		return
	}
	if target, perRow := rowIssueTarget(key); perRow {
		c.releaseRowIssueLocked(target)
	}
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
//
// A NEW per-ROW key is admitted only while its target is under the same
// rowIssueCapPerTarget budget the latch uses. prunePaced cannot bound this map on
// its own once a per-row fault is re-derived on a redelivery floor shorter than
// its staleness window — every stuck row then refreshes its entry forever — so
// without the budget the map would be sized by the consumer's MaxAckPending
// rather than by the cap the design advertises. A refused key is reported
// NOT-loud and dated `now`: an untracked key has no clock to tell arrival from
// continuation, and for a map deliberately not grown the safe side is the quiet
// one. Nothing is dropped, only lowered to Debug — alertPaced's own rule.
func (c *issueCache) pacedRaise(key, severity, code string, now time.Time) (loud bool, arrivedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	last, ok := c.paced[key]
	if !ok {
		if target, perRow := rowIssueTarget(key); perRow {
			if c.rowPaced[target] >= rowIssueCapPerTarget {
				return false, now
			}
			c.rowPaced[target]++
		}
	}
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
			c.releaseRowPacedLocked(k)
			n++
		}
	}
	return n
}

// releaseRowPacedLocked gives one per-row PACE slot back, for a key just removed
// from c.paced. Only prunePaced and clearPrefix remove pace entries — clear
// deliberately does not, which is the whole point of the map — so those two are
// the only sites that return a slot.
func (c *issueCache) releaseRowPacedLocked(key string) {
	target, perRow := rowIssueTarget(key)
	if !perRow {
		return
	}
	if n := c.rowPaced[target]; n > 1 {
		c.rowPaced[target] = n - 1
	} else {
		delete(c.rowPaced, target)
	}
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
			c.releaseRowPacedLocked(k)
		}
	}
	// The per-row slot accounting is settled per TARGET after the walk rather
	// than per key inside it: a prefix clear removes a whole entity's or a whole
	// target's entries at once, and reconciling the overflow entry mid-walk
	// could retire it against a count the same walk is still draining — and,
	// for a target-wide clear, against the overflow entry the walk is itself
	// about to remove.
	touched := make(map[string]struct{})
	for k := range c.issues {
		if strings.HasPrefix(k, prefix) {
			if target, perRow := rowIssueTarget(k); perRow {
				if n := c.rowIssues[target]; n > 1 {
					c.rowIssues[target] = n - 1
				} else {
					delete(c.rowIssues, target)
				}
				touched[target] = struct{}{}
			}
			delete(c.issues, k)
			delete(c.since, k)
			n++
		}
	}
	for target := range touched {
		if c.rowIssues[target] == 0 {
			c.retireRowIssueOverflowLocked(target)
		}
	}
	return n
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

	// ackStats reads a managed durable's live un-acked count. Nil disables the
	// saturation leg entirely (unit fixtures with no supervisor).
	ackStats ackStatsReader

	// consumerSaturatedSince tracks each saturated lane-1 durable's first-arose
	// timestamp, on exactly the same terms as consumerPausedSince: the issue is
	// built inline from live consumer state rather than latched in issueCache, so
	// a consumer that drains, or a target that leaves, simply stops appearing and
	// needs no teardown leg of its own.
	consumerSaturatedSince map[string]string
}

// ackStatsReader is the heartbeat's window onto a managed durable's server-side
// ack state — substrate.ConsumerSupervisor in production.
type ackStatsReader interface {
	AckStatsForConsumer(ctx context.Context, name string) (substrate.AckStats, error)
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
		conn:                   conn,
		bucket:                 healthBucket,
		instance:               instance,
		startedAt:              time.Now(),
		interval:               every,
		states:                 states,
		issues:                 issues,
		source:                 source,
		marks:                  marks,
		sweep:                  sweep,
		temporal:               temporal,
		shadow:                 shadow,
		contraction:            contraction,
		admission:              admission,
		logger:                 logger,
		ttlMultiplier:          healthkv.DefaultTTLMultiplier,
		effectMismatchAlerted:  make(map[string]struct{}),
		consumerPausedSince:    make(map[string]string),
		consumerSaturatedSince: make(map[string]string),
	}
}

// SetAckStatsReader wires the saturation leg's window onto live consumer state.
// Must be called before run starts; left unset, the leg is skipped.
func (h *heartbeater) SetAckStatsReader(r ackStatsReader) { h.ackStats = r }

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
	issues = append(issues, h.saturatedIssues(ctx, states, now, metrics)...)

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

// saturatedIssues reports every lane-1 durable whose pending set has reached the
// consumer's MaxAckPending cap, and records each target's raw ack-pending count
// as a metric so an operator can see the gradient rather than only the cliff.
//
// This is the observable side of the decline loop. A Nak'd row holds its pending
// slot until it is acked or its message is superseded, so one mis-authored gap
// column parks every violating row of its target in the pending set; at the cap,
// getNextMsg stops handing out NEW deliveries for that target entirely, and
// entities that appear from then on are never evaluated at all. Nothing else in
// the platform observes that: the per-consumer health sink carries running/paused
// and the durable is running throughout.
//
// `error`, deliberately, and not in tension with the config-error classes being
// `warning`. Those describe a target that is DEGRADED — its violating rows are
// not remediated while the fault stands, but they are still being evaluated and
// the fix is picked up automatically. This describes a target whose deliveries
// have STOPPED: it is not self-healing, no redelivery re-derives it away, and
// for that target Weaver genuinely "cannot fulfil its primary responsibility"
// (Contract #5 §5.2).
//
// Built inline rather than latched in issueCache, exactly like pausedIssues: the
// fact is re-derived in full on every pass from live consumer state, so a
// consumer that drains or a target that leaves needs no teardown leg, and the
// entry can never consume one of the per-row cache slots it is often reporting on.
func (h *heartbeater) saturatedIssues(ctx context.Context, states map[string]string, now time.Time,
	metrics map[string]any) []healthIssue {

	if h.ackStats == nil {
		return nil
	}
	var issues []healthIssue
	pending := make(map[string]uint64)
	saturatedNow := make(map[string]struct{})
	for name, state := range states {
		targetID, isLane1 := strings.CutPrefix(name, laneConsumerPrefix)
		if !isLane1 || state != "running" {
			continue
		}
		stats, err := h.ackStats.AckStatsForConsumer(ctx, name)
		if err != nil {
			// A consumer being torn down between the snapshot and this read is
			// ordinary; the next pass re-derives the whole set either way.
			h.logger.Debug("weaver heartbeat: lane-1 ack-stats read failed",
				"consumer", name, "err", err)
			continue
		}
		if stats.AckPending > 0 {
			pending[targetID] = stats.AckPending
		}
		if stats.AckPending < uint64(laneMaxAckPending) {
			continue
		}
		saturatedNow[name] = struct{}{}
		since, ok := h.consumerSaturatedSince[name]
		if !ok {
			since = substrate.FormatTimestamp(now)
			h.consumerSaturatedSince[name] = since
		}
		issues = append(issues, healthIssue{
			Severity: "error",
			Code:     "ConsumerSaturated",
			Message: "target " + targetID + ": lane-1 consumer " + name + " holds " +
				strconv.FormatUint(stats.AckPending, 10) + " un-acked rows, its MaxAckPending cap of " +
				strconv.Itoa(laneMaxAckPending) + " — declined rows are held pending until they are fixed or " +
				"re-projected, and at the cap NO new entity of this target is delivered at all",
			Since: since,
		})
	}
	for name := range h.consumerSaturatedSince {
		if _, ok := saturatedNow[name]; !ok {
			delete(h.consumerSaturatedSince, name)
		}
	}
	if len(pending) > 0 {
		metrics["laneAckPending"] = pending
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
		return listingRank(ranked[i]) < listingRank(ranked[j])
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

// listingRank orders issues for the truncation cut, in three tiers: every
// `error` first, then the per-target cache-overflow markers, then everything
// else.
//
// The middle tier exists because of what that marker is. It is the only entry
// that describes issues the CACHE refused to hold — facts with no other record
// anywhere — and it is raised at a `data:` key, in the same warning-severity
// family as the hundreds of per-row entries whose volume caused it. Ranked on
// severity alone it would sort among them in key order and effectively never
// make the listed fifty, so the one entry saying "N further rows are broken and
// are not tracked" would itself be truncated away. There is at most one such
// marker per target, so pinning them cannot crowd out much; errors still sort
// strictly ahead of them.
func listingRank(issue healthIssue) int {
	if severityRank(issue.Severity) == 0 {
		return 0
	}
	if issue.Code == rowIssuesCappedCode {
		return 1
	}
	return 2
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
