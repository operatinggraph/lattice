package weaver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// defaultMarkLease is the production §10.3 mark lease: sized ≫ expected
// remediation latency, so an expired lease means "presumed dead" and the
// rare-double re-fire stays rare.
const defaultMarkLease = 30 * time.Minute

// defaultSweepInterval is the production reconciler sweep cadence — the prompt
// half of the §10.3 lease enforcement (the per-key TTL is the backstop).
const defaultSweepInterval = time.Minute

// defaultSweepOrphanWarmup is how long after engine start the sweep's orphan
// legs stay gated — a registry-replay-readiness proxy (see sweeper.warmup).
const defaultSweepOrphanWarmup = 5 * time.Minute

// defaultReclaimBackoffCap caps the collapse-only-action reclaim backoff. It is
// the Contract #4 §4.3 op-tracker TTL horizon: past 24h a duplicate re-dispatch
// would no longer collapse on the tracker, so there is no point pacing slower.
const defaultReclaimBackoffCap = 24 * time.Hour

// Sweep dispositions logged on every mark the sweep deletes or reclaims.
const (
	sweepReasonLeaseExpired  = "leaseExpired"
	sweepReasonTargetRemoved = "targetRemoved"
	sweepReasonOrphanColumn  = "orphanColumn"
	sweepReasonCorrupt       = "corrupt"
	sweepReasonGapClosed     = "gapClosed"
)

// The weaver-state key families deleteCorrupt names in its alert, so an
// operator reading a CorruptMark issue is told which shape was deleted rather
// than being told every family is a "mark".
const (
	corruptShapeMark   = "mark"
	corruptShapeEffect = "confidence window"
	corruptShapeCount  = "dispatch-count"
)

// collapseOnlyReclaim reports whether re-dispatching this reclaim would COLLAPSE
// onto the artifact the open episode already created rather than mount a genuinely
// new attempt: the two human userTask actions (assignTask; triggerLoom of a
// userTask-containing pattern), whose §10.3 claimId-verbatim preservation makes the
// re-dispatch idempotent at the consumer, plus the Augur's proposalscoped
// `proposedOp` (augur-dispatch-pickup §3.3/§3.4). confirmedConcluded narrows it to
// the non-external case: an EXTERNAL gap's reclaim re-dispatch IS a fresh attempt
// (§10.3 "re-call a dead vendor / mint a fresh service instance").
//
// Two reclaim-path decisions share this one predicate — backoff pacing (a collapsed
// re-dispatch is pure churn, so pace it) and `__effect` window booking (a collapsed
// re-dispatch is not a new attempt, so do not book one).
func collapseOnlyReclaim(action string, confirmedConcluded bool) bool {
	return !confirmedConcluded &&
		(action == actionAssignTask || action == actionTriggerLoom || action == actionProposedOp)
}

// sweeper is the §10.3 active reconciler: on each pass it enumerates the whole
// weaver-state bucket and level-reconciles every key it holds against that
// key's current weaver-targets row. The bucket holds four reserved families —
// §10.3 in-flight marks (bounded by the in-flight count), `…__count` retry
// budgets, `__effect` confidence windows, and the per-target `__control`
// dispatch-skip marker — and each has its own leg (see pass): closed/orphaned
// marks clear promptly and expired leases reclaim with a fresh dispatch
// episode; an orphaned confidence window is deleted; a retry budget re-derives
// its gap's standing §10.8 issue for as long as the budget suppresses dispatch,
// and re-dispatches the gap itself once it stops. The sweep is the primary
// reclaim lane; the mark's per-key TTL is only the backstop, so the sweep must
// observe an expired lease while the key still exists (TTL =
// markTTLBackstopFactor × lease plus the withDefaults SweepInterval ≤ MarkLease
// clamp guarantee that window). There is no watcher on the weaver-state backing
// stream — the sweep is interval-cadence by design.
type sweeper struct {
	engine   *Engine
	interval time.Duration
	// warmup gates the two orphan legs (target not installed; playbook lacks
	// the gap column) for this long after start. It is a registry-replay-
	// readiness proxy: the registry source replays meta.weaverTarget history
	// asynchronously and exposes no replay-done signal, so an early
	// "uninstalled"/"column dropped" verdict may be replay lag, not truth —
	// deleting on it would orphan a live gap (the sweep enumerates marks, so
	// a markless open gap is invisible until the next row delivery).
	// Expired-lease reclaim and level clearing are never gated.
	warmup time.Duration
	// startedAt anchors the warm-up window. Set at construction (engine
	// start); tests may rewind it before any pass runs.
	startedAt time.Time
	// backoffBase and backoffCap pace repeat reclaims of a still-open
	// collapse-only userTask episode (assignTask/triggerLoom): the n-th repeat
	// waits backoffBase × 2^(count-1), capped at backoffCap. The first reclaim
	// (count 0→1) fires at lease-expiry unchanged; directOp/external gaps never
	// back off (see reclaim). See backoffInterval.
	backoffBase time.Duration
	backoffCap  time.Duration

	mu                 sync.Mutex
	reclaims           int64
	reclaimsSuppressed int64
	// reArms counts the count leg's arm-(n) dispatches — an episode fired for a
	// markless, un-suppressed gap no CDC delivery was coming for. A lost
	// CAS-create (a concurrent lane-1 delivery won the gap) also completes
	// cleanly and counts here, so this reads as "re-arms that reached a
	// dispatcher", never as a failure count.
	reArms         int64
	orphansDeleted int64
	corrupt        int64
	lastRunAt      time.Time
	// corruptAlerted tracks weaver-state keys carrying a standing CorruptMark
	// issue. Each is retired either by the first completed pass that no longer
	// lists the key (the delete held and nothing recreated it) or, when the key
	// comes back with a value that parses, by the leg that read it
	// (retireCorrupt).
	corruptAlerted map[string]struct{}
}

func newSweeper(e *Engine, interval, warmup, backoffBase, backoffCap time.Duration) *sweeper {
	return &sweeper{
		engine:         e,
		interval:       interval,
		warmup:         warmup,
		backoffBase:    backoffBase,
		backoffCap:     backoffCap,
		startedAt:      time.Now(),
		corruptAlerted: make(map[string]struct{}),
	}
}

// backoffInterval returns how long a repeat reclaim of the same open episode
// must wait, given the episode's current dispatch-count: backoffBase × 2^(count-1),
// capped at backoffCap. count 0 or 1 (the first reclaim) returns the base, so the
// first reclaim fires at lease-expiry as today; each subsequent real reclaim bumps
// the count, lengthening the next interval up to the cap.
func (s *sweeper) backoffInterval(count int) time.Duration {
	if count < 1 {
		return s.backoffBase
	}
	interval := s.backoffBase
	for i := 1; i < count; i++ {
		interval *= 2
		if interval >= s.backoffCap {
			return s.backoffCap
		}
	}
	if interval > s.backoffCap {
		return s.backoffCap
	}
	return interval
}

// warmPass runs a single reconcile at engine start so a cold start does not wait
// a full interval before the first sweep. The recurring cadence is driven by the
// durable @every sweep schedule (Engine.armSweepSchedule + the weaver-sweep
// durable), not an in-process ticker — so the cadence survives restart, is
// operator-visible, and fires exactly once across replicas. The warm pass and a
// fired pass are OCC-safe even if they overlap (the standing §10.3 invariant);
// on a fresh arm the first occurrence fires a full interval later, so in
// practice they do not.
func (s *sweeper) warmPass(ctx context.Context) {
	s.pass(ctx)
}

