package weaver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/weaver/planner"
)

// handleRow is the lane-1 handler: one KV-CDC message = the current state of
// one weaver-targets row (value = the §10.2 row JSON; an empty body is the
// entity-deletion tombstone). The handler is level-driven and idempotent —
// at-least-once redelivery re-evaluates the same row against the same durable
// marks and converges to the same dispatch set.
func (e *Engine) handleRow(ctx context.Context, msg substrate.Message) substrate.Decision {
	key := strings.TrimPrefix(msg.Subject, e.rowSubjectPrefix)
	targetID, entityID, ok := splitRowKey(key)
	if !ok {
		// Redelivery cannot fix a malformed key; drop it loudly.
		e.logger.Warn("weaver: row key is not <targetId>.<entityId>; dropping", "key", key)
		return substrate.Ack
	}
	target, ok := e.source.target(targetID)
	if !ok {
		// The target was removed/rejected but its consumer has not been torn
		// down yet (the reconcile runs on registry callbacks). Drop.
		e.logger.Debug("weaver: row for unregistered target; dropping", "targetId", targetID)
		return substrate.Ack
	}

	// An empty body is the entity-deletion tombstone (§10.2 IsDeleted path):
	// no row columns remain true, so the level reconcile clears every mark.
	var row map[string]any
	if len(msg.Body) != 0 {
		if err := json.Unmarshal(msg.Body, &row); err != nil {
			e.logger.Warn("weaver: row value unparseable; dropping", "key", key, "err", err)
			return substrate.Ack
		}
	}

	// Level-reconciled mark-clearing runs on EVERY row update first, violating
	// or not (§10.3: never edge-triggered — a coalescing watch can drop the
	// transitional flip). A mark only ever exists at a gap column the playbook
	// names, so the candidate set is the union of the playbook's gaps keys and
	// the row's missing_* columns; any candidate whose missing_<col> is not
	// currently true has its mark deleted. This single code path also clears
	// the marks of a closed gap and of a deleted entity. A clearing failure is
	// retried on a delayed cadence so a persistent KV failure cannot hot-loop.
	if !e.clearClosedMarks(ctx, target, targetID, entityID, row) {
		return substrate.NakWithDelay
	}

	// Contraction monitor (design weaver-planner-mandate-design.md §3.4):
	// records this row's current violating state on EVERY delivery, violating
	// or not, including the tombstone case (row == nil reads as boolColumn's
	// safe nil-map false) — the heartbeat-cadence trajectory input. Purely
	// in-memory bookkeeping; runs even for a disabled target, mirroring
	// mark-clearing above.
	violating := row != nil && e.boolColumn(targetID, row, "violating")
	e.contraction.observe(targetID, entityID, violating)

	if row == nil {
		return substrate.Ack
	}

	// Lane-3 scheduling leg: a row carrying a future freshUntil (re-)arms its
	// per-target-per-entity @at timer on EVERY delivery, violating or not —
	// level-driven, idempotent under one-schedule-per-subject replace. Runs
	// even for a disabled target: arming the timer is state-recording
	// bookkeeping, so an instant re-enable loses no deadline. Only a
	// schedule-publish failure defers the row.
	if !e.scheduleFreshness(ctx, targetID, entityID, key, row) {
		return substrate.NakWithDelay
	}

	// Dispatch-skip: a target carrying the `<targetId>.__control`
	// disabled marker (reflected in the in-memory disabled-set) Acks
	// here — mark-clearing (above) and freshness arming (above) still ran (a
	// disabled target keeps its violation-detection bookkeeping current), but
	// no NEW in-flight mark is created and no remediation
	// (Strategist/Actuator: triggerLoom/assignTask/directOp) runs for
	// this row. On enable, remediation resumes for whatever is still violating.
	if e.isTargetDisabled(targetID) {
		return substrate.Ack
	}

	if !violating {
		// L1: not violating — clearing already ran; nothing to dispatch.
		return substrate.Ack
	}

	entityKey, _ := row["entityKey"].(string)
	if entityKey == "" {
		// §10.2 requires the entityKey echo; without it the mark and the
		// remediation cannot name the candidate. A single malformed anchor row is
		// a per-row DATA error: surface it and skip the row, but keep remediating
		// every other row — Weaver still fulfils its primary responsibility, so
		// this is a Contract #5 §5.2 `warning` (degraded), never an `error`
		// (unhealthy = cannot fulfil the responsibility). Redelivery cannot fix
		// the projected row.
		msg := "weaver-targets row " + key + " is violating but carries no entityKey"
		e.logger.Warn("weaver: " + msg)
		e.issues.set(issueKeyData(targetID, "entityKey"), "warning", "RowDataError", msg)
		return substrate.Ack
	}

	nak := false
	delayed := false
	for _, col := range e.openGapColumns(targetID, row) {
		if suppressed, exhausted := e.gapSuppressed(ctx, targetID, entityID, row, col); suppressed {
			// A remediation is in flight (inflight_<g>): ordinary in-progress
			// state, never escalated — the gap stays violating but must NOT be
			// (re-)dispatched. Skip it — mark-clearing already ran above, so a
			// stale mark does not linger.
			//
			// The retry budget is spent (maxretries_<g> reached): a decision
			// point, not a park (Contract #10 §10.8 Planner extension: "budget
			// exhaustion... raises a standing Health issue at the suppression
			// site, never a silent park") — escalateExhaustedGap redirects to
			// the Augur AI tier if the target opts "exhausted" into its augur
			// block, else raises that standing issue itself.
			if exhausted {
				switch e.escalateExhaustedGap(ctx, target, targetID, entityID, entityKey, col, row, msg.Sequence) {
				case substrate.Nak:
					nak = true
				case substrate.NakWithDelay:
					delayed = true
				default:
				}
			}
			continue
		}
		switch e.dispatchGap(ctx, target, targetID, entityID, entityKey, col, row, msg) {
		case substrate.Nak:
			nak = true
		case substrate.NakWithDelay:
			delayed = true
		default:
		}
	}
	if nak {
		// At least one gap needs an immediate retry; redelivery re-evaluates
		// every gap idempotently (existing marks re-fire the same episode
		// requestId).
		return substrate.Nak
	}
	if delayed {
		// Only delayed-retry gaps (unresolved references, metadata gaps) —
		// redeliver on the bounded cadence, never a hot loop.
		return substrate.NakWithDelay
	}
	return substrate.Ack
}

