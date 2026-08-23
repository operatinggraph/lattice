package weaver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestDirectOpEnumerations_ReachEnvelope proves the DELIVERING line, not the
// rule: a gap's declared kv.Links walks must arrive on the op envelope Weaver
// actually publishes, with each hub resolved from the violation row. A field
// that parses into GapAction, survives into plan, and never reaches the wire is
// a declaration nobody can read — the exact shape of a rule covered many ways
// with its delivery covered zero times.
//
// It drives the whole dispatch path (handleRow → buildPlan → fire → submit) and
// reads the published envelope off ops.system, so no in-process assertion can
// stand in for the publish.
func TestDirectOpEnumerations_ReachEnvelope(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureEnumerations"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps: map[string]GapAction{
			"missing_residue": {
				Action:    actionDirectOp,
				Operation: "SweepResidue",
				Reads:     []string{"row.entityKey"},
				Enumerations: []GapEnumeration{
					{Hub: "row.entityKey", Relation: "boundTo", Direction: "in"},
					{Hub: "row.entityKey", Relation: "boundTo", Direction: "out"},
				},
			},
			// A gap that declares no walks must publish no enumerations key at
			// all — the omitempty half of the same wire contract.
			"missing_seal": {Action: actionDirectOp, Operation: "SealResidue", Reads: []string{"row.entityKey"}},
			// The case that exercises submit's ContextHint guard on the
			// enumerations disjunct ALONE. Every other gap here, and every gap
			// shipped in the corpus, also declares Reads — so with only those
			// the guard would attach a contextHint for the reads and carry the
			// walks along for free, and reverting the disjunct would change
			// nothing anywhere. An op that walks links without hydrating any
			// key is the shape that proves the disjunct is load bearing.
			"missing_walksOnly": {
				Action:    actionDirectOp,
				Operation: "SweepWithoutHydrating",
				Enumerations: []GapEnumeration{
					{Hub: "row.entityKey", Relation: "duplicateOf", Direction: "out"},
				},
			},
		},
	})

	entityID := testNanoID(t)
	entityKey := "vtx.identity." + entityID

	t.Run("declared walks arrive resolved", func(t *testing.T) {
		row := map[string]any{"entityKey": entityKey, "violating": true, "missing_residue": true}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 5, 1)); dec != substrate.Ack {
			t.Fatalf("dispatch must Ack, got %v", dec)
		}
		op := h.nextOp(t)
		hint, ok := op["contextHint"].(map[string]any)
		if !ok {
			t.Fatalf("published envelope carries no contextHint: %v", op)
		}
		ens, ok := hint["enumerations"].([]any)
		if !ok {
			t.Fatalf("contextHint carries no enumerations: %v", hint)
		}
		want := []map[string]any{
			{"hub": entityKey, "relation": "boundTo", "direction": "in"},
			{"hub": entityKey, "relation": "boundTo", "direction": "out"},
		}
		if len(ens) != len(want) {
			t.Fatalf("enumerations = %v, want %d entries", ens, len(want))
		}
		for i, w := range want {
			got, ok := ens[i].(map[string]any)
			if !ok {
				t.Fatalf("enumerations[%d] = %v, want an object", i, ens[i])
			}
			for k, v := range w {
				if got[k] != v {
					t.Errorf("enumerations[%d].%s = %v, want %v", i, k, got[k], v)
				}
			}
		}
	})

	t.Run("walks alone attach the contextHint", func(t *testing.T) {
		other := testNanoID(t)
		otherKey := "vtx.identity." + other
		row := map[string]any{"entityKey": otherKey, "violating": true, "missing_walksOnly": true}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, other, row, 9, 1)); dec != substrate.Ack {
			t.Fatalf("dispatch must Ack, got %v", dec)
		}
		op := h.nextOp(t)
		hint, ok := op["contextHint"].(map[string]any)
		if !ok {
			t.Fatalf("a gap declaring only walks must still publish a contextHint — the enumerations disjunct is what attaches it: %v", op)
		}
		if _, present := hint["reads"]; present {
			t.Errorf("this gap declares no reads; the contextHint on the wire is the enumerations disjunct's doing: %v", hint)
		}
		ens, ok := hint["enumerations"].([]any)
		if !ok || len(ens) != 1 {
			t.Fatalf("contextHint.enumerations = %v, want one entry", hint["enumerations"])
		}
		got := ens[0].(map[string]any)
		for k, v := range map[string]any{"hub": otherKey, "relation": "duplicateOf", "direction": "out"} {
			if got[k] != v {
				t.Errorf("enumerations[0].%s = %v, want %v", k, got[k], v)
			}
		}
	})

	t.Run("a gap declaring no walks publishes no enumerations key", func(t *testing.T) {
		other := testNanoID(t)
		row := map[string]any{"entityKey": "vtx.identity." + other, "violating": true, "missing_seal": true}
		h.engine.handleRow(ctx, h.rowMessage(t, targetID, other, row, 7, 1))
		op := h.nextOp(t)
		hint, ok := op["contextHint"].(map[string]any)
		if !ok {
			t.Fatalf("published envelope carries no contextHint: %v", op)
		}
		if _, present := hint["enumerations"]; present {
			t.Errorf("contextHint carries an enumerations key for a gap that declares none: %v", hint)
		}
	})
}

