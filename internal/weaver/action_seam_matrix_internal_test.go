package weaver

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	nats "github.com/nats-io/nats.go"
)

// The action × seam gate.
//
// Every value target.Gaps[col].Action can hold must be CLASSIFIED at every seam
// that can reach planGap: the seam either builds a plan for it, or refuses it
// before planGap runs. There is no third option, because buildPlan's `default:`
// arm turns an action it has no case for into a standing PlaybookConfigError —
// a Health alert whose subject is a contract-legal package declaration, which is
// a false alarm rather than a diagnostic — and declines the row on the long
// redelivery floor for as long as the alert stands.
//
// The two axes are crossed here rather than guarded one point at a time:
//
//   - the ACTION axis is playbookActionSeamMatrix, and
//     TestPlaybookActionVocabulary_CoversEveryDeclaredActionConstant parses this
//     package's own const declarations so a NEW action* constant fails the suite
//     until its author classifies it at every seam below;
//   - the SEAM axis is actionSeams — dispatchGap (lane 1), the sweeper's
//     reclaim, escalateExhaustedGap, and the sweep's count-leg re-arm — derived
//     from the planGap call sites in evaluator.go and reconciler.go.
//
// Each cell asserts three things. The seam publishes exactly the ops the matrix
// says it must (none, for a cell the seam is required to refuse). No
// `error`-severity issue is raised — a `warning` about genuinely absent data is
// ordinary, but an `error` naming the action itself is the defect this gate
// exists for. And a per-cell SENTINEL issue, planted at the gap-config key
// planGap clears on a successful plan, is cleared for a dispatching cell and
// left standing for a refused one — the witness that the refusal happened
// BEFORE planGap rather than inside it.
//
// Every refusing cell is driven in the same pass, over the same shape, as a
// CONTROL gap that differs from it in one field (its action is directOp): the
// control's dispatch is the proof that the vector reached the seam and would
// have reached planGap, so a cell can never pass by being inert.

// planTimeResolvedAction is the playbook shape that names no action at all: a
// planned/goal-mode gap whose dispatchable action only a plan can produce
// (resolvePlannedAction). No constant declares it — there is nothing to name —
// but every seam meets it exactly where the declared actions arrive, so the
// matrix crosses it like any other value.
const planTimeResolvedAction = ""

// seamGateAdvice is the instruction a failure of this gate hands its reader. It
// is attached to every cell assertion because the fix is never local to the
// assertion that fired.
const seamGateAdvice = "\n\nEvery playbook action must be CLASSIFIED at every seam that can reach planGap: " +
	"the seam either builds a plan for it, or refuses it BEFORE planGap. buildPlan's `default:` arm " +
	"turns an unclassified action into a standing PlaybookConfigError against a declaration " +
	"that is entirely contract-legal, and declines the row on the long redelivery floor while it stands." +
	"\nIf you added an action: give buildPlan a case for it, or guard it " +
	"at each seam the way surfaceOnlyGap guards `surface`, then record the verdict for every seam in " +
	"playbookActionSeamMatrix (internal/weaver/action_seam_matrix_internal_test.go).\nIf you added a SEAM " +
	"that calls planGap: add it to actionSeams in the same file."

// --- the action axis --------------------------------------------------------

// actionSeamSpec is one row of the action axis: a contract-legal playbook
// declaration for one action, the row columns its dispatch reads, and the
// operationTypes each seam must publish for it (nil = the seam must refuse it
// before planGap).
type actionSeamSpec struct {
	// action is the value target.Gaps[col].Action holds.
	action string
	// constName is the identifier declaring action in this package, or "" for
	// the plan-time-resolved empty form, which no constant declares. The
	// vocabulary-drift test compares this set against the package's own consts.
	constName string
	// label names the cell in failure messages.
	label string
	// gapColumn is this action's own missing_<g> column, so one target hosts
	// the whole axis at once and every Health issue key names its cell.
	gapColumn string
	// buildPlanHandles reports whether buildPlan has a case for this action.
	// False means every seam MUST refuse it upstream — the property
	// TestBuildPlanDefaultArm_ReachableOnlyForAnUnhandledAction pins directly.
	buildPlanHandles bool
	// mode is the target's planner mode this declaration is legal under.
	mode string
	// gap builds the playbook declaration.
	gap func(f matrixFixture) GapAction
	// rowColumns are the extra row columns this action's dispatch reads.
	rowColumns func(f matrixFixture) map[string]any
	// markAction is the action recorded on an in-flight mark for this gap — for
	// `surface`, the DIFFERENT action a package upgrade replaced, which is the
	// only way a surface column ever comes to hold a mark at all.
	markAction string

	dispatchOps []string // seam: dispatchGap (lane 1)
	reclaimOps  []string // seam: sweeper.reclaim
	escalateOps []string // seam: escalateExhaustedGap
	reArmOps    []string // seam: sweepCount's re-arm arm
}

