package pkgmgr

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
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

// Gap-companion column prefixes (Contract #10 §10.3). For gap column
// `missing_<g>` a lens may project `inflight_<g>` (a remediation is legitimately
// in flight) and `maxretries_<g>` (the gap's retry cap). Re-stated here so the
// installer enforces the §10.3 companion-pair rule without importing
// internal/weaver (the installer must not depend on an engine); pinned against
// the engine's own constants by TestGapCompanionPrefixes_MatchWeaverVocabulary.
const (
	inflightColumnPrefix   = "inflight_"
	maxretriesColumnPrefix = "maxretries_"
)

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

// staticallyExternalGapActions holds the §10.8 actions for which Contract #10
// §10.3's companion-pair rule is BOTH statically decidable and materially
// different from declaring nothing at all. It is a strict subset of the actions
// internal/weaver's externalDispatchGap classifies external-class outright, and
// each exclusion is a distinct reason:
//
//   - `directOp` is in. The engine's own gapSuppressed falls back to
//     defaultDirectOpRetryBudget for exactly one action — directOp — and only
//     while the row declares no inflight_<g>. So on a directOp gap the marker is
//     what DECLINES that default, and declaring it without a cap is strictly
//     worse than declaring neither: it converts a bounded gap into an unbounded
//     one. That is the harm §10.3 names, and it is decidable from the playbook.
//   - `proposedOp` is out, though the classifier does decide it external
//     outright. The default-budget fallback does not apply to it, so "neither
//     companion" and "inflight_<g> only" leave a proposedOp gap in the SAME
//     uncapped state — refusing the second while admitting the first would
//     refuse the better-documented of two identical outcomes (packages/augur's
//     shipped augurDispatch target occupies the admitted one). Its reclaim is
//     also collapse-paced, not fresh-attempt (the engine's collapseOnlyReclaim
//     lists it), so the §10.3 rationale does not reach it either.
//   - `triggerLoom` is out because its class is read from the referenced
//     pattern's own step kinds, and that reference may itself be a row.<column>
//     template only a live row resolves. The engine classifies an unresolvable
//     or unindexed pattern as NOT external (the fail-safe direction), so an
//     installer guessing the other way would produce a false refusal.
//   - `assignTask` and `surface` never make an external call at all.
//
// Re-stated rather than imported for the same reason as the action constants
// above; the membership and the subset relation are pinned against the engine's
// classifier by TestStaticallyExternalGapActions_MatchWeaverClassifier.
var staticallyExternalGapActions = map[string]bool{
	actionDirectOp: true,
}

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
		// Gaps is a Go map, whose range order is randomized per run. Two
		// offending gaps on one target would otherwise surface a different
		// refusal each time the author re-ran the install — the same authoring
		// bug reported under two different names. Sorted iteration makes the
		// gate's verdict reproducible.
		for _, col := range sortedGapColumns(t.Gaps) {
			ga := t.Gaps[col]
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
			if err := def.validateGapCompanionPair(idx, t, col, ga); err != nil {
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

// sortedGapColumns returns a target's gaps keys in a stable order, so a
// validation refusal over a multi-gap target names the same gap on every run.
func sortedGapColumns(gaps map[string]GapActionSpec) []string {
	cols := make([]string, 0, len(gaps))
	for col := range gaps {
		cols = append(cols, col)
	}
	sort.Strings(cols)
	return cols
}

// validateGapCompanionPair enforces Contract #10 §10.3 — "a gap that declares
// `inflight_<g>` MUST declare `maxretries_<g>`" — for the gaps whose class is
// decidable from the playbook alone (staticallyExternalGapActions, today
// directOp only).
//
// The harm it prevents is concrete. The engine's gapSuppressed falls back to its
// default directOp retry budget only while the row declares NO inflight_<g>: a
// lens that declares the marker has taken over pacing that gap, and the engine
// stops second-guessing it. Declaring the marker without a cap therefore leaves
// the gap with no bound of any kind — the dispatch-count can never reach a cap,
// so §10.8's `GapBudgetExhausted` (the loud stop that tells an operator a
// remediation is not converging) is structurally unreachable and the gap
// re-dispatches indefinitely, paced only by backoff and visible only as a
// counter. A gap that declares neither companion keeps the engine default and is
// bounded; declaring only the marker is strictly worse than declaring nothing.
//
// Declaration is read from the union of the feeding lens's Output.BodyColumns
// and Output.StaticEmptyColumns, because BOTH land in the row body the engine
// reads: Refractor's projection driver writes every BodyColumn into the envelope
// and then writes each StaticEmptyColumn as an empty array, and Weaver
// deserializes that envelope verbatim. A marker in StaticEmptyColumns is
// therefore a PRESENT inflight_<g> key at runtime, which is exactly what
// declines the engine's default budget — reading BodyColumns alone would let
// that shape through the gate untouched. The lens is resolved from the target's
// LensRef by canonicalName, mirroring resolveLensRef (build.go).
//
// Two absences are skipped rather than refused, deliberately. A LensRef that
// names no lens in this batch (an already-installed lens from another package,
// referenced by NanoID) and a lens with no Output descriptor (not an
// actor-aggregate) both leave the declaration outside what this installer can
// see, and a gate cannot validate what it cannot read — refusing on absence
// would fail every cross-package target. The runtime `uncappedExternal` backoff
// remains the backstop for everything static validation cannot reach: this gate
// reads DECLARATIONS, while the engine's gapSuppressed reads VALUES, and a lens
// may declare `maxretries_<g>` and still project null or zero into the row.
//
// col has already been checked to carry the gapColumnPrefix by the caller's
// loop, so the trimmed remainder is the gap name `<g>`.
func (def Definition) validateGapCompanionPair(targetIdx int, t WeaverTargetSpec, col string, ga GapActionSpec) error {
	if !staticallyExternalGapActions[ga.Action] {
		return nil
	}
	lens := def.lensByCanonicalName(t.LensRef)
	if lens == nil || lens.Output == nil {
		return nil
	}
	gap := strings.TrimPrefix(col, gapColumnPrefix)
	inflightCol, maxretriesCol := inflightColumnPrefix+gap, maxretriesColumnPrefix+gap
	declared := declaredRowBodyColumns(lens.Output)
	inflightIn, declaresInflight := declared[inflightCol]
	if _, declaresCap := declared[maxretriesCol]; !declaresInflight || declaresCap {
		return nil
	}
	return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q: lens %q declares row-body column %q (in %s) but no %q — action %q is external-class, and Contract #10 §10.3 requires the companion pair there: the declared marker takes the gap off the engine's default retry budget, so without a cap the dispatch count can never reach one, §10.8's GapBudgetExhausted can never fire, and the gap re-dispatches indefinitely with nothing telling an operator it is not converging. Declare %q in the lens's Output.BodyColumns, sized to what draining this gap can legitimately take (a StaticEmptyColumns entry projects an empty array, which the engine reads as no usable cap at all). Dropping %q instead is also a legal fix, but only because that hands the gap back to the engine's default retry budget — a real bound, not a way past this check",
		targetIdx, t.TargetID, col, lens.CanonicalName, inflightCol, inflightIn, maxretriesCol, ga.Action, maxretriesCol, inflightCol)
}

// declaredRowBodyColumns returns every column an actor-aggregate lens's Output
// descriptor puts into the projected row body, mapped to the descriptor field
// that declares it (so a refusal can point the author at the right list).
// Refractor's projection driver materializes BodyColumns and StaticEmptyColumns
// into the same envelope, so both are columns the Weaver sees; a name in both
// lists is attributed to BodyColumns, which is the one carrying a real value.
func declaredRowBodyColumns(out *OutputDescriptorSpec) map[string]string {
	declared := make(map[string]string, len(out.BodyColumns)+len(out.StaticEmptyColumns))
	for _, c := range out.StaticEmptyColumns {
		declared[c] = "Output.StaticEmptyColumns"
	}
	for _, c := range out.BodyColumns {
		declared[c] = "Output.BodyColumns"
	}
	return declared
}

// lensByCanonicalName resolves a WeaverTarget's LensRef to the lens this batch
// declares under that canonicalName, mirroring resolveLensRef's (build.go)
// canonicalName lookup — including its LAST-wins tie-break, since resolveLensRef
// reads a canonicalName→id map the batch build overwrites per duplicate. A
// Definition declaring one canonicalName twice is refused outright by
// validateCanonicalNameUniqueness, but that check runs after this one, so
// agreeing with the installer here keeps this gate from masking the real error
// with a refusal about a lens the install would never have bound.
//
// It returns nil when the ref names no lens here — an empty ref, or a NanoID
// naming a lens an already-installed package owns — which is the resolution this
// installer genuinely cannot see through, not an error: resolveLensRef returns
// early on an empty ref and passes a valid NanoID through verbatim.
func (def Definition) lensByCanonicalName(lensRef string) *LensSpec {
	if lensRef == "" {
		return nil
	}
	var found *LensSpec
	for i := range def.Lenses {
		if def.Lenses[i].CanonicalName == lensRef {
			found = &def.Lenses[i]
		}
	}
	return found
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
	return validateGapEnumerations(targetIdx, targetID, col, "", ga.Enumerations)
}

// validateLoomPatterns runs the §10.5 install-time validations on every
// declared LoomPatternSpec, fail-closed and pure. It validates pattern and step
// STRUCTURE (patternId/subjectType non-empty; ≥1 step; each step kind ∈
// {systemOp,userTask,externalTask}, with each kind's §10.5 shape enforced
// exactly — required fields present AND foreign fields absent: systemOp/userTask
// require a non-empty operation and forbid adapter/instanceOp/replyOp/params,
// externalTask requires adapter/instanceOp/replyOp and forbids operation, and
// only a systemOp may declare Reads/OptionalReads, whose entries must be
// subject-relative templates) plus
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
				if s.Kind == stepKindUserTask {
					if err := rejectStepReads(idx, p.PatternID, sIdx, s, "the engine derives a userTask's read-set from the CreateTask invariant"); err != nil {
						return err
					}
				} else {
					if err := validateStepReadTemplates(idx, p.PatternID, sIdx, "reads", s.Reads); err != nil {
						return err
					}
					if err := validateStepReadTemplates(idx, p.PatternID, sIdx, "optionalReads", s.OptionalReads); err != nil {
						return err
					}
					if err := validateStepEnumerations(idx, p.PatternID, sIdx, s.Enumerations); err != nil {
						return err
					}
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
				if err := rejectStepReads(idx, p.PatternID, sIdx, s, "an externalTask's read-set is inferred from its declared params"); err != nil {
					return err
				}
			default:
				return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: kind %q unsupported (systemOp | userTask | externalTask)",
					idx, p.PatternID, sIdx, s.Kind)
			}
		}
	}
	return nil
}

