package pkgmgr

import (
	"strings"
	"testing"
)

// TestBuildInstallBatch_AbstractDDL_EmitsDataAbstractNoScriptNoPermittedCommands
// pins dynamic-type-taxonomy-design.md §3.2: an abstract DDL's root document
// carries `data.abstract == true`, and NEITHER a `.script` NOR a
// `.permittedCommands` aspect is emitted — an abstract type names no
// instance, so emitting either (even empty) would make it look concrete to a
// reader of the aspect list alone.
func TestBuildInstallBatch_AbstractDDL_EmitsDataAbstractNoScriptNoPermittedCommands(t *testing.T) {
	def := Definition{
		Name: "location-domain",
		DDLs: []DDLSpec{abstractDDL("location")},
	}
	inst := &Installer{}
	pkgKey := PackageVertexPrefix + EntityNanoIDForTest(def.Name, "package")
	ddlIDs := []string{EntityNanoIDForTest(def.Name, "ddl:location")}
	ops, _, err := inst.buildInstallBatch(def, pkgKey, ddlIDs, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildInstallBatch: %v", err)
	}

	rootKey := metaVertexPrefix + ddlIDs[0]
	root, ok := findOp(ops, rootKey)
	if !ok {
		t.Fatalf("no root vertex emitted at %s", rootKey)
	}
	data, _ := root.Document["data"].(map[string]any)
	if v, _ := data["abstract"].(bool); !v {
		t.Errorf("root data.abstract = %v, want true", data["abstract"])
	}

	if _, ok := findOp(ops, rootKey+".script"); ok {
		t.Errorf("abstract DDL emitted a %s aspect; want none", rootKey+".script")
	}
	if _, ok := findOp(ops, rootKey+".permittedCommands"); ok {
		t.Errorf("abstract DDL emitted a %s aspect; want none", rootKey+".permittedCommands")
	}
	// The description aspect is NOT one of the two exempted, so it must still
	// be present — the exemption is specific to script/permittedCommands, not
	// "every aspect this DDL would otherwise carry".
	if _, ok := findOp(ops, rootKey+".description"); !ok {
		t.Errorf("abstract DDL must still emit %s", rootKey+".description")
	}
}

// TestBuildInstallBatch_AbstractDDL_LeafBudgetEmittedOnlyWhenDeclared pins the
// opt-in shape of §10.2's leafBudget marker: a non-zero LeafBudget lands on
// the root document's data; a zero (undeclared) LeafBudget emits no
// "leafBudget" key at all, leaving the default-8 resolution to whichever
// consumer reads the field later.
func TestBuildInstallBatch_AbstractDDL_LeafBudgetEmittedOnlyWhenDeclared(t *testing.T) {
	withBudget := abstractDDL("location")
	withBudget.LeafBudget = 4
	withoutBudget := abstractDDL("billable")

	def := Definition{Name: "location-domain", DDLs: []DDLSpec{withBudget, withoutBudget}}
	inst := &Installer{}
	pkgKey := PackageVertexPrefix + EntityNanoIDForTest(def.Name, "package")
	ddlIDs := []string{
		EntityNanoIDForTest(def.Name, "ddl:location"),
		EntityNanoIDForTest(def.Name, "ddl:billable"),
	}
	ops, _, err := inst.buildInstallBatch(def, pkgKey, ddlIDs, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildInstallBatch: %v", err)
	}

	withOp, ok := findOp(ops, metaVertexPrefix+ddlIDs[0])
	if !ok {
		t.Fatalf("no root vertex emitted at %s", metaVertexPrefix+ddlIDs[0])
	}
	withData, _ := withOp.Document["data"].(map[string]any)
	if got, _ := withData["leafBudget"].(int); got != 4 {
		t.Errorf("leafBudget = %v, want 4", withData["leafBudget"])
	}

	withoutOp, ok := findOp(ops, metaVertexPrefix+ddlIDs[1])
	if !ok {
		t.Fatalf("no root vertex emitted at %s", metaVertexPrefix+ddlIDs[1])
	}
	withoutData, _ := withoutOp.Document["data"].(map[string]any)
	if _, present := withoutData["leafBudget"]; present {
		t.Errorf("undeclared LeafBudget must emit no leafBudget key, got %v", withoutData["leafBudget"])
	}
}