// pass lists the weaver-state bucket once and routes every key to the leg that
// owns its family — an `__effect` confidence window to sweepEffect, a `…__count`
// retry budget to sweepCount, the `__control` marker to no leg at all, and
// everything else to sweepMark as a §10.3 mark. It then retires CorruptMark
// issues whose keys are no longer listed (the corrupt entry was deleted on an
// earlier pass and nothing recreated it) — a one-off corrupt key must not
// degrade the heartbeat for the life of the process. A key that DID come back,
// carrying a value that parses, is retired by its own leg instead
// (retireCorrupt).
func (s *sweeper) pass(ctx context.Context) {
	e := s.engine
	keys, err := e.conn.KVListKeys(ctx, e.cfg.WeaverStateBucket)
	if err != nil {
		e.logger.Warn("weaver sweep: list marks failed", "err", err)
		return
	}
	listed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		listed[key] = struct{}{}
	}
	for _, key := range keys {
		if ctx.Err() != nil {
			return
		}
		if strings.Contains(key, effectKeyMarker) {
			// A `<targetId>.__effect.<gapColumn>.<actionRef>` confidence window is
			// not a §10.3 mark either — it carries its own orphan leg (below),
			// mirroring the mark orphan legs but keyed at (target, gapColumn,
			// actionRef) granularity, since it has no <entityId> segment to level-
			// reconcile against a row.
			s.sweepEffect(ctx, key)
			continue
		}
		if strings.HasSuffix(key, controlKeySuffix) {
			// The `<targetId>.__control` dispatch-skip marker is not a §10.3
			// mark — it has no <entityId>.<gapColumn> tail, so splitMarkKey
			// would reject it as corrupt. It is reserved and engine-owned
			// (Disable/Enable/Revoke write it), it names no entity to
			// level-reconcile against a row, and the sweep neither reclaims nor
			// deletes it: it is read, via the in-memory disabled-set, as a gate
			// on the other legs.
			continue
		}
		if strings.HasSuffix(key, countKeySuffix) {
			// A `<targetId>.<entityId>.<gapColumn>.__count` retry-budget
			// dispatch-count is not a §10.3 mark either (a 4th segment;
			// splitMarkKey would reject it), but it IS per-(target, entity, gap)
			// state that level-reconciles against a row — and it outlives the
			// gap's mark by two orders of magnitude, so for a row that has gone
			// quiet it is the only durable trace of a suppressed gap. Its own
			// leg (below) reconciles it.
			s.sweepCount(ctx, key, listed)
			continue
		}
		s.sweepMark(ctx, key)
	}
	s.mu.Lock()
	for key := range s.corruptAlerted {
		if _, present := listed[key]; !present {
			delete(s.corruptAlerted, key)
			e.issues.clear(issueKeySweep(key))
		}
	}
	s.lastRunAt = time.Now()
	s.mu.Unlock()
	// Contraction monitor's sweep-cadence sample (design
	// weaver-planner-mandate-design.md §3.4): appends each registered
	// target's current violating-row count (lane-1's incremental tracker) to
	// its trajectory ring. Runs every pass regardless of whether any mark
	// exists this pass.
	e.contraction.sample(e.source.targetIDs())
}

// sweepMark level-reconciles one mark against its current row and lease:
//
//	(a) corrupt key/value → alert + delete (weaver-state is weaver-private;
//	    garbage otherwise lives forever);
//	(b) row gone, or missing_<gapColumn> not currently true → delete (the
//	    sweep leg of §10.3 level-reconciled clearing — a mark may only stand
//	    for a currently-true column). An UNPARSEABLE row leaves the mark:
//	    never delete on unreadable evidence (the lease/TTL backstop bounds it).
//	    Runs regardless of the target's __control freeze: closing an
//	    already-satisfied gap is cleanup, never new dispatch;
//	(c) column true and lease unexpired → leave, the episode is in flight;
//	(d) column true, lease expired (or absent — a lease-less mark carries no
//	    TTL either and would otherwise be immortal), target DISABLED (the
//	    __control marker) → leave untouched, no reclaim: an operator freeze
//	    (or the oscillation auto-freeze, freezeOscillatingPair) stops NEW
//	    dispatch, and reclaim — a fresh episode, fresh lease, fresh requestId —
//	    is dispatch. The mark's own per-key TTL (markTTLBackstopFactor × lease,
//	    state.go, re-armed only on a reclaim that never runs while frozen)
//	    keeps counting down unrefreshed regardless, so a frozen target's marks
//	    still self-bound without this leg's help — never immortal;
//	(e) column true, lease expired, target active → reclaim.
//
// Every delete is revision-conditioned at the revision read THIS pass: a
// CAS-create racing the sweep (a fresh episode) must never be deleted blind.
func (s *sweeper) sweepMark(ctx context.Context, key string) {
	e := s.engine
	entry, err := e.conn.KVGet(ctx, e.cfg.WeaverStateBucket, key)
	if err != nil {
		if !errors.Is(err, substrate.ErrKeyNotFound) {
			e.logger.Warn("weaver sweep: mark read failed", "key", key, "err", err)
		}
		return
	}

	targetID, entityID, gapColumn, ok := splitMarkKey(key)
	if !ok {
		s.deleteCorrupt(ctx, key, entry.Revision, corruptShapeMark,
			"mark key is not <targetId>.<entityId>.<gapColumn>")
		return
	}
	var rec mark
	if err := json.Unmarshal(entry.Value, &rec); err != nil {
		s.deleteCorrupt(ctx, key, entry.Revision, corruptShapeMark, "mark value unparseable: "+err.Error())
		return
	}
	s.retireCorrupt(key)

	rowEntry, err := e.conn.KVGet(ctx, e.cfg.WeaverTargetsBucket, targetID+"."+entityID)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			// The row is gone (entity tombstoned, or never projected): no
			// column can be true, so the level reconcile clears the mark.
			s.deleteMark(ctx, key, entry.Revision, rec.Action, sweepReasonGapClosed,
				targetID, entityID, gapColumn)
			return
		}
		e.logger.Warn("weaver sweep: row read failed; leaving mark", "key", key, "err", err)
		return
	}
	var row map[string]any
	if len(rowEntry.Value) != 0 {
		if err := json.Unmarshal(rowEntry.Value, &row); err != nil {
			// Unreadable evidence: leave the mark — the lease/TTL backstop
			// bounds it (mirrors the lane-1 handler's no-clearing posture on
			// an unparseable row).
			e.logger.Warn("weaver sweep: row value unparseable; leaving mark",
				"key", key, "err", err)
			return
		}
	}
	if !e.boolColumn(targetID, entityID, row, gapColumn) {
		// The gap is closed (or the column is gone from the row): prompt
		// level-reconciled clear, no lease wait.
		s.deleteMark(ctx, key, entry.Revision, rec.Action, sweepReasonGapClosed,
			targetID, entityID, gapColumn)
		return
	}

	if leaseLive(rec.LeaseExpiresAt, time.Now()) {
		// The episode is in flight.
		return
	}
	if e.isTargetDisabled(targetID) {
		// The operator __control freeze (Engine.Disable/Revoke) or the
		// oscillation auto-freeze (freezeOscillatingPair) stops NEW dispatch —
		// handleRow's lane-1 Ack-skip (evaluator.go) already honors it, and a
		// reclaim is dispatch too (a fresh lease, a fresh requestId, a real op
		// fire), so it must be gated the same way. Leave the mark exactly as
		// found: the level-reconciled clears above already ran unconditionally
		// (a frozen target's closed gaps still clean up), only the re-dispatch
		// is gated here. The mark is not left immortal — its own per-key TTL
		// (markTTLBackstopFactor × lease, state.go) was armed at its last
		// create/replace and keeps counting down un-refreshed while frozen
		// (re-arming happens only on a reclaim, which does not run here), so it
		// self-removes on schedule even though this leg never touches it again.
		e.logger.Debug("weaver sweep: target disabled; leaving expired mark unreclaimed",
			"targetId", targetID, "entityId", entityID, "gap", gapColumn)
		return
	}
	s.reclaim(ctx, key, entry.Revision, &rec, targetID, entityID, gapColumn, row, rowEntry.Revision)
}

