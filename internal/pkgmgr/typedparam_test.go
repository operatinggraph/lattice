package pkgmgr

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/weaver"
)

// TestValidateWeaverTargets_MalformedTypedLiteralParamRejected is the INSTALL
// half of the dual enforcement on the json:<literal> param token, mirroring
// the reservedGapParam posture this installer already documents: "the engine's
// validateTarget rejects it at load; install rejects it first for a clearer
// author error." Whether a typed literal decodes depends on the authored value
// alone, so a malformed one could never dispatch for any row — without the
// install refusal the package installs clean and then fails validateTarget on
// a running stack, killing the WHOLE weaver target rather than the one gap,
// and doing it long after the install the author was watching.
func TestValidateWeaverTargets_MalformedTypedLiteralParamRejected(t *testing.T) {
	t.Parallel()

	gap := func(params map[string]string) Definition {
		return Definition{WeaverTargets: []WeaverTargetSpec{{
			TargetID: "leaseSigning",
			Gaps: map[string]GapActionSpec{
				"missing_signature": {Action: "directOp", Operation: "SignLease", Params: params},
			},
		}}}
	}

	// Positive control: every accepted literal type installs clean.
	require.NoError(t, gap(map[string]string{
		"limit":   "json:5",
		"enabled": "json:true",
		"label":   `json:"leased"`,
		"tags":    `json:["a",2]`,
		"opts":    `json:{"k":1}`,
		"escaped": `json:"json:foo"`,
	}).validateWeaverTargets(), "well-formed typed literals must install clean")

	// Omission vector: a gap declaring no params at all installs clean.
	require.NoError(t, gap(nil).validateWeaverTargets(), "a gap declaring no params must install clean")

	// Plain-literal control: the classification is a whitelist on the leading
	// token, so an unrecognised prefix — or the token anywhere but the front —
	// is a plain string and is never inspected as JSON.
	require.NoError(t, gap(map[string]string{
		"status":   "leased",
		"embedded": "see json:5 for the shape",
		"other":    "yaml:{oops",
		"spaced":   " json:{oops",
	}).validateWeaverTargets(), "a plain literal must install whatever it contains")

	// Negative: an undecodable suffix.
	err := gap(map[string]string{"limit": "json:{oops"}).validateWeaverTargets()
	require.Error(t, err, "a malformed typed literal must be refused at install")
	require.ErrorContains(t, err, `param "limit"`, "the refusal must name the offending param")
	require.ErrorContains(t, err, "not valid JSON")
	require.ErrorContains(t, err, `gaps key "missing_signature"`)
	require.ErrorContains(t, err, "json:\"json:foo\"", "the refusal must show the escape for a string that starts with the token")

	// Negative: the null literal, refused on the same terms — a null param is
	// indistinguishable from an absent one.
	nullErr := gap(map[string]string{"limit": "json:null"}).validateWeaverTargets()
	require.Error(t, nullErr, "json:null must be refused at install")
	require.ErrorContains(t, nullErr, "null")
	require.ErrorContains(t, nullErr, "omit the param instead")
}

// TestValidateWeaverTargets_ActionsCatalogMalformedTypedLiteralRejected is the
// catalog-entry mirror: an entry chosen by goal regression dispatches through
// the identical buildPlan an explicit gap does, so its params carry the same
// permanent defect and the same install-time refusal.
func TestValidateWeaverTargets_ActionsCatalogMalformedTypedLiteralRejected(t *testing.T) {
	t.Parallel()

	catalog := func(params map[string]string) Definition {
		return Definition{WeaverTargets: []WeaverTargetSpec{{
			TargetID: "identityErasureComplete",
			Gaps: map[string]GapActionSpec{
				"missing_residue": {
					Goal: json.RawMessage(`{"present":"subject.data.clear"}`),
					Actions: []ActionCatalogEntrySpec{{
						Ref: "sweep", Action: "directOp", Operation: "Sweep",
						Params:  params,
						Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.clear"}`)},
					}},
				},
			},
		}}}
	}

	// Positive control: a well-formed catalog typed literal installs clean.
	require.NoError(t, catalog(map[string]string{"limit": "json:5"}).validateWeaverTargets(),
		"a well-formed catalog typed literal must install clean")

	// Omission vector: a catalog entry declaring no params installs clean.
	require.NoError(t, catalog(nil).validateWeaverTargets(),
		"a catalog entry declaring no params must install clean")

	// Negative: the whole target is refused, not the offending entry silently
	// dropped.
	err := catalog(map[string]string{"limit": "json:{oops"}).validateWeaverTargets()
	require.Error(t, err, "a catalog entry's malformed typed literal must be refused at install")
	require.ErrorContains(t, err, "actions[0]")
	require.ErrorContains(t, err, `ref "sweep"`)
	require.ErrorContains(t, err, `param "limit"`)
	require.ErrorContains(t, err, "not valid JSON")

	require.ErrorContains(t, catalog(map[string]string{"limit": "json:null"}).validateWeaverTargets(), "null")
}