// TestTargetGap_EnumerationsRoundTripFromSpecBody proves the wire half of the
// gap surface, the counterpart of loom's
// TestPatternStep_ReadsRoundTripFromSpecBody: the `enumerations` key (and its
// three inner keys) that pkgmgr's gapActionBody writes into the
// meta.weaverTarget spec aspect must deserialize onto GapAction. The two sides
// are hand-copied string literals on one side and json tags on the other, and
// those tags are the ONLY thing joining them — rename either and the target
// installs cleanly, Enumerations decodes to nil, and every dispatch publishes a
// walk-free envelope with nothing red anywhere.
//
// TestDirectOpEnumerations_ReachEnvelope cannot cover this and must not be
// mistaken for covering it: it seeds through h.seedTarget, which writes a
// constructed &Target{} straight into engine.source.targets and never decodes
// JSON at all. That is this component's own recorded hazard — a test that
// hand-seeds an engine's internal registry map pins the FALLBACK, not the wire
// name. So this test starts from the body, not from the struct.
//
// It also covers the catalog surface, whose entries reach the same envelope
// through catalogEntryGapAction.
func TestTargetGap_EnumerationsRoundTripFromSpecBody(t *testing.T) {
	t.Parallel()

	const specBody = `{
	  "targetId": "identityErasureComplete",
	  "lensRef": "identityErasureResidue",
	  "gaps": {
	    "missing_credentialResidue": {
	      "action": "directOp",
	      "operation": "UnbindIdentityCredentials",
	      "reads": ["row.entityKey"],
	      "enumerations": [
	        {"hub": "row.entityKey", "relation": "boundTo", "direction": "in"},
	        {"hub": "row.entityKey", "relation": "boundTo", "direction": "out"}
	      ],
	      "goal": {"present": "subject.data.clear"},
	      "actions": [
	        {"ref": "sweep", "action": "directOp", "operation": "UnbindIdentityCredentials",
	         "enumerations": [{"hub": "row.entityKey", "relation": "boundTo", "direction": "in"}],
	         "effects": [{"present": "subject.data.clear"}]}
	      ]
	    },
	    "missing_erasureSeal": {"action": "directOp", "operation": "SealIdentityForErasureComplete"}
	  }
	}`

	var target Target
	require.NoError(t, json.Unmarshal([]byte(specBody), &target))
	require.NoError(t, validateTarget(&target))

	ga := target.Gaps["missing_credentialResidue"]
	require.Equal(t, []GapEnumeration{
		{Hub: "row.entityKey", Relation: "boundTo", Direction: "in"},
		{Hub: "row.entityKey", Relation: "boundTo", Direction: "out"},
	}, ga.Enumerations, "the gap's declared walks must survive the spec-body decode")

	require.Equal(t,
		[]GapEnumeration{{Hub: "row.entityKey", Relation: "boundTo", Direction: "in"}},
		ga.Actions[0].Enumerations,
		"a catalog entry dispatches through the same buildPlan, so its walks must decode too")

	// And the entry's walks must survive materialization into the GapAction
	// buildPlan actually consumes — the converter is where a field silently
	// stops existing.
	require.Equal(t, ga.Actions[0].Enumerations, catalogEntryGapAction(ga.Actions[0]).Enumerations)
	require.Equal(t,
		[]GapEnumeration{{Hub: "row.entityKey", Relation: "boundTo", Direction: "in"}},
		candidateGapAction(GapCandidate{
			Action:       actionDirectOp,
			Enumerations: []GapEnumeration{{Hub: "row.entityKey", Relation: "boundTo", Direction: "in"}},
		}).Enumerations,
		"a selected candidate dispatches through the same buildPlan")

	require.Nil(t, target.Gaps["missing_erasureSeal"].Enumerations,
		"a gap declaring nothing round-trips as nil")
}