// stepSubjectToken is the root of a step's declared-read template grammar. It
// mirrors loom's subjectToken: the installer and the engine validate the same
// grammar so an install never admits a template the engine would reject at CDC
// load — the same lockstep validateLoomPatterns keeps with validate().
const stepSubjectToken = "subject"

// rejectStepReads refuses a declared read-set on a step kind whose read-set the
// engine derives for itself (userTask from the CreateTask invariant,
// externalTask from its params). Mirrors loom's rejectDeclaredReads.
func rejectStepReads(idx int, patternID string, sIdx int, s StepSpec, because string) error {
	for _, f := range []struct {
		name    string
		entries []string
	}{{"Reads", s.Reads}, {"OptionalReads", s.OptionalReads}} {
		if len(f.entries) != 0 {
			return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: %s is a systemOp-only field, not permitted on a %s step (%s)",
				idx, patternID, sIdx, f.name, s.Kind, because)
		}
	}
	if len(s.Enumerations) != 0 {
		return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: Enumerations is a systemOp-only field, not permitted on a %s step (%s)",
			idx, patternID, sIdx, s.Kind, because)
	}
	return nil
}

// validateStepEnumerations checks a systemOp step's declared kv.Links walks
// (Contract #2 §2.5 class (e)). The hub runs through the same subject-relative
// grammar as a declared read — it is a key, and the rendered-key charset
// argument applies to it identically — and the relation/direction pair is held
// to the shape the Processor's envelope parse enforces, so an install never
// admits a declaration the Processor would reject terminally on every
// redelivery. Mirrors loom's validateEnumerations.
func validateStepEnumerations(idx int, patternID string, sIdx int, ens []EnumerationSpec) error {
	for i, en := range ens {
		field := fmt.Sprintf("enumerations[%d].hub", i)
		if err := validateStepReadTemplates(idx, patternID, sIdx, field, []string{en.Hub}); err != nil {
			return err
		}
		if strings.TrimSpace(en.Relation) == "" {
			return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: enumerations[%d] requires a Relation",
				idx, patternID, sIdx, i)
		}
		if en.Direction != enumerationDirectionOut && en.Direction != enumerationDirectionIn {
			return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: enumerations[%d] Direction must be %q or %q, got %q",
				idx, patternID, sIdx, i, enumerationDirectionOut, enumerationDirectionIn, en.Direction)
		}
	}
	return nil
}

