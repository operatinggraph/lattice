package pkgmgr

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLoomPatternSpecBody_EmitsDeclaredReads locks the installer's half of the
// systemOp declared-read wire: the spec body a package's LoomPatternSpec
// installs must carry `reads`/`optionalReads` under exactly the json keys
// loom.Step deserializes, and must OMIT them for a step that declares none —
// otherwise every shipped read-free pattern's spec body changes shape on the
// next install for no reason.
func TestLoomPatternSpecBody_EmitsDeclaredReads(t *testing.T) {
	t.Parallel()

	body := loomPatternSpecBody(LoomPatternSpec{
		PatternID:   "identityErasure",
		SubjectType: "identity",
		Steps: []StepSpec{
			{
				Kind: "systemOp", Operation: "ShredIdentityKey",
				Reads: []string{"subject"}, OptionalReads: []string{"subject.piiKey"},
			},
			{Kind: "systemOp", Operation: "PurgeIdentityDedupFootprint"},
		},
	})

	steps, ok := body["steps"].([]any)
	require.True(t, ok)
	require.Len(t, steps, 2)

	declaring := steps[0].(map[string]any)
	require.Equal(t, []string{"subject"}, declaring["reads"])
	require.Equal(t, []string{"subject.piiKey"}, declaring["optionalReads"])

	readFree := steps[1].(map[string]any)
	require.NotContains(t, readFree, "reads", "a step declaring nothing must not emit the key")
	require.NotContains(t, readFree, "optionalReads")
}

// TestValidateLoomPatterns_DeclaredReads is the installer-side mirror of loom's
// TestPatternValidate_DeclaredReads. validateLoomPatterns exists so an install
// never admits a pattern the engine would reject at CDC load — a divergence
// here would let a package install cleanly and then leave its pattern dark,
// which is the failure mode the two validators are kept in lockstep to prevent.
func TestValidateLoomPatterns_DeclaredReads(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		step    StepSpec
		wantErr string
	}{
		{
			"systemOp: bare subject",
			StepSpec{Kind: "systemOp", Operation: "ShredIdentityKey", Reads: []string{"subject"}}, "",
		},
		{
			"systemOp: subject aspect",
			StepSpec{Kind: "systemOp", Operation: "ShredIdentityKey", OptionalReads: []string{"subject.piiKey"}}, "",
		},
		{
			"systemOp: a literal key is not a template",
			StepSpec{Kind: "systemOp", Operation: "ShredIdentityKey", Reads: []string{"vtx.identity.BBsubjectHJKMNPQRSTV"}},
			"subject-relative templates",
		},
		{
			"systemOp: an aspect localName is a single segment",
			StepSpec{Kind: "systemOp", Operation: "ShredIdentityKey", Reads: []string{"subject.piiKey.data"}},
			"not a Contract #1 aspect localName",
		},
		{
			"systemOp: an aspect outside the localName charset",
			StepSpec{Kind: "systemOp", Operation: "ShredIdentityKey", Reads: []string{"subject.pii key"}},
			"not a Contract #1 aspect localName",
		},
		{
			"systemOp: an aspect carrying a NATS wildcard",
			StepSpec{Kind: "systemOp", Operation: "ShredIdentityKey", OptionalReads: []string{"subject.*"}},
			"not a Contract #1 aspect localName",
		},
		{
			"userTask may not declare reads",
			StepSpec{Kind: "userTask", Operation: "SignLease", Reads: []string{"subject"}},
			"Reads is a systemOp-only field",
		},
		{
			"externalTask may not declare optionalReads",
			StepSpec{
				Kind: "externalTask", Adapter: "esign", InstanceOp: "CreateEnvelope",
				ReplyOp: "ResolveEnvelope", OptionalReads: []string{"subject"},
			},
			"OptionalReads is a systemOp-only field",
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
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestKnownStepFields_CoverStepArtifact is the drift guard between StepArtifact
// and the unknown-key scan that audits an AI-authored proposal. A field added
// to StepArtifact but not to knownStepFields is the silent half of the pair:
// json.Unmarshal accepts the key, so the step materializes with it set, while
// unknownLoomPatternFields never reports it — the §5 stored-invalid audit trail
// the scan exists to produce would simply not mention it. The reverse (a key in
// the map the struct cannot carry) is the loud half and also caught here.
func TestKnownStepFields_CoverStepArtifact(t *testing.T) {
	t.Parallel()

	tags := map[string]bool{}
	rt := reflect.TypeOf(StepArtifact{})
	for i := range rt.NumField() {
		tag := rt.Field(i).Tag.Get("json")
		require.NotEmpty(t, tag, "StepArtifact.%s carries no json tag", rt.Field(i).Name)
		tags[strings.Split(tag, ",")[0]] = true
	}

	for tag := range tags {
		require.True(t, knownStepFields[tag],
			"StepArtifact exposes %q but knownStepFields does not list it — a smuggled %q would bypass the stored-invalid audit trail", tag, tag)
	}
	for k := range knownStepFields {
		require.True(t, tags[k], "knownStepFields lists %q but StepArtifact has no such field", k)
	}
}

// TestStepArtifact_DeclaredReadsMaterializeAndValidate walks the AI-authoring
// lane end to end for the new fields: a loomPattern proposal declaring reads
// must materialize into the same StepSpec a hand-authored package would carry
// (the StepSpec(s) conversion is the compile-time half of that guarantee), and
// must then face the identical validateLoomPatterns check — no separate, weaker
// path for an AI-authored pattern.
func TestStepArtifact_DeclaredReadsMaterializeAndValidate(t *testing.T) {
	t.Parallel()

	const content = `{
	  "patternId": "identityErasure",
	  "subjectType": "identity",
	  "steps": [{"kind": "systemOp", "operation": "ShredIdentityKey",
	             "reads": ["subject"], "optionalReads": ["subject.piiKey"]}]
	}`

	var lp LoomPatternArtifactContent
	require.NoError(t, json.Unmarshal([]byte(content), &lp))
	require.Empty(t, unknownLoomPatternFields(json.RawMessage(content)),
		"reads/optionalReads are exposed fields, not smuggled keys")

	def := loomPatternArtifactDefinition(lp, "privacy-base", "1.0.0")
	step := def.LoomPatterns[0].Steps[0]
	require.Equal(t, []string{"subject"}, step.Reads)
	require.Equal(t, []string{"subject.piiKey"}, step.OptionalReads)
	require.NoError(t, def.validateLoomPatterns())

	// And the same content with a literal key fails the shared validator, rather
	// than installing a pattern the engine would reject at CDC load.
	bad := strings.Replace(content, `"reads": ["subject"]`, `"reads": ["vtx.identity.BBsubjectHJKMNPQRSTV"]`, 1)
	var badLP LoomPatternArtifactContent
	require.NoError(t, json.Unmarshal([]byte(bad), &badLP))
	require.ErrorContains(t,
		loomPatternArtifactDefinition(badLP, "privacy-base", "1.0.0").validateLoomPatterns(),
		"subject-relative templates")
}
