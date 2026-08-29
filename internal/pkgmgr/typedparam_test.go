package pkgmgr

import (
	"encoding/json"
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

// TestGapActionBody_TypedLiteralParamRoundTripsVerbatim pins the half of D1
// that is deliberately a NON-change: the typed literal lives entirely in the
// value grammar, so the installed JSON body keeps its map[string]string shape
// and the token travels to the engine as the authored string, decoded only at
// dispatch. A body that pre-decoded it would change the shape every installed
// package already carries.
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
