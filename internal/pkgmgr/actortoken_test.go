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
		{"queue", GapActionSpec{Action: "assignTask", Operation: "ApproveX",
			Queue: actorToken, Target: "row.entityKey"}},
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

// TestValidateWeaverTargets_ActorTokenInParamsBagRejected closes the one field
// the string-field refusal above deliberately does not cover. Contract #10
// §10.8 admits the token "on a hub only — on a param, a reads/optionalReads
// entry, or any other authored value it is refused at install and at load", and
// dispatchStringFields omits the params bag on purpose: that bag is the typed
// literal's home, and the two tokens' homes are exactly opposite.
//
// The refusal is worth taking at install precisely because nothing downstream
// fails. The token is a well-formed string, so an authored actorKey: "{actor}"
// installs, loads and dispatches into the op's payload as its own literal text,
// to be read by a script as a vertex key that names nothing. An op that
// genuinely needs the submitter's identity already has it: every envelope
// carries actor, and the Processor hands the script that identity directly.
func TestValidateWeaverTargets_ActorTokenInParamsBagRejected(t *testing.T) {
	t.Parallel()

	gapParams := func(params map[string]string) Definition {
		return Definition{WeaverTargets: []WeaverTargetSpec{{
			TargetID: "leaseSigning",
			Gaps: map[string]GapActionSpec{
				"missing_signature": {Action: "directOp", Operation: "SignLease", Params: params},
			},
		}}}
	}

	err := gapParams(map[string]string{"actorKey": actorToken}).validateWeaverTargets()
	require.Error(t, err, "a params-bag %s must be refused at install", actorToken)
	require.ErrorContains(t, err, `param "actorKey"`, "the refusal must name the offending param")
	require.ErrorContains(t, err, "enumeration hub", "the refusal must say where the token IS meaningful")
	require.ErrorContains(t, err, "envelope's actor", "the refusal must name the remedy the op already has")
	require.ErrorContains(t, err, `gaps key "missing_signature"`)

	// Positive controls: the bag keeps all three of its own arms, and the
	// refusal is whole-value — a param that merely mentions the token in a
	// larger string is an ordinary literal, since nothing in the bag would have
	// substituted it either way.
	require.NoError(t, gapParams(map[string]string{
		"literal":   "vtx.unit.AAunitHJKMNPQRSTUVWX",
		"templated": "row.entityKey",
		"typed":     "json:5",
		"mentions":  "see " + actorToken + " in the design",
	}).validateWeaverTargets(), "the params bag must keep every arm it had")

	// The catalog surface carries its own params bag and the same rule.
	catalogBad := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "identityErasureComplete",
		Gaps: map[string]GapActionSpec{
			"missing_residue": {
				Goal: json.RawMessage(`{"present":"subject.data.clear"}`),
				Actions: []ActionCatalogEntrySpec{{
					Ref: "sweep", Action: "directOp", Operation: "Sweep",
					Params:  map[string]string{"actorKey": actorToken},
					Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.clear"}`)},
				}},
			},
		},
	}}}
	catErr := catalogBad.validateWeaverTargets()
	require.Error(t, catErr, "a catalog entry's params bag must be refused too")
	require.ErrorContains(t, catErr, "actions[0]")
	require.ErrorContains(t, catErr, `ref "sweep"`)
	require.ErrorContains(t, catErr, `param "actorKey"`)
}

// TestValidateWeaverTargets_HubBracePlaceholderVocabularyIsClosed pins the gap
// hub's brace vocabulary as DEFAULT-DENY at install: {actor} installs, and
// every other placeholder shape is refused rather than falling through to the
// literal arm.
//
// Each spelling below is a syntactically fine key, which is the whole hazard —
// nothing downstream rejects it. It installs, loads, dispatches, lands on the
// envelope, and matches no walk the script makes, leaving the declaration
// covering zero of what it names while the package source reads as though it
// covers the walk. The near-miss spellings matter most: {Actor} and { actor }
// are what an author actually writes.
//
// This is the same closure the op-descriptor dispatch surface takes on its own
// hubs (opdispatchtemplates.go's enumerationHubRules), which is what lets
// Contract #10 §10.8 say the token spells and means the same on both.
func TestValidateWeaverTargets_HubBracePlaceholderVocabularyIsClosed(t *testing.T) {
	t.Parallel()

	gapHub := func(hub string) Definition {
		return Definition{WeaverTargets: []WeaverTargetSpec{{
			TargetID: "leaseSigning",
			Gaps: map[string]GapActionSpec{
				"missing_signature": {Action: "directOp", Operation: "Fix", Enumerations: atHub(hub)},
			},
		}}}
	}

	for _, hub := range []string{
		"{Actor}",                    // wrong case
		"{ actor }",                  // padded
		"{actor:id}",                 // a modifier this surface has no resolver for
		"vtx.identity." + actorToken, // whole segment, but not the whole value
		"{payload.bookingKey}",       // op-descriptor vocabulary, not a weaver gap's
		"{bogus}",
	} {
		err := gapHub(hub).validateWeaverTargets()
		require.Error(t, err, "hub %q carries a brace form nothing resolves and must be refused at install", hub)
		require.ErrorContains(t, err, "enumerations[0].hub", "the refusal must name the offending field")
		require.ErrorContains(t, err, hub, "the refusal must quote the offending hub")
		require.ErrorContains(t, err, "WHOLE vertex key", "the refusal must say why a hub cannot carry a fragment")
	}

	// Positive controls: the vocabulary is closed, not empty.
	for _, hub := range []string{actorToken, "vtx.unit.AAunitHJKMNPQRSTUVWX", "row.entityKey"} {
		require.NoError(t, gapHub(hub).validateWeaverTargets(), "hub %q must still install", hub)
	}

	// The catalog surface shares the grammar and the verdict.
	catalogBad := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "identityErasureComplete",
		Gaps: map[string]GapActionSpec{
			"missing_residue": {
				Goal: json.RawMessage(`{"present":"subject.data.clear"}`),
				Actions: []ActionCatalogEntrySpec{{
					Ref: "sweep", Action: "directOp", Operation: "Sweep",
					Enumerations: atHub("{Actor}"),
					Effects:      []json.RawMessage{json.RawMessage(`{"present":"subject.data.clear"}`)},
				}},
			},
		},
	}}}
	catErr := catalogBad.validateWeaverTargets()
	require.Error(t, catErr, "a catalog entry's hub must be refused too")
	require.ErrorContains(t, catErr, "actions[0]")
	require.ErrorContains(t, catErr, "enumerations[0].hub")
}