// dispatchGap runs Evaluator L2 + Strategist + Actuator for one open gap.
//
// Dispatch OCC (§10.8): the weaver-state CAS-create is the anti-storm gate —
// create wins → dispatch; create loses → the winner dispatched, drop. The
// in-flight skip applies to FIRST deliveries only: on a redelivery
// (msg.NumDelivered > 1, i.e. a prior delivery Nak'd or crashed before ack)
// EVERY in-flight gap on the row re-fires its episode requestId — the
// redelivery signal is per-message, not per-gap, so the retry is a blanket
// re-fire across the row's in-flight gaps. Each re-fire derives the same
// requestId from its mark's create revision and collapses on the Contract #4
// tracker, so the blanket retry never double-acts and a lost publish is not
// wedged behind its own mark.
func (e *Engine) dispatchGap(ctx context.Context, target *Target, targetID, entityID, entityKey, col string,
	row map[string]any, msg substrate.Message) substrate.Decision {

	ga, ok := target.Gaps[col]
	if !ok {
		// No playbook entry for this gap. If the target's augur policy escalates
		// `unplannable` (Contract #10 §10.8 "Augur escalation"), redirect the
		// dead-end to the AI reasoning tier: dispatch the reasoning op directly
		// as a directOp → bridge (Option F — single-step episode, no Loom
		// wrapper). Otherwise it is a config error: alert, never silently
		// skipped (FR29 discipline).
		esc, escalated := augurEscalation(e.source, target, escalateUnplannable, targetID, entityID, entityKey, col)
		if !escalated {
			e.alert(issueKeyGap(targetID, col), "error", "GapWithoutPlaybook",
				"target "+targetID+": row column "+col+" is true but the playbook defines no gaps entry for it")
			return substrate.Ack
		}
		// The augur policy now covers this gap — clear any GapWithoutPlaybook
		// alert raised before the policy was added, and dispatch the reasoning
		// episode through the normal lane-1 path (anti-storm mark + OCC + reclaim).
		e.issues.clear(issueKeyGap(targetID, col))
		ga = esc
	}

	if ga.Action == actionSurface {
		// FR29: surface-only, never dispatch. No mark, no OCC, no episode —
		// just a Health-KV issue for as long as the gap stays open (cleared by
		// clearClosedMarks below when the row stops naming this column).
		sev := ga.IssueSeverity
		if sev == "" {
			sev = "warning"
		}
		code := ga.IssueCode
		if code == "" {
			code = "Surface"
		}
		e.issues.set(issueKeyGap(targetID, col), sev, code,
			"target "+targetID+": row column "+col+" is true")
		return substrate.Ack
	}

	// Fire 4 shadow comparison (Contract #10 §10.8 Planner extension):
	// diagnostic-only, never alters what fires below. A no-op unless the
	// target is mode:"shadow" and this gap declares candidates.
	e.shadowCompare(ctx, target, targetID, entityID, col, ga, row)

	// The row's substrate per-key revision arrives free on the CDC message
	// (the backing-stream sequence IS the KV revision) — the op payload's OCC
	// revision-condition. A zero sequence means JetStream metadata is
	// unavailable: never publish expectedRevision 0 (the "must not exist" OCC
	// sentinel) — defer to a delayed redelivery, which carries metadata.
	if msg.Sequence == 0 {
		e.logger.Warn("weaver: message metadata unavailable (sequence 0); deferring gap dispatch",
			"targetId", targetID, "entityId", entityID, "gap", col)
		return substrate.NakWithDelay
	}

	// Read the mark ONCE, up front: both the Fire 5 planned-mode candidate
	// resolution (reuse an existing pin, never re-rank one) and the fire
	// decision below must see the exact same snapshot — reading it twice
	// could let it change in between (e.g. a legitimate close→reopen) and
	// plan against a pin that no longer describes the episode actually being
	// fired.
	rec, markRev, found, err := e.marks.get(ctx, targetID, entityID, col)
	if err != nil {
		e.logger.Error("weaver: mark read failed; nak with delay",
			"targetId", targetID, "entityId", entityID, "gap", col, "err", err)
		return substrate.NakWithDelay
	}
	pinnedAction := ""
	if found {
		pinnedAction = rec.Action
	}

	// Fire 6, R1: a goal-mode gap's pinned LEG may have already completed
	// (its declared effects now hold in the row) even though the gap's own
	// missing_<g> column is still open — a chain mid-flight, not a closed
	// gap. releaseCompletedLeg clears the leg's mark/count/effect-close
	// bookkeeping and, on release, the rest of this call proceeds as a
	// genuinely fresh episode: planGap synthesizes/dispatches the NEXT leg
	// from the now-advanced state. A no-op for every non-goal gap.
	if found && e.releaseCompletedLeg(ctx, targetID, entityID, col, ga, pinnedAction, row, markRev) {
		found = false
		pinnedAction = ""
	}

	pl, action, dec := e.planGap(ctx, target, targetID, entityID, col, ga, row, msg.Sequence, pinnedAction)
	if pl == nil {
		return dec
	}

	// redelivered classifies this delivery for the in-flight branch: only
	// NumDelivered 1 is a definitively FRESH delivery (the anti-storm drop).
	// NumDelivered 0 (metadata unavailable) deliberately counts as a
	// redelivery: it may be a retry whose prior delivery never published, and
	// re-firing is the safe side (the same episode requestId collapses on the
	// Contract #4 tracker; a drop could wedge a lost publish behind its own
	// mark).
	//
	// staleMark reports whether col is an EXTERNAL gap (Contract #10 §10.3: "a
	// legitimate close→reopen... mints a new claimId ⇒ a fresh artifact...
	// External gaps are unchanged — their reclaim re-dispatch is intended
	// (re-call a dead vendor / mint a fresh service instance), episode-scoped
	// on markRevision and bounded by inflight_<g> + maxretries_<g>") — a lens
	// author marks a gap as belonging to that class by declaring its
	// inflight_<g> companion column at all; the human userTask gaps
	// (assignTask, and triggerLoom of a userTask-containing pattern) declare
	// no such column and are structurally untouched by this branch (staleMark
	// returns false immediately below), so their claimId stays preserved
	// verbatim exactly as §10.3 requires. staleMark reading false on a
	// declared column does NOT need to distinguish "confirmed concluded" from
	// "not yet dispatched" — for a real external gap, inflight_<g> is computed
	// from actual outcome-presence (e.g. "dispatch aspect set, outcome aspect
	// absent"), so it cannot read false while a call is genuinely still
	// pending: a still-pending call keeps inflight_<g>=true, which
	// gapSuppressed (checked before dispatchGap is ever reached) would already
	// be blocking on. The additional `!leaseLive` requirement below only rules
	// out the brief, same-process propagation-lag window right after THIS
	// mark's own fresh dispatch (before its own effects have reprojected into
	// this row) — an in-memory CDC round trip, milliseconds, not the mark's
	// lease (seconds to the production default of 30 minutes).
	stale := found && !leaseLive(rec.LeaseExpiresAt, time.Now()) && e.staleMark(targetID, row, col)
	return e.fireEpisode(ctx, targetID, entityID, entityKey, col, action, pl, msg.NumDelivered != 1, rec, markRev, found, stale)
}

