package semanticcontracts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition keeps manifest.yaml and the Go
// Definition in lockstep (the loftspace-ledger precedent): the install reads
// the Definition, but the manifest is the human-facing declaration, and a
// drift between the two is a silent install hazard.
func TestPackage_ManifestMatchesDefinition(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	m, err := pkgmgr.ParseManifest(filepath.Join(wd, "manifest.yaml"))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := m.VerifyAgainstDefinition(Package); err != nil {
		t.Fatalf("manifest <-> Definition drift: %v", err)
	}
}

// TestPackage_StructurePins pins every declared element by count and canonical
// name (Vertical Package Standard S6, loftspace-domain/package_test.go idiom). A
// declaration added or dropped without a deliberate edit here reds this test
// rather than reaching an install, where the same change is a silent capability
// or read-model shift.
func TestPackage_StructurePins(t *testing.T) {
	if got, want := len(Package.DDLs), 5; got != want {
		t.Errorf("DDLs: got %d, want %d", got, want)
	}
	if got, want := len(Package.Lenses), 1; got != want {
		t.Errorf("Lenses: got %d, want %d", got, want)
	}
	if got, want := len(Package.Permissions), 3; got != want {
		t.Errorf("Permissions: got %d, want %d", got, want)
	}
	if got, want := len(Package.OpMetas), 3; got != want {
		t.Errorf("OpMetas: got %d, want %d", got, want)
	}
	if got, want := len(Package.Roles), 0; got != want {
		t.Errorf("Roles: got %d, want %d", got, want)
	}
	if got, want := len(Package.WeaverTargets), 1; got != want {
		t.Errorf("WeaverTargets: got %d, want %d", got, want)
	}
	if got, want := len(Package.LoomPatterns), 0; got != want {
		t.Errorf("LoomPatterns: got %d, want %d — proration computes in Starlark bignum arithmetic, with no Weaver runtime and no rounding UDF", got, want)
	}
	wantDeps := []string{"lease-signing", "loftspace-ledger"}
	if len(Package.Depends) != len(wantDeps) {
		t.Errorf("Depends: got %v, want %v", Package.Depends, wantDeps)
	}
	for i, want := range wantDeps {
		if i < len(Package.Depends) && Package.Depends[i] != want {
			t.Errorf("Depends[%d]: got %q, want %q", i, Package.Depends[i], want)
		}
	}

	wantDDLs := []struct{ name, class string }{
		{"clause", "meta.ddl.vertexType"},
		{"clauseProse", "meta.ddl.aspectType"},
		{"clauseTerms", "meta.ddl.aspectType"},
		{"clauseStatus", "meta.ddl.aspectType"},
		{"clauseInspection", "meta.ddl.aspectType"},
	}
	for i, want := range wantDDLs {
		if i >= len(Package.DDLs) {
			break
		}
		got := Package.DDLs[i]
		if got.CanonicalName != want.name || got.Class != want.class {
			t.Errorf("DDLs[%d]: got %s/%s, want %s/%s", i, got.CanonicalName, got.Class, want.name, want.class)
		}
	}
	if got := Package.Lenses[0].CanonicalName; got != "clauseSatisfaction" {
		t.Errorf("Lenses[0]: got %q, want %q", got, "clauseSatisfaction")
	}
	wantPerms := []struct{ op, scope string }{
		{"CreateClause", "any"}, {"InspectPremises", "any"}, {"SupersedeClause", "any"},
	}
	for i, want := range wantPerms {
		if i >= len(Package.Permissions) {
			break
		}
		got := Package.Permissions[i]
		if got.OperationType != want.op || got.Scope != want.scope {
			t.Errorf("Permissions[%d]: got %s/%s, want %s/%s", i, got.OperationType, got.Scope, want.op, want.scope)
		}
	}
}
