package pkgmgr

import (
	"errors"
	"fmt"
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

// TestInstaller_LeafBudget_CountsTransitiveClosureThroughAbstractMidType
// pins the invariant this check enforces: an abstract's LeafBudget compares
// against the FULL transitive concrete closure the runtime resolver
// (internal/refractor/taxonomy) actually expands a `*` label into, not
// merely its direct children. "apextype" has exactly one direct subtypeOf
// child ("branch", itself abstract with no instances of its own) and four
// concrete leaves two hops down, through branch, so the warning must report
// that transitive count (4) — the number the runtime narrowed filter
// actually has to hold — against the declared budget (3).
func TestInstaller_LeafBudget_CountsTransitiveClosureThroughAbstractMidType(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	root := abstractDDL("apextype") // "root" collides with the bootstrap-seeded primordial meta-meta DDL.
	root.LeafBudget = 3
	branch := abstractDDL("branch")
	branch.SubtypeOfRef = "apextype"
	leaf1 := minimalDDL("leaf1", "meta.ddl.vertexType", false)
	leaf1.SubtypeOfRef = "branch"
	leaf2 := minimalDDL("leaf2", "meta.ddl.vertexType", false)
	leaf2.SubtypeOfRef = "branch"
	leaf3 := minimalDDL("leaf3", "meta.ddl.vertexType", false)
	leaf3.SubtypeOfRef = "branch"
	leaf4 := minimalDDL("leaf4", "meta.ddl.vertexType", false)
	leaf4.SubtypeOfRef = "branch"

	def := Definition{Name: "twolevel-pkg", Version: "0.1.0", DDLs: []DDLSpec{root, branch, leaf1, leaf2, leaf3, leaf4}}
	res, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	rootID := EntityNanoIDForTest("twolevel-pkg", "ddl:apextype")
	var rootWarning string
	for _, w := range res.LeafBudgetWarnings {
		if strings.Contains(w, rootID) {
			rootWarning = w
		}
	}
	if rootWarning == "" {
		t.Fatalf("expected a LeafBudget warning naming root's TRANSITIVE count (4 concrete leaves reached "+
			"through the abstract mid \"branch\") — got %v", res.LeafBudgetWarnings)
	}
	if !containsAll(rootWarning, "leaf count 4", "LeafBudget 3") {
		t.Errorf("warning should report the transitive count (4); got %q", rootWarning)
	}
}

// TestInstaller_LeafBudget_ConcreteRootCountsItself pins the boundary a
// CONCRETE subtypeOf target sits at (amendment A5 — a concrete type may
// have subtypes): the runtime resolver's Expand is reflexive for a concrete
// label, so a concrete target's OWN instances count toward its transitive
// closure exactly like any of its concrete descendants do. "widgetunit" is
// concrete with 8 concrete subtypes — at the default budget of 8 (a
// concrete DDL cannot declare its own LeafBudget, abstractscope.go), the
// count must be 9 (widgetunit itself plus its 8 subtypes), not 8, or the
// exact boundary where the runtime expansion silently exceeds
// maxNarrowedFilterLabels goes unwarned.
func TestInstaller_LeafBudget_ConcreteRootCountsItself(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	root := minimalDDL("widgetunit", "meta.ddl.vertexType", false)
	ddls := []DDLSpec{root}
	for i := 0; i < 8; i++ {
		leaf := minimalDDL(fmt.Sprintf("widgetleaf%d", i), "meta.ddl.vertexType", false)
		leaf.SubtypeOfRef = "widgetunit"
		ddls = append(ddls, leaf)
	}

	def := Definition{Name: "concrete-root-pkg", Version: "0.1.0", DDLs: ddls}
	res, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	rootID := EntityNanoIDForTest("concrete-root-pkg", "ddl:widgetunit")
	var rootWarning string
	for _, w := range res.LeafBudgetWarnings {
		if strings.Contains(w, rootID) {
			rootWarning = w
		}
	}
	if rootWarning == "" {
		t.Fatalf("expected a LeafBudget warning: widgetunit's transitive count is 9 (itself + 8 concrete "+
			"subtypes) against the default budget of 8 — a count that omits the concrete root itself would "+
			"read 8, exactly at budget, and never warn. got %v", res.LeafBudgetWarnings)
	}
	if !containsAll(rootWarning, "leaf count 9", "LeafBudget 8") {
		t.Errorf("warning should report count 9 (root included) against budget 8; got %q", rootWarning)
	}
}

// TestInstaller_LeafBudget_TransitiveCount_IgnoresAbstractMidType pins the
// exclusion at its exact boundary: "root2" has budget 2 and a transitive
// concrete closure of exactly 2 (leaf5, leaf6, reached through the abstract
// mid "branch2"). Counting "branch2" itself — an abstract type has no
// instances, §3.4 — would wrongly push the total to 3 and warn; excluding
// it correctly leaves the total at exactly the budget, which must NOT warn.
func TestInstaller_LeafBudget_TransitiveCount_IgnoresAbstractMidType(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	root := abstractDDL("root2")
	root.LeafBudget = 2
	branch := abstractDDL("branch2")
	branch.SubtypeOfRef = "root2"
	leaf1 := minimalDDL("leaf5", "meta.ddl.vertexType", false)
	leaf1.SubtypeOfRef = "branch2"
	leaf2 := minimalDDL("leaf6", "meta.ddl.vertexType", false)
	leaf2.SubtypeOfRef = "branch2"

	def := Definition{Name: "boundary-pkg", Version: "0.1.0", DDLs: []DDLSpec{root, branch, leaf1, leaf2}}
	res, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	rootID := EntityNanoIDForTest("boundary-pkg", "ddl:root2")
	for _, w := range res.LeafBudgetWarnings {
		if strings.Contains(w, rootID) {
			t.Fatalf("root2's transitive concrete count is exactly 2 (leaf5, leaf6) at budget 2 — must NOT "+
				"warn; counting the abstract mid \"branch2\" itself would wrongly push the total to 3. got %q", w)
		}
	}
}

// TestInstaller_LeafBudget_TransitiveCount_ExternalAbstractMidTypeExcluded
// proves the exclusion above also holds when the abstract mid-type was
// installed by a DIFFERENT, earlier package — the common real shape, since
// leaf installs land incrementally over time. Determining "extbranch" is
// abstract here requires a Core KV read (internal/pkgmgr/taxonomy.go's
// isAbstractMetaVertex), not a batch-local lookup: the package under test
// (the leaves package, which installs the two new edges) never declares
// "extbranch" itself. The leaves package's OWN install result is what is
// asserted on — the ancestor-recheck walk (ancestorsOf) means "extroot"
// is re-validated from THIS install even though this package never names
// "extroot" at all, so no extra forcing package is needed to exercise it.
func TestInstaller_LeafBudget_TransitiveCount_ExternalAbstractMidTypeExcluded(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	rootDef := Definition{Name: "extmid-root-pkg", Version: "0.1.0", DDLs: []DDLSpec{func() DDLSpec {
		d := abstractDDL("extroot")
		d.LeafBudget = 2
		return d
	}()}}
	if _, err := inst.Install(ctx, rootDef); err != nil {
		t.Fatalf("install root: %v", err)
	}

	branchDef := Definition{Name: "extmid-branch-pkg", Version: "0.1.0", DDLs: []DDLSpec{func() DDLSpec {
		d := abstractDDL("extbranch")
		d.SubtypeOfRef = "extroot"
		return d
	}()}}
	if _, err := inst.Install(ctx, branchDef); err != nil {
		t.Fatalf("install branch: %v", err)
	}

	leavesDef := Definition{Name: "extmid-leaves-pkg", Version: "0.1.0", DDLs: []DDLSpec{
		func() DDLSpec {
			d := minimalDDL("extleaf1", "meta.ddl.vertexType", false)
			d.SubtypeOfRef = "extbranch"
			return d
		}(),
		func() DDLSpec {
			d := minimalDDL("extleaf2", "meta.ddl.vertexType", false)
			d.SubtypeOfRef = "extbranch"
			return d
		}(),
	}}
	res, err := inst.Install(ctx, leavesDef)
	if err != nil {
		t.Fatalf("install leaves: %v", err)
	}
	rootID := EntityNanoIDForTest("extmid-root-pkg", "ddl:extroot")
	for _, w := range res.LeafBudgetWarnings {
		if strings.Contains(w, rootID) {
			t.Fatalf("extroot's transitive concrete count is exactly 2 (extleaf1, extleaf2, reached through "+
				"the EXTERNALLY-installed abstract mid \"extbranch\") at budget 2 — must NOT warn; failing to "+
				"read extbranch's abstractness from Core KV would wrongly count it as concrete. got %q", w)
		}
	}
}

// TestInstaller_LeafBudget_AncestorRecheck_CrossPackageIndirectOverrun pins
// finding 4's fix directly: a batch whose OWN newChildrenByTarget never
// names an ancestor at all must still trigger that ancestor's LeafBudget
// recheck when the ancestor's transitive closure grows underneath it.
// "root" (budget 2, package A) has one abstract child "branch" (package B,
// no budget of its own); "branch" gets three NEW concrete leaves in package
// C. Package C's install never references "root" — its own direct target
// is "branch" — but root's transitive count is now 3, over its budget of
// 2, and package C's install result is where that must surface: the
// ancestor walk (ancestorsOf, ranging from "branch" up through the
// already-installed "branch subtypeOf root" edge) is what makes package C's
// install see "root" at all.
func TestInstaller_LeafBudget_AncestorRecheck_CrossPackageIndirectOverrun(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	rootDef := Definition{Name: "ancestor-root-pkg", Version: "0.1.0", DDLs: []DDLSpec{func() DDLSpec {
		d := abstractDDL("ancroot")
		d.LeafBudget = 2
		return d
	}()}}
	if _, err := inst.Install(ctx, rootDef); err != nil {
		t.Fatalf("install root: %v", err)
	}

	branchDef := Definition{Name: "ancestor-branch-pkg", Version: "0.1.0", DDLs: []DDLSpec{func() DDLSpec {
		d := abstractDDL("ancbranch")
		d.SubtypeOfRef = "ancroot"
		return d
	}()}}
	if _, err := inst.Install(ctx, branchDef); err != nil {
		t.Fatalf("install branch: %v", err)
	}

	leavesDef := Definition{Name: "ancestor-leaves-pkg", Version: "0.1.0", DDLs: []DDLSpec{
		func() DDLSpec {
			d := minimalDDL("ancleaf1", "meta.ddl.vertexType", false)
			d.SubtypeOfRef = "ancbranch"
			return d
		}(),
		func() DDLSpec {
			d := minimalDDL("ancleaf2", "meta.ddl.vertexType", false)
			d.SubtypeOfRef = "ancbranch"
			return d
		}(),
		func() DDLSpec {
			d := minimalDDL("ancleaf3", "meta.ddl.vertexType", false)
			d.SubtypeOfRef = "ancbranch"
			return d
		}(),
	}}
	res, err := inst.Install(ctx, leavesDef)
	if err != nil {
		t.Fatalf("install leaves: %v", err)
	}

	rootID := EntityNanoIDForTest("ancestor-root-pkg", "ddl:ancroot")
	var rootWarning string
	for _, w := range res.LeafBudgetWarnings {
		if strings.Contains(w, rootID) {
			rootWarning = w
		}
	}
	if rootWarning == "" {
		t.Fatalf("expected the leaves package's install to surface ancroot's LeafBudget overrun "+
			"(transitive count 3 > budget 2) even though this package never names ancroot at all — "+
			"got %v", res.LeafBudgetWarnings)
	}
	if !containsAll(rootWarning, "leaf count 3", "LeafBudget 2") {
		t.Errorf("warning should report ancroot's transitive count (3) and budget (2); got %q", rootWarning)
	}
}
