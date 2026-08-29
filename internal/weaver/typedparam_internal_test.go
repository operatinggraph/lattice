package weaver

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// Fixture keys: 20-char NanoIDs over the canonical alphabet
// (internal/substrate.Alphabet — no I, O or 0).
const (
	tpEntityID = "AAentHJKMNPQRSTUVWXY"
	tpUnitKey  = "vtx.unit.AAunitHJKMNPQRSTUVWX"
	tpIdentity = "vtx.identity.AAidHJKMNPQRSTUVWXYZ"
)

// directOpPlan builds a directOp plan for one params bag against one row and
// returns the resolved payload. The op reads nothing and targets nothing, so
// the assertions below are about the param grammar alone.
func directOpPlan(t *testing.T, params map[string]string, row map[string]any) map[string]any {
	t.Helper()
	pl, perr := buildPlan(nil, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Params: params}, row, 7)
	require.Nil(t, perr, "buildPlan must resolve the params bag")
	return pl.payload("")
}

// directOpParamError resolves a one-param directOp gap and returns the failure.
func directOpParamError(t *testing.T, value string) *planError {
	t.Helper()
	_, perr := buildPlan(nil, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Params: map[string]string{"limit": value}}, map[string]any{}, 7)
	return perr
}

// TestResolveParam_TypedLiteralCarriesItsJSONType pins the json:<literal> arm
// of the param value grammar: the payload carries the value encoding/json
// decodes the suffix into, with its GO TYPE, not the authored spelling. Types
// are asserted, not just values — the whole point of the token is that a
// playbook declaring 5 against an op's "type":"number" InputSchema sends a
// number, where a plain literal could only ever send the string "5".
func TestResolveParam_TypedLiteralCarriesItsJSONType(t *testing.T) {
	t.Parallel()
	payload := directOpPlan(t, map[string]string{
		"limit":    "json:5",
		"ratio":    "json:1.5",
		"enabled":  "json:true",
		"disabled": "json:false",
		"zero":     "json:0",
		"label":    `json:"leased"`,
		"tags":     `json:["a",2]`,
		"opts":     `json:{"deep":{"k":1}}`,
	}, map[string]any{})

	require.Equal(t, float64(5), payload["limit"], "json:5 must dispatch as a number")
	require.IsType(t, float64(0), payload["limit"])
	require.Equal(t, 1.5, payload["ratio"])
	require.Equal(t, true, payload["enabled"], "json:true must dispatch as a bool")
	require.IsType(t, false, payload["enabled"])
	// The two falsy-but-valid literals: neither may be mistaken for absent.
	require.Equal(t, false, payload["disabled"])
	require.IsType(t, false, payload["disabled"])
	require.Equal(t, float64(0), payload["zero"])
	require.IsType(t, float64(0), payload["zero"])

	require.Equal(t, "leased", payload["label"], "a JSON string literal dispatches as its unquoted string")
	require.IsType(t, "", payload["label"])

	require.Equal(t, []any{"a", float64(2)}, payload["tags"], "an array literal dispatches as a slice")
	require.IsType(t, []any{}, payload["tags"])
	require.Equal(t, map[string]any{"deep": map[string]any{"k": float64(1)}}, payload["opts"])
	require.IsType(t, map[string]any{}, payload["opts"])
}

// TestResolveParam_PlainLiteralIsUntouched is the regression that matters: the
// one literal the live package corpus actually declares
// (packages/lease-signing/targets.go's status "leased") must still reach the
// payload as exactly that Go string. A value that merely CONTAINS the token,
// or carries it anywhere but the leading position, is likewise a plain string
// — the grammar classifies by a leading token (a whitelist), so an
// unrecognised shape falls through to plain-string rather than being guessed
// at.
func TestResolveParam_PlainLiteralIsUntouched(t *testing.T) {
	t.Parallel()
	payload := directOpPlan(t, map[string]string{
		"status":   "leased",
		"embedded": "see json:5 for the shape",
		"trailing": "5json:",
		"spaced":   " json:5",
		"numeric":  "5",
		"boolish":  "true",
		"upper":    "JSON:5",
	}, map[string]any{})

	require.Equal(t, "leased", payload["status"], "the live corpus's one plain literal must not change shape")
	require.IsType(t, "", payload["status"])
	require.Equal(t, "see json:5 for the shape", payload["embedded"], "a value merely containing the token is a plain string")
	require.Equal(t, "5json:", payload["trailing"])
	require.Equal(t, " json:5", payload["spaced"], "the token is matched on the leading bytes, not after trimming")
	// The fail-silent shape the token exists to give authors a way out of:
	// unprefixed "5" is still, deliberately, the string "5".
	require.Equal(t, "5", payload["numeric"])
	require.IsType(t, "", payload["numeric"])
	require.Equal(t, "true", payload["boolish"])
	require.Equal(t, "JSON:5", payload["upper"], "the token is case-sensitive")
}

