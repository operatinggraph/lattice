package weaver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/weaver/nudge"
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
	// (Strategist/Actuator: triggerLoom/nudge/assignTask/directOp) runs for
	// this row. On enable, remediation resumes for whatever is still violating.
	if e.isTargetDisabled(targetID) {
		return substrate.Ack
	}

	if !e.boolColumn(targetID, row, "violating") {
		// L1: not violating — clearing already ran; nothing to dispatch.
		return substrate.Ack
	}

	entityKey, _ := row["entityKey"].(string)
	if entityKey == "" {
		// §10.2 requires the entityKey echo; without it the mark and the
		// remediation cannot name the candidate. Data error — surface, do not
		// fire (redelivery cannot fix the projected row).
		e.alert(issueKeyData(targetID, "entityKey"), "error", "RowDataError",
			"weaver-targets row "+key+" is violating but carries no entityKey")
		return substrate.Ack
	}

	nak := false
	delayed := false
	for _, col := range e.openGapColumns(targetID, row) {
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
		// A true missing_* column with no playbook entry is a config error →
		// alert, never silently skipped (FR29 discipline).
		e.alert(issueKeyGap(targetID, col), "error", "GapWithoutPlaybook",
			"target "+targetID+": row column "+col+" is true but the playbook defines no gaps entry for it")
		return substrate.Ack
	}

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
	pl, dec := e.planGap(targetID, entityID, col, ga, row, msg.Sequence)
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
	return e.fireEpisode(ctx, targetID, entityID, entityKey, col, ga.Action, pl, msg.Sequence, msg.NumDelivered != 1)
}

// planGap resolves one gap's plan (Evaluator L2 + Strategist), routing a
// failure by its class: an unresolved reference defers on the bounded
// redelivery cadence; a config/data error is alerted and the gap skipped
// (retrying cannot fix it). pl == nil means do not dispatch — the returned
// Decision is the caller's disposition for this gap.
func (e *Engine) planGap(targetID, entityID, col string, ga GapAction, row map[string]any,
	rowRevision uint64) (*plan, substrate.Decision) {

	pl, perr := buildPlan(e.source, targetID, entityID, col, ga, row, rowRevision)
	if perr != nil {
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
			return nil, substrate.NakWithDelay
		case errData:
			e.alert(issueKeyData(targetID, col), "error", "TemplateDataError",
				"target "+targetID+" gap "+col+": "+perr.msg)
			return nil, substrate.Ack
		default:
			e.alert(issueKeyGap(targetID, col), "error", "PlaybookConfigError",
				"target "+targetID+" gap "+col+": "+perr.msg)
			return nil, substrate.Ack
		}
	}
	e.issues.clear(issueKeyGap(targetID, col))
	e.issues.clear(issueKeyData(targetID, col))
	return pl, substrate.Ack
}

