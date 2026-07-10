package pkgmgr

import (
	"fmt"
	"regexp"
	"strings"
)

// singleTokenPattern accepts a value usable as a single NATS KV key segment,
// subject token, and durable-name segment: no dots, no wildcards, no spaces.
// It mirrors the engines' install-time key-shape rule (weaver registry
// singleTokenPattern, loom step rules) so an unusable targetId / gap column /
// operationType fails loudly at install rather than at CDC load.
var singleTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// gapColumnPrefix is the §10.8 convention every weaver-target gaps key carries:
// each column is `missing_<gap>` (it becomes the third segment of the
// <targetId>.<entityId>.<gapColumn> mark key the engine writes).
const gapColumnPrefix = "missing_"

// reservedGapParam is the engine-owned playbook param a package may not set:
// the engine writes the OCC revision-condition under this key, so a package
// supplying it would collide with engine state. The engine's validateTarget
// rejects it at load; install rejects it first for a clearer author error.
const reservedGapParam = "expectedRevision"

// Loom step kinds (Contract #10 §10.5). Re-stated here so the installer
// validates patterns without importing internal/loom (the installer must not
// depend on an engine).
const (
	stepKindSystemOp     = "systemOp"
	stepKindUserTask     = "userTask"
	stepKindExternalTask = "externalTask"
)

// Gap action names (Contract #10 §10.8 action table). Re-stated here so the
// installer validates a target's per-gap action and its mandatory fields
// without importing internal/weaver. A package may not declare an action
// outside this set, and each action's required fields must be present (a
// row.<column> template token counts as present — install checks presence, the
// engine resolves the value live).
const (
	actionTriggerLoom = "triggerLoom"
	actionAssignTask  = "assignTask"
	actionDirectOp    = "directOp"
	// actionProposedOp is the Fire 2b dynamic-op action (Contract #10 §10.8
	// "Augur dispatch"): unlike the three static actions, its op + params come
	// from the violation ROW (an approved Augur proposal's proposedAction /
	// proposedParams), not playbook config, so it declares no static fields —
	// install-time validation requires nothing beyond the action name itself
	// (see validateGapAction). Reserved for the augur package's primordial
	// augurDispatch target; a package wiring it to any other target's row is an
	// authoring bug the engine's dispatch-time §5 re-validation does not exist
	// to catch (that check trusts the row came from a §5-validated proposal).
	actionProposedOp = "proposedOp"
	// actionSurface is FR29's "surface, never dispatch" gap (Contract #10
	// §10.8): raises/clears a named Health-KV issue while the gap is
	// open/closed, dispatching no op and creating no mark. Requires IssueCode;
	// IssueSeverity is optional (engine default: "warning").
	actionSurface = "surface"
)

// Augur escalation triggers (Contract #10 §10.8 "Augur escalation"). Re-stated
// here so the installer validates a target's optional `augur.escalate` set
// without importing internal/weaver, mirroring the engine's validateAugurPolicy.
const (
	escalateUnplannable = "unplannable"
	escalateExhausted   = "exhausted"
)