// staleMark reports whether gap column col is declared as an EXTERNAL gap
// (Contract #10 §10.3) whose row currently shows no call in flight, so a
// found mark for it is a stale bookkeeping remnant of a concluded attempt,
// not a live episode. See the dispatchGap call site for the full contract
// citation and why an absent inflight_<g> column (the human userTask gaps)
// makes this unconditionally false, leaving their reclaim untouched (claimId
// preserved verbatim, per §10.3).
func (e *Engine) staleMark(targetID string, row map[string]any, col string) bool {
	g, ok := strings.CutPrefix(col, gapColumnPrefix)
	if !ok {
		return false
	}
	if _, declared := row[inflightColumnPrefix+g]; !declared {
		return false
	}
	return !e.boolColumn(targetID, row, inflightColumnPrefix+g)
}

// planGap resolves one gap's plan (Evaluator L2 + Strategist), routing a
// failure by its class: an unresolved reference defers on the bounded
// redelivery cadence; a config or data error is surfaced and the gap skipped
// (retrying cannot fix it). The two carry different Contract #5 §5.2
// severities: a per-row DATA error (a malformed/incomplete anchor row whose
// template references resolve null) is a `warning` (degraded) — one bad row,
// every other row still remediates, so Weaver fulfils its responsibility; a
// CONFIG error (a package playbook missing a gaps entry, an un-dispatchable
// action) is an `error` (unhealthy) — it affects every row of the target and
// only a package re-author can fix it. pl == nil means do not dispatch — the
// returned Decision is the caller's disposition for this gap.
//
// pinnedAction (Fire 5/6) is the mark's currently-recorded actionRef, or ""
// for a genuinely fresh episode — the sole input resolvePlannedAction needs
// to tell "pick fresh" from "reuse the pin" apart for a planned-mode
// candidates-only or goal-only gap; every other gap shape ignores it. The
// returned string is the resolved actionRef (== ga.Action unchanged for
// every non-planned gap; the picked candidate's Action; or a goal leg's own
// catalog Ref) the caller threads into the mark/effect-bookkeeping so a
// fresh pick gets recorded, and a reused pin gets re-recorded identically.
//
// A goal gap's Synthesize dead-end (planner.ErrNoPlan) — or a redelivered
// episode whose pin was itself a prior escalation (resolveGoalAction's doc)
// — surfaces as an unplannable-flagged *planError; before falling through to
// its ordinary disposition, this retries EXACTLY the same
// augur.escalate("unplannable") policy dispatchGap's "no playbook entry"
// dead-end already uses (Contract #10 §10.8 "Augur escalation" — "its
// meaning extends to 'no playbook entry AND no derivable plan'; no new
// trigger token"), so a target with that policy redirects a stuck goal chain
// to AI reasoning instead of alerting forever.
func (e *Engine) planGap(ctx context.Context, target *Target, targetID, entityID, col string, ga GapAction, row map[string]any,
	rowRevision uint64, pinnedAction string) (*plan, string, substrate.Decision) {

	resolved, actionRef, perr := e.resolvePlannedAction(ctx, target, targetID, entityID, col, ga, row, pinnedAction)
	if perr != nil && perr.unplannable {
		entityKey, _ := row["entityKey"].(string)
		if esc, escalated := augurEscalation(e.source, target, escalateUnplannable, targetID, entityID, entityKey, col); escalated {
			e.issues.clear(issueKeyGap(targetID, col))
			resolved, actionRef, perr = esc, esc.Action, nil
		}
	}
	if perr == nil {
		if !e.admitGap(target, targetID, entityID, col, resolved.Adapter, row) {
			// Fire 8 admission control (design §3.4): a declared budget has no
			// spare capacity for this gap right now. No mark, no plan, no
			// issue — this is ordinary pacing, not a fault; the redelivery
			// cadence is the retry, exactly like an unresolved-reference defer.
			e.logger.Debug("weaver: gap dispatch deferred by admission control",
				"targetId", targetID, "entityId", entityID, "gap", col)
			return nil, "", substrate.NakWithDelay
		}
		var pl *plan
		if pl, perr = buildPlan(e.source, targetID, entityID, col, resolved, row, rowRevision); perr == nil {
			e.issues.clear(issueKeyGap(targetID, col))
			e.issues.clear(issueKeyData(targetID, col))
			return pl, actionRef, substrate.Ack
		}
	}
	switch perr.kind {
	case errTransient:
		// An unresolved reference may be replay lag or a permanent config
		// error (a typo'd pattern, an uninstalled package) — retry on the
		// bounded redelivery cadence (never a hot loop) and surface to
		// Health until it resolves; the issue clears on the first
		// successful plan.
		e.logger.Warn("weaver: gap dispatch deferred; nak with delay for redelivery",
			"targetId", targetID, "entityId", entityID, "gap", col, "reason", perr.msg)
		e.issues.set(issueKeyGap(targetID, col), "warning", "UnresolvedReference",
			"target "+targetID+" gap "+col+": "+perr.msg)
		return nil, "", substrate.NakWithDelay
	case errData:
		msg := "target " + targetID + " gap " + col + ": " + perr.msg
		e.logger.Warn("weaver: " + msg)
		e.issues.set(issueKeyData(targetID, col), "warning", "TemplateDataError", msg)
		return nil, "", substrate.Ack
	default:
		e.alert(issueKeyGap(targetID, col), "error", "PlaybookConfigError",
			"target "+targetID+" gap "+col+": "+perr.msg)
		return nil, "", substrate.Ack
	}
}

