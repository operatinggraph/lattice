package pkgmgr

import (
	"strings"
	"testing"
)

func validRetentionClass() RetentionClassSpec {
	return RetentionClassSpec{
		CanonicalName:   "clinicalRecord",
		Description:     "Clinical encounter notes, retained for the statutory medical-records period.",
		Policy:          RetentionPolicyEraseOnExpiry,
		RetentionPeriod: "P7Y",
	}
}

func custodiedDDL(c CustodySpec) DDLSpec {
	return DDLSpec{
		CanonicalName:    "encounter",
		Class:            ddlClassAspectType,
		Sensitive:        true,
		Custody:          c,
		InputSchema:      "{}",
		OutputSchema:     "{}",
		FieldDescription: map[string]string{"note": "the clinical note"},
		Examples:         []ExampleSpec{{Name: "e", Payload: map[string]any{}, ExpectedOutcome: "ok"}},
	}
}

// The positive vector first: a well-formed declaration passes every SHAPE
// rule, so each rejection below is proven to be about the thing it names
// rather than about the fixture being malformed some other way. It then stops
// at the availability gate — which is itself the assertion that the gate runs
// LAST, after the shape rules rather than instead of them.
func TestCustodyScope_WellFormedRetentionClassDeclaration_StopsOnlyAtTheAvailabilityGate(t *testing.T) {
	def := Definition{
		Name:             "clinic-domain",
		RetentionClasses: []RetentionClassSpec{validRetentionClass()},
		DDLs: []DDLSpec{custodiedDDL(CustodySpec{
			Kind: CustodyKindRetentionClass, RetentionClass: "clinicalRecord",
		})},
	}
	if err := def.validateRetentionClasses(); err != nil {
		t.Fatalf("validateRetentionClasses: %v", err)
	}
	err := def.validateCustodyScope()
	if err == nil {
		t.Fatal("retentionClass custody must be refused while the read path cannot resolve it")
	}
	if !strings.Contains(err.Error(), "not installable yet") {
		t.Fatalf("a well-formed declaration must fail ONLY on the availability gate, got: %v", err)
	}
}

// A DDL that declares no custody at all is the overwhelmingly common case and
// must stay untouched — the zero value means the identity kind.
func TestCustodyScope_UndeclaredCustody_Passes(t *testing.T) {
	def := Definition{Name: "identity-domain", DDLs: []DDLSpec{custodiedDDL(CustodySpec{})}}
	if err := def.validateCustodyScope(); err != nil {
		t.Fatalf("an undeclared custody must pass unchanged: %v", err)
	}
}

