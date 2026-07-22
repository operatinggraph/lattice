package weaver

import (
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
)

// TestValidateTarget_ActionsCatalog proves the Fire-6 Increment-3 install-time
// validation of a goal gap's actions catalog (design revision, 2026-07-05): a
// well-formed catalog entry — unique ref, a dispatch binding, a
// goalColumns-bridged effect — parses and caches preGuard/effectGuards; goal
// with no actions, actions with no goal, a duplicate ref, a missing
// action/effects, a negative cost, and an unreachable (unbridged) aspect path
// in either pre or effects all reject the whole target, matching the
// goal/candidates/effects fail-wholesale doctrine.
func TestValidateTarget_ActionsCatalog(t *testing.T) {
	t.Parallel()
	valid := &Target{
		TargetID: "fixtureActionsCatalog",
		Gaps: map[string]GapAction{
			"missing_signature": {
				Goal:        json.RawMessage(`{"present":"subject.signature.data.signedAt"}`),
				GoalColumns: map[string]string{"signedAt": "subject.signature.data.signedAt"},
				Actions: []ActionCatalogEntry{{
					Ref:       "SignLease",
					Action:    "directOp",
					Operation: "SignLease",
					Effects:   []json.RawMessage{json.RawMessage(`{"present":"subject.signature.data.signedAt"}`)},
				}},
			},
		},
	}
	if err := validateTarget(valid); err != nil {
		t.Fatalf("a well-formed actions catalog must pass: %v", err)
	}
	entry := valid.Gaps["missing_signature"].Actions[0]
	if entry.Cost != 1 {
		t.Fatalf("cost = %d, want the default of 1", entry.Cost)
	}
	if entry.effectGuards == nil || len(entry.effectGuards) != 1 {
		t.Fatalf("effectGuards not cached: %+v", entry.effectGuards)
	}

	bridgedPath := guardgrammar.Path{Aspect: "signature", Field: "signedAt"}

	cases := map[string]*Target{
		"goal with no actions": {
			TargetID: "fixtureActionsNoActions",
			Gaps: map[string]GapAction{
				"missing_a": {
					Goal: json.RawMessage(`{"present":"subject.data.col"}`),
				},
			},
		},
		"actions with no goal": {
			TargetID: "fixtureActionsNoGoal",
			Gaps: map[string]GapAction{
				"missing_a": {
					Actions: []ActionCatalogEntry{{
						Ref:     "Do",
						Action:  "directOp",
						Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.col"}`)},
					}},
				},
			},
		},
		"duplicate ref": {
			TargetID: "fixtureActionsDupRef",
			Gaps: map[string]GapAction{
				"missing_a": {
					Goal: json.RawMessage(`{"present":"subject.data.col"}`),
					Actions: []ActionCatalogEntry{
						{Ref: "Do", Action: "directOp", Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.col"}`)}},
						{Ref: "Do", Action: "directOp", Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.col"}`)}},
					},
				},
			},
		},
		"missing ref": {
			TargetID: "fixtureActionsMissingRef",
			Gaps: map[string]GapAction{
				"missing_a": {
					Goal: json.RawMessage(`{"present":"subject.data.col"}`),
					Actions: []ActionCatalogEntry{
						{Action: "directOp", Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.col"}`)}},
					},
				},
			},
		},
		"missing action": {
			TargetID: "fixtureActionsMissingAction",
			Gaps: map[string]GapAction{
				"missing_a": {
					Goal: json.RawMessage(`{"present":"subject.data.col"}`),
					Actions: []ActionCatalogEntry{
						{Ref: "Do", Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.col"}`)}},
					},
				},
			},
		},
		"missing effects": {
			TargetID: "fixtureActionsMissingEffects",
			Gaps: map[string]GapAction{
				"missing_a": {
					Goal:    json.RawMessage(`{"present":"subject.data.col"}`),
					Actions: []ActionCatalogEntry{{Ref: "Do", Action: "directOp"}},
				},
			},
		},
		"negative cost": {
			TargetID: "fixtureActionsNegativeCost",
			Gaps: map[string]GapAction{
				"missing_a": {
					Goal: json.RawMessage(`{"present":"subject.data.col"}`),
					Actions: []ActionCatalogEntry{
						{Ref: "Do", Action: "directOp", Cost: -1,
							Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.col"}`)}},
					},
				},
			},
		},
		"non-concrete effect (anyOf)": {
			TargetID: "fixtureActionsAnyOfEffect",
			Gaps: map[string]GapAction{
				"missing_a": {
					Goal: json.RawMessage(`{"present":"subject.data.col"}`),
					Actions: []ActionCatalogEntry{
						{Ref: "Do", Action: "directOp",
							Effects: []json.RawMessage{json.RawMessage(
								`{"anyOf":[{"present":"subject.data.col"},{"present":"subject.data.other"}]}`)}},
					},
				},
			},
		},
		"unreachable aspect effect (no goalColumns bridge)": {
			TargetID: "fixtureActionsUnreachableEffect",
			Gaps: map[string]GapAction{
				"missing_a": {
					Goal: json.RawMessage(`{"present":"subject.data.col"}`),
					Actions: []ActionCatalogEntry{
						{Ref: "Do", Action: "directOp",
							Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.signature.data.signedAt"}`)}},
					},
				},
			},
		},
		"unreachable aspect pre (bridged by a DIFFERENT gap)": {
			TargetID: "fixtureActionsUnreachablePre",
			Gaps: map[string]GapAction{
				"missing_a": {
					Goal: json.RawMessage(`{"present":"subject.data.col"}`),
					Actions: []ActionCatalogEntry{
						{Ref: "Do", Action: "directOp",
							Pre:     json.RawMessage(`{"present":"subject.signature.data.signedAt"}`),
							Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.col"}`)}},
					},
				},
				"missing_b": {
					Goal:        json.RawMessage(`{"present":"subject.signature.data.signedAt"}`),
					GoalColumns: map[string]string{"signedAt": "subject.signature.data.signedAt"},
				},
			},
		},
	}
	for name, target := range cases {
		if err := validateTarget(target); err == nil {
			t.Errorf("%s: expected validateTarget to reject the target, got nil error", name)
		}
	}

	// Sanity: an actions[].pre MAY address the SAME gap's bridged aspect path
	// (unlike candidates[].pre, which is root-only with no bridge at all).
	preOK := &Target{
		TargetID: "fixtureActionsPreBridged",
		Gaps: map[string]GapAction{
			"missing_signature": {
				Goal:        json.RawMessage(`{"present":"subject.signature.data.signedAt"}`),
				GoalColumns: map[string]string{"signedAt": "subject.signature.data.signedAt"},
				Actions: []ActionCatalogEntry{{
					Ref:     "SignLease",
					Action:  "directOp",
					Pre:     json.RawMessage(`{"absent":"subject.signature.data.signedAt"}`),
					Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.signature.data.signedAt"}`)},
				}},
			},
		},
	}
	if err := validateTarget(preOK); err != nil {
		t.Fatalf("a goalColumns-bridged actions[].pre must pass: %v", err)
	}
	if got := preOK.Gaps["missing_signature"].Actions[0].preGuard; got == nil {
		t.Fatalf("preGuard not cached")
	}
	if got := preOK.Gaps["missing_signature"].goalColumnPaths["signedAt"]; got != bridgedPath {
		t.Fatalf("goalColumnPaths[%q] = %+v, want %+v", "signedAt", got, bridgedPath)
	}
}