// fireEpisode is the lane-1 dispatch core: resolve the in-flight mark,
// CAS-create on absence (the dispatch OCC), and fire the episode op.
// redelivered selects the in-flight disposition — false drops (the anti-storm
// gate: another episode is in flight), true re-publishes the SAME episode
// requestId (idempotent at the Contract #4 tracker). The reconciler sweep
// does not pass through here: its reclaim replaces the expired mark in place
// under a revision condition and fires directly.
//
// The nudge action diverges: its mark is CAS-created with createNudge (minting
// the claimId atomically, §10.3) and dispatch runs the Two-Phase Nudge protocol
// over that claimId rather than a plain ops.<lane> submit. A redelivery over a
// live nudge mark routes to Recover (read-before-act, reusing the mark's
// claimId) — never Run, which would land ErrClaimExists on the existing claim.
// rowRevision is the §10.2 row's OCC revision-condition, carried into the resolve
// op payload; it is unused by the non-nudge plain-submit path.
func (e *Engine) fireEpisode(ctx context.Context, targetID, entityID, entityKey, col, action string,
	pl *plan, rowRevision uint64, redelivered bool) substrate.Decision {

	rec, markRev, inFlight, err := e.marks.get(ctx, targetID, entityID, col)
	if err != nil {
		e.logger.Error("weaver: mark read failed; nak with delay", "targetId", targetID, "entityId", entityID, "gap", col, "err", err)
		return substrate.NakWithDelay
	}
	if inFlight {
		if !redelivered {
			// A fresh delivery while the episode is in flight — the anti-storm
			// drop.
			return substrate.Ack
		}
		if action == actionNudge {
			// A redelivery over a live nudge mark: the claim already exists, so a
			// fresh Run would land ErrClaimExists. Recover reuses the mark's
			// claimId read-before-act (probe Core KV for a landed resolve before
			// re-executing on the same idempotencyKey).
			return e.recoverNudge(ctx, targetID, entityID, col, rec.ClaimID, pl, rowRevision)
		}
		// Redelivery retry path: re-publish the same episode.
		return e.fire(ctx, targetID, entityID, col, markRev, pl)
	}

	if action == actionNudge {
		claimID, _, lost, err := e.marks.createNudge(ctx, targetID, entityID, col, entityKey, action)
		if err != nil {
			e.logger.Error("weaver: nudge mark create failed; nak with delay",
				"targetId", targetID, "entityId", entityID, "gap", col, "err", err)
			return substrate.NakWithDelay
		}
		if lost {
			// A concurrent evaluation won the CAS — the winner dispatched.
			return substrate.Ack
		}
		return e.fireNudge(ctx, targetID, entityID, col, claimID, pl, rowRevision)
	}

	rev, lost, err := e.marks.create(ctx, targetID, entityID, col, entityKey, action)
	if err != nil {
		e.logger.Error("weaver: mark create failed; nak with delay",
			"targetId", targetID, "entityId", entityID, "gap", col, "err", err)
		return substrate.NakWithDelay
	}
	if lost {
		// A concurrent evaluation won the CAS — the winner dispatched.
		return substrate.Ack
	}
	return e.fire(ctx, targetID, entityID, col, rev, pl)
}

// fire materializes one episode's op and fire-and-forget publishes it. A
// publish failure Naks: the mark already exists, so the redelivery re-derives
// the SAME requestId and re-publishes (idempotent at the Processor).
func (e *Engine) fire(ctx context.Context, targetID, entityID, col string, markRevision uint64, pl *plan) substrate.Decision {
	requestID := deriveEpisodeRequestID(targetID, entityID, col, markRevision)
	if err := e.act.submit(ctx, requestID, pl.operationType, pl.payload(markRevision), pl.authTarget, pl.reads); err != nil {
		e.logger.Error("weaver: op publish failed; nak for retry",
			"targetId", targetID, "entityId", entityID, "gap", col, "requestId", requestID, "err", err)
		return substrate.Nak
	}
	return substrate.Ack
}

// nudgeDispatch builds the nudge.Dispatch for one episode from the resolved
// nudge plan + the minted/carried claimId. IdempotencyKey is the claimId
// (nudge.Dispatch enforces it).
func nudgeDispatch(claimID string, np *nudgePlan) nudge.Dispatch {
	return nudge.Dispatch{
		ClaimID:   claimID,
		Adapter:   np.adapter,
		Operation: np.operation,
		Subject:   np.subject,
		Params:    np.params,
	}
}

// fireNudge runs the Two-Phase Nudge protocol for a FRESH nudge episode (the
// claimId was just minted with the mark, §10.3): Nudger.Run writes the claim
// (state=claimed, NFR-S11 visible-intent-before-execute), advances to executing,
// calls the adapter on idempotencyKey=claimId, then submits the resolve op and
// records resolved. The outcome maps to a Decision following the §10.3 /
// fire-and-forget posture:
//
//   - success (claim resolved) → Ack;
//   - adapter hard-fail (claim left failed) → Ack + Health issue: the claim is
//     durable and re-drivable, and the reconciler sweep re-attempts at lease
//     expiry — a Nak would hot-loop lane-1 against a deterministically failing
//     adapter (the §10.3 model is lease-bounded re-attempt, not a redelivery
//     storm);
//   - a missing adapter (config error) → Ack + Health issue (errConfig posture:
//     redelivery cannot fix a name the registry does not know);
//   - any other failure (resolve-submit failure leaving the claim executing, or
//     an infra error) → Nak so the redelivery re-drives via Recover before the
//     sweep would.
func (e *Engine) fireNudge(ctx context.Context, targetID, entityID, col, claimID string,
	pl *plan, rowRevision uint64) substrate.Decision {

	claim, err := e.nudger.Run(ctx, nudgeDispatch(claimID, pl.nudge), e.resolveFunc(pl.nudge, rowRevision))
	return e.nudgeDecision(targetID, entityID, col, claimID, claim, err)
}