// matrixFixture carries the live registry references a declaration resolves
// against: a pattern that parks on a human, an op meta-vertex for assignTask's
// forOperation, and the trusted candidate an Augur proposal is scoped to.
type matrixFixture struct {
	patternRef string
	assignee   string
	taskOp     string
	candidate  string
}

// playbookActionSeamMatrix is the SINGLE place this package's tests enumerate
// the playbook action vocabulary.
var playbookActionSeamMatrix = []actionSeamSpec{
	{
		action:    actionSurface,
		constName: "actionSurface",
		label:     "surface",
		gapColumn: "missing_surface",
		// FR29's "surface, never dispatch": handled entirely in dispatchGap,
		// upstream of buildPlan, which has no case for it. The other three seams
		// meet it only through state a package upgrade stranded (a mark, a
		// dispatch-count) and must refuse it.
		buildPlanHandles: false,
		gap: func(matrixFixture) GapAction {
			// IssueSeverity is pinned to `warning` rather than left to default
			// so the cell's own declared issue can never be confused with the
			// `error` this gate hunts. A package may legally declare `error`
			// here; that issue is the gap's subject matter, not a seam's verdict
			// on the action.
			return GapAction{Action: actionSurface, IssueCode: "MatrixSurface", IssueSeverity: "warning"}
		},
		markAction:  actionDirectOp,
		dispatchOps: nil,
		reclaimOps:  nil,
		escalateOps: nil,
		reArmOps:    nil,
	},
	{
		action:           actionDirectOp,
		constName:        "actionDirectOp",
		label:            "directOp",
		gapColumn:        "missing_directop",
		buildPlanHandles: true,
		gap: func(matrixFixture) GapAction {
			return GapAction{Action: actionDirectOp, Operation: "FixMatrixSubject"}
		},
		markAction:  actionDirectOp,
		dispatchOps: []string{"FixMatrixSubject"},
		reclaimOps:  []string{"FixMatrixSubject"},
		escalateOps: []string{defaultAugurOp},
		reArmOps:    []string{"FixMatrixSubject"},
	},
	{
		action:           actionTriggerLoom,
		constName:        "actionTriggerLoom",
		label:            "triggerLoom",
		gapColumn:        "missing_triggerloom",
		buildPlanHandles: true,
		gap: func(f matrixFixture) GapAction {
			return GapAction{Action: actionTriggerLoom, Pattern: f.patternRef, Subject: "row.entityKey"}
		},
		markAction:  actionTriggerLoom,
		dispatchOps: []string{opStartLoomPattern},
		reclaimOps:  []string{opStartLoomPattern},
		escalateOps: []string{defaultAugurOp},
		// The fixture pattern parks on a human, so the re-arm's
		// collapseOnlyReclaim classifier refuses it: a markless episode may
		// still be open, and a fresh dispatch would mint a duplicate.
		reArmOps: nil,
	},
	{
		action:           actionAssignTask,
		constName:        "actionAssignTask",
		label:            "assignTask",
		gapColumn:        "missing_assigntask",
		buildPlanHandles: true,
		gap: func(f matrixFixture) GapAction {
			return GapAction{Action: actionAssignTask, Operation: f.taskOp,
				Assignee: f.assignee, Target: "row.entityKey"}
		},
		markAction:  actionAssignTask,
		dispatchOps: []string{opCreateTask},
		reclaimOps:  []string{opCreateTask},
		escalateOps: []string{defaultAugurOp},
		// Collapse-only for the same reason: a userTask outlives its mark.
		reArmOps: nil,
	},
	{
		action:           actionProposedOp,
		constName:        "actionProposedOp",
		label:            "proposedOp",
		gapColumn:        "missing_proposedop",
		buildPlanHandles: true,
		gap: func(matrixFixture) GapAction {
			return GapAction{Action: actionProposedOp}
		},
		rowColumns: func(f matrixFixture) map[string]any {
			// The augurDispatchPending lens columns buildProposedOpPlan resolves
			// the dispatch from: an approved proposal whose materialised inner
			// action is a directOp scoped to the trusted candidate.
			return map[string]any{
				"proposedAction": actionDirectOp,
				"proposedParams": map[string]any{
					"operation": "FixMatrixProposed",
					"target":    f.candidate,
					"params":    map[string]any{"status": "leased"},
				},
				"candidateKey":  f.candidate,
				"targetMetaKey": "vtx.meta.AAmatrixTargetHJKMNPQ",
			}
		},
		markAction:  actionProposedOp,
		dispatchOps: []string{"FixMatrixProposed", opRecordProposalDispatch},
		reclaimOps:  []string{"FixMatrixProposed", opRecordProposalDispatch},
		escalateOps: []string{defaultAugurOp},
		// The re-arm row declares inflight_<g>, so staleMark confirms the call
		// concluded and §10.3 makes the re-dispatch a genuinely fresh attempt.
		reArmOps: []string{"FixMatrixProposed", opRecordProposalDispatch},
	},
	{
		action:    planTimeResolvedAction,
		constName: "",
		label:     "plan-time-resolved",
		gapColumn: "missing_plantime",
		// Nothing reaches buildPlan under this value: resolvePlannedAction
		// materialises the picked candidate first, and every seam that cannot
		// run a plan must refuse the gap instead of handing the empty action on.
		buildPlanHandles: false,
		mode:             targetModePlanned,
		gap: func(matrixFixture) GapAction {
			return GapAction{Candidates: []GapCandidate{
				{Action: actionDirectOp, Operation: "SendMatrixReminder", Cost: 0},
			}}
		},
		// A reclaim re-dispatches the mark's PINNED candidate, never a fresh
		// rank — so the mark records the candidate's own action.
		markAction:  actionDirectOp,
		dispatchOps: []string{"SendMatrixReminder"},
		reclaimOps:  []string{"SendMatrixReminder"},
		escalateOps: []string{defaultAugurOp},
		// The re-arm refuses it: only a plan could say what it would fire, and
		// running one would spend an admission token and clear the gap's
		// standing issues for a dispatch that may never happen.
		reArmOps: nil,
	},
}

