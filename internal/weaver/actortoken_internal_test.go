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

	// A real declaration is a LIST, and the token is one entry of it rather
	// than the whole list's shape — the shipped use (a confinement walk
	// alongside the subject's own walks) is exactly this mix. Two things only
	// a multi-entry list can show: each entry resolves on its own arm, and the
	// resolved list keeps the authored order, so the hub an envelope carries
	// at index i is the one the playbook wrote at index i.
	pl, perr = buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Enumerations: []GapEnumeration{
			{Hub: "row.entityKey", Relation: "forSession", Direction: "out"},
			{Hub: tpUnitKey, Relation: "settles", Direction: "in"},
			{Hub: actorToken, Relation: "holdsRole", Direction: "out"},
		}},
		map[string]any{"entityKey": tpIdentity}, 7)
	require.Nil(t, perr, "a mixed list must resolve every arm")
	require.Equal(t, []GapEnumeration{
		{Hub: tpIdentity, Relation: "forSession", Direction: "out"},
		{Hub: tpUnitKey, Relation: "settles", Direction: "in"},
		{Hub: fixtureActorKey, Relation: "holdsRole", Direction: "out"},
	}, pl.enumerations, "each hub resolves on its own arm, in the authored order")
}

// TestBuildPlan_HubBracePlaceholderVocabularyIsClosed pins the hub's grammar as
// DEFAULT-DENY over brace forms: {actor} resolves, and every other placeholder
// shape is refused rather than falling through to the literal arm.
//
// The fall-through is the whole hazard. Each spelling below is syntactically a
// fine key, so without the refusal it installs, loads and dispatches — and
// lands on the envelope naming nothing the script's kv.Links walks from. The
// declaration then covers zero of what it names while the package source reads
// as though it covers the walk, which is the exact outcome declaring a walk
// exists to prevent. It is also why the near-miss cases matter more than the
// nonsense ones: {Actor} and { actor } are what an author actually writes.
func TestBuildPlan_HubBracePlaceholderVocabularyIsClosed(t *testing.T) {
	t.Parallel()

	for _, hub := range []string{
		"{Actor}",                    // wrong case
		"{ actor }",                  // padded
		"{actor:id}",                 // a modifier this surface has no resolver for
		"vtx.identity." + actorToken, // whole segment, but not the whole value
		"{payload.bookingKey}",       // op-descriptor vocabulary, not weaver's
		"{bogus}",
	} {
		_, perr := buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
			GapAction{Action: actionDirectOp, Operation: "Fix", Enumerations: atHub(hub)},
			map[string]any{}, 7)
		require.NotNil(t, perr, "hub %q carries a brace form nothing resolves and must be refused", hub)
		require.Equal(t, errConfig, perr.kind,
			"hub %q: the refusal must be a config error, not a per-row data error: %v", hub, perr.msg)
		require.Contains(t, perr.msg, "enumerations[0].hub", "the refusal must name the offending field")
		require.Contains(t, perr.msg, hub, "the refusal must quote the offending hub")
		require.Contains(t, perr.msg, "WHOLE vertex key", "the refusal must say why a hub cannot carry a fragment")
	}

	// The refusal names the offending ENTRY: a list whose first hub is fine and
	// whose second is a near-miss must report index 1, not a hardcoded zero.
	_, perr := buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Enumerations: []GapEnumeration{
			{Hub: actorToken, Relation: "holdsRole", Direction: "out"},
			{Hub: "{Actor}", Relation: "settles", Direction: "in"},
		}}, map[string]any{}, 7)
	require.NotNil(t, perr, "a valid first hub must not excuse a broken second")
	require.Contains(t, perr.msg, "enumerations[1].hub", "the refusal must name the offending entry's index")

	// The same vectors at load, where a package author meets the verdict
	// first — and on all three authoring surfaces, since they share one grammar.
	gapTarget := func(ga GapAction) *Target {
		return &Target{TargetID: "fixtureActorHubVocab", Gaps: map[string]GapAction{"missing_a": ga}}
	}
	require.ErrorContains(t,
		validateTarget(gapTarget(GapAction{Action: actionDirectOp, Operation: "Fix", Enumerations: atHub("{Actor}")})),
		"enumerations[0].hub", "a near-miss hub must be refused at load")
	require.ErrorContains(t,
		validateTarget(&Target{TargetID: "fixtureActorHubVocabCand", Gaps: map[string]GapAction{
			"missing_a": {Candidates: []GapCandidate{{Action: actionDirectOp, Operation: "Fix",
				Enumerations: atHub("vtx.identity." + actorToken)}}},
		}}),
		"candidates[0]", "a candidate's hub must be refused too")
	require.ErrorContains(t,
		validateTarget(&Target{TargetID: "fixtureActorHubVocabCat", Gaps: map[string]GapAction{
			"missing_a": {
				Goal: json.RawMessage(`{"present":"subject.data.done"}`),
				Actions: []ActionCatalogEntry{{
					Ref: "sweep", Action: actionDirectOp, Operation: "Sweep",
					Enumerations: atHub("{actor:id}"),
					Effects:      []json.RawMessage{json.RawMessage(`{"present":"subject.data.done"}`)},
				}},
			},
		}}),
		"actions[0]", "a catalog entry's hub must be refused too")

	// Positive controls: the vocabulary is closed, not empty. The one admitted
	// brace form and both brace-free arms still resolve.
	for _, tc := range []struct{ hub, want string }{
		{actorToken, fixtureActorKey},
		{tpUnitKey, tpUnitKey},
		{"row.entityKey", tpUnitKey},
	} {
		pl, perr := buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
			GapAction{Action: actionDirectOp, Operation: "Fix", Enumerations: atHub(tc.hub)},
			map[string]any{"entityKey": tpUnitKey}, 7)
		require.Nil(t, perr, "hub %q must still resolve", tc.hub)
		require.Equal(t, tc.want, pl.enumerations[0].Hub)
	}
}