// TestResolveParam_TypedLiteralEscapesTheToken pins the escape: a genuine
// string that must itself begin with the token is expressible as its own JSON
// string, so no value is unreachable in the grammar.
func TestResolveParam_TypedLiteralEscapesTheToken(t *testing.T) {
	t.Parallel()
	payload := directOpPlan(t, map[string]string{
		"escaped": `json:"json:foo"`,
		"twice":   `json:"json:json:foo"`,
	}, map[string]any{})

	require.Equal(t, "json:foo", payload["escaped"], `json:"json:foo" must resolve to the string json:foo`)
	require.IsType(t, "", payload["escaped"])
	require.Equal(t, "json:json:foo", payload["twice"], "the decode runs once — the escape is not recursive")
}

// TestResolveParam_RowTemplateWins pins the ORDER of the two tokens, which is
// load-bearing in the one direction that can actually differ: row.<column> is
// resolved first and its substituted value is handed to the payload as-is,
// never re-scanned for the typed-literal token. A lens row whose column
// literally holds "json:5" (a status string, a stored template, anything) must
// dispatch that string — otherwise row data would silently steer its own
// decoding, which is the same class of injection validateProposedDispatch
// refuses for the row.<column> token.
func TestResolveParam_RowTemplateWins(t *testing.T) {
	t.Parallel()
	payload := directOpPlan(t, map[string]string{
		"fromRow": "row.stored",
		"nested":  "row.quoted",
		"typed":   "row.count",
	}, map[string]any{
		"stored": "json:5",
		"quoted": `json:"` + tpIdentity + `"`,
		"count":  int64(7),
	})

	require.Equal(t, "json:5", payload["fromRow"], "a row value that looks like a typed literal stays a string")
	require.IsType(t, "", payload["fromRow"])
	require.Equal(t, `json:"`+tpIdentity+`"`, payload["nested"])
	// The templated arm keeps delivering the row's own type, unchanged.
	require.Equal(t, int64(7), payload["typed"])
	require.IsType(t, int64(0), payload["typed"])
}

// TestResolveParam_MalformedTypedLiteralIsConfigError proves the fail-closed
// half: a value that CLAIMS the token but carries an undecodable suffix is a
// config error (permanent — no row can fix it), never a quiet demotion to the
// plain-string arm, which would ship an op the payload the author never wrote.
func TestResolveParam_MalformedTypedLiteralIsConfigError(t *testing.T) {
	t.Parallel()
	// Positive control: the well-formed shape of each malformed vector below
	// resolves, so the refusals are about malformedness and not the token.
	payload := directOpPlan(t, map[string]string{
		"obj": `json:{"oops":1}`,
		"arr": `json:[1,2]`,
		"str": `json:"x"`,
		"num": "json:5",
	}, map[string]any{})
	require.Equal(t, map[string]any{"oops": float64(1)}, payload["obj"])
	require.Equal(t, []any{float64(1), float64(2)}, payload["arr"])
	require.Equal(t, "x", payload["str"])
	require.Equal(t, float64(5), payload["num"])

	for _, bad := range []string{
		"json:{oops",
		"json:[1,2",
		`json:"unterminated`,
		"json:",
		"json:nope",
		"json:5 6",
		"json:{}{}",
	} {
		perr := directOpParamError(t, bad)
		require.NotNil(t, perr, "a malformed typed literal %q must fail the dispatch, not fall through to a plain string", bad)
		require.Equal(t, errConfig, perr.kind,
			"a malformed typed literal is an authoring defect no row can fix (%q): %v", bad, perr.msg)
		require.Contains(t, perr.msg, "limit", "the refusal must name the offending param")
	}
}

// TestResolveParam_EmptyValuedTypedLiteralRefused pins the two spellings of
// "declared, but carrying nothing": json:null and json:"". Both are
// indistinguishable at the receiving op from a param that was never declared,
// which is exactly the ambiguity the plain arm already refuses by rejecting an
// unwritten value and the templated arm by rejecting a null column. json:false
// and json:0 are NOT empty and stay accepted (pinned above) — this refuses
// absence, not falsiness.
func TestResolveParam_EmptyValuedTypedLiteralRefused(t *testing.T) {
	t.Parallel()
	for _, spelling := range []string{"json:null", "json: null"} {
		perr := directOpParamError(t, spelling)
		require.NotNil(t, perr, "%q must be refused", spelling)
		require.Equal(t, errConfig, perr.kind)
		require.Contains(t, perr.msg, "null")
		require.Contains(t, perr.msg, "omit the param instead", "the refusal must say what to do instead")
	}

	// The empty JSON string reaches the same place by a different route: it
	// decodes cleanly and is not null, so only its own check catches it.
	perr := directOpParamError(t, `json:""`)
	require.NotNil(t, perr, `json:"" must be refused`)
	require.Equal(t, errConfig, perr.kind)
	require.Contains(t, perr.msg, "empty string")
	require.Contains(t, perr.msg, "omit the param instead")
}