// sweepEffect level-reconciles one `<targetId>.__effect.<gapColumn>.<actionRef>`
// confidence window: unparseable key/value is corrupt (delete + alert —
// weaver-state is weaver-private, so garbage otherwise lives forever);
// otherwise, mirroring the mark orphan legs' registry warm-up gate, a window
// whose target is no longer installed or whose gap column the playbook no
// longer declares is orphaned and deleted. A live (target, gapColumn) pair is
// left untouched regardless of its window contents — level reconcile here
// never resets confidence, only removes what can no longer accumulate it (a
// full-target removal is also covered for free by deleteByTargetPrefix on
// Revoke, the one verb that prefix-deletes, since `__effect` keys share the
// `<targetId>.` prefix — this leg is what catches the narrower orphan-column
// case and a target removed between reconciler passes). Resetting a LIVE
// pair's window is the operator's call, not the sweep's:
// Engine.ResetConfidence.
func (s *sweeper) sweepEffect(ctx context.Context, key string) {
	e := s.engine
	entry, err := e.conn.KVGet(ctx, e.cfg.WeaverStateBucket, key)
	if err != nil {
		if !errors.Is(err, substrate.ErrKeyNotFound) {
			e.logger.Warn("weaver sweep: effect key read failed", "key", key, "err", err)
		}
		return
	}
	targetID, gapColumn, _, ok := splitEffectKey(key)
	if !ok {
		s.deleteCorrupt(ctx, key, entry.Revision, corruptShapeEffect,
			"effect key is not <targetId>.__effect.<gapColumn>.<actionRef>")
		return
	}
	var stats effectStats
	if err := json.Unmarshal(entry.Value, &stats); err != nil {
		s.deleteCorrupt(ctx, key, entry.Revision, corruptShapeEffect, "effect value unparseable: "+err.Error())
		return
	}
	s.retireCorrupt(key)
	target, installed := e.source.target(targetID)
	if !installed {
		if !s.warmedUp() {
			// Registry warm-up: see sweeper.warmup.
			return
		}
		if s.deleteEffect(ctx, key, entry.Revision, sweepReasonTargetRemoved) {
			s.bump(&s.orphansDeleted)
		}
		return
	}
	if _, ok := target.Gaps[gapColumn]; !ok {
		if !s.warmedUp() {
			return
		}
		if s.deleteEffect(ctx, key, entry.Revision, sweepReasonOrphanColumn) {
			s.bump(&s.orphansDeleted)
		}
	}
}

// deleteEffect deletes one `__effect` confidence-window key at the revision
// read this pass. A revision conflict means a fresh dispatch/close raced the
// sweep between the read and this delete — the fresh state is intact and the
// delete is skipped (re-evaluated next pass).
func (s *sweeper) deleteEffect(ctx context.Context, key string, revision uint64, reason string) bool {
	e := s.engine
	if err := e.conn.KVDeleteRevision(ctx, e.cfg.WeaverStateBucket, key, revision); err != nil {
		if errors.Is(err, substrate.ErrRevisionConflict) {
			e.logger.Debug("weaver sweep: effect key changed since read; skipping delete", "key", key)
			return false
		}
		e.logger.Warn("weaver sweep: effect key delete failed", "key", key, "err", err)
		return false
	}
	e.logger.Warn("weaver sweep: effect key reclaimed", "key", key, "reason", reason)
	return true
}

