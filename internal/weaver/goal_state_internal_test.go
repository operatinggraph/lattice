package weaver

import (
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
	"github.com/operatinggraph/lattice/internal/weaver/planner"
)

// TestValidateTarget_GoalColumns proves the Fire-6 Increment-2 install-time
// validation, scoped per-gap (not target-wide, so two gaps in one target can
// never fight over the same column name): a well-formed, aspect-qualified,
// goal-referenced entry parses and caches into goalColumnPaths; a root-shaped
// entry (redundant — rowState already addresses root columns by default), a
// malformed path, an entry the gap's goal never references (as inert as a
// typo'd column name and just as silent otherwise), a goalColumns set with no
// goal at all, and two columns mapping to the same path (nondeterministic
// rowState under Go's map-iteration order) all reject the whole target,
// matching the goal/candidates/effects fail-wholesale doctrine.
func TestValidateTarget_GoalColumns(t *testing.T) {
	t.Parallel()
	valid := &Target{
		TargetID: "fixtureGoalColumns",
		Gaps: map[string]GapAction{
			"missing_signature": {
				Goal:        json.RawMessage(`{"present":"subject.signature.data.signedAt"}`),
				GoalColumns: map[string]string{"signedAt": "subject.signature.data.signedAt"},
				Actions: []ActionCatalogEntry{{
					Ref:     "SignLease",
					Action:  "directOp",
					Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.signature.data.signedAt"}`)},
				}},
			},
		},
	}
	if err := validateTarget(valid); err != nil {
		t.Fatalf("a well-formed goalColumns entry must pass: %v", err)
	}
	want := guardgrammar.Path{Aspect: "signature", Field: "signedAt"}
	if got := valid.Gaps["missing_signature"].goalColumnPaths["signedAt"]; got != want {
		t.Fatalf("goalColumnPaths[%q] = %+v, want %+v", "signedAt", got, want)
	}

	cases := map[string]*Target{
		"root-shaped": {
			TargetID: "fixtureGoalColumnsRoot",
			Gaps: map[string]GapAction{
				"missing_a": {
					Goal:        json.RawMessage(`{"present":"subject.data.col"}`),
					GoalColumns: map[string]string{"col": "subject.data.col"},
				},
			},
		},
		"malformed path": {
			TargetID: "fixtureGoalColumnsBad",
			Gaps: map[string]GapAction{
				"missing_a": {
					Goal:        json.RawMessage(`{"present":"subject.data.col"}`),
					GoalColumns: map[string]string{"col": "not.a.path"},
				},
			},
		},
		"unreferenced by goal": {
			TargetID: "fixtureGoalColumnsUnreferenced",
			Gaps: map[string]GapAction{
				"missing_a": {
					Goal:        json.RawMessage(`{"present":"subject.other.data.field"}`),
					GoalColumns: map[string]string{"col": "subject.signature.data.signedAt"},
				},
			},
		},
		"goalColumns with no goal": {
			TargetID: "fixtureGoalColumnsNoGoal",
			Gaps: map[string]GapAction{
				"missing_a": {
					GoalColumns: map[string]string{"col": "subject.signature.data.signedAt"},
				},
			},
		},
		"duplicate path values": {
			TargetID: "fixtureGoalColumnsDup",
			Gaps: map[string]GapAction{
				"missing_a": {
					Goal: json.RawMessage(`{"present":"subject.signature.data.signedAt"}`),
					GoalColumns: map[string]string{
						"colA": "subject.signature.data.signedAt",
						"colB": "subject.signature.data.signedAt",
					},
				},
			},
		},
	}
	for name, target := range cases {
		if err := validateTarget(target); err == nil {
			t.Errorf("%s: expected validateTarget to reject the target, got nil error", name)
		}
	}
}

// TestValidateTarget_CandidatesPre_RejectsAspectPath proves candidates[].pre
// must stay root-shaped: rankCandidates evaluates it against
// rowState(row, nil), which never bridges an aspect path (only goal gets that
// bridge, via GoalColumns), so an aspect-shaped pre would be permanently
// unsatisfiable — a silent config error without this rejection.
func TestValidateTarget_CandidatesPre_RejectsAspectPath(t *testing.T) {
	t.Parallel()
	target := &Target{
		TargetID: "fixtureCandidatesPreAspect",
		Gaps: map[string]GapAction{
			"missing_a": {
				Candidates: []GapCandidate{{
					Action: "directOp",
					Pre:    json.RawMessage(`{"present":"subject.signature.data.signedAt"}`),
				}},
			},
		},
	}
	if err := validateTarget(target); err == nil {
		t.Fatalf("an aspect-shaped candidates[].pre must reject the whole target")
	}

	rootOK := &Target{
		TargetID: "fixtureCandidatesPreRoot",
		Gaps: map[string]GapAction{
			"missing_a": {
				Candidates: []GapCandidate{{
					Action: "directOp",
					Pre:    json.RawMessage(`{"present":"subject.data.col"}`),
				}},
			},
		},
	}
	if err := validateTarget(rootOK); err != nil {
		t.Fatalf("a root-shaped candidates[].pre must pass: %v", err)
	}
}

