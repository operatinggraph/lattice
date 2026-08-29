package weaver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/weaver/planner"
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
			// A per-row DATA error, and one whose fix can only ever arrive as a
			// re-projection — which delivers on its own (per-subject compaction
			// supersedes any pending state), so redelivery buys no retry value
			// here and the row Acks. What the Ack must not do is drop the fact
			// silently: the standing issue is the audibility, raised from the one
			// read that decides it and retired by the same read below.
			unreadable := "weaver-targets row " + key + " carries a body that is not readable JSON: " + err.Error()
			e.logger.Warn("weaver: " + unreadable)
			e.issues.set(issueKeyDataEntity(targetID, entityID, rowBodyColumn), "warning", "RowDataError", unreadable)
			return substrate.Ack
		}
		// A body that parses IS the repair, so the read that decides the fact
		// also retires it — the raise/clear pair the entityKey echo below uses.
		// The clear is load-bearing rather than belt-and-braces: the failure arm
		// returns above clearClosedMarks, so the `data:` family prefix clear that
		// retires a tombstoned entity's entries cannot run on the delivery that
		// raised this one, only on a later one.
		e.issues.clear(issueKeyDataEntity(targetID, entityID, rowBodyColumn))
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
	violating := false
	if row != nil {
		violating, _ = e.boolColumn(targetID, entityID, row, "violating")
	}
	e.contraction.observe(targetID, entityID, violating)

	// Anchor bookkeeping, decided on every delivery that reaches here — a row key
	// that parses, a registered target, a body that parses, mark-clearing that
	// succeeded — alongside the contraction monitor above. The fact is "this row
	// is violating and its body carries no entityKey echo (§10.2)"; not violating
	// or echo present is the repair, so the same read that raises the entry also
	// retires it and the next projection of a fixed row clears the level signal.
	// Runs for a disabled target too: like the two steps above it is
	// violation-detection bookkeeping, and gating it on enablement would strand
	// the entry of a row whose target is disabled after the raise. A tombstone
	// takes the clear branch on the nil map's empty-string index, behind the
	// data-family prefix clear clearClosedMarks already ran for it.
	// The log line and the Health entry are one loud-failure pair, raised
	// together from the one read that decides the fact, so every delivery that
	// surfaces the entry also names the row in the log — a disabled target
	// included.
	entityKey, _ := row["entityKey"].(string)
	if violating && entityKey == "" {
		anchorless := "weaver-targets row " + key + " is violating but carries no entityKey"
		e.logger.Warn("weaver: " + anchorless)
		e.issues.set(issueKeyDataEntity(targetID, entityID, "entityKey"), "warning", "RowDataError", anchorless)
	} else {
		e.issues.clear(issueKeyDataEntity(targetID, entityID, "entityKey"))
	}

	if row == nil {
		return substrate.Ack
	}

	// Lane-3 scheduling leg: a row carrying a freshUntil (re-)arms its
	// per-target-per-entity @at timer on EVERY delivery, violating or not —
	// level-driven, idempotent under one-schedule-per-subject replace. A future
	// instant arms a pending timer; a past instant is published verbatim and
	// fires immediately (overdue deadline). Runs even for a disabled target:
	// arming the timer is state-recording bookkeeping, so an instant re-enable
	// loses no deadline. Only a schedule-publish failure defers the row.
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

	if entityKey == "" {
		// §10.2 requires the entityKey echo; without it the mark and the
		// remediation cannot name the candidate. A single malformed anchor row is
		// a per-row DATA error: skip the row (the bookkeeping above holds both
		// halves of the signal) but keep remediating every other row — Weaver
		// still fulfils its primary responsibility, so this is a Contract #5 §5.2
		// `warning` (degraded), never an `error` (unhealthy = cannot fulfil the
		// responsibility). Redelivery cannot fix the projected row.
		return substrate.Ack
	}

	nak := false
	delayed := false
	longDelayed := false
	for _, col := range e.openGapColumns(targetID, entityID, row) {
		// The static playbook entry for this gap (zero value when the column has
		// no gaps entry at all — the "no playbook entry" dead-end dispatchGap
		// below handles separately): gapSuppressed's cap-fallback term only ever
		// engages for a literal "directOp" action.
		ga := target.Gaps[col]
		// A surface gap has nothing to suppress: it dispatches no op, mints no
		// mark and advances no dispatch-count, so neither companion column can
		// describe it (surfaceOnlyGap). A package upgrade that rewrote this
		// column to `surface` nevertheless leaves the previous action's
		// dispatch-count behind, and the cap term reads maxretries_<g> without
		// consulting the action — so that stranded count reads as spent, takes
		// the branch below, and skips the column entirely. The gap's own Surface
		// issue, raised by dispatchGap, would then stop being raised for as long
		// as the count outlived the migration: the diagnostic the column exists
		// to produce, silently switched off, with nothing on Health to say so.
		// Fall straight through to the dispatch leg, which is where `surface` is
		// actually handled.
		suppressed, exhausted, budgetIsDefault := false, false, false
		if !surfaceOnlyGap(ga) {
			suppressed, exhausted, budgetIsDefault = e.gapSuppressed(ctx, targetID, entityID, row, col, ga.Action)
		}
		if suppressed {
			// A remediation is in flight (inflight_<g>): ordinary in-progress
			// state, never escalated — the gap stays violating but must NOT be
			// (re-)dispatched. Skip it — mark-clearing already ran above, so a
			// stale mark does not linger.
			//
			// The retry budget is spent (a declared maxretries_<g>, or the
			// engine's defaultDirectOpRetryBudget when none is declared): a
			// decision point, not a park (Contract #10 §10.8 Planner extension:
			// "budget exhaustion... raises a standing Health issue at the
			// suppression site, never a silent park") — escalateExhaustedGap
			// redirects to the Augur AI tier if the target opts "exhausted"
			// into its augur block, else raises that standing issue itself.
			if exhausted {
				switch e.escalateExhaustedGap(ctx, target, targetID, entityID, entityKey, col, row, msg.Sequence, budgetIsDefault) {
				case substrate.Nak:
					nak = true
				case substrate.NakWithDelay:
					delayed = true
				case substrate.NakWithLongDelay:
					longDelayed = true
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
		case substrate.NakWithLongDelay:
			longDelayed = true
		default:
		}
	}
	// One message carries the whole row, so the row's gaps have to agree on a
	// single disposition, and the aggregate is the SHORTEST retry any gap asked
	// for: Nak > NakWithDelay > NakWithLongDelay > Ack. A gap that wants prompt
	// re-evaluation must not have to wait out another gap's config-error floor,
	// while a gap that only wants the long floor is re-evaluated by the shorter
	// redelivery anyway — idempotently, like every other redelivery. The
	// precedence is decided here rather than by assignment order so no accumulator
	// can overwrite another's answer.
	switch {
	case nak:
		// At least one gap needs an immediate retry; redelivery re-evaluates
		// every gap idempotently (existing marks re-fire the same episode
		// requestId).
		return substrate.Nak
	case delayed:
		// Only delayed-retry gaps (unresolved references, metadata gaps) —
		// redeliver on the bounded cadence, never a hot loop.
		return substrate.NakWithDelay
	case longDelayed:
		// Only config-error declines (§3.2's config class: an unbuildable
		// template, an un-dispatchable action). Their fix arrives as a
		// registry/package edit that produces NO new row delivery, so this
		// redelivery loop is the only automatic uptake path — paced on the long
		// floor, because a short cadence there buys nothing but churn.
		return substrate.NakWithLongDelay
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
			// A config dead-end: the row names a column the playbook does not.
			// The standing issue is the whole disposition — the row ACKS.
			//
			// Ack, not the config class's long redelivery floor, because this exit
			// has a population whose fix never arrives. A package may project a
			// `missing_*` column DELIBERATELY with no gaps entry, ORing it into
			// `violating` to keep the row violating without dispatching anything
			// (packages/lease-signing's missing_decision / missing_manager, whose
			// closure is a human decision or nothing at all). A long Nak parks such
			// a row for as long as the column stands — for missing_manager, forever
			// — holding a MaxAckPending slot and re-running the whole
			// clearClosedMarks preamble every floor for a configuration that is
			// already correct. That is the same argument that leaves the
			// unregistered-target exit at Ack. A genuinely missing entry is
			// surfaced by the standing issue and taken up by the next projection of
			// the row, which delivers on its own.
			//
			// `warning`, not `error`: the condition self-heals the moment the
			// playbook names the column, and Weaver goes on dispatching every other
			// target meanwhile — Contract #5 §5.2 reserves `unhealthy` (which any
			// `error` drives, aggregateStatus) for "cannot fulfil its primary
			// responsibility". A package-authoring typo is a degradation.
			//
			// Raised through the paced seam, not alert: this fault is re-derived on
			// every delivery of every violating row of the target for as long as the
			// column stands, so its log record needs the same rationing planGap's
			// config arms get — and the paced seam is the one that logs at the
			// caller's own severity, so a `warning` fact is not written at Error.
			e.alertPaced(issueKeyGapConfig(targetID, col), "warning", "GapWithoutPlaybook",
				"target "+targetID+": row column "+col+" is true but the playbook defines no gaps entry for it")
			return substrate.Ack
		}
		// The augur policy now covers this gap — clear any GapWithoutPlaybook
		// alert raised before the policy was added, and dispatch the reasoning
		// episode through the normal lane-1 path (anti-storm mark + OCC + reclaim).
		e.issues.clear(issueKeyGapConfig(targetID, col))
		ga = esc
	}

	if ga.Action == actionSurface {
		// FR29: surface-only, never dispatch. No mark, no OCC, no episode —
		// just a Health-KV issue for as long as THIS entity's gap stays open
		// (cleared by clearClosedMarks below when this row stops naming the
		// column). The issue states a fact about one row, so it is keyed per
		// (target, entity, gap): N subjects violating the same column
		// concurrently raise and retire N independent issues.
		sev := ga.IssueSeverity
		if sev == "" {
			sev = "warning"
		}
		code := ga.IssueCode
		if code == "" {
			code = "Surface"
		}
		e.issues.set(issueKeyGapEntity(targetID, entityID, col), sev, code,
			"target "+targetID+" entity "+entityID+": row column "+col+" is true")
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
	// author nominates a gap for that class by declaring its inflight_<g>
	// companion column at all, and staleMark's own externalDispatchGap
	// classifier confirms the dispatch really is one (directOp/proposedOp, or a
	// triggerLoom whose pattern is external-eligible). The human userTask gaps
	// (assignTask; triggerLoom of a pattern that parks) fail that classifier
	// whether or not they declare the column, so this branch never touches them
	// and their claimId stays preserved verbatim exactly as §10.3 requires.
	//
	// inflight_<g> reading false is NOT proof no call is outstanding. A lens
	// computes it from artifact presence — lease-signing uses "dispatch aspect
	// set AND outcome aspect absent" (packages/lease-signing/lenses.go) — and
	// the .dispatch aspect is written by the BRIDGE, only once the adapter has
	// accepted the call. Between the dispatch op committing (the Loom instance
	// and its claim vertex exist) and .dispatch landing, a call is
	// committed-and-queued while inflight_<g> still reads false, so a reclaim
	// in that window mints a genuinely second call. What bounds the exposure is
	// the `!leaseLive` requirement below: the mark's whole lease must have
	// expired first (production default 30 minutes), which is orders of
	// magnitude longer than a bridge turnaround. It is a window, not an
	// impossibility — tests that shorten the mark lease have to keep it well
	// clear of that turnaround (see asyncConvergeOpts in
	// internal/leaseconvergence).
	stale := found && !leaseLive(rec.LeaseExpiresAt, time.Now()) && e.staleMark(targetID, entityID, row, col, ga)
	return e.fireEpisode(ctx, targetID, entityID, entityKey, col, action, pl, msg.NumDelivered != 1, rec, markRev, found, stale)
}

// staleMark reports whether gap column col is declared as an EXTERNAL gap
// (Contract #10 §10.3) whose row currently shows no call in flight, so a
// found mark for it is a stale bookkeeping remnant of a concluded attempt,
// not a live episode. See the dispatchGap call site for the full contract
// citation. An absent inflight_<g> column makes this unconditionally false, so
// a gap that never nominated itself for the external class keeps its reclaim
// untouched (claimId preserved verbatim, per §10.3).
//
// inflight_<g> carries exactly one meaning by contract
// (10-orchestration-weaver.md: "a remediation is already in flight → suppress
// re-dispatch"): it is available to ANY gap and honored for suppression on both
// dispatch legs (gapSuppressed). This function reads it for a SECOND, narrower
// purpose — gating the external-gap stale-reconcile, the reclaim that mints a
// FRESH claimId (10-orchestration-substrate.md §10.3: "re-call a dead vendor /
// mint a fresh service instance"). That authority belongs only to a gap whose
// dispatch can actually make such a call with a later, outcome-driven
// conclusion, which externalDispatchGap decides from the dispatch's real shape
// (the pattern's step kinds), never from the action name.
//
// A gap that concludes on a human instead (an assignTask, or a triggerLoom of a
// pattern that parks) is NOT an authoring bug for declaring inflight_<g> — it is
// using the column for its sole contract purpose, suppression. It simply confers
// no stale-reconcile authority here, so staleMark returns false and the gap keeps
// §10.3's claimId-preserved-verbatim rule; gapSuppressed still honors the marker
// on both legs. The two human-paced lease-signing gaps (onboarding, signature)
// declare it for exactly this reason — it is what stops their reclaim from
// re-firing a remediation whose task already sits open. So the non-external case
// raises nothing; it is logged at Debug (the `why` names the transient
// unreplayed-pattern case vs. the permanent human-gap one for operators reading
// the log, but neither is a Health issue).
func (e *Engine) staleMark(targetID, entityID string, row map[string]any, col string, ga GapAction) bool {
	g, ok := strings.CutPrefix(col, gapColumnPrefix)
	if !ok {
		return false
	}
	if _, declared := row[inflightColumnPrefix+g]; !declared {
		return false
	}
	external, _, why := e.externalDispatchGap(ga, row)
	if !external {
		e.logger.Debug("weaver: inflight_<g> declared on a non-external gap; honored for "+
			"suppression, ignored for stale-reconcile ("+why+")",
			"targetId", targetID, "gap", col)
		return false
	}
	inflight, _ := e.boolColumn(targetID, entityID, row, inflightColumnPrefix+g)
	return !inflight
}

// externalDispatchGap reports whether ga's dispatch concludes on an EXTERNAL
// call's outcome — the class Contract #10 §10.3 governs with inflight_<g> +
// maxretries_<g>. When it does not, why explains it and transient says whether
// the answer could change on its own (so the caller logs rather than alerts).
//
// directOp qualifies outright; so does proposedOp, since an Augur proposal's
// materialized inner action is data-dependent and may itself be directOp
// (§10.8). triggerLoom is decided by the PATTERN, not the action: a pattern
// whose every step is a known non-parking kind runs to an outcome with no human
// in it — precisely the shape lease-signing's backgroundCheck and collectPayment
// gaps use — while a pattern that parks (a userTask step, or any step kind this
// build cannot vouch for — externalEligibleSteps) concludes on a person, and
// re-dispatching it would mint a duplicate human task.
//
// An UNKNOWN pattern — one whose spec the registry has not indexed — is
// classified NOT external, the fail-safe direction: a duplicated human task is
// a worse outcome than a delayed retry, and the classification self-corrects
// the moment the spec replays. That is the transient case. It is genuinely
// reachable from the SWEEP, which calls staleMark well before planGap (see
// reconciler.go reclaim) — so a Weaver restart with marks already in
// weaver-state hits it on every pass until the registry finishes replaying.
// Lane-1 does not: planGap runs first there and defers the gap on an
// unresolvable pattern before dispatchGap ever consults staleMark.
//
// assignTask and surface never make an external call.
func (e *Engine) externalDispatchGap(ga GapAction, row map[string]any) (external, transient bool, why string) {
	switch ga.Action {
	case actionDirectOp, actionProposedOp:
		return true, false, ""
	case actionTriggerLoom:
		ref, perr := resolveStringParam("pattern", ga.Pattern, row)
		if perr != nil {
			// A row-templated pattern reference that resolves null is a row-data
			// problem the next projection can fix, not a fixed playbook mistake.
			return false, true, "its triggerLoom pattern reference does not resolve: " + perr.msg
		}
		eligible, known := e.source.patternIsExternalEligible(ref)
		switch {
		case !known:
			return false, true, "pattern " + strconv.Quote(ref) + " has no indexed spec yet, so it " +
				"counts as parking on a human until one replays"
		case !eligible:
			return false, false, "pattern " + strconv.Quote(ref) + " is not externalTask-only — it has a " +
				"userTask step, no steps at all, or a step kind this build does not recognise"
		}
		return true, false, ""
	default:
		return false, false, "its playbook action " + strconv.Quote(ga.Action) + " never makes an external call"
	}
}

// surfaceOnlyGap reports whether ga is FR29's `surface` action — the one entry
// in the playbook vocabulary that dispatches NOTHING (Contract #10: "surface,
// never dispatch"). It raises a named Health issue while its column is true and
// is cleared by the ordinary level reconcile; it mints no mark, no episode and
// no dispatch-count, and buildPlan has no case for it at all.
//
// It is a predicate rather than a repeated comparison because four sites need
// the same verdict for the same reason, and the reason is not obvious at any of
// them: a package upgrade that rewrites a gap's action to `surface` STRANDS the
// weaver-state the previous action left — a mark for an episode that was in
// flight at the upgrade, a dispatch-count for the chain that had run, both
// outliving the action that created them (a surface column is even skipped by
// clearClosedMarks' mark cleanup, so only TTL collects it). Every leg that
// reaches a gap through that stranded state, rather than through dispatchGap's
// own surface branch, therefore meets a `surface` gap it must not act on:
// planning one falls to buildPlan's default and alerts `PlaybookConfigError`
// against a playbook that is entirely contract-legal; escalating one raises
// GapBudgetExhausted at the very latch lane-1 holds the surface issue on; and
// merely reading the suppression verdict for one makes a stranded count look
// like a spent budget, which SKIPS the column and switches its diagnostic off.
//
// The four sites are handleRow's suppression gate, escalateExhaustedGap, the
// sweep's reclaim, and the count leg's re-arm. handleRow and reclaim each need a
// guard of their own because each declines something the escalation never sees:
// handleRow SKIPS the column outright on a spent budget, and the Surface issue
// dispatchGap would have raised is what that skip costs; reclaim would
// re-dispatch a stranded mark. With both guarded, the escalation's only
// currently-reachable surface path is the count leg's arm (l) — but the guard
// sits INSIDE escalateExhaustedGap rather than at that one caller, so a future
// third caller inherits it instead of having to remember it.
//
// A column with no playbook entry at all reads false here — the zero GapAction
// carries no action — which is right: an orphan column is a different verdict
// with its own arms.
func surfaceOnlyGap(ga GapAction) bool {
	return ga.Action == actionSurface
}

// planGap resolves one gap's plan (Evaluator L2 + Strategist), routing a
// failure by its class: an unresolved reference defers on the bounded
// redelivery cadence; a config or data error is surfaced and the gap declines on
// the long floor, since a package edit is what fixes it and that edit delivers
// nothing. Both classes raise at `warning` (degraded), and their SCOPE is what
// differs: a per-row DATA error (a malformed/incomplete anchor row whose
// template references resolve null) is one bad row, keyed per entity, while a
// CONFIG error (an un-dispatchable action, a vanished pin) is identical for
// every row of the target and keyed per (target, gap). Neither pins the
// component unhealthy — each self-heals on a package edit while Weaver goes on
// dispatching every other target (Contract #5 §5.2). pl == nil means do not
// dispatch — the returned Decision is the caller's disposition for this gap.
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
			e.issues.clear(issueKeyGapConfig(targetID, col))
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
			// A plan built and about to fire disproves THREE standing facts: the
			// playbook resolves (the config issue), this entity's gap has an
			// attempt left after all (its GapBudgetExhausted, which a raised
			// maxretries_<g> or an escalation can make stale without the gap ever
			// having closed), and this row's template references resolve (the
			// template issue — building the plan IS the resolution).
			//
			// The gap column's own `data:` entry is deliberately NOT cleared here:
			// a built plan says nothing about whether missing_<g> carries a §10.2
			// bool, and that entry is raised and retired by boolColumn's own read.
			e.issues.clear(issueKeyGapConfig(targetID, col))
			e.issues.clear(issueKeyGapEntity(targetID, entityID, col))
			e.issues.clear(issueKeyTemplateEntity(targetID, entityID, col))
			return pl, actionRef, substrate.Ack
		}
	}
	// Every arm below is re-derived on a CADENCE, not on an event: for a parked
	// gap this switch runs once per sweep pass — from reclaim, the count leg's
	// re-arm, the goal leg-advance and escalateExhaustedGap — for the whole life
	// of the dispatch-count TTL. All three therefore log through alertPaced,
	// which keeps the Health latch set on every pass and rations the loud
	// records to one an hour per key. None of these faults is event-shaped: each
	// message is a pure function of its key and the current failure, so nothing
	// unique is lost to a damped pass.
	switch perr.kind {
	case errTransient:
		// An unresolved reference may be replay lag or a permanent config
		// error (a typo'd pattern, an uninstalled package) — retry on the
		// bounded redelivery cadence (never a hot loop) and surface to
		// Health until it resolves; the issue clears on the first
		// successful plan.
		// The message names the entity even though the KEY does not. A gap's
		// `pattern` / `operation` resolve through resolveStringParam, so a
		// row.<column> template makes the unresolved reference genuinely
		// per-row: two entities can reach this one target-scoped key with
		// different unresolved refs. The latch keeps only the newest of them and
		// the pacing damps the rest to Debug, so a record that did not name its
		// row would leave neither surface able to say which row.
		e.alertPaced(issueKeyGapConfig(targetID, col), "warning", "UnresolvedReference",
			"target "+targetID+" entity "+entityID+" gap "+col+" dispatch deferred for redelivery: "+perr.msg)
		return nil, "", substrate.NakWithDelay
	case errData:
		// The fault is template × row, so one of its two fix paths — a template
		// or playbook edit — produces no new delivery of this row. That puts it
		// in the config class by the fix-path rule: the long floor, so the edit
		// is picked up automatically, while a re-projection of the row (the other
		// fix path) supersedes the pending delivery and is picked up at once.
		e.alertPaced(issueKeyTemplateEntity(targetID, entityID, col), "warning", "TemplateDataError",
			"target "+targetID+" entity "+entityID+" gap "+col+": "+perr.msg)
		return nil, "", substrate.NakWithLongDelay
	default:
		// A CONFIG error — an un-dispatchable action, a vanished pin. Only a
		// package edit fixes it and that edit delivers nothing, so the long
		// redelivery floor is the automatic uptake path. `warning`, not `error`,
		// for the reason dispatchGap's GapWithoutPlaybook raise gives: the fact
		// self-heals on the edit and Weaver keeps fulfilling its responsibility
		// for every other target while it stands (Contract #5 §5.2).
		e.alertPaced(issueKeyGapConfig(targetID, col), "warning", "PlaybookConfigError",
			"target "+targetID+" gap "+col+": "+perr.msg)
		return nil, "", substrate.NakWithLongDelay
	}
}