// validateWeaverTargets runs the §10.8 install-time validations on every
// declared WeaverTargetSpec, fail-closed and pure (no I/O) so it runs before
// any KV write. It mirrors the engine's validateTarget rules plus a
// package-local targetId-uniqueness check (cross-package collision is caught at
// runtime, but a package colliding with itself is an authoring bug worth
// failing fast). LensRef resolution happens during batch build (resolveLensRef
// in build.go), which needs the declared lens set and fails closed before any
// KV write.
func (def Definition) validateWeaverTargets() error {
	seen := make(map[string]int, len(def.WeaverTargets))
	for idx, t := range def.WeaverTargets {
		if t.TargetID == "" {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d]: TargetID is required", idx)
		}
		if !singleTokenPattern.MatchString(t.TargetID) {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: TargetID is not a valid single KV-key segment (must match %s — it becomes a weaver-targets key prefix and a durable-name segment, so dots are forbidden)",
				idx, t.TargetID, singleTokenPattern.String())
		}
		if prev, dup := seen[t.TargetID]; dup {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: duplicate TargetID (already declared by WeaverTarget[%d])",
				idx, t.TargetID, prev)
		}
		seen[t.TargetID] = idx
		if err := validateTargetMode(idx, t.TargetID, t.Mode); err != nil {
			return err
		}
		for col, ga := range t.Gaps {
			if !strings.HasPrefix(col, gapColumnPrefix) || col == gapColumnPrefix {
				return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q does not match the missing_<gap> column convention",
					idx, t.TargetID, col)
			}
			if !singleTokenPattern.MatchString(col) {
				return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q contains characters invalid in a KV key segment (it becomes the <targetId>.<entityId>.<gapColumn> mark-key segment; must match %s)",
					idx, t.TargetID, col, singleTokenPattern.String())
			}
			if _, reserved := ga.Params[reservedGapParam]; reserved {
				return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q param %q is reserved (the engine writes the OCC revision-condition under that payload field)",
					idx, t.TargetID, col, reservedGapParam)
			}
			// A goal-authored gap (R1) legitimately declares no top-level
			// Action — dispatch comes entirely from the Actions catalog via
			// goal regression (mirrors the engine: buildPlan's Mode==planned
			// branch falls through to the goal/candidates resolution exactly
			// when ga.Action == ""). validateGapAction's action-table check
			// only applies when an explicit Action is authored; a gap with
			// neither an Action nor a Goal has nothing telling the engine what
			// to do and is rejected here instead.
			if ga.Action != "" {
				if err := validateGapAction(idx, t.TargetID, col, ga); err != nil {
					return err
				}
			} else if len(ga.Goal) == 0 {
				return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q has no action and no goal (a gap must declare an explicit action or a goal-authored catalog)",
					idx, t.TargetID, col)
			}
			if err := validateGapPlannerFields(idx, t.TargetID, col, ga); err != nil {
				return err
			}
		}
		if err := def.validateAugurSpec(idx, t.TargetID, t.Augur); err != nil {
			return err
		}
		if err := validateAdmissionSpec(idx, t.TargetID, t.Admission); err != nil {
			return err
		}
	}
	return nil
}

// validateAugurSpec runs the §10.8 "Augur escalation" install-time validations
// on a target's optional augur block, mirroring the engine's validateAugurPolicy.
// A nil block is the frozen-contract default (always valid). When present: at
// least one escalate trigger (each ∈ {unplannable, exhausted}); the optional
// Op/Adapter/ReplyOp/Model overrides, when set, are single tokens (Option F —
// Weaver dispatches the reasoning op directly as a directOp, so there is NO loom
// pattern to resolve; the op / adapter / replyOp default at dispatch when
// omitted, and model is a bare adapter override token);
// autoApply.actions ⊆ the §10.8 action table; minConfidence ∈ [0,1].
func (def Definition) validateAugurSpec(targetIdx int, targetID string, a *AugurSpec) error {
	if a == nil {
		return nil
	}
	if len(a.Escalate) == 0 {
		return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: augur block present but escalate is empty (omit the block to disable escalation, or list a trigger)",
			targetIdx, targetID)
	}
	for _, trig := range a.Escalate {
		if trig != escalateUnplannable && trig != escalateExhausted {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: augur.escalate value %q is not a known trigger (%s | %s)",
				targetIdx, targetID, trig, escalateUnplannable, escalateExhausted)
		}
	}
	for field, v := range map[string]string{"op": a.Op, "adapter": a.Adapter, "replyOp": a.ReplyOp, "model": a.Model} {
		if v != "" && !singleTokenPattern.MatchString(v) {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: augur.%s value %q must be a single token matching %s",
				targetIdx, targetID, field, v, singleTokenPattern.String())
		}
	}
	if a.AutoApply != nil {
		for _, act := range a.AutoApply.Actions {
			if act != actionTriggerLoom && act != actionAssignTask && act != actionDirectOp {
				return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: augur.autoApply.actions value %q is not a known action (%s | %s | %s)",
					targetIdx, targetID, act, actionTriggerLoom, actionAssignTask, actionDirectOp)
			}
		}
		if a.AutoApply.MinConfidence < 0 || a.AutoApply.MinConfidence > 1 {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: augur.autoApply.minConfidence %v is out of range (must be in [0,1])",
				targetIdx, targetID, a.AutoApply.MinConfidence)
		}
	}
	return nil
}