// TestActorToken_RefusedInTheParamsBag closes the last field the token could
// reach. Contract #10 §10.8 admits it "on a hub only — on a param, a
// reads/optionalReads entry, or any other authored value it is refused at
// install and at load", and the params bag is the one list the string-field
// refusal deliberately does not cover: it is the typed literal's home, and so
// the field list validateGapStringFields walks omits it by design.
//
// Nothing in the bag resolves the token, so the failure it prevents is silent
// rather than loud: an authored actorKey: "{actor}" dispatches the literal
// brace string into the op's payload, where a script reads it as a vertex key
// that names nothing. An op that genuinely needs the submitter already has it
// on the envelope.
func TestActorToken_RefusedInTheParamsBag(t *testing.T) {
	t.Parallel()

	bag := map[string]string{"actorKey": actorToken}

	_, perr := buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Params: bag}, map[string]any{}, 7)
	require.NotNil(t, perr, "a params-bag %s must be refused at dispatch", actorToken)
	require.Equal(t, errConfig, perr.kind,
		"the refusal must be a config error, not a per-row data error: %v", perr.msg)
	require.Contains(t, perr.msg, "actorKey", "the refusal must name the offending param")
	require.Contains(t, perr.msg, "enumeration hub", "the refusal must say where the token IS meaningful")

	gapTarget := func(ga GapAction) *Target {
		return &Target{TargetID: "fixtureActorParam", Gaps: map[string]GapAction{"missing_a": ga}}
	}
	err := validateTarget(gapTarget(GapAction{Action: actionDirectOp, Operation: "Fix", Params: bag}))
	require.Error(t, err, "a params-bag %s must be refused at load", actorToken)
	require.ErrorContains(t, err, "actorKey")
	require.ErrorContains(t, err, "enumeration hub")

	// Both planner surfaces carry a params bag of their own and the same rule.
	require.ErrorContains(t, validateTarget(&Target{
		TargetID: "fixtureActorParamCand",
		Gaps: map[string]GapAction{
			"missing_a": {Candidates: []GapCandidate{{Action: actionDirectOp, Operation: "Fix", Params: bag}}},
		},
	}), "candidates[0]", "a candidate's params bag must be refused too")

	require.ErrorContains(t, validateTarget(&Target{
		TargetID: "fixtureActorParamCat",
		Gaps: map[string]GapAction{
			"missing_a": {
				Goal: json.RawMessage(`{"present":"subject.data.done"}`),
				Actions: []ActionCatalogEntry{{
					Ref: "sweep", Action: actionDirectOp, Operation: "Sweep", Params: bag,
					Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.done"}`)},
				}},
			},
		},
	}), "actions[0]", "a catalog entry's params bag must be refused too")

	// Positive controls: the bag keeps all three of its own arms, and the
	// refusal is whole-value like the hub's — a param that merely mentions the
	// token in a larger string is an ordinary literal, since nothing in the bag
	// would have substituted it either way.
	pl, perr := buildPlan(nil, fixtureActorKey, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Params: map[string]string{
			"literal":   tpUnitKey,
			"templated": "row.entityKey",
			"typed":     "json:5",
			"mentions":  "see " + actorToken + " in the design",
		}}, map[string]any{"entityKey": tpUnitKey}, 7)
	require.Nil(t, perr, "the params bag must keep every arm it had")
	payload := pl.payload("")
	require.Equal(t, tpUnitKey, payload["literal"])
	require.Equal(t, tpUnitKey, payload["templated"])
	require.Equal(t, float64(5), payload["typed"])
	require.Equal(t, "see "+actorToken+" in the design", payload["mentions"])
}

