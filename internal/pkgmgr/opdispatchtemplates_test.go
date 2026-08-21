package pkgmgr

import (
	"strings"
	"testing"
)

// dispatchDef builds a single-op Definition carrying the given Reads /
// OptionalReads templates, the minimal fixture shape this file's table
// tests need.
func dispatchDef(reads, optionalReads []string) Definition {
	return Definition{
		Name: "testpkg",
		OpMetas: []OpMetaSpec{{
			OperationType: "TestOp",
			Dispatch: &OpDispatchSpec{
				Reads:         reads,
				OptionalReads: optionalReads,
			},
		}},
	}
}

func TestValidateOpDispatchTemplates_NilDispatchAccepted(t *testing.T) {
	def := Definition{OpMetas: []OpMetaSpec{{OperationType: "TestOp", Dispatch: nil}}}
	if err := def.ValidateOpDispatchTemplates(); err != nil {
		t.Fatalf("expected nil Dispatch to be accepted, got: %v", err)
	}
}

func TestValidateOpDispatchTemplates_EmptyReadListsAccepted(t *testing.T) {
	def := dispatchDef(nil, nil)
	if err := def.ValidateOpDispatchTemplates(); err != nil {
		t.Fatalf("expected empty read lists to be accepted, got: %v", err)
	}
}

// --- Acceptance: every legal placeholder, ± :id -----------------------------

func TestValidateOpDispatchTemplates_LegalResolvablePlaceholders(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry string
	}{
		{"actor", "{actor}"},
		{"actor:id", "{actor:id}"},
		{"scopedTo", "{scopedTo}"},
		{"scopedTo:id", "{scopedTo:id}"},
		{"service", "{service}"},
		{"service:id", "{service:id}"},
		{"payload.field", "{payload.leaseAppKey}"},
		{"payload.field:id", "{payload.leaseAppKey:id}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Resolvable placeholders are legal in BOTH lists.
			if err := dispatchDef([]string{tc.entry}, nil).ValidateOpDispatchTemplates(); err != nil {
				t.Errorf("Reads entry %q: expected accept, got: %v", tc.entry, err)
			}
			if err := dispatchDef(nil, []string{tc.entry}).ValidateOpDispatchTemplates(); err != nil {
				t.Errorf("OptionalReads entry %q: expected accept, got: %v", tc.entry, err)
			}
		})
	}
}

func TestValidateOpDispatchTemplates_WholeSegmentMeInOptionalReads(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry string
	}{
		{"whole-key form", "{me.leaseapp}"},
		{"whole-key form with :id", "{me.leaseapp:id}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := dispatchDef(nil, []string{tc.entry}).ValidateOpDispatchTemplates(); err != nil {
				t.Errorf("OptionalReads entry %q: expected accept, got: %v", tc.entry, err)
			}
		})
	}
}

// TestValidateOpDispatchTemplates_LiveCorpusShapesAccepted pins the exact
// live template strings the census (internal/pkgregistry) counts, so this
// file and the corpus can never silently disagree about what the gate
// accepts.
func TestValidateOpDispatchTemplates_LiveCorpusShapesAccepted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry string
	}{
		{"cafe Charge", "lnk.leaseapp.{me.leaseapp:id}.applicationFor.identity.{actor:id}"},
		{"cafe Settle", "lnk.tab.{payload.tabKey:id}.chargedTo.leaseapp.{me.leaseapp:id}"},
		{"wellness mid-segment resolvable", "vtx.session.{payload.session:id}.bkr{actor:id}"},
		{"clinic-reminders mid-segment resolvable", "{payload.patientKey}.activeVisitSeriesWith{payload.providerKey:id}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := dispatchDef(nil, []string{tc.entry}).ValidateOpDispatchTemplates(); err != nil {
				t.Errorf("OptionalReads entry %q: expected accept, got: %v", tc.entry, err)
			}
		})
	}
}

// --- Refusal: one vector per excluded shape, each proven against its
//     minimally-different accepted sibling first ----------------------------

