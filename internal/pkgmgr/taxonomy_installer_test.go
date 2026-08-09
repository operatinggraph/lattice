package pkgmgr

import (
	"errors"
	"strings"
	"testing"
)

// abstractPkgDef returns a synthetic package declaring exactly one abstract
// DDL (no leaves) — the minimal cross-package resolution target.
func abstractPkgDef(pkgName, canonicalName string) Definition {
	return Definition{
		Name:        pkgName,
		Version:     "0.1.0",
		Description: "abstract-only test package",
		DDLs:        []DDLSpec{abstractDDL(canonicalName)},
	}
}

// leafPkgDef returns a synthetic package declaring one CONCRETE DDL naming
// subtypeOfRef as its taxonomy ancestor.
func leafPkgDef(pkgName, ddlCanonical, subtypeOfRef string) Definition {
	d := minimalDDL(ddlCanonical, "meta.ddl.vertexType", false)
	d.SubtypeOfRef = subtypeOfRef
	return Definition{
		Name:        pkgName,
		Version:     "0.1.0",
		Description: "leaf test package",
		DDLs:        []DDLSpec{d},
	}
}

// declaredKeyPresent reports whether want appears in keys.
func declaredKeyPresent(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}

// TestInstaller_SubtypeOf_BatchLocalResolution installs a single package
// declaring BOTH the abstract type and a leaf naming it in the same
// Definition — the batch-local resolution path (§3.5), which must resolve
// with no bucket scan and emit the subtypeOf link in the same atomic batch.
func TestInstaller_SubtypeOf_BatchLocalResolution(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	abstract := abstractDDL("location")
	leaf := minimalDDL("unit", "meta.ddl.vertexType", false)
	leaf.SubtypeOfRef = "location"
	def := Definition{Name: "location-domain", Version: "0.1.0", DDLs: []DDLSpec{abstract, leaf}}

	res, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	abstractID := EntityNanoIDForTest(def.Name, "ddl:location")
	leafID := EntityNanoIDForTest(def.Name, "ddl:unit")
	wantKey := "lnk.meta." + leafID + ".subtypeOf.meta." + abstractID
	if !declaredKeyPresent(res.DeclaredKeys, wantKey) {
		t.Fatalf("expected declared key %q, got %v", wantKey, res.DeclaredKeys)
	}
}

// TestInstaller_SubtypeOf_CrossPackageResolution installs the abstract in one
// package and the leaf in a SECOND, unrelated package — proving §3.5's
// headline property: "cross-package declaration needs no cooperation from
// the abstract's owner."
func TestInstaller_SubtypeOf_CrossPackageResolution(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	if _, err := inst.Install(ctx, abstractPkgDef("location-domain", "location")); err != nil {
		t.Fatalf("install abstract package: %v", err)
	}

	res, err := inst.Install(ctx, leafPkgDef("unit-domain", "unit", "location"))
	if err != nil {
		t.Fatalf("install leaf package: %v", err)
	}

	abstractID := EntityNanoIDForTest("location-domain", "ddl:location")
	leafID := EntityNanoIDForTest("unit-domain", "ddl:unit")
	wantKey := "lnk.meta." + leafID + ".subtypeOf.meta." + abstractID
	if !declaredKeyPresent(res.DeclaredKeys, wantKey) {
		t.Fatalf("expected cross-package subtypeOf link %q, got %v", wantKey, res.DeclaredKeys)
	}
}

// TestInstaller_SubtypeOf_FailsClosed_Unresolvable pins §3.5's fail-closed
// resolution: a SubtypeOfRef naming nothing installed must reject the whole
// install — NEVER resolveLensRef's NanoID pass-through.
func TestInstaller_SubtypeOf_FailsClosed_Unresolvable(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	_, err := inst.Install(ctx, leafPkgDef("unit-domain", "unit", "doesnotexist"))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, ErrSubtypeOfRefUnresolved) {
		t.Fatalf("expected ErrSubtypeOfRefUnresolved, got %v", err)
	}
}

