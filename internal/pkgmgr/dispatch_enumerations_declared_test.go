package pkgmgr

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpDispatchBody_EmitsDeclaredEnumerations locks the installer's half of the
// class-(e) declaration wire on the op-meta dispatch surface. The `.dispatch`
// aspect body is what every descriptor-driven client reads, so a walk a package
// declares and the installer drops is a walk that never reaches an envelope —
// the field covered, the delivering line not.
//
// It also pins the omission: an op declaring no walks must emit no
// `enumerations` key at all, so every shipped walk-free dispatch body keeps its
// exact shape across installs.
func TestOpDispatchBody_EmitsDeclaredEnumerations(t *testing.T) {
	t.Parallel()

	body := opDispatchBody(&OpDispatchSpec{
		Class:       "cafeOrder",
		AuthContext: "standing",
		Reads:       []string{"{actor}"},
		Enumerations: []EnumerationSpec{
			{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
			{Hub: "{payload.orderKey}", Relation: "placedBy", Direction: "in"},
		},
	})
	require.Equal(t, []any{
		map[string]any{"hub": "{actor}", "relation": "holdsRole", "direction": "out"},
		map[string]any{"hub": "{payload.orderKey}", "relation": "placedBy", "direction": "in"},
	}, body["enumerations"])

	walkFree := opDispatchBody(&OpDispatchSpec{Class: "cafeOrder", AuthContext: "standing"})
	require.NotContains(t, walkFree, "enumerations", "an op declaring no walks must not emit the key")
}

// TestOpDispatchArtifact_EnumerationsRoundTripToTheDispatchBody walks the
// AI-authoring lane end to end: an opMeta proposal declaring walks must be
// reported as an EXPOSED field rather than a smuggled key
// (knownDispatchFields), materialize onto the same OpDispatchSpec a
// hand-authored package carries, and emit onto the `.dispatch` body with its
// hub/relation/direction intact.
//
// The unknownOpMetaFields assertion is load-bearing: OpDispatchArtifact and
// knownDispatchFields are two independent lists, and a field added to the
// struct alone materializes fine while the artifact is rejected as smuggling an
// unexposed key.
func TestOpDispatchArtifact_EnumerationsRoundTripToTheDispatchBody(t *testing.T) {
	t.Parallel()

	const content = `{
	  "operationType": "PlaceCafeOrder",
	  "dispatch": {"class": "cafeOrder", "authContext": "standing",
	               "reads": ["{actor}"],
	               "enumerations": [{"hub": "{actor}", "relation": "holdsRole", "direction": "out"}]}
	}`

	var oc OpMetaArtifactContent
	require.NoError(t, json.Unmarshal([]byte(content), &oc))
	require.Empty(t, unknownOpMetaFields(json.RawMessage(content)),
		"enumerations is an exposed dispatch field, not a smuggled key")

	def := opMetaArtifactDefinition(oc, "cafe-domain", "1.0.0")
	dispatch := def.OpMetas[0].Dispatch
	require.NotNil(t, dispatch)
	require.Equal(t,
		[]EnumerationSpec{{Hub: "{actor}", Relation: "holdsRole", Direction: "out"}},
		dispatch.Enumerations,
		"the artifact's declared walk must materialize onto OpDispatchSpec")
	require.NoError(t, def.ValidateOpDispatchTemplates())

	require.Equal(t,
		[]any{map[string]any{"hub": "{actor}", "relation": "holdsRole", "direction": "out"}},
		opDispatchBody(dispatch)["enumerations"],
		"the materialized walk must survive onto the installed dispatch body")
}

// dispatchArtifactExclusions are the OpDispatchSpec fields an AI-authored op
// meta may NOT declare. The list is spelled out here, not derived, because that
// is the whole point: what the authored surface omits is a decision about the
// authored-artifact admission model, and a decision has to be written down
// somewhere a reviewer reads.
var dispatchArtifactExclusions = map[string]bool{
	"ClassChoices": true,
	"VisibleWhen":  true,
}

// TestOpDispatchArtifact_IsTheDeclaredSubsetOfOpDispatchSpec pins the authored
// dispatch surface as an explicit SUBSET of OpDispatchSpec: every spec field
// except the named exclusions, and nothing else. Adding a field to
// OpDispatchSpec then fails here until someone decides which side it lands on,
// so a field is never silently absent from the authored lane and never
// silently admitted into it.
//
// It also ties the struct to knownDispatchFields in both directions. The two
// are independent lists that must agree: a field on the struct with no
// allowlist entry is rejected as a smuggled key even though it materializes
// fine, and an allowlist entry with no field admits a key that json.Unmarshal
// then silently drops. Neither failure is visible from reading either list
// alone.
func TestOpDispatchArtifact_IsTheDeclaredSubsetOfOpDispatchSpec(t *testing.T) {
	t.Parallel()

	fieldNames := func(v any) []string {
		typ := reflect.TypeOf(v)
		out := make([]string, 0, typ.NumField())
		for i := range typ.NumField() {
			out = append(out, typ.Field(i).Name)
		}
		sort.Strings(out)
		return out
	}

	specFields := fieldNames(OpDispatchSpec{})
	want := make([]string, 0, len(specFields))
	for _, f := range specFields {
		if !dispatchArtifactExclusions[f] {
			want = append(want, f)
		}
	}

	require.Equal(t, want, fieldNames(OpDispatchArtifact{}),
		"OpDispatchArtifact must be exactly OpDispatchSpec minus %v. A field added to OpDispatchSpec needs a deliberate call here: ADMIT it to the authored lane (add it to this struct, to knownDispatchFields, and to opMetaArtifactDefinition's conversion) or WITHHOLD it (add its name to dispatchArtifactExclusions) — admitting one widens what an AI-authored capability may declare, which is a decision about the admission model, not bookkeeping",
		dispatchArtifactExclusions)

	// An exclusion naming a field OpDispatchSpec no longer has is stale: it
	// would silently excuse a real field of the same name from the check above
	// if one were ever added back.
	for name := range dispatchArtifactExclusions {
		require.Contains(t, specFields, name,
			"dispatchArtifactExclusions names %q, which is not an OpDispatchSpec field — drop the stale exclusion", name)
	}

	// Struct ↔ allowlist, both directions.
	artifactType := reflect.TypeOf(OpDispatchArtifact{})
	wireNames := make(map[string]bool, artifactType.NumField())
	for i := range artifactType.NumField() {
		f := artifactType.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		require.NotEmpty(t, tag, "field %s needs a json tag naming the key an author writes", f.Name)
		wireNames[tag] = true
		require.True(t, knownDispatchFields[tag],
			"field %s is declarable as %q but knownDispatchFields does not admit it — unknownOpMetaFields would report a legal artifact as smuggling %q", f.Name, tag, tag)
	}
	for tag := range knownDispatchFields {
		require.True(t, wireNames[tag],
			"knownDispatchFields admits %q but no OpDispatchArtifact field carries that json tag — an author writing it would have it silently dropped instead of refused", tag)
	}
}

// TestOpDispatchArtifact_WithheldFieldsAreRefused pins the consequence of the
// exclusions from the authoring side: an artifact declaring a withheld field is
// REFUSED by name, not quietly dropped, so an author is told the surface does
// not take it rather than shipping an op meta missing the affordance they wrote.
func TestOpDispatchArtifact_WithheldFieldsAreRefused(t *testing.T) {
	t.Parallel()

	const content = `{
	  "operationType": "PauseVisitSeries",
	  "dispatch": {"authContext": "standing", "targetField": "seriesKey",
	               "classChoices": ["clinicVisitSeries"],
	               "visibleWhen": {"field": "series_status", "equals": "active"}}
	}`

	require.Equal(t,
		[]string{"dispatch.classChoices", "dispatch.visibleWhen"},
		unknownOpMetaFields(json.RawMessage(content)),
		"a withheld dispatch field must be reported as a key this surface does not expose")
}

// TestValidateOpDispatchTemplates_EnumerationsHeldToTheEnvelopeShape refuses at
// install every declaration the Processor's envelope parse would refuse (hub
// and relation non-empty, direction "out" or "in") plus a hub outside the
// dispatch read-template vocabulary. The refusal downstream is not a
// degradation: the Processor rejects the WHOLE envelope on a malformed
// enumeration, terminally, so the op never runs and every redelivery
// reproduces the identical dead envelope — install is the loud failure point.
func TestValidateOpDispatchTemplates_EnumerationsHeldToTheEnvelopeShape(t *testing.T) {
	t.Parallel()

	enumDef := func(en EnumerationSpec) Definition {
		return Definition{
			Name: "testpkg",
			OpMetas: []OpMetaSpec{{
				OperationType: "TestOp",
				Dispatch:      &OpDispatchSpec{Enumerations: []EnumerationSpec{en}},
			}},
		}
	}

	for _, tc := range []struct {
		name    string
		en      EnumerationSpec
		wantErr string
	}{
		{
			"the actor-role confinement walk is admitted",
			EnumerationSpec{Hub: "{actor}", Relation: "holdsRole", Direction: "out"}, "",
		},
		{
			"a payload-templated hub is admitted",
			EnumerationSpec{Hub: "{payload.orderKey}", Relation: "placedBy", Direction: "in"}, "",
		},
		{
			"a literal hub is admitted",
			EnumerationSpec{Hub: "vtx.identity.AAidentityHJKMNPQRST", Relation: "holdsRole", Direction: "out"}, "",
		},
		{
			"an empty hub names no vertex",
			EnumerationSpec{Relation: "holdsRole", Direction: "out"}, "requires a Hub",
		},
		{
			"an empty relation names no walk",
			EnumerationSpec{Hub: "{actor}", Direction: "out"}, "requires a Relation",
		},
		{
			"a direction outside out|in is rejected",
			EnumerationSpec{Hub: "{actor}", Relation: "holdsRole", Direction: "outward"}, "Direction must be",
		},
		{
			"an empty direction is rejected",
			EnumerationSpec{Hub: "{actor}", Relation: "holdsRole", Direction: ""}, "Direction must be",
		},
		{
			"a hub outside the read-template vocabulary is rejected",
			EnumerationSpec{Hub: "{bogus}", Relation: "holdsRole", Direction: "out"},
			"outside the closed read-template vocabulary",
		},
		{
			"a hub carrying a ContextParams-only placeholder is rejected",
			EnumerationSpec{Hub: "{entity.orderKey}", Relation: "placedBy", Direction: "in"},
			"ContextParams-only vocabulary",
		},
		{
			"a hub with an unterminated brace is rejected",
			EnumerationSpec{Hub: "{actor", Relation: "holdsRole", Direction: "out"},
			"unterminated '{' never closes",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := enumDef(tc.en).ValidateOpDispatchTemplates()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
			require.ErrorContains(t, err, `op "TestOp"`, "the refusal must name the op meta")
			require.ErrorContains(t, err, "Enumerations[0]", "the refusal must name the offending index")
		})
	}
}

