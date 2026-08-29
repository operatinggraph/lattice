package pkgmgr

import (
	"encoding/json"
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
