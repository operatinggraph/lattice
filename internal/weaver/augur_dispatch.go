package weaver

import (
	"fmt"
	"strings"

	"github.com/asolgan/lattice/internal/substrate"
)

// augurAllowedActions is the dispatch-time (third) leg of the design's §5
// deterministic-validation boundary — the SAME allowed escalation vocabulary
// the record-time (packages/augur/ddls.go ALLOWED_ACTIONS) and approval-time
// (revalidate_for_approval) legs enforce, re-run here in Go as defense in depth
// immediately before the row-carried action ever reaches buildPlan. A proposal
// gains no new authority by surviving to "approved" — every leg re-derives the
// SAME accept/reject boundary independently.
var augurAllowedActions = map[string]bool{
	actionTriggerLoom: true,
	actionAssignTask:  true,
	actionDirectOp:    true,
}

// buildProposedOpPlan resolves the augurDispatch target's one gap
// (missing_dispatch) into the Fire 2b two-op dispatch (design
// augur-dispatch-pickup §3.2/§3.3). entityID is the row's entity segment — the
// proposal's bare NanoID handle (§10.2: the augurDispatchPending lens's
// Output.KeyColumn puts the anchor's bare id there, same as every other
// weaver-target). row carries the augurDispatchPending lens's columns:
// proposedAction, proposedParams, candidateKey, targetMetaKey.
//
// A dispatch-time-INVALID proposal (bad action, scope escape, a stale
// operation/pattern reference) fires ONLY the RecordProposalDispatch{outcome:
// invalid} flip — no remediation, so an unresolvable proposal can never
// half-dispatch. A valid proposal's plan carries a proposal-scoped deterministic
// requestId (collapse-only under a sweep reclaim) and a followUp that flips
// approved → dispatched once the remediation is fired.
//
// An errTransient from the inner buildPlan (a pattern/op reference not yet
// replayed) defers via NakWithDelay with NO flip — nothing was dispatched, so
// nothing to record yet; the next redelivery/reclaim retries the same
// resolution.
func buildProposedOpPlan(source *targetSource, entityID string, row map[string]any, expectedRevision uint64) (*plan, *planError) {
	handle := entityID
	candidateKey, _ := row["candidateKey"].(string)
	if candidateKey == "" {
		return recordDispatchOutcomePlan(handle, "invalid",
			"augurDispatch row carries no candidateKey (the proposal's trusted .gap context)"), nil
	}
	action, _ := row["proposedAction"].(string)
	params, _ := row["proposedParams"].(map[string]any)

	if reason := validateProposedDispatch(action, params, candidateKey); reason != "" {
		return recordDispatchOutcomePlan(handle, "invalid", reason), nil
	}

	var innerPlan *plan

	if action == actionDirectOp {
		// directOp's final op payload can carry ANY JSON type (bool, number,
		// nested object) — not just strings — so it is built directly here
		// rather than round-tripped through GapAction.Params (map[string]string,
		// designed for the STATIC playbook's literal-or-row.<column> STRING
		// tokens). There is no live-registry resolution to reuse for directOp
		// (buildPlan's directOp case does none either), so bypassing it costs
		// nothing.
		pl, err := buildProposedDirectOpPlan(params, expectedRevision)
		if err != nil {
			return recordDispatchOutcomePlan(handle, "invalid", err.Error()), nil
		}
		innerPlan = pl
	} else {
		innerGA, err := materializeGapAction(action, params)
		if err != nil {
			return recordDispatchOutcomePlan(handle, "invalid", err.Error()), nil
		}

		// candidateKey is TRUSTED (echoed from the proposal's .gap aspect via the
		// lens) — its bare id is the inner buildPlan's entityID (assignTask/
		// triggerLoom fold it into their STABLE artifact ids, §10.3), so a re-
		// approved proposal for the same candidate/gap still collapses on the
		// same downstream task/instance, not a fresh one per proposal handle.
		_, candidateID, ok := substrate.ParseVertexKey(candidateKey)
		if !ok {
			return recordDispatchOutcomePlan(handle, "invalid",
				"candidateKey "+candidateKey+" is not a well-formed vtx.<type>.<id> vertex key"), nil
		}

		// expectedRevision here is the augurDispatch ROW's own revision (the
		// proposal's projection), not the candidate vertex's — a mismatch of the
		// SAME class objects-base's TombstoneObject dispatch already documents
		// and accepts (targets.go: "Weaver also auto-injects the candidate's row
		// revision as expectedRevision; TombstoneObject ignores it ... and
		// relies on [its own OCC] instead"). A materialised op's own DDL is
		// responsible for whatever conditioning is meaningful for its shape;
		// Weaver cannot know in advance whether an arbitrary proposed op even
		// reads this field.
		pl, perr := buildPlan(source, "augurDispatch", candidateID, "missing_dispatch", innerGA, row, expectedRevision)
		if perr != nil {
			if perr.kind == errTransient {
				// Defer — a live-catalog reference (pattern meta-vertex or
				// op meta-vertex) has not replayed yet, or never will. Nothing
				// was dispatched, so no flip.
				return nil, perr
			}
			return recordDispatchOutcomePlan(handle, "invalid",
				"dispatch-time resolution failed: "+perr.msg), nil
		}
		innerPlan = pl
	}

	innerPlan.requestID = func(string) string { return deriveProposalDispatchRequestID(handle) }
	innerPlan.followUp = recordDispatchOutcomePlan(handle, "dispatched", "")
	return innerPlan, nil
}

