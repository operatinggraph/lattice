package pkgmgr

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/weaver"
)

// TestGapActionBody_EmitsDeclaredOptionalReads locks the installer's half of
// the declaration wire on both weaver surfaces that carry OptionalReads: a
// gap's own entry and a goal catalog entry. The body each spec installs is
// what the engine deserializes, so a declaration that a package writes and
// the installer drops is a declaration that never exists at run time — the
// rule covered, the delivering line not.
//
// It also pins the omission: a gap or catalog entry that declares no
// OptionalReads must emit no `optionalReads` key at all, so every shipped
// gap's spec body keeps its exact shape across installs.
func TestGapActionBody_EmitsDeclaredOptionalReads(t *testing.T) {
	t.Parallel()

	t.Run("gap entry", func(t *testing.T) {
		t.Parallel()
		body := gapActionBody(GapActionSpec{
			Action:        "directOp",
			Operation:     "UnbindIdentityCredentials",
			Reads:         []string{"row.entityKey"},
			OptionalReads: []string{"vtx.config.residueGuard", "row.entityKey"},
		})
		require.Equal(t, []any{"vtx.config.residueGuard", "row.entityKey"}, body["optionalReads"])

		declaresNone := gapActionBody(GapActionSpec{Action: "directOp", Operation: "SealResidue"})
		require.NotContains(t, declaresNone, "optionalReads", "a gap declaring nothing must not emit the key")
	})

	t.Run("catalog entry", func(t *testing.T) {
		t.Parallel()
		body := actionCatalogEntryBody(ActionCatalogEntrySpec{
			Ref:           "sweep",
			Action:        "directOp",
			Operation:     "UnbindIdentityCredentials",
			OptionalReads: []string{"row.entityKey"},
			Effects:       []json.RawMessage{json.RawMessage(`{"present":"subject.data.clear"}`)},
		})
		require.Equal(t, []any{"row.entityKey"}, body["optionalReads"])

		declaresNone := actionCatalogEntryBody(ActionCatalogEntrySpec{
			Ref: "sweep", Action: "directOp", Operation: "UnbindIdentityCredentials",
			Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.clear"}`)},
		})
		require.NotContains(t, declaresNone, "optionalReads", "a catalog entry declaring nothing must not emit the key")
	})
}

// TestGapActionBody_OptionalReadsRoundTripsIntoWeaverRegistry proves the
// round trip across the package boundary: the body gapActionBody/
// actionCatalogEntryBody emit is exactly what the weaver registry's
// json.Unmarshal into Target/GapAction/ActionCatalogEntry consumes. The two
// sides are hand-written string literals (this file's body-builders) and
// struct json tags (weaver's GapAction/ActionCatalogEntry) — those tags are
// the ONLY thing joining them, so this test starts from the emitted body, not
// from a hand-typed JSON string, to prove the actual installer output
// deserializes cleanly rather than a string this file merely believes matches
// it.
func TestGapActionBody_OptionalReadsRoundTripsIntoWeaverRegistry(t *testing.T) {
	t.Parallel()

	gapBody := gapActionBody(GapActionSpec{
		Action:        "directOp",
		Operation:     "UnbindIdentityCredentials",
		Reads:         []string{"row.entityKey"},
		OptionalReads: []string{"vtx.config.residueGuard", "row.entityKey"},
		Goal:          json.RawMessage(`{"present":"subject.data.clear"}`),
		Actions: []ActionCatalogEntrySpec{{
			Ref:           "sweep",
			Action:        "directOp",
			Operation:     "UnbindIdentityCredentials",
			OptionalReads: []string{"row.entityKey"},
			Effects:       []json.RawMessage{json.RawMessage(`{"present":"subject.data.clear"}`)},
		}},
	})

	targetBody := map[string]any{
		"targetId": "identityErasureComplete",
		"lensRef":  "identityErasureResidue",
		"gaps": map[string]any{
			"missing_credentialResidue": gapBody,
		},
	}
	raw, err := json.Marshal(targetBody)
	require.NoError(t, err)

	var target weaver.Target
	require.NoError(t, json.Unmarshal(raw, &target))

	ga := target.Gaps["missing_credentialResidue"]
	require.Equal(t, []string{"vtx.config.residueGuard", "row.entityKey"}, ga.OptionalReads,
		"the emitted gap body must deserialize its optionalReads onto weaver.GapAction")
	require.Len(t, ga.Actions, 1)
	require.Equal(t, []string{"row.entityKey"}, ga.Actions[0].OptionalReads,
		"the emitted catalog entry body must deserialize its optionalReads onto weaver.ActionCatalogEntry")
}