// --- the seam axis ----------------------------------------------------------

// actionSeam is one seam that can reach planGap: a driver that runs the WHOLE
// action axis once alongside a control, the selector naming what that seam owes
// each action, and the op the control dispatches there.
type actionSeam struct {
	name string
	// controlOp is what the control gap — a directOp declaration under the same
	// vector as every cell — publishes at this seam.
	controlOp string
	want      func(spec actionSeamSpec) []string
	run       func(t *testing.T, ctx context.Context, seam actionSeam) *seamOutcome
}

var actionSeams = []actionSeam{
	{
		name:      "dispatchGap",
		controlOp: matrixControlOp,
		want:      func(s actionSeamSpec) []string { return s.dispatchOps },
		run:       runDispatchGapSeam,
	},
	{
		name:      "reclaim",
		controlOp: matrixControlOp,
		want:      func(s actionSeamSpec) []string { return s.reclaimOps },
		run:       runReclaimSeam,
	},
	{
		// The escalation dispatches the augur reasoning op whatever the gap's
		// own action is — the seam consults that action for one thing only, the
		// guard — so the control publishes the same op every planned cell does.
		name:      "escalateExhaustedGap",
		controlOp: defaultAugurOp,
		want:      func(s actionSeamSpec) []string { return s.escalateOps },
		run:       runEscalateSeam,
	},
	{
		name:      "sweepCountReArm",
		controlOp: matrixControlOp,
		want:      func(s actionSeamSpec) []string { return s.reArmOps },
		run:       runCountLegReArmSeam,
	},
}

// TestPlaybookActionSeamMatrix crosses every playbook action with every seam
// that can reach planGap.
func TestPlaybookActionSeamMatrix(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, seam := range actionSeams {
		t.Run(seam.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			checkSeamOutcome(t, seam, seam.run(t, ctx, seam))
		})
	}
}