// TestResolveParam_LossyIntegerRefused: a JSON number is a float64, so an
// integer past 2^53 silently becomes a DIFFERENT integer. The row.<column> arm
// carries a lens column's exact int64, so accepting a rounded literal would
// make the typed literal the one arm of the grammar that changes the author's
// value — and it would do it invisibly, on exactly the values (ids, epochs,
// nanosecond timestamps) where being off by one matters most.
func TestResolveParam_LossyIntegerRefused(t *testing.T) {
	t.Parallel()

	// Positive control at the boundary: 2^53 itself is exactly representable
	// and must still resolve, so the refusal is about precision loss and not
	// about "large".
	payload := directOpPlan(t, map[string]string{
		"boundary": "json:9007199254740992",
		"float":    "json:1e3",
		"decimal":  "json:1.25",
		"small":    "json:42",
	}, map[string]any{})
	require.Equal(t, float64(9007199254740992), payload["boundary"])
	require.Equal(t, float64(1000), payload["float"], "exponent notation is float notation and is left alone")
	require.Equal(t, 1.25, payload["decimal"])
	require.Equal(t, float64(42), payload["small"])

	for _, bad := range []string{
		"json:9007199254740993",
		"json:-9007199254740993",
		"json:1700000000000000123",
		"json:12345678901234567890",
		"json:[1,9007199254740993]",
		`json:{"a":9007199254740993}`,
	} {
		perr := directOpParamError(t, bad)
		require.NotNil(t, perr, "%q must be refused: float64 cannot hold it exactly", bad)
		require.Equal(t, errConfig, perr.kind)
		require.Contains(t, perr.msg, "cannot hold exactly")
	}

	// The refusal shows the author both numbers, so the loss is legible.
	perr := directOpParamError(t, "json:9007199254740993")
	require.Contains(t, perr.msg, "9007199254740993")
	require.Contains(t, perr.msg, "9007199254740992")
}

// TestResolveStringParam_TypedLiteralRefusedOutright is the security boundary
// of the grammar. resolveStringParam serves every value that must BE a string
// — keys, operationTypes, pattern refs — and it refuses the typed-literal
// token rather than decoding it, because the gates upstream of dispatch
// compare those fields as RAW authored strings: pkgmgr's authored-dispatch
// scope guard tests protected.ops[ga.Operation] and
// protected.patterns[ga.Pattern] by equality, and Augur's proposal check
// compares raw param values against the escalated candidate. A field that
// decoded at dispatch would let an authored target name a protected operation
// (json:"AssignRole") in a spelling no gate recognises, and then dispatch it
// under the Weaver's own service actor.
//
// It is errConfig, not errData: the defect is in the playbook and no row can
// fix it, so it must not mint a per-entity Health issue per violating row.
func TestResolveStringParam_TypedLiteralRefusedOutright(t *testing.T) {
	t.Parallel()

	cases := []struct {
		field string
		ga    GapAction
	}{
		{"target", GapAction{Action: actionDirectOp, Operation: "Fix", Target: `json:"` + tpUnitKey + `"`}},
		{"operation", GapAction{Action: actionAssignTask, Operation: `json:"AssignRole"`, Assignee: tpIdentity, Target: tpUnitKey}},
		{"assignee", GapAction{Action: actionAssignTask, Operation: "ApproveX", Assignee: `json:"` + tpIdentity + `"`, Target: tpUnitKey}},
		{"target", GapAction{Action: actionAssignTask, Operation: "ApproveX", Assignee: tpIdentity, Target: `json:"` + tpUnitKey + `"`}},
		{"pattern", GapAction{Action: actionTriggerLoom, Pattern: `json:"onboarding"`, Subject: tpUnitKey}},
		{"subject", GapAction{Action: actionTriggerLoom, Pattern: "onboarding", Subject: `json:"` + tpUnitKey + `"`}},
		{"reads[0]", GapAction{Action: actionDirectOp, Operation: "Fix", Reads: []string{`json:"` + tpUnitKey + `"`}}},
		{"optionalReads[0]", GapAction{Action: actionDirectOp, Operation: "Fix", OptionalReads: []string{`json:"` + tpUnitKey + `"`}}},
		{"enumerations[0].hub", GapAction{Action: actionDirectOp, Operation: "Fix",
			Enumerations: []GapEnumeration{{Hub: `json:"` + tpUnitKey + `"`, Relation: "holdsRole", Direction: "out"}}}},
	}
	for _, tc := range cases {
		_, perr := buildPlan(nil, "typedParams", tpEntityID, "missing_a", tc.ga, map[string]any{}, 7)
		require.NotNil(t, perr, "%s must refuse the typed-literal token", tc.field)
		require.Equal(t, errConfig, perr.kind,
			"%s: the refusal must be a config error, not a per-row data error that latches a Health issue per entity: %v", tc.field, perr.msg)
		require.Contains(t, perr.msg, tc.field, "the refusal must name the offending field")
		require.Contains(t, perr.msg, "params bag", "the refusal must say where the token IS meaningful")
	}

	// A non-string typed literal is refused by the same one check, before any
	// decode — so no field can ever coerce a number into a key.
	_, perr := buildPlan(nil, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Target: "json:5"}, map[string]any{}, 7)
	require.NotNil(t, perr)
	require.Equal(t, errConfig, perr.kind)

	// Positive controls: the two arms a string field DOES have still work.
	pl, perr := buildPlan(nil, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Target: tpUnitKey}, map[string]any{}, 7)
	require.Nil(t, perr, "a plain literal key must still resolve")
	require.Equal(t, tpUnitKey, pl.authTarget)

	pl, perr = buildPlan(nil, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Target: "row.entityKey"},
		map[string]any{"entityKey": tpUnitKey}, 7)
	require.Nil(t, perr, "a row template must still resolve")
	require.Equal(t, tpUnitKey, pl.authTarget)
}

