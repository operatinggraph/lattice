package cafeledger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition keeps manifest.yaml and the Go
// Definition in lockstep (the loftspace-ledger/clinic-ledger precedent): the
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

// TestPackage_StructurePins pins what this package declares, by count and by
// canonical name (Vertical Package Standard S6, loftspace-domain/package_test.go
// idiom). A declaration added or dropped without a deliberate edit here reds
// this test rather than reaching an install, where the same change is a silent
// capability or read-model shift.
func TestPackage_StructurePins(t *testing.T) {
	if got, want := len(Package.DDLs), 3; got != want {
		t.Errorf("DDLs: got %d, want %d", got, want)
	}
	if got, want := len(Package.Permissions), 3; got != want {
		t.Errorf("Permissions: got %d, want %d", got, want)
	}
	if got, want := len(Package.Lenses), 2; got != want {
		t.Errorf("Lenses: got %d, want %d", got, want)
	}
	if got, want := len(Package.WeaverTargets), 0; got != want {
		t.Errorf("WeaverTargets: got %d, want %d", got, want)
	}
	if got, want := len(Package.LoomPatterns), 0; got != want {
		t.Errorf("LoomPatterns: got %d, want %d", got, want)
	}
	if got, want := len(Package.OpMetas), 0; got != want {
		t.Errorf("OpMetas: got %d, want %d", got, want)
	}

	wantDDLs := []string{"cafeaccount", "cafeLedgerAccountGuard", "cafetransaction"}
	for i, d := range Package.DDLs {
		if i < len(wantDDLs) && d.CanonicalName != wantDDLs[i] {
			t.Errorf("DDLs[%d]: got %q, want %q", i, d.CanonicalName, wantDDLs[i])
		}
	}

	wantPerms := []struct{ op, scope string }{{"CreateAccount", "any"}, {"DebitAccount", "any"}, {"CreditAccount", "any"}}
	for i, want := range wantPerms {
		if i >= len(Package.Permissions) {
			break
		}
		got := Package.Permissions[i]
		if got.OperationType != want.op || got.Scope != want.scope {
			t.Errorf("Permissions[%d]: got %s/%s, want %s/%s", i, got.OperationType, got.Scope, want.op, want.scope)
		}
	}

	wantLenses := []string{"cafeLedgerHistory", "cafeLeaseAccounts"}
	for i, d := range Package.Lenses {
		if i < len(wantLenses) && d.CanonicalName != wantLenses[i] {
			t.Errorf("Lenses[%d]: got %q, want %q", i, d.CanonicalName, wantLenses[i])
		}
	}
}
