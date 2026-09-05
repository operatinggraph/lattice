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
	//
	// A body that does not parse is a per-row DATA error, and the only thing
	// that can fix it is a re-projection — which supersedes this revision and
	// delivers on its own, so the row Acks rather than holding a pending slot.
	// What the Ack must not do is lose the fact: the standing issue at the
	// row's synthetic body column is the durable "acked but declined" record,
	// and the successful parse below is its repair. Nothing else can retire it
	// — no column reader ever names rowBodyColumn — except the two prefix
	// teardowns that retire the whole `data:` family for a subject that leaves
	// (the deletion tombstone in clearClosedMarks, and a target's Revoke /
	// registry removal).
	var row map[string]any
	if len(msg.Body) != 0 {
		if err := json.Unmarshal(msg.Body, &row); err != nil {
			unparseable := "weaver-targets row " + key + " does not parse as JSON: " + err.Error()
			e.logger.Warn("weaver: " + unparseable)
			e.issues.set(issueKeyDataEntity(targetID, entityID, rowBodyColumn), "warning", "RowDataError", unparseable)
			return substrate.Ack
		}
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
	violating := row != nil && e.boolColumn(targetID, entityID, row, "violating")
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

	// The three retry accumulators, collapsed at the tail with precedence
	// Nak > NakWithDelay > NakWithLongDelay > Ack: a message carries ONE
	// decision for the whole row, so the shortest floor any gap asked for wins
	// — a gap needing an immediate retry must not wait out another gap's
	// config-error floor, and the long floor's own gap is re-evaluated by
	// whichever redelivery arrives first either way. Each switch below names
	// every non-Ack Decision explicitly, because a value that falls through to
	// `default` is silently downgraded to Ack rather than failing to compile.
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
	if nak {
		// At least one gap's op publish failed and needs an immediate retry.
		// The redelivery re-evaluates every gap idempotently: the failed gap is
		// named in the republish set and re-publishes its SAME episode
		// requestId, every other in-flight gap takes dispatchGap's anti-storm
		// drop untouched, and a gap that plans afresh derives its requestId from
		// the mark it creates. Nothing is double-acted.
		return substrate.Nak
	}
	if delayed {
		// Only delayed-retry gaps (unresolved references, metadata gaps) —
		// redeliver on the bounded cadence, never a hot loop.
		return substrate.NakWithDelay
	}
	if longDelayed {
		// Only config-error gaps (no playbook entry, an unbuildable template,
		// an undispatchable action). Their fix arrives as a package/target
		// change that projects no new row, so the redelivery IS the uptake
		// path — held on the long floor, which prices the re-poll cadence of a
		// row that stays declined until an author fixes it.
		return substrate.NakWithLongDelay
	}
	return substrate.Ack
}