// admitGap consults Fire 8's admission scheduler for one resolved gap
// dispatch, called from BOTH fresh-dispatch seams planGap serves (lane-1's
// dispatchGap and the reconciler's reclaim) — mirroring bumpEffectDispatch/
// bumpOscillation's "same two seams" precedent, so a declared budget paces
// reclaim re-fires exactly like fresh episodes. target.Admission == nil (every
// target before this fire) short-circuits true without reading the row's
// priority column — byte-identical dispatch. id is the mark-key shape
// (<targetId>.<entityId>.<gapColumn>), a stable identity for this gap's
// pending-admission entry across redeliveries.
func (e *Engine) admitGap(target *Target, targetID, entityID, col, adapter string, row map[string]any) bool {
	if target.Admission == nil {
		return true
	}
	priority, _ := e.intColumn(targetID, row, admissionPriorityColumn)
	id := targetID + "." + entityID + "." + col
	return e.admission.admit(target.Admission, targetID, id, adapter, priority, time.Now())
}

// fireEpisode is the lane-1 dispatch core: CAS-create the mark on absence
// (the dispatch OCC) and fire the episode op. rec/markRev/found/stale are the
// caller's own already-read mark snapshot (dispatchGap reads it once, up
// front, so the Fire 5 candidate-pin resolution and this fire decision never
// see two different mark states). redelivered selects the genuinely-in-flight
// disposition — false drops (the anti-storm gate: another episode is in
// flight), true re-publishes the SAME episode requestId (idempotent at the
// Contract #4 tracker). stale (staleMark) reclaims the mark in place instead —
// see that branch. The reconciler sweep's OWN reclaim does not pass through
// here for its lease-expiry case: it replaces the expired mark in place under
// a revision condition and fires directly, independently. action is recorded
// on the mark (the §10.3 value shape) so a later reclaim can re-dispatch the
// right episode.
func (e *Engine) fireEpisode(ctx context.Context, targetID, entityID, entityKey, col, action string,
	pl *plan, redelivered bool, rec *mark, markRev uint64, found, stale bool) substrate.Decision {

	if found && !stale {
		if !redelivered {
			// A fresh delivery while the episode is genuinely in flight — the
			// anti-storm drop.
			return substrate.Ack
		}
		// Redelivery retry path: re-publish the same episode with the existing
		// mark's preserved claimId (so the userTask identity stays stable).
		return e.fire(ctx, targetID, entityID, col, markRev, rec.ClaimID, pl)
	}

	if found && stale {
		// col is an EXTERNAL gap (staleMark's doc: a lens-declared inflight_<g>
		// companion, currently false) with an already-expired lease — nothing
		// has cleared its mark yet (clearClosedMarks only fires once the GAP
		// itself closes, still open here; only the prior ATTEMPT concluded),
		// and the sweep's lease-based reclaim may not have ticked yet or may
		// lose the race against the mark's own TTL. Reclaim it in place with
		// the SAME CAS-replace the reconciler sweep uses for an expired lease,
		// rather than a bare create (which would just lose the CAS against the
		// still-present key, silently dropping this delivery exactly like the
		// bug this branch fixes) or leaving it (which would wedge the gap
		// behind a mark nothing else promptly clears).
		//
		// Mints a FRESH claimId rather than preserving rec.ClaimID — Contract
		// #10 §10.3: "External gaps... their reclaim re-dispatch is intended
		// (re-call a dead vendor / mint a fresh service instance)," unlike the
		// human userTask gaps (assignTask; triggerLoom of a userTask-containing
		// pattern), whose §10.3-mandated claimId-verbatim preservation this
		// branch never reaches (they declare no inflight_<g> column, so
		// staleMark is unconditionally false for them — see dispatchGap).
		// Reusing the old claimId here would seed the fresh triggerLoom
		// dispatch with the SAME already-terminal Loom-instance identity
		// (deriveStableInstanceID is claimId-seeded, strategist.go), making it
		// a no-op collapse rather than the fresh service instance §10.3 calls
		// for.
		claimID, err := substrate.NewNanoID()
		if err != nil {
			e.logger.Error("weaver: stale mark reclaim claimId mint failed; nak with delay",
				"targetId", targetID, "entityId", entityID, "gap", col, "err", err)
			return substrate.NakWithDelay
		}
		rev, conflict, err := e.marks.replace(ctx, targetID, entityID, col, entityKey, action, claimID,
			markRev, markTTLBackstopFactor*e.marks.lease)
		if err != nil {
			e.logger.Error("weaver: stale mark reclaim failed; nak with delay",
				"targetId", targetID, "entityId", entityID, "gap", col, "err", err)
			return substrate.NakWithDelay
		}
		if conflict {
			// The mark changed since this delivery's read — a concurrent
			// reclaim (a redelivery of this same message, or the sweep) already
			// won; the winner dispatched.
			return substrate.Ack
		}
		e.bumpDispatchCount(ctx, targetID, entityID, col)
		e.bumpEffectDispatch(ctx, targetID, col, action)
		e.bumpOscillation(ctx, targetID, action)
		return e.fire(ctx, targetID, entityID, col, rev, claimID, pl)
	}

	rev, claimID, lost, err := e.marks.create(ctx, targetID, entityID, col, entityKey, action)
	if err != nil {
		e.logger.Error("weaver: mark create failed; nak with delay",
			"targetId", targetID, "entityId", entityID, "gap", col, "err", err)
		return substrate.NakWithDelay
	}
	if lost {
		// A concurrent evaluation won the CAS — the winner dispatched.
		return substrate.Ack
	}
	// The CAS-create won: a fresh episode is being dispatched, so the chain's
	// retry-budget dispatch-count advances by one. This is the SOLE
	// per-anti-storm-window increment in lane-1 — the redelivery re-fire above
	// re-publishes the existing episode and must not double-count. The sweep's
	// reclaim increments at its own fresh-dispatch point (reconciler.fire-after-
	// replace). A failed increment is logged but never blocks the dispatch: the
	// budget is a backstop, and over-counting (re-incrementing on a redelivery
	// that lost the CAS) is structurally impossible, while under-counting only
	// allows one extra attempt — far safer than wedging a live dispatch.
	e.bumpDispatchCount(ctx, targetID, entityID, col)
	e.bumpEffectDispatch(ctx, targetID, col, action)
	e.bumpOscillation(ctx, targetID, action)
	return e.fire(ctx, targetID, entityID, col, rev, claimID, pl)
}

