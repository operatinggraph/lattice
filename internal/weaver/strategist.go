package weaver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/weaver/planner"
)

// Action names of the Contract #10 §10.8 action table.
const (
	actionTriggerLoom = "triggerLoom"
	actionAssignTask  = "assignTask"
	actionDirectOp    = "directOp"
	// actionProposedOp is the Fire 2b "Augur dispatch" action (Contract #10
	// §10.8): unlike the three static actions, its op + params are sourced from
	// the ROW — an approved Augur proposal's proposedAction/proposedParams (the
	// augur package's augurDispatchPending lens) — materialised into a GapAction
	// after a dispatch-time re-validation (buildProposedOpPlan). Reserved for the
	// augur package's primordial augurDispatch target.
	actionProposedOp = "proposedOp"
	// actionSurface is FR29's "surface, never dispatch" gap (Contract #10
	// §10.8): raises/clears a named Health-KV issue while the gap stays
	// open/closes, dispatching no op, creating no mark. Handled entirely in
	// evaluator.go's dispatchGap/clearClosedMarks, upstream of buildPlan — this
	// switch never sees it.
	actionSurface = "surface"
)

// Operation types the Actuator submits.
const (
	opStartLoomPattern       = "StartLoomPattern"
	opCreateTask             = "CreateTask"
	opMarkExpired            = "MarkExpired"
	opRecordProposalDispatch = "RecordProposalDispatch"
)

// assignTaskGrantTTL is the expiry horizon set on an assignTask grant. The
// human response window is unbounded by design; the grant outlives any
// realistic response, and the bound op's commit auto-completes the task.
const assignTaskGrantTTL = 30 * 24 * time.Hour

// rowTemplatePrefix marks a templated param value: row.<column> substitutes
// that column's value from the violation row (Contract #10 §10.8 Templating).
const rowTemplatePrefix = "row."

// errKind classifies a plan failure so the evaluator can route it: a config or
// data error is alerted and the gap skipped (redelivery cannot fix it); a
// transient error (a pattern/op reference the registry has not resolved) is
// surfaced to Health and retried on a delayed redelivery cadence — bounded
// cadence, unbounded count, so a reference that resolves later (replay lag, a
// pattern installed after the target) still recovers.
type errKind int

const (
	errConfig errKind = iota
	errData
	errTransient
)

// planError is a gap-planning failure with its routing class.
type planError struct {
	kind errKind
	msg  string
	// unplannable marks a failure planGap must retry through the target's
	// augur.escalate("unplannable") policy before falling through to this
	// error's ordinary disposition (Fire 6, R1: design
	// loftspace-lease-renewal-goal-authored-target-design.md §5 — "'no plan
	// derivable' flows into the existing unplannable trigger"). Set only by
	// resolveGoalAction: a fresh Synthesize returning planner.ErrNoPlan, or a
	// pinned leg ref the gap's Actions catalog no longer declares (which is
	// indistinguishable, without more bookkeeping, from a redelivered episode
	// that was itself already escalated to the augur — retrying the
	// escalation re-derives the identical deterministic dispatch either way).
	unplannable bool
}

func (e *planError) Error() string { return e.msg }