// TestBuildInstallBatch_ConcreteDDL_RootDataUnchanged pins the invariant an
// ordinary DDL depends on: a DDL that never sets Abstract or LeafBudget emits
// a root document with EMPTY data, regardless of what other DDLs in the same
// package declare.
func TestBuildInstallBatch_ConcreteDDL_RootDataUnchanged(t *testing.T) {
	def := Definition{Name: "p", DDLs: []DDLSpec{minimalDDL("unit", "meta.ddl.vertexType", false)}}
	inst := &Installer{}
	pkgKey := PackageVertexPrefix + EntityNanoIDForTest(def.Name, "package")
	ddlIDs := []string{EntityNanoIDForTest(def.Name, "ddl:unit")}
	ops, _, err := inst.buildInstallBatch(def, pkgKey, ddlIDs, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildInstallBatch: %v", err)
	}
	rootKey := metaVertexPrefix + ddlIDs[0]
	root, ok := findOp(ops, rootKey)
	if !ok {
		t.Fatalf("no root vertex emitted at %s", rootKey)
	}
	data, _ := root.Document["data"].(map[string]any)
	if len(data) != 0 {
		t.Errorf("a concrete DDL's root data must stay empty, got %v", data)
	}
	if _, ok := findOp(ops, rootKey+".script"); !ok {
		t.Errorf("a concrete DDL must still emit %s", rootKey+".script")
	}
	if _, ok := findOp(ops, rootKey+".permittedCommands"); !ok {
		t.Errorf("a concrete DDL must still emit %s", rootKey+".permittedCommands")
	}
}

// TestBuildInstallBatch_SubtypeOfLink pins §3.3's link shape: 6 segments,
// `meta` on both sides, sourced on the LEAF (the later-arriving vertex per
// Contract #1 §1.1) and targeting the abstract — mirroring the
// `forOperation.meta` precedent verbatim in shape (build.go's docLink call).
func TestBuildInstallBatch_SubtypeOfLink(t *testing.T) {
	def := Definition{
		Name: "location-domain",
		DDLs: []DDLSpec{minimalDDL("unit", "meta.ddl.vertexType", false)},
	}
	inst := &Installer{}
	pkgKey := PackageVertexPrefix + EntityNanoIDForTest(def.Name, "package")
	leafID := EntityNanoIDForTest(def.Name, "ddl:unit")
	ddlIDs := []string{leafID}
	abstractID := EntityNanoIDForTest("location-domain", "ddl:location")

	ops, _, err := inst.buildInstallBatch(def, pkgKey, ddlIDs, nil, nil, nil, nil, nil, nil, nil, nil,
		map[int]string{0: abstractID})
	if err != nil {
		t.Fatalf("buildInstallBatch: %v", err)
	}

	wantKey := "lnk.meta." + leafID + ".subtypeOf.meta." + abstractID
	op, ok := findOp(ops, wantKey)
	if !ok {
		t.Fatalf("no subtypeOf link emitted at %s", wantKey)
	}
	if got := op.Document["class"]; got != "subtypeOf" {
		t.Errorf("subtypeOf link class = %v, want \"subtypeOf\"", got)
	}
	if got, _ := op.Document["sourceVertex"].(string); got != metaVertexPrefix+leafID {
		t.Errorf("subtypeOf sourceVertex = %q, want the LEAF (later-arriving, Contract #1 §1.1)", got)
	}
	if got, _ := op.Document["targetVertex"].(string); got != metaVertexPrefix+abstractID {
		t.Errorf("subtypeOf targetVertex = %q, want the abstract", got)
	}
	if !strings.HasPrefix(wantKey, "lnk.meta.") || !strings.Contains(wantKey, ".subtypeOf.meta.") {
		t.Errorf("subtypeOf link key %q must be lnk.meta.<leaf>.subtypeOf.meta.<abstract>", wantKey)
	}
}

// TestBuildInstallBatch_NoSubtypeOfRef_EmitsNoLink is the negative half: a
// DDL with no resolved subtypeAbstractIDs entry emits no link at all.
func TestBuildInstallBatch_NoSubtypeOfRef_EmitsNoLink(t *testing.T) {
	def := Definition{Name: "p", DDLs: []DDLSpec{minimalDDL("unit", "meta.ddl.vertexType", false)}}
	inst := &Installer{}
	pkgKey := PackageVertexPrefix + EntityNanoIDForTest(def.Name, "package")
	ddlIDs := []string{EntityNanoIDForTest(def.Name, "ddl:unit")}
	ops, _, err := inst.buildInstallBatch(def, pkgKey, ddlIDs, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildInstallBatch: %v", err)
	}
	for _, op := range ops {
		if strings.Contains(op.Key, ".subtypeOf.") {
			t.Errorf("no SubtypeOfRef declared; want no subtypeOf link, got %s", op.Key)
		}
	}
}
