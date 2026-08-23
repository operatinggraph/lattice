package weaver

import (
	"context"
	"testing"
	"time"

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