// plan is one gap's fully-resolved dispatch, pending only the per-open-episode
// claimId: payload(claimID) materializes the op payload at fire time. The
// userTask actions fold the claimId into a STABLE artifact identity (assignTask's
// taskId, triggerLoom's Loom instanceId) so re-dispatch of the same open gap
// collapses on the existing task/instance; the op's own requestId stays
// episode-scoped (the mark create/replace revision) and collapses a same-episode
// re-fire on the Contract #4 tracker. Idempotency rests on these derived ids, not
// payload equality (time-derived fields such as assignTask's expiresAt differ per
// fire).
type plan struct {
	operationType string
	authTarget    string
	payload       func(claimID string) map[string]any
	// reads is the dispatched op's ContextHint.Reads: the BARE vertex keys the
	// op's DDL script hydrates + validates (vertex_alive). The dispatcher
	// declares them because it builds the payload and so knows the exact keys
	// the op touches. Empty for read-free ops (StartLoomPattern, MarkExpired,
	// most directOps). NO `.state` suffixes — the DDLs read bare keys.
	reads []string
	// optionalReads, when non-nil, yields the dispatched op's
	// ContextHint.OptionalReads (Contract #2 §2.5 — declared absence-tolerant
	// reads) for the episode's claimID. A closure, like payload, because the
	// assignTask dedup key derives from the claimId-seeded stable taskId. Nil
	// for ops that read no absence-tolerant keys.
	optionalReads func(claimID string) []string
	// requestID, when non-nil, overrides the ordinary episode-scoped
	// deriveEpisodeRequestID derivation for this op (Fire 2b's proposedOp
	// dispatch: the proposed remediation's requestId must be PROPOSAL-scoped,
	// not episode/mark-scoped, so a sweep reclaim re-derives the identical id and
	// collapses on the Contract #4 tracker instead of double-applying — design
	// augur-dispatch-pickup §3.3). Nil for every ordinary gap (unchanged
	// behavior).
	requestID func(claimID string) string
	// followUp, when non-nil, is fired immediately after this op succeeds, in
	// the SAME dispatch (Fire 2b's two-op proposedOp dispatch: the proposed
	// remediation, then RecordProposalDispatch). A followUp publish failure does
	// NOT fail the primary dispatch — it only delays the flip, which self-heals
	// on the next reconciler sweep (design §3.4); only the primary op's failure
	// Naks for redelivery. followUp's own requestID/followUp fields are honored
	// (nested one level deep is all Fire 2b needs); its reads/authTarget are used
	// as normal.
	followUp *plan
}

