package weaver

import (
	"fmt"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// Action names of the Contract #10 §10.8 action table.
const (
	actionTriggerLoom = "triggerLoom"
	actionNudge       = "nudge"
	actionAssignTask  = "assignTask"
	actionDirectOp    = "directOp"
)

// Operation types the Actuator submits.
const (
	opStartLoomPattern = "StartLoomPattern"
	opCreateTask       = "CreateTask"
	opMarkExpired      = "MarkExpired"
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
}

func (e *planError) Error() string { return e.msg }

// plan is one gap's fully-resolved dispatch, pending only the episode tag (the
// mark's create revision): payload(markRevision) materializes the op payload
// at fire time. A re-fire of the same episode reuses the same deterministic
// requestId and collapses on the Contract #4 tracker — idempotency rests on
// the requestId, not payload equality (time-derived fields such as
// assignTask's expiresAt differ per fire).
//
// nudge is non-nil only for the §10.8 nudge action: that action does not fire a
// plain ops.<lane> submit off operationType/authTarget/payload — it runs the
// Two-Phase Nudge protocol (claim → adapter execute → resolve op) over a minted
// claimId. operationType/authTarget/payload are unused for a nudge plan; the
// nudge carrier holds the adapter name, resolve op type, subject, and resolved
// params the protocol needs at fire time. fireEpisode branches on nudge != nil.
type plan struct {
	operationType string
	authTarget    string
	payload       func(markRevision uint64) map[string]any
	// reads is the dispatched op's ContextHint.Reads: the BARE vertex keys the
	// op's DDL script hydrates + validates (vertex_alive). The dispatcher
	// declares them because it builds the payload and so knows the exact keys
	// the op touches. Empty for read-free ops (StartLoomPattern, MarkExpired,
	// most directOps). NO `.state` suffixes — the DDLs read bare keys.
	reads []string
	nudge *nudgePlan
}

// nudgePlan carries one nudge gap's resolved §10.8 fields to the live dispatch
// path: the adapter name (config — never templated from the row), the resolve
// operation type (the §10.8 nudge action's Operation — a literal op type the
// resolve mutation submits under), the subject the external action concerns
// (templated), and the resolved params (each a literal or a row.<column>
// substitution). The claimId is NOT here — it is minted at fire time with the
// weaver-state mark CAS-create (§10.3) and threaded through the protocol.
type nudgePlan struct {
	adapter   string
	operation string
	subject   string
	params    map[string]string
}

// buildPlan resolves one open gap against its playbook entry: templated params
// are substituted from the row, and action-specific references (pattern → the
// live meta.loomPattern vertex; operation → the live op meta-vertex) resolve
// against the registry at dispatch time. expectedRevision is the candidate
// row's substrate per-key revision off the CDC message; every remediation op's
// payload carries it as the OCC revision-condition.
//
// The nudge action resolves into a nudgePlan (adapter/operation/subject/params)
// rather than a plain-submit plan: the live dispatch path runs the Two-Phase
// Nudge protocol over it (claim → adapter → resolve op) instead of a single
// ops.<lane> publish.
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
			payload: func(uint64) map[string]any {
				return map[string]any{
					"patternRef":       metaKey,
					"subjectKey":       subject,
					"expectedRevision": expectedRevision,
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
			payload: func(markRevision uint64) map[string]any {
				return map[string]any{
					"assignee":         assignee,
					"forOperation":     forOperation,
					"scopedTo":         taskTarget,
					"expiresAt":        substrate.FormatTimestamp(time.Now().Add(assignTaskGrantTTL)),
					"taskId":           deriveEpisodeTaskID(targetID, entityID, gapColumn, markRevision),
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
		return &plan{
			operationType: ga.Operation,
			authTarget:    authTarget,
			payload:       func(uint64) map[string]any { return params },
		}, nil

	case actionNudge:
		// The adapter name is config (the §10.8 GapAction.Adapter field), never
		// templated from the row — a blank one is a config error, and a row.
		// prefix is a config error too (mirrors directOp's operation guard).
		if ga.Adapter == "" {
			return nil, &planError{kind: errConfig, msg: "nudge requires an adapter"}
		}
		if strings.HasPrefix(ga.Adapter, rowTemplatePrefix) {
			return nil, &planError{kind: errConfig, msg: "nudge adapter must be a literal adapter name"}
		}
		// The resolve operation type is likewise config (the §10.8 nudge action's
		// Operation — the op type the resolve mutation submits under).
		if ga.Operation == "" {
			return nil, &planError{kind: errConfig, msg: "nudge requires an operation (the resolve op type)"}
		}
		if strings.HasPrefix(ga.Operation, rowTemplatePrefix) {
			return nil, &planError{kind: errConfig, msg: "nudge operation must be a literal operationType"}
		}
		subject, perr := resolveStringParam("subject", ga.Subject, row)
		if perr != nil {
			return nil, perr
		}
		params := make(map[string]string, len(ga.Params))
		for name, v := range ga.Params {
			resolved, perr := resolveStringParam(name, v, row)
			if perr != nil {
				return nil, perr
			}
			params[name] = resolved
		}
		return &plan{
			nudge: &nudgePlan{
				adapter:   ga.Adapter,
				operation: ga.Operation,
				subject:   subject,
				params:    params,
			},
		}, nil

	default:
		return nil, &planError{kind: errConfig, msg: fmt.Sprintf("unknown action %q", ga.Action)}
	}
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
