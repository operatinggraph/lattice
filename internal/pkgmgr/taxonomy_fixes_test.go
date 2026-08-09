package pkgmgr

import (
	"errors"
	"strings"
	"testing"
)

// Three more distinct valid Contract #1 NanoIDs for the direct
// scanInstalledSubtypeOfEdgesFromKeys unit test below, drawn from the same
// Alphabet (no I/l/O/0) as the other test NanoID constants in this package.
const (
	testTaxonomyNanoID1 = "Bb2Cc3Dd4Ee5Ff6Gg7Hh"
	testTaxonomyNanoID2 = "Jj8Kk9Mm1Nn2Pp3Qq4Rr"
	testTaxonomyNanoID3 = "Ss5Tt6Uu7Vv8Ww9Xx1Yy"
)

// TestInstaller_CanonicalNameCollision_ShadowKeyShape restores the pre-fire
// collision coverage for a meta-vertex seeded at the shadow-key shape
// (`vtx.meta.<canonicalName>`, a non-NanoID root — the same convention
// ddl_cache.go's own Refresh honors for test fixtures seeded directly via
// KVPut rather than through a real package install): a package declaring a
// DDL with the SAME canonicalName must still be rejected as a collision.
func TestInstaller_CanonicalNameCollision_ShadowKeyShape(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	const root = "vtx.meta.widget"
	if _, err := conn.KVPut(ctx, CoreBucket, root,
		[]byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed shadow-key root: %v", err)
	}
	if _, err := conn.KVPut(ctx, CoreBucket, root+".canonicalName",
		[]byte(`{"class":"canonicalName","isDeleted":false,"data":{"value":"widget"}}`)); err != nil {
		t.Fatalf("seed shadow-key canonicalName aspect: %v", err)
	}

	def := Definition{Name: "widget-domain", Version: "0.1.0", DDLs: []DDLSpec{minimalDDL("widget", "meta.ddl.vertexType", false)}}
	_, err := inst.Install(ctx, def)
	if err == nil {
		t.Fatal("expected ErrCanonicalNameCollision against the shadow-key-seeded \"widget\", got nil")
	}
	if !errors.Is(err, ErrCanonicalNameCollision) {
		t.Fatalf("expected ErrCanonicalNameCollision, got %v", err)
	}
}

// TestInstaller_SubtypeOf_Reparent_LegalUpgradeNotFalseCycle proves the
// re-parenting fix: v1 declares {abstract typea; typeb subtypeOf typea}; v2
// of the SAME package inverts the relationship — {typea subtypeOf typeb;
// typeb no parent}. The committed graph after v2 is acyclic (typea -> typeb,
// nothing else), but resolving it requires excluding the package's OWN
// stale installed edge (typeb -> typea) from the merge, or the walk sees
// both directions and reports a false cycle.
func TestInstaller_SubtypeOf_Reparent_LegalUpgradeNotFalseCycle(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	typeaV1 := abstractDDL("typea")
	typebV1 := minimalDDL("typeb", "meta.ddl.vertexType", false)
	typebV1.SubtypeOfRef = "typea"
	v1 := Definition{Name: "reparent-pkg", Version: "0.1.0", DDLs: []DDLSpec{typeaV1, typebV1}}
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("install v1: %v", err)
	}

	typeaV2 := abstractDDL("typea")
	typeaV2.SubtypeOfRef = "typeb"
	typebV2 := minimalDDL("typeb", "meta.ddl.vertexType", false) // no longer declares a parent
	v2 := Definition{Name: "reparent-pkg", Version: "0.2.0", DDLs: []DDLSpec{typeaV2, typebV2}}

	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("a legal re-parenting upgrade of the package's OWN taxonomy must not be rejected as a false cycle: %v", err)
	}
	if res.Created == 0 {
		t.Errorf("expected the new typea->typeb edge to be created, got Created=%d", res.Created)
	}
	if res.Tombstoned == 0 {
		t.Errorf("expected the stale typeb->typea edge to be tombstoned, got Tombstoned=%d", res.Tombstoned)
	}
}