// buildPlan resolves one open gap against its playbook entry: templated params
// are substituted from the row, and action-specific references (pattern → the
// live meta.loomPattern vertex; operation → the live op meta-vertex) resolve
// against the registry at dispatch time. expectedRevision is the candidate
// row's substrate per-key revision off the CDC message; every remediation op's
// payload carries it as the OCC revision-condition.
func buildPlan(source *targetSource, targetID, entityID, gapColumn string,
	ga GapAction, row map[string]any, expectedRevision uint64) (*plan, *planError) {

	switch ga.Action {
	case actionTriggerLoom:
		subject, perr := resolveStringParam("subject", ga.Subject, row)
		if perr != nil {
			return nil, perr
		}
		patternRef, perr := resolveStringParam("pattern", ga.Pattern, row)
		if perr != nil {
			return nil, perr
		}
		metaKey, ok := source.patternMetaKey(patternRef)
		if !ok {
			// The pattern meta-vertex may not have replayed yet (the CDC registry
			// is asynchronous) or the reference may never resolve (a typo, a
			// pattern not installed) — indistinguishable here, so the evaluator
			// retries on a delayed cadence and surfaces the condition to Health
			// until it resolves.
			return nil, &planError{kind: errTransient,
				msg: fmt.Sprintf("pattern %q has no loaded meta.loomPattern vertex", patternRef)}
		}
		return &plan{
			operationType: opStartLoomPattern,
			// Pattern-as-target (§10.8): Weaver holds StartLoomPattern @ scope: any
			// via the operator role, and per-pattern auth anchors on the pattern
			// definition vertex.
			authTarget: metaKey,
			payload: func(claimID string) map[string]any {
				return map[string]any{
					"patternRef":       metaKey,
					"subjectKey":       subject,
					"expectedRevision": expectedRevision,
					// A STABLE Loom instanceId (claimId-seeded, §10.3): every reclaim
					// re-supplies the same id, so the re-emitted loom.patternStarted
					// collapses on Loom's existing instance.<id> (no duplicate pattern,
					// hence no duplicate onboarding userTask). Absent claimId (a
					// pre-claimId mark mid-migration) yields a stable empty-seed id —
					// still consistent across that episode's reclaims.
					"instanceId": deriveStableInstanceID(targetID, entityID, gapColumn, claimID),
				}
			},
		}, nil

	case actionAssignTask:
		operation, perr := resolveStringParam("operation", ga.Operation, row)
		if perr != nil {
			return nil, perr
		}
		assignee, perr := resolveStringParam("assignee", ga.Assignee, row)
		if perr != nil {
			return nil, perr
		}
		taskTarget, perr := resolveStringParam("target", ga.Target, row)
		if perr != nil {
			return nil, perr
		}
		forOperation, ok := source.opMetaKey(operation)
		if !ok {
			return nil, &planError{kind: errTransient,
				msg: fmt.Sprintf("operation %q has no loaded op meta-vertex (forOperation unresolved)", operation)}
		}
		return &plan{
			operationType: opCreateTask,
			authTarget:    taskTarget,
			// The task DDL validates all three link endpoints with vertex_alive
			// (orchestration-base/ddls.go) — the caller MUST hydrate them. They are
			// the BARE keys (assignee/forOperation/scopedTo); the DDL reads no
			// `.state` aspect, so none is listed (a non-existent .state key would be
			// a HydrationMiss). Cross-checked against the script by
			// TestCreateTaskReads_MatchDDLScript.
			reads: []string{assignee, forOperation, taskTarget},
			// The two absence-tolerant kv.Read keys the CreateTask script
			// branches on (Contract #2 §2.5 optionalReads): the stable task key
			// (the cross-reclaim dedup read — absent → create, present+alive →
			// no-op, resolved at the step-4 snapshot with the lost race absorbed
			// by the Processor's CreateOnly-backstop retry) and the assignee's
			// `.availability` routing aspect (absent == available).
			optionalReads: func(claimID string) []string {
				return []string{
					"vtx.task." + deriveStableTaskID(targetID, entityID, gapColumn, claimID),
					assignee + ".availability",
				}
			},
			payload: func(claimID string) map[string]any {
				return map[string]any{
					"assignee":     assignee,
					"forOperation": forOperation,
					"scopedTo":     taskTarget,
					"expiresAt":    substrate.FormatTimestamp(time.Now().Add(assignTaskGrantTTL)),
					// A STABLE taskId (claimId-seeded, §10.3): every reclaim re-supplies
					// the same id, so a re-dispatched CreateTask collapses on the existing
					// task (the CreateTask script's kv.Read no-op + the CreateOnly
					// backstop) instead of spawning a duplicate per mark-lease expiry.
					"taskId":           deriveStableTaskID(targetID, entityID, gapColumn, claimID),
					"expectedRevision": expectedRevision,
				}
			},
		}, nil

	case actionDirectOp:
		if ga.Operation == "" {
			return nil, &planError{kind: errConfig, msg: "directOp requires an operation"}
		}
		if strings.HasPrefix(ga.Operation, rowTemplatePrefix) {
			return nil, &planError{kind: errConfig, msg: "directOp operation must be a literal operationType"}
		}
		authTarget := ""
		if ga.Target != "" {
			t, perr := resolveStringParam("target", ga.Target, row)
			if perr != nil {
				return nil, perr
			}
			authTarget = t
		}
		params := make(map[string]any, len(ga.Params)+1)
		for name, v := range ga.Params {
			resolved, perr := resolveParam(name, v, row)
			if perr != nil {
				return nil, perr
			}
			params[name] = resolved
		}
		params["expectedRevision"] = expectedRevision
		// The dispatched op's reads: each is a literal or a row.<column> template
		// (e.g. row.entityKey to hand the op its candidate vertex). The candidate
		// key is already in the lens row, so this just routes it into the op's
		// ContextHint.Reads so its DDL can hydrate + validate it.
		var reads []string
		for i, rt := range ga.Reads {
			r, perr := resolveStringParam(fmt.Sprintf("reads[%d]", i), rt, row)
			if perr != nil {
				return nil, perr
			}
			reads = append(reads, r)
		}
		return &plan{
			operationType: ga.Operation,
			authTarget:    authTarget,
			payload:       func(string) map[string]any { return params },
			reads:         reads,
		}, nil

	case actionProposedOp:
		// Fire 2b: the augurDispatch target's only gap. The op + params are NOT
		// playbook config (ga carries nothing) — they are sourced from the row
		// itself (an approved Augur proposal, augurDispatchPending lens) and
		// resolved through buildProposedOpPlan's own dispatch-time §5
		// re-validation + materialisation, then a recursive buildPlan call for the
		// resolved inner action (reusing this same live-registry resolution).
		return buildProposedOpPlan(source, entityID, row, expectedRevision)

	default:
		return nil, &planError{kind: errConfig, msg: fmt.Sprintf("unknown action %q", ga.Action)}
	}
}