// TestValidateOpDispatchTemplates_EnumerationHubRefusesClientOnlyPlaceholder
// pins both halves of the client-only rule on a hub: the refusal itself, and
// the REMEDY it offers. The hub and the Reads list share one classifier but not
// one fix — Dispatch.Enumerations has no optional half, so an author who
// followed the Reads remedy ("move this read to Dispatch.OptionalReads") would
// be writing a field that does not exist on this declaration. Asserting each
// tail's presence AND the other's absence is what stops the two from re-merging
// into one sentence that is wrong for one caller.
func TestValidateOpDispatchTemplates_EnumerationHubRefusesClientOnlyPlaceholder(t *testing.T) {
	t.Parallel()

	// Positive vector: the same walk with a server-resolvable hub installs
	// clean, so the refusal below is about the placeholder and not the walk.
	okDef := Definition{Name: "testpkg", OpMetas: []OpMetaSpec{{
		OperationType: "TestOp",
		Dispatch: &OpDispatchSpec{Enumerations: []EnumerationSpec{
			{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
		}},
	}}}
	require.NoError(t, okDef.ValidateOpDispatchTemplates())

	hubDef := Definition{Name: "testpkg", OpMetas: []OpMetaSpec{{
		OperationType: "TestOp",
		Dispatch: &OpDispatchSpec{Enumerations: []EnumerationSpec{
			{Hub: "{me.instructor:id}", Relation: "holdsRole", Direction: "out"},
		}},
	}}}
	hubErr := hubDef.ValidateOpDispatchTemplates()
	require.ErrorContains(t, hubErr, "client-only")
	require.ErrorContains(t, hubErr, "Enumerations[0].Hub", "the refusal must name the offending hub")
	require.ErrorContains(t, hubErr, "{me.instructor:id}")
	require.ErrorContains(t, hubErr, "server-resolvable placeholder",
		"the hub remedy is a server-resolvable hub")
	require.ErrorContains(t, hubErr, "literal key",
		"a literal hub is the other remedy open to an enumeration")
	require.NotContains(t, hubErr.Error(), "move this read to Dispatch.OptionalReads",
		"Dispatch.Enumerations has no optional half — pointing an author there produces a spec field that does not exist")

	// The Reads list keeps its own remedy, so the two tails cannot re-merge
	// from the other direction either.
	readsErr := dispatchDef([]string{"{me.instructor:id}"}, nil).ValidateOpDispatchTemplates()
	require.ErrorContains(t, readsErr, "move this read to Dispatch.OptionalReads")

	// And the placeholder stays legal where it belongs.
	require.NoError(t, dispatchDef(nil, []string{"{me.instructor:id}"}).ValidateOpDispatchTemplates(),
		"OptionalReads is the list a client-only placeholder is written for")
}

// TestValidateOpDispatchTemplates_EnumerationsWiredIntoValidateAll pins that the
// enumeration check is reached from Definition.validateAll — the install path
// every package actually travels. Every other assertion in this file calls
// ValidateOpDispatchTemplates directly, so without this the delegating line
// could be deleted and nothing would fail.
func TestValidateOpDispatchTemplates_EnumerationsWiredIntoValidateAll(t *testing.T) {
	// Positive vector: the known-good installer fixture plus one well-formed
	// declaration passes validateAll outright, so the negative below fails
	// BECAUSE of the malformed walk and not because the fixture trips some
	// other validator first (validateAll short-circuits on the first error).
	ok := sampleDef("0.1.0")
	ok.OpMetas = []OpMetaSpec{{
		OperationType: "SampleOp",
		Dispatch: &OpDispatchSpec{Enumerations: []EnumerationSpec{
			{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
		}},
	}}
	require.NoError(t, ok.validateAll(),
		"a well-formed dispatch enumeration must install clean")

	bad := sampleDef("0.1.0")
	bad.OpMetas = []OpMetaSpec{{
		OperationType: "SampleOp",
		Dispatch: &OpDispatchSpec{Enumerations: []EnumerationSpec{
			{Hub: "{actor}", Relation: "holdsRole", Direction: "outward"},
		}},
	}}
	err := bad.validateAll()
	require.Error(t, err, "validateAll must refuse a direction the Processor would refuse")
	require.ErrorContains(t, err, "Direction must be")
	require.ErrorContains(t, err, "outward")
}