// TestInstaller_SubtypeOf_MergedGraphCycle_CrossPackage is the merged-graph
// cycle test: today's cycle coverage only crosses the merged graph via the
// DEPTH bound (TestInstaller_SubtypeOf_DepthBoundExceeded); this proves a
// genuine 2-node CYCLE spanning an already-installed cross-package edge plus
// a brand-new one is also refused. typeb-pkg declares typeb subtypeOf typea
// (installed); typea-pkg is then upgraded to declare typea subtypeOf typeb —
// closing typea -> typeb -> typea. typeb-pkg's edge is NOT this package's own
// (it belongs to a different package), so it is not excluded from the merge.
func TestInstaller_SubtypeOf_MergedGraphCycle_CrossPackage(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	if _, err := inst.Install(ctx, abstractPkgDef("typea-pkg", "typea")); err != nil {
		t.Fatalf("install typea package: %v", err)
	}
	if _, err := inst.Install(ctx, leafPkgDef("typeb-pkg", "typeb", "typea")); err != nil {
		t.Fatalf("install typeb package (typeb subtypeOf typea): %v", err)
	}

	typeaV2 := abstractDDL("typea")
	typeaV2.SubtypeOfRef = "typeb"
	v2 := Definition{Name: "typea-pkg", Version: "0.2.0", DDLs: []DDLSpec{typeaV2}}
	_, err := inst.Upgrade(ctx, v2)
	if !errors.Is(err, ErrTaxonomyCycle) {
		t.Fatalf("expected ErrTaxonomyCycle spanning the installed edge (typeb-pkg, not excluded) + this upgrade's new edge, got %v", err)
	}
}

// TestInstaller_SubtypeOf_LeafBudget_SamePackageReapply_NoDoubleCount proves
// re-applying an UNCHANGED definition (same abstract + same single leaf,
// only the package version bumped) reports ZERO warnings. Two distinct fresh
// packages (as TestInstaller_SubtypeOf_LeafBudgetWarning_NeverRejects uses)
// cannot expose a double-count, because that scenario intentionally pushes
// the count over budget with a genuinely NEW leaf each time — this one holds
// the leaf set fixed and only re-declares it.
func TestInstaller_SubtypeOf_LeafBudget_SamePackageReapply_NoDoubleCount(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	abstract := abstractDDL("location")
	abstract.LeafBudget = 1
	leaf := minimalDDL("unit", "meta.ddl.vertexType", false)
	leaf.SubtypeOfRef = "location"
	def := Definition{Name: "reapply-pkg", Version: "0.1.0", DDLs: []DDLSpec{abstract, leaf}}

	res1, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(res1.LeafBudgetWarnings) != 0 {
		t.Fatalf("first install (1 leaf, budget 1): expected no warnings, got %v", res1.LeafBudgetWarnings)
	}

	def2 := def
	def2.Version = "0.2.0"
	res2, err := inst.Upgrade(ctx, def2)
	if err != nil {
		t.Fatalf("upgrade (unchanged taxonomy, version bump only): %v", err)
	}
	if len(res2.LeafBudgetWarnings) != 0 {
		t.Fatalf("an unchanged re-apply must not double-count its own already-installed edge as both existing and new; got warnings %v", res2.LeafBudgetWarnings)
	}
}

