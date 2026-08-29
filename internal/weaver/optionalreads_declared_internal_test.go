package weaver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestDirectOpOptionalReads_ReachEnvelope proves the DELIVERING line, not the
// rule (the same standard TestDirectOpEnumerations_ReachEnvelope holds
// OptionalReads' sibling field to): a gap's declared OptionalReads must arrive
// on the op envelope Weaver actually publishes, with a literal passed through
// verbatim and a row.<column> template resolved from the violation row. A
// field that parses into GapAction, survives into plan, and never reaches the
// wire is a declaration nobody can read.
//
// It drives the whole dispatch path (handleRow → buildPlan → fire → submit)
// and reads the published envelope off ops.system, so no in-process assertion
// can stand in for the publish.
func TestDirectOpOptionalReads_ReachEnvelope(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureOptReads"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps: map[string]GapAction{
			"missing_residue": {
				Action:        actionDirectOp,
				Operation:     "SweepResidue",
				Reads:         []string{"row.entityKey"},
				OptionalReads: []string{"vtx.config.residueGuard", "row.entityKey"},
			},
			// A gap that declares no OptionalReads must publish no
			// optionalReads key at all — the omitempty half of the same wire
			// contract (dossier: every fixture that always supplies an
			// optional input pins only the supplied case).
			"missing_seal": {Action: actionDirectOp, Operation: "SealResidue", Reads: []string{"row.entityKey"}},
			// The case that exercises submit's ContextHint guard on the
			// optionalReads disjunct ALONE, mirroring the enumerations-only
			// vector: every other gap here also declares Reads, so with only
			// those the guard would attach a contextHint for the reads and
			// carry optionalReads along for free.
			"missing_walksOnly": {
				Action:        actionDirectOp,
				Operation:     "SweepWithoutHydrating",
				OptionalReads: []string{"row.entityKey"},
			},
		},
	})

	entityID := testNanoID(t)
	entityKey := "vtx.identity." + entityID

	t.Run("declared optional reads arrive resolved", func(t *testing.T) {
		row := map[string]any{"entityKey": entityKey, "violating": true, "missing_residue": true}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 5, 1)); dec != substrate.Ack {
			t.Fatalf("dispatch must Ack, got %v", dec)
		}
		op := h.nextOp(t)
		hint, ok := op["contextHint"].(map[string]any)
		if !ok {
			t.Fatalf("published envelope carries no contextHint: %v", op)
		}
		got, ok := hint["optionalReads"].([]any)
		if !ok {
			t.Fatalf("contextHint carries no optionalReads: %v", hint)
		}
		want := []any{"vtx.config.residueGuard", entityKey}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("optionalReads = %v, want %v (literal passed through, row.entityKey resolved)", got, want)
		}
	})

	t.Run("optionalReads alone attaches the contextHint", func(t *testing.T) {
		other := testNanoID(t)
		otherKey := "vtx.identity." + other
		row := map[string]any{"entityKey": otherKey, "violating": true, "missing_walksOnly": true}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, other, row, 9, 1)); dec != substrate.Ack {
			t.Fatalf("dispatch must Ack, got %v", dec)
		}
		op := h.nextOp(t)
		hint, ok := op["contextHint"].(map[string]any)
		if !ok {
			t.Fatalf("a gap declaring only optionalReads must still publish a contextHint — the optionalReads disjunct is what attaches it: %v", op)
		}
		if _, present := hint["reads"]; present {
			t.Errorf("this gap declares no reads; the contextHint on the wire is the optionalReads disjunct's doing: %v", hint)
		}
		got, ok := hint["optionalReads"].([]any)
		if !ok || len(got) != 1 || got[0] != otherKey {
			t.Fatalf("contextHint.optionalReads = %v, want [%q]", hint["optionalReads"], otherKey)
		}
	})

	t.Run("a gap declaring no optionalReads publishes no optionalReads key", func(t *testing.T) {
		other := testNanoID(t)
		row := map[string]any{"entityKey": "vtx.identity." + other, "violating": true, "missing_seal": true}
		h.engine.handleRow(ctx, h.rowMessage(t, targetID, other, row, 7, 1))
		op := h.nextOp(t)
		hint, ok := op["contextHint"].(map[string]any)
		if !ok {
			t.Fatalf("published envelope carries no contextHint: %v", op)
		}
		if _, present := hint["optionalReads"]; present {
			t.Errorf("contextHint carries an optionalReads key for a gap that declares none: %v", hint)
		}
	})
}

