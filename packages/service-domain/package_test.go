package servicedomain

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

// TestPackage_DDLAndOps pins the single service DDL, its lifecycle commands,
// the three operator-scoped permission grants (RequestService carries none —
// its authorization is the structural service-path cap.svc grant, not a
// standing PermissionSpec), the three op-metas, and — the load-bearing scope
// assertion — that the package declares ZERO lenses (sidesteps the carried
// pkgmgr canonicalName-uniqueness gap and honours the Phase-3 read-path
// deferral).
func TestPackage_DDLAndOps(t *testing.T) {
	if got := len(Package.DDLs); got != 1 {
		t.Fatalf("expected 1 DDL, got %d", got)
	}
	ddl := Package.DDLs[0]
	if ddl.CanonicalName != "service" {
		t.Fatalf("DDL[0] canonicalName = %q, want service", ddl.CanonicalName)
	}
	if ddl.Class != "meta.ddl.vertexType" {
		t.Fatalf("DDL[0] class = %q, want meta.ddl.vertexType", ddl.Class)
	}

	wantCmds := map[string]bool{"CreateServiceTemplate": false, "CreateServiceInstance": false, "RecordServiceOutcome": false, "RequestService": false}
	for _, c := range ddl.PermittedCommands {
		if _, ok := wantCmds[c]; !ok {
			t.Fatalf("unexpected permittedCommand %q", c)
		}
		wantCmds[c] = true
	}
	for c, seen := range wantCmds {
		if !seen {
			t.Fatalf("permittedCommands missing %q (have %v)", c, ddl.PermittedCommands)
		}
	}

	// Every lifecycle op is granted to operator (scope any) and nothing else.
	wantPerms := map[string]bool{"CreateServiceTemplate": false, "CreateServiceInstance": false, "RecordServiceOutcome": false}
	if got := len(Package.Permissions); got != len(wantPerms) {
		t.Fatalf("expected %d permissions, got %d", len(wantPerms), got)
	}
	for _, perm := range Package.Permissions {
		if _, ok := wantPerms[perm.OperationType]; !ok {
			t.Fatalf("unexpected permission for %q", perm.OperationType)
		}
		wantPerms[perm.OperationType] = true
		if perm.Scope != "any" {
			t.Fatalf("%s scope = %q, want any", perm.OperationType, perm.Scope)
		}
		if len(perm.GrantsTo) != 1 || perm.GrantsTo[0] != "operator" {
			t.Fatalf("%s grantsTo = %v, want [operator]", perm.OperationType, perm.GrantsTo)
		}
	}
	for op, seen := range wantPerms {
		if !seen {
			t.Fatalf("missing permission for op %q", op)
		}
	}

	// Op-metas: CreateServiceInstance + RecordServiceOutcome are
	// forOperation-resolvable (14.4's externalTask path binds them);
	// RequestService is forOperation-resolvable AND carries the
	// descriptor-vocabulary aspects (edge-manifest Fire 1); CreateServiceTemplate
	// is install-time admin and declares none.
	wantMetas := map[string]bool{"CreateServiceInstance": false, "RecordServiceOutcome": false, "RequestService": false}
	if got := len(Package.OpMetas); got != len(wantMetas) {
		t.Fatalf("expected %d opMetas, got %d", len(wantMetas), got)
	}
	for _, om := range Package.OpMetas {
		if _, ok := wantMetas[om.OperationType]; !ok {
			t.Fatalf("unexpected opMeta for %q", om.OperationType)
		}
		wantMetas[om.OperationType] = true
	}
	for op, seen := range wantMetas {
		if !seen {
			t.Fatalf("missing opMeta for op %q", op)
		}
	}

	// No lens — the read-path / cap.svc auth plane is Phase-3 deferred, and
	// declaring no lens sidesteps the carried canonicalName-uniqueness gap.
	if got := len(Package.Lenses); got != 0 {
		t.Fatalf("expected 0 lenses, got %d", got)
	}
	// No weaver targets / loom patterns / roles either (14.4 declares those).
	if got := len(Package.WeaverTargets); got != 0 {
		t.Fatalf("expected 0 weaverTargets, got %d", got)
	}
	if got := len(Package.LoomPatterns); got != 0 {
		t.Fatalf("expected 0 loomPatterns, got %d", got)
	}
	if got := len(Package.Roles); got != 0 {
		t.Fatalf("expected 0 roles, got %d", got)
	}
}

// TestPackage_NoScans mirrors the known-key discipline guard the other
// packages enforce: the script must read only by known key.
func TestPackage_NoScans(t *testing.T) {
	src := Package.DDLs[0].Script
	for _, forbidden := range []string{"KVListKeys", "list_keys", "scan(", "keys_with_prefix"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("service script must not reference prefix-scan helper %q", forbidden)
		}
	}
}