// TestGapActionBody_TypedLiteralParamRoundTripsVerbatim pins where the typed
// literal lives: entirely in the value grammar. The installed JSON body keeps
// its map[string]string shape and the token travels to the engine as the
// authored string, decoded only at dispatch. An installer that pre-decoded it
// would emit a body shape no installed package carries and no engine struct
// parses.
func TestGapActionBody_TypedLiteralParamRoundTripsVerbatim(t *testing.T) {
	t.Parallel()

	gapBody := gapActionBody(GapActionSpec{
		Action:    "directOp",
		Operation: "SetListingStatus",
		Params:    map[string]string{"limit": "json:5", "status": "leased"},
		Goal:      json.RawMessage(`{"present":"subject.data.clear"}`),
		Actions: []ActionCatalogEntrySpec{{
			Ref: "sweep", Action: "directOp", Operation: "Sweep",
			Params:  map[string]string{"limit": "json:true"},
			Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.clear"}`)},
		}},
	})

	raw, err := json.Marshal(map[string]any{
		"targetId": "leaseApplicationComplete",
		"lensRef":  "leaseApplicationGaps",
		"gaps":     map[string]any{"missing_listingLeased": gapBody},
	})
	require.NoError(t, err)

	var target weaver.Target
	require.NoError(t, json.Unmarshal(raw, &target))

	ga := target.Gaps["missing_listingLeased"]
	require.Equal(t, map[string]string{"limit": "json:5", "status": "leased"}, ga.Params,
		"the emitted gap body must deliver the param values verbatim as strings")
	require.Len(t, ga.Actions, 1)
	require.Equal(t, map[string]string{"limit": "json:true"}, ga.Actions[0].Params,
		"the emitted catalog entry body must deliver its param values verbatim as strings")
}