// validateAdmissionSpec runs the §10.8 "Admission control" install-time
// validations on a target's optional admission block (Fire 8), mirroring the
// engine's validateAdmissionPolicy (internal/weaver/registry.go) so a package
// that would fail the engine's own CDC-load validation fails loudly at install
// instead. A nil block is the default (unbounded dispatch) and always valid. A
// present block must declare at least one strictly positive rate — an empty
// block is exactly as inert as omitting it and almost certainly an authoring
// mistake, so it is rejected rather than silently accepted as a no-op.
func validateAdmissionSpec(targetIdx int, targetID string, a *AdmissionSpec) error {
	if a == nil {
		return nil
	}
	if a.GlobalRate < 0 {
		return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: admission.globalRate %v must be >= 0 (0 means not declared)",
			targetIdx, targetID, a.GlobalRate)
	}
	declared := a.GlobalRate > 0
	for adapter, rate := range a.AdapterRates {
		if adapter == "" {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: admission.adapterRates has an empty adapter key",
				targetIdx, targetID)
		}
		if rate <= 0 {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: admission.adapterRates[%q] %v must be > 0 (omit the entry to leave that adapter ungoverned)",
				targetIdx, targetID, adapter, rate)
		}
		declared = true
	}
	if !declared {
		return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: admission block present but declares no positive rate (omit the block to leave the target unbounded)",
			targetIdx, targetID)
	}
	return nil
}

// validateGapAction fails closed on a gap's remediation action: the Action must
// be one of the §10.8 action table names, and the action's mandatory fields
// must be non-empty. A row.<column> template token is non-empty, so this checks
// presence only — the engine resolves the literal-or-template value live at
// dispatch. The required-field set mirrors the engine's dispatch-time
// requirements (internal/weaver/strategist.go buildPlan): triggerLoom needs
// Pattern + Subject, assignTask needs Operation + Assignee + Target, directOp
// needs Operation.
func validateGapAction(targetIdx int, targetID, col string, ga GapActionSpec) error {
	missing := func(field string) error {
		return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q action %q requires field %q",
			targetIdx, targetID, col, ga.Action, field)
	}
	switch ga.Action {
	case actionTriggerLoom:
		if ga.Pattern == "" {
			return missing("Pattern")
		}
		if ga.Subject == "" {
			return missing("Subject")
		}
	case actionAssignTask:
		if ga.Operation == "" {
			return missing("Operation")
		}
		if ga.Assignee == "" {
			return missing("Assignee")
		}
		if ga.Target == "" {
			return missing("Target")
		}
	case actionDirectOp:
		if ga.Operation == "" {
			return missing("Operation")
		}
	case actionProposedOp:
		// Sourced entirely from the row (an approved Augur proposal) — no static
		// field is required or meaningful; a package setting one anyway is
		// harmless (buildProposedOpPlan never reads it) but not validated here.
	case actionSurface:
		if ga.IssueCode == "" {
			return missing("IssueCode")
		}
		if ga.IssueSeverity != "" && ga.IssueSeverity != "warning" && ga.IssueSeverity != "error" {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q action %q issueSeverity %q must be \"warning\" or \"error\" (omit for the \"warning\" default)",
				targetIdx, targetID, col, ga.Action, ga.IssueSeverity)
		}
	default:
		return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q action %q is not a known action (triggerLoom | assignTask | directOp | proposedOp | surface)",
			targetIdx, targetID, col, ga.Action)
	}
	return nil
}