// TestTargetGap_OptionalReadsRoundTripFromSpecBody proves the wire half of the
// gap surface for OptionalReads, the counterpart of
// TestTargetGap_EnumerationsRoundTripFromSpecBody: the `optionalReads` key
// pkgmgr's gapActionBody/actionCatalogEntryBody write into the
// meta.weaverTarget spec aspect must deserialize onto GapAction and
// ActionCatalogEntry. The two sides are hand-copied string literals on one
// side and json tags on the other, and those tags are the ONLY thing joining
// them — rename either and the target installs cleanly, OptionalReads decodes
// to nil, and every dispatch publishes an optionalReads-free envelope with
// nothing red anywhere.
//
// It also covers the catalog surface end to end: the catalog entry's
// OptionalReads decodes AND survives catalogEntryGapAction's materialization
// into the GapAction shape buildPlan actually consumes.
func TestTargetGap_OptionalReadsRoundTripFromSpecBody(t *testing.T) {
	t.Parallel()

	const specBody = `{
	  "targetId": "identityErasureComplete",
	  "lensRef": "identityErasureResidue",
	  "gaps": {
	    "missing_credentialResidue": {
	      "action": "directOp",
	      "operation": "UnbindIdentityCredentials",
	      "reads": ["row.entityKey"],
	      "optionalReads": ["vtx.config.residueGuard", "row.entityKey"],
	      "goal": {"present": "subject.data.clear"},
	      "actions": [
	        {"ref": "sweep", "action": "directOp", "operation": "UnbindIdentityCredentials",
	         "optionalReads": ["row.entityKey"],
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
	require.Equal(t, []string{"vtx.config.residueGuard", "row.entityKey"}, ga.OptionalReads,
		"the gap's declared optional reads must survive the spec-body decode")

	require.Equal(t, []string{"row.entityKey"}, ga.Actions[0].OptionalReads,
		"a catalog entry dispatches through the same buildPlan, so its optionalReads must decode too")

	require.Equal(t, ga.Actions[0].OptionalReads, catalogEntryGapAction(ga.Actions[0]).OptionalReads,
		"the entry's optionalReads must survive materialization into the GapAction buildPlan consumes")

	require.Nil(t, target.Gaps["missing_erasureSeal"].OptionalReads,
		"a gap declaring nothing round-trips as nil")
}

// TestValidateTarget_RejectsOptionalReadsOnNonDirectOp enforces one
// deterministic field, one writer: buildPlan's assignTask arm already builds its OWN
// plan.optionalReads closure (the stable task dedup key + the assignee
// availability aspect), so a package-declared OptionalReads on an assignTask
// (or any non-directOp) gap would collide with it. validateTarget refuses the
// collision at install rather than let one writer silently win, mirroring the
// existing expectedRevision reserved-param refusal in this same file. The
// refusal reaches both surfaces that carry OptionalReads — the gap's own
// entry and a goal catalog entry — because both dispatch through the same
// buildPlan and would hit the same collision.
func TestValidateTarget_RejectsOptionalReadsOnNonDirectOp(t *testing.T) {
	t.Parallel()

	// Positive control: a directOp declaring OptionalReads loads cleanly.
	ok := &Target{
		TargetID: "fixtureOptReadsOK",
		Gaps: map[string]GapAction{
			"missing_a": {Action: actionDirectOp, Operation: "FixA", OptionalReads: []string{"row.entityKey"}},
		},
	}
	require.NoError(t, validateTarget(ok), "a directOp declaring OptionalReads must load")

	// Negative: an assignTask declaring OptionalReads collides with the
	// engine's own dedup-key closure and must be refused at install.
	assignTaskBad := &Target{
		TargetID: "fixtureOptReadsAssignTask",
		Gaps: map[string]GapAction{
			"missing_a": {
				Action: actionAssignTask, Operation: "ApproveX", Assignee: "row.assignee", Target: "row.entityKey",
				OptionalReads: []string{"row.entityKey"},
			},
		},
	}
	err := validateTarget(assignTaskBad)
	require.Error(t, err, "an assignTask declaring OptionalReads must be rejected at install time")
	require.ErrorContains(t, err, "optionalReads")
	require.ErrorContains(t, err, actionAssignTask)

	// Negative: triggerLoom too — every non-directOp arm is in scope, not just
	// assignTask.
	triggerLoomBad := &Target{
		TargetID: "fixtureOptReadsTriggerLoom",
		Gaps: map[string]GapAction{
			"missing_a": {
				Action: actionTriggerLoom, Pattern: "onboarding", Subject: "row.entityKey",
				OptionalReads: []string{"row.entityKey"},
			},
		},
	}
	require.ErrorContains(t, validateTarget(triggerLoomBad), "optionalReads")

	// Negative: a goal catalog entry whose Action is assignTask hits the same
	// collision through the OTHER declaration surface — the whole target
	// must be rejected, not just the offending entry silently dropped.
	catalogBad := &Target{
		TargetID: "fixtureOptReadsCatalog",
		Gaps: map[string]GapAction{
			"missing_a": {
				Goal: json.RawMessage(`{"present":"subject.data.done"}`),
				Actions: []ActionCatalogEntry{{
					Ref: "assign", Action: actionAssignTask, Operation: "ApproveX",
					Assignee: "row.assignee", Target: "row.entityKey",
					OptionalReads: []string{"row.entityKey"},
					Effects:       []json.RawMessage{json.RawMessage(`{"present":"subject.data.done"}`)},
				}},
			},
		},
	}
	catalogErr := validateTarget(catalogBad)
	require.Error(t, catalogErr, "a catalog entry declaring OptionalReads on a non-directOp action must be rejected")
	require.ErrorContains(t, catalogErr, "optionalReads")
	require.ErrorContains(t, catalogErr, "actions[0]")

	// Positive control on the catalog surface: a directOp entry declaring
	// OptionalReads loads cleanly.
	catalogOK := &Target{
		TargetID: "fixtureOptReadsCatalogOK",
		Gaps: map[string]GapAction{
			"missing_a": {
				Goal: json.RawMessage(`{"present":"subject.data.done"}`),
				Actions: []ActionCatalogEntry{{
					Ref: "sweep", Action: actionDirectOp, Operation: "Sweep",
					OptionalReads: []string{"row.entityKey"},
					Effects:       []json.RawMessage{json.RawMessage(`{"present":"subject.data.done"}`)},
				}},
			},
		},
	}
	require.NoError(t, validateTarget(catalogOK), "a directOp catalog entry declaring OptionalReads must load")
}

// TestResolveGoalAction_CarriesOptionalReadsIntoDispatchedPlan proves the
// catalog surface end to end: an ActionCatalogEntrySpec's OptionalReads is not
// just decodable
// (TestTargetGap_OptionalReadsRoundTripFromSpecBody) but reaches a REAL
// dispatched plan on the mode:"planned" path — resolveGoalAction picks the
// entry, catalogEntryGapAction materializes it, and buildPlan resolves its
// OptionalReads exactly like an explicit gap's own. Without this carry, a
// mode:"planned" target's catalog leg would silently drop the declaration its
// explicit-gap sibling can make, leaving the two dispatch surfaces asymmetric.
func TestResolveGoalAction_CarriesOptionalReadsIntoDispatchedPlan(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)

	// validateTarget parses/caches Goal + Effects onto unexported fields
	// (goalGuard/effectGuards) that resolveGoalAction's Synthesize call needs
	// — resolvePlannedAction must be fed the VALIDATED gap, exactly like
	// goalGapFixture (goal_dispatch_internal_test.go).
	fixtureTarget := &Target{
		TargetID: "fixtureCatalogOptReads",
		Mode:     targetModePlanned,
		Gaps: map[string]GapAction{
			"missing_x": {
				Goal: json.RawMessage(`{"present":"subject.data.done"}`),
				Actions: []ActionCatalogEntry{{
					Ref: "sweep", Action: actionDirectOp, Operation: "SweepResidue",
					OptionalReads: []string{"vtx.config.residueGuard", "row.entityKey"},
					Effects:       []json.RawMessage{json.RawMessage(`{"present":"subject.data.done"}`)},
				}},
			},
		},
	}
	require.NoError(t, validateTarget(fixtureTarget))
	ga := fixtureTarget.Gaps["missing_x"]

	target := &Target{TargetID: "t1", Mode: targetModePlanned}
	resolved, ref, perr := e.resolvePlannedAction(context.Background(), target, "t1", "e1", "missing_x", ga, map[string]any{}, "")
	if perr != nil {
		t.Fatalf("resolvePlannedAction: %v", perr)
	}
	if ref != "sweep" {
		t.Fatalf("ref = %q, want the only catalog entry (sweep)", ref)
	}

	row := map[string]any{"entityKey": "vtx.object.AAobjHJKMNPQRSTUVWX"}
	pl, perr := buildPlan(nil, "t1", "e1", "missing_x", resolved, row, 1)
	if perr != nil {
		t.Fatalf("buildPlan: %v", perr)
	}
	if pl.optionalReads == nil {
		t.Fatalf("the catalog entry declares OptionalReads but the dispatched plan carries none")
	}
	got := pl.optionalReads("anyClaimId")
	want := []string{"vtx.config.residueGuard", "vtx.object.AAobjHJKMNPQRSTUVWX"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("optionalReads = %v want %v", got, want)
	}
}