// TestRowState_AspectColumns_RecognizesGoalAlreadyMet proves the State-schema
// gap is resolved end to end, grounded against the real lease-signing shape
// (packages/lease-signing/ddls.go's SignLease Effect,
// packages/lease-signing/lenses.go:551's `app.signature.data.signedAt AS
// signedAt` projection). The row already carries a signed timestamp — the
// signature is DONE — but a §10.2 row flattens it onto the untagged column
// "signedAt", so without the bridge the goal (authored at the same
// aspect-qualified path SignLease's own declared Effect asserts) reads the
// fact as absent and would synthesize a SPURIOUS SignLease plan for an
// application that needs no remediation at all. With the bridge, the same
// row — no live Core-KV read of any kind — correctly resolves the goal as
// already satisfied.
func TestRowState_AspectColumns_RecognizesGoalAlreadyMet(t *testing.T) {
	t.Parallel()
	target := &Target{
		TargetID: "leaseApplicationComplete",
		Gaps: map[string]GapAction{
			// Closing missing_signature means the signature becomes PRESENT —
			// the goal the gap's remediation must reach.
			"missing_signature": {
				Goal:        json.RawMessage(`{"present":"subject.signature.data.signedAt"}`),
				GoalColumns: map[string]string{"signedAt": "subject.signature.data.signedAt"},
				Actions: []ActionCatalogEntry{{
					Ref:     "SignLease",
					Action:  "directOp",
					Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.signature.data.signedAt"}`)},
				}},
			},
		},
	}
	if err := validateTarget(target); err != nil {
		t.Fatalf("validateTarget: %v", err)
	}
	gap := target.Gaps["missing_signature"]
	goalGuard := gap.goalGuard

	catalog := []planner.Action{{
		Ref:     "SignLease",
		Cost:    1,
		Effects: mustParseGuards(t, `{"present":"subject.signature.data.signedAt"}`),
	}}

	// The row already reflects a signed lease — signedAt carries a real
	// timestamp, exactly what a real §10.2 lens row looks like once
	// SignLease has already run.
	row := map[string]any{"signedAt": "2026-07-04T00:00:00Z", "entityKey": "vtx.leaseapp.abc123"}

	bridged := rowState(row, gap.goalColumnPaths)
	plan, err := planner.Synthesize(goalGuard, bridged, catalog, 5)
	if err != nil {
		t.Fatalf("Synthesize (bridged): %v (goal is already met — must never return ErrNoPlan)", err)
	}
	if len(plan.Steps) != 0 {
		t.Fatalf("plan = %+v, want zero steps — the signature is already present, nothing to remediate", plan)
	}

	// Sanity: the pre-fix mapping (aspectCols=nil) must get this WRONG —
	// unable to see the aspect-qualified fact under the row's untagged root
	// key, it synthesizes a spurious SignLease plan for an application that
	// needs no remediation. This proves the test actually exercises the
	// bug this fix targets, not something that behaved correctly anyway.
	unbridged := rowState(row, nil)
	spurious, err := planner.Synthesize(goalGuard, unbridged, catalog, 5)
	if err != nil {
		t.Fatalf("Synthesize (unbridged): %v", err)
	}
	if len(spurious.Steps) != 1 || spurious.Steps[0].ActionRef != "SignLease" {
		t.Fatalf("unbridged plan = %+v, want a spurious single SignLease step "+
			"(demonstrating the root/aspect key mismatch this fix resolves)", spurious)
	}
}

func mustParseGuards(t *testing.T, raw ...string) []*guardgrammar.Guard {
	t.Helper()
	guards := make([]*guardgrammar.Guard, len(raw))
	for i, r := range raw {
		g, err := guardgrammar.Parse(json.RawMessage(r))
		if err != nil {
			t.Fatalf("parse guard %q: %v", r, err)
		}
		guards[i] = g
	}
	return guards
}
