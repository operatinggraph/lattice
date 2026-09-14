package weaver

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// atHub is the declared walk every vector below hangs off — the actor-role
// confinement walk a dispatched op's script runs, which is the whole reason
// the {actor} token exists (Contract #10 §10.8). Only the hub varies.
func atHub(hub string) []GapEnumeration {
	return []GapEnumeration{{Hub: hub, Relation: "holdsRole", Direction: "out"}}
}

// TestBuildPlan_ActorTokenHubResolvesToDispatchingActor is the token's whole
// meaning at dispatch: an enumerations[].hub authored as {actor} leaves
// buildPlan as the dispatching engine's own actor key — the identity the
// Actuator stamps on the envelope this plan becomes.
//
// The substitution happens BEFORE the hub travels resolveReadKey, which is
// what confines the token to this one field: the shared resolver refuses it on
// everything else (TestResolveStringParam_ActorTokenRefusedOutsideHub), so a
// hub that reached the resolver still carrying it would fail rather than
// resolve. The positive controls below say the same thing from the other side —
// the field did not become an {actor}-only field, it gained one arm.
func TestBuildPlan_ActorTokenHubResolvesToDispatchingActor(t *testing.T) {
	t.Parallel()

	pl, perr := buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Enumerations: atHub(actorToken)},
		map[string]any{}, 7)
	require.Nil(t, perr, "an %s hub must resolve", actorToken)
	require.Equal(t, []GapEnumeration{{Hub: fixtureActorKey, Relation: "holdsRole", Direction: "out"}},
		pl.enumerations, "the hub must be the dispatching engine's own actor key")

	// Positive control: a literal hub still passes through byte-for-byte.
	pl, perr = buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Enumerations: atHub(tpUnitKey)},
		map[string]any{}, 7)
	require.Nil(t, perr, "a literal hub must still resolve")
	require.Equal(t, tpUnitKey, pl.enumerations[0].Hub)

	// Positive control: a row.<column> hub still resolves from the row, and to
	// the row's value rather than the actor's.
	pl, perr = buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Enumerations: atHub("row.entityKey")},
		map[string]any{"entityKey": tpUnitKey}, 7)
	require.Nil(t, perr, "a row-templated hub must still resolve")
	require.Equal(t, tpUnitKey, pl.enumerations[0].Hub,
		"a row template must resolve from the row, not from the actor")

	// The token is one whole value, not a prefix: a hub that merely contains it
	// is an ordinary literal, exactly as it was before the token existed.
	pl, perr = buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Enumerations: atHub("vtx.identity." + actorToken)},
		map[string]any{}, 7)
	require.Nil(t, perr)
	require.Equal(t, "vtx.identity."+actorToken, pl.enumerations[0].Hub,
		"substitution is whole-value; an embedded token is a literal no resolver treats as a template")
}

// TestBuildPlan_ActorTokenHubEmptyActorIsConfigError pins the one condition the
// substitution must never absorb quietly. An empty actor key substituted into
// the hub would produce an EMPTY hub, and an empty hub is not an inert one: the
// read-drift guard reads a hub's vertex root to decide which live walks a
// declaration covers, and a rootless hub reads as "no root" — so the silently
// substituted "" would ADMIT the undeclared walks the declaration exists to
// cover, with the op's baseline row already retired against it.
//
// errConfig, not errData: no violation row can supply the engine an identity,
// so the defect must not mint a per-entity Health issue per row.
func TestBuildPlan_ActorTokenHubEmptyActorIsConfigError(t *testing.T) {
	t.Parallel()

	_, perr := buildPlan(nil, "", "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Enumerations: atHub(actorToken)},
		map[string]any{}, 7)
	require.NotNil(t, perr, "an %s hub with no actor key must fail, never dispatch an empty hub", actorToken)
	require.Equal(t, errConfig, perr.kind,
		"the refusal must be a config error, not a per-row data error: %v", perr.msg)
	require.Contains(t, perr.msg, "enumerations[0].hub", "the refusal must name the offending field")
	require.Contains(t, perr.msg, "no actor key", "the refusal must name the condition")

	// Control: with no {actor} hub to substitute, an empty actor key changes
	// nothing — the condition is scoped to the token, not to dispatch at large.
	pl, perr := buildPlan(nil, "", "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Enumerations: atHub(tpUnitKey)},
		map[string]any{}, 7)
	require.Nil(t, perr, "a literal hub must not care what the actor key is")
	require.Equal(t, tpUnitKey, pl.enumerations[0].Hub)
}