func TestValidateOpDispatchTemplates_EntityPlaceholderRefused(t *testing.T) {
	// Positive vector: the same-shaped entry with a resolvable placeholder
	// reaches the gate clean, so the refusal below is about {entity.*}
	// specifically, not the surrounding key shape.
	ok := dispatchDef(nil, []string{"{payload.studioKey}"})
	if err := ok.ValidateOpDispatchTemplates(); err != nil {
		t.Fatalf("positive vector: expected accept, got: %v", err)
	}

	bad := dispatchDef(nil, []string{"{entity.studioKey}"})
	err := bad.ValidateOpDispatchTemplates()
	if err == nil {
		t.Fatal("expected refusal for {entity.<column>} in a read list, got nil")
	}
	if !strings.Contains(err.Error(), "{entity.studioKey}") {
		t.Errorf("error should name the offending entry/placeholder %q; got %q", "{entity.studioKey}", err)
	}
	// The default-deny "unknown placeholder" refusal also names the entry
	// and placeholder, so without this the test cannot tell the tailored
	// {entity.*} guidance apart from a generic unknown-vocabulary refusal —
	// assert the ContextParams-specific wording only the tailored message
	// carries.
	if !strings.Contains(err.Error(), "ContextParams") {
		t.Errorf("error should carry the {entity.<column>}-specific ContextParams guidance, not just a generic unknown-placeholder refusal; got %q", err)
	}
}

func TestValidateOpDispatchTemplates_OptionalMarkerRefused(t *testing.T) {
	// Positive vector: the same placeholder without the `?` marker is legal
	// OptionalReads vocabulary.
	ok := dispatchDef(nil, []string{"{me.leaseapp:id}"})
	if err := ok.ValidateOpDispatchTemplates(); err != nil {
		t.Fatalf("positive vector: expected accept, got: %v", err)
	}

	bad := dispatchDef(nil, []string{"{me.leaseapp:id?}"})
	err := bad.ValidateOpDispatchTemplates()
	if err == nil {
		t.Fatal("expected refusal for a `?` OPTIONAL marker in a read list, got nil")
	}
	if !strings.Contains(err.Error(), "{me.leaseapp:id?}") {
		t.Errorf("error should name the offending entry/placeholder %q; got %q", "{me.leaseapp:id?}", err)
	}
}

func TestValidateOpDispatchTemplates_UnknownPlaceholderRefused(t *testing.T) {
	// Positive vector: a legal payload placeholder in the same position.
	ok := dispatchDef(nil, []string{"{payload.foo}"})
	if err := ok.ValidateOpDispatchTemplates(); err != nil {
		t.Fatalf("positive vector: expected accept, got: %v", err)
	}

	bad := dispatchDef(nil, []string{"{bogus}"})
	err := bad.ValidateOpDispatchTemplates()
	if err == nil {
		t.Fatal("expected refusal for an unknown placeholder, got nil")
	}
	if !strings.Contains(err.Error(), "{bogus}") {
		t.Errorf("error should name the offending entry/placeholder %q; got %q", "{bogus}", err)
	}
}

func TestValidateOpDispatchTemplates_RequiredSideMeRefused(t *testing.T) {
	// Positive vector: the identical placeholder is legal on the
	// OptionalReads side.
	ok := dispatchDef(nil, []string{"{me.leaseapp:id}"})
	if err := ok.ValidateOpDispatchTemplates(); err != nil {
		t.Fatalf("positive vector: expected accept, got: %v", err)
	}

	bad := dispatchDef([]string{"{me.leaseapp:id}"}, nil)
	err := bad.ValidateOpDispatchTemplates()
	if err == nil {
		t.Fatal("expected refusal for a required-side {me.<type>}, got nil")
	}
	if !strings.Contains(err.Error(), "{me.leaseapp:id}") {
		t.Errorf("error should name the offending entry/placeholder %q; got %q", "{me.leaseapp:id}", err)
	}
}