// bumpDispatchCount increments the gap's chain-scoped retry-budget dispatch-count
// on an actual fresh dispatch (the CAS-create-won lane-1 path and the sweep's
// reclaim). A failure is logged, never propagated: the count is a bound, not a
// gate on the dispatch itself, and the gapSuppressed read tolerates a stale count
// on the safe (dispatch) side.
func (e *Engine) bumpDispatchCount(ctx context.Context, targetID, entityID, col string) {
	if _, err := e.marks.incrementDispatchCount(ctx, targetID, entityID, col); err != nil {
		e.logger.Warn("weaver: dispatch-count increment failed; the retry budget may under-count",
			"targetId", targetID, "entityId", entityID, "gap", col, "err", err)
	}
}

// bumpEffectDispatch records a fresh dispatch episode against the
// per-(target, gapColumn, actionRef) confidence window (§10.3 `__effect`,
// weaver-planner-mandate design §3.2) at the exact same seam bumpDispatchCount
// uses — the CAS-create-won lane-1 path and the sweep's reclaim, never a
// redelivery re-fire. A failure is logged, never propagated: the window is
// Fire 5's future ranking input, not a dispatch gate.
func (e *Engine) bumpEffectDispatch(ctx context.Context, targetID, gapColumn, actionRef string) {
	if err := e.marks.recordEffectDispatch(ctx, targetID, gapColumn, actionRef); err != nil {
		e.logger.Warn("weaver: effect dispatch record failed",
			"targetId", targetID, "gap", gapColumn, "action", actionRef, "err", err)
	}
}

// bumpOscillation records this fresh-dispatch episode's touched aspect paths
// (from actionRef's declared op-DDL `.effects`, Fire 1) against the
// oscillation detector, at the SAME two fresh-dispatch seams
// bumpEffectDispatch uses — the CAS-create-won lane-1 path and the sweep's
// reclaim, never a redelivery re-fire. A confirmed fight (two targets
// alternately dispatching against the same aspect path) freezes both via the
// existing `__control` disable seam and raises ONE Health issue naming the
// pair (design weaver-planner-mandate-design.md §3.4) — diagnostic action
// only, never a new dispatch. An actionRef with no declared effects touches
// nothing and is a no-op.
func (e *Engine) bumpOscillation(ctx context.Context, targetID, actionRef string) {
	now := time.Now()
	for _, path := range e.source.effectPathsFor(actionRef) {
		a, b, ok := e.oscillation.record(path, targetID, now)
		if !ok {
			continue
		}
		e.freezeOscillatingPair(ctx, a, b, path)
	}
}

// fire materializes one episode's op and fire-and-forget publishes it. A
// publish failure Naks: the mark already exists, so the redelivery re-derives
// the SAME requestId and re-publishes (idempotent at the Processor). The op's
// requestId is episode-scoped (markRevision) UNLESS the plan overrides it
// (pl.requestID — Fire 2b's proposal-scoped dispatch); claimId is the
// per-open-episode token the payload folds into the STABLE userTask identity
// (§10.3).
//
// pl.followUp (Fire 2b's two-op proposedOp dispatch) fires immediately after a
// successful primary publish, in the SAME call: publish order is (a) the
// primary op, (b) the followUp. A followUp publish failure does NOT Nak the
// episode — only the primary op's failure does — because the followUp is a
// dispatched-flip whose loss self-heals on the reconciler's next sweep
// (design augur-dispatch-pickup §3.4); Nak-ing here would needlessly re-fire
// the ALREADY-SUCCEEDED primary op's redelivery path for a purely cosmetic
// flip delay.
// planOptionalReads resolves a plan's optional-read closure for one episode's
// claimID (nil-safe — most plans declare none).
func planOptionalReads(pl *plan, claimID string) []string {
	if pl.optionalReads == nil {
		return nil
	}
	return pl.optionalReads(claimID)
}

