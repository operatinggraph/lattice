package locationdomain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
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

// TestPackage_DDLAndOps pins the single location DDL, its four commands, the
// four operator-scoped permission grants, and — the load-bearing scope
// assertion — that the package declares ZERO lenses / roles / weaver / loom
// (it is a topology-only base domain; the read-path / auth plane is SL.2).
func TestPackage_DDLAndOps(t *testing.T) {
	if got := len(Package.DDLs); got != 1 {
		t.Fatalf("expected 1 DDL, got %d", got)
	}
	ddl := Package.DDLs[0]
	if ddl.CanonicalName != "location" {
		t.Fatalf("DDL[0] canonicalName = %q, want location", ddl.CanonicalName)
	}
	if ddl.Class != "meta.ddl.vertexType" {
		t.Fatalf("DDL[0] class = %q, want meta.ddl.vertexType", ddl.Class)
	}

	wantCmds := map[string]bool{"CreateLocation": false, "TombstoneLocation": false, "WireContainedIn": false, "UnwireContainedIn": false, "SetLocationPresentation": false}
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

	// Every op is granted to operator (scope any) and nothing else.
	wantPerms := map[string]bool{"CreateLocation": false, "TombstoneLocation": false, "WireContainedIn": false, "UnwireContainedIn": false, "SetLocationPresentation": false}
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

	// Topology-only base domain: no lens, role, weaver target, loom pattern,
	// or op-meta (SL.2's service-location owns the lens + the read path).
	if got := len(Package.Lenses); got != 0 {
		t.Fatalf("expected 0 lenses, got %d", got)
	}
	if got := len(Package.Roles); got != 0 {
		t.Fatalf("expected 0 roles, got %d", got)
	}
	if got := len(Package.WeaverTargets); got != 0 {
		t.Fatalf("expected 0 weaverTargets, got %d", got)
	}
	if got := len(Package.LoomPatterns); got != 0 {
		t.Fatalf("expected 0 loomPatterns, got %d", got)
	}
	if got := len(Package.OpMetas); got != 0 {
		t.Fatalf("expected 0 opMetas, got %d", got)
	}
	if len(Package.Depends) != 0 {
		t.Fatalf("expected no dependencies, got %v", Package.Depends)
	}
}

// TestPackage_NoScans mirrors the known-key discipline guard the other
// packages enforce: the script must read only by known key.
func TestPackage_NoScans(t *testing.T) {
	src := Package.DDLs[0].Script
	for _, forbidden := range []string{"KVListKeys", "list_keys", "scan(", "keys_with_prefix"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("location script must not reference prefix-scan helper %q", forbidden)
		}
	}
}

// TestPackage_ScriptGuardsContainedIn pins the load-bearing invariants the
// containedIn wire-op enforces: the link relation name, the shared location
// class guard, and the direction terms (child source / parent target).
func TestPackage_ScriptGuardsContainedIn(t *testing.T) {
	src := Package.DDLs[0].Script
	for _, want := range []string{"containedIn", "NotALocation", "LOCATION_CLASS", "require_live_location"} {
		if !strings.Contains(src, want) {
			t.Errorf("location script must reference %q", want)
		}
	}
}