// buildProposedDirectOpPlan materialises a proposed directOp {operation,
// target?, params, reads?} directly into a plan — params is copied VERBATIM
// (preserving bool/number/nested-object values a real op payload legitimately
// carries; e.g. SetAvailability's `available` is a bool, not a string), unlike
// GapAction.Params (map[string]string, the static playbook's literal-or-
// row.<column> token shape). No live-registry resolution applies to directOp
// (buildPlan's own directOp case does none either).
func buildProposedDirectOpPlan(params map[string]any, expectedRevision uint64) (*plan, error) {
	operation, ok := stringField(params, "operation")
	if !ok {
		return nil, fmt.Errorf("proposed directOp requires a string param operation")
	}
	// target is REQUIRED here (belt-and-suspenders): validateProposedDispatch
	// already enforces target == candidateKey before this function is ever
	// reached in the real dispatch path (buildProposedOpPlan), but this
	// function must never silently authTarget-less an op it materialises —
	// an absent target is a config error, not "no anchor needed".
	target, ok := stringField(params, "target")
	if !ok {
		return nil, fmt.Errorf("proposed directOp requires a string param target")
	}
	opParams, err := anyMapField(params, "params")
	if err != nil {
		return nil, err
	}
	reads, err := stringSliceField(params, "reads")
	if err != nil {
		return nil, err
	}
	payload := make(map[string]any, len(opParams)+1)
	for k, v := range opParams {
		payload[k] = v
	}
	payload["expectedRevision"] = expectedRevision
	return &plan{
		operationType: operation,
		authTarget:    target,
		payload:       func(string) map[string]any { return payload },
		reads:         reads,
	}, nil
}

// recordDispatchOutcomePlan builds the RecordProposalDispatch flip plan
// (design §3.3). Its requestId is proposal-scoped + outcome-scoped so a
// redelivery collapses it too (the DDL's approved-only guard is the second,
// independent backstop). ContextHint.Reads carries the proposal's .review
// aspect (the required key the DDL's kv.Read fails closed on — read-posture
// class (a), script-read-posture-design §13); the bare proposalKey rides
// alongside it for authTarget's belt-and-suspenders alive check, mirroring
// CreateAugurReasoningClaim's convention.
func recordDispatchOutcomePlan(handle, outcome, reason string) *plan {
	proposalKey := "vtx.augurproposal." + handle
	return &plan{
		operationType: opRecordProposalDispatch,
		authTarget:    proposalKey,
		requestID:     func(string) string { return deriveProposalDispatchFlipRequestID(handle, outcome) },
		payload: func(string) map[string]any {
			p := map[string]any{"externalRef": handle, "outcome": outcome}
			if reason != "" {
				p["reason"] = reason
			}
			return p
		},
		reads: []string{proposalKey, proposalKey + ".review"},
	}
}

