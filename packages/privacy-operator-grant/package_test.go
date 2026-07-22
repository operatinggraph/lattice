package privacyoperatorgrant

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition confirms the on-disk manifest.yaml
// cross-validates against the exported Definition (the most common package-
// authoring drift bug), before any NATS integration.
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

// TestPackage_GrantsShredToOperator pins the package's whole reason to exist:
// exactly the ShredIdentityKey → operator grant, and nothing more (no DDLs or
// lenses — a grant-only package).
func TestPackage_GrantsShredToOperator(t *testing.T) {
	if len(Package.DDLs) != 0 || len(Package.Lenses) != 0 {
		t.Fatalf("grant-only package must declare no DDLs/lenses; got %d DDLs, %d lenses",
			len(Package.DDLs), len(Package.Lenses))
	}
	if len(Package.Permissions) != 1 {
		t.Fatalf("Permissions = %d, want exactly 1", len(Package.Permissions))
	}
	p := Package.Permissions[0]
	if p.OperationType != "ShredIdentityKey" {
		t.Errorf("OperationType = %q, want ShredIdentityKey", p.OperationType)
	}
	if p.Scope != "any" {
		t.Errorf("Scope = %q, want any", p.Scope)
	}
	if len(p.GrantsTo) != 1 || p.GrantsTo[0] != "operator" {
		t.Errorf("GrantsTo = %v, want [operator]", p.GrantsTo)
	}
	// Depends on rbac-domain (the operator-role capability projection) and
	// privacy-base (the ShredIdentityKey op it authorizes).
	deps := map[string]bool{}
	for _, d := range Package.Depends {
		deps[d] = true
	}
	if !deps["rbac-domain"] || !deps["privacy-base"] {
		t.Errorf("Depends = %v, want to include rbac-domain and privacy-base", Package.Depends)
	}
}