func TestValidateOpDispatchTemplates_MidSegmentClientOnlyFragmentRefused(t *testing.T) {
	// Positive vector: the same placeholder occupying its own whole segment
	// is legal OptionalReads vocabulary.
	ok := dispatchDef(nil, []string{"{me.instructor:id}"})
	if err := ok.ValidateOpDispatchTemplates(); err != nil {
		t.Fatalf("positive vector: expected accept, got: %v", err)
	}

	for _, tc := range []struct {
		name  string
		entry string
	}{
		{"literal before", "bkr{me.instructor:id}"},
		{"literal after", "{me.instructor:id}bkr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := dispatchDef(nil, []string{tc.entry})
			err := bad.ValidateOpDispatchTemplates()
			if err == nil {
				t.Fatalf("expected refusal for a mid-segment client-only fragment %q, got nil", tc.entry)
			}
			if !strings.Contains(err.Error(), tc.entry) {
				t.Errorf("error should name the offending entry %q; got %q", tc.entry, err)
			}
			if !strings.Contains(err.Error(), "{me.instructor:id}") {
				t.Errorf("error should name the offending placeholder %q; got %q", "{me.instructor:id}", err)
			}
		})
	}
}

// --- Refusal: template well-formedness (unbalanced braces, empty segments) -

func TestValidateOpDispatchTemplates_UnterminatedBraceRefused(t *testing.T) {
	// Positive vector: the same entry with the closing brace present is a
	// legal payload placeholder.
	ok := dispatchDef(nil, []string{"{payload.leaseAppKey}"})
	if err := ok.ValidateOpDispatchTemplates(); err != nil {
		t.Fatalf("positive vector: expected accept, got: %v", err)
	}

	bad := dispatchDef(nil, []string{"{payload.leaseAppKey"})
	err := bad.ValidateOpDispatchTemplates()
	if err == nil {
		t.Fatal("expected refusal for an unterminated '{', got nil")
	}
	if !strings.Contains(err.Error(), "{payload.leaseAppKey") {
		t.Errorf("error should name the offending entry %q; got %q", "{payload.leaseAppKey", err)
	}
}

func TestValidateOpDispatchTemplates_StrayClosingBraceRefused(t *testing.T) {
	// Positive vector: the same entry with the stray '}' removed is plain
	// literal text, which is legal (no placeholder at all).
	ok := dispatchDef(nil, []string{"payload.leaseAppKey"})
	if err := ok.ValidateOpDispatchTemplates(); err != nil {
		t.Fatalf("positive vector: expected accept, got: %v", err)
	}

	bad := dispatchDef(nil, []string{"payload.leaseAppKey}"})
	err := bad.ValidateOpDispatchTemplates()
	if err == nil {
		t.Fatal("expected refusal for a stray '}', got nil")
	}
	if !strings.Contains(err.Error(), "payload.leaseAppKey}") {
		t.Errorf("error should name the offending entry %q; got %q", "payload.leaseAppKey}", err)
	}
}

func TestValidateOpDispatchTemplates_DoubledDotEmptySegmentRefused(t *testing.T) {
	// Positive vector: the same entry with a single '.' between the segments
	// is legal.
	ok := dispatchDef(nil, []string{"{actor}.state"})
	if err := ok.ValidateOpDispatchTemplates(); err != nil {
		t.Fatalf("positive vector: expected accept, got: %v", err)
	}

	bad := dispatchDef(nil, []string{"{actor}..state"})
	err := bad.ValidateOpDispatchTemplates()
	if err == nil {
		t.Fatal("expected refusal for a doubled '.' (empty segment), got nil")
	}
	if !strings.Contains(err.Error(), "{actor}..state") {
		t.Errorf("error should name the offending entry %q; got %q", "{actor}..state", err)
	}
}

func TestValidateOpDispatchTemplates_LeadingDotEmptySegmentRefused(t *testing.T) {
	// Positive vector: the same entry without the leading '.' is legal.
	ok := dispatchDef(nil, []string{"{actor}"})
	if err := ok.ValidateOpDispatchTemplates(); err != nil {
		t.Fatalf("positive vector: expected accept, got: %v", err)
	}

	bad := dispatchDef(nil, []string{".{actor}"})
	err := bad.ValidateOpDispatchTemplates()
	if err == nil {
		t.Fatal("expected refusal for a leading '.' (empty segment), got nil")
	}
	if !strings.Contains(err.Error(), ".{actor}") {
		t.Errorf("error should name the offending entry %q; got %q", ".{actor}", err)
	}
}