// TestResolveParam_OmittedParamsStillDispatch is the omission vector the
// weaver dossier requires for every optional input: a gap declaring NO params
// at all resolves to a payload carrying only the engine's own auto-injected
// expectedRevision. Without it, every vector above would only prove things
// about gaps that DO declare params.
func TestResolveParam_OmittedParamsStillDispatch(t *testing.T) {
	t.Parallel()
	pl, perr := buildPlan(nil, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix"}, map[string]any{}, 7)
	require.Nil(t, perr, "a gap declaring no params must still dispatch")
	payload := pl.payload("")
	require.Equal(t, map[string]any{"expectedRevision": uint64(7)}, payload)
}

// TestBuildPlan_DirectOp_OptionalReadsDropOnAbsentColumn pins the field's
// defining behaviour: an OptionalReads entry whose row.<column> template is
// null or absent for THIS row is dropped from the dispatch, and the gap still
// fires. Declaring an absence-tolerant read the natural way — a nullable lens
// column, null exactly when there is nothing to read — must not starve the
// gap on precisely the rows the declaration was written for. The dropped key
// degrades to the live undeclared read the script did before.
//
// The other half is that this leniency is scoped to DATA: a config error in
// the same list still fails the gap, because no row can fix it.
func TestBuildPlan_DirectOp_OptionalReadsDropOnAbsentColumn(t *testing.T) {
	t.Parallel()

	// Positive control: both entries resolvable, both carried.
	pl, perr := buildPlan(nil, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix",
			OptionalReads: []string{"row.entityKey", "row.priorClaimKey"}},
		map[string]any{"entityKey": tpUnitKey, "priorClaimKey": tpIdentity}, 7)
	require.Nil(t, perr)
	require.Equal(t, []string{tpUnitKey, tpIdentity}, pl.optionalReads(""))

	// The null column drops, the surviving entry is still declared, and the
	// gap dispatches.
	pl, perr = buildPlan(nil, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix",
			OptionalReads: []string{"row.entityKey", "row.priorClaimKey"}},
		map[string]any{"entityKey": tpUnitKey, "priorClaimKey": nil}, 7)
	require.Nil(t, perr, "a null optional-read column must not fail the gap")
	require.Equal(t, []string{tpUnitKey}, pl.optionalReads(""), "only the resolvable entry is declared")

	// A column missing from the row entirely behaves the same way.
	pl, perr = buildPlan(nil, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", OptionalReads: []string{"row.priorClaimKey"}},
		map[string]any{"entityKey": tpUnitKey}, 7)
	require.Nil(t, perr, "an absent optional-read column must not fail the gap")
	require.Nil(t, pl.optionalReads, "with every entry dropped the plan declares none at all")

	// Contrast, and the boundary of the leniency: a REQUIRED read on the same
	// absent column still fails the gap. The two lists are not the same rule.
	_, perr = buildPlan(nil, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Reads: []string{"row.priorClaimKey"}},
		map[string]any{"entityKey": tpUnitKey}, 7)
	require.NotNil(t, perr, "a required read on an absent column must still fail the gap")

	// Contrast: a config error in the optionalReads list is NOT dropped.
	_, perr = buildPlan(nil, "typedParams", tpEntityID, "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", OptionalReads: []string{`json:"` + tpUnitKey + `"`}},
		map[string]any{}, 7)
	require.NotNil(t, perr, "a config error in optionalReads must fail the gap, not silently drop the entry")
	require.Equal(t, errConfig, perr.kind)
}

