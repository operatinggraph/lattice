package weaver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
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

// sweeper is the §10.3 active reconciler: on each pass it enumerates every
// weaver-state mark (the bucket holds ONLY marks, bounded by the in-flight
// count) and level-reconciles each against its current weaver-targets row —
// clearing closed/orphaned marks promptly and reclaiming expired leases with a
// fresh dispatch episode. The sweep is the primary reclaim lane; the mark's
// per-key TTL is only the backstop, so the sweep must observe an expired lease
// while the key still exists (TTL = markTTLBackstopFactor × lease plus the
// withDefaults SweepInterval ≤ MarkLease clamp guarantee that window). There
// is no watcher on the weaver-state backing stream — the sweep is
// interval-cadence by design.
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
	orphansDeleted     int64
	corrupt            int64
	lastRunAt          time.Time
	// corruptAlerted tracks mark keys carrying a standing CorruptMark issue;
	// the issue is retired by the first completed pass that no longer lists
	// the key (the delete held).
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

// pass sweeps every current mark, then retires CorruptMark issues whose keys
// are no longer listed (the corrupt entry was deleted on an earlier pass and
// stayed gone) — a one-off corrupt mark must not degrade the heartbeat for
// the life of the process.
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
		if strings.HasSuffix(key, controlKeySuffix) || strings.HasSuffix(key, countKeySuffix) {
			// Neither the `<targetId>.__control` dispatch-skip marker nor a
			// `…__count` retry-budget dispatch-count is a §10.3 mark — the
			// control marker has no <entityId>.<gapColumn> tail and the count has
			// a 4th segment, so splitMarkKey would reject each as corrupt. Both
			// are reserved and engine-owned (the control marker via
			// Disable/Enable/Revoke; the count via fireEpisode/reclaim increment
			// and clearClosedMarks reset). The sweep never enumerates, reclaims,
			// or deletes either; the count's gap-close reset and its long TTL
			// backstop are its only lifecycle.
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
}

