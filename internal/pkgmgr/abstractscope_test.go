package pkgmgr

import (
	"strings"
	"testing"
)

// abstractDDL returns a well-formed abstract DDLSpec (positive vector): legal
// on every axis validateAbstractDDLScope checks, so each rejection test below
// can flip exactly one field and prove the rejection is about THAT field.
func abstractDDL(canonicalName string) DDLSpec {
	return DDLSpec{
		CanonicalName:    canonicalName,
		Class:            ddlClassVertexType,
		Abstract:         true,
		Description:      canonicalName + " abstract type",
		InputSchema:      `{"type":"object"}`,
		OutputSchema:     `{"type":"object"}`,
		FieldDescription: map[string]string{canonicalName: "the " + canonicalName},
		Examples:         []ExampleSpec{{Name: canonicalName, Payload: map[string]any{}, ExpectedOutcome: "n/a — abstract"}},
	}
}

// The positive vector first: a well-formed abstract declaration must pass, so
// every rejection below is proven to be about the flipped field, not about
// the fixture being malformed some other way.
func TestAbstractScope_WellFormedAbstractDDL_Passes(t *testing.T) {
	def := Definition{Name: "location-domain", DDLs: []DDLSpec{abstractDDL("location")}}
	if err := def.validateAbstractDDLScope(); err != nil {
		t.Fatalf("a well-formed abstract DDL must pass: %v", err)
	}
}

// Abstract is meaningful only when Class defaults to vertexType too (the
// empty-Class fallback buildInstallBatch itself applies).
func TestAbstractScope_EmptyClassDefaultsToVertexType_Passes(t *testing.T) {
	d := abstractDDL("location")
	d.Class = ""
	def := Definition{Name: "location-domain", DDLs: []DDLSpec{d}}
	if err := def.validateAbstractDDLScope(); err != nil {
		t.Fatalf("Abstract with an empty (vertexType-defaulting) Class must pass: %v", err)
	}
}

// A DDL with LeafBudget explicitly set to the same value as the default is
// still legal — the field is about DECLARING a budget, not about differing
// from the default.
func TestAbstractScope_ExplicitLeafBudget_Passes(t *testing.T) {
	d := abstractDDL("location")
	d.LeafBudget = 4
	def := Definition{Name: "location-domain", DDLs: []DDLSpec{d}}
	if err := def.validateAbstractDDLScope(); err != nil {
		t.Fatalf("a positive LeafBudget on an abstract type must pass: %v", err)
	}
}

// A concrete DDL declaring SubtypeOfRef (a leaf naming its abstract ancestor)
// is the other well-formed shape validateAbstractDDLScope must accept.
func TestAbstractScope_ConcreteSubtypeOfRef_Passes(t *testing.T) {
	def := Definition{
		Name: "location-domain",
		DDLs: []DDLSpec{minimalDDL("unit", "meta.ddl.vertexType", false)},
	}
	def.DDLs[0].SubtypeOfRef = "location"
	if err := def.validateAbstractDDLScope(); err != nil {
		t.Fatalf("a concrete DDL's SubtypeOfRef must pass: %v", err)
	}
}

// An abstract type may itself declare SubtypeOfRef — a multi-level chain
// (§3.4 "multi-level depth: allowed") — as long as the ref names a DIFFERENT
// type than its own CanonicalName.
func TestAbstractScope_AbstractSubtypeOfAbstract_Passes(t *testing.T) {
	d := abstractDDL("billablelocation")
	d.SubtypeOfRef = "billable"
	def := Definition{Name: "billing-domain", DDLs: []DDLSpec{d}}
	if err := def.validateAbstractDDLScope(); err != nil {
		t.Fatalf("an abstract type's own SubtypeOfRef must pass: %v", err)
	}
}

// TestAbstractScope_ConcreteVertexTypeOrdinaryName_Passes is the positive
// vector beside the "concrete vertexType canonicalName meta/op" rejections
// below: an ordinary concrete vertexType name must still pass — the reserved-
// name check is about the two specific names, not a blanket new rejection of
// concrete DDLs.
func TestAbstractScope_ConcreteVertexTypeOrdinaryName_Passes(t *testing.T) {
	def := Definition{Name: "p", DDLs: []DDLSpec{minimalDDL("widget", "meta.ddl.vertexType", false)}}
	if err := def.validateAbstractDDLScope(); err != nil {
		t.Fatalf("an ordinary concrete vertexType CanonicalName must pass: %v", err)
	}
}

