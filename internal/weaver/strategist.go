package weaver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/weaver/planner"
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
	opStartLoomPattern       = "StartLoomPattern"       // op-name: (submits) the Actuator submits this to trigger a Loom pattern instance for a triggerLoom action
	opCreateTask             = "CreateTask"             // op-name: (submits) the Actuator submits this for an assignTask action, assigning a bound op to a human subject
	opMarkExpired            = "MarkExpired"            // op-name: (submits) the Actuator submits this when a gap's mark has outlived its TTL
	opRecordProposalDispatch = "RecordProposalDispatch" // op-name: (submits) the Actuator submits this to record that an approved Augur proposal was dispatched as a directOp
)

// assignTaskGrantTTL is the expiry horizon set on an assignTask grant. The
// human response window is unbounded by design; the grant outlives any
// realistic response, and the bound op's commit auto-completes the task.
const assignTaskGrantTTL = 30 * 24 * time.Hour

// rowTemplatePrefix marks a templated param value: row.<column> substitutes
// that column's value from the violation row (Contract #10 §10.8 Templating).
const rowTemplatePrefix = "row."

// typedLiteralPrefix marks a param value that carries its own JSON type:
// json:<literal> resolves to whatever encoding/json decodes the suffix into
// (json:5 → float64(5), json:true → true, json:[1,2] → a slice), so a playbook
// authored in GapAction.Params' map[string]string shape can still hand an op a
// number, a bool, or a structured value. A value that must itself begin with
// the token is written as its own JSON string — json:"json:foo" resolves to
// the string json:foo.
//
// The two tokens are checked in order and are mutually exclusive by their
// leading bytes: row.<column> first (its resolved row value is never re-read
// as a token — a row column literally holding "json:5" stays that string),
// then json:<literal>, and an unprefixed value is a plain string literal.
// A value whose json: suffix does not decode is a config error, never a
// silent fall-through to plain-string.
//
// The token is confined to a gap's params bag. Every value that must be a
// string — a key, an operationType, a pattern ref — refuses it outright
// (resolveStringParam), because the gates upstream of dispatch compare those
// fields as raw authored strings.
//
// Note when grepping: the escape spelling json:"…" is character-identical to
// the opening of a Go struct tag (`json:"field,omitempty"`), so a bare grep
// for it across this repo returns overwhelmingly struct tags. Search for the
// constant, or for the token inside a params-bag literal.
const typedLiteralPrefix = "json:"

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
	// unplannable marks a failure planGap offers to the target's
	// augur.escalate("unplannable") policy before falling through to this
	// error's ordinary disposition (Fire 6, R1: design
	// loftspace-lease-renewal-goal-authored-target-design.md §5 — "'no plan
	// derivable' flows into the existing unplannable trigger"). Set at exactly
	// one site: resolveGoalAction's fresh Synthesize returning planner.ErrNoPlan
	// — the gap has a catalog and a goal, and no chain of its own actions
	// reaches that goal from this row. A fault in what the playbook DECLARES is
	// a config error instead, whichever branch finds it.
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
	// class pins the dispatched op's DDL canonical name (opEnvelope.Class) —
	// required only when operationType is admitted by more than one installed
	// vertexType DDL (an ambiguous operationType the Processor's reverse index
	// won't resolve for free). Empty for every unambiguous op, unchanged
	// behavior.
	class      string
	authTarget string
	payload    func(claimID string) map[string]any
	// reads is the dispatched op's ContextHint.Reads: the BARE vertex keys the
	// op's DDL script hydrates + validates (vertex_alive). The dispatcher
	// declares them because it builds the payload and so knows the exact keys
	// the op touches. Empty for read-free ops (StartLoomPattern, most
	// directOps). NO `.state` suffixes — the DDLs read bare keys.
	reads []string
	// optionalReads, when non-nil, yields the dispatched op's
	// ContextHint.OptionalReads (Contract #2 §2.5 — declared absence-tolerant
	// reads) for the episode's claimID. A closure, like payload, because the
	// assignTask dedup key derives from the claimId-seeded stable taskId. Nil
	// for ops that read no absence-tolerant keys.
	optionalReads func(claimID string) []string
	// enumerations is the dispatched op's ContextHint.Enumerations (Contract #2
	// §2.5 class (e)): the kv.Links walks the op's script runs, each hub already
	// resolved from the violation row. Metadata on the envelope — nothing
	// hydrates from it. Empty for every op that walks no links.
	enumerations []GapEnumeration
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
			// `.state` aspect, so none is listed — the declared set names exactly
			// what the script reads. Cross-checked against the script by
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
		// The dispatched op's reads: each is a literal, a row.<column> template
		// (e.g. row.entityKey to hand the op its candidate vertex), or a
		// row.<column>.<aspect> template for a key one aspect below a row
		// column's own vertex root (script-read-posture-design.md §13 hard
		// case 4 — mirrors the Starlark idiom `unit + ".listing"`; the column
		// need not itself carry the composed key). The candidate key is
		// already in the lens row, so this just routes it into the op's
		// ContextHint.Reads so its DDL can hydrate + validate it.
		var reads []string
		for i, rt := range ga.Reads {
			r, perr := resolveReadKey(fmt.Sprintf("reads[%d]", i), rt, row)
			if perr != nil {
				return nil, perr
			}
			reads = append(reads, r)
		}
		// The dispatched op's declared absence-tolerant reads: same resolver
		// and template grammar as Reads, so a directOp can route a
		// row.<column> candidate key into ContextHint.OptionalReads exactly as
		// it routes one into Reads.
		//
		// A DATA-shaped failure drops that one entry instead of failing the
		// gap, and this is the field where the two differ. The natural way to
		// declare an absence-tolerant read is a nullable lens column
		// (row.priorClaimKey, null exactly when there is no prior claim), and
		// the rows where it is null are precisely the rows the declaration was
		// written for — failing them would starve the gap forever on the
		// dispatch it exists to make. A dropped entry degrades to what the
		// script did before it was declared: a live undeclared read, correct
		// but unhydrated. A CONFIG error (a typed-literal token, any
		// permanently undispatchable shape) still fails the gap, because no
		// row can fix it.
		var optionalReads []string
		for i, rt := range ga.OptionalReads {
			r, perr := resolveReadKey(fmt.Sprintf("optionalReads[%d]", i), rt, row)
			if perr != nil {
				if perr.kind == errData {
					continue
				}
				return nil, perr
			}
			optionalReads = append(optionalReads, r)
		}
		// plan.optionalReads is a closure like plan.payload's own
		// func(string) map[string]any — ignoring claimID here, since a
		// directOp's declared reads are pure row-templates, unlike
		// assignTask's claimId-seeded dedup key. Nil and empty are equivalent
		// downstream (the actuator attaches a contextHint only when some list
		// is non-empty, and the field carries omitempty), so nil is simply
		// what `var` + `append` yields when nothing resolves, allocating no
		// closure for a gap that declares none.
		var optionalReadsFn func(claimID string) []string
		if len(optionalReads) > 0 {
			optionalReadsFn = func(string) []string { return optionalReads }
		}
		// The dispatched op's declared link walks: the hub travels the SAME
		// resolver as a declared read (it is a key, in the same template
		// grammar), while relation and direction are literals the playbook
		// states outright. Declaring the walk does not change what the script
		// does — the kv.Links call is a bounded paged live read either way — it
		// puts the walk on the envelope instead of leaving it knowable only by
		// reading the script.
		var enumerations []GapEnumeration
		for i, en := range ga.Enumerations {
			hub, perr := resolveReadKey(fmt.Sprintf("enumerations[%d].hub", i), en.Hub, row)
			if perr != nil {
				return nil, perr
			}
			enumerations = append(enumerations, GapEnumeration{
				Hub:       hub,
				Relation:  en.Relation,
				Direction: en.Direction,
			})
		}
		return &plan{
			operationType: ga.Operation,
			class:         ga.Class,
			authTarget:    authTarget,
			payload:       func(string) map[string]any { return params },
			reads:         reads,
			optionalReads: optionalReadsFn,
			enumerations:  enumerations,
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
// (ga.Action / the picked candidate's Action) — a goal
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

// resolvedLegAction answers "what dispatch contract type would this gap fire
// right now" WITHOUT building a plan — the question a caller must settle before
// it may plan, because planGap consumes an admission token and clears the gap's
// standing issues on the strength of a dispatch about to happen.
//
// It is exactly resolvePlannedAction's fresh-episode resolution, whose two
// planned-mode branches are cheap and side-effect-free: a goal gap's bounded
// regression is a pure function of (row, catalog), and a candidates gap's rank
// reads only the `__effect` confidence windows. For every other shape it
// returns the playbook's own Action unchanged, so one helper answers for the
// static and the plan-time-resolved gaps alike — which is what lets the
// operator verb refuse exactly what the sweep's re-arm permanently declines.
//
// A planError is the honest "it would fire nothing for this row": no candidate
// eligible, no derivable plan, a pinned ref the catalog dropped.
//
// It returns the resolved REF alongside the action, so a caller that goes on to
// plan can pin the plan to the same resolution rather than taking a second one.
// The two would not always agree: a candidates gap's rank reads the `__effect`
// confidence windows, which a concurrent close moves, so a second unpinned
// resolution can pick a different candidate — and the classification this helper
// exists to serve would then have been taken over an action that never fires.
func (e *Engine) resolvedLegAction(ctx context.Context, target *Target, targetID, entityID, gapColumn string,
	ga GapAction, row map[string]any) (action, ref string, perr *planError) {

	resolved, actionRef, perr := e.resolvePlannedAction(ctx, target, targetID, entityID, gapColumn, ga, row, "")
	if perr != nil {
		return "", "", perr
	}
	return resolved.Action, actionRef, nil
}

// candidateGapAction materializes a chosen GapCandidate into the GapAction
// shape buildPlan consumes (registry.go's GapCandidate doc: "the same
// action-contract shape as GapAction ... dispatches exactly like an explicit
// GapAction").
func candidateGapAction(c GapCandidate) GapAction {
	return GapAction{
		Action:        c.Action,
		Pattern:       c.Pattern,
		Subject:       c.Subject,
		Adapter:       c.Adapter,
		Operation:     c.Operation,
		Assignee:      c.Assignee,
		Target:        c.Target,
		Params:        c.Params,
		Reads:         c.Reads,
		OptionalReads: c.OptionalReads,
		Enumerations:  c.Enumerations,
	}
}

// catalogEntryGapAction materializes a chosen ActionCatalogEntry into the
// GapAction shape buildPlan consumes — the goal-branch analogue of
// candidateGapAction (registry.go's ActionCatalogEntry doc: "the same
// action-contract shape as GapCandidate").
func catalogEntryGapAction(entry ActionCatalogEntry) GapAction {
	return GapAction{
		Action:        entry.Action,
		Pattern:       entry.Pattern,
		Subject:       entry.Subject,
		Adapter:       entry.Adapter,
		Operation:     entry.Operation,
		Assignee:      entry.Assignee,
		Target:        entry.Target,
		Params:        entry.Params,
		Reads:         entry.Reads,
		OptionalReads: entry.OptionalReads,
		Enumerations:  entry.Enumerations,
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
		// The pinned ref isn't in the current catalog: the playbook changed
		// since this episode was dispatched. A CONFIG error, the sibling of the
		// candidates branch's identical verdict above — only a package re-author
		// can fix it, and retrying the same row changes nothing.
		//
		// An escalation's mark pins a dispatch CLASS that lives outside this
		// gap's Ref space entirely and reaches this arm the same way. The two
		// causes are not indistinguishable: an escalation declares its class on
		// its own mark (mark.Escalation), so a reader that must tell them apart
		// reads that field rather than inferring a cause from a resolution that
		// failed.
		return GapAction{}, "", &planError{kind: errConfig, msg: fmt.Sprintf(
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
	defaultAugurOp      = "CreateAugurReasoningClaim" // op-name: (submits) Weaver dispatches this directly as a directOp, minting the reasoning claim vertex write-ahead and emitting the external.<adapter> event
	defaultAugurAdapter = "augur"
	defaultAugurReplyOp = "RecordProposal" // op-name: (policy) Weaver never publishes this; it names the verb in the dispatch params, the augur script copies it into the external event, and the Bridge posts it — a core-owned default over a verb packages/augur owns pin=TestAugurConvergence_HappyPath
)

// escalatesTrigger reports whether the target's augur block opts this trigger
// into its escalate list (Contract #10 §10.8's two tokens). It is the policy
// question on its own, for the sites that must know whether a standing
// escalation is still one the target wants — the orphan-column arm, which
// spares a door-2 episode only while the policy that produced it stands — from
// the sites that go on to build the dispatch.
func escalatesTrigger(target *Target, trigger string) bool {
	if target.Augur == nil {
		return false
	}
	for _, t := range target.Augur.Escalate {
		if t == trigger {
			return true
		}
	}
	return false
}

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
	if !escalatesTrigger(target, trigger) {
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

// resolveRowTemplate resolves the row.<column> arm shared by every value in
// the playbook's grammar. templated reports whether the value claimed the
// token at all; when it did, the column's own Go value is returned — an
// int64 column arrives as an int64 — and a null or absent column is a data
// error, surfaced rather than fired as a malformed remediation (§10.8
// Templating).
func resolveRowTemplate(name, value string, row map[string]any) (v any, templated bool, perr *planError) {
	col, templated := strings.CutPrefix(value, rowTemplatePrefix)
	if !templated {
		return nil, false, nil
	}
	v, ok := row[col]
	if !ok || v == nil {
		return nil, true, &planError{kind: errData,
			msg: fmt.Sprintf("param %q references row.%s, which is null/absent in the row", name, col)}
	}
	return v, true, nil
}

// resolveParam resolves one PARAMS-BAG value against the three arms of the
// value grammar: row.<column>, substituted from the violation row and
// delivering that column's own Go type; json:<literal>, decoded into the JSON
// value its suffix encodes; or, unprefixed, a plain string literal passed
// through byte-for-byte. A json: suffix that does not decode is a config
// error: no row can ever make it dispatchable.
//
// Only the params bag reaches this resolver. Every other authored value is a
// key, an operationType or a pattern ref — always a string — and takes the
// two-arm resolveStringParam below, which refuses the typed-literal token
// outright. The split is the security boundary: gates upstream of dispatch
// (pkgmgr's authored-dispatch scope check, the Augur proposal scope check)
// compare those fields as RAW authored strings, so a field that decoded at
// dispatch would be one the gate never saw.
func resolveParam(name, value string, row map[string]any) (any, *planError) {
	if value == "" {
		return nil, &planError{kind: errConfig, msg: fmt.Sprintf("param %q is required", name)}
	}
	if v, templated, perr := resolveRowTemplate(name, value, row); templated {
		return v, perr
	}
	if literal, typed := strings.CutPrefix(value, typedLiteralPrefix); typed {
		return decodeTypedLiteral(name, literal)
	}
	return value, nil
}

// decodeTypedLiteral decodes the suffix of a json:<literal> param into the
// value it encodes, failing closed rather than degrading to the plain-string
// arm on each of four shapes:
//
//   - a suffix that is not valid JSON;
//   - the literal null, and the empty string — both indistinguishable at the
//     receiving op from a param that was never declared, so accepting either
//     would give one dispatch two spellings (the plain arm likewise refuses an
//     unwritten value, and the templated arm refuses a null column);
//   - an integer whose decimal spelling float64 cannot hold exactly, which
//     would otherwise dispatch a DIFFERENT number than the author wrote.
func decodeTypedLiteral(name, literal string) (any, *planError) {
	var v any
	if err := json.Unmarshal([]byte(literal), &v); err != nil {
		return nil, &planError{kind: errConfig,
			msg: fmt.Sprintf("param %q carries the %s typed-literal token but %q is not valid JSON: %v — a string that must itself begin with the token is written as its own JSON string (%s\"json:foo\" resolves to json:foo)",
				name, typedLiteralPrefix, literal, err, typedLiteralPrefix)}
	}
	if v == nil {
		return nil, &planError{kind: errConfig,
			msg: fmt.Sprintf("param %q resolves to the %snull typed literal — a null param is indistinguishable from an absent one; omit the param instead", name, typedLiteralPrefix)}
	}
	if s, isStr := v.(string); isStr && s == "" {
		return nil, &planError{kind: errConfig,
			msg: fmt.Sprintf("param %q resolves to the empty string — an empty param is indistinguishable from an absent one; omit the param instead", name)}
	}
	if lossy, spelling := lossyJSONInteger(literal); lossy {
		return nil, &planError{kind: errConfig,
			msg: fmt.Sprintf("param %q declares the integer %s, which a JSON number (float64) cannot hold exactly — it would dispatch as %s; send it as a %sstring, or use a row.<column> template, which carries the column's exact type",
				name, spelling, formatJSONFloat(spelling), typedLiteralPrefix)}
	}
	return v, nil
}

// lossyJSONInteger reports whether a JSON document contains an integer-spelled
// number float64 cannot represent exactly, and returns that spelling. The
// row.<column> arm delivers a lens column's exact int64, so a typed literal
// that silently rounded (9007199254740993 → …92, a nanosecond timestamp →
// …000) would be the one arm of the grammar that changes the author's value.
// Only integer spellings are checked: a spelling that already carries a
// decimal point or an exponent is float notation, where the author has asked
// for float semantics outright.
func lossyJSONInteger(literal string) (bool, string) {
	dec := json.NewDecoder(strings.NewReader(literal))
	dec.UseNumber()
	var shadow any
	if err := dec.Decode(&shadow); err != nil {
		return false, ""
	}
	return walkJSONNumbers(shadow)
}

func walkJSONNumbers(v any) (bool, string) {
	switch t := v.(type) {
	case json.Number:
		s := t.String()
		if strings.ContainsAny(s, ".eE") {
			return false, ""
		}
		if formatJSONFloat(s) != s {
			return true, s
		}
	case []any:
		for _, e := range t {
			if lossy, s := walkJSONNumbers(e); lossy {
				return true, s
			}
		}
	case map[string]any:
		// Sorted, because Go randomizes map range and two lossy numbers in one
		// object would otherwise name a different one on each run.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if lossy, s := walkJSONNumbers(t[k]); lossy {
				return true, s
			}
		}
	}
	return false, ""
}

// formatJSONFloat renders an integer spelling as the decimal float64 actually
// dispatches for it, so a refusal can show the author both numbers.
func formatJSONFloat(spelling string) string {
	f, err := strconv.ParseFloat(spelling, 64)
	if err != nil {
		return spelling
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// typedLiteralError reports the config error a params-bag value would raise at
// dispatch through decodeTypedLiteral, or nil when the value is not a typed
// literal or is a well-formed one. The token's suffix is authored, never
// row-derived, so the verdict is row-independent — which is what lets
// validateTarget refuse a malformed literal at load, for every row at once,
// instead of leaving each dispatch to discover the same permanent defect.
func typedLiteralError(name, value string) *planError {
	literal, typed := strings.CutPrefix(value, typedLiteralPrefix)
	if !typed {
		return nil
	}
	_, perr := decodeTypedLiteral(name, literal)
	return perr
}

// resolveStringParam resolves a value that must produce a non-empty string
// (keys, operation types, pattern refs) against TWO arms: row.<column>, or a
// plain string literal.
//
// The json:<literal> token is refused outright here, loudly, rather than
// decoded or quietly passed through. A string field has nothing to gain from
// the token — json:"Foo" is a verbose spelling of Foo — and everything to
// lose: pkgmgr's authored-dispatch scope guard compares ga.Operation and
// ga.Pattern against its protected sets by RAW string equality, and Augur's
// proposal scope check compares raw param values against the escalated
// candidate, so a field that decoded at dispatch would let an authored target
// name a protected op (or a foreign vertex key) in a spelling no gate
// recognises. Refusing beats passing the token through untouched, which would
// merely defer the failure to an unresolvable operationType.
func resolveStringParam(name, value string, row map[string]any) (string, *planError) {
	if value == "" {
		return "", &planError{kind: errConfig, msg: fmt.Sprintf("param %q is required", name)}
	}
	if strings.HasPrefix(value, typedLiteralPrefix) {
		return "", &planError{kind: errConfig,
			msg: fmt.Sprintf("param %q must be a key, operationType or pattern ref — always a string — so the %s typed literal is not permitted here (it is meaningful only in a gap's params bag); write the value directly",
				name, typedLiteralPrefix)}
	}
	v, templated, perr := resolveRowTemplate(name, value, row)
	if perr != nil {
		return "", perr
	}
	if !templated {
		return value, nil
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", &planError{kind: errData,
			msg: fmt.Sprintf("param %q must resolve to a non-empty string (got %T)", name, v)}
	}
	return s, nil
}

// resolveReadKey resolves one GapActionSpec.Reads template entry. It accepts
// the same row.<column> substitution resolveStringParam does; when that
// fails because the column is absent, it falls back to a derived-aspect form
// row.<column>.<aspect> — the column resolves to a vertex root key and
// <aspect> (which may itself contain dots) is joined onto it, mirroring the
// Starlark idiom `unit + ".listing"` for a read one aspect below a row
// column's own key (script-read-posture-design.md §13 hard case 4). The three
// declared key lists use it — Reads, OptionalReads and each Enumerations hub —
// while Params/Target/Operation stay exact row-column lookups, since those
// values are not necessarily composable keys.
func resolveReadKey(name, value string, row map[string]any) (string, *planError) {
	s, perr := resolveStringParam(name, value, row)
	if perr == nil {
		return s, nil
	}
	if perr.kind != errData {
		return "", perr
	}
	col, templated := strings.CutPrefix(value, rowTemplatePrefix)
	if !templated {
		return "", perr
	}
	base, aspect, found := strings.Cut(col, ".")
	if !found {
		return "", perr
	}
	v, ok := row[base]
	if !ok || v == nil {
		return "", perr
	}
	root, ok := v.(string)
	if !ok || root == "" {
		return "", perr
	}
	return root + "." + aspect, nil
}