// TestResolveStringParam_ActorTokenRefusedOutsideHub is the dispatch half of
// the token's confinement. resolveStringParam serves every value that must BE
// a string, and buildPlan substitutes the token before a hub reaches it — so
// every value arriving here still carrying it is a field the token means
// nothing on, and is refused rather than passed through as the literal
// brace-and-letters string it would otherwise be.
//
// The two halves of the damage a pass-through does: on an operationType, a
// pattern ref, an assignee or a target, the gates upstream of dispatch compare
// the field RAW, so the value they read is not the value that dispatches; on a
// reads / optionalReads entry it declares a read the op never makes, which is
// worse than declaring nothing, because a declaration is exactly what the
// read-drift guard adjudicates a live walk against.
//
// errConfig, not errData, for the same reason the typed-literal refusal is:
// the defect is in the playbook and no row can fix it.
func TestResolveStringParam_ActorTokenRefusedOutsideHub(t *testing.T) {
	t.Parallel()

	cases := []struct {
		field string
		ga    GapAction
	}{
		{"subject", GapAction{Action: actionTriggerLoom, Pattern: "onboarding", Subject: actorToken}},
		{"pattern", GapAction{Action: actionTriggerLoom, Pattern: actorToken, Subject: tpUnitKey}},
		{"operation", GapAction{Action: actionAssignTask, Operation: actorToken, Assignee: tpIdentity, Target: tpUnitKey}},
		{"assignee", GapAction{Action: actionAssignTask, Operation: "ApproveX", Assignee: actorToken, Target: tpUnitKey}},
		{"target", GapAction{Action: actionAssignTask, Operation: "ApproveX", Assignee: tpIdentity, Target: actorToken}},
		{"target", GapAction{Action: actionDirectOp, Operation: "Fix", Target: actorToken}},
		{"reads[0]", GapAction{Action: actionDirectOp, Operation: "Fix", Reads: []string{actorToken}}},
		{"optionalReads[0]", GapAction{Action: actionDirectOp, Operation: "Fix", OptionalReads: []string{actorToken}}},
	}
	for _, tc := range cases {
		_, perr := buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a", tc.ga, map[string]any{}, 7)
		require.NotNil(t, perr, "%s must refuse the %s token", tc.field, actorToken)
		require.Equal(t, errConfig, perr.kind,
			"%s: the refusal must be a config error, not a per-row data error that latches a Health issue per entity: %v", tc.field, perr.msg)
		require.Contains(t, perr.msg, tc.field, "the refusal must name the offending field")
		require.Contains(t, perr.msg, "enumeration hub", "the refusal must say where the token IS meaningful")
	}

	// An optionalReads entry is the one list whose failures can be DROPPED
	// rather than raised — but only data-shaped ones. A token no row can fix
	// must fail the gap, not silently vanish from the declared set and leave
	// the walk undeclared.
	_, perr := buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", OptionalReads: []string{actorToken}},
		map[string]any{}, 7)
	require.NotNil(t, perr, "a config error in optionalReads must fail the gap, not drop the entry")
	require.Equal(t, errConfig, perr.kind)

	// Positive controls: the two arms a string field DOES have still work, so
	// the refusal is about the token alone.
	pl, perr := buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Target: tpUnitKey}, map[string]any{}, 7)
	require.Nil(t, perr, "a plain literal key must still resolve")
	require.Equal(t, tpUnitKey, pl.authTarget)

	pl, perr = buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Target: "row.entityKey"},
		map[string]any{"entityKey": tpUnitKey}, 7)
	require.Nil(t, perr, "a row template must still resolve")
	require.Equal(t, tpUnitKey, pl.authTarget)
}

// TestBuildPlan_ActorTokenHubOnCandidateAndCatalogSurfaces proves the
// substitution is not a property of the explicit-gap surface. A candidate's
// enumerations and a catalog entry's are copied verbatim into the GapAction
// buildPlan dispatches (candidateGapAction / catalogEntryGapAction), so all
// three authoring surfaces share one hub grammar — and a token that resolved
// on only one of them would leave a planner-selected action dispatching a hub
// no walk can match.
func TestBuildPlan_ActorTokenHubOnCandidateAndCatalogSurfaces(t *testing.T) {
	t.Parallel()

	cand := GapCandidate{Action: actionDirectOp, Operation: "Fix", Enumerations: atHub(actorToken)}
	pl, perr := buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
		candidateGapAction(cand), map[string]any{}, 7)
	require.Nil(t, perr)
	require.Equal(t, fixtureActorKey, pl.enumerations[0].Hub,
		"a selected candidate's %s hub must resolve to the dispatching actor", actorToken)

	entry := ActionCatalogEntry{Ref: "confine", Action: actionDirectOp, Operation: "Fix",
		Enumerations: atHub(actorToken)}
	pl, perr = buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
		catalogEntryGapAction(entry), map[string]any{}, 7)
	require.Nil(t, perr)
	require.Equal(t, fixtureActorKey, pl.enumerations[0].Hub,
		"a synthesized catalog leg's %s hub must resolve to the dispatching actor", actorToken)
}