// dispatchGap runs Evaluator L2 + Strategist + Actuator for one open gap.
//
// Dispatch OCC (§10.8): the weaver-state CAS-create is the anti-storm gate —
// create wins → dispatch; create loses → the winner dispatched, drop.
//
// The anti-storm decision is taken EARLY, from the mark read, before anything
// plans or admits: a gap whose mark is present and whose lease is live has an
// episode genuinely in flight, and the ONE thing that can still be owed against
// it is whether that episode's op actually reached the Processor. The republish
// set answers exactly that, per gap, so the live-mark disposition is a two-way
// split: re-publish the episode when its last publish failed, drop otherwise.
//
// That ordering is load-bearing now that a row's OTHER gap can hold the whole
// row on a redelivery loop — a config-error gap Naks the message, and every
// redelivery it causes re-enters this function for every open gap on the row.
// Planning a marked gap on each of those passes would consume an admission
// token per cycle and, worse, re-publish the in-flight episode's op every cycle
// for as long as the row keeps coming back. The republish set is what keeps the
// re-publish per-GAP and conditional on a real publish failure; a message's own
// redelivery count cannot serve, being per-MESSAGE for an obligation each of the
// row's gaps holds separately.
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
			// A CONFIG error: the fix is a package re-author adding the gaps
			// entry, which projects no new row — so nothing would ever
			// re-deliver this row and an Ack would strand it violating forever.
			// Hold it on the long redelivery floor instead: each redelivery
			// re-evaluates against the CURRENT playbook, so the fix is picked
			// up automatically within one floor with no rebuild.
			//
			// `warning`, not `error`: the fact now stands for as long as it
			// holds (the loop re-raises it every floor), and aggregateStatus
			// maps any `error` over the whole issue set to `unhealthy` — so an
			// `error` here would report "cannot fulfil its primary
			// responsibility" (Contract #5 §5.2) for a Weaver that is
			// dispatching normally for every other target. The severity is one
			// value for every caller of this raise, lane 1 included.
			//
			// alertPaced, not alert: this raise is now re-derived on a CADENCE —
			// once per long floor per stuck row, for as long as the playbook is
			// wrong — and alert logs every call at Error. It is the third raise at
			// this target-scoped key, and it needs its two siblings' seam for the
			// same reason they do: clearClosedMarks retires the key as soon as ANY
			// one entity's column closes, so an arrival test built on the latch
			// (alertStanding) would report an arrival on essentially every pass and
			// damp nothing. A clock survives that clear.
			//
			// The message stays a pure function of the key — the playbook is wrong
			// for the target, identically for every row — so nothing is lost to a
			// damped pass, exactly as for the PlaybookConfigError sibling. (The
			// UnresolvedReference sibling names its entity because ITS fault
			// genuinely differs per row; this one does not.)
			e.alertPaced(issueKeyGapConfig(targetID, col), "warning", "GapWithoutPlaybook",
				"target "+targetID+": row column "+col+" is true but the playbook defines no gaps entry for it")
			return substrate.NakWithLongDelay
		}
		// The augur policy now covers this gap — clear any GapWithoutPlaybook
		// alert raised before the policy was added, and dispatch the reasoning
		// episode through the normal lane-1 path (anti-storm mark + OCC + reclaim).
		e.issues.clear(issueKeyGapConfig(targetID, col))
		ga = esc
	}

	if ga.Action == actionSurface {
		// FR29: surface-only, never dispatch. No mark, no OCC, no episode —
		// just the entity's membership in this column's open-row set, and the
		// ONE Health-KV issue that set is reflected into (issueKeyGapOpen),
		// carrying the count. The fact is the target's open WORKLOAD for the
		// column, identical in kind for every row holding it, so it is one
		// target-scoped entry rather than N per-row ones: the per-row budget
		// bounds FAULTS, and a backlog of open business work is not a fault.
		// Row identity stays where it is authoritative and complete — the
		// projected row set this gap is computed from.
		//
		// Only a membership TRANSITION writes: a CDC redelivery of an
		// already-open row leaves the entry untouched, count and `since` alike.
		// The retirements are surfaceStats' three removal legs (its doc
		// comment); clearClosedMarks below reaches the first of them when this
		// row stops naming the column.
		//
		// A message whose value changes is safe on this seam and only on this
		// seam: bare issues.set touches neither standingAs (which ignores
		// messages) nor pacedRaise (which would read a changing count as a
		// fresh arrival). Routing this raise through either would mean moving
		// the count to a metric.
		sev := ga.IssueSeverity
		if sev == "" {
			sev = "warning"
		}
		code := ga.IssueCode
		if code == "" {
			code = "Surface"
		}
		if reflection, changed := e.surface.add(targetID, col, entityID, code, sev); changed {
			e.reflectSurface(targetID, reflection)
		}
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
	// the `!leaseLive` requirement here: the mark's whole lease must have
	// expired first (production default 30 minutes), which is orders of
	// magnitude longer than a bridge turnaround. It is a window, not an
	// impossibility — tests that shorten the mark lease have to keep it well
	// clear of that turnaround (see asyncConvergeOpts in
	// internal/leaseconvergence).
	stale := found && !leaseLive(rec.LeaseExpiresAt, time.Now()) && e.staleMark(targetID, entityID, row, col, ga)

	// A live mark means an episode for THIS gap is genuinely in flight, and the
	// republish set is the one thing that can still be owed against it: whether
	// that episode's op actually reached the Processor. Nothing else this
	// delivery could do for the gap is owed, so the two arms are the whole
	// disposition.
	if found && !stale {
		if !e.republish.owes(targetID, entityID, col) {
			// The anti-storm drop. Taken here rather than after planning,
			// because a row that keeps being redelivered — for its own sake or
			// for a sibling gap's config-error floor — would otherwise burn one
			// admission token and re-publish one op per cycle, forever, for a
			// gap whose episode is already running.
			//
			// Nothing owed is skipped by returning above planGap. planGap's
			// three retires all describe facts a LIVE mark has already
			// disproved: the mark exists only because an earlier pass built and
			// fired a plan for this row and gap, which cleared the config and
			// template entries then; and the gap's own GapBudgetExhausted entry,
			// if one stands, is retired by the episode's completion closing the
			// column (retireClosedGapIssues) or by the next delivery once the
			// mark clears. releaseCompletedLeg runs above this, so a goal leg
			// that finished is released rather than mistaken for an in-flight
			// one.
			return substrate.Ack
		}
		// This episode's op publish FAILED and has not been made good since, so
		// this redelivery — the one that failure asked for — re-publishes it.
		// Falling through re-plans against the mark's pinned action and reaches
		// fireEpisode's in-flight arm, which fires with the mark's preserved
		// claimId: same requestId, so a publish that was in fact ambiguous
		// (op accepted, ack lost) collapses on the Contract #4 tracker rather
		// than minting a second episode.
	}

	pl, action, dec := e.planGap(ctx, target, targetID, entityID, col, ga, row, msg.Sequence, pinnedAction)
	if pl == nil {
		return dec
	}

	return e.fireEpisode(ctx, targetID, entityID, entityKey, col, action, pl, rec, markRev, found, stale)
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
	return !e.boolColumn(targetID, entityID, row, inflightColumnPrefix+g)
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
// GapBudgetExhausted — "this row's remediation ran out" — for a gap that by
// contract never remediates; and merely reading the suppression verdict for one
// makes a stranded count look like a spent budget, which SKIPS the column and
// switches its diagnostic off.
//
// The four sites are handleRow's suppression gate, escalateExhaustedGap, the
// sweep's reclaim, and the count leg's re-arm. handleRow and reclaim each need a
// guard of their own because each declines something the escalation never sees:
// handleRow SKIPS the column outright on a spent budget, and the open-row
// membership dispatchGap would have recorded — the count on the column's
// gapOpen: entry — is what that skip costs; reclaim would re-dispatch a
// stranded mark. With both guarded, the escalation's only
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
// failure by WHERE ITS FIX CAN COME FROM: an unresolved reference is transient
// mid-convergence and defers on the short redelivery cadence; a template fault
// and an un-dispatchable action both have a fix path that projects no new row
// (an edit to the template, the playbook, or the package), so redelivery is
// their only automatic uptake and they defer on the LONG floor instead. None of
// the three is skipped-and-forgotten: pl == nil means do not dispatch on THIS
// pass, and the returned Decision is the caller's disposition.
//
// All three raise at `warning`. A per-row DATA error is one bad row while every
// other row still remediates; a CONFIG error affects every row of the target but
// is now a STANDING fact, re-derived for as long as it holds, and
// aggregateStatus maps any `error` over the whole issue set to `unhealthy` — so
// an `error` here would report "cannot fulfil its primary responsibility"
// (Contract #5 §5.2) for a Weaver dispatching normally for every other target.
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
		var pl *plan
		if pl, perr = buildPlan(e.source, targetID, entityID, col, resolved, row, rowRevision); perr == nil {
			// A plan that BUILT disproves THREE standing facts, whether or not
			// admission lets it fire on this pass: the playbook resolves (the
			// config issue), this entity's gap has an attempt left after all (its
			// GapBudgetExhausted, which a raised maxretries_<g> or an escalation
			// can make stale without the gap ever having closed), and this row's
			// template references resolve (the template issue — building the plan
			// IS the resolution). The build is what settles all three, so the
			// retires belong above the admission gate, not below it.
			//
			// The gap column's own `data:` entry is deliberately NOT cleared here:
			// a built plan says nothing about whether missing_<g> carries a §10.2
			// bool, and that entry is raised and retired by boolColumn's own read.
			e.issues.clear(issueKeyGapConfig(targetID, col))
			e.issues.clear(issueKeyGapEntity(targetID, entityID, col))
			e.issues.clear(issueKeyTemplateEntity(targetID, entityID, col))

			// Fire 8 admission control (design §3.4): a declared budget has no
			// spare capacity for this gap right now. No mark, no episode, no
			// issue — this is ordinary pacing, not a fault; the redelivery
			// cadence is the retry, exactly like an unresolved-reference defer.
			//
			// The gate sits BELOW the build, not above it, and both halves of that
			// ordering matter. A token is a permit to DISPATCH, so it must only be
			// drawn for a plan that would actually fire — above the build, a row
			// whose plan can never build (a template fault, an un-dispatchable
			// action) would draw one on every one of its redeliveries for as long
			// as the fault stood. And a deferral is the 5 s transient class, so
			// deciding it above the build would hand that class to a CONFIG-error
			// row whose own class is the long floor, collapsing a paced target's
			// stuck population onto a 5 s loop.
			if !e.admitGap(target, targetID, entityID, col, resolved.Adapter, row) {
				e.logger.Debug("weaver: gap dispatch deferred by admission control",
					"targetId", targetID, "entityId", entityID, "gap", col)
				return nil, "", substrate.NakWithDelay
			}
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
		// The fault is template × row: a template reference that resolves null
		// against THIS row. One of its two fix paths — an edit to the action
		// template or the playbook — projects no new row, so the row would
		// never be re-evaluated after an Ack. The long floor covers both paths
		// (a corrected projection supersedes the pending revision and delivers
		// on its own; a corrected template is picked up by the next
		// redelivery).
		e.alertPaced(issueKeyTemplateEntity(targetID, entityID, col), "warning", "TemplateDataError",
			"target "+targetID+" entity "+entityID+" gap "+col+": "+perr.msg)
		return nil, "", substrate.NakWithLongDelay
	default:
		// A CONFIG error — an action the deployment cannot dispatch. Only a
		// package re-author fixes it and that produces no new row delivery, so
		// the long redelivery floor is the automatic uptake path, exactly as
		// for a gap with no playbook entry. `warning` for the same reason:
		// a standing `error` would pin the whole component `unhealthy`
		// (aggregateStatus) over one target's authoring fault while every
		// other target dispatches normally.
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
// here never see two different mark states). stale (staleMark) reclaims the
// mark in place — see that branch. The reconciler sweep's OWN reclaim does not
// pass through here for its lease-expiry case: it replaces the expired mark in
// place under a revision condition and fires directly, independently. action is
// recorded on the mark (the §10.3 value shape) so a later reclaim can
// re-dispatch the right episode.
//
// A live mark (found && !stale) reaches here only from dispatchGap, and only
// when the republish set says this gap's last publish FAILED — the anti-storm
// drop for every other live-mark delivery is taken there, above planning. This
// arm is therefore the re-publish and nothing else. Every other caller passes
// found=false, so none of them can reach it.
func (e *Engine) fireEpisode(ctx context.Context, targetID, entityID, entityKey, col, action string,
	pl *plan, rec *mark, markRev uint64, found, stale bool) substrate.Decision {

	if found && !stale {
		// Re-publish the same episode with the existing mark's preserved
		// claimId, so the userTask identity — and the derived requestId — stay
		// stable and the Processor collapses the duplicate.
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

// fire materializes one episode's op and fire-and-forget publishes it, and it
// is the SOLE writer of the republish set — the record of which gaps owe a
// re-publish.
//
// A primary-op publish failure Naks, which asks for the row back immediately,
// and records the obligation. On that redelivery dispatchGap finds the mark
// present with a live lease and would take the anti-storm drop; the recorded
// obligation is what makes it re-publish this same episode instead, with the
// mark's preserved claimId so the requestId is unchanged and the duplicate
// collapses on the Contract #4 tracker. A successful publish retires the
// obligation at the same key. If the obligation cannot be recorded (the target
// is at its cap) the key simply degrades to the restart behaviour — the sweep's
// lease-expiry reclaim re-derives the same episode identity from the mark.
//
// The op's requestId is episode-scoped (markRevision) UNLESS the plan overrides
// it (pl.requestID — Fire 2b's proposal-scoped dispatch); claimId is the
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
// flip delay. For the same reason it records no obligation: the republish set
// exists to re-fire an episode whose op was lost, and this episode's op landed.
func (e *Engine) fire(ctx context.Context, targetID, entityID, col string, markRevision uint64, claimID string, pl *plan) substrate.Decision {
	requestID := deriveEpisodeRequestID(targetID, entityID, col, markRevision)
	if pl.requestID != nil {
		requestID = pl.requestID(claimID)
	}
	if err := e.act.submit(ctx, requestID, pl.operationType, pl.class, pl.payload(claimID), pl.authTarget, pl.reads, planOptionalReads(pl, claimID), pl.enumerations); err != nil {
		if !e.republish.add(targetID, entityID, col) {
			e.logger.Warn("weaver: republish obligations at their per-target cap; this lost publish falls back to the reclaim ladder",
				"targetId", targetID, "entityId", entityID, "gap", col, "cap", republishCapPerTarget)
		}
		e.logger.Error("weaver: op publish failed; nak for retry",
			"targetId", targetID, "entityId", entityID, "gap", col, "requestId", requestID, "err", err)
		return substrate.Nak
	}
	e.republish.clear(targetID, entityID, col)
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
		// from an empty body. The prefix is also what retires the synthetic
		// rowBodyColumn entry: an entity whose last projected body was
		// unparseable can end by deletion instead of by a parseable revision,
		// and the tombstone's empty body needs no parsing to get here.
		//
		// The `gap:` family is retired by the same prefix and for a stronger
		// reason: its entries — a spent retry budget, an escalation to the
		// reasoning tier — are raised at whatever openGapColumns enumerated,
		// which is every true missing_* column WHETHER OR NOT the playbook names
		// it, while the candidate walk below yields only the playbook's keys
		// unioned with the (now empty) row's. A gap entry raised at an orphan
		// column is therefore reachable by no per-key clear once the entity is
		// gone, and the prefix is its only retirement.
		e.issues.clearPrefix(issuePrefixGapEntity + targetID + "." + entityID + ".")
		e.issues.clearPrefix(issuePrefixData + targetID + "." + entityID + ".")
		e.issues.clearPrefix(issuePrefixTemplate + targetID + "." + entityID + ".")
		// A `surface` gap's open-row membership has the identical orphan-column
		// hazard and the same answer in memory: the entity is dropped from EVERY
		// column set of the target, not just from the ones the walk below will
		// name, because a column the playbook has since dropped never yields
		// from an empty body and the membership would leak with the entity gone
		// and no leg able to reach it. Each changed column's entry is rewritten
		// with the smaller count, or retired if the deleted row was its last.
		for _, reflection := range e.surface.removeEntity(targetID, entityID) {
			e.reflectSurface(targetID, reflection)
		}
	}
	anyOpen := false
	for _, col := range markCandidateColumns(target, row) {
		// readable answers only the config-latch retirement below: a tombstone
		// row says the whole entity is gone, and a row that carries the column
		// as an explicit bool or drops it entirely has stated its value, but a
		// PRESENT non-bool has said nothing this pass can act on.
		open, readable := false, true
		if row != nil {
			open, readable = e.boolColumnRead(targetID, entityID, row, col)
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
		//
		// It is also the only retirement here that turns on the column having
		// genuinely CLOSED rather than merely not reading true, and the only
		// one whose key is shared with every other entity of the target. An
		// unreadable column (present, non-bool) is not evidence of closure, and
		// retiring a target-scoped fact on it would let one repeatedly
		// re-projecting broken row clear the latch at its projection rate,
		// re-stamping the `since` of a config fault every other row is still
		// raising. The per-entity retirements below are unaffected: they are
		// this row's own facts, and a row that cannot state the column has no
		// use for them either way.
		e.retireClosedGapIssues(targetID, entityID, col)
		if readable {
			e.issues.clear(issueKeyGapConfig(targetID, col))
		}
		if ga, isSurface := target.Gaps[col]; isSurface && ga.Action == actionSurface {
			// A surface gap never creates a mark (dispatchGap returns before
			// e.marks.get) — nothing further to clear for this column. Its one
			// piece of state, the entity's membership in the column's open-row
			// set, was retired by retireClosedGapIssues above.
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
		// The gap ended, so nothing is owed against its episode any more: retire
		// any republish obligation with the mark it was owed against. Runs on the
		// same not-currently-true condition as the mark and count deletes, so it
		// covers a gap that closed, a column the row dropped, and a deleted
		// entity alike. It cannot race the `fire` that ADDS one: this pass runs
		// in handleRow's preamble and only names columns the row is NOT reporting
		// open, while the dispatch leg below only fires columns it IS.
		e.republish.clear(targetID, entityID, col)
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
// plan-side facts above, the gap's own entity-scoped latch (a spent retry
// budget, a budget spent and escalated to the reasoning tier), and this entity's
// membership in the column's `surface` open-row set — which rewrites that
// column's one gapOpen: entry with the smaller count, or retires it when this
// was the last open row.
//
// The membership remove is a no-op for every non-surface gap, which is why it
// rides here rather than behind an action test: the sweep legs that reach this
// helper hold a mark or a dispatch-count and a `surface` gap has neither, so
// they can never carry one, and the one leg that can — clearClosedMarks'
// candidate walk — reaches it for the column that actually closed.
//
// The split from retireGapPlanIssues is the difference between "this gap ended"
// and "this gap is no longer dispatchable", and it is load-bearing. A gap the
// playbook dropped is undispatchable but may still read TRUE in the row, and
// lane-1 goes on raising at issueKeyGapEntity for exactly that column —
// openGapColumns enumerates every true missing_* whether the playbook names it
// or not, and escalateExhaustedGap raises there on both its arms (an
// un-escalated budget, and an escalation dispatched). Clearing
// that latch from a leg that only knows the gap left the PLAYBOOK makes it flap:
// the next delivery re-raises, the arrival test sees an empty latch and logs a
// spurious Error, and the fresh stamp destroys the age of a fact that never
// stopped holding. Only a leg that observed the column itself go false — or the
// row cease to exist — may retire it.
func (e *Engine) retireClosedGapIssues(targetID, entityID, gapColumn string) {
	e.issues.clear(issueKeyGapEntity(targetID, entityID, gapColumn))
	if reflection, changed := e.surface.remove(targetID, gapColumn, entityID); changed {
		e.reflectSurface(targetID, reflection)
	}
	e.retireGapPlanIssues(targetID, entityID, gapColumn)
}

// reflectSurface writes a `surface` gap column's one Health entry from the
// membership surfaceStats just changed: the count while rows are still open, and
// the entry's retirement once the last one closes. It is the SINGLE writer of a
// gapOpen: key, so the number on the board is always the size of the set behind
// it — an entry written only on the raise would sit at its high-water mark while
// the backlog drained.
//
// setLocked preserves an existing `since`, so a rewrite restates the count
// without disturbing the entry's age; the retirement deletes it, so a column
// that reopens after emptying arrives with a fresh stamp. That is what makes the
// entry's `since` read as "when this column last went from no open rows to
// some".
func (e *Engine) reflectSurface(targetID string, reflection surfaceReflection) {
	key := issueKeyGapOpen(targetID, reflection.column)
	if reflection.count == 0 {
		e.issues.clear(key)
		return
	}
	e.issues.set(key, reflection.severity, reflection.code,
		"target "+targetID+": "+strconv.Itoa(reflection.count)+" rows have column "+reflection.column+" true")
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
func (e *Engine) boolColumn(targetID, entityID string, row map[string]any, col string) bool {
	value, _ := e.boolColumnRead(targetID, entityID, row, col)
	return value
}

// boolColumnRead is boolColumn's full result: the column's bool value, plus
// whether that value is READABLE — whether the row actually said something
// about the column.
//
// Readable covers the two §10.2-conformant shapes and only those. An explicit
// bool speaks for itself. An ABSENT (or null) column is the §10.2 retraction
// shape and reads as a closed column — the whole basis on which clearClosedMarks
// treats a column the row stopped reporting as a gap that ended, tombstone
// included. A PRESENT value of any other type is neither: the false returned
// with it is a conservative default, not a fact about the row, so a caller whose
// decision turns on the column genuinely being false must not act on it.
//
// One caller needs the distinction — clearClosedMarks' retirement of the
// TARGET-scoped config latch, which an unreadable column would otherwise retire
// on every projection of a broken row. Every other caller wants the
// conservative false and takes boolColumn.
func (e *Engine) boolColumnRead(targetID, entityID string, row map[string]any, col string) (value, readable bool) {
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
	if e.boolColumn(targetID, entityID, row, inflightColumnPrefix+g) {
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
		// wrong for it, and the reason is the DISPATCH, not a key collision. The
		// un-escalated arm would raise GapBudgetExhausted — "this row's
		// remediation ran out" — for a gap that by contract has no remediation
		// and never attempted one, which is a fabricated fault an operator would
		// go looking for. The escalated arm would additionally fire a real Augur
		// reasoning episode for a gap whose whole contract is that Weaver never
		// dispatches for it.
		// Ack: nothing to do, and nothing to redeliver for.
		//
		// The column's own standing fact — the count at its gapOpen: entry — is
		// lane-1's and sits at a different key, so neither arm could reach it;
		// this guard is not what protects it. What it protects is the honesty of
		// the two exhaustion codes and the never-dispatch promise.
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
	// Retire a GapBudgetExhausted raised for this row BEFORE the augur policy
	// covered the gap — that fact is superseded the moment an escalation is the
	// disposition. The guard is what keeps the clear to that one job: this
	// function's own escalation record lives at the same latch, and clearing
	// unconditionally would wipe it on every derivation that short-circuits below
	// on a live mark, leaving a "standing" record that never stands for longer
	// than one delivery.
	if !e.issues.standingAs(issueKeyGapEntity(targetID, entityID, gapColumn), "warning", codeGapEscalatedToAugur) {
		e.issues.clear(issueKeyGapEntity(targetID, entityID, gapColumn))
	}

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
	//     This arm is ALSO what bounds the cost of re-deriving the same
	//     exhaustion: a decline-floor redelivery, a sweep pass and an operator
	//     replay all reach this function, and every one of them that finds a
	//     live mark costs one mark read and no model call.
	//   - A STALE mark (expired lease) or none at all belongs to the
	//     original action's now-spent retry lineage (or a prior escalation
	//     attempt that never completed) — clear it, revision-conditioned on
	//     the read just taken so a genuinely concurrent fresh episode is
	//     never clobbered, then fire fresh. This is the ONLY recovery a dead
	//     escalation episode has: the reasoning claim's own Loom instance or
	//     bridge call can be lost, and nothing else re-derives it, so this arm
	//     must stay reachable from every derivation. Its re-fire rate is one per
	//     mark lease per gap, because each re-fire mints a fresh live mark —
	//     which is what keeps it from becoming a per-pass model call.
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
	dec = e.fireEpisode(ctx, targetID, entityID, entityKey, gapColumn, actionRef, pl, nil, 0, false, false)
	if dec != substrate.Ack {
		// A publish failure inside `fire` records a republish obligation at this
		// gap's key, and for an escalation that obligation is both useless and
		// unsafe, so it is withdrawn here rather than left to age out.
		//
		// Useless: handleRow routes an exhausted gap through THIS function and
		// never reaches dispatchGap, which is the set's only reader — so the
		// entry can never be consulted while the gap stays exhausted, and it
		// occupies one of the target's 256 slots meanwhile.
		//
		// Unsafe: it does not stay unreachable. An operator `resetBudget`, or a
		// lens raising maxretries_<g>, makes the gap dispatchable again while the
		// escalation's own mark is still present — and dispatchGap's live-mark
		// arm falls through on `owes`, re-planning against that mark's pinned
		// actionRef and re-publishing an episode nothing asked for. Beyond the
		// Contract #4 tracker's 24 h horizon that is a genuine second dispatch.
		//
		// Nothing is lost by withdrawing it: this episode's retry is the
		// lease-expiry re-fire above, which is reached by every derivation of the
		// exhaustion, not the republish set.
		e.republish.clear(targetID, entityID, gapColumn)
		return dec
	}
	// The escalation is now a standing FACT about this gap, recorded as one so an
	// operator can see which gaps are on the reasoning tier and how long they
	// have been there. It replaces, at the same latch, the GapBudgetExhausted the
	// un-escalated branch raises: both state what a spent budget led to for this
	// one row, they are mutually exclusive, and sharing the latch is what puts
	// them on the same retirement machinery.
	//
	// It is a RECORD, not a gate. Nothing reads it back to decide whether to
	// escalate: what bounds the re-escalation of one gap is the mark lease read
	// above — a live mark Acks, an expired one re-fires — and a latch consulted
	// as a gate there would suppress the expired-mark arm too, which is the only
	// recovery a dead reasoning episode has.
	//
	// The raise sits BELOW planGap, which clears this latch for any gap whose
	// plan builds — including this escalation's own. Written above the plan it
	// would be erased on the way out and record nothing.
	//
	// `warning`, not `error`: an escalated gap is one Weaver could not remediate
	// conventionally and has handed to the reasoning tier, which is degraded
	// service for that row, not a Weaver that cannot fulfil its responsibility
	// (Contract #5 §5.2) — every other row still remediates. It is set rather
	// than alerted because the dispatch this line follows is already logged, and
	// alertStanding's arrival level is Error, which no successful escalation
	// deserves.
	e.issues.set(issueKeyGapEntity(targetID, entityID, gapColumn), "warning", codeGapEscalatedToAugur,
		"target "+targetID+" entity "+entityID+": row column "+gapColumn+" exhausted its retry budget and was "+
			"escalated to Augur reasoning")
	return substrate.Ack
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
		if e.boolColumn(targetID, entityID, row, col) {
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
//
// A raise the per-row budget REFUSES is the case the arrival test cannot serve,
// and it inverts the seam if left to it: nothing tracks the key, so standingAs
// says "not standing" on every pass and the raise is Error once a sweep pass for
// as long as the target sits at its cap — the flood this seam exists to prevent,
// arriving exactly when the target is worst off. A refused raise is paced on the
// cache's refusal clock instead: loud once per (target, family) per
// logPaceInterval, Debug in between. The arrival is still heard; the flood ends.
func (e *Engine) alertStanding(key, severity, code, message string) {
	standing := e.issues.standingAs(key, severity, code)
	refused := e.issues.set(key, severity, code, message)
	switch {
	case refused:
		if e.issues.refusedLoud(key, e.now()) {
			e.logger.Error("weaver: " + message)
		} else {
			e.logger.Debug("weaver: " + message)
		}
	case standing:
		e.logger.Debug("weaver: " + message)
	default:
		e.logger.Error("weaver: " + message)
	}
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
	loud, arrivedAt := e.issues.pacedRaise(key, severity, code, e.now())
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

// The issue-key family prefixes. Every key constructor below and every
// teardown prefix in issueKeyTargetPrefixes is built from these, so a key shape
// and the prefix that retires it cannot drift apart.
const (
	issuePrefixGapEntity = "gap:"
	issuePrefixGapConfig = "gapConfig:"
	issuePrefixGapOpen   = "gapOpen:"
	issuePrefixData      = "data:"
	issuePrefixTemplate  = "template:"
	issuePrefixSweep     = "sweep:"
	issuePrefixConsumer  = "consumer:"
	issuePrefixTarget    = "target:"
	issuePrefixTimer     = "timer:"
	issuePrefixPending   = "pendingSpec:"
	issuePrefixOscillate = "oscillation:"
)

// codeGapEscalatedToAugur is the issueKeyGapEntity code recording that one
// row's gap spent its retry budget and was handed to the Augur reasoning tier —
// the operator-facing counterpart of GapBudgetExhausted, which the same latch
// carries when no augur policy takes the gap.
//
// It is a record and nothing gates on it. What bounds a gap's re-escalation is
// the escalation mark's own lease (escalateExhaustedGap's live/expired arms);
// gating on this latch instead would suppress the expired-mark arm, which is the
// only recovery a reasoning episode that died has.
const codeGapEscalatedToAugur = "GapEscalatedToAugur"

// perEntityIssuePrefixes are the issue families keyed BELOW the target, one
// entry per (entity, column) or per mark: their population is the target's row
// count, not the number of targets. listingRank ranks them last, behind the
// target-scoped families that explain a fault rather than count it.
var perEntityIssuePrefixes = []string{
	issuePrefixGapEntity,
	issuePrefixData,
	issuePrefixTemplate,
	issuePrefixSweep,
}

// targetScopedIssuePrefixes are the families raised once per target, spec or
// timer. They are what an operator needs in front of them when a target breaks,
// and together with perEntityIssuePrefixes they must account for every family
// this engine raises — TestListingRank_EveryIssueFamilyIsClassified pins that.
var targetScopedIssuePrefixes = []string{
	issuePrefixGapConfig,
	issuePrefixGapOpen,
	issuePrefixConsumer,
	issuePrefixTarget,
	issuePrefixTimer,
	issuePrefixPending,
	issuePrefixOscillate,
}

// issueKeyTargetPrefixes lists the per-target issue-key families that a target
// teardown must retire wholesale (Engine.Revoke) — the gap-entity, gap-config,
// gap-open, row-data, template and sweep families. These are the families whose
// keys carry a segment below the target — a per-entity or per-gap one — so a
// teardown has no single key to name and must clear by prefix; the families
// keyed by targetID alone (consumer, timer, owner) are cleared by key at the
// same site. Each prefix ends in the "." separator, which is what keeps "t1."
// from matching a key under "t10."
//
// Every family whose entries a revoke would otherwise strand belongs here: a
// revoked target delivers no rows and keeps no marks, so each of these has no
// live path left that could ever retire it. The effect family is the one
// deliberate omission — it needs no prefix clear, because flagEffectMismatches
// reconciles its alert set against every heartbeat's scan and Revoke deletes
// the target's `__effect` windows, so those entries self-clear on the next
// heartbeat.
//
// `sweep:` is here for a second reason on top of the first. Its entries would in
// fact self-retire — the sweep clears a CorruptMark once the key stops being
// listed, and a revoke deletes every key under the target's prefix — but that
// retirement waits for the next sweep pass, and the family is counted against the
// target's per-row budget (rowIssueTarget). A budget slot released only on a
// later pass makes the cap lag the teardown; released here it does not. One
// residue is named rather than left to be rediscovered: the sweeper's own
// `corruptAlerted` set still holds those keys after this clear, so its later
// listing-based retirement clears keys the cache no longer has. That is a no-op
// on an absent key, and the entry is dropped from the set by the same pass, so it
// self-heals within one cadence and can never resurrect an issue.
func issueKeyTargetPrefixes(targetID string) []string {
	return []string{
		issuePrefixGapEntity + targetID + ".",
		issuePrefixGapConfig + targetID + ".",
		issuePrefixGapOpen + targetID + ".",
		issuePrefixData + targetID + ".",
		issuePrefixTemplate + targetID + ".",
		issuePrefixSweep + targetID + ".",
	}
}

// issueKeyGapEntity keys a Health issue whose raised fact is about ONE ROW's
// gap: this entity's gap having spent its retry budget, or that spent budget
// having been escalated to the reasoning tier. Two entities exhausting the same
// (target, gap) are two independent facts, each raised and retired on its own
// subject's timeline, so the key carries the entity segment — mirroring
// issueKeyEffect's three-segment shape.
//
// A `surface` gap raises at issueKeyGapOpen instead: its population is the
// target's open workload rather than a set of per-row faults, so it is one
// counted entry per (target, column) and outside this family's budget.
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

// issueKeyGapOpen keys the one Health issue a `surface` gap raises: the count of
// rows this instance observes holding one (target, gap column) open. The fact is
// the target's open WORKLOAD for that column — a backlog of business work, whose
// population is supposed to be large on a healthy system — not a fault in any
// one row, so it is raised once per (target, gap) with the row identities held
// in surfaceStats rather than fanned out one issue per row.
//
// The key is target-scoped on purpose, and that is what keeps the workload
// population OUT of the per-row budget: rowIssueTarget's family test names the
// four per-row prefixes and this is not one of them — `gap:` does not
// prefix-match `gapOpen:` — so no gapOpen entry can consume one of the 500 slots
// the budget sizes for FAULTS. Without that, a healthy backlog of open rows
// fills the budget and every fault raised afterwards for the target is refused.
// The family test, not the separator count, is what decides, so the property
// holds even for a gap column carrying a `.` (which install-time validation
// rejects anyway — a gaps key is a dot-free `missing_<gap>` token).
func issueKeyGapOpen(targetID, col string) string {
	return issuePrefixGapOpen + targetID + "." + col
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
// One member of the family is keyed at a SYNTHETIC column: rowBodyColumn, whose
// fact is "this row's body does not parse at all". It has no column reader, so
// its retirement is not a read — the successful json.Unmarshal of a later
// revision of the same row clears it at the raise site, and the two prefix
// teardowns cover a subject that leaves (the entity's deletion tombstone, whose
// empty body needs no parsing, and the target's Revoke or registry removal).
//
// Two exits from handleRow retire nothing else, and neither strands an entry. A
// row whose body does not parse returns before the reconcile: that is unreadable
// evidence, not evidence of repair (the sweep takes the same posture on the same
// input), and every retirement above stays reachable by the next delivery that
// does parse — including the deletion tombstone. A row for an unregistered
// target returns at the registry miss, and its target's whole issue set is
// retired wholesale by the reconcileConsumers teardown that made the
// registration go away.
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
	return "effect:" + targetID + "." + gapColumn + "." + actionRef
}

// rowIssuesCappedSegment is the trailing segment of the per-target overflow
// entry the issue cache maintains once a target's per-ROW issue set reaches
// rowIssueCapPerTarget. It cannot be mistaken for a member of that family by
// SHAPE: a per-row key is `data:<targetId>.<entityId>.<column>` and this one is
// a segment short of it, which is exactly the test rowIssueTarget applies. The
// `__` prefix is the same engine-synthetic reading convention as
// rowBodyColumn's — legible, not load-bearing.
const rowIssuesCappedSegment = "__capped"

// issueKeyRowIssuesCapped keys that overflow entry. It deliberately sits inside
// the `data:` family so the two teardowns that retire a target's per-row issues
// wholesale — Revoke and the reconcileConsumers removal, both walking
// issueKeyTargetPrefixes — carry it away with them; and it deliberately sits
// ABOVE the entity segment, because the fact is the target's, not any one row's,
// so the per-entity tombstone clear must not retire it.
func issueKeyRowIssuesCapped(targetID string) string {
	return issuePrefixData + targetID + "." + rowIssuesCappedSegment
}

// rowIssueTarget reports whether key is a member of a PER-ROW issue family and
// if so which target's budget it counts against. It is rowIssueFamily with the
// family discarded, for the callers that need only the budget's owner; the two
// share one switch so a family's membership cannot be answered two ways.
func rowIssueTarget(key string) (string, bool) {
	target, _, ok := rowIssueFamily(key)
	return target, ok
}

// rowIssueFamily reports whether key is a member of a PER-ROW issue family — a
// `gap:`, `data:` or `template:` entry keyed per (target, entity, column) — and
// if so which target's budget it counts against and which family it belongs to,
// named by that family's own prefix constant.
//
// The family is the second key the issue cache's refusal accounting uses. A
// raise the cap turns away is counted and log-paced per (target, family), so a
// `data:` flood that exhausts a target cannot silence the arrival of the first
// refused `template:` fault behind it, and the overflow entry can say which
// families the refusals came from — the two populations have different
// re-derivation cadences, and an operator reading one number cannot tell them
// apart.
//
// The test is the key SHAPE, not a list of raise sites: every member carries an
// entity segment and a column segment below the target, so a key whose family
// tail has fewer than two separators is not one. That is what excludes the
// overflow entry itself (target + reserved segment only) without special-casing
// it, and what keeps a future target-scoped member of any family from silently
// consuming a per-row budget.
//
// `gap:` belongs here for the same reason the other two do, and its omission
// would leave the bound describing less than it names. Both members of that
// family — a spent retry budget, a budget spent and escalated — are one fact
// about one (entity, column) and multiply with the lens exactly like a row-data
// fault, so a systemically-broken target grows the in-memory map and the
// per-heartbeat sort over it without limit. Capping it
// costs an operator nothing they could otherwise see: the heartbeat DOCUMENT is
// already truncated to maxHeartbeatIssues severity-first, far below this cap,
// and a refused raise still carries its severity into the overflow entry, so
// aggregateStatus reaches the same verdict either way.
//
// `sweep:` belongs here too, and its membership is decided by the same test even
// though its key arrives differently: issueKeySweep is handed a weaver-state key
// whole rather than a (target, entity, column) triple, so the entity segment is
// EMBEDDED rather than passed. Two of the three shapes that reach it — a mark and
// its `…__count` retry budget — are per-(entity, column) and multiply with the
// subject count; the third, a `<targetId>.__effect.<gapColumn>.<actionRef>`
// window, is not, and is counted anyway. That over-count is deliberate and is the
// safe direction: it is bounded by the playbook (gap columns × action refs), it
// consumes slots from the RIGHT target, and refusing to count a family because
// one of its shapes is bounded is how an unbounded shape rides in beside it.
//
// The target segment is always the FIRST segment, for every shape, which is what
// makes the arithmetic below safe on a key that by definition failed validation:
// every writer to weaver-state builds its key as targetID + "." + …, and a
// registered targetId is a single token with no dot (singleTokenPattern). A key
// that does not fit — an empty first segment, a tail with no second separator —
// falls out as not-a-per-row-key and is left uncounted, which costs only the
// bound on that one entry. There is no input that attributes an entry to a
// target other than the one whose prefix it carries.
func rowIssueFamily(key string) (target, family string, ok bool) {
	tail := ""
	switch {
	case strings.HasPrefix(key, issuePrefixGapEntity):
		tail, family = key[len(issuePrefixGapEntity):], issuePrefixGapEntity
	case strings.HasPrefix(key, issuePrefixData):
		tail, family = key[len(issuePrefixData):], issuePrefixData
	case strings.HasPrefix(key, issuePrefixTemplate):
		tail, family = key[len(issuePrefixTemplate):], issuePrefixTemplate
	case strings.HasPrefix(key, issuePrefixSweep):
		tail, family = key[len(issuePrefixSweep):], issuePrefixSweep
	default:
		return "", "", false
	}
	targetID, rest, cut := strings.Cut(tail, ".")
	if !cut || targetID == "" || !strings.Contains(rest, ".") {
		return "", "", false
	}
	return targetID, family, true
}