// TestGapCandidate_CarriesOptionalReads proves the candidate surface carries
// the declaration: a candidate body's optionalReads decodes onto GapCandidate
// (the json tag is the only thing joining the installed body to the struct)
// and survives candidateGapAction's materialization into the GapAction shape
// buildPlan consumes. Without the field a planner-selected candidate would
// silently dispatch without a declaration its explicit-gap sibling can make —
// the same asymmetry Reads and Enumerations are on the candidate shape to
// avoid.
func TestGapCandidate_CarriesOptionalReads(t *testing.T) {
	t.Parallel()

	var cand GapCandidate
	require.NoError(t, json.Unmarshal([]byte(`{"action":"directOp","operation":"Fix","optionalReads":["row.priorClaimKey"]}`), &cand))
	require.Equal(t, []string{"row.priorClaimKey"}, cand.OptionalReads,
		"a candidate body's optionalReads must decode onto GapCandidate")

	require.Equal(t, []string{"row.priorClaimKey"}, candidateGapAction(cand).OptionalReads,
		"candidateGapAction must carry it into the GapAction buildPlan consumes")

	pl, perr := buildPlan(nil, "typedParams", tpEntityID, "missing_a",
		candidateGapAction(cand), map[string]any{"priorClaimKey": tpIdentity}, 7)
	require.Nil(t, perr)
	require.Equal(t, []string{tpIdentity}, pl.optionalReads(""),
		"a selected candidate's declared optional read must reach the dispatched plan")

	// Omission vector: a candidate declaring none leaves the plan field nil.
	pl, perr = buildPlan(nil, "typedParams", tpEntityID, "missing_a",
		candidateGapAction(GapCandidate{Action: actionDirectOp, Operation: "Fix"}), map[string]any{}, 7)
	require.Nil(t, perr)
	require.Nil(t, pl.optionalReads)

	// Carrying the field brings its scope rule with it: on any action but
	// directOp the engine's own dispatch owns ContextHint.OptionalReads, so a
	// candidate declaring one there is refused at load rather than left to a
	// silent, order-dependent collision — the same refusal the gap and catalog
	// surfaces carry.
	scopeBad := &Target{
		TargetID: "fixtureCandOptScope",
		Gaps: map[string]GapAction{
			"missing_a": {Candidates: []GapCandidate{{
				Action: actionAssignTask, Operation: "ApproveX", Assignee: tpIdentity, Target: tpUnitKey,
				OptionalReads: []string{"row.entityKey"},
			}}},
		},
	}
	err := validateTarget(scopeBad)
	require.Error(t, err, "a non-directOp candidate declaring optionalReads must be refused at load")
	require.ErrorContains(t, err, "candidates[0]")
	require.ErrorContains(t, err, "optionalReads")

	// Positive control: the same candidate as a directOp loads cleanly.
	require.NoError(t, validateTarget(&Target{
		TargetID: "fixtureCandOptScopeOK",
		Gaps: map[string]GapAction{
			"missing_a": {Candidates: []GapCandidate{{
				Action: actionDirectOp, Operation: "Sweep", OptionalReads: []string{"row.entityKey"},
			}}},
		},
	}), "a directOp candidate declaring optionalReads must load")
}

