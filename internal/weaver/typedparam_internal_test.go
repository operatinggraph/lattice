package weaver

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// directOpPlan builds a directOp plan for one params bag against one row,
// returning the resolved payload. The op reads nothing and targets nothing so
// the assertions below are about the param grammar alone.
func directOpPlan(t *testing.T, params map[string]string, row map[string]any) map[string]any {
	t.Helper()
	pl, perr := buildPlan(nil, "typedParams", "AAentHJKMNPQRSTUVWX", "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Params: params}, row, 7)
	require.Nil(t, perr, "buildPlan must resolve the params bag")
	return pl.payload("")
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
		"empty":    `json:""`,
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
	// An explicitly declared empty string is a value; only the plain arm's
	// unwritten value ("") is "required, and missing".
	require.Equal(t, "", payload["empty"])
	require.IsType(t, "", payload["empty"])

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
		"quoted": `json:"vtx.identity.AAidHJKMNPQRSTUVWX"`,
		"count":  int64(7),
	})

	require.Equal(t, "json:5", payload["fromRow"], "a row value that looks like a typed literal stays a string")
	require.IsType(t, "", payload["fromRow"])
	require.Equal(t, `json:"vtx.identity.AAidHJKMNPQRSTUVWX"`, payload["nested"])
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
		_, perr := buildPlan(nil, "typedParams", "AAentHJKMNPQRSTUVWX", "missing_a",
			GapAction{Action: actionDirectOp, Operation: "Fix", Params: map[string]string{"limit": bad}}, map[string]any{}, 7)
		require.NotNil(t, perr, "a malformed typed literal %q must fail the dispatch, not fall through to a plain string", bad)
		require.Equal(t, errConfig, perr.kind,
			"a malformed typed literal is an authoring defect no row can fix (%q): %v", bad, perr.msg)
		require.Contains(t, perr.msg, "limit", "the refusal must name the offending param")
	}
}

// TestResolveParam_TypedNullRefused pins the null decision: json:null is
// REFUSED as a config error. A null param is indistinguishable from an omitted
// one at the receiving op, so accepting it would give two spellings for one
// dispatch; the templated arm already treats a null row value as an error, and
// the plain arm already refuses the empty authored value. json:false and
// json:0 are NOT null and stay accepted (pinned above) — this refuses the null
// literal, not falsiness.
func TestResolveParam_TypedNullRefused(t *testing.T) {
	t.Parallel()
	for _, spelling := range []string{"json:null", "json: null"} {
		_, perr := buildPlan(nil, "typedParams", "AAentHJKMNPQRSTUVWX", "missing_a",
			GapAction{Action: actionDirectOp, Operation: "Fix", Params: map[string]string{"limit": spelling}}, map[string]any{}, 7)
		require.NotNil(t, perr, "%q must be refused", spelling)
		require.Equal(t, errConfig, perr.kind)
		require.Contains(t, perr.msg, "null")
		require.Contains(t, perr.msg, "omit the param instead", "the refusal must say what to do instead")
	}
}

// TestResolveStringParam_NonStringTypedLiteralRefused covers the interaction
// with the fields that are not free-form payload: Target, Operation (on the
// arms that template it) and Assignee resolve through resolveStringParam,
// which type-asserts. A typed literal that decodes to a number or a bool there
// must fail LOUDLY with the type it got — never be coerced back into its
// spelling, which would make the token silently meaningless on exactly the
// fields whose values become keys.
func TestResolveStringParam_NonStringTypedLiteralRefused(t *testing.T) {
	t.Parallel()

	// directOp target.
	_, perr := buildPlan(nil, "typedParams", "AAentHJKMNPQRSTUVWX", "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Target: "json:5"}, map[string]any{}, 7)
	require.NotNil(t, perr, "a numeric typed literal in target must be refused")
	require.Contains(t, perr.msg, "must resolve to a non-empty string")
	require.Contains(t, perr.msg, "float64", "the refusal must name the type it actually got")
	require.Contains(t, perr.msg, "target")

	// assignTask operation, which unlike directOp's literal operationType does
	// resolve through resolveStringParam.
	_, perr = buildPlan(nil, "typedParams", "AAentHJKMNPQRSTUVWX", "missing_a",
		GapAction{Action: actionAssignTask, Operation: "json:true", Assignee: "vtx.identity.AAidHJKMNPQRSTUVWX", Target: "vtx.unit.AAunitHJKMNPQRSTUV"},
		map[string]any{}, 7)
	require.NotNil(t, perr, "a bool typed literal in operation must be refused")
	require.Contains(t, perr.msg, "must resolve to a non-empty string")
	require.Contains(t, perr.msg, "bool")

	// assignTask assignee, the third string-only field.
	_, perr = buildPlan(nil, "typedParams", "AAentHJKMNPQRSTUVWX", "missing_a",
		GapAction{Action: actionAssignTask, Operation: "ApproveX", Assignee: `json:["a"]`, Target: "vtx.unit.AAunitHJKMNPQRSTUV"},
		map[string]any{}, 7)
	require.NotNil(t, perr, "an array typed literal in assignee must be refused")
	require.Contains(t, perr.msg, "must resolve to a non-empty string")

	// Positive control: the escape hatch still works on a string-only field —
	// a JSON string literal resolves to its string and dispatches normally.
	pl, perr := buildPlan(nil, "typedParams", "AAentHJKMNPQRSTUVWX", "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix", Target: `json:"vtx.unit.AAunitHJKMNPQRSTUV"`}, map[string]any{}, 7)
	require.Nil(t, perr)
	require.Equal(t, "vtx.unit.AAunitHJKMNPQRSTUV", pl.authTarget)
}