// materializeGapAction turns an approved Augur proposal's row-carried
// {proposedAction, proposedParams} into a GapAction for the two STATIC-shaped
// actions (design §3.2 "Param vocabulary, resolved not deferred") —
//
//	triggerLoom {pattern, subject}
//	assignTask  {operation, assignee, target}
//
// — every field of which is a plain string, so GapAction's map[string]string-
// shaped fields round-trip losslessly. directOp is NOT handled here: its
// `params` sub-object can carry any JSON type (bool, number, nested object),
// which GapAction.Params (map[string]string) cannot hold without lossy
// stringification — see buildProposedDirectOpPlan instead.
//
// Every value here is a resolved literal (the row IS the resolved data, never
// a row.<column> template) — validateProposedDispatch has already rejected any
// value carrying the reserved row.<column> template prefix, so buildPlan's
// resolveParam/resolveStringParam treat every field as a literal, never
// re-templating it against Weaver's OWN internal row.
func materializeGapAction(action string, params map[string]any) (GapAction, error) {
	switch action {
	case actionTriggerLoom:
		pattern, ok1 := stringField(params, "pattern")
		subject, ok2 := stringField(params, "subject")
		if !ok1 || !ok2 {
			return GapAction{}, fmt.Errorf("proposed triggerLoom requires string params pattern + subject")
		}
		return GapAction{Action: actionTriggerLoom, Pattern: pattern, Subject: subject}, nil

	case actionAssignTask:
		operation, ok1 := stringField(params, "operation")
		assignee, ok2 := stringField(params, "assignee")
		target, ok3 := stringField(params, "target")
		if !ok1 || !ok2 || !ok3 {
			return GapAction{}, fmt.Errorf("proposed assignTask requires string params operation + assignee + target")
		}
		return GapAction{Action: actionAssignTask, Operation: operation, Assignee: assignee, Target: target}, nil

	default:
		return GapAction{}, fmt.Errorf("action %q is not in the allowed escalation vocabulary (%s|%s)",
			action, actionTriggerLoom, actionAssignTask)
	}
}

// dispatchAnchorField names, per action, the ONE field the design's own
// "Param vocabulary" (§3.2) designates as the candidate anchor — triggerLoom's
// subject, assignTask's/directOp's target. Pinning this SPECIFIC field to
// candidateKey (below), rather than only requiring the candidate to appear
// SOMEWHERE in the params bag, closes a structural gap a review caught: a
// proposal could otherwise satisfy "the candidate is mentioned somewhere" via
// an unrelated field (e.g. a read-only `reads` entry) while leaving the field
// that actually determines the materialised op's real target absent or
// non-vtx-shaped (the generic scan below only examines vtx.<type>.<id>-shaped
// values, by design — see isVtxKey). Returns "" for an action outside the
// vocabulary (the caller checks that separately).
func dispatchAnchorField(action string) string {
	switch action {
	case actionTriggerLoom:
		return "subject"
	case actionAssignTask, actionDirectOp:
		return "target"
	default:
		return ""
	}
}