// sweepMark level-reconciles one mark against its current row and lease:
//
//	(a) corrupt key/value → alert + delete (weaver-state is weaver-private;
//	    garbage otherwise lives forever);
//	(b) row gone, or missing_<gapColumn> not currently true → delete (the
//	    sweep leg of §10.3 level-reconciled clearing — a mark may only stand
//	    for a currently-true column). An UNPARSEABLE row leaves the mark:
//	    never delete on unreadable evidence (the lease/TTL backstop bounds it);
//	(c) column true and lease unexpired → leave, the episode is in flight;
//	(d) column true and lease expired (or absent — a lease-less mark carries
//	    no TTL either and would otherwise be immortal) → reclaim.
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
		s.deleteCorrupt(ctx, key, entry.Revision,
			"mark key is not <targetId>.<entityId>.<gapColumn>")
		return
	}
	var rec mark
	if err := json.Unmarshal(entry.Value, &rec); err != nil {
		s.deleteCorrupt(ctx, key, entry.Revision, "mark value unparseable: "+err.Error())
		return
	}

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
	if !e.boolColumn(targetID, row, gapColumn) {
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
	s.reclaim(ctx, key, entry.Revision, &rec, targetID, entityID, gapColumn, row, rowEntry.Revision)
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

	if !e.boolColumn(targetID, row, "violating") {
		// Mirrors lane-1's L1 gate (handleRow dispatches only violating rows):
		// an open missing_* on a non-violating row must not be re-dispatched
		// here when lane-1 never would fire it. Leave the mark to level
		// clearing or the next CDC delivery; the TTL backstop bounds a stale
		// one.
		return
	}

	if e.gapSuppressed(ctx, targetID, entityID, row, gapColumn) {
		// Mirrors lane-1's dispatch-suppression gate: a gap with inflight_<g> set
		// or whose weaver-state dispatch-count has reached maxretries_<g> must NOT
		// be re-dispatched. This is the LOAD-BEARING skip — the mark-lease expiry →
		// reclaim is the actual re-dispatch path for a long-pending external call
		// (the lane-1 skip alone does not stop the sweep). Leave the expired mark;
		// it is cleared by level reconcile once the gap closes, and the TTL
		// backstop bounds it if not. The gap stays violating throughout — only
		// re-dispatch is suppressed.
		return
	}

	// Default per-key TTL backstop for the re-armed mark; widened below for a
	// paced userTask reclaim.
	markTTL := markTTLBackstopFactor * e.marks.lease
	if rec.Action == actionAssignTask || rec.Action == actionTriggerLoom || rec.Action == actionProposedOp {
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
		// (non-Augur) directOp/external reclaim never reaches here (action check)
		// — it is the intended bounded retry (§inflight_<g>/maxretries_<g>), never
		// backed off. Best-effort: a count read or ClaimedAt parse failure falls
		// through to a normal (unpaced) reclaim.
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
					"action", rec.Action, "dispatchCount", count, "elapsed", elapsed)
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

	entityKey, _ := row["entityKey"].(string)
	if entityKey == "" {
		// Without the §10.2 entityKey echo the remediation cannot name its
		// candidate — and an expired mark over such a row can never be
		// reclaimed, so leaving it would re-alert on every pass forever (a
		// lease-less mark has no TTL to bound it). Treat the pair as corrupt
		// evidence: alert + delete; the next well-formed row delivery
		// dispatches fresh.
		s.deleteCorrupt(ctx, key, markRev,
			"row "+targetID+"."+entityID+" is violating but carries no entityKey")
		return
	}

	// Plan BEFORE touching the expired mark: a failed plan (unresolved
	// reference, template data error) alerts through the shared planGap issue
	// keys and leaves the mark in place — the next sweep retries. Bounded,
	// loud, never a hot loop.
	pl, _ := e.planGap(targetID, entityID, gapColumn, ga, row, rowRevision)
	if pl == nil {
		e.logger.Warn("weaver sweep: reclaim plan failed; leaving expired mark for the next sweep",
			"targetId", targetID, "entityId", entityID, "gap", gapColumn)
		return
	}

	// The atomic claim: replace the expired mark in place, conditioned on the
	// revision read this pass. A conflict means the key changed under the
	// sweep — a fresh episode CAS-created it, or its TTL marker landed — and
	// the current state owns the gap; skip.
	// Preserve the mark's per-open-episode claimId across the reclaim (§10.3): a
	// reclaim is the SAME open episode, so the userTask identity it seeds stays
	// stable and the re-dispatch collapses on the existing task/instance.
	newRev, conflict, err := e.marks.replace(ctx, targetID, entityID, gapColumn, entityKey, ga.Action, rec.ClaimID, markRev, markTTL)
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
	// Fresh episode: the requestId derives from the replace revision (a real new
	// dispatch attempt). The preserved claimId keeps the userTask identity stable,
	// so the new attempt collapses on the existing task/instance. A publish failure
	// here leaves the fresh mark holding a live lease, so the retry is real — the
	// sweep re-attempts at that lease's expiry, and a lane-1 redelivery re-fires
	// the same fresh requestId before then.
	if e.fire(ctx, targetID, entityID, gapColumn, newRev, rec.ClaimID, pl) != substrate.Ack {
		e.logger.Warn("weaver sweep: reclaim re-dispatch did not publish; the fresh mark's lease bounds the retry",
			"targetId", targetID, "entityId", entityID, "gap", gapColumn)
	}
}

// deleteCorrupt deletes a mark the sweep can never act on (unreadable key or
// value, or reclaim evidence that cannot name a candidate) and alerts (Error
// log + Health KV issue) AFTER the delete succeeds — the issue text claims a
// deletion, so it must follow one. weaver-state is weaver-private: nothing
// else ever cleans it, so such an entry left in place lives forever. A
// revision conflict means the key changed under the sweep (skip — the new
// state is swept next pass); any other failure Warn-logs and retries next
// pass. The CorruptMark issue is retired by a later pass that no longer lists
// the key (see pass).
func (s *sweeper) deleteCorrupt(ctx context.Context, key string, revision uint64, reason string) {
	e := s.engine
	if err := e.conn.KVDeleteRevision(ctx, e.cfg.WeaverStateBucket, key, revision); err != nil {
		if errors.Is(err, substrate.ErrRevisionConflict) {
			return
		}
		e.logger.Warn("weaver sweep: corrupt mark delete failed", "key", key, "err", err)
		return
	}
	e.alert(issueKeySweep(key), "error", "CorruptMark",
		"weaver-state mark "+key+" was corrupt ("+reason+"); deleted")
	s.mu.Lock()
	s.corrupt++
	s.corruptAlerted[key] = struct{}{}
	s.mu.Unlock()
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
func (s *sweeper) metrics() (reclaims, reclaimsSuppressed, orphansDeleted, corrupt int64, lastRunAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reclaims, s.reclaimsSuppressed, s.orphansDeleted, s.corrupt, s.lastRunAt
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