// sweepCount level-reconciles one
// `<targetId>.<entityId>.<gapColumn>.__count` retry-budget dispatch-count
// against its current row. The count is what SUPPRESSES dispatch once the
// budget is spent (gapSuppressed's cap term) and it outlives the gap's mark by
// two orders of magnitude (dispatchCountTTLBackstopFactor × lease against
// markTTLBackstopFactor × lease), so it — not the mark — is the durable state a
// suppressed gap's standing Health issue is re-derived from: the issueCache is
// process-local, and an exhausted gap stops refreshing its mark, so the mark
// leg cannot carry Contract #10 §10.8's "never a silent park" promise for as
// long as the suppression it explains actually lasts.
//
// The arms are ordered by what each one NEEDS, not by how the leg reads. Every
// arm that RETIRES a fact — a stale issue, a budget whose chain has ended —
// sits above every gate that says this leg may not ACT, because a gate answers
// "may this leg dispatch" while a retirement answers "does this fact still
// hold", and a frozen or mid-replay target must still shed bookkeeping its row
// has already contradicted (sweepMark's own posture: closing an
// already-satisfied gap is cleanup, never new dispatch). Only the two
// DESTRUCTIVE steps sit below the gates:
//
//	(a) corrupt KEY → alert + delete, ungated: a key that does not split names
//	    no target, so no gate could decide it (sweepMark arm (a));
//	(b) body parses → retire any CorruptMark issue standing at this key. Reading
//	    is not acting, so this runs above every gate: the issue is about a VALUE
//	    that no longer exists, it is `error`-severity, and any error pins the
//	    whole component unhealthy — leaving one standing for the length of an
//	    operator freeze would be a lie the operator cannot clear. A body that
//	    does NOT parse defers its delete to (h);
//	(c) the gap's own mark is listed in THIS pass → return, before the row is
//	    ever read: the mark leg visits this gap with the same row and reaches
//	    the same escalation site, so a read here would be a duplicate every pass
//	    of every actively-remediating gap. Bookkeeping this leg would have done
//	    waits one pass, which is right — the mark leg owns the gap while its
//	    mark stands;
//	(d) row gone → retire this gap's standing issue and LEAVE the count. A
//	    Refractor lens rebuild purges and re-replays a target's rows, during
//	    which every entity reads row-gone; the count is the retry BOUND, so
//	    deleting it there would re-arm exactly the storm the budget exists to
//	    stop. The 128 h TTL collects a genuinely orphaned count. A failed row
//	    read, or a row this build cannot parse, acts on nothing at all — never
//	    act on unreadable evidence (sweepMark's posture);
//	(e) missing_<gapColumn> not currently true → delete the count and retire the
//	    issue: a PRESENT row whose column reads false is positive evidence that
//	    the chain this budget bounded has ended, and no mark exists to carry
//	    that level reconcile;
//	(f) target not registered → return: the registry replays asynchronously, so
//	    "not installed" may be replay lag rather than absence;
//	(g) target DISABLED (the `__control` marker) → return: an operator freeze
//	    (or the oscillation auto-freeze) stops NEW dispatch, and an escalation
//	    is dispatch — the gate sweepMark arm (d) and lane-1's Ack-skip apply;
//	(h) the body did not parse → alert + delete, BELOW the two gates above,
//	    because unlike (b) it destroys durable state and stopping that during
//	    replay lag or under an operator freeze is what those gates are for — a
//	    rolling upgrade writing a body an older build cannot parse is the
//	    realistic trigger. A garbled body is not cosmetic otherwise:
//	    getDispatchCount errors on it, so gapSuppressed reads its safe
//	    (dispatchable) side and incrementDispatchCount's read-modify-write fails
//	    the same way, leaving a directOp gap retrying unbounded — the exact
//	    outcome defaultDirectOpRetryBudget exists to prevent. The delete re-arms
//	    from 0 (what the garbled body already yields) and leaves a key that can
//	    count again;
//	(i) row carries no §10.2 entityKey echo → return: the escalation feeds
//	    entityKey to planGap to name its candidate, and lane 1 itself declines
//	    to dispatch a violating row missing it. The mark leg reaches the
//	    escalation only past the reclaim's non-empty entityKey guard; this leg
//	    has no mark to have been guarded, so it guards itself;
//	(j) the playbook no longer names gapColumn → return, escalating NOTHING.
//	    Weaver has no remediation to bound here, and on an augur-escalating
//	    target an escalation would not even terminate: it re-creates the mark
//	    and re-arms the count, the mark leg's own orphan arm deletes that mark,
//	    and the next pass escalates again. The standing issue is left exactly as
//	    found — lane 1 raises at this same latch for this same column
//	    (openGapColumns enumerates every true missing_*, playbook or not), so a
//	    clear here would fight that raise, flap the latch and re-stamp its
//	    `since` on every round trip. The orphan column has its own diagnostic;
//	(k) row not violating → return, mirroring lane-1's L1 gate and the
//	    reclaim's own violating gate: this leg never acts where lane-1 would
//	    not;
//	(l) gap suppressed with its retry budget SPENT → escalate, through the same
//	    escalateExhaustedGap site the mark leg calls — a fresh Augur episode
//	    where the target escalates "exhausted", else the standing
//	    GapBudgetExhausted issue;
//	(m) gap suppressed with a call in flight (inflight_<g>) → return: the lens
//	    re-projects when the call lands and lane-1 re-delivers;
//	(n) NOT suppressed, a COUNT-BASED CAP governs the gap, and no mark → retire
//	    the standing issue, then DISPATCH. Arms (c)–(l) have already established
//	    exactly the state lane-1 dispatches on — registered, active, planned,
//	    violating, entityKey-echoed, un-suppressed — and markless on top of it,
//	    so for a row that has gone quiet there is no delivery coming to act on
//	    it. The leg fires the episode itself, through the same planGap →
//	    fireEpisode path the mark leg's leg-advance uses. This is the re-arm: it
//	    is how a budget that stops suppressing (an operator raising
//	    maxretries_<g> without touching anything else, or draining the count
//	    outright) turns back into a dispatch rather than into a quieter park.
//	    Three shapes are excluded, each for its own reason: a `surface` gap (FR29
//	    says it never dispatches, and the latch at its key is lane-1's); a
//	    collapse-only action, whose re-dispatch is idempotent only through a
//	    mark's claimId this leg does not have; and a gap no count-based cap
//	    governs at all. The last two are not cosmetic — see their gates.
//
// The (n) dispatch is bounded by the count, and the count only bounds a gap the
// cap term actually governs. fireEpisode CAS-creates the gap's mark, so the next
// pass stops at (c) — but marks.create arms only the DEFAULT per-key TTL
// (markTTLBackstopFactor × lease), not the widened one reclaim computes for a
// backed-off episode, so for a gap that can never be suppressed the mark simply
// TTL-expires and this arm fires again, forever, on a period this leg picks
// rather than the one reclaim's backoff picked — minting a fresh claimId, and so
// a duplicate userTask/Loom instance, every time. The needsCount gate is what
// makes the arm terminate: every gap it serves is one whose dispatch-count
// advances toward a cap that eventually suppresses it, at which point arm (l)
// owns it. A path that publishes nothing — an unbuildable plan, admission
// control, a failed create — leaves no mark and is re-attempted next pass; a
// publish that fails AFTER the create leaves a mark with a live lease, so the
// retry is the mark leg's, a full MarkLease later.
//
// The count read here is the only one the pass needs: its value feeds the
// suppression terms directly, so the gate does not
// re-read the key. Every delete is revision-conditioned at the revision read
// THIS pass — a dispatch racing the sweep bumps the count, and that fresh
// budget must never be deleted blind.
func (s *sweeper) sweepCount(ctx context.Context, key string, listed map[string]struct{}) {
	e := s.engine
	entry, err := e.conn.KVGet(ctx, e.cfg.WeaverStateBucket, key)
	if err != nil {
		if !errors.Is(err, substrate.ErrKeyNotFound) {
			e.logger.Warn("weaver sweep: dispatch-count read failed", "key", key, "err", err)
		}
		return
	}
	targetID, entityID, gapColumn, ok := splitCountKey(key)
	if !ok {
		s.deleteCorrupt(ctx, key, entry.Revision, corruptShapeCount,
			"dispatch-count key is not <targetId>.<entityId>.<gapColumn>.__count")
		return
	}
	var count dispatchCount
	bodyErr := json.Unmarshal(entry.Value, &count)
	if bodyErr == nil {
		s.retireCorrupt(key)
	}

	if _, marked := listed[markKey(targetID, entityID, gapColumn)]; marked {
		// The mark leg visits this same gap in this same pass, from the same
		// row, and reaches the same escalation site from its own evidence.
		return
	}

	rowEntry, err := e.conn.KVGet(ctx, e.cfg.WeaverTargetsBucket, targetID+"."+entityID)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			// Absence is not evidence: the row may be mid-rebuild. Retire the
			// standing issue — it states a fact about a row that is not there —
			// and leave the bound itself to the TTL. The next pass re-raises if
			// the row returns still exhausted.
			e.logger.Debug("weaver sweep: row gone; retiring the gap issue, leaving the dispatch-count",
				"targetId", targetID, "entityId", entityID, "gap", gapColumn)
			e.issues.clear(issueKeyGapEntity(targetID, entityID, gapColumn))
			return
		}
		e.logger.Warn("weaver sweep: row read failed; leaving dispatch-count", "key", key, "err", err)
		return
	}
	var row map[string]any
	if len(rowEntry.Value) != 0 {
		if err := json.Unmarshal(rowEntry.Value, &row); err != nil {
			e.logger.Warn("weaver sweep: row value unparseable; leaving dispatch-count",
				"key", key, "err", err)
			return
		}
	}
	if !e.boolColumn(targetID, entityID, row, gapColumn) {
		// The gap is closed (or the column is gone from the row) and no mark
		// exists to carry the level reconcile: this leg is the one that resets
		// the budget and retires the standing issue.
		s.deleteCount(ctx, key, entry.Revision, targetID, entityID, gapColumn)
		return
	}

	target, installed := e.source.target(targetID)
	if !installed {
		return
	}
	if e.isTargetDisabled(targetID) {
		e.logger.Debug("weaver sweep: target disabled; leaving the dispatch-count unreconciled",
			"targetId", targetID, "entityId", entityID, "gap", gapColumn)
		return
	}
	if bodyErr != nil {
		// No reader can parse this budget, so it can never accumulate. Delete
		// it (the next dispatch creates a countable one) and retire the issue
		// with it: the leg reaches this gap only through the key it is about to
		// remove, so a latch left standing here would never be revisited.
		s.deleteCorrupt(ctx, key, entry.Revision, corruptShapeCount,
			"dispatch-count value unparseable: "+bodyErr.Error())
		e.issues.clear(issueKeyGapEntity(targetID, entityID, gapColumn))
		return
	}
	entityKey, _ := row["entityKey"].(string)
	if entityKey == "" {
		// Without the §10.2 entityKey echo the escalation's own plan cannot name
		// its candidate, and lane-1 declines to dispatch such a row at all
		// (handleRow raises a RowDataError and skips it, evaluator.go) — so this
		// leg must not dispatch one either. The count stays: an incomplete row
		// is no evidence about the budget, and its TTL bounds the key.
		return
	}
	ga, planned := target.Gaps[gapColumn]
	if !planned {
		// A column the playbook no longer names: nothing here to bound and
		// nothing to escalate. The latch at this key belongs to lane 1, which
		// raises for exactly this column too — leave it alone (see arm (j)).
		return
	}
	if !e.boolColumn(targetID, entityID, row, "violating") {
		return
	}

	// One evaluation of the row's suppression terms serves both arms below.
	// gapSuppressedWithCount is exactly this pair (evaluator.go) and stays the
	// verdict's definition; the leg unfolds it because the re-arm needs the TERMS
	// themselves, not just the boolean — "not suppressed" is returned both for a
	// gap with budget left and for a gap with no budget concept at all, and those
	// two are opposite answers for arm (n).
	terms := e.gapSuppressionTerms(targetID, entityID, row, gapColumn, ga.Action)
	suppressed, exhausted, budgetIsDefault := terms.suppressed, false, false
	if terms.needsCount {
		suppressed, exhausted, budgetIsDefault = terms.verdict(count.Count)
	}
	if suppressed {
		if exhausted {
			// The loud stop, re-derived from the state that causes the
			// suppression rather than from a mark the exhausted gap stopped
			// refreshing (§10.8: "never a silent park").
			if e.escalateExhaustedGap(ctx, target, targetID, entityID, entityKey, gapColumn, row, rowEntry.Revision, budgetIsDefault) != substrate.Ack {
				e.logger.Warn("weaver sweep: exhausted-gap escalation dispatch did not complete cleanly; will retry",
					"targetId", targetID, "entityId", entityID, "gap", gapColumn)
			}
		}
		return
	}

	// The re-arm. The gap is open, violating and markless, and its chain still
	// has budget — nothing suppresses it, so lane-1 would dispatch it on the
	// next delivery, and for a row that has gone quiet no delivery is coming.
	// This leg is the only one that still visits such a gap, so it dispatches.
	if ga.Action == actionSurface {
		// FR29's surface gap never dispatches — lane-1 returns before it ever
		// builds a plan — so a count left behind by the column's previous action
		// is not a re-arm to act on, and planning one would raise an
		// `error`-severity PlaybookConfigError against a correctly-authored
		// playbook, every pass. The latch at this key is lane-1's here: it holds
		// the surface issue for as long as the column is true, so this leg leaves
		// it exactly as it leaves the orphan column's (arm (j)).
		return
	}
	// The standing GapBudgetExhausted claims this gap has no attempt left. The
	// suppression verdict just taken says it has one — and says so whether or
	// not the dispatch below can be planned, since an unresolved reference or an
	// admission deferral means the gap is blocked or paced, never exhausted. So
	// the fact is retired here, level-driven on the READ, rather than riding on
	// planGap's own clear, which a plan that fails to build never reaches. It
	// also sits ABOVE the two dispatch guards below, which are permanent
	// properties of a gap's class rather than conditions that pass: a retire
	// under a guard that never lifts is stranded for the life of the gap, and
	// arm (l) — in this same leg, the only other raiser at this key — will have
	// raised for a capped collapse-only gap whose operator has since raised the
	// cap. Nothing else raises here for a planned, non-surface column:
	// issueKeyGapEntity has exactly two raise sites (evaluator.go), lane-1's
	// surface branch — excluded by the guard above — and escalateExhaustedGap,
	// which raises only with the budget SPENT, which this arm has excluded.
	e.issues.clear(issueKeyGapEntity(targetID, entityID, gapColumn))

	if collapseOnlyReclaim(ga.Action, e.staleMark(targetID, entityID, row, gapColumn, ga)) {
		// A collapse-only gap's re-dispatch is only harmless because it reuses
		// the open episode's claimId, which reproduces the same taskId /
		// Loom-instance id and collapses at the consumer (§10.3's
		// claimId-verbatim rule; reclaim passes rec.ClaimID for exactly this).
		// That claimId lives on the MARK, and this arm is reached only when
		// there is no mark — so the dispatch it would make is a fresh-claimId
		// one, minting a SECOND human task beside a first that may still be open
		// in the world. A markless collapse-only gap is not evidence the
		// remediation ended; a mark that TTL-expired unrefreshed under a freeze
		// (sweepMark arm (d)) leaves exactly this shape with the task still
		// standing. External gaps are excluded from the guard by
		// confirmedConcluded, because for them §10.3 says the re-dispatch IS the
		// intended fresh attempt.
		e.logger.Debug("weaver sweep: markless gap's action collapses on a claimId this leg does not have; not re-arming",
			"targetId", targetID, "entityId", entityID, "gap", gapColumn, "action", ga.Action)
		return
	}
	if !terms.needsCount {
		// No count-based cap governs this gap, so its dispatch-count is a record
		// of attempts and nothing else: it never reaches a cap, the suppression
		// verdict above can never flip, and arm (l) never takes the gap off this
		// arm's hands. Dispatching here would therefore not be a re-arm at all
		// but a permanent second dispatch cadence, running at whatever period
		// marks.create's DEFAULT TTL happens to give (markTTLBackstopFactor ×
		// lease) — and pre-empting the one that already governs these gaps.
		// reclaim paces exactly this class with its ClaimedAt + dispatch-count
		// backoff, widening the mark's TTL past the backoff window precisely so
		// the mark never lapses into a markless open gap; a re-arm firing in the
		// gap that lapse leaves would mint a fresh claimId every cycle. Their
		// pacing is reclaim's, not this leg's.
		//
		// needsCount, not hasUsableRetryCap: the flagship case of this whole leg
		// is a directOp gap parked by defaultDirectOpRetryBudget with no declared
		// maxretries_<g>, which hasUsableRetryCap reports false for by design.
		e.logger.Debug("weaver sweep: markless gap has no count-based retry cap; leaving its pacing to the mark leg",
			"targetId", targetID, "entityId", entityID, "gap", gapColumn, "action", ga.Action)
		return
	}
	pl, actionRef, dec := e.planGap(ctx, target, targetID, entityID, gapColumn, ga, row, rowEntry.Revision, "")
	if pl == nil {
		// planGap has already surfaced why (an unresolved reference, a template
		// data error, a playbook config error) on its own issue keys; this only
		// attributes the attempt to the sweep. Nothing was published and no mark
		// was taken, so the next pass re-attempts.
		e.logger.Debug("weaver sweep: re-armed gap has no dispatchable plan; leaving it for the next pass",
			"targetId", targetID, "entityId", entityID, "gap", gapColumn, "decision", dec)
		return
	}
	// A genuinely fresh episode (no mark to pin an action from, nothing to
	// reclaim), so the CAS-create is the OCC: a lane-1 delivery racing this pass
	// collapses on it exactly as two evaluations already do, and the loser drops.
	if e.fireEpisode(ctx, targetID, entityID, entityKey, gapColumn, actionRef, pl, false, nil, 0, false, false) != substrate.Ack {
		// Two different retries, and they are not the same wait. A failed
		// CAS-create leaves the gap markless, so the next pass (≤ 1 min)
		// re-enters this arm. A publish that failed AFTER the create leaves a
		// mark holding a FRESH lease, so sweepMark reads the episode as in
		// flight and defers; the retry is reclaim's, a full MarkLease later.
		// Bounded either way, and loud here because neither is routine.
		e.logger.Warn("weaver sweep: re-arm dispatch did not complete cleanly; will retry",
			"targetId", targetID, "entityId", entityID, "gap", gapColumn)
		return
	}
	s.bump(&s.reArms)
}