// TestValidateWeaverTargets_OptionalReadsRejectedOnNonDirectOp mirrors
// TestValidateWeaverTargets_ReservedExpectedRevisionParamRejected's shape for
// the second engine-owned-field collision: an assignTask (or any non-directOp)
// gap declaring OptionalReads collides with buildPlan's own assignTask
// closure (the stable task dedup key + assignee availability aspect), so
// install refuses it first — the same "install rejects it first for a
// clearer author error" posture reservedGapParam documents — rather than let
// the package install cleanly and have the WHOLE target die later at the
// engine's own validateTarget.
func TestValidateWeaverTargets_OptionalReadsRejectedOnNonDirectOp(t *testing.T) {
	// Positive control: a directOp gap declaring OptionalReads installs clean.
	ok := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps: map[string]GapActionSpec{
			"missing_signature": {Action: "directOp", Operation: "SignLease", OptionalReads: []string{"row.lease"}},
		},
	}}}
	if err := ok.validateWeaverTargets(); err != nil {
		t.Fatalf("a directOp gap declaring OptionalReads must install clean, got: %v", err)
	}

	// Omission vector: a gap declaring nothing installs clean (the reserved
	// field's absence is not itself a finding).
	none := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: "assignTask", Operation: "SignLease", Assignee: "row.tenant", Target: "row.lease"}},
	}}}
	if err := none.validateWeaverTargets(); err != nil {
		t.Fatalf("a gap declaring no OptionalReads must install clean, got: %v", err)
	}

	// Negative: assignTask.
	assignTaskBad := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps: map[string]GapActionSpec{
			"missing_signature": {
				Action: "assignTask", Operation: "SignLease", Assignee: "row.tenant", Target: "row.lease",
				OptionalReads: []string{"row.lease"},
			},
		},
	}}}
	err := assignTaskBad.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "optionalReads") {
		t.Fatalf("expected an optionalReads-collision error for an assignTask gap, got %v", err)
	}
	if !strings.Contains(err.Error(), "assignTask") {
		t.Fatalf("error must name the offending action, got %v", err)
	}

	// Negative: every non-directOp arm is in scope, not just assignTask.
	triggerLoomBad := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps: map[string]GapActionSpec{
			"missing_signature": {
				Action: "triggerLoom", Pattern: "leaseSigning", Subject: "row.lease",
				OptionalReads: []string{"row.lease"},
			},
		},
	}}}
	if err := triggerLoomBad.validateWeaverTargets(); err == nil || !strings.Contains(err.Error(), "optionalReads") {
		t.Fatalf("expected an optionalReads-collision error for a triggerLoom gap, got %v", err)
	}
}

// TestValidateWeaverTargets_ActionsCatalogOptionalReadsRejectedOnNonDirectOp
// is the catalog-entry-level mirror of the check above: a goal-authored
// gap's Actions catalog carries the same collision surface, since an entry
// dispatches through the identical buildPlan an explicit gap does.
func TestValidateWeaverTargets_ActionsCatalogOptionalReadsRejectedOnNonDirectOp(t *testing.T) {
	// Positive control: a directOp catalog entry declaring OptionalReads
	// installs clean.
	ok := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "identityErasureComplete",
		Gaps: map[string]GapActionSpec{
			"missing_residue": {
				Goal: json.RawMessage(`{"present":"subject.data.clear"}`),
				Actions: []ActionCatalogEntrySpec{{
					Ref: "sweep", Action: "directOp", Operation: "Sweep",
					OptionalReads: []string{"row.entityKey"},
					Effects:       []json.RawMessage{json.RawMessage(`{"present":"subject.data.clear"}`)},
				}},
			},
		},
	}}}
	if err := ok.validateWeaverTargets(); err != nil {
		t.Fatalf("a directOp catalog entry declaring OptionalReads must install clean, got: %v", err)
	}

	// Omission vector: a catalog entry declaring nothing installs clean.
	none := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "identityErasureComplete",
		Gaps: map[string]GapActionSpec{
			"missing_residue": {
				Goal: json.RawMessage(`{"present":"subject.data.clear"}`),
				Actions: []ActionCatalogEntrySpec{{
					Ref: "sweep", Action: "directOp", Operation: "Sweep",
					Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.clear"}`)},
				}},
			},
		},
	}}}
	if err := none.validateWeaverTargets(); err != nil {
		t.Fatalf("a catalog entry declaring no OptionalReads must install clean, got: %v", err)
	}

	// Negative: a catalog entry whose Action is assignTask hits the same
	// collision. The whole target must be rejected, not just the offending
	// entry silently dropped.
	bad := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "identityErasureComplete",
		Gaps: map[string]GapActionSpec{
			"missing_residue": {
				Goal: json.RawMessage(`{"present":"subject.data.done"}`),
				Actions: []ActionCatalogEntrySpec{{
					Ref: "assign", Action: "assignTask", Operation: "ApproveX",
					Assignee: "row.assignee", Target: "row.entityKey",
					OptionalReads: []string{"row.entityKey"},
					Effects:       []json.RawMessage{json.RawMessage(`{"present":"subject.data.done"}`)},
				}},
			},
		},
	}}}
	err := bad.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "optionalReads") {
		t.Fatalf("expected an optionalReads-collision error for an assignTask catalog entry, got %v", err)
	}
	if !strings.Contains(err.Error(), "actions[0]") {
		t.Fatalf("error must name the offending catalog entry, got %v", err)
	}
}