// admitGap consults Fire 8's admission scheduler for one resolved gap
// dispatch, called from every fresh-dispatch seam planGap serves — lane-1's
// dispatchGap, the reconciler's reclaim, and the reconciler's count-leg re-arm
// (sweepCount's arm (n), for a gap whose row has gone quiet) — so a declared
// budget paces a re-fire and a re-arm exactly like a fresh episode. target.Admission == nil (a
// target with no policy configured) short-circuits true without reading the
// row's priority column — byte-identical dispatch. id is the mark-key shape
// (<targetId>.<entityId>.<gapColumn>), a stable identity for this gap's
// pending-admission entry across redeliveries.
func (e *Engine) admitGap(target *Target, targetID, entityID, col, adapter string, row map[string]any) bool {
	if target.Admission == nil {
		// No policy means the priority column is never read again, so a standing
		// data error for it describes a read that no longer happens — the same
		// shape as boolColumn's absent-column clear, and the only site that can
		// see it: clearClosedMarks retires that entry when no candidate column of
		// the entity stayed open, and an entity whose gap never closes never
		// reaches it. Idempotent when none stands.
		e.issues.clear(issueKeyDataEntity(targetID, entityID, admissionPriorityColumn))
		return true
	}
	priority, _ := e.intColumn(targetID, entityID, row, admissionPriorityColumn)
	id := targetID + "." + entityID + "." + col
	return e.admission.admit(target.Admission, targetID, id, adapter, priority, time.Now())
}