func (e *Engine) fire(ctx context.Context, targetID, entityID, col string, markRevision uint64, claimID string, pl *plan) substrate.Decision {
	requestID := deriveEpisodeRequestID(targetID, entityID, col, markRevision)
	if pl.requestID != nil {
		requestID = pl.requestID(claimID)
	}
	if err := e.act.submit(ctx, requestID, pl.operationType, pl.payload(claimID), pl.authTarget, pl.reads, planOptionalReads(pl, claimID)); err != nil {
		e.logger.Error("weaver: op publish failed; nak for retry",
			"targetId", targetID, "entityId", entityID, "gap", col, "requestId", requestID, "err", err)
		return substrate.Nak
	}
	if fu := pl.followUp; fu != nil {
		fuRequestID := deriveEpisodeRequestID(targetID, entityID, col, markRevision)
		if fu.requestID != nil {
			fuRequestID = fu.requestID(claimID)
		}
		if err := e.act.submit(ctx, fuRequestID, fu.operationType, fu.payload(claimID), fu.authTarget, fu.reads, planOptionalReads(fu, claimID)); err != nil {
			e.logger.Warn("weaver: follow-up op publish failed; will retry on next reconcile",
				"targetId", targetID, "entityId", entityID, "gap", col, "requestId", fuRequestID, "err", err)
		}
	}
	return substrate.Ack
}

// clearClosedMarks is the level-reconciled mark-clearing pass. Returns false
// when a delete failed (the caller Naks with delay so the reconcile re-runs
// without hot-looping). A nil row (entity deleted) clears every candidate.
//
// Closing a gap also DELETES its retry-budget dispatch-count (§E mechanism B): a
// success closes the gap, so the chain's attempt accounting resets and a later
// reopen of the same gap starts a fresh budget. This is the reset the budget
// exists for — the lens predicate cannot express "failures since the last
// success," so the gap-close path here owns it. The count delete shares the
// gap's not-currently-true condition with the mark delete, so it runs in exactly
// the same cases (gap closed, column dropped, or entity deleted).
//
// A gap actually being cleared here (a mark existed) is also a CLOSE event for
// the §10.3 `__effect` confidence window (design §3.2): the mark's Action names
// which actionRef to record the close against. Read BEFORE delete (the delete
// itself carries no value to recover the action from); a read failure logs and
// skips the effect record — the window is a future ranking input, never a gate,
// so it must never block the mark/count clear it rides alongside.
func (e *Engine) clearClosedMarks(ctx context.Context, target *Target, targetID, entityID string, row map[string]any) bool {
	ok := true
	for _, col := range markCandidateColumns(target, row) {
		if row != nil && e.boolColumn(targetID, row, col) {
			continue
		}
		if ga, isSurface := target.Gaps[col]; isSurface && ga.Action == actionSurface {
			// A surface gap never creates a mark (dispatchGap returns before
			// e.marks.get) — clear its issue directly and skip the mark/
			// dispatch-count/effect-close bookkeeping below, which has nothing
			// to clear for this column.
			e.issues.clear(issueKeyGap(targetID, col))
			continue
		}
		rec, _, found, gErr := e.marks.get(ctx, targetID, entityID, col)
		if gErr != nil {
			e.logger.Warn("weaver: mark read before clear failed; effect close not recorded",
				"targetId", targetID, "entityId", entityID, "gap", col, "err", gErr)
		}
		if err := e.marks.delete(ctx, targetID, entityID, col); err != nil {
			e.logger.Error("weaver: mark clear failed",
				"targetId", targetID, "entityId", entityID, "gap", col, "err", err)
			ok = false
		}
		if err := e.marks.deleteDispatchCount(ctx, targetID, entityID, col); err != nil {
			e.logger.Error("weaver: dispatch-count reset failed",
				"targetId", targetID, "entityId", entityID, "gap", col, "err", err)
			ok = false
		}
		if gErr == nil && found {
			if cErr := e.marks.recordEffectClose(ctx, targetID, col, rec.Action); cErr != nil {
				e.logger.Warn("weaver: effect close record failed",
					"targetId", targetID, "entityId", entityID, "gap", col, "err", cErr)
			}
		}
	}
	return ok
}

// releaseCompletedLeg reports whether gap col's currently-pinned goal-mode
// leg (Fire 6, R1) has its declared Effects all holding against the current
// row — the leg is DONE — and if so releases it: clears the mark, resets the
// gap's per-chain dispatch-count, and credits the just-finished leg's
// `__effect` close, mirroring clearClosedMarks' gap-close bookkeeping but
// scoped to one LEG rather than the whole gap. A no-op (false) for every
// non-goal gap, a fresh episode (pinnedAction==""), or a pin whose ref the
// catalog no longer names (planGap's unplannable retry owns that case).
//
// A release is a LEG boundary, not a gap boundary: the gap's own missing_<g>
// column may well still be true (more legs remain), so the caller must
// re-evaluate as a genuinely fresh episode (pinnedAction="") immediately
// after a true return — the next resolveGoalAction call synthesizes the NEXT
// leg from the now-advanced row state, per the design's "replanning happens
// only at leg boundaries (effects-hold) and gap boundaries (close→reopen)."
// markRev is the revision the caller read the mark at (dispatchGap's
// up-front read, or the sweep's own read this pass) — the delete is
// revision-conditioned on it so a mark that changed underneath (a concurrent
// path already released/advanced this SAME leg) is left alone rather than
// blindly cleared, mirroring the revision-conditioning every other
// sweep-path mark mutation (replace, deleteMark) already applies.
func (e *Engine) releaseCompletedLeg(ctx context.Context, targetID, entityID, col string, ga GapAction, pinnedAction string, row map[string]any, markRev uint64) bool {
	if ga.Goal == nil || pinnedAction == "" {
		return false
	}
	var entry *ActionCatalogEntry
	for i := range ga.Actions {
		if ga.Actions[i].Ref == pinnedAction {
			entry = &ga.Actions[i]
			break
		}
	}
	if entry == nil {
		return false
	}
	state := rowState(row, ga.goalColumnPaths)
	for _, g := range entry.effectGuards {
		if !planner.EvalGuard(g, state) {
			return false
		}
	}
	conflict, err := e.marks.deleteRevision(ctx, targetID, entityID, col, markRev)
	if err != nil {
		e.logger.Error("weaver: goal leg release mark clear failed",
			"targetId", targetID, "entityId", entityID, "gap", col, "action", pinnedAction, "err", err)
		return false
	}
	if conflict {
		// The mark changed since the caller's read — a concurrent path
		// already released or is otherwise handling this episode. Not this
		// caller's release to claim.
		return false
	}
	if err := e.marks.deleteDispatchCount(ctx, targetID, entityID, col); err != nil {
		e.logger.Warn("weaver: goal leg release dispatch-count reset failed",
			"targetId", targetID, "entityId", entityID, "gap", col, "err", err)
	}
	if err := e.marks.recordEffectClose(ctx, targetID, col, pinnedAction); err != nil {
		e.logger.Warn("weaver: goal leg release effect-close record failed",
			"targetId", targetID, "entityId", entityID, "gap", col, "action", pinnedAction, "err", err)
	}
	e.logger.Info("weaver: goal leg released; re-planning from the advanced state",
		"targetId", targetID, "entityId", entityID, "gap", col, "action", pinnedAction)
	return true
}