// TestResolveParam_OmittedParamsStillDispatch is the omission vector the
// weaver dossier requires for every optional input: a gap declaring NO params
// at all resolves to a payload carrying only the engine's own auto-injected
// expectedRevision. Without it, every vector above would only prove things
// about gaps that DO declare params.
func TestResolveParam_OmittedParamsStillDispatch(t *testing.T) {
	t.Parallel()
	pl, perr := buildPlan(nil, "typedParams", "AAentHJKMNPQRSTUVWX", "missing_a",
		GapAction{Action: actionDirectOp, Operation: "Fix"}, map[string]any{}, 7)
	require.Nil(t, perr, "a gap declaring no params must still dispatch")
	payload := pl.payload("")
	require.Equal(t, map[string]any{"expectedRevision": uint64(7)}, payload)
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

	// Negative: json:null is refused at load too, on the same surface.
	nullBad := &Target{
		TargetID: "fixtureTypedNull",
		Gaps: map[string]GapAction{
			"missing_a": {Action: actionDirectOp, Operation: "FixA", Params: map[string]string{"limit": "json:null"}},
		},
	}
	require.ErrorContains(t, validateTarget(nullBad), "null")

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

// TestValidateProposedDispatch_TypedLiteralInjectionRejected is the sibling of
// TestValidateProposedDispatch_RowTemplateInjectionRejected for the second
// reserved token, and closes the scope-escape the token would otherwise open:
// the dispatch-time scope check inspects the RAW proposed strings, and
// `json:"vtx.identity.X"` is not vtx-shaped raw (isVtxKey splits on dots and
// sees a first segment of `json:"vtx`), so it would pass containment and THEN
// be decoded by resolveStringParam into a foreign vertex key. A model-proposed
// literal carrying the token is refused outright, so validation and dispatch
// can never see two different strings.
func TestValidateProposedDispatch_TypedLiteralInjectionRejected(t *testing.T) {
	t.Parallel()

	// Positive control: the same proposal without the token is accepted, so
	// the refusal below is about the token and not the shape of the fixture.
	require.Empty(t, validateProposedDispatch("assignTask", map[string]any{
		"operation": "ApproveLeaseApplication",
		"assignee":  dpCandidate,
		"target":    dpCandidate,
	}, dpCandidate), "a plain proposal must still be accepted")

	// Negative: a foreign vertex key smuggled through the typed-literal token.
	// Raw, the value is not vtx-shaped, so the existing containment check
	// passes it; decoded at dispatch it is a key outside the escalated scope.
	reason := validateProposedDispatch("assignTask", map[string]any{
		"operation": "ApproveLeaseApplication",
		"assignee":  `json:"vtx.identity.AAidHJKMNPQRSTUVWX"`,
		"target":    dpCandidate,
	}, dpCandidate)
	require.NotEmpty(t, reason, "a param value using the reserved typed-literal prefix must be rejected")
	require.Contains(t, reason, "typed-literal")

	// The same refusal on the anchor field itself, where the decoded value
	// would otherwise differ from the byte-compared one.
	require.NotEmpty(t, validateProposedDispatch("directOp", map[string]any{
		"operation": "SomeOp",
		"target":    `json:"` + dpCandidate + `"`,
	}, dpCandidate), "a typed-literal anchor must be rejected, not decoded and compared")
}