// checkSeamOutcome applies the three per-cell assertions to one seam's pass.
func checkSeamOutcome(t *testing.T, seam actionSeam, out *seamOutcome) {
	t.Helper()

	// Setup proof: the control gap — one field different from every refused
	// cell — dispatched. Without it a refusal proves nothing, because an inert
	// leg refuses everything.
	if got := out.cellOps[matrixControlGap]; !sameOrder(got, []string{seam.controlOp}) {
		t.Fatalf("seam %s: the control gap published %v, want [%s] — the seam did not run, "+
			"so every refusal below would hold vacuously (all ops: %v)%s",
			seam.name, got, seam.controlOp, out.ops, seamGateAdvice)
	}
	if out.sentinel[matrixControlGap] {
		t.Fatalf("seam %s: the control gap left its sentinel standing, so this seam's ops did not "+
			"come through planGap and the witness below means nothing%s", seam.name, seamGateAdvice)
	}

	for _, spec := range playbookActionSeamMatrix {
		wantOps := seam.want(spec)
		gotOps := out.cellOps[spec.gapColumn]
		if !sameOrder(gotOps, wantOps) {
			t.Errorf("cell %s/%s published %v, want %v%s",
				seam.name, spec.label, gotOps, wantOps, seamGateAdvice)
		}

		// The before-planGap witness: planGap clears the gap-config key on a
		// successful plan, so a refused cell must still find its sentinel while
		// a dispatching one must not.
		if len(wantOps) == 0 {
			if !out.sentinel[spec.gapColumn] {
				t.Errorf("cell %s/%s: its sentinel at the gap-config key is gone — the seam reached "+
					"planGap for an action it must refuse UPSTREAM of it%s",
					seam.name, spec.label, seamGateAdvice)
			}
			continue
		}
		if out.sentinel[spec.gapColumn] {
			t.Errorf("cell %s/%s dispatched %v but left its sentinel standing — the ops did not come "+
				"through planGap, so this cell is not exercising the seam it claims to%s",
				seam.name, spec.label, gotOps, seamGateAdvice)
		}
	}

	// The defect itself: an `error`-severity issue naming a contract-legal
	// declaration. Attribute it to its cell through the gap column its message
	// carries, so the failure says which classification is missing.
	for _, issue := range out.raised {
		t.Errorf("seam %s raised an `error`-severity issue over a contract-legal playbook (%s: %s — %s)%s",
			seam.name, cellOf(issue.Message), issue.Code, issue.Message, seamGateAdvice)
	}
}

// cellOf names the matrix cell an issue message implicates, for a failure
// message that points at the classification to add rather than at a key.
func cellOf(message string) string {
	for _, spec := range playbookActionSeamMatrix {
		if strings.Contains(message, spec.gapColumn) {
			return "cell " + spec.label
		}
	}
	if strings.Contains(message, matrixControlGap) {
		return "the control gap"
	}
	return "unattributed"
}

// --- buildPlan's default arm: the failure's actual mouth ---------------------

// TestBuildPlanDefaultArm_ReachableOnlyForAnUnhandledAction pins the arm every
// unclassified action falls into. For each action the matrix declares handled,
// buildPlan must never answer `unknown action` — and for each it declares
// unhandled, it must, which is precisely why every seam has to refuse those
// upstream. A value outside the vocabulary entirely is the arm's one legitimate
// subject.
func TestBuildPlanDefaultArm_ReachableOnlyForAnUnhandledAction(t *testing.T) {
	t.Parallel()
	f := matrixFixture{
		patternRef: "matrixUnindexedPattern",
		assignee:   "vtx.identity.AAmatrixStaffHJKMNP",
		taskOp:     "ApproveMatrixSubject",
		candidate:  dpCandidate,
	}
	row := map[string]any{"entityKey": "vtx.leaseApp.AAmatrixEntityHJKMN"}
	// An empty live registry: every action's reference resolution answers "not
	// loaded", which is a TRANSIENT plan error and never the default arm — so
	// what this test observes is the switch's own classification, uncoloured by
	// whichever pattern or op vertex happened to be indexed.
	source := newTestSource(t)

	for _, spec := range playbookActionSeamMatrix {
		ga := spec.gap(f)
		// The matrix's declaration is authored for a SEAM, which resolves a
		// planned gap's candidate first; buildPlan is handed the raw action, so
		// the plan-time form arrives here exactly as an unresolved gap would.
		_, perr := buildPlan(source, "matrixBuildPlan", "AAmatrixEntityHJKMN", spec.gapColumn, ga, row, 7)
		defaulted := perr != nil && strings.Contains(perr.msg, "unknown action")
		if spec.buildPlanHandles && defaulted {
			t.Errorf("action %q is recorded as handled by buildPlan but fell to its default arm: %s%s",
				spec.action, perr.msg, seamGateAdvice)
		}
		if !spec.buildPlanHandles && !defaulted {
			t.Errorf("action %q is recorded as UNHANDLED by buildPlan, which is what obliges every seam "+
				"to refuse it upstream — but buildPlan now answers %v. Re-classify it in "+
				"playbookActionSeamMatrix%s", spec.action, perr, seamGateAdvice)
		}
	}

	_, perr := buildPlan(source, "matrixBuildPlan", "AAmatrixEntityHJKMN", "missing_x",
		GapAction{Action: "harvestOrgans"}, row, 7)
	if perr == nil || perr.kind != errConfig || !strings.Contains(perr.msg, "unknown action") {
		t.Fatalf("an action outside the vocabulary must reach buildPlan's default arm, got %v%s",
			perr, seamGateAdvice)
	}
}