// boolColumn reads a §10.2 bool column off a row. A present value of any other
// type is a Lens data error: surfaced (Warn log + Health KV issue) and treated
// conservatively as not actionable — never silently inverted into a clean
// false.
func (e *Engine) boolColumn(targetID string, row map[string]any, col string) bool {
	v, ok := row[col]
	if !ok || v == nil {
		return false
	}
	b, isBool := v.(bool)
	if !isBool {
		msg := fmt.Sprintf("target %s: row column %q is %T, not the §10.2 bool; treated as not actionable", targetID, col, v)
		e.logger.Warn("weaver: " + msg)
		e.issues.set(issueKeyData(targetID, col), "warning", "RowDataError", msg)
	}
	return b
}

// intColumn reads a §10.2 integer column off a row, returning ok=false when the
// column is absent or carries a non-numeric value (the latter a Lens data error:
// surfaced as a RowDataError, like boolColumn's non-bool path). JSON-decoded rows
// carry numbers as float64; directly-constructed rows (unit tests) may carry int
// or int64 — all coerce. A fractional float is floored to its integer part (a cap
// is a whole count by construction). The bool form of a present-but-wrong-type
// value is the caller's "no usable value" signal (ok=false), never a silent 0.
func (e *Engine) intColumn(targetID string, row map[string]any, col string) (int, bool) {
	v, ok := row[col]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		msg := fmt.Sprintf("target %s: row column %q is %T, not the §10.2 integer; treated as absent", targetID, col, v)
		e.logger.Warn("weaver: " + msg)
		e.issues.set(issueKeyData(targetID, col), "warning", "RowDataError", msg)
		return 0, false
	}
}

// gapSuppressed reports whether gap column gapCol (a missing_<g>) must NOT be
// (re-)dispatched, and — when suppressed — WHY: its inflight_<g> companion is
// true (a remediation is legitimately in flight, `exhausted` false) OR its
// weaver-state dispatch-count has reached the row's maxretries_<g> cap (the
// retry budget is spent — §E mechanism B, `exhausted` true). It is the dispatch
// gate read by BOTH dispatch legs — the lane-1 loop and the sweep's reclaim —
// so a suppressed gap is neither freshly dispatched nor reclaimed while it
// stays violating. The two suppression reasons are NOT interchangeable for the
// caller: inflight is ordinary in-progress state that must always be left
// alone, while exhausted is a decision point (Contract #10 §10.8 Planner
// extension: "budget exhaustion... raises a standing Health issue at the
// suppression site, never a silent park") — callers branch on `exhausted` to
// invoke escalateExhaustedGap rather than silently skipping.
//
// inflight is authoritative and read first (a true inflight short-circuits the
// KV read, and is never `exhausted`). An absent/non-bool inflight reads false
// via boolColumn (which surfaces a non-bool as a RowDataError); an
// absent/garbled maxretries reads 0 via intColumn, and a count-read failure
// logs and is treated as NOT-suppressing on the cap term — the safe side in
// every case is to dispatch, so a missing/garbled companion or a transient KV
// error never silently wedges a real gap. A gapCol without the missing_ prefix
// has no companions, so it is never suppressed.
func (e *Engine) gapSuppressed(ctx context.Context, targetID, entityID string, row map[string]any, gapCol string) (suppressed, exhausted bool) {
	g, ok := strings.CutPrefix(gapCol, gapColumnPrefix)
	if !ok {
		return false, false
	}
	if e.boolColumn(targetID, row, inflightColumnPrefix+g) {
		return true, false
	}
	capN, ok := e.intColumn(targetID, row, maxretriesColumnPrefix+g)
	if !ok || capN <= 0 {
		// No usable cap on the row → the budget term cannot suppress (only
		// inflight, already checked, can). A non-positive cap means "no budget
		// configured for this gap" — never auto-suppress on it.
		return false, false
	}
	count, err := e.marks.getDispatchCount(ctx, targetID, entityID, gapCol)
	if err != nil {
		// A transient count read failure must not silently wedge the gap: leave
		// inflight authoritative and let the gap dispatch (the safe side). The next
		// evaluation re-reads the count.
		e.logger.Warn("weaver: dispatch-count read failed; not suppressing on the cap term",
			"targetId", targetID, "entityId", entityID, "gap", gapCol, "err", err)
		return false, false
	}
	if count >= capN {
		return true, true
	}
	return false, false
}