// validateLoomPatterns runs the §10.5 install-time validations on every
// declared LoomPatternSpec, fail-closed and pure. It validates pattern and step
// STRUCTURE (patternId/subjectType non-empty; ≥1 step; each step kind ∈
// {systemOp,userTask,externalTask}, with each kind's §10.5 shape enforced
// exactly — required fields present AND foreign fields absent: systemOp/userTask
// require a non-empty operation and forbid adapter/instanceOp/replyOp/params,
// externalTask requires adapter/instanceOp/replyOp and forbids operation) plus
// a package-local patternId-uniqueness check (two patterns
// minting the same create-only loomPattern key would collide on an opaque
// conflict). It mirrors the engine's validate() exactly so an install never
// admits a pattern the engine would reject at CDC load. Step Guard bodies are
// author-supplied maps validated by the engine at CDC load; the installer does
// not interpret guard grammar.
func (def Definition) validateLoomPatterns() error {
	seen := make(map[string]int, len(def.LoomPatterns))
	for idx, p := range def.LoomPatterns {
		if strings.TrimSpace(p.PatternID) == "" {
			return fmt.Errorf("pkgmgr: LoomPattern[%d]: PatternID is required", idx)
		}
		if prev, dup := seen[p.PatternID]; dup {
			return fmt.Errorf("pkgmgr: LoomPattern[%d] %q: duplicate PatternID (already declared by LoomPattern[%d])",
				idx, p.PatternID, prev)
		}
		seen[p.PatternID] = idx
		if strings.TrimSpace(p.SubjectType) == "" {
			return fmt.Errorf("pkgmgr: LoomPattern[%d] %q: SubjectType is required", idx, p.PatternID)
		}
		if len(p.Steps) == 0 {
			return fmt.Errorf("pkgmgr: LoomPattern[%d] %q: at least one step is required", idx, p.PatternID)
		}
		for sIdx, s := range p.Steps {
			switch s.Kind {
			case stepKindSystemOp, stepKindUserTask:
				if strings.TrimSpace(s.Operation) == "" {
					return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: operation is required", idx, p.PatternID, sIdx)
				}
				if strings.TrimSpace(s.Adapter) != "" {
					return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: adapter is an externalTask-only field, not permitted on a %s step", idx, p.PatternID, sIdx, s.Kind)
				}
				if strings.TrimSpace(s.InstanceOp) != "" {
					return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: instanceOp is an externalTask-only field, not permitted on a %s step", idx, p.PatternID, sIdx, s.Kind)
				}
				if strings.TrimSpace(s.ReplyOp) != "" {
					return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: replyOp is an externalTask-only field, not permitted on a %s step", idx, p.PatternID, sIdx, s.Kind)
				}
				if len(s.Params) != 0 {
					return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: params is an externalTask-only field, not permitted on a %s step", idx, p.PatternID, sIdx, s.Kind)
				}
			case stepKindExternalTask:
				if strings.TrimSpace(s.Adapter) == "" {
					return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: adapter is required for externalTask", idx, p.PatternID, sIdx)
				}
				if strings.TrimSpace(s.InstanceOp) == "" {
					return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: instanceOp is required for externalTask", idx, p.PatternID, sIdx)
				}
				if strings.TrimSpace(s.ReplyOp) == "" {
					return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: replyOp is required for externalTask", idx, p.PatternID, sIdx)
				}
				if strings.TrimSpace(s.Operation) != "" {
					return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: operation is a systemOp/userTask-only field, not permitted on an externalTask step", idx, p.PatternID, sIdx)
				}
			default:
				return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: kind %q unsupported (systemOp | userTask | externalTask)",
					idx, p.PatternID, sIdx, s.Kind)
			}
		}
	}
	return nil
}

// validateOpMetas checks every declared OpMetaSpec carries a non-empty,
// single-token OperationType, fail-closed and pure, plus a package-local
// OperationType-uniqueness check (two op-metas minting the same create-only
// opMeta key would collide on an opaque conflict).
func (def Definition) validateOpMetas() error {
	seen := make(map[string]int, len(def.OpMetas))
	for idx, o := range def.OpMetas {
		if o.OperationType == "" {
			return fmt.Errorf("pkgmgr: OpMeta[%d]: OperationType is required", idx)
		}
		if !singleTokenPattern.MatchString(o.OperationType) {
			return fmt.Errorf("pkgmgr: OpMeta[%d] %q: OperationType is not a valid single token (must match %s)",
				idx, o.OperationType, singleTokenPattern.String())
		}
		if prev, dup := seen[o.OperationType]; dup {
			return fmt.Errorf("pkgmgr: OpMeta[%d] %q: duplicate OperationType (already declared by OpMeta[%d])",
				idx, o.OperationType, prev)
		}
		seen[o.OperationType] = idx
	}
	return nil
}