// deleteCount deletes one `…__count` retry-budget dispatch-count at the
// revision read this pass and retires the (target, entity, gap) standing issue
// the budget's exhaustion raises. Its one caller is the gap-close level
// reconcile: a present row whose gap column reads false. A revision conflict
// means a dispatch bumped the count between the read and this delete — that
// fresh budget stands and the delete is skipped (re-evaluated next pass); a key
// that already vanished is already in the desired state.
//
// The issue retirement is driven by the ROW read that reached this call, not by
// the delete's outcome, so the latch is retired by the same level reconcile
// that observed the close — idempotent when none stands. A won delete counts on
// the sweepOrphansDeleted metric, like every other key this sweep removes.
func (s *sweeper) deleteCount(ctx context.Context, key string, revision uint64, targetID, entityID, gapColumn string) {
	e := s.engine
	if err := e.conn.KVDeleteRevision(ctx, e.cfg.WeaverStateBucket, key, revision); err != nil {
		switch {
		case errors.Is(err, substrate.ErrRevisionConflict), errors.Is(err, substrate.ErrKeyNotFound):
			e.logger.Debug("weaver sweep: dispatch-count changed since read; skipping delete", "key", key)
		default:
			e.logger.Warn("weaver sweep: dispatch-count delete failed", "key", key, "err", err)
		}
	} else {
		e.logger.Info("weaver sweep: dispatch-count cleared",
			"targetId", targetID, "entityId", entityID, "gap", gapColumn,
			"reason", sweepReasonGapClosed)
		s.bump(&s.orphansDeleted)
	}
	e.issues.clear(issueKeyGapEntity(targetID, entityID, gapColumn))
}

