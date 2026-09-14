package pkgmgr

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// atHub is the declared walk every vector below hangs off — the actor-role
// confinement walk a dispatched op's script runs, which is the whole reason
// the {actor} token exists (Contract #10 §10.8). Only the hub varies.
func atHub(hub string) []EnumerationSpec {
	return []EnumerationSpec{{Hub: hub, Relation: "holdsRole", Direction: "out"}}
}

// TestValidateWeaverTargets_ActorTokenOutsideHubRejected is the install half of
// the {actor} token's confinement, on the same dual posture reservedGapParam
// documents: "the engine's validateTarget rejects it at load; install rejects
// it first for a clearer author error."
//
// The token is admitted on an enumeration hub alone, where the engine
// substitutes its own actor key at plan time. Everywhere else it is inert — a
// literal string of braces and letters that nothing resolves — and inert is
// precisely what makes the install refusal worth taking. A gap declaring
// `reads: ["{actor}"]` is a syntactically fine key: it installs, loads and
// dispatches, declaring a read the op never makes, and a declaration is what
// the read-drift guard adjudicates a live walk against. Nothing downstream
// would ever complain; the author would simply have a walk declared on paper
// and wrong in fact.
func TestValidateWeaverTargets_ActorTokenOutsideHubRejected(t *testing.T) {
	t.Parallel()

	gap := func(ga GapActionSpec) Definition {
		return Definition{WeaverTargets: []WeaverTargetSpec{{
			TargetID: "leaseSigning",
			Gaps:     map[string]GapActionSpec{"missing_signature": ga},
		}}}
	}

	cases := []struct {
		field string
		ga    GapActionSpec
	}{
		{"subject", GapActionSpec{Action: "triggerLoom", Pattern: "onboarding", Subject: actorToken}},
		{"pattern", GapActionSpec{Action: "triggerLoom", Pattern: actorToken, Subject: "row.entityKey"}},
		{"operation", GapActionSpec{Action: "directOp", Operation: actorToken}},
		{"assignee", GapActionSpec{Action: "assignTask", Operation: "ApproveX",
			Assignee: actorToken, Target: "row.entityKey"}},
		{"target", GapActionSpec{Action: "directOp", Operation: "Fix", Target: actorToken}},
		{"reads[0]", GapActionSpec{Action: "directOp", Operation: "Fix", Reads: []string{actorToken}}},
		{"optionalReads[0]", GapActionSpec{Action: "directOp", Operation: "Fix",
			OptionalReads: []string{actorToken}}},
	}
	for _, tc := range cases {
		err := gap(tc.ga).validateWeaverTargets()
		require.Error(t, err, "%s carrying the %s token must be refused at install", tc.field, actorToken)
		require.ErrorContains(t, err, tc.field, "the refusal must name the offending field")
		require.ErrorContains(t, err, "enumeration hub", "the refusal must say where the token IS meaningful")
	}

	// Positive control, and the point of the whole gate: the hub is the ONE
	// field that admits the token, so a target hubbing on it installs clean —
	// alongside plain and templated values on every other field, and the params
	// bag keeping its own token.
	require.NoError(t, gap(GapActionSpec{
		Action: "directOp", Operation: "Fix", Target: "row.entityKey",
		Reads:         []string{"vtx.unit.AAunitHJKMNPQRSTUVWX", "row.entityKey"},
		OptionalReads: []string{"row.priorClaimKey"},
		Enumerations:  atHub(actorToken),
		Params:        map[string]string{"limit": "json:5"},
	}).validateWeaverTargets(), "an %s hub must install, and the refusal must not reach the other fields' valid arms", actorToken)

	// Positive control on the hub's other two arms — a literal key and a
	// row.<column> template — so the field did not become an {actor}-only one.
	require.NoError(t, gap(GapActionSpec{
		Action: "directOp", Operation: "Fix",
		Enumerations: atHub("vtx.unit.AAunitHJKMNPQRSTUVWX"),
	}).validateWeaverTargets(), "a literal hub must still install")
	require.NoError(t, gap(GapActionSpec{
		Action: "directOp", Operation: "Fix", Enumerations: atHub("row.entityKey"),
	}).validateWeaverTargets(), "a row-templated hub must still install")

	// The typed literal stays refused on a hub: the two tokens have different
	// scopes, and widening one must not widen the other.
	require.ErrorContains(t, gap(GapActionSpec{
		Action: "directOp", Operation: "Fix",
		Enumerations: atHub(`json:"vtx.unit.AAunitHJKMNPQRSTUVWX"`),
	}).validateWeaverTargets(), "enumerations[0].hub",
		"the typed literal has no valid form on a hub either, and still has none")

	// The catalog surface carries the same fields and the same refusal — a
	// synthesized leg dispatches through the identical buildPlan an explicit
	// gap does.
	catalog := func(entry ActionCatalogEntrySpec) Definition {
		entry.Ref = "sweep"
		entry.Effects = []json.RawMessage{json.RawMessage(`{"present":"subject.data.clear"}`)}
		return Definition{WeaverTargets: []WeaverTargetSpec{{
			TargetID: "identityErasureComplete",
			Gaps: map[string]GapActionSpec{
				"missing_residue": {
					Goal:    json.RawMessage(`{"present":"subject.data.clear"}`),
					Actions: []ActionCatalogEntrySpec{entry},
				},
			},
		}}}
	}

	err := catalog(ActionCatalogEntrySpec{Action: "directOp", Operation: "Sweep",
		Reads: []string{actorToken}}).validateWeaverTargets()
	require.Error(t, err, "a catalog entry's string field must be refused too")
	require.ErrorContains(t, err, "actions[0]")
	require.ErrorContains(t, err, `ref "sweep"`)
	require.ErrorContains(t, err, "reads[0]")
	require.ErrorContains(t, err, "enumeration hub")

	require.NoError(t, catalog(ActionCatalogEntrySpec{Action: "directOp", Operation: "Sweep",
		Enumerations: atHub(actorToken)}).validateWeaverTargets(),
		"a catalog entry's %s hub must install", actorToken)
}
