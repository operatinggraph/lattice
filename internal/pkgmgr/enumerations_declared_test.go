package pkgmgr

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEmittedBodies_CarryDeclaredEnumerations locks the installer's half of the
// class-(e) declaration wire on BOTH surfaces that carry one: a loom step and a
// weaver gap. The body each spec installs is what the engines deserialize, so a
// declaration that a package writes and the installer drops is a declaration
// that never exists at run time — the rule covered, the delivering line not.
//
// It also pins the omission: a step or gap that declares no walks must emit no
// `enumerations` key at all, so every shipped walk-free spec body keeps its
// exact shape across installs.
func TestEmittedBodies_CarryDeclaredEnumerations(t *testing.T) {
	t.Parallel()

	wantWire := []any{
		map[string]any{"hub": "subject", "relation": "boundTo", "direction": "in"},
		map[string]any{"hub": "subject", "relation": "boundTo", "direction": "out"},
	}

	t.Run("loom step", func(t *testing.T) {
		t.Parallel()
		body := loomPatternSpecBody(LoomPatternSpec{
			PatternID:   "identityErasure",
			SubjectType: "identity",
			Steps: []StepSpec{
				{
					Kind: "systemOp", Operation: "UnbindIdentityCredentials",
					Reads: []string{"subject"},
					Enumerations: []EnumerationSpec{
						{Hub: "subject", Relation: "boundTo", Direction: "in"},
						{Hub: "subject", Relation: "boundTo", Direction: "out"},
					},
				},
				{Kind: "systemOp", Operation: "CompletePattern"},
			},
		})
		steps, ok := body["steps"].([]any)
		require.True(t, ok)
		require.Len(t, steps, 2)

		declaring := steps[0].(map[string]any)
		require.Equal(t, wantWire, declaring["enumerations"])

		walkFree := steps[1].(map[string]any)
		require.NotContains(t, walkFree, "enumerations", "a step declaring no walks must not emit the key")
	})

	t.Run("weaver gap", func(t *testing.T) {
		t.Parallel()
		body := gapActionBody(GapActionSpec{
			Action:    "directOp",
			Operation: "UnbindIdentityCredentials",
			Reads:     []string{"row.entityKey"},
			Enumerations: []EnumerationSpec{
				{Hub: "subject", Relation: "boundTo", Direction: "in"},
				{Hub: "subject", Relation: "boundTo", Direction: "out"},
			},
		})
		require.Equal(t, wantWire, body["enumerations"])

		walkFree := gapActionBody(GapActionSpec{Action: "directOp", Operation: "SealResidue"})
		require.NotContains(t, walkFree, "enumerations", "a gap declaring no walks must not emit the key")
	})
}

