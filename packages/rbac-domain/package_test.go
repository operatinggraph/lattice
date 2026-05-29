package rbacdomain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asolgan/lattice/internal/pkgmgr"
)

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

func TestPackage_TenPermittedCommands(t *testing.T) {
	if got := len(Package.DDLs); got != 1 {
		t.Fatalf("expected 1 DDL, got %d", got)
	}
	want := []string{
		"CreateRole", "UpdateRole", "TombstoneRole",
		"CreatePermission", "UpdatePermission", "TombstonePermission",
		"AssignRole", "RevokeRole",
		"GrantPermission", "RevokePermission",
	}
	if got := Package.DDLs[0].PermittedCommands; len(got) != len(want) {
		t.Fatalf("permittedCommands: got %d, want %d", len(got), len(want))
	}
}

func TestPackage_ScriptUsesGrantedByName(t *testing.T) {
	src := Package.DDLs[0].Script
	if !strings.Contains(src, "grantedBy") {
		t.Error("script must construct grantedBy link keys")
	}
	if strings.Contains(src, "grantsPermission") {
		t.Error("script must NOT reference the deprecated grantsPermission name")
	}
}

func TestPackage_ScriptHasNoScans(t *testing.T) {
	src := Package.DDLs[0].Script
	for _, forbidden := range []string{
		"KVListKeys", "list_keys", "scan(", "keys_with_prefix",
	} {
		if strings.Contains(src, forbidden) {
			t.Errorf("script must not reference prefix-scan helper %q", forbidden)
		}
	}
}

func TestPackage_TenPermissions(t *testing.T) {
	if got := len(Package.Permissions); got != 10 {
		t.Fatalf("expected 10 permissions, got %d", got)
	}
	for _, p := range Package.Permissions {
		if len(p.GrantsTo) != 1 || p.GrantsTo[0] != "operator" {
			t.Errorf("permission %s grantsTo=%v, expected [operator]", p.OperationType, p.GrantsTo)
		}
	}
}