// TestValidateTarget_RejectsMalformedTypedLiteralParam is the ENGINE-LOAD half
// of the dual enforcement. A malformed typed literal can never dispatch for
// any row, so the engine refuses the target at load rather than raising the
// identical config error per violation row forever. It reaches all three
// surfaces that carry a params bag through buildPlan: the gap's own action, a
// goal catalog entry, and a planner candidate.
func TestValidateTarget_RejectsMalformedTypedLiteralParam(t *testing.T) {
	t.Parallel()

	// Positive control: a well-formed typed literal loads cleanly.
	ok := &Target{
		TargetID: "fixtureTypedOK",
		Gaps: map[string]GapAction{
			"missing_a": {Action: actionDirectOp, Operation: "FixA", Params: map[string]string{"limit": "json:5"}},
		},
	}
	require.NoError(t, validateTarget(ok), "a well-formed typed literal must load")

	// Omission control: a gap declaring no params at all loads cleanly.
	none := &Target{
		TargetID: "fixtureTypedNone",
		Gaps:     map[string]GapAction{"missing_a": {Action: actionDirectOp, Operation: "FixA"}},
	}
	require.NoError(t, validateTarget(none), "a gap declaring no params must load")

	// Plain-literal control: an unrecognised prefix is a plain string, never a
	// load-time refusal — the classification is a whitelist.
	plain := &Target{
		TargetID: "fixtureTypedPlain",
		Gaps: map[string]GapAction{
			"missing_a": {Action: actionDirectOp, Operation: "FixA",
				Params: map[string]string{"status": "leased", "embedded": "see json:5", "other": "yaml:{oops"}},
		},
	}
	require.NoError(t, validateTarget(plain), "a plain literal must load whatever it contains")

	// Negative: the gap's own params.
	bad := &Target{
		TargetID: "fixtureTypedGap",
		Gaps: map[string]GapAction{
			"missing_a": {Action: actionDirectOp, Operation: "FixA", Params: map[string]string{"limit": "json:{oops"}},
		},
	}
	err := validateTarget(bad)
	require.Error(t, err, "a malformed typed literal must be refused at load")
	require.ErrorContains(t, err, "limit")
	require.ErrorContains(t, err, "not valid JSON")
	require.ErrorContains(t, err, `gaps key "missing_a"`)

	// Negative: json:null and the lossy integer are refused at load too, on
	// the same surface — the load gate runs the dispatch rule, not a subset.
	for _, spelling := range []string{"json:null", `json:""`, "json:9007199254740993"} {
		nullBad := &Target{
			TargetID: "fixtureTypedEmpty",
			Gaps: map[string]GapAction{
				"missing_a": {Action: actionDirectOp, Operation: "FixA", Params: map[string]string{"limit": spelling}},
			},
		}
		require.Error(t, validateTarget(nullBad), "%s must be refused at load", spelling)
	}

	// Negative: a goal catalog entry's params reach the same refusal — an
	// entry dispatches through the same buildPlan an explicit gap does.
	catalogBad := &Target{
		TargetID: "fixtureTypedCatalog",
		Gaps: map[string]GapAction{
			"missing_a": {
				Goal: json.RawMessage(`{"present":"subject.data.done"}`),
				Actions: []ActionCatalogEntry{{
					Ref: "sweep", Action: actionDirectOp, Operation: "Sweep",
					Params:  map[string]string{"limit": "json:{oops"},
					Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.done"}`)},
				}},
			},
		},
	}
	catalogErr := validateTarget(catalogBad)
	require.Error(t, catalogErr, "a catalog entry's malformed typed literal must be refused")
	require.ErrorContains(t, catalogErr, "actions[0]")
	require.ErrorContains(t, catalogErr, "not valid JSON")

	// Positive control on the catalog surface.
	catalogOK := &Target{
		TargetID: "fixtureTypedCatalogOK",
		Gaps: map[string]GapAction{
			"missing_a": {
				Goal: json.RawMessage(`{"present":"subject.data.done"}`),
				Actions: []ActionCatalogEntry{{
					Ref: "sweep", Action: actionDirectOp, Operation: "Sweep",
					Params:  map[string]string{"limit": "json:5"},
					Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.done"}`)},
				}},
			},
		},
	}
	require.NoError(t, validateTarget(catalogOK), "a well-formed catalog typed literal must load")

	// Negative: a planner candidate's params. GapCandidate carries its own
	// Params bag and a picked candidate dispatches through candidateGapAction
	// into the same buildPlan, so its params are validated on the same terms.
	candidateBad := &Target{
		TargetID: "fixtureTypedCand",
		Gaps: map[string]GapAction{
			"missing_a": {
				Candidates: []GapCandidate{{
					Action: actionDirectOp, Operation: "Sweep",
					Params: map[string]string{"limit": "json:{oops"},
				}},
			},
		},
	}
	candErr := validateTarget(candidateBad)
	require.Error(t, candErr, "a candidate's malformed typed literal must be refused")
	require.ErrorContains(t, candErr, "candidates[0]")
	require.ErrorContains(t, candErr, "not valid JSON")

	// Positive control on the candidate surface.
	candidateOK := &Target{
		TargetID: "fixtureTypedCandOK",
		Gaps: map[string]GapAction{
			"missing_a": {
				Candidates: []GapCandidate{{
					Action: actionDirectOp, Operation: "Sweep",
					Params: map[string]string{"limit": "json:5"},
				}},
			},
		},
	}
	require.NoError(t, validateTarget(candidateOK), "a well-formed candidate typed literal must load")
}