// TestInstaller_SubtypeOf_ConcreteParentAccepted pins Andrew's ratification:
// "a concrete type may have subtypes — that is correct" (§3.4). "location" is
// installed as an ORDINARY concrete DDL (Abstract left false, live and
// script-bearing), then "unit" declares SubtypeOfRef: "location" — §3.4's own
// example, "room subtypeOf unit", with the parent concrete. This must
// resolve and install cleanly; a concrete parent is not a resolution error.
func TestInstaller_SubtypeOf_ConcreteParentAccepted(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, Definition{
		Name: "location-domain", Version: "0.1.0",
		DDLs: []DDLSpec{minimalDDL("location", "meta.ddl.vertexType", false)},
	}); err != nil {
		t.Fatalf("install concrete location package: %v", err)
	}
	res, err := inst.Install(ctx, leafPkgDef("unit-domain", "unit", "location"))
	if err != nil {
		t.Fatalf("a concrete parent must resolve and install cleanly: %v", err)
	}
	parentID := EntityNanoIDForTest("location-domain", "ddl:location")
	leafID := EntityNanoIDForTest("unit-domain", "ddl:unit")
	wantKey := "lnk.meta." + leafID + ".subtypeOf.meta." + parentID
	if !declaredKeyPresent(res.DeclaredKeys, wantKey) {
		t.Fatalf("expected subtypeOf link %q against the concrete parent, got %v", wantKey, res.DeclaredKeys)
	}
}

// TestInstaller_SubtypeOf_FailsClosed_NonVertexTypeTarget pins the SHAPE of
// the fail-closed check the ratification correction leaves in place: the
// target must still resolve to a live vertexType meta-vertex. SubtypeOfRef
// names a LENS's canonicalName by (plausible authoring) typo — the class
// check must still catch it even though the abstract requirement is gone.
// The lens canonicalName is deliberately an all-lowercase valid Contract #1
// type segment ("samplelens"), so this test isolates the CLASS check from
// validateAbstractDDLScope's separate type-segment-shape check (item 9),
// which would otherwise catch a camelCase lens name first.
func TestInstaller_SubtypeOf_FailsClosed_NonVertexTypeTarget(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	lensPkg := Definition{
		Name:    "lens-only-pkg",
		Version: "0.1.0",
		Lenses: []LensSpec{{
			CanonicalName: "samplelens",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        "sample-bucket",
			Engine:        "full",
			Spec:          `MATCH (n:sample) RETURN n.key AS key`,
		}},
	}
	if _, err := inst.Install(ctx, lensPkg); err != nil {
		t.Fatalf("install lens-only package: %v", err)
	}
	_, err := inst.Install(ctx, leafPkgDef("unit-domain", "unit", "samplelens"))
	if !errors.Is(err, ErrSubtypeOfRefUnresolved) {
		t.Fatalf("expected ErrSubtypeOfRefUnresolved (ref names a lens, not a vertexType), got %v", err)
	}
}

// TestInstaller_SubtypeOf_FailsClosed_Tombstoned uninstalls the abstract's
// OWNING package (§3.5's "one uninstall hazard, named") and confirms a
// SUBSEQUENT leaf install naming the now-tombstoned abstract fails closed
// rather than resolving to a dead meta-vertex.
func TestInstaller_SubtypeOf_FailsClosed_Tombstoned(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, abstractPkgDef("location-domain", "location")); err != nil {
		t.Fatalf("install abstract package: %v", err)
	}
	if _, err := inst.Uninstall(ctx, "location-domain"); err != nil {
		t.Fatalf("uninstall abstract package: %v", err)
	}
	_, err := inst.Install(ctx, leafPkgDef("unit-domain", "unit", "location"))
	if !errors.Is(err, ErrSubtypeOfRefUnresolved) {
		t.Fatalf("expected ErrSubtypeOfRefUnresolved (ref tombstoned), got %v", err)
	}
}

// TestInstaller_SubtypeOf_CycleRefused declares two abstracts in ONE package,
// each naming the other as SubtypeOfRef — a batch-local 2-cycle, which must
// be refused rather than installed into an undefined leaf set (§3.4).
func TestInstaller_SubtypeOf_CycleRefused(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	a := abstractDDL("typea")
	a.SubtypeOfRef = "typeb"
	b := abstractDDL("typeb")
	b.SubtypeOfRef = "typea"
	def := Definition{Name: "cycle-pkg", Version: "0.1.0", DDLs: []DDLSpec{a, b}}
	_, err := inst.Install(ctx, def)
	if !errors.Is(err, ErrTaxonomyCycle) {
		t.Fatalf("expected ErrTaxonomyCycle, got %v", err)
	}
}