// TestValidateTarget_RejectsActorTokenOutsideHub is the load-time half of the
// confinement resolveStringParam enforces at dispatch. A target vertex can
// reach this engine without passing through this binary's installer (a
// hand-written meta vertex, an artifact applied by an older build, a version
// skew), so the refusal is taken here too — and taken over the FIELD LIST, not
// per authoring surface, because the gap, its candidates and its catalog all
// funnel through validateGapStringFields.
//
// A load-time verdict is also the only one that arrives before the damage: an
// {actor} read entry is a syntactically fine key, so without this it installs,
// loads and dispatches, declaring a read the op never makes.
func TestValidateTarget_RejectsActorTokenOutsideHub(t *testing.T) {
	t.Parallel()

	gapTarget := func(ga GapAction) *Target {
		return &Target{TargetID: "fixtureActorStr", Gaps: map[string]GapAction{"missing_a": ga}}
	}

	cases := []struct {
		field string
		ga    GapAction
	}{
		{"subject", GapAction{Action: actionTriggerLoom, Pattern: "onboarding", Subject: actorToken}},
		{"pattern", GapAction{Action: actionTriggerLoom, Pattern: actorToken, Subject: tpUnitKey}},
		{"operation", GapAction{Action: actionDirectOp, Operation: actorToken}},
		{"assignee", GapAction{Action: actionAssignTask, Operation: "ApproveX", Assignee: actorToken, Target: tpUnitKey}},
		{"target", GapAction{Action: actionDirectOp, Operation: "Fix", Target: actorToken}},
		{"reads[0]", GapAction{Action: actionDirectOp, Operation: "Fix", Reads: []string{actorToken}}},
		{"optionalReads[0]", GapAction{Action: actionDirectOp, Operation: "Fix", OptionalReads: []string{actorToken}}},
	}
	for _, tc := range cases {
		err := validateTarget(gapTarget(tc.ga))
		require.Error(t, err, "%s carrying the %s token must be refused at load", tc.field, actorToken)
		require.ErrorContains(t, err, tc.field)
		require.ErrorContains(t, err, "enumeration hub")
	}

	// Positive control, and the point of the whole gate: the hub is the ONE
	// field that admits the token, so the same target loads clean when the
	// token sits where it means something — alongside plain and templated
	// values on every other field, and the params bag keeping its own token.
	require.NoError(t, validateTarget(gapTarget(GapAction{
		Action: actionDirectOp, Operation: "Fix", Target: "row.entityKey",
		Reads:         []string{tpUnitKey, "row.entityKey"},
		OptionalReads: []string{"row.priorClaimKey"},
		Enumerations:  atHub(actorToken),
		Params:        map[string]string{"limit": "json:5"},
	})), "an %s hub must load, and the refusal must not reach the other fields' valid arms", actorToken)

	// The typed literal stays refused on a hub: the two tokens have different
	// scopes, and widening one must not widen the other.
	require.ErrorContains(t,
		validateTarget(gapTarget(GapAction{Action: actionDirectOp, Operation: "Fix",
			Enumerations: atHub(`json:"` + tpUnitKey + `"`)})),
		"enumerations[0].hub",
		"the typed literal has no valid form on a hub either, and still has none")

	// The candidate surface carries the same fields and the same refusal.
	candidateBad := &Target{
		TargetID: "fixtureActorStrCand",
		Gaps: map[string]GapAction{
			"missing_a": {Candidates: []GapCandidate{{Action: actionDirectOp, Operation: "Fix",
				Reads: []string{actorToken}}}},
		},
	}
	candErr := validateTarget(candidateBad)
	require.Error(t, candErr, "a candidate's string field must be refused too")
	require.ErrorContains(t, candErr, "candidates[0]")
	require.ErrorContains(t, candErr, "reads[0]")

	// And so does the catalog surface.
	catalogBad := &Target{
		TargetID: "fixtureActorStrCat",
		Gaps: map[string]GapAction{
			"missing_a": {
				Goal: json.RawMessage(`{"present":"subject.data.done"}`),
				Actions: []ActionCatalogEntry{{
					Ref: "sweep", Action: actionDirectOp, Operation: "Sweep",
					OptionalReads: []string{actorToken},
					Effects:       []json.RawMessage{json.RawMessage(`{"present":"subject.data.done"}`)},
				}},
			},
		},
	}
	catErr := validateTarget(catalogBad)
	require.Error(t, catErr, "a catalog entry's string field must be refused too")
	require.ErrorContains(t, catErr, "actions[0]")
	require.ErrorContains(t, catErr, "optionalReads[0]")

	// Positive control on both planner surfaces: each may hub on the token.
	require.NoError(t, validateTarget(&Target{
		TargetID: "fixtureActorStrCandOK",
		Gaps: map[string]GapAction{
			"missing_a": {Candidates: []GapCandidate{{Action: actionDirectOp, Operation: "Fix",
				Enumerations: atHub(actorToken)}}},
		},
	}), "a candidate's %s hub must load", actorToken)

	require.NoError(t, validateTarget(&Target{
		TargetID: "fixtureActorStrCatOK",
		Gaps: map[string]GapAction{
			"missing_a": {
				Goal: json.RawMessage(`{"present":"subject.data.done"}`),
				Actions: []ActionCatalogEntry{{
					Ref: "sweep", Action: actionDirectOp, Operation: "Sweep",
					Enumerations: atHub(actorToken),
					Effects:      []json.RawMessage{json.RawMessage(`{"present":"subject.data.done"}`)},
				}},
			},
		},
	}), "a catalog entry's %s hub must load", actorToken)
}