// --- the vocabulary-drift gate ----------------------------------------------

// TestPlaybookActionVocabulary_CoversEveryDeclaredActionConstant makes a new
// action fail loudly. Go constants are not enumerable at runtime, so the check
// reads this package's own declarations: every `action<Name>` string constant
// must appear in playbookActionSeamMatrix under that exact identifier, and the
// matrix may not invent one the package does not declare.
func TestPlaybookActionVocabulary_CoversEveryDeclaredActionConstant(t *testing.T) {
	t.Parallel()
	declared := declaredActionConstants(t)

	recorded := make(map[string]string, len(playbookActionSeamMatrix))
	for _, spec := range playbookActionSeamMatrix {
		if spec.constName == "" {
			if spec.action != planTimeResolvedAction {
				t.Fatalf("matrix entry %q declares no constant but is not the plan-time-resolved form", spec.label)
			}
			continue
		}
		if prior, dup := recorded[spec.constName]; dup {
			t.Fatalf("matrix records %s twice (%q and %q)", spec.constName, prior, spec.action)
		}
		recorded[spec.constName] = spec.action
	}

	for name, value := range declared {
		got, ok := recorded[name]
		if !ok {
			t.Errorf("the package declares %s = %q but playbookActionSeamMatrix does not carry it.\n"+
				"A new playbook action is not finished until it is classified at EVERY seam that can "+
				"reach planGap%s", name, value, seamGateAdvice)
			continue
		}
		if got != value {
			t.Errorf("the matrix records %s as %q but the package declares it as %q", name, got, value)
		}
	}
	for name := range recorded {
		if _, ok := declared[name]; !ok {
			t.Errorf("playbookActionSeamMatrix records %s, which this package no longer declares — "+
				"drop the entry and its per-seam classifications", name)
		}
	}
}

// declaredActionConstants parses the package's non-test sources for the
// `action<Name>` string constants that make up the playbook action vocabulary.
func declaredActionConstants(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, s := range gen.Specs {
				spec, ok := s.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, ident := range spec.Names {
					if !isActionConstName(ident.Name) || i >= len(spec.Values) {
						continue
					}
					lit, ok := spec.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Fatalf("%s declares %s with a non-literal value; the vocabulary gate can only "+
							"read string literals%s", name, ident.Name, seamGateAdvice)
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("%s: unquote %s: %v", name, ident.Name, err)
					}
					out[ident.Name] = value
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no action constants found: the vocabulary gate is reading the wrong sources")
	}
	return out
}

func isActionConstName(name string) bool {
	rest, ok := strings.CutPrefix(name, "action")
	return ok && rest != "" && unicode.IsUpper(rune(rest[0]))
}

// --- seam drivers -----------------------------------------------------------

const (
	// matrixControlGap is the control every seam runs alongside the action
	// axis: the same vector, one field different (an action that dispatches).
	matrixControlGap = "missing_control"
	matrixControlOp  = "FixMatrixControl"
	// matrixSentinelCode marks the issue planted at each gap's config key —
	// planGap clears that key on a successful plan, so the sentinel's fate says
	// whether the seam reached planGap for that gap.
	matrixSentinelCode = "MatrixSentinel"
	// matrixBudget is the declared retry cap the count-anchored vectors carry.
	matrixBudget = 3
)

// seamOutcome is one seam's whole pass: what each cell published, whether each
// cell's before-planGap witness survived, and what the seam left on Health.
type seamOutcome struct {
	targetID string
	target   *Target
	entity   map[string]string
	// ops is every operationType the seam published, in publication order.
	ops []string
	// cellOps attributes those ops to the gap column that produced them.
	cellOps map[string][]string
	// sentinel reports, per gap column, whether its planted witness survived.
	sentinel map[string]bool
	// raised accumulates every `error`-severity issue seen while the seam ran.
	// It is collected per cell rather than read once at the end because lane-1's
	// level reconcile retires the config issue of every gap column the delivered
	// row does not carry — so a sibling cell's delivery would otherwise erase
	// the very alert this gate exists to catch.
	raised []healthIssue
	issues *issueCache
}