// resolvePlannedAction resolves one gap's playbook entry to a concrete,
// dispatchable GapAction (design weaver-planner-mandate-design.md §3.3, Fire
// 5/6): the ONLY gaps this touches are a candidates-only or goal-only gap
// ("" Action) on a target in mode:"planned" — every other shape (an explicit
// Action, or a non-planned/absent/shadow mode) returns ga UNCHANGED, so those
// targets' dispatch stays byte-identical to every fire before this one.
// Alongside the resolved GapAction, it returns the actionRef the caller
// records on the mark and the `__effect`/oscillation bookkeeping: for every
// unchanged/candidates shape this is exactly the dispatch contract type
// (ga.Action / the picked candidate's Action, as before this fire) — a goal
// leg's ref is its OWN catalog Ref instead, decoupled from its dispatch
// contract type, because a goal catalog may (and the first real consumer
// does) declare multiple legs sharing one contract type (e.g. several
// assignTask legs to different assignees) that must stay individually
// pin-matchable and individually credited in the `__effect` window.
//
// pinnedAction is the mark's currently-recorded actionRef ("" for a genuinely
// fresh episode with no mark yet). This is the load-bearing branch (design
// §2): a fresh episode RANKS candidates / SYNTHESIZES a plan and picks the
// winner; an episode that already has a mark (an in-flight redelivery, or the
// sweep reclaiming an expired lease) MUST reuse that exact pin rather than
// re-ranking/re-planning — both depend on live, time-varying inputs (the
// §10.3 `__effect` close-rate window; a goal plan additionally on the
// CURRENT row state), so re-deriving mid-episode could silently swap which
// action a retry fires under the SAME requestId/claimId, corrupting the
// Contract #4 idempotency the mark exists to guarantee. Replanning only ever
// happens at a fresh episode (a mark absent because the gap just opened, the
// previous episode closed, or — goal gaps only — the pinned LEG's declared
// effects came to hold and releaseCompletedLeg cleared it) — exactly the
// design's "replanning happens only at episode/leg boundaries."
func (e *Engine) resolvePlannedAction(ctx context.Context, target *Target, targetID, entityID, gapColumn string,
	ga GapAction, row map[string]any, pinnedAction string) (GapAction, string, *planError) {

	if target.Mode != targetModePlanned || ga.Action != "" {
		return ga, ga.Action, nil
	}
	if len(ga.Candidates) > 0 {
		if pinnedAction != "" {
			for _, c := range ga.Candidates {
				if c.Action == pinnedAction {
					return candidateGapAction(c), c.Action, nil
				}
			}
			// The playbook changed since this episode was dispatched (the pinned
			// candidate was removed) — a config error, not a data error: only a
			// package re-author can fix it, and retrying the same row changes
			// nothing.
			return GapAction{}, "", &planError{kind: errConfig, msg: fmt.Sprintf(
				"gap %q: pinned action %q no longer exists among the playbook's candidates", gapColumn, pinnedAction)}
		}
		picked, ok := e.rankCandidates(ctx, targetID, gapColumn, ga.Candidates, row)
		if !ok {
			// No candidate's precondition currently holds against this row — a
			// per-row data condition (this row's fields don't satisfy anything
			// eligible right now), not a systemic config error; bounded, alerted,
			// never a hot loop (mirrors an ordinary template-data error).
			return GapAction{}, "", &planError{kind: errData, msg: fmt.Sprintf(
				"gap %q: no candidate is currently eligible (every candidate's precondition evaluated false)", gapColumn)}
		}
		for _, c := range ga.Candidates {
			if c.Action == picked {
				return candidateGapAction(c), picked, nil
			}
		}
		return GapAction{}, "", &planError{kind: errConfig, msg: fmt.Sprintf(
			"gap %q: internal — ranked candidate %q not found in its own candidate list", gapColumn, picked)}
	}
	if ga.Goal != nil {
		return e.resolveGoalAction(gapColumn, ga, row, pinnedAction)
	}
	return ga, ga.Action, nil
}