// TestValidateTarget_RejectsMalformedEnumerations is the engine-load refusal
// MINOR-5's absence left open, and the weaver counterpart of loom's
// validateEnumerations running inside Pattern.validate.
//
// The installer already checks this shape, but the installer is not the only
// door: a target vertex reaches this engine from Core KV, and it can have been
// written by a hand-authored meta vertex, by an artifact applied through an
// older binary, or by an installer from a build that predates the check. What
// makes a malformed declaration worth refusing at load — rather than letting it
// through as inert metadata — is that it is not inert. opwire refuses the WHOLE
// envelope on it, and terminally: the mark is already written, so redelivery
// re-derives the identical requestId, the op never runs, and the gap converges
// never while looking perfectly live. A rejected target is loud; this is the
// difference between the two.
func TestValidateTarget_RejectsMalformedEnumerations(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		en      GapEnumeration
		wantErr string
	}{
		{"a complete declaration loads", GapEnumeration{Hub: "row.entityKey", Relation: "boundTo", Direction: "in"}, ""},
		{"an empty relation", GapEnumeration{Hub: "row.entityKey", Direction: "in"}, "requires hub and relation"},
		{"an empty hub", GapEnumeration{Relation: "boundTo", Direction: "in"}, "requires hub and relation"},
		{"an empty direction", GapEnumeration{Hub: "row.entityKey", Relation: "boundTo"}, "direction must be"},
		{"a direction outside the pair", GapEnumeration{Hub: "row.entityKey", Relation: "boundTo", Direction: "both"}, "direction must be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := &Target{
				TargetID: "identityErasureComplete",
				Gaps: map[string]GapAction{"missing_residue": {
					Action: actionDirectOp, Operation: "Sweep", Enumerations: []GapEnumeration{tc.en},
				}},
			}
			err := validateTarget(target)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}

	// The same refusal must reach the two planner surfaces, because their
	// entries dispatch through the same buildPlan and land in the same
	// envelope.
	bad := []GapEnumeration{{Hub: "row.entityKey", Relation: "boundTo", Direction: "sideways"}}
	require.ErrorContains(t, validateTarget(&Target{
		TargetID: "t",
		Gaps: map[string]GapAction{"missing_residue": {
			Action:     actionDirectOp,
			Candidates: []GapCandidate{{Action: actionDirectOp, Enumerations: bad}},
		}},
	}), "candidates[0]: enumerations[0] direction must be")

	require.ErrorContains(t, validateTarget(&Target{
		TargetID: "t",
		Gaps: map[string]GapAction{"missing_residue": {
			Action:  actionDirectOp,
			Goal:    json.RawMessage(`{"present": "subject.data.clear"}`),
			Actions: []ActionCatalogEntry{{Ref: "r", Action: actionDirectOp, Enumerations: bad, Effects: []json.RawMessage{json.RawMessage(`{"present": "subject.data.clear"}`)}}},
		}},
	}), "actions[0] (ref \"r\"): enumerations[0] direction must be")
}