// newSeamOutcome builds the target hosting the whole action axis plus the
// control, and mints an entity per cell.
func newSeamOutcome(t *testing.T, targetID string, e *Engine, f matrixFixture) *seamOutcome {
	t.Helper()
	gaps := map[string]GapAction{matrixControlGap: {Action: actionDirectOp, Operation: matrixControlOp}}
	mode := ""
	entity := map[string]string{matrixControlGap: testNanoID(t)}
	for _, spec := range playbookActionSeamMatrix {
		gaps[spec.gapColumn] = spec.gap(f)
		entity[spec.gapColumn] = testNanoID(t)
		if spec.mode != "" {
			mode = spec.mode
		}
	}
	return &seamOutcome{
		targetID: targetID,
		target: &Target{
			TargetID: targetID,
			// The planner mode a candidates-only declaration needs. Every
			// explicit action passes through resolvePlannedAction unchanged
			// under it, so the other cells dispatch exactly as they would on a
			// mode-less target.
			Mode: mode,
			Gaps: gaps,
			// The exhausted-trigger policy escalateExhaustedGap dispatches under.
			Augur: &AugurPolicy{Escalate: []string{escalateExhausted}},
		},
		entity:   entity,
		cellOps:  map[string][]string{},
		sentinel: map[string]bool{},
		issues:   e.issues,
	}
}

// plant puts one cell's before-planGap witness at the gap-config key planGap
// clears on a successful plan.
func (out *seamOutcome) plant(gapColumn string) {
	out.issues.set(issueKeyGapConfig(out.targetID, gapColumn), "warning", matrixSentinelCode,
		"planted by the action × seam matrix; planGap clears this key on a successful plan")
}

// observe records whether one cell's witness survived the seam, and banks any
// `error`-severity issue standing at that moment. Both are read per cell for
// the same reason (see seamOutcome.raised).
func (out *seamOutcome) observe(gapColumn string) {
	issue, standing := issueAt(out.issues, issueKeyGapConfig(out.targetID, gapColumn))
	out.sentinel[gapColumn] = standing && issue.Code == matrixSentinelCode
	for _, is := range out.issues.snapshot() {
		if is.Severity != "error" || out.alreadyRaised(is) {
			continue
		}
		out.raised = append(out.raised, is)
	}
}

func (out *seamOutcome) alreadyRaised(candidate healthIssue) bool {
	for _, is := range out.raised {
		if is.Code == candidate.Code && is.Message == candidate.Message {
			return true
		}
	}
	return false
}

// seedMatrixRegistry loads the live registry references the declarations
// resolve against at dispatch time.
func seedMatrixRegistry(t *testing.T, e *Engine, targetID string) matrixFixture {
	t.Helper()
	f := matrixFixture{
		patternRef: "matrixParksOnAHuman",
		assignee:   "vtx.identity.AAmatrixStaffHJKMNP",
		taskOp:     "ApproveMatrixSubject",
		candidate:  dpCandidate,
	}
	// A pattern with a userTask step: dispatchable, and classified as parking
	// on a human, which is what makes the re-arm's collapse-only refusal a
	// property of the dispatch SHAPE rather than of the action's name.
	seedPatternSpec(t, e.source, f.patternRef, stepKindSystemOp, stepKindUserTask)
	e.source.mu.Lock()
	e.source.opMetaByType[f.taskOp] = "vtx.meta." + testNanoID(t)
	// The owning meta vertex augurEscalation threads into the escalation's
	// targetId param and its no-orphan reads.
	e.source.targetOwner[targetID] = testNanoID(t)
	e.source.mu.Unlock()
	return f
}

// matrixRow is the §10.2 shape every cell's vector starts from: one violating
// entity with one open gap column.
func matrixRow(entityID, gapColumn string, extra map[string]any) map[string]any {
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID,
		"violating": true,
		gapColumn:   true,
	}
	for k, v := range extra {
		row[k] = v
	}
	return row
}

// matrixCountRow is matrixRow plus the §10.3 companions the count-anchored
// seams read: a declared retry cap and no in-flight remediation.
func matrixCountRow(entityID, gapColumn string, extra map[string]any) map[string]any {
	row := exhaustedRow(entityID, gapColumn, matrixBudget)
	for k, v := range extra {
		row[k] = v
	}
	return row
}

func specRowColumns(spec actionSeamSpec, f matrixFixture) map[string]any {
	if spec.rowColumns == nil {
		return nil
	}
	return spec.rowColumns(f)
}

// matrixCells walks the control first (its dispatch is every refusal's setup
// proof) and then the action axis, handing each visit its gap column, the
// entity carrying it, and the row its declaration reads.
func matrixCells(out *seamOutcome, f matrixFixture, row func(entityID, gapColumn string, extra map[string]any) map[string]any,
	visit func(gapColumn, markAction string, row map[string]any)) {

	control := out.entity[matrixControlGap]
	visit(matrixControlGap, actionDirectOp, row(control, matrixControlGap, nil))
	for _, spec := range playbookActionSeamMatrix {
		entityID := out.entity[spec.gapColumn]
		visit(spec.gapColumn, spec.markAction, row(entityID, spec.gapColumn, specRowColumns(spec, f)))
	}
}