// TestInstaller_Abstract_RefusedWhenLiveInstancesExist pins the
// install-time half of the frozen-instance guard: an Upgrade that would flip
// a live concrete type to Abstract is refused with a clear error naming the
// type and an example offending key, rather than landing and leaving every
// existing instance permanently unwritable (create/update/tombstone all
// rejected by the two step-6 gates — see step6_validate.go's tombstone
// exemption for the other half of this guard).
func TestInstaller_Abstract_RefusedWhenLiveInstancesExist(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	def := Definition{Name: "unit-domain", Version: "0.1.0", DDLs: []DDLSpec{minimalDDL("unit", "meta.ddl.vertexType", false)}}
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("install: %v", err)
	}

	unitKey := "vtx.unit." + testTaxonomyNanoID1
	if _, err := conn.KVPut(ctx, CoreBucket, unitKey, []byte(`{"class":"unit","isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed live unit instance: %v", err)
	}

	v2 := Definition{Name: "unit-domain", Version: "0.2.0", DDLs: []DDLSpec{abstractDDL("unit")}}
	_, err := inst.Upgrade(ctx, v2)
	if err == nil {
		t.Fatal("expected the upgrade to be refused: a live instance of \"unit\" already exists")
	}
	if !strings.Contains(err.Error(), "unit") || !strings.Contains(err.Error(), unitKey) {
		t.Errorf("error should name the type and an example offending key; got %v", err)
	}
}

// TestInstaller_Abstract_AllowedWhenPriorInstanceTombstoned is the positive
// vector beside the refusal above: a type whose only instance was already
// tombstoned has no LIVE instance, so declaring it Abstract must succeed.
func TestInstaller_Abstract_AllowedWhenPriorInstanceTombstoned(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	def := Definition{Name: "unit-domain2", Version: "0.1.0", DDLs: []DDLSpec{minimalDDL("unit", "meta.ddl.vertexType", false)}}
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("install: %v", err)
	}

	unitKey := "vtx.unit." + testTaxonomyNanoID2
	if _, err := conn.KVPut(ctx, CoreBucket, unitKey, []byte(`{"class":"unit","isDeleted":true,"data":{}}`)); err != nil {
		t.Fatalf("seed tombstoned unit instance: %v", err)
	}

	v2 := Definition{Name: "unit-domain2", Version: "0.2.0", DDLs: []DDLSpec{abstractDDL("unit")}}
	if _, err := inst.Upgrade(ctx, v2); err != nil {
		t.Fatalf("a tombstoned prior instance must not block declaring Abstract: %v", err)
	}
}

// TestScanInstalledSubtypeOfEdgesFromKeys_SkipsTombstoned pins the tombstone
// filter directly against the function, independent of any install flow: a
// live subtypeOf link is returned; a tombstoned one (isDeleted:true) is not.
func TestScanInstalledSubtypeOfEdgesFromKeys_SkipsTombstoned(t *testing.T) {
	ctx, conn, _ := newInstallerHarness(t)

	liveKey := "lnk.meta." + testTaxonomyNanoID1 + ".subtypeOf.meta." + testTaxonomyNanoID2
	deadKey := "lnk.meta." + testTaxonomyNanoID3 + ".subtypeOf.meta." + testTaxonomyNanoID2
	if _, err := conn.KVPut(ctx, CoreBucket, liveKey, []byte(`{"class":"subtypeOf","isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed live edge: %v", err)
	}
	if _, err := conn.KVPut(ctx, CoreBucket, deadKey, []byte(`{"class":"subtypeOf","isDeleted":true,"data":{}}`)); err != nil {
		t.Fatalf("seed tombstoned edge: %v", err)
	}

	keys, err := conn.KVListKeys(ctx, CoreBucket)
	if err != nil {
		t.Fatalf("KVListKeys: %v", err)
	}
	edges, err := scanInstalledSubtypeOfEdgesFromKeys(ctx, conn, keys)
	if err != nil {
		t.Fatalf("scanInstalledSubtypeOfEdgesFromKeys: %v", err)
	}
	if parents, ok := edges[testTaxonomyNanoID1]; !ok || len(parents) != 1 || parents[0] != testTaxonomyNanoID2 {
		t.Errorf("live edge from %s: got %v, want [%s]", testTaxonomyNanoID1, parents, testTaxonomyNanoID2)
	}
	if parents, ok := edges[testTaxonomyNanoID3]; ok {
		t.Errorf("tombstoned edge from %s must be skipped, got %v", testTaxonomyNanoID3, parents)
	}
}