// TestAbstractScope_AspectTypeDDLNamedMeta_Passes proves the reserved-name
// check is scoped to vertexType DDLs deliberately, not by accident: an
// aspectType DDL's CanonicalName is never written into a key's vertex-type
// segment (Contract #1 §1.1), so §1.2's reservation does not apply to it —
// "meta" here must be ACCEPTED.
func TestAbstractScope_AspectTypeDDLNamedMeta_Passes(t *testing.T) {
	def := Definition{Name: "p", DDLs: []DDLSpec{minimalDDL("meta", "meta.ddl.aspectType", false)}}
	if err := def.validateAbstractDDLScope(); err != nil {
		t.Fatalf("an aspectType DDL named %q must pass — the reserved-name check is scoped to vertexType DDLs only: %v", "meta", err)
	}
}

func TestAbstractScope_Rejections(t *testing.T) {
	cases := []struct {
		name    string
		def     Definition
		wantSub string
	}{
		{
			name: "abstract on an aspectType DDL",
			def: Definition{Name: "p", DDLs: []DDLSpec{func() DDLSpec {
				d := abstractDDL("secret")
				d.Class = "meta.ddl.aspectType"
				return d
			}()}},
			wantSub: "abstract is meaningful only for Class",
		},
		{
			name: "abstract with a script",
			def: Definition{Name: "p", DDLs: []DDLSpec{func() DDLSpec {
				d := abstractDDL("location")
				d.Script = "def execute(state, op):\n    fail(\"noop\")\n"
				return d
			}()}},
			wantSub: "declares no script",
		},
		{
			name: "abstract with permittedCommands",
			def: Definition{Name: "p", DDLs: []DDLSpec{func() DDLSpec {
				d := abstractDDL("location")
				d.PermittedCommands = []string{"SomeOp"}
				return d
			}()}},
			wantSub: "declares no permitted commands",
		},
		{
			name: "abstract with a negative LeafBudget",
			def: Definition{Name: "p", DDLs: []DDLSpec{func() DDLSpec {
				d := abstractDDL("location")
				d.LeafBudget = -1
				return d
			}()}},
			wantSub: "LeafBudget is negative",
		},
		{
			name:    "abstract canonicalName meta",
			def:     Definition{Name: "p", DDLs: []DDLSpec{abstractDDL("meta")}},
			wantSub: "is reserved",
		},
		{
			name:    "abstract canonicalName op",
			def:     Definition{Name: "p", DDLs: []DDLSpec{abstractDDL("op")}},
			wantSub: "is reserved",
		},
		{
			name:    "concrete vertexType canonicalName meta",
			def:     Definition{Name: "p", DDLs: []DDLSpec{minimalDDL("meta", "meta.ddl.vertexType", false)}},
			wantSub: "is reserved",
		},
		{
			name:    "concrete vertexType canonicalName op",
			def:     Definition{Name: "p", DDLs: []DDLSpec{minimalDDL("op", "meta.ddl.vertexType", false)}},
			wantSub: "is reserved",
		},
		{
			name:    "abstract canonicalName not a valid type segment",
			def:     Definition{Name: "p", DDLs: []DDLSpec{abstractDDL("Location")}},
			wantSub: "not a valid Contract #1 type segment",
		},
		{
			name: "LeafBudget set but Abstract false",
			def: Definition{Name: "p", DDLs: []DDLSpec{func() DDLSpec {
				d := minimalDDL("unit", "meta.ddl.vertexType", false)
				d.LeafBudget = 8
				return d
			}()}},
			wantSub: "LeafBudget is meaningful only on an abstract type",
		},
		{
			name: "SubtypeOfRef on a non-vertexType DDL",
			def: Definition{Name: "p", DDLs: []DDLSpec{func() DDLSpec {
				d := minimalDDL("secret", "meta.ddl.aspectType", true)
				d.SubtypeOfRef = "location"
				return d
			}()}},
			wantSub: "subtypeOf is meaningful only for Class",
		},
		{
			name: "SubtypeOfRef equals own CanonicalName",
			def: Definition{Name: "p", DDLs: []DDLSpec{func() DDLSpec {
				d := minimalDDL("location", "meta.ddl.vertexType", false)
				d.SubtypeOfRef = "location"
				return d
			}()}},
			wantSub: "own CanonicalName",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.def.validateAbstractDDLScope()
			if err == nil {
				t.Fatalf("%s must reject", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("%s: error %q does not contain %q", tc.name, err.Error(), tc.wantSub)
			}
		})
	}
}