// fireEpisode is the lane-1 dispatch core: CAS-create the mark on absence
// (the dispatch OCC) and fire the episode op. rec/markRev/found/stale are the
// caller's own already-read mark snapshot (dispatchGap reads it once, up
// front, so the Fire 5 candidate-pin resolution and the fire decision made
// here never see two different mark states). redelivered selects the
// genuinely-in-flight disposition — false drops (the anti-storm gate:
// another episode is in flight), true re-publishes the SAME episode
// requestId (idempotent at the Contract #4 tracker). stale (staleMark)
// reclaims the mark in place instead — see that branch. The reconciler
// sweep's OWN reclaim does not pass through here for its lease-expiry case:
// it replaces the expired mark in place under a revision condition and
// fires directly, independently. action is recorded on the mark (the §10.3
// value shape) so a later reclaim can re-dispatch the right episode.
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
		// human userTask gaps (assignTask; triggerLoom of a pattern that parks),
		// whose §10.3-mandated claimId-verbatim preservation this branch never
		// reaches. Note that is NOT because they leave inflight_<g> undeclared —
		// lease-signing's lens declares inflight_onboarding and
		// inflight_signature over exactly such gaps — but because staleMark's
		// externalDispatchGap classifier rejects them on the dispatch's shape
		// regardless of the column (see dispatchGap).
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