// candidateGapAction materializes a chosen GapCandidate into the GapAction
// shape buildPlan consumes (registry.go's GapCandidate doc: "the same
// action-contract shape as GapAction ... dispatches exactly like an explicit
// GapAction").
func candidateGapAction(c GapCandidate) GapAction {
	return GapAction{
		Action:    c.Action,
		Pattern:   c.Pattern,
		Subject:   c.Subject,
		Adapter:   c.Adapter,
		Operation: c.Operation,
		Assignee:  c.Assignee,
		Target:    c.Target,
		Params:    c.Params,
		Reads:     c.Reads,
	}
}

// catalogEntryGapAction materializes a chosen ActionCatalogEntry into the
// GapAction shape buildPlan consumes — the goal-branch analogue of
// candidateGapAction (registry.go's ActionCatalogEntry doc: "the same
// action-contract shape as GapCandidate").
func catalogEntryGapAction(entry ActionCatalogEntry) GapAction {
	return GapAction{
		Action:    entry.Action,
		Pattern:   entry.Pattern,
		Subject:   entry.Subject,
		Adapter:   entry.Adapter,
		Operation: entry.Operation,
		Assignee:  entry.Assignee,
		Target:    entry.Target,
		Params:    entry.Params,
		Reads:     entry.Reads,
	}
}

// goalMaxDepthSlack bounds Synthesize's search depth relative to the gap's
// own catalog size (design loftspace-lease-renewal-goal-authored-target-design.md
// §4.3: "maxDepth = len(actions) + 2", an R1 constant): every real chain is at
// most one leg per catalog entry, and the +2 slack absorbs a bounded amount of
// oscillation (an action whose effects transiently undo another's) without
// letting the search run unbounded.
const goalMaxDepthSlack = 2

