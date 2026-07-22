package objectsbase

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

// TestPackage_DDLAndOps pins the single object DDL, its three lifecycle
// commands, the three operator-scoped permission grants, the three op-metas,
// and — the load-bearing scope assertion — that the package declares ZERO
// lenses / weaverTargets / loomPatterns / roles (GC is the v1b increment; the
// type-agnostic graph plane depends on nothing).
func TestPackage_DDLAndOps(t *testing.T) {
	if got := len(Package.DDLs); got != 1 {
		t.Fatalf("expected 1 DDL, got %d", got)
	}
	ddl := Package.DDLs[0]
	if ddl.CanonicalName != "object" {
		t.Fatalf("DDL[0] canonicalName = %q, want object", ddl.CanonicalName)
	}
	if ddl.Class != "meta.ddl.vertexType" {
		t.Fatalf("DDL[0] class = %q, want meta.ddl.vertexType", ddl.Class)
	}

	wantCmds := map[string]bool{"AttachObject": false, "DetachObject": false, "TombstoneObject": false}
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
	wantPerms := map[string]bool{"AttachObject": false, "DetachObject": false, "TombstoneObject": false}
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

	wantMetas := map[string]bool{"AttachObject": false, "DetachObject": false, "TombstoneObject": false}
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

	// Two lenses: the v1b GC detection lens (objectLiveness → Loop A directOp) and
	// the objectAttachments display lens (the vertical apps' P5-clean byte-plane
	// read model). Exactly one weaverTarget (objectLiveness → directOp — the
	// display lens drives no convergence), and NO loom pattern (directOp, not
	// triggerLoom), no roles, no package dependency (type-agnostic).
	if got := len(Package.Lenses); got != 2 {
		t.Fatalf("expected 2 lenses, got %d", got)
	}
	lensNames := map[string]bool{}
	for _, l := range Package.Lenses {
		lensNames[l.CanonicalName] = true
	}
	for _, want := range []string{"objectLiveness", "objectAttachments"} {
		if !lensNames[want] {
			t.Fatalf("missing lens %q (have %v)", want, lensNames)
		}
	}
	if got := len(Package.WeaverTargets); got != 1 {
		t.Fatalf("expected 1 weaverTarget, got %d", got)
	}
	wt := Package.WeaverTargets[0]
	if wt.TargetID != "objectLiveness" || wt.LensRef != "objectLiveness" {
		t.Fatalf("weaverTarget = {%q,%q}, want {objectLiveness,objectLiveness}", wt.TargetID, wt.LensRef)
	}
	ga, ok := wt.Gaps["missing_owner"]
	if !ok {
		t.Fatalf("weaverTarget missing the missing_owner gap (have %v)", wt.Gaps)
	}
	if ga.Action != "directOp" || ga.Operation != "TombstoneObject" {
		t.Fatalf("missing_owner gap = {%q,%q}, want {directOp,TombstoneObject}", ga.Action, ga.Operation)
	}
	if len(ga.Reads) != 1 || ga.Reads[0] != "row.entityKey" {
		t.Fatalf("missing_owner gap reads = %v, want [row.entityKey]", ga.Reads)
	}
	if got := len(Package.LoomPatterns); got != 0 {
		t.Fatalf("expected 0 loomPatterns (directOp, not triggerLoom), got %d", got)
	}
	if got := len(Package.Roles); got != 0 {
		t.Fatalf("expected 0 roles, got %d", got)
	}
	if got := len(Package.Depends); got != 0 {
		t.Fatalf("expected 0 depends, got %d", got)
	}
}

// TestPackage_NoScans mirrors the known-key discipline guard the other packages
// enforce: the script must read only by known key (no prefix scans / adjacency
// / lens-output reads).
func TestPackage_NoScans(t *testing.T) {
	src := Package.DDLs[0].Script
	for _, forbidden := range []string{"KVListKeys", "list_keys", "scan(", "keys_with_prefix", "adjacency"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("object script must not reference prefix-scan / adjacency helper %q", forbidden)
		}
	}
}