func TestCustodyScope_Rejections(t *testing.T) {
	cases := []struct {
		name    string
		def     Definition
		wantSub string
	}{
		{
			name:    "unknown kind",
			def:     Definition{Name: "p", DDLs: []DDLSpec{custodiedDDL(CustodySpec{Kind: "vault"})}},
			wantSub: "must be",
		},
		{
			name: "custody on a non-aspectType DDL",
			def: Definition{
				Name:             "p",
				RetentionClasses: []RetentionClassSpec{validRetentionClass()},
				DDLs: []DDLSpec{func() DDLSpec {
					d := custodiedDDL(CustodySpec{Kind: CustodyKindRetentionClass, RetentionClass: "clinicalRecord"})
					d.Class = "meta.ddl.vertexType"
					return d
				}()},
			},
			wantSub: "custody is meaningful only for Class",
		},
		{
			// The one an author is most likely to get wrong, and the one whose
			// silent acceptance would be worst: a package believing it has a
			// retention posture over data that is never encrypted.
			name: "custody declared but not sensitive",
			def: Definition{
				Name:             "p",
				RetentionClasses: []RetentionClassSpec{validRetentionClass()},
				DDLs: []DDLSpec{func() DDLSpec {
					d := custodiedDDL(CustodySpec{Kind: CustodyKindRetentionClass, RetentionClass: "clinicalRecord"})
					d.Sensitive = false
					return d
				}()},
			},
			wantSub: "Sensitive is false",
		},
		{
			name:    "retentionClass kind with no class named",
			def:     Definition{Name: "p", DDLs: []DDLSpec{custodiedDDL(CustodySpec{Kind: CustodyKindRetentionClass})}},
			wantSub: "requires Custody.RetentionClass",
		},
		{
			// Cross-package holder references are refused: a class is a data
			// controller's own declaration, not a shared handle.
			name: "retentionClass names a class this package does not declare",
			def: Definition{
				Name: "p",
				DDLs: []DDLSpec{custodiedDDL(CustodySpec{
					Kind: CustodyKindRetentionClass, RetentionClass: "someoneElsesClass",
				})},
			},
			wantSub: "is not declared by this package",
		},
		{
			name: "identity kind naming a class",
			def: Definition{
				Name:             "p",
				RetentionClasses: []RetentionClassSpec{validRetentionClass()},
				DDLs: []DDLSpec{custodiedDDL(CustodySpec{
					Kind: CustodyKindIdentity, RetentionClass: "clinicalRecord",
				})},
			},
			wantSub: "only kind",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.def.validateCustodyScope()
			if err == nil {
				t.Fatalf("%s must reject", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestRetentionClasses_Rejections(t *testing.T) {
	mutate := func(f func(*RetentionClassSpec)) Definition {
		rc := validRetentionClass()
		f(&rc)
		return Definition{Name: "p", RetentionClasses: []RetentionClassSpec{rc}}
	}
	cases := []struct {
		name    string
		def     Definition
		wantSub string
	}{
		{"no canonical name", mutate(func(rc *RetentionClassSpec) { rc.CanonicalName = "" }), "CanonicalName required"},
		{"unimplemented policy", mutate(func(rc *RetentionClassSpec) { rc.Policy = "eraseOnRequest" }), "is the only policy implemented"},
		{"no period", mutate(func(rc *RetentionClassSpec) { rc.RetentionPeriod = "" }), "RetentionPeriod required"},
		{"no description", mutate(func(rc *RetentionClassSpec) { rc.Description = "" }), "Description required"},
		{
			// The name salts the holder's NanoID, so a duplicate would custody
			// two declared obligations on one key — and shredding either would
			// silently destroy both.
			"duplicate canonical name",
			Definition{Name: "p", RetentionClasses: []RetentionClassSpec{validRetentionClass(), validRetentionClass()}},
			"declared twice",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.def.validateRetentionClasses()
			if err == nil {
				t.Fatalf("%s must reject", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

// The holder key a package resolves at install must be the key the Processor
// later addresses: deterministic, version-independent, and — the trap this
// design had to correct for — an ALL-LOWERCASE type segment.
func TestRetentionClassKey_IsDeterministicAndLowercase(t *testing.T) {
	a := RetentionClassKey("clinic-domain", "clinicalRecord")
	b := RetentionClassKey("clinic-domain", "clinicalRecord")
	if a != b {
		t.Fatalf("holder key is not deterministic: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "vtx.retentionclass.") {
		t.Fatalf("holder key %q must carry the all-lowercase retentionclass type segment", a)
	}
	if other := RetentionClassKey("lease-signing", "clinicalRecord"); other == a {
		t.Fatal("two packages declaring the same class name must not collide on one holder")
	}
}

// The install batch is where a declared class becomes an addressable holder
// and a DDL's class NAME becomes a resolved holder KEY. Getting the second
// half wrong is what would force the commit path into an extra read per
// sensitive write — the cost §3.2 exists to avoid.
func TestBuildInstallBatch_RetentionClassMintsHolderAndResolvesCustody(t *testing.T) {
	def := Definition{
		Name:             "clinic-domain",
		RetentionClasses: []RetentionClassSpec{validRetentionClass()},
		DDLs: []DDLSpec{custodiedDDL(CustodySpec{
			Kind: CustodyKindRetentionClass, RetentionClass: "clinicalRecord",
		})},
	}
	ops, declared, err := BuildInstallBatchForTest(def)
	if err != nil {
		t.Fatalf("BuildInstallBatchForTest: %v", err)
	}

	holderKey := RetentionClassKey("clinic-domain", "clinicalRecord")
	byKey := map[string]map[string]any{}
	for _, op := range ops {
		byKey[op.Key] = op.Document
	}

	root, ok := byKey[holderKey]
	if !ok {
		t.Fatalf("no holder vertex minted at %s", holderKey)
	}
	if root["class"] != RetentionClassVertexType {
		t.Fatalf("holder class = %v, want %q", root["class"], RetentionClassVertexType)
	}

	policy, ok := byKey[holderKey+".retentionPolicy"]
	if !ok {
		t.Fatalf("no .retentionPolicy aspect on %s", holderKey)
	}
	pd, _ := policy["data"].(map[string]any)
	if pd["canonicalName"] != "clinicalRecord" || pd["policy"] != RetentionPolicyEraseOnExpiry || pd["retentionPeriod"] != "P7Y" {
		t.Fatalf("retentionPolicy body wrong: %v", pd)
	}

	// The DDL's .custody aspect must carry the RESOLVED holder key, not just
	// the class name — that is what keeps step 6.5 read-free.
	ddlKey := "vtx.meta." + EntityNanoIDForTest(def.Name, "ddl:encounter")
	custody, ok := byKey[ddlKey+".custody"]
	if !ok {
		t.Fatalf("no .custody aspect on the DDL %s", ddlKey)
	}
	cd, _ := custody["data"].(map[string]any)
	if cd["kind"] != CustodyKindRetentionClass {
		t.Fatalf("custody kind = %v, want %q", cd["kind"], CustodyKindRetentionClass)
	}
	if cd["holderKey"] != holderKey {
		t.Fatalf("custody holderKey = %v, want the minted holder %q", cd["holderKey"], holderKey)
	}

	// Both holder keys must be reclaimable at uninstall.
	for _, want := range []string{holderKey, holderKey + ".retentionPolicy"} {
		found := false
		for _, d := range declared {
			if d == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s is not in declaredKeys, so uninstall would orphan it", want)
		}
	}
}

// A package declaring no custody emits no .custody aspect and mints no holder:
// its install batch carries nothing from this mechanism at all.
func TestBuildInstallBatch_NoCustodyDeclared_EmitsNoCustodyAspect(t *testing.T) {
	def := Definition{Name: "identity-domain", DDLs: []DDLSpec{custodiedDDL(CustodySpec{})}}
	ops, _, err := BuildInstallBatchForTest(def)
	if err != nil {
		t.Fatalf("BuildInstallBatchForTest: %v", err)
	}
	for _, op := range ops {
		if strings.HasSuffix(op.Key, ".custody") {
			t.Fatalf("an undeclared custody must emit nothing, got %s", op.Key)
		}
		if strings.HasPrefix(op.Key, "vtx."+RetentionClassVertexType+".") {
			t.Fatalf("no retention-class holder may be minted when none is declared, got %s", op.Key)
		}
	}
}