// TestValidateEnumerationsScope_DirectOpOnly pins the action-scope gate on the
// declared-walk list, the exact counterpart of the one OptionalReads carries.
// buildPlan reads ga.Enumerations in its directOp arm and in no other, so a
// walk declared on any other action is not redundant but silently DROPPED: the
// envelope goes out with no contextHint enumerations, the script's kv.Links
// runs undeclared, and the package source says otherwise. The token's canonical
// use — an actor-role confinement walk — is exactly the kind an author would
// plausibly hang off an assignTask or a triggerLoom gap.
func TestValidateEnumerationsScope_DirectOpOnly(t *testing.T) {
	t.Parallel()

	gapTarget := func(ga GapAction) *Target {
		return &Target{TargetID: "fixtureEnumScope", Gaps: map[string]GapAction{"missing_a": ga}}
	}

	for _, ga := range []GapAction{
		{Action: actionTriggerLoom, Pattern: "onboarding", Subject: tpUnitKey, Enumerations: atHub(actorToken)},
		{Action: actionAssignTask, Operation: "ApproveX", Assignee: tpIdentity, Target: tpUnitKey,
			Enumerations: atHub(actorToken)},
		{Action: actionProposedOp, Enumerations: atHub(actorToken)},
		{Action: actionSurface, IssueCode: "SomethingStalled", Enumerations: atHub(actorToken)},
	} {
		err := validateTarget(gapTarget(ga))
		require.Error(t, err, "action %q must not be able to declare enumerations", ga.Action)
		require.ErrorContains(t, err, "only meaningful for directOp")
		require.ErrorContains(t, err, ga.Action)
	}

	// Positive control, both halves: directOp may declare them, and every other
	// action may still dispatch — the gate is about the pairing, not the action.
	require.NoError(t, validateTarget(gapTarget(GapAction{
		Action: actionDirectOp, Operation: "Fix", Enumerations: atHub(actorToken)})),
		"directOp must still declare its walks")
	require.NoError(t, validateTarget(gapTarget(GapAction{
		Action: actionAssignTask, Operation: "ApproveX", Assignee: tpIdentity, Target: tpUnitKey})),
		"an action declaring no enumerations must be untouched by the gate")

	// And on both planner surfaces, which share the same dispatch.
	require.ErrorContains(t, validateTarget(&Target{
		TargetID: "fixtureEnumScopeCand",
		Gaps: map[string]GapAction{
			"missing_a": {Candidates: []GapCandidate{{Action: actionAssignTask, Operation: "ApproveX",
				Assignee: tpIdentity, Target: tpUnitKey, Enumerations: atHub(actorToken)}}},
		},
	}), "candidates[0]", "a candidate's enumerations carry the same scope rule")

	require.ErrorContains(t, validateTarget(&Target{
		TargetID: "fixtureEnumScopeCat",
		Gaps: map[string]GapAction{
			"missing_a": {
				Goal: json.RawMessage(`{"present":"subject.data.done"}`),
				Actions: []ActionCatalogEntry{{
					Ref: "assign", Action: actionAssignTask, Operation: "ApproveX",
					Assignee: tpIdentity, Target: tpUnitKey, Enumerations: atHub(actorToken),
					Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.done"}`)},
				}},
			},
		},
	}), "actions[0]", "a catalog entry's enumerations carry the same scope rule")
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

	// The refusal names the offending ENTRY, not just the field: a list whose
	// first hub is fine and whose second is the token must report index 1. The
	// single-entry vectors above cannot tell a correct index from a hardcoded
	// zero, and a message pointing at the wrong entry sends an author to a
	// declaration that is not the broken one.
	_, perr = buildPlan(nil, "", "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Enumerations: []GapEnumeration{
			{Hub: tpUnitKey, Relation: "settles", Direction: "in"},
			{Hub: actorToken, Relation: "holdsRole", Direction: "out"},
		}}, map[string]any{}, 7)
	require.NotNil(t, perr)
	require.Contains(t, perr.msg, "enumerations[1].hub", "the refusal must name the offending entry's index")

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
		{"queue", GapAction{Action: actionAssignTask, Operation: "ApproveX", Queue: actorToken, Target: tpUnitKey}},
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
		{"queue", GapAction{Action: actionAssignTask, Operation: "ApproveX", Queue: actorToken, Target: tpUnitKey}},
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