// recoverNudge re-drives a nudge episode whose claim already exists (a lane-1
// redelivery over a live mark, or the reconciler reclaim) via Nudger.Recover:
// read-before-act on the SAME claimId (probe Core KV for an already-landed
// resolve via resolveProbe; if landed, advance to resolved with no second
// side-effect; else re-execute on the same idempotencyKey — the adapter dedups).
// claimID MUST be the existing mark's claimId (§10.3 carries it forward); a blank
// one is a corrupt mark, surfaced and refused by Recover rather than re-minted.
// Outcome maps to a Decision like fireNudge.
func (e *Engine) recoverNudge(ctx context.Context, targetID, entityID, col, claimID string,
	pl *plan, rowRevision uint64) substrate.Decision {

	if claimID == "" {
		// A live nudge mark with no claimId is corrupt (§10.3 impossible-by-
		// construction). Recover would refuse it; surface here and leave the
		// reconciler's corrupt-claim guard to delete+alert the mark.
		e.alert(issueKeyData(targetID, col), "error", "CorruptNudgeClaim",
			"target "+targetID+" gap "+col+": live nudge mark carries an empty claimId")
		return substrate.Ack
	}
	e.checkClaimWedge(ctx, targetID, col, claimID)
	claim, err := e.nudger.Recover(ctx, nudgeDispatch(claimID, pl.nudge), e.resolveProbe(), e.resolveFunc(pl.nudge, rowRevision))
	return e.nudgeDecision(targetID, entityID, col, claimID, claim, err)
}

// checkClaimWedge surfaces the executing-wedge Health signal on EVERY recovery
// (sweep reclaim and lane-1 live redelivery both route through recoverNudge), on
// its OWN issue key (issueKeyNudgeWedge) so nudgeDecision's clear/raise on
// issueKeyNudge cannot clobber it. A claim still pre-resolved past the Contract #4
// idempotency horizon (claimWedgeBound) can no longer trust the adapter's dedup
// window (or the resolve tracker) to suppress a duplicate on re-execute: the gap
// keeps converging (the fresh lease bounds re-attempts) but the lapsed guarantee
// must be operator-visible, not silent. An unparseable claimedAt is itself a
// corrupt claim — surfaced rather than skipped (a skip would go dark on the only
// signal for a lapsed dedup guarantee). A missing/resolved claim, or one still
// inside the horizon, clears any standing wedge issue.
func (e *Engine) checkClaimWedge(ctx context.Context, targetID, col, claimID string) {
	claim, found, err := e.claims.Get(ctx, claimID)
	if err != nil {
		// A transient claim-read failure is not evidence of a wedge; leave any
		// standing issue and let the next recovery re-evaluate.
		return
	}
	if !found || claim.State == nudge.StateResolved {
		e.issues.clear(issueKeyNudgeWedge(targetID, col))
		return
	}
	claimedAt, perr := time.Parse(time.RFC3339Nano, claim.ClaimedAt)
	if perr != nil {
		e.alert(issueKeyNudgeWedge(targetID, col), "warning", "NudgeClaimCorrupt",
			"target "+targetID+" gap "+col+" claim "+claimID+
				": claimedAt "+claim.ClaimedAt+" is unparseable; the executing-wedge dedup-horizon check cannot run: "+perr.Error())
		return
	}
	if time.Since(claimedAt) > claimWedgeBound {
		e.issues.set(issueKeyNudgeWedge(targetID, col), "warning", "NudgeClaimWedged",
			"target "+targetID+" gap "+col+" claim "+claimID+
				": claim has been unresolved (state "+string(claim.State)+") past the "+
				claimWedgeBound.String()+" idempotency horizon; re-execute can no longer be guaranteed duplicate-free")
		return
	}
	e.issues.clear(issueKeyNudgeWedge(targetID, col))
}