// resolveGoalAction resolves one goal-mode gap (Fire 6, R1 — design
// loftspace-lease-renewal-goal-authored-target-design.md §4.3/§9): bounded
// goal regression over the gap's declared Actions catalog picks the
// cheapest leg toward Goal from the CURRENT row state (rowState bridged
// through this gap's own goalColumnPaths, exactly like rankCandidates' root
// mapping but with the Fire-6 aspect bridge). A fresh episode
// (pinnedAction=="") synthesizes and dispatches the winning plan's first
// step; an in-flight episode reuses the pinned leg verbatim — no re-rank, no
// re-plan — mirroring the candidates branch's pin discipline exactly, for
// the same idempotency reason (doc above).
func (e *Engine) resolveGoalAction(gapColumn string, ga GapAction, row map[string]any, pinnedAction string) (GapAction, string, *planError) {
	if pinnedAction != "" {
		for _, entry := range ga.Actions {
			if entry.Ref == pinnedAction {
				return catalogEntryGapAction(entry), entry.Ref, nil
			}
		}
		// The pinned ref isn't in the current catalog. Two indistinguishable
		// causes: the playbook changed since dispatch (a genuine config
		// error), or this episode was previously escalated to the augur
		// reasoning tier (planGap's unplannable retry pins the escalated
		// directOp's OWN Action string, which lives outside this gap's Ref
		// space entirely) and is being redelivered/reclaimed. Route through
		// the SAME unplannable-escalation retry a fresh Synthesize failure
		// gets, so a redelivered escalated episode re-derives the identical
		// deterministic augur dispatch instead of alerting; a target with no
		// escalation policy still surfaces this as a data error either way.
		return GapAction{}, "", &planError{kind: errData, unplannable: true, msg: fmt.Sprintf(
			"gap %q: pinned plan leg %q is not in the goal's actions catalog", gapColumn, pinnedAction)}
	}

	state := rowState(row, ga.goalColumnPaths)
	catalog := make([]planner.Action, len(ga.Actions))
	for i, entry := range ga.Actions {
		cost := entry.Cost
		if cost == 0 {
			cost = 1
		}
		catalog[i] = planner.Action{Ref: entry.Ref, Cost: cost, Precondition: entry.preGuard, Effects: entry.effectGuards}
	}
	p, err := planner.Synthesize(ga.goalGuard, state, catalog, len(ga.Actions)+goalMaxDepthSlack)
	if err != nil {
		if !errors.Is(err, planner.ErrNoPlan) {
			return GapAction{}, "", &planError{kind: errConfig, msg: fmt.Sprintf(
				"gap %q: internal planner error: %v", gapColumn, err)}
		}
		return GapAction{}, "", &planError{kind: errData, unplannable: true, msg: fmt.Sprintf(
			"gap %q: no plan derivable toward the goal from the current row state", gapColumn)}
	}
	if len(p.Steps) == 0 {
		// The goal already holds against the current row, yet the gap column
		// is still open — the lens authors its goal and its missing_<g>
		// column to agree (design §4.3), so this is a lens/goal mismatch, not
		// a normal outcome. Surface it loudly rather than dispatch nothing
		// while the gap stays open forever.
		return GapAction{}, "", &planError{kind: errData, msg: fmt.Sprintf(
			"gap %q: the goal already holds against the current row, but the gap column is still open (lens/goal authoring mismatch)", gapColumn)}
	}
	leg := p.Steps[0].ActionRef
	for _, entry := range ga.Actions {
		if entry.Ref == leg {
			return catalogEntryGapAction(entry), entry.Ref, nil
		}
	}
	return GapAction{}, "", &planError{kind: errConfig, msg: fmt.Sprintf(
		"gap %q: internal — synthesized leg %q not found in its own actions catalog", gapColumn, leg)}
}

// defaultAugur* are the reasoning-tier dispatch defaults a target's augur block
// inherits when it omits the explicit override (Contract #10 §10.8). The
// reasoning episode is single-step, so Weaver dispatches the reasoning op
// DIRECTLY as a directOp (Option F — no Loom wrapper): CreateAugurReasoningClaim
// mints the claim vertex write-ahead + emits external.<adapter>; the bridge
// calls the model and posts RecordProposal as the replyOp.
const (
	defaultAugurOp      = "CreateAugurReasoningClaim"
	defaultAugurAdapter = "augur"
	defaultAugurReplyOp = "RecordProposal"
)

