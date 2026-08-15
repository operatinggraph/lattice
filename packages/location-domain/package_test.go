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

// TestPackage_DDLAndOps pins the FOUR-DDL taxonomy (abstract `location` +
// concrete unit/building/property, each a subtypeOf it), the five commands
// every concrete leaf admits, the five operator-scoped permission grants, and
// — the load-bearing scope assertion — that the package declares ZERO lenses /
// roles / weaver / loom (it is a topology-only base domain; the read-path /
// auth plane is SL.2).
func TestPackage_DDLAndOps(t *testing.T) {
	if got := len(Package.DDLs); got != 4 {
		t.Fatalf("expected 4 DDLs (abstract location + 3 concrete leaves), got %d", got)
	}
	byName := map[string]pkgmgr.DDLSpec{}
	for _, d := range Package.DDLs {
		if d.Class != "meta.ddl.vertexType" {
			t.Fatalf("DDL %q class = %q, want meta.ddl.vertexType", d.CanonicalName, d.Class)
		}
		byName[d.CanonicalName] = d
	}

	// The abstract parent names no instance: no script, no permittedCommands.
	abstract, ok := byName["location"]
	if !ok {
		t.Fatalf("no DDL declares the abstract canonicalName location (have %v)", ddlNames())
	}
	if !abstract.Abstract {
		t.Fatalf("the location DDL must declare Abstract: true")
	}
	if abstract.Script != "" {
		t.Fatalf("the abstract location DDL must declare no script")
	}
	if len(abstract.PermittedCommands) != 0 {
		t.Fatalf("the abstract location DDL must declare no permittedCommands, got %v", abstract.PermittedCommands)
	}
	if abstract.SubtypeOfRef != "" {
		t.Fatalf("the abstract location DDL is the taxonomy root and declares no SubtypeOfRef, got %q", abstract.SubtypeOfRef)
	}

	// Each concrete leaf is a subtypeOf location, carries the shared script,
	// and admits all five ops.
	wantOps := []string{"CreateLocation", "TombstoneLocation", "WireContainedIn", "UnwireContainedIn", "SetLocationPresentation"}
	for _, leafName := range LocationTypes {
		leaf, ok := byName[leafName]
		if !ok {
			t.Fatalf("no DDL declares the concrete leaf %q (have %v)", leafName, ddlNames())
		}
		if leaf.Abstract {
			t.Fatalf("leaf %q must not be abstract", leafName)
		}
		if leaf.SubtypeOfRef != "location" {
			t.Fatalf("leaf %q subtypeOfRef = %q, want location", leafName, leaf.SubtypeOfRef)
		}
		if leaf.Script != locationDDLScript {
			t.Fatalf("leaf %q must carry the shared location script", leafName)
		}
		seen := map[string]bool{}
		for _, c := range leaf.PermittedCommands {
			seen[c] = true
		}
		for _, want := range wantOps {
			if !seen[want] {
				t.Fatalf("leaf %q permittedCommands missing %q (have %v)", leafName, want, leaf.PermittedCommands)
			}
		}
		if len(leaf.PermittedCommands) != len(wantOps) {
			t.Fatalf("leaf %q permittedCommands = %v, want exactly %v", leafName, leaf.PermittedCommands, wantOps)
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
	src := locationDDLScript
	for _, forbidden := range []string{"KVListKeys", "list_keys", "scan(", "keys_with_prefix"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("location script must not reference prefix-scan helper %q", forbidden)
		}
	}
}

// TestPackage_ScriptGuardsContainedIn pins the load-bearing invariants the
// containedIn wire-op enforces: the link relation name, the key-type-segment
// location guard, and the direction terms (child source / parent target).
func TestPackage_ScriptGuardsContainedIn(t *testing.T) {
	src := locationDDLScript
	for _, want := range []string{"containedIn", "NotALocation", "LOCATION_TYPES", "key_type_of", "require_live_location"} {
		if !strings.Contains(src, want) {
			t.Errorf("location script must reference %q", want)
		}
	}
	// The endpoint guard is a CONJUNCTION of the key's type segment and the
	// root class, and each conjunct catches what the other cannot: the key
	// segment is the only thing that can say "any location" across the three
	// levels (a location's class is its own key type), while the class is what
	// proves location-domain minted the vertex rather than a foreign package
	// keying under vtx.unit.*. Both must be present.
	for _, want := range []string{"class_of", "LOCATION_CLASSES"} {
		if !strings.Contains(src, want) {
			t.Errorf("location script must reference %q — the endpoint guard checks the key AND the class", want)
		}
	}
	if strings.Contains(src, "LEGACY_LOCATION_CLASS") {
		t.Error("LEGACY_LOCATION_CLASS was retired (dynamic-type-taxonomy-design.md §17.22 — the 25 legacy-classed roots were repaired 2026-08-10); it must not reappear")
	}
	// The admitted class set is exactly the per-type classes: the 2026-08-10
	// repair rewrote every legacy-classed root to its key type, so nothing
	// live can carry the old shared discriminator any more.
	if !strings.Contains(src, `LOCATION_CLASSES = LOCATION_TYPES`) {
		t.Error("the admitted class set must be exactly the per-type classes")
	}
}

// ddlNames lists the declared canonicalNames, for a legible failure message.
func ddlNames() []string {
	out := make([]string, 0, len(Package.DDLs))
	for _, d := range Package.DDLs {
		out = append(out, d.CanonicalName)
	}
	return out
}