// reclaim handles an expired (or lease-less) mark whose column is still true:
// if the target is installed, its playbook still names the gap, and the row
// is violating, the gap is re-dispatched as a FRESH episode — a
// revision-conditioned in-place replace of the mark (fresh lease/claimedAt/
// heldBy, re-armed per-key TTL) whose update revision derives the new
// requestId (a real re-dispatch, not a Contract #4 collapse). The key is
// never absent across a reclaim, so a crash at any point leaves either the
// old expired mark (re-swept next pass) or the fresh mark (its lease bounds
// the retry) — never a markless open gap. Orphaned marks (target removed,
// column gone from the playbook) are deleted without dispatch, gated by the
// registry warm-up.
//
// A re-fired triggerLoom/assignTask collapses on the existing task/instance
// (the §10.3 claimId-preservation makes the re-dispatch idempotent at the
// consumer), so it never duplicates the artifact — but every repeat still
// commits a no-op op, writes a fresh Contract #4 tracker, and bumps the count.
// To stop that phantom churn for an episode held open for days, repeat reclaims
// of these two collapse-only userTask actions are PACED by an exponential
// backoff keyed on the mark's own ClaimedAt + dispatch-count (the first reclaim
// still fires at lease-expiry; see the backoff guard below). directOp/external
// gaps, where a reclaim re-dispatch IS the intended bounded retry, are never
// backed off.
func (s *sweeper) reclaim(ctx context.Context, key string, markRev uint64, rec *mark,
	targetID, entityID, gapColumn string, row map[string]any, rowRevision uint64) {

	e := s.engine
	target, installed := e.source.target(targetID)
	if !installed {
		if !s.warmedUp() {
			// Registry warm-up: see sweeper.warmup.
			return
		}
		if s.deleteMark(ctx, key, markRev, rec.Action, sweepReasonTargetRemoved,
			targetID, entityID, gapColumn) {
			s.bump(&s.orphansDeleted)
		}
		return
	}
	ga, ok := target.Gaps[gapColumn]
	if !ok {
		if !s.warmedUp() {
			// Same warm-up gate: mid-replay the loaded definition may be an
			// intermediate revision that does not yet name this gap.
			return
		}
		if s.deleteMark(ctx, key, markRev, rec.Action, sweepReasonOrphanColumn,
			targetID, entityID, gapColumn) {
			s.bump(&s.orphansDeleted)
		}
		return
	}

	// entityKey is needed by BOTH paths below (the leg-advance dispatch and
	// the ordinary reclaim), so resolve and validate it once here, ahead of
	// the goal-release check, rather than resolving it separately on each path.
	entityKey, _ := row["entityKey"].(string)
	if entityKey == "" {
		// Without the §10.2 entityKey echo no remediation can name its
		// candidate — and an expired mark over such a row can never be
		// reclaimed, so leaving it would re-alert on every pass forever (a
		// lease-less mark has no TTL to bound it). Treat the pair as corrupt
		// evidence: alert + delete; the next well-formed row delivery
		// dispatches fresh.
		s.deleteCorrupt(ctx, key, markRev, corruptShapeMark,
			"row "+targetID+"."+entityID+" is violating but carries no entityKey")
		return
	}

	// Fire 6, R1: the pinned leg's declared effects may already hold in the
	// CURRENT row (releaseCompletedLeg re-reads it fresh here, independent of
	// CDC delivery timing) even though the mark's lease has expired — a leg
	// boundary, not a stuck episode. The sweep enumerates MARKS, not rows
	// (see sweeper doc), so simply releasing and returning would leave the
	// gap markless and INVISIBLE until an unrelated future row write happens
	// to touch this entity again — no such write is guaranteed (the write
	// that satisfied this leg's effect may be the last one for a while).
	// Dispatch the next leg as a genuinely fresh episode via the SAME
	// CAS-create path lane-1 uses (fireEpisode's found=false branch)
	// instead of merely releasing.
	if e.releaseCompletedLeg(ctx, targetID, entityID, gapColumn, ga, rec.Action, row, markRev) {
		pl, actionRef, dec := e.planGap(ctx, target, targetID, entityID, gapColumn, ga, row, rowRevision, "")
		if pl == nil {
			e.logger.Warn("weaver sweep: leg-advance plan failed; the gap stays markless until the next row delivery",
				"targetId", targetID, "entityId", entityID, "gap", gapColumn, "decision", dec)
			return
		}
		if e.fireEpisode(ctx, targetID, entityID, entityKey, gapColumn, actionRef, pl, false, nil, 0, false, false) != substrate.Ack {
			// Either the fresh mark's CAS-create itself failed (truly
			// markless — the next sweep pass retries the same release) or
			// its op publish failed (the mark exists; the lease/reclaim
			// cycle retries it like any other stuck episode) — either way
			// this is a bounded, retried condition, never a silent wedge.
			e.logger.Warn("weaver sweep: leg-advance dispatch did not complete cleanly; will retry",
				"targetId", targetID, "entityId", entityID, "gap", gapColumn)
		}
		return
	}

	if !e.boolColumn(targetID, entityID, row, "violating") {
		// Mirrors lane-1's L1 gate (handleRow dispatches only violating rows):
		// an open missing_* on a non-violating row must not be re-dispatched
		// here when lane-1 never would fire it. Leave the mark to level
		// clearing or the next CDC delivery; the TTL backstop bounds a stale
		// one.
		return
	}

	if suppressed, exhausted, budgetIsDefault := e.gapSuppressed(ctx, targetID, entityID, row, gapColumn, ga.Action); suppressed {
		// Mirrors lane-1's dispatch-suppression gate: a gap with inflight_<g> set
		// must NOT be re-dispatched. This is the LOAD-BEARING skip — the
		// mark-lease expiry → reclaim is the actual re-dispatch path for a
		// long-pending external call (the lane-1 skip alone does not stop the
		// sweep). Leave the expired mark; it is cleared by level reconcile once
		// the gap closes, and the TTL backstop bounds it if not. The gap stays
		// violating throughout — only re-dispatch is suppressed.
		//
		// A gap whose retry budget is spent (a declared maxretries_<g>, or the
		// engine's defaultDirectOpRetryBudget when a "directOp" gap declares
		// neither maxretries_<g> nor inflight_<g> — gapSuppressed) is different:
		// the sweep is the ONLY dispatch leg that still visits a row with no
		// fresh CDC deliveries, so it is this site — not lane-1 — that must
		// actually close the §10.8 "never a silent park" promise for a row that
		// has gone quiet. escalateExhaustedGap either fires a fresh Augur
		// escalation episode or raises the standing Health issue; it never
		// touches this gap's own (already-exhausted, possibly already-expired)
		// mark.
		if exhausted {
			if e.escalateExhaustedGap(ctx, target, targetID, entityID, entityKey, gapColumn, row, rowRevision, budgetIsDefault) != substrate.Ack {
				e.logger.Warn("weaver sweep: exhausted-gap escalation dispatch did not complete cleanly; will retry",
					"targetId", targetID, "entityId", entityID, "gap", gapColumn)
			}
		}
		return
	}

	// confirmedConcluded mirrors fireEpisode's staleMark (evaluator.go): true
	// when gapColumn is an EXTERNAL gap (a lens-declared inflight_<g>
	// companion, currently false) per Contract #10 §10.3 — "External gaps are
	// unchanged — their reclaim re-dispatch is intended (re-call a dead vendor
	// / mint a fresh service instance), episode-scoped on markRevision and
	// bounded by inflight_<g> + maxretries_<g>," distinct from the human
	// userTask gaps (assignTask; triggerLoom of a userTask-containing
	// pattern), which are governed instead by §10.3's
	// claimId-verbatim-preservation rule — staleMark's externalDispatchGap
	// classifier reads false for them even when a lens misdeclares the column,
	// so confirmedConcluded never applies to them. It gates
	// both the backoff pacing below (that pacing exists to avoid phantom-task
	// churn on a still-open human episode; §10.3 already bounds an external
	// gap's retry by inflight_<g>/maxretries_<g> instead) and the claimId
	// choice (below the pacing block).
	confirmedConcluded := e.staleMark(targetID, entityID, row, gapColumn, ga)

	// Default per-key TTL backstop for the re-armed mark; widened below for any
	// paced reclaim, so the mark outlives its own backoff window.
	markTTL := markTTLBackstopFactor * e.marks.lease
	collapseOnly := collapseOnlyReclaim(rec.Action, confirmedConcluded)

	// Defense in depth for Contract #10 §10.3's rule that a gap declaring
	// inflight_<g> MUST also declare maxretries_<g>. An external gap's reclaim is
	// deliberately NOT collapse-only — each one is a fresh vendor call — and is
	// meant to be bounded by that cap rather than paced by the timer below. A
	// lens that declares inflight_<g> without a usable cap gets neither:
	// gapSuppressed explicitly declines to substitute the engine's default budget
	// for a row that declares inflight_<g> (its own pacing is not this engine's
	// to second-guess), so the gap would re-dispatch a fresh external call every
	// mark-lease expiry, forever.
	//
	// pkgmgr's validateWeaverTargets refuses that pair at install, but only where
	// the gap's class is decidable from the playbook alone (directOp/proposedOp),
	// and only over DECLARATIONS. This runs over VALUES, on the three shapes the
	// install gate structurally cannot reach: a lens that declares maxretries_<g>
	// in its BodyColumns and projects null or a non-positive integer into the row;
	// a triggerLoom gap whose external class only externalDispatchGap's pattern
	// probe can decide, and only at dispatch time; and a target whose LensRef
	// resolves outside the installing batch, whose columns the gate never saw.
	// Unbounded in count, but never unbounded AND unpaced, and the hold is visible
	// on the sweepReclaimsSuppressed metric.
	uncappedExternal := confirmedConcluded && !e.hasUsableRetryCap(targetID, entityID, row, gapColumn)

	if collapseOnly || uncappedExternal {
		// Collapse-only reclaim: pace repeat reclaims with an exponential backoff
		// keyed on the mark's own ClaimedAt + dispatch-count — the consumer
		// collapses any repeat re-dispatch anyway, so re-firing every sweep is
		// pure phantom churn (a no-op op + a fresh Contract #4 tracker). This
		// covers TWO distinct collapse-only classes: the userTask actions
		// (assignTask/triggerLoom, §10.3 — the claimId-seeded stable task/instance
		// id) and the Augur's `proposedOp` (design augur-dispatch-pickup §3.3/§3.4
		// — a PROPOSAL-scoped deterministic requestId, set regardless of which
		// inner action {triggerLoom, assignTask, directOp} the proposal
		// materialises to, since the mark's recorded Action is always the OUTER
		// static playbook entry "proposedOp", never the inner one). An ordinary
		// (non-Augur) directOp/external reclaim never reaches here on the
		// collapseOnly term — it is the intended bounded retry
		// (§inflight_<g>/maxretries_<g>), never backed off; confirmedConcluded
		// clears the term for exactly that reason. It reaches here only on the
		// uncappedExternal term above, where the cap that was supposed to bound
		// it is missing. Best-effort either way: a count read or ClaimedAt parse
		// failure falls through to a normal (unpaced) reclaim.
		if count, err := e.marks.getDispatchCount(ctx, targetID, entityID, gapColumn); err != nil {
			e.logger.Debug("weaver sweep: reclaim backoff dispatch-count read failed; not pacing",
				"targetId", targetID, "entityId", entityID, "gap", gapColumn, "err", err)
		} else if claimedAt, perr := time.Parse(time.RFC3339Nano, rec.ClaimedAt); perr == nil {
			if elapsed := time.Since(claimedAt); elapsed < s.backoffInterval(count) {
				// Dispatched within the backoff window: leave the mark untouched —
				// no replace/fire/bumpDispatchCount. The mark survives on its
				// backoff-sized TTL (set by the prior reclaim below); the next sweep
				// re-enters this cheap compare until ClaimedAt ages past the interval.
				e.logger.Debug("weaver sweep: reclaim backed off; episode dispatched recently",
					"targetId", targetID, "entityId", entityID, "gap", gapColumn,
					"action", rec.Action, "dispatchCount", count, "elapsed", elapsed,
					"uncappedExternal", uncappedExternal)
				s.bump(&s.reclaimsSuppressed)
				return
			}
			// Proceeding: size the re-armed mark's TTL to outlast the NEXT backoff
			// window (the count bumps after fire) plus a sweep-cadence margin, so the
			// mark is always reclaimed before it can TTL-expire into a markless open
			// gap — which a CDC redelivery would otherwise re-dispatch with a fresh
			// claimId, minting a DUPLICATE task. Never below the default backstop.
			if want := s.backoffInterval(count+1) + 2*s.interval; want > markTTL {
				markTTL = want
			}
		}
	}

	// Plan BEFORE touching the expired mark: a failed plan (unresolved
	// reference, template data error) alerts through the shared planGap issue
	// keys and leaves the mark in place — the next sweep retries. Bounded,
	// loud, never a hot loop. rec.Action is passed as the pinned action (Fire
	// 5): the sweep is ALWAYS reclaiming an existing, already-dispatched
	// episode, never a fresh one, so a planned-mode candidates gap must reuse
	// the mark's recorded pick rather than re-ranking it (design §2 — a
	// reclaim never replans mid-episode).
	pl, resolvedAction, _ := e.planGap(ctx, target, targetID, entityID, gapColumn, ga, row, rowRevision, rec.Action)
	if pl == nil {
		e.logger.Warn("weaver sweep: reclaim plan failed; leaving expired mark for the next sweep",
			"targetId", targetID, "entityId", entityID, "gap", gapColumn)
		return
	}

	// The claimId a reclaim seeds the fresh dispatch with: by default preserve
	// the mark's per-open-episode claimId across the reclaim (§10.3's
	// claimId-verbatim rule for the human userTask gaps — assignTask;
	// triggerLoom of a userTask-containing pattern) so the userTask/Loom-
	// instance identity it seeds stays stable and a late-arriving completion
	// of the OLD attempt still lands on it. But confirmedConcluded (above)
	// means gapColumn is instead an EXTERNAL gap, for which §10.3 says
	// "reclaim re-dispatch is intended... mint a fresh service instance" —
	// preserving the old claimId here would seed the fresh triggerLoom
	// dispatch with the SAME already-terminal Loom-instance identity
	// (deriveStableInstanceID is claimId-seeded, strategist.go), collapsing
	// the "retry" onto the dead episode as a no-op rather than the fresh
	// instance §10.3 calls for. Mint a fresh one in that case, mirroring
	// fireEpisode's stale-mark reclaim branch (dispatchGap's lane-1 analog of
	// this same §10.3 external-gap rule).
	claimID := rec.ClaimID
	if confirmedConcluded {
		if fresh, cErr := substrate.NewNanoID(); cErr == nil {
			claimID = fresh
		} else {
			e.logger.Warn("weaver sweep: fresh claimId mint failed; preserving the concluded episode's claimId (the retry may collapse onto it)",
				"targetId", targetID, "entityId", entityID, "gap", gapColumn, "err", cErr)
		}
	}

	// The atomic claim: replace the expired mark in place, conditioned on the
	// revision read this pass. A conflict means the key changed under the
	// sweep — a fresh episode CAS-created it, or its TTL marker landed — and
	// the current state owns the gap; skip.
	// resolvedAction is written back unchanged for every non-candidates gap
	// (== ga.Action) and re-pins the SAME candidate for a planned-mode one
	// (== rec.Action, by construction of resolvePlannedAction's pinned-lookup
	// branch) — the mark's Action never drifts across a reclaim.
	newRev, conflict, err := e.marks.replace(ctx, targetID, entityID, gapColumn, entityKey, resolvedAction, claimID, markRev, markTTL)
	if err != nil {
		e.logger.Warn("weaver sweep: reclaim re-arm failed; leaving expired mark for the next sweep",
			"targetId", targetID, "entityId", entityID, "gap", gapColumn, "err", err)
		return
	}
	if conflict {
		e.logger.Debug("weaver sweep: mark changed since read; skipping reclaim", "key", key)
		return
	}
	s.bump(&s.reclaims)
	e.logger.Warn("weaver sweep: mark reclaimed",
		"targetId", targetID, "entityId", entityID, "gap", gapColumn,
		"action", rec.Action, "reason", sweepReasonLeaseExpired)
	// A reclaim IS a fresh dispatch (a new episode against a re-armed mark), so it
	// advances the chain's retry-budget dispatch-count exactly like the lane-1
	// CAS-create path — this is how a multi-attempt chain driven by sweep
	// re-dispatches (not just CDC touches) accumulates toward maxretries_<g>.
	e.bumpDispatchCount(ctx, targetID, entityID, gapColumn)
	// The `__effect` confidence window books ATTEMPTS, not dispatches, and its
	// close side credits at most once per episode (clearClosedMarks flips the
	// oldest pending slot when the GAP closes). A collapse-only re-dispatch
	// mounts no new attempt — the consumer collapses it onto the artifact the
	// open episode already created — so booking one appends a pending slot no
	// close can ever answer. An episode held open across many reclaims (a human
	// userTask can sit for days) would fill its whole window that way and trip a
	// LensEffectMismatch that describes nothing real. The retry-budget count
	// above is deliberately NOT gated: it bounds reclaim effort per §10.8,
	// which is exactly what a repeat reclaim spends.
	if !collapseOnly {
		e.bumpEffectDispatch(ctx, targetID, gapColumn, resolvedAction)
	}
	e.bumpOscillation(ctx, targetID, resolvedAction)
	// Fresh episode: the requestId derives from the replace revision (a real new
	// dispatch attempt). claimID (preserved or freshly minted, above) seeds the
	// dispatch identity. A publish failure here leaves the fresh mark holding a
	// live lease, so the retry is real — the sweep re-attempts at that lease's
	// expiry, and a lane-1 redelivery re-fires the same fresh requestId before
	// then.
	if e.fire(ctx, targetID, entityID, gapColumn, newRev, claimID, pl) != substrate.Ack {
		e.logger.Warn("weaver sweep: reclaim re-dispatch did not publish; the fresh mark's lease bounds the retry",
			"targetId", targetID, "entityId", entityID, "gap", gapColumn)
	}
}

