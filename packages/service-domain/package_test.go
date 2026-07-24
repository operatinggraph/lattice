package servicedomain

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

// TestPackage_DDLAndOps pins the service DDL + its lifecycle/wiring commands,
// the serviceprovider vertexType DDL + its two guard aspect-type DDLs (the
// provider-archetype binding, persona-worlds-design.md Fire W0), the
// permission grants (RequestService carries none — its authorization is the
// structural service-path cap.svc grant, not a standing PermissionSpec), the
// three op-metas, and — the load-bearing scope assertion — that the package
// declares ZERO lenses (sidesteps the carried pkgmgr canonicalName-uniqueness
// gap and honours the Phase-3 read-path deferral).
func TestPackage_DDLAndOps(t *testing.T) {
	if got := len(Package.DDLs); got != 5 {
		t.Fatalf("expected 5 DDLs, got %d", got)
	}
	ddl := Package.DDLs[0]
	if ddl.CanonicalName != "service" {
		t.Fatalf("DDL[0] canonicalName = %q, want service", ddl.CanonicalName)
	}
	if ddl.Class != "meta.ddl.vertexType" {
		t.Fatalf("DDL[0] class = %q, want meta.ddl.vertexType", ddl.Class)
	}

	wantCmds := map[string]bool{"CreateServiceTemplate": false, "CreateServiceInstance": false, "RecordServiceOutcome": false, "RequestService": false, "RetireServiceTemplate": false, "WireProvidedBy": false}
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

	// The serviceprovider vertexType DDL + its two guard aspect-type DDLs.
	wantDDLs := map[string]string{
		"serviceprovider":              "meta.ddl.vertexType",
		"serviceProviderProfile":       "meta.ddl.aspectType",
		"serviceProviderIdentityClaim": "meta.ddl.aspectType",
		"identityServiceProviderClaim": "meta.ddl.aspectType",
	}
	for _, d := range Package.DDLs[1:] {
		want, ok := wantDDLs[d.CanonicalName]
		if !ok {
			t.Fatalf("unexpected DDL canonicalName %q", d.CanonicalName)
		}
		if d.Class != want {
			t.Errorf("%s class = %q, want %q", d.CanonicalName, d.Class, want)
		}
		delete(wantDDLs, d.CanonicalName)
	}
	if len(wantDDLs) != 0 {
		t.Fatalf("missing DDLs: %v", wantDDLs)
	}
	spDDL := Package.DDLs[1]
	wantSPCmds := map[string]bool{"CreateServiceProvider": false, "BindServiceProviderIdentity": false}
	for _, c := range spDDL.PermittedCommands {
		if _, ok := wantSPCmds[c]; !ok {
			t.Fatalf("unexpected serviceprovider permittedCommand %q", c)
		}
		wantSPCmds[c] = true
	}
	for c, seen := range wantSPCmds {
		if !seen {
			t.Fatalf("serviceprovider permittedCommands missing %q", c)
		}
	}

	// Every permission is granted to operator (scope any), plus the
	// provider-archetype widening Fire W0 adds: RecordServiceOutcome widens its
	// EXISTING row to add `provider`. Entity creation and the role-minting bind
	// (CreateServiceProvider / BindServiceProviderIdentity) stay operator-only.
	wantPerms := map[string][]string{
		"CreateServiceTemplate":       {"operator"},
		"CreateServiceInstance":       {"operator"},
		"RecordServiceOutcome":        {"operator", "provider"},
		"RetireServiceTemplate":       {"operator"},
		"CreateServiceProvider":       {"operator"},
		"BindServiceProviderIdentity": {"operator"},
		"WireProvidedBy":              {"operator"},
	}
	if got := len(Package.Permissions); got != len(wantPerms) {
		t.Fatalf("expected %d permissions, got %d", len(wantPerms), got)
	}
	seenPerms := map[string]bool{}
	for _, perm := range Package.Permissions {
		want, ok := wantPerms[perm.OperationType]
		if !ok {
			t.Fatalf("unexpected permission for %q", perm.OperationType)
		}
		seenPerms[perm.OperationType] = true
		if perm.Scope != "any" {
			t.Fatalf("%s scope = %q, want any", perm.OperationType, perm.Scope)
		}
		if len(perm.GrantsTo) != len(want) {
			t.Fatalf("%s grantsTo = %v, want %v", perm.OperationType, perm.GrantsTo, want)
		}
		gotSet := map[string]bool{}
		for _, g := range perm.GrantsTo {
			gotSet[g] = true
		}
		for _, w := range want {
			if !gotSet[w] {
				t.Fatalf("%s grantsTo = %v, missing %q", perm.OperationType, perm.GrantsTo, w)
			}
		}
	}
	for op := range wantPerms {
		if !seenPerms[op] {
			t.Fatalf("missing permission for op %q", op)
		}
	}

	// Op-metas: CreateServiceInstance + RecordServiceOutcome are
	// forOperation-resolvable (14.4's externalTask path binds them);
	// RequestService is forOperation-resolvable AND carries the
	// descriptor-vocabulary aspects (edge-manifest Fire 1); CreateServiceTemplate
	// is install-time admin and declares none. CreateServiceProvider /
	// BindServiceProviderIdentity / WireProvidedBy are staff/operator
	// ceremonies and declare no op-meta either (mirrors clinic-domain's
	// CreateProvider / BindProviderIdentity).
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
// packages enforce: every DDL's script must read only by known key.
func TestPackage_NoScans(t *testing.T) {
	for _, d := range Package.DDLs {
		for _, forbidden := range []string{"KVListKeys", "list_keys", "scan(", "keys_with_prefix"} {
			if strings.Contains(d.Script, forbidden) {
				t.Errorf("%s script must not reference prefix-scan helper %q", d.CanonicalName, forbidden)
			}
		}
	}
}