// Link directions a declared enumeration may name (Contract #2 §2.5): the hub
// is either the link's source or its target. The Processor rejects any other
// value at envelope parse, so install holds the same two; the pair is pinned
// against that parser by TestEnumerationDirections_MatchTheEnvelopeVocabulary.
const (
	enumerationDirectionOut = "out"
	enumerationDirectionIn  = "in"
)

// validateGapEnumerations checks a gap's declared kv.Links walks against the
// same envelope-parse shape (hub and relation non-empty, direction out|in).
// The hub's row.<column> template is resolved by the engine at dispatch time
// against a row this installer cannot see, so only its presence is checkable
// here — the same division validateGapAction already keeps for Reads.
//
// where names the sub-location within the gap ("" for the gap's own
// enumerations, `actions[<ref>]` for a planner catalog entry's). Both surfaces
// carry the field and both are validated at load by the engine, so both are
// validated here: a spec that installs clean and then fails engine validation
// takes the WHOLE target down at load — every gap on it, not just the entry
// with the bad direction.
func validateGapEnumerations(targetIdx int, targetID, col, where string, ens []EnumerationSpec) error {
	if where != "" {
		where = " " + where
	}
	for i, en := range ens {
		if strings.TrimSpace(en.Hub) == "" {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q%s enumerations[%d] requires a Hub",
				targetIdx, targetID, col, where, i)
		}
		if strings.TrimSpace(en.Relation) == "" {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q%s enumerations[%d] requires a Relation",
				targetIdx, targetID, col, where, i)
		}
		if en.Direction != enumerationDirectionOut && en.Direction != enumerationDirectionIn {
			return fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: gaps key %q%s enumerations[%d] Direction must be %q or %q, got %q",
				targetIdx, targetID, col, where, i, enumerationDirectionOut, enumerationDirectionIn, en.Direction)
		}
	}
	return nil
}

// validateStepReadTemplates checks a systemOp step's declared read-set against
// the subject-relative grammar the engine resolves at submit time: the bare
// `subject` token, or `subject.<aspect>` for one of its aspects, with the aspect
// held to the full Contract #1 localName — a rendered key is fetched with a NATS
// KV GET, whose charset rejects (as a hard error, not an absence) anything a
// bare dot-free check would admit. Mirrors loom's validateSubjectTemplates.
func validateStepReadTemplates(idx int, patternID string, sIdx int, field string, entries []string) error {
	for _, e := range entries {
		if e == stepSubjectToken {
			continue
		}
		aspect, ok := strings.CutPrefix(e, stepSubjectToken+".")
		if !ok {
			return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: %s entry %q must be %q or %q — a step's reads are subject-relative templates",
				idx, patternID, sIdx, field, e, stepSubjectToken, stepSubjectToken+".<aspect>")
		}
		if !substrate.IsValidLocalName(aspect) {
			return fmt.Errorf("pkgmgr: LoomPattern[%d] %q step %d: %s entry %q names %q, which is not a Contract #1 aspect localName",
				idx, patternID, sIdx, field, e, aspect)
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