func TestValidateOpDispatchTemplates_TrailingDotEmptySegmentRefused(t *testing.T) {
	// Positive vector: the same entry without the trailing '.' is legal.
	ok := dispatchDef(nil, []string{"{actor}"})
	if err := ok.ValidateOpDispatchTemplates(); err != nil {
		t.Fatalf("positive vector: expected accept, got: %v", err)
	}

	bad := dispatchDef(nil, []string{"{actor}."})
	err := bad.ValidateOpDispatchTemplates()
	if err == nil {
		t.Fatal("expected refusal for a trailing '.' (empty segment), got nil")
	}
	if !strings.Contains(err.Error(), "{actor}.") {
		t.Errorf("error should name the offending entry %q; got %q", "{actor}.", err)
	}
}

// TestValidateOpDispatchTemplates_EmptySegmentInsidePlaceholderBodyRefused
// is the vector that pins validateNoEmptySegments splitting the RAW entry
// rather than a placeholder-masked one: a doubled '.' INSIDE a placeholder
// body is only visible to a raw split — a masked split collapses the whole
// `{...}` to one opaque token before the empty segment is ever seen.
func TestValidateOpDispatchTemplates_EmptySegmentInsidePlaceholderBodyRefused(t *testing.T) {
	// Positive vector: the same placeholder with a single '.' between "a"
	// and "b" is a legal (if unusual) payload field reference.
	ok := dispatchDef(nil, []string{"{payload.a.b}"})
	if err := ok.ValidateOpDispatchTemplates(); err != nil {
		t.Fatalf("positive vector: expected accept, got: %v", err)
	}

	bad := dispatchDef(nil, []string{"{payload.a..b}"})
	err := bad.ValidateOpDispatchTemplates()
	if err == nil {
		t.Fatal("expected refusal for a doubled '.' inside a placeholder body, got nil")
	}
	if !strings.Contains(err.Error(), "{payload.a..b}") {
		t.Errorf("error should name the offending entry %q; got %q", "{payload.a..b}", err)
	}
}

// TestValidateOpDispatchTemplates_WiredIntoValidateAll pins that
// ValidateOpDispatchTemplates is actually reached from Definition.validateAll
// — the install path every one of pkgmgr's other checks is proven through
// (packagename_test.go:168's TestValidatePackageName_RefusesUnnormalizedName
// is the precedent this mirrors). Every other test in this file calls
// ValidateOpDispatchTemplates directly, and the pkgregistry corpus census
// walks Definition structurally — neither exercises the validateAll wiring
// line in definition.go, so deleting that line breaks nothing without this
// test.
func TestValidateOpDispatchTemplates_WiredIntoValidateAll(t *testing.T) {
	// Positive vector: a Definition that is otherwise the known-good
	// installer fixture, plus one legal dispatch template, passes
	// validateAll() outright — proves the negative below fails BECAUSE of
	// the offending template, not because the fixture trips some other
	// validator first (validateAll short-circuits on the first error).
	ok := sampleDef("0.1.0")
	ok.OpMetas = []OpMetaSpec{{
		OperationType: "SampleOp",
		Dispatch:      &OpDispatchSpec{OptionalReads: []string{"{payload.id}"}},
	}}
	if err := ok.validateAll(); err != nil {
		t.Fatalf("positive vector: expected validateAll() to accept a legal dispatch template, got: %v", err)
	}

	bad := sampleDef("0.1.0")
	bad.OpMetas = []OpMetaSpec{{
		OperationType: "SampleOp",
		Dispatch:      &OpDispatchSpec{OptionalReads: []string{"{entity.id}"}},
	}}
	err := bad.validateAll()
	if err == nil {
		t.Fatal("expected validateAll() to refuse a {entity.<column>} read template, got nil")
	}
	if !strings.Contains(err.Error(), "{entity.id}") || !strings.Contains(err.Error(), "ContextParams") {
		t.Errorf("validateAll() error should be ValidateOpDispatchTemplates' own {entity.<column>} refusal, naming the entry and the ContextParams guidance; got %q", err)
	}
}