// validateProposedDispatch is the dispatch-time (third) leg of design §5: it
// re-checks the action vocabulary, REQUIRES the action's designated anchor
// field (dispatchAnchorField) to be present and equal the TRUSTED
// candidateKey exactly, and then re-runs the default-deny scope containment
// (every OTHER vtx-shaped value the proposal carries, under ANY param name,
// must also equal candidateKey) — mirroring packages/augur/ddls.go's
// scope_verdict, so the three legs (record/approve/dispatch) enforce the
// identical boundary, now pinned to the field that actually matters rather
// than "referenced somewhere." Values are compared RAW (no trimming): a
// padded/whitespace-decorated vtx-key simply fails to match rather than being
// normalized before comparison — the validated value must be BYTE-IDENTICAL
// to what gets dispatched, so validation and dispatch can never see two
// different strings. Returns "" when valid, else the invalid reason.
func validateProposedDispatch(action string, params map[string]any, candidateKey string) string {
	if !augurAllowedActions[action] {
		return fmt.Sprintf("action not in allowed escalation vocabulary (%s|%s|%s): %q",
			actionTriggerLoom, actionAssignTask, actionDirectOp, action)
	}
	if params == nil {
		return "proposed params missing"
	}
	anchorField := dispatchAnchorField(action)
	anchor, ok := stringField(params, anchorField)
	if !ok {
		return fmt.Sprintf("proposed %s requires a string param %q naming the escalated candidate", action, anchorField)
	}
	if anchor != candidateKey {
		return fmt.Sprintf("proposed %s.%s = %q does not equal the escalated candidate %q", action, anchorField, anchor, candidateKey)
	}

	strs, tooDeep := collectParamStrings(params)
	if tooDeep {
		return "proposed params nested deeper than the flat action-param model can scope-check"
	}
	for _, s := range strs {
		if strings.HasPrefix(s, rowTemplatePrefix) {
			// A model-proposed literal may never use the reserved row.<column>
			// template prefix: buildPlan's resolveParam would otherwise
			// re-interpret it as a template against WEAVER'S OWN internal
			// augurDispatch row (candidateKey/targetMetaKey/entityKey columns) —
			// a template-injection scope-escape vector distinct from (and not
			// caught by) the plain vtx-key check below. Fail-closed.
			return "proposed param value " + s + " uses the reserved row.<column> template prefix, not permitted in a model-proposed literal"
		}
		if s != candidateKey && isVtxKey(s) {
			return "scope escape: param value " + s + " references an entity other than the escalated candidate " + candidateKey
		}
	}
	return ""
}

// isVtxKey mirrors packages/augur/ddls.go's is_vtx_key: true if v looks like a
// vertex key (vtx.<type>.<id>) a proposal would ACT ON. Operation-type names,
// canonicalNames, timestamps, booleans, and free text are not vertex keys and
// are not scope-checked.
func isVtxKey(v string) bool {
	parts := strings.Split(v, ".")
	return len(parts) >= 3 && parts[0] == "vtx" && parts[1] != "" && parts[2] != ""
}

// collectParamStrings flattens a proposal's params to the string values it
// carries, mirroring packages/augur/ddls.go's collect_param_strings EXACTLY
// (one level plus each dict/list's immediate children — no deeper recursion)
// so the dispatch-time leg enforces the identical accept/reject boundary as
// the record/approval-time Starlark legs, even though Go itself has no
// recursion restriction. A value nested deeper sets tooDeep (conservatively
// rejected — a false-positive over-reject is the contained failure, never a
// false-negative that lets a foreign reference through).
func collectParamStrings(params map[string]any) (out []string, tooDeep bool) {
	flattenOne := func(v any) {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case map[string]any, []any:
			tooDeep = true
		}
	}
	for _, v := range params {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case map[string]any:
			for _, v2 := range t {
				flattenOne(v2)
			}
		case []any:
			for _, item := range t {
				flattenOne(item)
			}
		}
	}
	return out, tooDeep
}

// stringField reads a required non-empty string field.
func stringField(params map[string]any, name string) (string, bool) {
	v, ok := params[name]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

// anyMapField reads an optional object-valued field verbatim (directOp's
// nested `params`, whose values may be any JSON type — bool, number, string,
// or a further-nested object/array; the dispatched op's own DDL interprets
// them). Absent projects nil (an empty payload). A present-but-non-object
// value errors rather than silently dropping data a proposal's remediation
// actually needs.
func anyMapField(params map[string]any, name string) (map[string]any, error) {
	v, ok := params[name]
	if !ok || v == nil {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("proposed params.%s must be an object, got %T", name, v)
	}
	return m, nil
}

// stringSliceField reads an optional []string field (directOp's `reads`);
// absent projects nil (buildPlan treats a nil/empty Reads as read-free).
func stringSliceField(params map[string]any, name string) ([]string, error) {
	v, ok := params[name]
	if !ok || v == nil {
		return nil, nil
	}
	l, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("proposed params.%s must be an array, got %T", name, v)
	}
	out := make([]string, 0, len(l))
	for i, item := range l {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("proposed params.%s[%d] must be a string, got %T", name, i, item)
		}
		out = append(out, s)
	}
	return out, nil
}