// TestValidateEnumerations_RejectsWhatTheProcessorWould keeps install-time
// validation in lockstep with the Processor's envelope parse
// (opwire.ParseEnvelope: hub and relation non-empty, direction "out" or "in").
// A declaration admitted here and refused there is terminal on every
// redelivery — the op never runs and no retry can change that — so the loud
// place to catch it is install.
//
// The step arm additionally holds the hub to the same subject-relative grammar
// as a declared read: it is a key, and a rendered key outside the NATS KV
// charset fails as a hard error rather than an absence.
func TestValidateEnumerations_RejectsWhatTheProcessorWould(t *testing.T) {
	t.Parallel()

	t.Run("loom step", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name    string
			step    StepSpec
			wantErr string
		}{
			{
				"a subject-relative hub is admitted",
				StepSpec{Kind: "systemOp", Operation: "Sweep", Enumerations: []EnumerationSpec{
					{Hub: "subject", Relation: "boundTo", Direction: "in"},
				}}, "",
			},
			{
				"a literal hub is not a template",
				StepSpec{Kind: "systemOp", Operation: "Sweep", Enumerations: []EnumerationSpec{
					{Hub: "vtx.identity.BBsubjectHJKMNPQRSTV", Relation: "boundTo", Direction: "in"},
				}}, "subject-relative templates",
			},
			{
				"an empty relation names no walk",
				StepSpec{Kind: "systemOp", Operation: "Sweep", Enumerations: []EnumerationSpec{
					{Hub: "subject", Relation: "", Direction: "in"},
				}}, "requires a Relation",
			},
			{
				"a direction outside out|in is rejected",
				StepSpec{Kind: "systemOp", Operation: "Sweep", Enumerations: []EnumerationSpec{
					{Hub: "subject", Relation: "boundTo", Direction: "both"},
				}}, "Direction must be",
			},
			{
				"a userTask may not declare walks",
				StepSpec{Kind: "userTask", Operation: "SignLease", Enumerations: []EnumerationSpec{
					{Hub: "subject", Relation: "boundTo", Direction: "in"},
				}}, "Enumerations is a systemOp-only field",
			},
			{
				"an externalTask may not declare walks",
				StepSpec{
					Kind: "externalTask", Adapter: "esign", InstanceOp: "CreateEnvelope", ReplyOp: "ResolveEnvelope",
					Enumerations: []EnumerationSpec{{Hub: "subject", Relation: "boundTo", Direction: "in"}},
				}, "Enumerations is a systemOp-only field",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				def := Definition{LoomPatterns: []LoomPatternSpec{{
					PatternID: "identityErasure", SubjectType: "identity", Steps: []StepSpec{tc.step},
				}}}
				err := def.validateLoomPatterns()
				if tc.wantErr == "" {
					require.NoError(t, err)
					return
				}
				require.ErrorContains(t, err, tc.wantErr)
			})
		}
	})

	t.Run("weaver gap", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name    string
			en      EnumerationSpec
			wantErr string
		}{
			{"a complete declaration is admitted", EnumerationSpec{Hub: "row.entityKey", Relation: "boundTo", Direction: "out"}, ""},
			{"an empty hub names no vertex", EnumerationSpec{Relation: "boundTo", Direction: "out"}, "requires a Hub"},
			{"an empty relation names no walk", EnumerationSpec{Hub: "row.entityKey", Direction: "out"}, "requires a Relation"},
			{"a direction outside out|in is rejected", EnumerationSpec{Hub: "row.entityKey", Relation: "boundTo", Direction: ""}, "Direction must be"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				err := validateGapAction(0, "identityErasureComplete", "missing_residue", GapActionSpec{
					Action:       "directOp",
					Operation:    "UnbindIdentityCredentials",
					Enumerations: []EnumerationSpec{tc.en},
				})
				if tc.wantErr == "" {
					require.NoError(t, err)
					return
				}
				require.ErrorContains(t, err, tc.wantErr)
			})
		}
	})
}

// TestGapActionArtifact_EnumerationsMaterializeAndValidate walks the
// AI-authoring lane for the gap surface: a weaverTarget proposal declaring
// walks must materialize into the same GapActionSpec a hand-authored package
// carries, be reported as an exposed field rather than a smuggled key, and face
// the identical validateGapAction check — no separate, weaker path.
func TestGapActionArtifact_EnumerationsMaterializeAndValidate(t *testing.T) {
	t.Parallel()

	const content = `{
	  "targetId": "identityErasureComplete",
	  "lensRef": "identityErasureResidue",
	  "gaps": {"missing_credentialResidue": {"action": "directOp",
	           "operation": "UnbindIdentityCredentials",
	           "reads": ["row.entityKey"],
	           "enumerations": [{"hub": "row.entityKey", "relation": "boundTo", "direction": "in"}]}}
	}`

	var wc WeaverTargetArtifactContent
	require.NoError(t, json.Unmarshal([]byte(content), &wc))
	require.Empty(t, unknownWeaverTargetFields(json.RawMessage(content)),
		"enumerations is an exposed field, not a smuggled key")

	def := weaverTargetArtifactDefinition(wc, "privacy-base", "1.0.0")
	ga := def.WeaverTargets[0].Gaps["missing_credentialResidue"]
	require.Equal(t,
		[]EnumerationSpec{{Hub: "row.entityKey", Relation: "boundTo", Direction: "in"}},
		ga.Enumerations)
	require.NoError(t, def.validateAll())
}