// planOptionalReads resolves a plan's optional-read closure for one episode's
// claimID (nil-safe — most plans declare none).
func planOptionalReads(pl *plan, claimID string) []string {
	if pl.optionalReads == nil {
		return nil
	}
	return pl.optionalReads(claimID)
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
func (e *Engine) fire(ctx context.Context, targetID, entityID, col string, markRevision uint64, claimID string, pl *plan) substrate.Decision {
	requestID := deriveEpisodeRequestID(targetID, entityID, col, markRevision)
	if pl.requestID != nil {
		requestID = pl.requestID(claimID)
	}
	if err := e.act.submit(ctx, requestID, pl.operationType, pl.class, pl.payload(claimID), pl.authTarget, pl.reads, planOptionalReads(pl, claimID), pl.enumerations); err != nil {
		e.logger.Error("weaver: op publish failed; nak for retry",
			"targetId", targetID, "entityId", entityID, "gap", col, "requestId", requestID, "err", err)
		return substrate.Nak
	}
	if fu := pl.followUp; fu != nil {
		fuRequestID := deriveEpisodeRequestID(targetID, entityID, col, markRevision)
		if fu.requestID != nil {
			fuRequestID = fu.requestID(claimID)
		}
		if err := e.act.submit(ctx, fuRequestID, fu.operationType, fu.class, fu.payload(claimID), fu.authTarget, fu.reads, planOptionalReads(fu, claimID), fu.enumerations); err != nil {
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
// A gap actually being cleared here is also a CLOSE event for the §10.3
// `__effect` confidence window (design §3.2): the mark's Action names which
// actionRef to record the close against, read BEFORE the delete (the delete
// carries no value to recover the action from). The delete is revision-
// conditioned on that read and the close credited only when THIS path wins it,
// so lane-1 racing the sweep on the same close credits it once, not twice
// (mirroring releaseCompletedLeg and every other sweep-path mark mutation); a
// lost race is a normal outcome, never an error, and must not Nak. A read
// failure has no revision to condition on, so it logs, falls back to a blind
// best-effort clear, and records no close — the window is a future ranking
// input, never a gate, so it must never block the mark/count clear it rides
// alongside.
func (e *Engine) clearClosedMarks(ctx context.Context, target *Target, targetID, entityID string, row map[string]any) bool {
	ok := true
	if row == nil {
		// The entity is deleted: every data and template error its columns ever
		// raised describes a row that no longer exists. The column readers
		// retire the data ones on a value that parses, but a deleted entity
		// projects no values at all, so this is the only pass that can retire
		// them — by prefix, since the set of columns that raised is not knowable
		// from an empty body.
		e.issues.clearPrefix(issuePrefixData + targetID + "." + entityID + ".")
		e.issues.clearPrefix(issuePrefixTemplate + targetID + "." + entityID + ".")
	}
	anyOpen := false
	for _, col := range markCandidateColumns(target, row) {
		// A deletion tombstone reports no columns at all, which is a well-formed
		// closure of every one of them — the same reading as a row that dropped
		// the column.
		open, wellFormed := false, true
		if row != nil {
			open, wellFormed = e.boolColumn(targetID, entityID, row, col)
		}
		if open {
			// anyOpen answers exactly one question — is any gap left that could
			// READ the admission priority column — so only a gap that can reach
			// the admission gate may hold it true. A `surface` gap cannot:
			// dispatchGap returns at its own branch before planGap, so admitGap
			// is never called for it. Letting one hold the answer open would pin
			// an entry about a column nothing will read again. Every other
			// candidate counts, including one the playbook does not name: an
			// augur escalation plans those, and planning reaches the gate.
			if !surfaceOnlyGap(target.Gaps[col]) {
				anyOpen = true
			}
			continue
		}
		// A closed gap retires the standing issues at this column along with its
		// bookkeeping — a row that stopped reporting the column closed it, a
		// retraction tombstone included (its body carries no gap columns at
		// all). retireClosedGapIssues carries the per-(entity, gap) set, shared
		// with the sweep legs that observe the same end from a mark or a
		// dispatch-count.
		//
		// The config scope retires here and only here, and that is not
		// incidental: this is the ONLY clear site a GapWithoutPlaybook /
		// UnresolvedReference / PlaybookConfigError can reach when the column
		// simply stops being reported, since every other config clear requires
		// the config to have been FIXED. It stays on this leg because it is a
		// fact about the whole target, and only a walk of the candidate set —
		// which the sweep, holding one mark, does not have — observes a column
		// leaving rather than one entity's gap ending. Idempotent when none
		// stands.
		e.retireClosedGapIssues(targetID, entityID, col)
		if wellFormed {
			// The config latch is TARGET-scoped, so this one entity's column is
			// evidence about every row of the target — and only a well-formed read
			// is evidence at all. A column present with a non-bool value says
			// nothing about whether the gap closed (boolColumn already raises its
			// own RowDataError for it), so clearing on it would let one repeatedly
			// re-projecting broken row retire the whole target's config alert at
			// its projection rate. An absent column is the opposite case and does
			// clear: it IS the closure this arm exists for.
			e.issues.clear(issueKeyGapConfig(targetID, col))
		}
		if ga, isSurface := target.Gaps[col]; isSurface && ga.Action == actionSurface {
			// A surface gap never creates a mark (dispatchGap returns before
			// e.marks.get) — nothing further to clear for this column.
			continue
		}
		rec, markRev, found, gErr := e.marks.get(ctx, targetID, entityID, col)
		if gErr != nil {
			e.logger.Warn("weaver: mark read before clear failed; effect close not recorded",
				"targetId", targetID, "entityId", entityID, "gap", col, "err", gErr)
		}
		markCleared := false
		if gErr == nil && found {
			conflict, err := e.marks.deleteRevision(ctx, targetID, entityID, col, markRev)
			if err != nil {
				e.logger.Error("weaver: mark clear failed",
					"targetId", targetID, "entityId", entityID, "gap", col, "err", err)
				ok = false
			} else {
				markCleared = !conflict
			}
		} else if err := e.marks.delete(ctx, targetID, entityID, col); err != nil {
			e.logger.Error("weaver: mark clear failed",
				"targetId", targetID, "entityId", entityID, "gap", col, "err", err)
			ok = false
		}
		if err := e.marks.deleteDispatchCount(ctx, targetID, entityID, col); err != nil {
			e.logger.Error("weaver: dispatch-count reset failed",
				"targetId", targetID, "entityId", entityID, "gap", col, "err", err)
			ok = false
		}
		if markCleared {
			if cErr := e.marks.recordEffectClose(ctx, targetID, col, rec.Action); cErr != nil {
				e.logger.Warn("weaver: effect close record failed",
					"targetId", targetID, "entityId", entityID, "gap", col, "err", cErr)
			}
		}
	}
	// The admission priority column is read once per gap DISPATCH (admitGap), so
	// an entity with no dispatchable candidate still open has no read left to
	// retire a malformed value's entry. It is scoped to the entity, not to any
	// one gap, so the retirement is too: clearing it per closing gap would erase
	// the entry a multi-gap entity's still-open gap keeps re-raising, and the two
	// would flap. A clear can only flap against a leg that RE-RAISES, which is
	// why anyOpen counts dispatchable candidates rather than open ones.
	if !anyOpen {
		e.issues.clear(issueKeyDataEntity(targetID, entityID, admissionPriorityColumn))
	}
	return ok
}

// retireGapPlanIssues retires the facts that end when this gap stops being a
// DISPATCHABLE candidate for this entity: the template fault at its plan, and
// the two companion columns' data errors. All three are raised only on the way
// to a dispatch — planGap's template resolution, and the suppression terms'
// reads of inflight_<g> / maxretries_<g> — so once nothing will plan this gap
// for this row again, nothing will read them again either.
//
// THREE legs can be the one that observes that end, and any of them can be the
// only one that does. Lane-1's clearClosedMarks sees it when a delivery stops
// reporting the column. The sweep's deleteMark sees it from a mark — gap closed,
// playbook dropped the gap, or target gone — and its deleteCount sees the same
// closed-column fact from a dispatch-count when no mark survives to carry the
// reconcile. For a row that has gone QUIET the sweep legs are the only ones that
// run at all; for a gap the playbook no longer names they are the only ones that
// CAN (markCandidateColumns is the playbook's gaps keys unioned with the row's
// missing_* columns, so a gap dropped from both is never walked again). One
// function keeps a retirement added for one leg from silently missing the others.
//
// gapColumn is not assumed to carry the missing_ prefix. On the lane-1 leg the
// candidate set guarantees it, but the sweep legs derive it from a weaver-state
// KEY, and splitMarkKey validates the segment's token shape rather than the
// column convention — so a key stranded by an earlier package version reaches
// here with any legal token, and a gap column that is not missing_*-shaped
// simply has no companions to name.
//
// Three things deliberately do NOT retire here. The gap column's own data entry
// says "missing_<g> is not the §10.2 bool", and the read that decides that is
// the same read that brought any of these legs to this point — it has just
// settled the entry itself, so clearing it again would retire a fault the
// current row is still carrying. The target-scoped config issue is one fact for
// every row of the target, and one entity's gap ending is not evidence about the
// playbook. The admission priority entry belongs to the ENTITY rather than to
// any one gap: it may retire only when the entity has no DISPATCHABLE candidate
// column still open, which a caller holding a single gap cannot see, and a
// per-gap clear would erase an entry the entity's other open gap keeps
// re-raising. That omission costs the sweep legs a retirement they could
// otherwise make — a sweep-driven planGap reaches admitGap and raises it — and
// it is kept anyway, because the no-flap rule outranks the extra coverage;
// admitGap's own no-policy short-circuit and clearClosedMarks' walk of the
// entity's whole candidate set are where that entry retires.
func (e *Engine) retireGapPlanIssues(targetID, entityID, gapColumn string) {
	e.issues.clear(issueKeyTemplateEntity(targetID, entityID, gapColumn))
	if g, isGapColumn := strings.CutPrefix(gapColumn, gapColumnPrefix); isGapColumn {
		e.issues.clear(issueKeyDataEntity(targetID, entityID, inflightColumnPrefix+g))
		e.issues.clear(issueKeyDataEntity(targetID, entityID, maxretriesColumnPrefix+g))
	}
}

// retireClosedGapIssues retires everything a gap ACTUALLY ENDING retires: the
// plan-side facts above, plus the gap's own entity-scoped latch (a `surface`
// gap standing open, a spent retry budget).
//
// The split from retireGapPlanIssues is the difference between "this gap ended"
// and "this gap is no longer dispatchable", and it is load-bearing. A gap the
// playbook dropped is undispatchable but may still read TRUE in the row, and
// lane-1 goes on raising at issueKeyGapEntity for exactly that column —
// openGapColumns enumerates every true missing_* whether the playbook names it
// or not, and escalateExhaustedGap raises there through alertStanding. Clearing
// that latch from a leg that only knows the gap left the PLAYBOOK makes it flap:
// the next delivery re-raises, the arrival test sees an empty latch and logs a
// spurious Error, and the fresh stamp destroys the age of a fact that never
// stopped holding. Only a leg that observed the column itself go false — or the
// row cease to exist — may retire it.
func (e *Engine) retireClosedGapIssues(targetID, entityID, gapColumn string) {
	e.issues.clear(issueKeyGapEntity(targetID, entityID, gapColumn))
	e.retireGapPlanIssues(targetID, entityID, gapColumn)
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
// false. entityID names the row the bad value is in: the issue is that row's,
// not the target's, so every caller threads the entity it is reading for.
//
// The read is level-driven, like every other Weaver issue: a column that parses
// — or is absent, the retraction-tombstone shape — RETIRES this row's standing
// error for it. For a column every delivery reads (violating, a gap column
// itself) that is the whole lifecycle: the next projection carrying a usable
// value is the retirement, and without it the entry would stand for the
// process's lifetime, one per (entity, column), for a lens fault that has been
// fixed. The columns read only while some gap is open — inflight_<g>,
// maxretries_<g> — get no such next read once that gap closes, so
// clearClosedMarks retires theirs at the close instead; issueKeyDataEntity's
// doc holds the whole family's teardown map.
// wellFormed reports whether the read produced a §10.2-legal answer, and it is
// NOT "the value was literally a bool": an absent or nil column is well-formed
// too (the retraction-tombstone shape — a row that stopped reporting a column
// closed it, which is the reading clearClosedMarks depends on). Exactly one
// read is ill-formed: a column PRESENT with a non-bool value, which says
// nothing at all about the fact and must not be mistaken for a false. Callers
// that only need the actionable answer ignore it.
func (e *Engine) boolColumn(targetID, entityID string, row map[string]any, col string) (value, wellFormed bool) {
	key := issueKeyDataEntity(targetID, entityID, col)
	v, ok := row[col]
	if !ok || v == nil {
		e.issues.clear(key)
		return false, true
	}
	b, isBool := v.(bool)
	if !isBool {
		msg := fmt.Sprintf("target %s entity %s: row column %q is %T, not the §10.2 bool; treated as not actionable",
			targetID, entityID, col, v)
		e.logger.Warn("weaver: " + msg)
		e.issues.set(key, "warning", "RowDataError", msg)
		return b, false
	}
	e.issues.clear(key)
	return b, true
}

// intColumn reads a §10.2 integer column off a row, returning ok=false when the
// column is absent or carries a non-numeric value (the latter a Lens data error:
// surfaced as a RowDataError, like boolColumn's non-bool path). JSON-decoded rows
// carry numbers as float64; directly-constructed rows (unit tests) may carry int
// or int64 — all coerce. A fractional float is floored to its integer part (a cap
// is a whole count by construction). The bool form of a present-but-wrong-type
// value is the caller's "no usable value" signal (ok=false), never a silent 0.
// entityID names the row the bad value is in, and a usable (or absent) value
// retires this row's standing error for the column, exactly as in boolColumn.
// The columns read here — maxretries_<g> and the admission priority column —
// are read only while some gap of the entity is open, so this read is not their
// only retirement: clearClosedMarks retires them at the close that ends the
// read (issueKeyDataEntity's doc holds the family's whole teardown map).
func (e *Engine) intColumn(targetID, entityID string, row map[string]any, col string) (int, bool) {
	key := issueKeyDataEntity(targetID, entityID, col)
	v, ok := row[col]
	if !ok || v == nil {
		e.issues.clear(key)
		return 0, false
	}
	n, parsed := 0, true
	switch t := v.(type) {
	case float64:
		n = int(t)
	case float32:
		n = int(t)
	case int:
		n = t
	case int64:
		n = int(t)
	default:
		parsed = false
	}
	if !parsed {
		msg := fmt.Sprintf("target %s entity %s: row column %q is %T, not the §10.2 integer; treated as absent",
			targetID, entityID, col, v)
		e.logger.Warn("weaver: " + msg)
		e.issues.set(key, "warning", "RowDataError", msg)
		return 0, false
	}
	e.issues.clear(key)
	return n, true
}

// defaultDirectOpRetryBudget is the engine-owned retry cap a "directOp" gap
// falls back to when its row declares NO usable maxretries_<g> AND no
// inflight_<g> companion either — Contract #10 §10.3's external-gap bound
// ("directOp likewise" bounded by inflight_<g> + maxretries_<g>") assumed
// every directOp package author declares it; a target declaring NEITHER
// column (orchestration-base's orphanedTaskGrants, wellness-domain's
// ReleaseOrphanedBooking) would otherwise reclaim a hard-failing op every
// sweep interval forever, because collapseOnlyReclaim deliberately never
// backs off a directOp reclaim (it IS the intended bounded retry, so nothing
// else paces it). This is a safety net, not a package-authoring substitute: a
// row's own maxretries_<g>, however small, always wins (gapSuppressed checks
// it first), and a gap that DOES declare inflight_<g> — even absent
// maxretries_<g> — is left alone too, since that lens has already opted into
// the §10.3 external-gap contract and its pacing is its own call. assignTask/
// triggerLoom/proposedOp are unaffected regardless (they keep the Option-D
// backoff path, reconciler.go's collapseOnlyReclaim) — this constant is
// consulted for a "directOp" action only.
const defaultDirectOpRetryBudget = 3

// gapSuppressed reports whether gap column gapCol (a missing_<g>) must NOT be
// (re-)dispatched, and — when suppressed — WHY: its inflight_<g> companion is
// true (a remediation is legitimately in flight, `exhausted` false) OR its
// weaver-state dispatch-count has reached its retry cap (the retry budget is
// spent — §E mechanism B, `exhausted` true). It is the dispatch gate read by
// BOTH dispatch legs — the lane-1 loop and the sweep's reclaim — so a
// suppressed gap is neither freshly dispatched nor reclaimed while it stays
// violating. The two suppression reasons are NOT interchangeable for the
// caller: inflight is ordinary in-progress state that must always be left
// alone, while exhausted is a decision point (Contract #10 §10.8 Planner
// extension: "budget exhaustion... raises a standing Health issue at the
// suppression site, never a silent park") — callers branch on `exhausted` to
// invoke escalateExhaustedGap rather than silently skipping.
//
// inflight is authoritative and read first (a true inflight short-circuits the
// KV read, and is never `exhausted`). An absent/non-bool inflight reads false
// via boolColumn (which surfaces a non-bool as a RowDataError). action is the
// gap's resolved dispatch contract type (the caller's target.Gaps[col].Action,
// or the mark's own recorded Action for a reclaim) — consulted ONLY to decide
// the cap-fallback below; it never affects the inflight term. A gapCol without
// the missing_ prefix has no companions, so it is never suppressed.
//
// The cap term: a row-declared maxretries_<g> always wins when present and
// positive (package policy always beats the engine). Absent that, a
// "directOp" gap whose row also does NOT declare inflight_<g> falls back to
// defaultDirectOpRetryBudget instead of reading "no cap" — every other
// action, and every directOp that DOES declare inflight_<g>, is unaffected
// (no usable cap → the budget term never suppresses). A count-read
// failure logs and is treated as NOT-suppressing — the safe side in every
// case is to dispatch, so a missing/garbled companion or a transient KV error
// never silently wedges a real gap. budgetIsDefault reports (only when
// exhausted) whether the spent budget was this engine default rather than a
// declared cap, so the caller's escalation message can say which.
func (e *Engine) gapSuppressed(ctx context.Context, targetID, entityID string, row map[string]any, gapCol, action string) (suppressed, exhausted, budgetIsDefault bool) {
	terms := e.gapSuppressionTerms(targetID, entityID, row, gapCol, action)
	if !terms.needsCount {
		return terms.suppressed, false, false
	}
	count, err := e.marks.getDispatchCount(ctx, targetID, entityID, gapCol)
	if err != nil {
		// A transient count read failure must not silently wedge the gap: leave
		// inflight authoritative and let the gap dispatch (the safe side). The next
		// evaluation re-reads the count.
		e.logger.Warn("weaver: dispatch-count read failed; not suppressing on the cap term",
			"targetId", targetID, "entityId", entityID, "gap", gapCol, "err", err)
		return false, false, false
	}
	return terms.verdict(count)
}

// gapSuppressedWithCount is gapSuppressed's verdict over a dispatch-count the
// caller has ALREADY read — the reconciler sweep's count leg holds the key's
// value and its revision from the same read that decided the key was not
// corrupt, and a second read of the identical key would be a third KV
// round-trip per parked gap per pass. Same terms, same order, same answers; the
// only difference is where the count came from, so a caller with no count in
// hand must keep using gapSuppressed (which reads it, and only if the verdict
// actually rests on the budget term).
func (e *Engine) gapSuppressedWithCount(targetID, entityID string, row map[string]any, gapCol, action string, count int) (suppressed, exhausted, budgetIsDefault bool) {
	terms := e.gapSuppressionTerms(targetID, entityID, row, gapCol, action)
	if !terms.needsCount {
		return terms.suppressed, false, false
	}
	return terms.verdict(count)
}

// suppressionTerms is everything gapSuppressed decides from the ROW alone: the
// authoritative inflight_<g> term, and whether a usable retry cap exists at all
// (a row-declared maxretries_<g>, else the engine default for a bare directOp
// gap). needsCount=false means suppressed is already the final answer and no
// dispatch-count is consulted — which is why the inflight and no-cap paths cost
// no KV read. needsCount=true means the verdict rests on the budget term, and
// capN/budgetIsDefault describe the cap the count is measured against.
type suppressionTerms struct {
	suppressed      bool
	needsCount      bool
	capN            int
	budgetIsDefault bool
}

// verdict applies the budget term to a dispatch-count: the budget is spent —
// and the gap is suppressed as `exhausted`, the §10.8 decision point — iff the
// count has reached the cap.
func (t suppressionTerms) verdict(count int) (suppressed, exhausted, budgetIsDefault bool) {
	if count >= t.capN {
		return true, true, t.budgetIsDefault
	}
	return false, false, false
}

func (e *Engine) gapSuppressionTerms(targetID, entityID string, row map[string]any, gapCol, action string) suppressionTerms {
	g, ok := strings.CutPrefix(gapCol, gapColumnPrefix)
	if !ok {
		return suppressionTerms{}
	}
	if inflight, _ := e.boolColumn(targetID, entityID, row, inflightColumnPrefix+g); inflight {
		return suppressionTerms{suppressed: true}
	}
	capN, ok := e.intColumn(targetID, entityID, row, maxretriesColumnPrefix+g)
	budgetIsDefault := false
	if !ok || capN <= 0 {
		if _, declaresInflight := row[inflightColumnPrefix+g]; action != actionDirectOp || declaresInflight {
			// No usable cap on the row, and either this isn't a directOp gap
			// (assignTask/triggerLoom/proposedOp keep their Option-D backoff
			// path instead of a hard cap) or the row already declares
			// inflight_<g> (a lens that has adopted the §10.3 external-gap
			// contract; its own pacing, not this engine's to second-guess) —
			// never auto-suppress.
			return suppressionTerms{}
		}
		capN, budgetIsDefault = defaultDirectOpRetryBudget, true
	}
	return suppressionTerms{needsCount: true, capN: capN, budgetIsDefault: budgetIsDefault}
}

// hasUsableRetryCap reports whether the row carries a positive maxretries_<g>
// for this gap — the same column, read the same way, that gapSuppressed caps
// the dispatch chain on. It answers only "would the cap term ever fire", not
// whether the budget is spent, and it never falls back to the engine default
// (gapSuppressed's own default is directOp-only and explicitly declines to
// apply to a row that declares inflight_<g>).
func (e *Engine) hasUsableRetryCap(targetID, entityID string, row map[string]any, gapCol string) bool {
	g, ok := strings.CutPrefix(gapCol, gapColumnPrefix)
	if !ok {
		return false
	}
	capN, ok := e.intColumn(targetID, entityID, row, maxretriesColumnPrefix+g)
	return ok && capN > 0
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
// CreateAugurReasoningClaim's own anti-storm mark. Called from all three
// suppression sites: lane-1 (handleRow), the sweep's mark leg (reclaim), and
// the sweep's count leg (sweepCount) — the last reached from the retry budget
// rather than from a mark, which is what keeps the §10.8 promise for a row that
// has gone quiet and for a gap whose mark has expired. Its un-escalated raise
// goes through alertStanding: the count leg re-derives it every pass for the
// life of the budget, so only its arrival is loud.
// budgetIsDefault (gapSuppressed's verdict) only shapes the un-escalated
// alert's wording below — it never changes the escalation/alert branch taken.
func (e *Engine) escalateExhaustedGap(ctx context.Context, target *Target, targetID, entityID, entityKey, gapColumn string,
	row map[string]any, rowRevision uint64, budgetIsDefault bool) substrate.Decision {

	if surfaceOnlyGap(target.Gaps[gapColumn]) {
		// A surface gap has no remediation chain, so it has no budget to have
		// spent: the count that reached this call is stranded from the action
		// the package replaced (surfaceOnlyGap). Both branches below would be
		// wrong for it. The un-escalated one raises GapBudgetExhausted at
		// issueKeyGapEntity — the SAME latch lane-1 holds this column's surface
		// issue on, so the two would overwrite each other and re-stamp `since`
		// on every round trip, the flap the orphan-column arm was amended to
		// stop. The escalated one dispatches a real Augur reasoning episode for
		// a gap whose whole contract is that Weaver never dispatches for it.
		// Ack: nothing to do, and nothing to redeliver for.
		//
		// The latch is left exactly as found. It is lane-1's here, and a clear
		// would fight the raise it re-derives while the column stays true.
		e.logger.Debug("weaver: exhausted-gap escalation skipped for a surface gap; its budget is stranded state, not a spent chain",
			"targetId", targetID, "entityId", entityID, "gap", gapColumn)
		return substrate.Ack
	}

	esc, escalated := augurEscalation(e.source, target, escalateExhausted, targetID, entityID, entityKey, gapColumn)
	if !escalated {
		budget := "its declared retry budget"
		if budgetIsDefault {
			g, _ := strings.CutPrefix(gapColumn, gapColumnPrefix)
			budget = fmt.Sprintf("the engine's default retry budget (%d; no maxretries_%s declared)", defaultDirectOpRetryBudget, g)
		}
		// One row's spent budget, not the target's: the count this exhausted is
		// per (target, entity, gap) in weaver-state, so the issue is keyed the
		// same way and another entity's gap closing never retires it.
		e.alertStanding(issueKeyGapEntity(targetID, entityID, gapColumn), "warning", "GapBudgetExhausted",
			"target "+targetID+" entity "+entityID+": row column "+gapColumn+" has exhausted "+budget+" with no augur escalation configured for \"exhausted\"")
		return substrate.Ack
	}
	e.issues.clear(issueKeyGapEntity(targetID, entityID, gapColumn))

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
func (e *Engine) openGapColumns(targetID, entityID string, row map[string]any) []string {
	var out []string
	for col := range row {
		if !strings.HasPrefix(col, gapColumnPrefix) {
			continue
		}
		if open, _ := e.boolColumn(targetID, entityID, row, col); open {
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
// pair. Every call logs: the families raised here include ones whose message is
// the only thing telling two genuinely distinct faults apart at one key (a
// dropped fired timer names the timer it dropped), so a raise is never assumed
// to be a repetition of the last one. A raise that IS re-derived on a fixed
// cadence for as long as its condition holds belongs on alertStanding instead.
func (e *Engine) alert(key, severity, code, message string) {
	e.logger.Error("weaver: " + message)
	e.issues.set(key, severity, code, message)
}

// alertStanding records a Health KV issue exactly as alert does, but logs the
// fact's ARRIVAL at Error and its continuation at Debug. It is for a raise the
// engine re-derives on a CADENCE rather than on an event: the §10.8
// exhausted-gap raise is re-evaluated by every reconciler sweep pass for as
// long as the retry budget stands (~128h at the defaults, one pass a minute),
// so logging each re-derivation at Error would write thousands of identical
// lines per parked gap and bury every arrival — including the arrivals of other
// faults — in a stream an operator reads to find out what just happened. The
// Health issue is unaffected: it is a latch, it carries the full fact, and its
// `since` still says when the condition first arose.
//
// The seam is deliberately narrow. A caller belongs here only if its message is
// a pure function of its key, because the arrival test compares severity and
// code alone (issueCache.standingAs): a family that varies its message per
// occurrence at one key would have those occurrences damped to Debug and lose
// them, so those keep using alert. A change of severity or code at the same key
// is a different fact and arrives loudly again.
func (e *Engine) alertStanding(key, severity, code, message string) {
	if e.issues.standingAs(key, severity, code) {
		e.logger.Debug("weaver: " + message)
	} else {
		e.logger.Error("weaver: " + message)
	}
	e.issues.set(key, severity, code, message)
}

// alertPaced records a Health KV issue exactly as alert does, but rations its
// LOUD log records to one per logPaceInterval per key — logging the rest at
// Debug — and it rations them against a CLOCK rather than against the latch.
// That is the whole difference from alertStanding, and it is what planGap's
// failure switch needs. planGap re-derives its faults once per sweep pass for a
// parked gap, and the keys it raises at are cleared by paths that are not
// evidence the fault ended: issueKeyGapConfig is target-scoped, so one entity's
// column closing retires the fact another entity's parked gap is still raising.
// An arrival test built on latch presence therefore reports an arrival on
// essentially every pass, which is no damping at all. A clock survives the
// clear, so the pacing holds.
//
// The loud level is the caller's own severity — an `error` raise stays Error, a
// `warning` raise stays Warn — so each arm of the switch keeps the loudness it
// had; unlike alert, which is Error for every caller.
//
// The Health entry is set on EVERY call, carrying the newest message: latch
// semantics are untouched, and only the log is paced. The consequence is that a
// family varying its message at one key has the variants inside a window damped
// to Debug, where only the newest survives in the latch — so every caller of
// this seam must name its subject in the message it raises, since the log is
// where a damped variant survives at all. Two rules bound the damping: a record
// is never dropped, only lowered (an unrecoverable record is worse than a noisy
// one — weaver-exhausted-gap-durable-stop-design.md §3.2b), and Message is never
// the thing damped on, since an embedded count or timestamp would re-arrive
// every pass and restore the flood. A change of severity or code is a different
// fact and is loud immediately, whatever the window says.
//
// The pace entry also carries the fault's ARRIVAL, and that is what dates the
// Health entry here (setSince) rather than the latch's own stamp. Making the log
// quiet without this would leave an operator no surface at all from which to
// recover how long a config fault has stood: clear deletes since, and the clears
// this seam's target-scoped key sees are other entities' closes rather than
// repairs, so a latch-borne stamp resets about once a pass. pacedRaise's doc
// states what the pace-borne one can and cannot distinguish.
func (e *Engine) alertPaced(key, severity, code, message string) {
	loud, arrivedAt := e.issues.pacedRaise(key, severity, code, time.Now())
	switch {
	case !loud:
		e.logger.Debug("weaver: " + message)
	case severity == "error":
		e.logger.Error("weaver: " + message)
	default:
		e.logger.Warn("weaver: " + message)
	}
	e.issues.setSince(key, severity, code, message, arrivedAt)
}

// The issue-key family prefixes. Every key constructor in the package and every
// teardown prefix in issueKeyTargetPrefixes is built from these, so a key shape
// and the prefix that retires it cannot drift apart — and so the listing cut's
// family classification (health.go's familyRank) names the same families the
// constructors mint, rather than a second copy of the strings.
//
// The first four carry a segment below the target; the rest are keyed by the
// target, the meta-vertex or the mark alone. Which of them are bounded by the
// deployment and which by the row population is familyRank's subject.
const (
	issuePrefixGapEntity   = "gap:"
	issuePrefixGapConfig   = "gapConfig:"
	issuePrefixData        = "data:"
	issuePrefixTemplate    = "template:"
	issuePrefixEffect      = "effect:"
	issuePrefixSweep       = "sweep:"
	issuePrefixConsumer    = "consumer:"
	issuePrefixTimer       = "timer:"
	issuePrefixTarget      = "target:"
	issuePrefixPendingSpec = "pendingSpec:"
	issuePrefixOscillation = "oscillation:"
)

// rowBodyColumn is the column segment the `data:` family uses for a fault in
// the row's BODY rather than in one of its columns — a value that is not
// readable JSON at all, so no column name can be recovered from it.
//
// The angle brackets are what make it safe to share the family's key space: a
// lens column name is a Cypher identifier, which cannot contain them, so this
// synthetic segment can never collide with a real column that boolColumn or
// intColumn raises a RowDataError at through the same issueKeyDataEntity
// constructor. Sharing the family is what gives it the family's teardowns —
// clearClosedMarks prefix-clears `data:<target>.<entity>.` for a deletion
// tombstone, and issueKeyTargetPrefixes clears `data:<target>.` when the target
// leaves — on top of the raise site's own clear on the next body that parses.
const rowBodyColumn = "<body>"

// issueKeyTargetPrefixes lists the per-target issue-key families that a target
// teardown must retire wholesale (Engine.Revoke) — the gap-entity, gap-config,
// row-data and template families. These are the families whose keys carry a
// segment below the target — a per-entity or per-gap one — so a teardown has no
// single key to name and must clear by prefix; the families keyed by targetID
// alone (consumer, timer, owner) are cleared by key at the same site. Each
// prefix ends in the "." separator, which is what keeps "t1." from matching a
// key under "t10.".
//
// Every family whose entries a revoke would otherwise strand belongs here: a
// revoked target delivers no rows and keeps no marks, so each of these has no
// live path left that could ever retire it. The effect family is the one
// deliberate omission — it needs no prefix clear, because flagEffectMismatches
// reconciles its alert set against every heartbeat's scan and Revoke deletes
// the target's `__effect` windows, so those entries self-clear on the next
// heartbeat.
func issueKeyTargetPrefixes(targetID string) []string {
	return []string{
		issuePrefixGapEntity + targetID + ".",
		issuePrefixGapConfig + targetID + ".",
		issuePrefixData + targetID + ".",
		issuePrefixTemplate + targetID + ".",
	}
}

// issueKeyGapEntity keys a Health issue whose raised fact is about ONE ROW: a
// `surface` gap standing open for this entity, or this entity's gap having
// spent its retry budget. Two entities violating the same (target, gap)
// concurrently are two independent facts, each raised and retired on its own
// subject's timeline, so the key carries the entity segment — mirroring
// issueKeyEffect's three-segment shape.
func issueKeyGapEntity(targetID, entityID, col string) string {
	return issuePrefixGapEntity + targetID + "." + entityID + "." + col
}

// issueKeyGapConfig keys a Health issue whose raised fact is about the PLAYBOOK
// or the DEPLOYMENT — a missing gaps entry, an unresolvable reference, an
// un-dispatchable action. Such a fact is identical for every row of the target
// and only a package re-author can fix it, so it is raised once per
// (target, gap): segmenting it by entity would mint one duplicate config alert
// per violating row.
func issueKeyGapConfig(targetID, col string) string {
	return issuePrefixGapConfig + targetID + "." + col
}

// issueKeyDataEntity keys a per-ROW data error — a column whose value is not
// the §10.2 type the reader expects, a freshUntil that is not an RFC3339
// string, a violating row carrying no entityKey echo. Every one of these is a
// fact about the one projected row that carries the bad value, fixed for that
// row alone by the next projection, so the key carries the entity segment: N
// malformed rows are N independent issues, and repairing one never retires
// another's. The entity the body may be missing is always available from the
// row KEY (splitRowKey), so every member of the family can be keyed this way.
//
// Retirement is per COLUMN, and which site owns it depends on whether the
// column keeps being read. A column every delivery reads — violating, the
// entityKey echo, a gap column itself, freshUntil — is retired by its own next
// clean read, so the next projection carrying a usable value (or dropping the
// column) is the retirement. The columns read only while some gap is open —
// the per-gap companions inflight_<g> and maxretries_<g>, and the admission
// priority column — have no such next read once that gap closes, so the close
// retires them: the companions with their own gap (retireGapPlanIssues, called
// by every leg that can observe a gap ending — lane-1's clearClosedMarks and
// the sweep's deleteMark and deleteCount), and the priority entry from
// clearClosedMarks once no candidate column of the entity stayed open, or from
// admitGap when the target stops declaring an admission policy at all. Above those, two prefix teardowns
// cover a subject that leaves entirely — clearClosedMarks prefix-clears the
// entity on its deletion tombstone, and a target leaving by either route
// (Revoke or registry removal via reconcileConsumers) prefix-clears the target
// (issueKeyTargetPrefixes).
//
// Two exits from handleRow retire nothing of the COLUMN entries, and neither
// strands one. A row whose body does not parse returns before the reconcile:
// that is unreadable evidence, not evidence of repair (the sweep takes the same
// posture on the same input), and every retirement above stays reachable by the
// next delivery that does parse — including the deletion tombstone, whose empty
// body needs no parsing. That exit does raise one member of this family in its
// own right, at rowBodyColumn, whose fact is the unreadable body rather than any
// column; it is retired by the next body that parses, at the same read. A row
// for an unregistered target returns at the registry miss, and its target's
// whole issue set is retired wholesale by the reconcileConsumers teardown that
// made the registration go away.
func issueKeyDataEntity(targetID, entityID, col string) string {
	return issuePrefixData + targetID + "." + entityID + "." + col
}

// issueKeyTemplateEntity keys a per-ROW PLAN-BUILD data error: a gap whose
// action template references resolve null against this row, so no episode can
// be built for it (planGap's TemplateDataError). Like the data family it is a
// fact about one (target, entity, gap column) triple, and like it the fact is
// repaired for that row alone by the next projection.
//
// It cannot share issueKeyDataEntity's key at the gap column, because that key
// already latches a DIFFERENT fact about the same column — "missing_<g> is not
// the §10.2 bool", raised and retired by boolColumn. Every delivery that
// reaches planGap for a column has already read that column as a bool at least
// twice (clearClosedMarks' candidate walk, then openGapColumns), and each read
// retires whatever stands at the shared key. A template fault would therefore
// be erased and re-raised on every single delivery, re-stamping its `since` to
// now and reporting a week-old fault as seconds old (Contract #5 §5.5). Its own
// prefix keeps the two facts on independent timelines.
//
// Its teardowns are the data family's without the "read is the retirement"
// leg, which no column read can serve here: a successful plan retires it (that
// IS the fact ending), retireGapPlanIssues retires it on whichever leg observes
// the gap end, and the entity-tombstone and target prefix clears cover a subject
// that leaves entirely.
func issueKeyTemplateEntity(targetID, entityID, col string) string {
	return issuePrefixTemplate + targetID + "." + entityID + "." + col
}

func issueKeyEffect(targetID, gapColumn, actionRef string) string {
	return issuePrefixEffect + targetID + "." + gapColumn + "." + actionRef
}

// issueKeyTarget keys a Health issue about a target's REGISTRATION — a spec the
// registry rejected — and is keyed by the owning meta-vertex id, which is what
// the registry holds at the moment a spec fails to resolve to a targetId.
func issueKeyTarget(ownerVertexID string) string {
	return issuePrefixTarget + ownerVertexID
}