// TestValidateWeaverTargets_EnumerationsScopeIsDirectOpOnly pins the
// action-scope gate on the declared-walk list, the exact counterpart of the one
// OptionalReads already carries and refused for a sharper reason: the engine's
// buildPlan reads Enumerations in its directOp arm and in no other, so a walk
// declared on any other action is silently DROPPED at dispatch. The envelope
// goes out with no contextHint enumerations, the script's kv.Links runs
// undeclared, and the package source says otherwise.
//
// The {actor} token's canonical use — an actor-role confinement walk — is
// exactly the kind an author would plausibly hang off an assignTask or a
// triggerLoom gap, which is what makes an inert declaration here likely rather
// than hypothetical.
func TestValidateWeaverTargets_EnumerationsScopeIsDirectOpOnly(t *testing.T) {
	t.Parallel()

	gap := func(ga GapActionSpec) Definition {
		return Definition{WeaverTargets: []WeaverTargetSpec{{
			TargetID: "leaseSigning",
			Gaps:     map[string]GapActionSpec{"missing_signature": ga},
		}}}
	}

	for _, ga := range []GapActionSpec{
		{Action: "triggerLoom", Pattern: "onboarding", Subject: "row.entityKey", Enumerations: atHub(actorToken)},
		{Action: "assignTask", Operation: "ApproveX", Assignee: "row.assignee", Target: "row.entityKey",
			Enumerations: atHub(actorToken)},
		{Action: "proposedOp", Enumerations: atHub(actorToken)},
		{Action: "surface", IssueCode: "SomethingStalled", Enumerations: atHub(actorToken)},
	} {
		err := gap(ga).validateWeaverTargets()
		require.Error(t, err, "action %q must not be able to declare enumerations", ga.Action)
		require.ErrorContains(t, err, "only meaningful for directOp")
		require.ErrorContains(t, err, "run undeclared", "the refusal must name what goes wrong")
		require.ErrorContains(t, err, ga.Action)
	}

	// Positive control, both halves: directOp may declare them, and an action
	// declaring none is untouched — the gate is about the pairing.
	require.NoError(t, gap(GapActionSpec{Action: "directOp", Operation: "Fix",
		Enumerations: atHub(actorToken)}).validateWeaverTargets(), "directOp must still declare its walks")
	require.NoError(t, gap(GapActionSpec{Action: "assignTask", Operation: "ApproveX",
		Assignee: "row.assignee", Target: "row.entityKey"}).validateWeaverTargets(),
		"an action declaring no enumerations must be untouched by the gate")

	// The catalog surface carries the same pairing rule.
	catalogBad := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "identityErasureComplete",
		Gaps: map[string]GapActionSpec{
			"missing_residue": {
				Goal: json.RawMessage(`{"present":"subject.data.clear"}`),
				Actions: []ActionCatalogEntrySpec{{
					Ref: "assign", Action: "assignTask", Operation: "ApproveX",
					Assignee: "row.assignee", Target: "row.entityKey",
					Enumerations: atHub(actorToken),
					Effects:      []json.RawMessage{json.RawMessage(`{"present":"subject.data.clear"}`)},
				}},
			},
		},
	}}}
	catErr := catalogBad.validateWeaverTargets()
	require.Error(t, catErr, "a catalog entry's enumerations carry the same scope rule")
	require.ErrorContains(t, catErr, "actions[0]")
	require.ErrorContains(t, catErr, "only meaningful for directOp")
}