// nudgeDecision maps a Nudger.Run/Recover outcome to a lane-1 Decision and
// surfaces/clears Health. A nil error clears any standing nudge issue and Acks.
// A missing adapter is a config error → Ack + Health (errConfig posture:
// redelivery can never fix a name the registry does not hold, so a Nak would
// hot-loop lane-1). A claim left in state=failed is an adapter hard-fail → Ack +
// Health (the sweep re-attempts on the lease, no hot loop). Anything else (a
// resolve-submit failure leaving the claim executing, or an infra error) → Nak
// for redelivery, with Health raised so the condition is visible.
func (e *Engine) nudgeDecision(targetID, entityID, col, claimID string, claim *nudge.Claim, err error) substrate.Decision {
	if err == nil {
		e.issues.clear(issueKeyNudge(targetID, col))
		return substrate.Ack
	}
	if errors.Is(err, nudge.ErrAdapterNotFound) {
		// The nudge action names an adapter the registry does not hold — a config
		// error. Redelivery can never fix it, so Ack (no hot loop) and surface to
		// Health, mirroring the planGap errConfig posture.
		e.alert(issueKeyNudge(targetID, col), "error", "NudgeAdapterMissing",
			"target "+targetID+" gap "+col+" claim "+claimID+": "+err.Error())
		return substrate.Ack
	}
	if claim != nil && claim.State == nudge.StateFailed {
		e.alert(issueKeyNudge(targetID, col), "warning", "NudgeAdapterFailed",
			"target "+targetID+" gap "+col+" claim "+claimID+": adapter failed; the reconciler re-attempts at lease expiry: "+err.Error())
		return substrate.Ack
	}
	if errors.Is(err, nudge.ErrClaimExists) {
		// The fresh-Run path lost to an existing claim (a redelivery raced the
		// create). The live claim's owner is converging it; drop this delivery —
		// re-running would be the duplicate the create-semantics guard against.
		e.logger.Debug("weaver: nudge claim already exists; dropping racing fresh dispatch",
			"targetId", targetID, "entityId", entityID, "gap", col, "claimId", claimID)
		return substrate.Ack
	}
	e.alert(issueKeyNudge(targetID, col), "warning", "NudgeDispatchError",
		"target "+targetID+" gap "+col+" claim "+claimID+": nudge dispatch did not resolve; redelivery re-drives via recovery: "+err.Error())
	return substrate.Nak
}

// clearClosedMarks is the level-reconciled mark-clearing pass. Returns false
// when a delete failed (the caller Naks with delay so the reconcile re-runs
// without hot-looping). A nil row (entity deleted) clears every candidate.
func (e *Engine) clearClosedMarks(ctx context.Context, target *Target, targetID, entityID string, row map[string]any) bool {
	ok := true
	for _, col := range markCandidateColumns(target, row) {
		if row != nil && e.boolColumn(targetID, row, col) {
			continue
		}
		if err := e.marks.delete(ctx, targetID, entityID, col); err != nil {
			e.logger.Error("weaver: mark clear failed",
				"targetId", targetID, "entityId", entityID, "gap", col, "err", err)
			ok = false
		}
	}
	return ok
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

func issueKeyGap(targetID, col string) string   { return "gap:" + targetID + "." + col }
func issueKeyData(targetID, col string) string  { return "data:" + targetID + "." + col }
func issueKeyNudge(targetID, col string) string { return "nudge:" + targetID + "." + col }

// issueKeyNudgeWedge keys the executing-wedge / corrupt-claimedAt Health issue on
// its OWN namespace, distinct from issueKeyNudge: nudgeDecision clears and raises
// issueKeyNudge on every recovery, so the wedge alert (which must persist for the
// operator across recoveries) cannot share that key or it would be clobbered.
func issueKeyNudgeWedge(targetID, col string) string { return "nudge-wedge:" + targetID + "." + col }