// escalateExhaustedGap redirects a gap whose retry budget is spent
// (weaver-state dispatch-count reached maxretries_<g>) to the Augur AI-
// reasoning tier when the target's augur block opts "exhausted" into its
// escalate list (Contract #10 §10.8 Augur escalation) — the generalization of
// augurEscalation's existing "unplannable" redirect used by dispatchGap/
// planGap: "no playbook entry" and "no more playbook attempts left" are both
// dead ends for conventional remediation. When no augur policy escalates
// "exhausted", it raises the Planner extension's promised standing Health
// issue instead of silently parking the gap (§10.8: "Budget exhaustion on a
// planned gap raises a standing Health issue at the suppression site, never a
// silent park" — applied here to every gap class, frozen-table or planned,
// since an unescalated cap is the identical silent-park failure mode either
// way).
//
// Fires as a genuinely FRESH episode (planGap with no pinned action,
// fireEpisode's found=false branch) — never through the gap's OWN mark: an
// exhausted gap's mark (if one survives) belongs to the ORIGINAL action's
// retry lineage, while the escalation is a different action entirely, keyed
// under its own deterministic instanceKey (deriveAugurHandle) inside
// CreateAugurReasoningClaim's own anti-storm mark. Callable from both lane-1
// (handleRow) and the sweep (reclaim) — the shared suppression site both use.
func (e *Engine) escalateExhaustedGap(ctx context.Context, target *Target, targetID, entityID, entityKey, gapColumn string,
	row map[string]any, rowRevision uint64) substrate.Decision {

	esc, escalated := augurEscalation(e.source, target, escalateExhausted, targetID, entityID, entityKey, gapColumn)
	if !escalated {
		e.alert(issueKeyGap(targetID, gapColumn), "warning", "GapBudgetExhausted",
			"target "+targetID+": row column "+gapColumn+" has exhausted its retry budget with no augur escalation configured for \"exhausted\"")
		return substrate.Ack
	}
	e.issues.clear(issueKeyGap(targetID, gapColumn))

	// The exhausted gap's OWN mark, if one survives, occupies the SAME
	// <targetId>.<entityId>.<gapColumn> key the escalation's fresh CAS-create
	// needs. Two distinct cases, told apart the same way dispatchGap already
	// does (leaseLive), because the escalation is invisible to the LENS's
	// inflight_<g> companion (a different action class than the gap's normal
	// remediation, so the row never reflects that an escalation is running):
	//
	//   - A LIVE mark means the escalation this function fired last time is
	//     still genuinely in flight (its lease has not expired) — leave it
	//     alone, exactly like the ordinary inflight case, or every
	//     subsequent redelivery of this still-open gap would tear down and
	//     re-fire a brand-new escalation episode on top of one already
	//     running (a self-inflicted storm this function must not cause).
	//   - A STALE mark (expired lease) or none at all belongs to the
	//     original action's now-spent retry lineage (or a prior escalation
	//     attempt that never completed) — clear it, revision-conditioned on
	//     the read just taken so a genuinely concurrent fresh episode is
	//     never clobbered, then fire fresh.
	rec, markRev, found, err := e.marks.get(ctx, targetID, entityID, gapColumn)
	if err != nil {
		e.logger.Warn("weaver: mark read failed ahead of exhausted-gap escalation; nak with delay",
			"targetId", targetID, "entityId", entityID, "gap", gapColumn, "err", err)
		return substrate.NakWithDelay
	}
	if found && leaseLive(rec.LeaseExpiresAt, time.Now()) {
		return substrate.Ack
	}
	if found {
		if conflict, derr := e.marks.deleteRevision(ctx, targetID, entityID, gapColumn, markRev); derr != nil {
			e.logger.Warn("weaver: clearing the exhausted gap's own mark failed ahead of escalation; will retry",
				"targetId", targetID, "entityId", entityID, "gap", gapColumn, "err", derr)
			return substrate.NakWithDelay
		} else if conflict {
			// The mark changed under us (revision mismatch) since the read
			// above — a concurrent fresh episode owns the gap now; leave it.
			return substrate.Ack
		}
	}

	pl, actionRef, dec := e.planGap(ctx, target, targetID, entityID, gapColumn, esc, row, rowRevision, "")
	if pl == nil {
		return dec
	}
	return e.fireEpisode(ctx, targetID, entityID, entityKey, gapColumn, actionRef, pl, false, nil, 0, false, false)
}

// markCandidateColumns is the union of the playbook's gaps keys and the row's
// missing_* columns — every column a mark could exist at — in deterministic
// order.
func markCandidateColumns(target *Target, row map[string]any) []string {
	set := make(map[string]struct{}, len(target.Gaps))
	for col := range target.Gaps {
		set[col] = struct{}{}
	}
	for col := range row {
		if strings.HasPrefix(col, gapColumnPrefix) {
			set[col] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for col := range set {
		out = append(out, col)
	}
	sort.Strings(out)
	return out
}

// openGapColumns returns the row's missing_* columns whose value is true, in
// deterministic order. Gaps fire in parallel-safe sequence (independent
// marks); gap dependencies are the Lens's problem, not Weaver's (§10.8). A
// non-bool column value is surfaced and reads as not-open (boolColumn).
func (e *Engine) openGapColumns(targetID string, row map[string]any) []string {
	var out []string
	for col := range row {
		if !strings.HasPrefix(col, gapColumnPrefix) {
			continue
		}
		if e.boolColumn(targetID, row, col) {
			out = append(out, col)
		}
	}
	sort.Strings(out)
	return out
}

// splitRowKey splits a weaver-targets key <targetId>.<entityId> (§10.2: the
// entity segment is the bare NanoID, so exactly one dot separates the
// segments).
func splitRowKey(key string) (targetID, entityID string, ok bool) {
	i := strings.IndexByte(key, '.')
	if i <= 0 {
		return "", "", false
	}
	targetID, entityID = key[:i], key[i+1:]
	if !substrate.IsValidNanoID(entityID) {
		return "", "", false
	}
	return targetID, entityID, true
}

// alert records a Health KV issue and logs it at Error — the FR29 loud-failure
// pair.
func (e *Engine) alert(key, severity, code, message string) {
	e.logger.Error("weaver: " + message)
	e.issues.set(key, severity, code, message)
}

func issueKeyGap(targetID, col string) string  { return "gap:" + targetID + "." + col }
func issueKeyData(targetID, col string) string { return "data:" + targetID + "." + col }
func issueKeyEffect(targetID, gapColumn, actionRef string) string {
	return "effect:" + targetID + "." + gapColumn + "." + actionRef
}
