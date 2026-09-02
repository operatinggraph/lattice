package cafedomain

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition keeps manifest.yaml and the Go
// Definition in lockstep (the cafe-ledger/loftspace-ledger precedent): the
// install reads the Definition, but the manifest is the human-facing
// declaration, and a drift between the two is a silent install hazard.
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
//
// The permission list is pinned as (op, scope) PAIRS because a permission IS its
// pair (Contract #8 §8.1): OpenTab/Charge/Settle each carry both a staff
// scope=any grant and a resident scope=self one, and silently losing the self
// row would take self-service away while every count still matched.
func TestPackage_StructurePins(t *testing.T) {
	if got, want := len(Package.DDLs), 5; got != want {
		t.Errorf("DDLs: got %d, want %d", got, want)
	}
	if got, want := len(Package.Lenses), 5; got != want {
		t.Errorf("Lenses: got %d, want %d", got, want)
	}
	if got, want := len(Package.Permissions), 12; got != want {
		t.Errorf("Permissions: got %d, want %d", got, want)
	}
	if got, want := len(Package.OpMetas), 7; got != want {
		t.Errorf("OpMetas: got %d, want %d", got, want)
	}
	if got, want := len(Package.Roles), 0; got != want {
		t.Errorf("Roles: got %d, want %d", got, want)
	}
	if got, want := len(Package.WeaverTargets), 2; got != want {
		t.Errorf("WeaverTargets: got %d, want %d", got, want)
	}
	if got, want := len(Package.LoomPatterns), 0; got != want {
		t.Errorf("LoomPatterns: got %d, want %d", got, want)
	}
	wantDeps := []string{"lease-signing", "cafe-ledger"}
	if len(Package.Depends) != len(wantDeps) {
		t.Errorf("Depends: got %v, want %v", Package.Depends, wantDeps)
	}
	for i, want := range wantDeps {
		if i < len(Package.Depends) && Package.Depends[i] != want {
			t.Errorf("Depends[%d]: got %q, want %q", i, Package.Depends[i], want)
		}
	}

	wantDDLs := []struct{ name, class string }{
		{"tab", "meta.ddl.vertexType"},
		{"tabStatus", "meta.ddl.aspectType"},
		{"cafeOpenTabGuard", "meta.ddl.aspectType"},
		{"menuitem", "meta.ddl.vertexType"},
		{"menuItemPrice", "meta.ddl.aspectType"},
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
	for i, want := range []string{"cafeTabSettlement", "cafeStaleTabSettlement", "menuCatalog", "cafeLeaseWorkplaces"} {
		if i >= len(Package.Lenses) {
			break
		}
		if got := Package.Lenses[i].CanonicalName; got != want {
			t.Errorf("Lenses[%d]: got %q, want %q", i, got, want)
		}
	}
	wantPerms := []struct{ op, scope string }{
		{"OpenTab", "any"}, {"OpenTab", "self"},
		{"Charge", "any"}, {"Charge", "self"},
		{"VoidCharge", "any"},
		{"Settle", "any"}, {"Settle", "self"},
		{"SettleStaleTab", "any"},
		{"BackfillTabStaleAt", "any"},
		{"CreateMenuItem", "any"}, {"RetireMenuItem", "any"}, {"SetMenuItemLocation", "any"},
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

// TestLenses_StaleTabSettlement_ProjectsFreshUntil pins that
// cafeStaleTabSettlement's declared BodyColumns include "freshUntil" —
// staleTabSettlementSpec (lenses.go) computes the column, but Weaver's
// temporal lane (internal/weaver/temporal.go's freshUntilColumn) only ever
// sees a row column the LensSpec declares; a cypher that computes it and a
// BodyColumns list that omits it both compile clean, so nothing but this
// pin catches the two falling out of sync (found live: the tab's own
// staleAt deadline never armed a Weaver @at, and a stale tab settled only
// on an incidental write re-projecting its row).
func TestLenses_StaleTabSettlement_ProjectsFreshUntil(t *testing.T) {
	for _, l := range Lenses() {
		if l.CanonicalName != StaleTabSettlementTarget {
			continue
		}
		if l.Output == nil || !slices.Contains(l.Output.BodyColumns, "freshUntil") {
			t.Fatalf("%s BodyColumns = %v, must include \"freshUntil\"", StaleTabSettlementTarget, l.Output)
		}
		return
	}
	t.Fatalf("no lens named %q in Lenses()", StaleTabSettlementTarget)
}