// TestValidateTarget_RejectsTypedLiteralInStringField is the load-time half of
// the security boundary resolveStringParam enforces at dispatch: no
// string-typed field of any dispatch surface may carry the token. Refusing at
// load turns a permanently-undispatchable authored value into one verdict for
// the whole target, and — for an AI-authored target — refuses the shape whose
// whole point was to be spelled differently from what the scope gate reads.
func TestValidateTarget_RejectsTypedLiteralInStringField(t *testing.T) {
	t.Parallel()

	gapTarget := func(ga GapAction) *Target {
		return &Target{TargetID: "fixtureTypedStr", Gaps: map[string]GapAction{"missing_a": ga}}
	}

	cases := []struct {
		field string
		ga    GapAction
	}{
		{"operation", GapAction{Action: actionDirectOp, Operation: `json:"AssignRole"`}},
		{"pattern", GapAction{Action: actionTriggerLoom, Pattern: `json:"onboarding"`, Subject: tpUnitKey}},
		{"subject", GapAction{Action: actionTriggerLoom, Pattern: "onboarding", Subject: `json:"` + tpUnitKey + `"`}},
		{"assignee", GapAction{Action: actionAssignTask, Operation: "ApproveX", Assignee: `json:"` + tpIdentity + `"`, Target: tpUnitKey}},
		{"target", GapAction{Action: actionDirectOp, Operation: "Fix", Target: `json:"` + tpUnitKey + `"`}},
		{"reads[0]", GapAction{Action: actionDirectOp, Operation: "Fix", Reads: []string{`json:"` + tpUnitKey + `"`}}},
		{"optionalReads[0]", GapAction{Action: actionDirectOp, Operation: "Fix", OptionalReads: []string{`json:"` + tpUnitKey + `"`}}},
		{"enumerations[0].hub", GapAction{Action: actionDirectOp, Operation: "Fix",
			Enumerations: []GapEnumeration{{Hub: `json:"` + tpUnitKey + `"`, Relation: "holdsRole", Direction: "out"}}}},
	}
	for _, tc := range cases {
		err := validateTarget(gapTarget(tc.ga))
		require.Error(t, err, "%s carrying the typed-literal token must be refused at load", tc.field)
		require.ErrorContains(t, err, tc.field)
		require.ErrorContains(t, err, "params bag")
	}

	// Positive control: the same fields with plain literals and row templates
	// load cleanly, so the refusal is about the token alone.
	require.NoError(t, validateTarget(gapTarget(GapAction{
		Action: actionDirectOp, Operation: "Fix", Target: "row.entityKey",
		Reads:         []string{tpUnitKey, "row.entityKey"},
		OptionalReads: []string{"row.priorClaimKey"},
		Enumerations:  []GapEnumeration{{Hub: "row.entityKey", Relation: "holdsRole", Direction: "out"}},
		Params:        map[string]string{"limit": "json:5"},
	})), "plain and templated string fields must load, and the params bag keeps its token")

	// The catalog surface carries the same fields and the same refusal.
	catalogBad := &Target{
		TargetID: "fixtureTypedStrCat",
		Gaps: map[string]GapAction{
			"missing_a": {
				Goal: json.RawMessage(`{"present":"subject.data.done"}`),
				Actions: []ActionCatalogEntry{{
					Ref: "sweep", Action: actionDirectOp, Operation: "Sweep",
					Reads:   []string{`json:"` + tpUnitKey + `"`},
					Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.done"}`)},
				}},
			},
		},
	}
	err := validateTarget(catalogBad)
	require.Error(t, err, "a catalog entry's string field must be refused too")
	require.ErrorContains(t, err, "actions[0]")
	require.ErrorContains(t, err, "reads[0]")

	// And the candidate surface.
	candidateBad := &Target{
		TargetID: "fixtureTypedStrCand",
		Gaps: map[string]GapAction{
			"missing_a": {
				Candidates: []GapCandidate{{Action: actionDirectOp, Operation: `json:"AssignRole"`}},
			},
		},
	}
	candErr := validateTarget(candidateBad)
	require.Error(t, candErr, "a candidate's string field must be refused too")
	require.ErrorContains(t, candErr, "candidates[0]")
	require.ErrorContains(t, candErr, "operation")
}