// runDispatchGapSeam drives lane 1: one fresh CDC delivery per cell, no mark.
// Each cell's ops are drained at its own delivery, which is also the only
// moment its witness can be read (see seamOutcome.observe).
func runDispatchGapSeam(t *testing.T, ctx context.Context, seam actionSeam) *seamOutcome {
	const targetID = "matrixDispatchGap"
	h := newHandlerHarness(t, ctx)
	f := seedMatrixRegistry(t, h.engine, targetID)
	out := newSeamOutcome(t, targetID, h.engine, f)
	h.seedTarget(out.target)

	matrixCells(out, f, matrixRow, func(gapColumn, _ string, row map[string]any) {
		out.plant(gapColumn)
		h.engine.handleRow(ctx, h.rowMessage(t, targetID, out.entity[gapColumn], row, 5, 1))
		out.observe(gapColumn)
		out.record(t, gapColumn, drainOps(t, h.ops, cellOpCount(seam, gapColumn)))
	})
	return out
}

// runReclaimSeam drives the sweeper's mark leg: every cell carries an expired
// in-flight mark over a still-violating row, the shape a reclaim re-dispatches.
// One pass covers the whole axis, so the control's re-dispatch and every
// refusal are decided by the same sweep.
//
// reclaim reaches planGap twice — the ordinary re-dispatch and the goal
// leg-advance releaseCompletedLeg opens — and its action guard sits above BOTH,
// so what this drives is that shared guard. The leg-advance itself is reachable
// only for a gap declaring a Goal, which is one shape of the plan-time-resolved
// cell; the matrix drives that cell through its candidates form.
func runReclaimSeam(t *testing.T, ctx context.Context, seam actionSeam) *seamOutcome {
	const targetID = "matrixReclaim"
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()
	f := seedMatrixRegistry(t, h.engine, targetID)
	out := newSeamOutcome(t, targetID, h.engine, f)
	h.seedTarget(out.target)

	matrixCells(out, f, matrixRow, func(gapColumn, markAction string, row map[string]any) {
		out.plant(gapColumn)
		entityID := out.entity[gapColumn]
		h.putRow(t, ctx, targetID, entityID, row)
		h.putMark(t, ctx, markKey(targetID, entityID, gapColumn),
			fixtureMark(targetID, entityID, gapColumn, markAction, pastLease()))
	})

	h.pass(ctx)
	out.attribute(t, seam, drainOps(t, h.ops, passOpCount(seam)))
	return out
}

// runEscalateSeam drives escalateExhaustedGap directly — the guard lives inside
// that function rather than at one caller precisely so a future caller inherits
// it, so this is the granularity the invariant is stated at. Every cell fires
// the same augur op, so each call's ops are drained at the call.
func runEscalateSeam(t *testing.T, ctx context.Context, seam actionSeam) *seamOutcome {
	const targetID = "matrixEscalate"
	h := newSweepHarness(t, ctx)
	f := seedMatrixRegistry(t, h.engine, targetID)
	out := newSeamOutcome(t, targetID, h.engine, f)
	h.seedTarget(out.target)

	matrixCells(out, f, matrixCountRow, func(gapColumn, _ string, row map[string]any) {
		out.plant(gapColumn)
		entityKey, _ := row["entityKey"].(string)
		h.engine.escalateExhaustedGap(ctx, out.target, targetID, out.entity[gapColumn],
			entityKey, gapColumn, row, 42, false)
		out.observe(gapColumn)
		out.record(t, gapColumn, drainOps(t, h.ops, cellOpCount(seam, gapColumn)))
	})
	return out
}

// runCountLegReArmSeam drives the sweep's count leg over an operator's re-armed
// budget: a dispatch-count that exists and reads zero, no mark, an open gap.
func runCountLegReArmSeam(t *testing.T, ctx context.Context, seam actionSeam) *seamOutcome {
	const targetID = "matrixCountLegReArm"
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()
	f := seedMatrixRegistry(t, h.engine, targetID)
	out := newSeamOutcome(t, targetID, h.engine, f)
	h.seedTarget(out.target)

	matrixCells(out, f, matrixCountRow, func(gapColumn, _ string, row map[string]any) {
		out.plant(gapColumn)
		entityID := out.entity[gapColumn]
		h.seedReArmedCount(t, ctx, targetID, entityID, gapColumn)
		h.putRow(t, ctx, targetID, entityID, row)
	})

	h.pass(ctx)
	out.attribute(t, seam, drainOps(t, h.ops, passOpCount(seam)))
	return out
}