// deleteCorrupt deletes a weaver-state key the sweep can never act on
// (unreadable key or value, or reclaim evidence that cannot name a candidate)
// and alerts (Error log + Health KV issue) AFTER the delete succeeds — the
// issue text claims a deletion, so it must follow one. weaver-state is
// weaver-private: nothing else ever cleans it, so such an entry left in place
// lives forever. shape names the key family the deleted entry belonged to, so
// an operator reading the issue is not told a confidence window or a retry
// budget was a "mark". A revision conflict means the key changed under the
// sweep (skip — the new state is swept next pass); any other failure Warn-logs
// and retries next pass. The CorruptMark issue is retired by a later pass that
// no longer lists the key (see pass), or by retireCorrupt once the same key
// reads cleanly again.
func (s *sweeper) deleteCorrupt(ctx context.Context, key string, revision uint64, shape, reason string) {
	e := s.engine
	if err := e.conn.KVDeleteRevision(ctx, e.cfg.WeaverStateBucket, key, revision); err != nil {
		if errors.Is(err, substrate.ErrRevisionConflict) {
			return
		}
		e.logger.Warn("weaver sweep: corrupt mark delete failed", "key", key, "err", err)
		return
	}
	e.alert(issueKeySweep(key), "error", "CorruptMark",
		"weaver-state "+shape+" "+key+" was corrupt ("+reason+"); deleted")
	s.mu.Lock()
	s.corrupt++
	s.corruptAlerted[key] = struct{}{}
	s.mu.Unlock()
}