// augurEscalation builds the reasoning-tier GapAction for a stuck gap whose
// target escalates `trigger` to the Augur AI tier (Contract #10 §10.8 "Augur
// escalation"). It is a plain directOp straight to the bridge (Option F): the
// reasoning op carries the TRUSTED gap context as flat literal params
// (targetId/entityId are the live meta + candidate vertex keys, gapColumn +
// trigger the stuck-gap coordinates), so CreateAugurReasoningClaim mints the
// claim vertex + emits external.<adapter> without any Loom orchestration. The
// dispatch then runs through the normal lane-1 path (buildPlan(actionDirectOp) →
// fireEpisode), inheriting the anti-storm mark, OCC, and reconciler reclaim
// wholesale.
//
// ok=false means no augur policy escalates this trigger (the caller fails closed
// per the frozen contract) — or the target's meta vertex is unresolved (it
// always resolves for a registered target whose row we are processing).
func augurEscalation(source *targetSource, target *Target, trigger, targetID, entityID, entityKey, gapColumn string) (GapAction, bool) {
	if target.Augur == nil {
		return GapAction{}, false
	}
	escalates := false
	for _, t := range target.Augur.Escalate {
		if t == trigger {
			escalates = true
			break
		}
	}
	if !escalates {
		return GapAction{}, false
	}
	// The targetId param + the forTarget no-orphan endpoint need the FULL meta
	// key (vtx.meta.<id>); the row-key targetID is the canonicalName prefix.
	targetMetaKey, ok := source.targetMetaKey(targetID)
	if !ok {
		return GapAction{}, false
	}
	op := target.Augur.Op
	if op == "" {
		op = defaultAugurOp
	}
	adapter := target.Augur.Adapter
	if adapter == "" {
		adapter = defaultAugurAdapter
	}
	replyOp := target.Augur.ReplyOp
	if replyOp == "" {
		replyOp = defaultAugurReplyOp
	}
	params := map[string]string{
		"instanceKey": deriveAugurHandle(targetID, entityID, gapColumn),
		"adapter":     adapter,
		"replyOp":     replyOp,
		"targetId":    targetMetaKey,
		"entityId":    entityKey,
		"gapColumn":   gapColumn,
		"trigger":     trigger,
	}
	if target.Augur.Model != "" {
		// Optional adapter model override (Contract #10 §10.8 augur block;
		// "default claude-opus-4-8" is the ADAPTER's own default, applied when
		// this is omitted — Weaver passes it through only when the target sets
		// one, exactly like Op/Adapter/ReplyOp's own omit-means-default posture).
		params["model"] = target.Augur.Model
	}
	return GapAction{
		Action:    actionDirectOp,
		Operation: op,
		// authTarget anchors the capability check on the weaver target meta
		// vertex (parallels triggerLoom's pattern-as-target); Weaver's
		// service-actor holds the op at scope: any (augur permissions, Fire-1 (4)).
		Target: targetMetaKey,
		Params: params,
		// The no-orphan alive endpoints routed into ContextHint.Reads — the
		// candidate (forCandidate) and the weaver target (forTarget). The op's
		// own alive checks use kv.Read (read-path-independent), so these are
		// belt-and-suspenders matching the as-built op's Weaver-routes-the-keys
		// posture (packages/augur/ddls.go).
		Reads: []string{entityKey, targetMetaKey},
	}, true
}

// resolveParam resolves one playbook param value: either a literal or the
// token row.<column> substituted from the violation row. A row.<column> that
// resolves null/absent is a data error — surface, do not fire a malformed
// remediation (§10.8 Templating).
func resolveParam(name, value string, row map[string]any) (any, *planError) {
	if value == "" {
		return nil, &planError{kind: errConfig, msg: fmt.Sprintf("param %q is required", name)}
	}
	col, templated := strings.CutPrefix(value, rowTemplatePrefix)
	if !templated {
		return value, nil
	}
	v, ok := row[col]
	if !ok || v == nil {
		return nil, &planError{kind: errData,
			msg: fmt.Sprintf("param %q references row.%s, which is null/absent in the row", name, col)}
	}
	return v, nil
}

// resolveStringParam resolves a param that must produce a non-empty string
// (keys, operation types, pattern refs).
func resolveStringParam(name, value string, row map[string]any) (string, *planError) {
	v, perr := resolveParam(name, value, row)
	if perr != nil {
		return "", perr
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", &planError{kind: errData,
			msg: fmt.Sprintf("param %q must resolve to a non-empty string (got %T)", name, v)}
	}
	return s, nil
}