// TestInstaller_SubtypeOf_MultiLevelChainAccepted pins §3.4's transitivity
// rule: a multi-level chain (here 2 hops, concrete leaf -> mid abstract ->
// root abstract) installs cleanly, well within maxTaxonomyDepth.
func TestInstaller_SubtypeOf_MultiLevelChainAccepted(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	t0 := abstractDDL("t0")
	t1 := abstractDDL("t1")
	t1.SubtypeOfRef = "t0"
	t2 := minimalDDL("t2", "meta.ddl.vertexType", false)
	t2.SubtypeOfRef = "t1"
	def := Definition{Name: "chain-pkg", Version: "0.1.0", DDLs: []DDLSpec{t0, t1, t2}}
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("a 2-hop chain must install: %v", err)
	}
}

// TestInstaller_SubtypeOf_DepthBoundExceeded builds a batch-local 4-hop
// abstract chain (t4->t3->t2->t1->t0, exactly maxTaxonomyDepth) and confirms
// it installs, then confirms a SECOND package adding one more hop
// (t5 subtypeOf t4, a 5-hop walk from t5) is refused — the abuse-proofing
// mirroring maxInstanceOfHops's stated rationale.
func TestInstaller_SubtypeOf_DepthBoundExceeded(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	t0 := abstractDDL("t0")
	t1 := abstractDDL("t1")
	t1.SubtypeOfRef = "t0"
	t2 := abstractDDL("t2")
	t2.SubtypeOfRef = "t1"
	t3 := abstractDDL("t3")
	t3.SubtypeOfRef = "t2"
	t4 := abstractDDL("t4")
	t4.SubtypeOfRef = "t3"
	def := Definition{Name: "deep-chain-pkg", Version: "0.1.0", DDLs: []DDLSpec{t0, t1, t2, t3, t4}}
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("a 4-hop chain (at the maxTaxonomyDepth bound) must install: %v", err)
	}

	_, err := inst.Install(ctx, leafPkgDef("too-deep-pkg", "t5", "t4"))
	if !errors.Is(err, ErrTaxonomyCycle) {
		t.Fatalf("expected ErrTaxonomyCycle (depth exceeded by the 5th hop), got %v", err)
	}
}

// TestInstaller_SubtypeOf_LeafBudgetWarning_NeverRejects pins §10.2's
// asymmetry: a leaf install that pushes an abstract's resolved leaf count
// past its declared LeafBudget is WARNED, never rejected — rejecting would
// let one package's lens narrowing veto another package's type declaration.
func TestInstaller_SubtypeOf_LeafBudgetWarning_NeverRejects(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	abstract := abstractDDL("location")
	abstract.LeafBudget = 1
	def := Definition{Name: "location-domain", Version: "0.1.0", DDLs: []DDLSpec{abstract}}
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("install abstract with LeafBudget=1: %v", err)
	}

	// First leaf: exactly at budget (1) — no warning.
	res1, err := inst.Install(ctx, leafPkgDef("unit1-domain", "unit1", "location"))
	if err != nil {
		t.Fatalf("install leaf1: %v", err)
	}
	if len(res1.LeafBudgetWarnings) != 0 {
		t.Errorf("leaf1 (1st of budget 1): expected no warnings, got %v", res1.LeafBudgetWarnings)
	}

	// Second leaf: pushes count to 2 > budget 1 — a warning, but the install
	// must still SUCCEED.
	res2, err := inst.Install(ctx, leafPkgDef("unit2-domain", "unit2", "location"))
	if err != nil {
		t.Fatalf("a LeafBudget overrun must NEVER reject the install: %v", err)
	}
	if len(res2.LeafBudgetWarnings) == 0 {
		t.Fatal("expected a LeafBudget warning on the 2nd leaf pushing the abstract past its budget")
	}
	abstractID := EntityNanoIDForTest("location-domain", "ddl:location")
	warning := res2.LeafBudgetWarnings[0]
	if !containsAll(warning, abstractID, "leaf count 2", "LeafBudget 1") {
		t.Errorf("warning should name the abstract, the count, and the budget; got %q", warning)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