// retireCorrupt retires a standing CorruptMark issue for a key that has since
// been read and parsed cleanly — every leg calls it once its own value parses.
// It is the recurrence-tolerant counterpart to pass's listing-based retirement:
// that one fires only while the key stays ABSENT from a later listing, and
// every weaver-state key NAME comes back by construction. The next episode
// CAS-creates the same `<targetId>.<entityId>.<gapColumn>` mark, the next
// dispatch writes the same `__effect` window and recreates the same `…__count`
// budget — all at the very name a corrupt entry was deleted from — so a key
// that recurs promptly would carry its CorruptMark issue for the life of the
// process, describing a value that no longer exists. The issue is about the
// VALUE that was deleted, never the name. A key carrying no standing issue is a
// no-op.
func (s *sweeper) retireCorrupt(key string) {
	s.mu.Lock()
	_, alerted := s.corruptAlerted[key]
	delete(s.corruptAlerted, key)
	s.mu.Unlock()
	if alerted {
		s.engine.issues.clear(issueKeySweep(key))
	}
}

// deleteMark deletes one mark at the revision read this pass. A revision
// conflict means a fresh episode CAS-created the key between the sweep's read
// and this delete — the fresh episode is intact and the delete is skipped.
// Orphan deletes log at Warn (operator visibility); a gapClosed delete is the
// routine level reconcile and logs at Info.
func (s *sweeper) deleteMark(ctx context.Context, key string, revision uint64,
	action, reason, targetID, entityID, gapColumn string) bool {

	e := s.engine
	if err := e.conn.KVDeleteRevision(ctx, e.cfg.WeaverStateBucket, key, revision); err != nil {
		if errors.Is(err, substrate.ErrRevisionConflict) {
			e.logger.Debug("weaver sweep: mark changed since read; skipping delete", "key", key)
			return false
		}
		e.logger.Warn("weaver sweep: mark delete failed", "key", key, "err", err)
		return false
	}
	logArgs := []any{
		"targetId", targetID, "entityId", entityID, "gap", gapColumn,
		"action", action, "reason", reason,
	}
	if reason == sweepReasonGapClosed {
		e.logger.Info("weaver sweep: mark cleared", logArgs...)
		// A gapClosed delete is a CLOSE event for the §10.3 `__effect` window,
		// exactly as it is on lane-1 (clearClosedMarks) — the sweep is simply
		// whichever leg observed the close first, and for a row that has gone
		// quiet it is the ONLY leg that will. Crediting only lane-1 biased every
		// window toward zero closes and made false LensEffectMismatch warnings
		// likelier the more the sweep won. Only a WON delete credits, which
		// settles sweep-vs-sweep; lane-1's own delete is not revision-
		// conditioned, so a lane-1 delivery racing this pass on the same closed
		// gap can still credit alongside it. That over-credits by one slot only
		// when another episode of this same (target, gap, action) is
		// concurrently pending — a far narrower error, and in the safe
		// direction, versus the systematic zero-close bias removed here. The
		// orphan reasons never credit: targetRemoved/orphanColumn mean the gap
		// went away rather than closed, and sweepEffect deletes those windows
		// outright.
		if cErr := e.marks.recordEffectClose(ctx, targetID, gapColumn, action); cErr != nil {
			e.logger.Warn("weaver sweep: effect close record failed",
				"targetId", targetID, "entityId", entityID, "gap", gapColumn, "err", cErr)
		}
		// This entity's standing GapBudgetExhausted retires with the close: the
		// issue is keyed per (target, entity, gap), and this leg is whichever
		// one observed the close first. Only the entity scope retires here — a
		// mark close says nothing about the playbook, and lane-1's own delivery
		// of the closed (or deleted) row is what retires the target-scoped
		// config issues. Idempotent when none stands.
		e.issues.clear(issueKeyGapEntity(targetID, entityID, gapColumn))
	} else {
		e.logger.Warn("weaver sweep: mark reclaimed", logArgs...)
	}
	return true
}

// warmedUp reports whether the registry warm-up window has elapsed (the gate
// on both orphan legs — see sweeper.warmup).
func (s *sweeper) warmedUp() bool {
	return time.Since(s.startedAt) >= s.warmup
}

func (s *sweeper) bump(counter *int64) {
	s.mu.Lock()
	*counter++
	s.mu.Unlock()
}

// metrics snapshots the since-start sweep counters for the heartbeat.
func (s *sweeper) metrics() (reclaims, reclaimsSuppressed, reArms, orphansDeleted, corrupt int64, lastRunAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reclaims, s.reclaimsSuppressed, s.reArms, s.orphansDeleted, s.corrupt, s.lastRunAt
}

// leaseLive reports whether leaseExpiresAt is set and in the future. An absent
// lease reads as expired: a lease-less mark carries no per-key TTL either, so
// treating it as live would make it immortal. An unparseable lease also reads
// as expired — the reclaim replaces it with a well-formed mark, and the delete
// is revision-conditioned, so the failure mode is a (rare-double) re-dispatch,
// never a lost episode.
func leaseLive(leaseExpiresAt string, now time.Time) bool {
	if leaseExpiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, leaseExpiresAt)
	if err != nil {
		return false
	}
	return t.After(now)
}

// splitMarkKey splits a §10.3 mark key <targetId>.<entityId>.<gapColumn>.
// targetId and gapColumn are install-validated single dot-free tokens and the
// entity segment is the bare NanoID, so the split is positional; anything that
// does not parse is corrupt.
func splitMarkKey(key string) (targetID, entityID, gapColumn string, ok bool) {
	i := strings.IndexByte(key, '.')
	if i <= 0 {
		return "", "", "", false
	}
	rest := key[i+1:]
	j := strings.IndexByte(rest, '.')
	if j <= 0 {
		return "", "", "", false
	}
	targetID, entityID, gapColumn = key[:i], rest[:j], rest[j+1:]
	if !substrate.IsValidNanoID(entityID) ||
		!singleTokenPattern.MatchString(targetID) ||
		!singleTokenPattern.MatchString(gapColumn) {
		return "", "", "", false
	}
	return targetID, entityID, gapColumn, true
}

func issueKeySweep(markKey string) string { return "sweep:" + markKey }