// TestValidateWeaverTargets_TypedLiteralInStringFieldRejected is the install
// half of the grammar's security boundary. The typed-literal token belongs to
// the params bag alone; every other field of a gap is a key, an operationType
// or a pattern ref, and those are exactly the fields the authored-dispatch
// scope guard (authored_dispatch_scope.go) compares against its protected sets
// by RAW string equality. A gap declaring operation `json:"AssignRole"` is not
// the string AssignRole to that guard, so it passes apply — and would decode
// to AssignRole at dispatch, under the Weaver's own service actor, once per
// violating row. Install refuses the shape outright, on every string-typed
// field of both dispatch surfaces.
func TestValidateWeaverTargets_TypedLiteralInStringFieldRejected(t *testing.T) {
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
		{"operation", GapActionSpec{Action: "directOp", Operation: `json:"AssignRole"`}},
		{"pattern", GapActionSpec{Action: "triggerLoom", Pattern: `json:"onboarding"`, Subject: "row.entityKey"}},
		{"subject", GapActionSpec{Action: "triggerLoom", Pattern: "onboarding", Subject: `json:"vtx.unit.AAunitHJKMNPQRSTUVWX"`}},
		{"assignee", GapActionSpec{Action: "assignTask", Operation: "ApproveX",
			Assignee: `json:"vtx.identity.AAidHJKMNPQRSTUVWXYZ"`, Target: "row.entityKey"}},
		{"target", GapActionSpec{Action: "directOp", Operation: "Fix", Target: `json:"vtx.unit.AAunitHJKMNPQRSTUVWX"`}},
		{"reads[0]", GapActionSpec{Action: "directOp", Operation: "Fix", Reads: []string{`json:"vtx.unit.AAunitHJKMNPQRSTUVWX"`}}},
		{"optionalReads[0]", GapActionSpec{Action: "directOp", Operation: "Fix",
			OptionalReads: []string{`json:"vtx.unit.AAunitHJKMNPQRSTUVWX"`}}},
		{"enumerations[0].hub", GapActionSpec{Action: "directOp", Operation: "Fix",
			Enumerations: []EnumerationSpec{{Hub: `json:"vtx.unit.AAunitHJKMNPQRSTUVWX"`, Relation: "holdsRole", Direction: "out"}}}},
	}
	for _, tc := range cases {
		err := gap(tc.ga).validateWeaverTargets()
		require.Error(t, err, "%s carrying the typed-literal token must be refused at install", tc.field)
		require.ErrorContains(t, err, tc.field, "the refusal must name the offending field")
		require.ErrorContains(t, err, "params bag", "the refusal must say where the token IS meaningful")
	}

	// Positive control: the same fields, plain and templated, install clean —
	// and the params bag keeps its token, so the refusal is scoped, not global.
	require.NoError(t, gap(GapActionSpec{
		Action: "directOp", Operation: "Fix", Target: "row.entityKey",
		Reads:         []string{"vtx.unit.AAunitHJKMNPQRSTUVWX", "row.entityKey"},
		OptionalReads: []string{"row.priorClaimKey"},
		Enumerations:  []EnumerationSpec{{Hub: "row.entityKey", Relation: "holdsRole", Direction: "out"}},
		Params:        map[string]string{"limit": "json:5"},
	}).validateWeaverTargets(), "plain and templated string fields must install, params keeps its token")

	// The catalog surface carries the same fields and the same refusal.
	catalogBad := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "identityErasureComplete",
		Gaps: map[string]GapActionSpec{
			"missing_residue": {
				Goal: json.RawMessage(`{"present":"subject.data.clear"}`),
				Actions: []ActionCatalogEntrySpec{{
					Ref: "sweep", Action: "directOp", Operation: "Sweep",
					Reads:   []string{`json:"vtx.unit.AAunitHJKMNPQRSTUVWX"`},
					Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.clear"}`)},
				}},
			},
		},
	}}}
	err := catalogBad.validateWeaverTargets()
	require.Error(t, err, "a catalog entry's string field must be refused too")
	require.ErrorContains(t, err, "actions[0]")
	require.ErrorContains(t, err, `ref "sweep"`)
	require.ErrorContains(t, err, "reads[0]")
}

// typedLiteralCase is one entry of the corpus shared with internal/weaver.
type typedLiteralCase struct {
	Value  string `json:"value"`
	Accept bool   `json:"accept"`
	Why    string `json:"why"`
}

// TestTypedLiteralDecode_AgreesWithWeaverEngine checks the INSTALLER's copy of
// the json:<literal> rule against the corpus internal/weaver's own decoder is
// checked against, read from that package's testdata by path — the same
// read-the-engine-by-path device TestGapCompanionPrefixes_MatchWeaverVocabulary
// uses, and for the same reason: the installer must not import an engine, so
// something else has to tie the restatement back.
//
// The vocabulary pin next door covers the CONSTANT. This covers the RULE, and
// the two failure modes it prevents are opposite and both silent: a decode
// option added on the engine side alone lets a package install clean and then
// kill the whole weaver target at load, while one added on the install side
// alone refuses an author a value the engine would have dispatched happily.
func TestTypedLiteralDecode_AgreesWithWeaverEngine(t *testing.T) {
	t.Parallel()
	const corpusPath = "../weaver/testdata/typedliteral_corpus.json"
	raw, err := os.ReadFile(corpusPath)
	require.NoError(t, err, "the shared typed-literal corpus must be readable at %s", corpusPath)
	var doc struct {
		Cases []typedLiteralCase `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.NotEmpty(t, doc.Cases, "an empty corpus would make this gate pass everything")

	for _, tc := range doc.Cases {
		err := typedLiteralValueError(tc.Value)
		if tc.Accept {
			require.NoError(t, err, "corpus says %q is accepted (%s), the installer refused it", tc.Value, tc.Why)
			continue
		}
		require.Error(t, err, "corpus says %q is refused (%s), the installer accepted it", tc.Value, tc.Why)
	}
}