// typedLiteralCase is one entry of the shared corpus both implementations of
// the json:<literal> rule are checked against.
type typedLiteralCase struct {
	Value  string `json:"value"`
	Accept bool   `json:"accept"`
	Why    string `json:"why"`
}

// loadTypedLiteralCorpus reads the shared corpus. Path is relative to the
// package under test: internal/pkgmgr reads the SAME file at ../weaver/....
func loadTypedLiteralCorpus(t *testing.T, path string) []typedLiteralCase {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the shared typed-literal corpus must be readable at %s", path)
	var doc struct {
		Cases []typedLiteralCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.NotEmpty(t, doc.Cases, "an empty corpus would make this gate pass everything")
	return doc.Cases
}

// TestTypedLiteralDecode_MatchesSharedCorpus checks the ENGINE's decoder
// against the shared corpus. internal/pkgmgr's installer re-implements the
// same rule (it must not import an engine) and runs this same file, so the two
// can only agree or red: a rule changed in one implementation and not the
// other means a package that installs clean and then kills the whole weaver
// target at load, or the reverse — an author refused at install for a value
// the engine would have dispatched happily.
func TestTypedLiteralDecode_MatchesSharedCorpus(t *testing.T) {
	t.Parallel()
	for _, tc := range loadTypedLiteralCorpus(t, "testdata/typedliteral_corpus.json") {
		perr := typedLiteralError("limit", tc.Value)
		if tc.Accept {
			require.Nil(t, perr, "corpus says %q is accepted (%s), engine refused: %v", tc.Value, tc.Why, perr)
			continue
		}
		require.NotNil(t, perr, "corpus says %q is refused (%s), engine accepted it", tc.Value, tc.Why)
		require.Equal(t, errConfig, perr.kind, "%q: every typed-literal refusal is a config error", tc.Value)
	}
}

// TestValidateProposedDispatch_TypedLiteralInjectionRejected is the sibling of
// TestValidateProposedDispatch_RowTemplateInjectionRejected for the second
// reserved token. A model-proposed literal is scope-checked RAW, and
// `json:"vtx.identity.X"` is not vtx-shaped raw (isVtxKey splits on dots and
// sees a first segment of `json:"vtx`), so containment would pass a value that
// means something else once resolved. The proposal vocabulary refuses the
// whole token at one place, uniformly across the proposed actions.
func TestValidateProposedDispatch_TypedLiteralInjectionRejected(t *testing.T) {
	t.Parallel()

	// Positive control: the same proposal without the token is accepted, so
	// the refusals below are about the token and not the shape of the fixture.
	require.Empty(t, validateProposedDispatch("assignTask", map[string]any{
		"operation": "ApproveLeaseApplication",
		"assignee":  dpCandidate,
		"target":    dpCandidate,
	}, dpCandidate), "a plain proposal must still be accepted")

	// A foreign vertex key smuggled through the token in a non-anchor field.
	reason := validateProposedDispatch("assignTask", map[string]any{
		"operation": "ApproveLeaseApplication",
		"assignee":  `json:"vtx.identity.AAidHJKMNPQRSTUVWXYZ"`,
		"target":    dpCandidate,
	}, dpCandidate)
	require.NotEmpty(t, reason, "a param value using the reserved typed-literal prefix must be rejected")
	require.Contains(t, reason, "typed-literal", "the refusal must name the token, not some other check")

	// A second non-anchor field, to prove the scan is over every proposed
	// string and not one privileged name. `operation` is never vtx-shaped, so
	// only the token check can catch it.
	opReason := validateProposedDispatch("assignTask", map[string]any{
		"operation": `json:"ApproveLeaseApplication"`,
		"assignee":  dpCandidate,
		"target":    dpCandidate,
	}, dpCandidate)
	require.NotEmpty(t, opReason, "the token is refused on every proposed string, not just key-shaped ones")
	require.Contains(t, opReason, "typed-literal")

	// The ANCHOR field is a different check and answers first: anchor equality
	// (byte-for-byte against the escalated candidate) runs before the params
	// are scanned at all, so a token there is refused as a mismatched anchor.
	// Asserted explicitly so this vector is not mistaken for a proof of the
	// token scan — it would pass with the token check deleted.
	anchorReason := validateProposedDispatch("directOp", map[string]any{
		"operation": "SomeOp",
		"target":    `json:"` + dpCandidate + `"`,
	}, dpCandidate)
	require.NotEmpty(t, anchorReason, "a typed-literal anchor must be rejected, not decoded and compared")
	require.Contains(t, anchorReason, "does not equal the escalated candidate",
		"the anchor check answers first; the token scan never sees this value")
}