// --- op-stream helpers ------------------------------------------------------

// record attributes one cell's drained ops, for a seam driven cell by cell.
func (out *seamOutcome) record(t *testing.T, gapColumn string, ops []string) {
	t.Helper()
	out.cellOps[gapColumn] = ops
	out.ops = append(out.ops, ops...)
}

// attribute splits ONE sweep pass's ops across the cells that produced them.
// Every cell's operationTypes are disjoint by construction, so the split is
// exact — and an op no cell declares fails here rather than being silently
// dropped, which is the shape a seam dispatching for a refused action takes.
func (out *seamOutcome) attribute(t *testing.T, seam actionSeam, ops []string) {
	t.Helper()
	out.ops = ops
	out.cellOps[matrixControlGap] = filterOps(ops, []string{seam.controlOp})
	attributed := append([]string(nil), out.cellOps[matrixControlGap]...)
	for _, spec := range playbookActionSeamMatrix {
		mine := filterOps(ops, seam.want(spec))
		out.cellOps[spec.gapColumn] = mine
		attributed = append(attributed, mine...)
		out.observe(spec.gapColumn)
	}
	out.observe(matrixControlGap)
	if stray := leftoverOps(ops, attributed); len(stray) > 0 {
		named := make([]string, 0, len(stray))
		for _, op := range stray {
			named = append(named, op+" ("+cellOwningOp(op)+")")
		}
		t.Errorf("seam %s published %v; %v was dispatched for an action this seam must refuse "+
			"BEFORE planGap%s", seam.name, ops, named, seamGateAdvice)
	}
}

// cellOwningOp names the cell that declares an operationType at ANY seam, so a
// stray op is reported as the cell whose classification is wrong rather than as
// an anonymous op type.
func cellOwningOp(operationType string) string {
	if operationType == matrixControlOp {
		return "the control gap"
	}
	for _, spec := range playbookActionSeamMatrix {
		for _, ops := range [][]string{spec.dispatchOps, spec.reclaimOps, spec.escalateOps, spec.reArmOps} {
			for _, op := range ops {
				if op == operationType {
					return "cell " + spec.label
				}
			}
		}
	}
	return "declared by no cell"
}

// leftoverOps is the multiset difference got − attributed.
func leftoverOps(got, attributed []string) []string {
	remaining := map[string]int{}
	for _, op := range attributed {
		remaining[op]++
	}
	var out []string
	for _, op := range got {
		if remaining[op] > 0 {
			remaining[op]--
			continue
		}
		out = append(out, op)
	}
	return out
}

// cellOpCount is how many ops one cell must publish at this seam.
func cellOpCount(seam actionSeam, gapColumn string) int {
	if gapColumn == matrixControlGap {
		return 1
	}
	for _, spec := range playbookActionSeamMatrix {
		if spec.gapColumn == gapColumn {
			return len(seam.want(spec))
		}
	}
	return 0
}

// passOpCount is how many ops one whole-axis sweep pass must publish.
func passOpCount(seam actionSeam) int {
	n := 1 // the control
	for _, spec := range playbookActionSeamMatrix {
		n += len(seam.want(spec))
	}
	return n
}

// drainOps collects the operationTypes published on ops.system in publication
// order: it waits for `want` of them, then confirms the stream has gone quiet,
// so a seam that dispatched something it must refuse is caught rather than
// silently trimmed.
func drainOps(t *testing.T, sub *nats.Subscription, want int) []string {
	t.Helper()
	var got []string
	for {
		timeout := 500 * time.Millisecond
		if len(got) < want {
			timeout = 5 * time.Second
		}
		msg, err := sub.NextMsg(timeout)
		if err != nil {
			return got
		}
		var op map[string]any
		if err := json.Unmarshal(msg.Data, &op); err != nil {
			t.Fatalf("unmarshal op: %v", err)
		}
		operationType, _ := op["operationType"].(string)
		got = append(got, operationType)
	}
}

// filterOps keeps the ops belonging to one cell, preserving publication order.
func filterOps(ops, keep []string) []string {
	if len(keep) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(keep))
	for _, k := range keep {
		set[k] = struct{}{}
	}
	var out []string
	for _, op := range ops {
		if _, ok := set[op]; ok {
			out = append(out, op)
		}
	}
	return out
}

func sameOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
