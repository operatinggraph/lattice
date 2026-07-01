package loftspacedomain

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

// TestPackage_DDLs pins the four DDLs: two vertexType DDLs (loftspaceListing
// owning the listing/address ops, loftspaceOwnership owning the management-link
// ops) and two aspectType step-6 gates (listing, address). The aspect DDLs MUST
// be NON-sensitive (they attach to a unit, not an identity — a sensitive aspect
// there would trip step-6's sensitiveAspectScope).
func TestPackage_DDLs(t *testing.T) {
	if got := len(Package.DDLs); got != 4 {
		t.Fatalf("expected 4 DDLs, got %d", got)
	}

	byName := map[string]pkgmgr.DDLSpec{}
	for _, d := range Package.DDLs {
		byName[d.CanonicalName] = d
	}

	vertex, ok := byName["loftspaceListing"]
	if !ok {
		t.Fatal("missing loftspaceListing vertexType DDL")
	}
	if vertex.Class != "meta.ddl.vertexType" {
		t.Fatalf("loftspaceListing class = %q, want meta.ddl.vertexType", vertex.Class)
	}
	wantCmds := map[string]bool{"SetListing": false, "SetUnitAddress": false, "SetListingStatus": false}
	for _, c := range vertex.PermittedCommands {
		if _, ok := wantCmds[c]; !ok {
			t.Fatalf("unexpected loftspaceListing command %q", c)
		}
		wantCmds[c] = true
	}
	for c, seen := range wantCmds {
		if !seen {
			t.Fatalf("loftspaceListing missing command %q (have %v)", c, vertex.PermittedCommands)
		}
	}

	// loftspaceOwnership (vertexType) owns the two management-link ops.
	ownership, ok := byName["loftspaceOwnership"]
	if !ok {
		t.Fatal("missing loftspaceOwnership vertexType DDL")
	}
	if ownership.Class != "meta.ddl.vertexType" {
		t.Fatalf("loftspaceOwnership class = %q, want meta.ddl.vertexType", ownership.Class)
	}
	wantOwnerCmds := map[string]bool{"AssignUnitOwner": false, "RemoveUnitOwner": false}
	if len(ownership.PermittedCommands) != len(wantOwnerCmds) {
		t.Fatalf("loftspaceOwnership permittedCommands = %v, want %v", ownership.PermittedCommands, []string{"AssignUnitOwner", "RemoveUnitOwner"})
	}
	for _, c := range ownership.PermittedCommands {
		if _, ok := wantOwnerCmds[c]; !ok {
			t.Fatalf("unexpected loftspaceOwnership command %q", c)
		}
		wantOwnerCmds[c] = true
	}
	for c, seen := range wantOwnerCmds {
		if !seen {
			t.Fatalf("loftspaceOwnership missing command %q (have %v)", c, ownership.PermittedCommands)
		}
	}

	// The listing aspectType admits two writers (SetListing full upsert +
	// SetListingStatus status-only rewrite); address admits one.
	for name, writers := range map[string][]string{"listing": {"SetListing", "SetListingStatus"}, "address": {"SetUnitAddress"}} {
		asp, ok := byName[name]
		if !ok {
			t.Fatalf("missing %s aspectType DDL", name)
		}
		if asp.Class != "meta.ddl.aspectType" {
			t.Fatalf("%s class = %q, want meta.ddl.aspectType", name, asp.Class)
		}
		if asp.Sensitive {
			t.Fatalf("%s must NOT be sensitive (it attaches to a unit, not an identity)", name)
		}
		want := map[string]bool{}
		for _, w := range writers {
			want[w] = false
		}
		if len(asp.PermittedCommands) != len(want) {
			t.Fatalf("%s permittedCommands = %v, want %v", name, asp.PermittedCommands, writers)
		}
		for _, c := range asp.PermittedCommands {
			if _, ok := want[c]; !ok {
				t.Fatalf("%s unexpected permittedCommand %q (want %v)", name, c, writers)
			}
			want[c] = true
		}
		for c, seen := range want {
			if !seen {
				t.Fatalf("%s missing permittedCommand %q (have %v)", name, c, asp.PermittedCommands)
			}
		}
	}
}

// TestPackage_Permissions pins every op granted to operator (scope any) and
// nothing else, and the location-domain dependency.
func TestPackage_Permissions(t *testing.T) {
	wantPerms := map[string]bool{"SetListing": false, "SetUnitAddress": false, "SetListingStatus": false, "AssignUnitOwner": false, "RemoveUnitOwner": false}
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

	if len(Package.Depends) != 1 || Package.Depends[0] != "location-domain" {
		t.Fatalf("expected Depends [location-domain], got %v", Package.Depends)
	}

	// Three projection lenses (availableListings — the P5 read model for listed
	// units; applicantRoster — the unprotected NATS-KV roster the trusted-tool
	// console reads server-side; applicantRosterRead — the PROTECTED Postgres
	// identity-picker roster, D1.5); no role, weaver target, loom pattern, or
	// op-meta.
	if got := len(Package.Lenses); got != 3 {
		t.Fatalf("expected 3 lenses, got %d", got)
	}
	lensByName := map[string]pkgmgr.LensSpec{}
	for _, l := range Package.Lenses {
		lensByName[l.CanonicalName] = l
	}
	if l, ok := lensByName["availableListings"]; !ok ||
		l.Adapter != "nats-kv" || l.Bucket != LoftspaceListingsBucket {
		t.Fatalf("unexpected availableListings shape: %+v", lensByName["availableListings"])
	}
	if l, ok := lensByName["applicantRoster"]; !ok ||
		l.Adapter != "nats-kv" || l.Bucket != LoftspaceIdentitiesBucket {
		t.Fatalf("unexpected applicantRoster shape: %+v", lensByName["applicantRoster"])
	}
	if l, ok := lensByName["applicantRosterRead"]; !ok ||
		l.Adapter != "postgres" || l.Table != "read_loftspace_identities" || !l.Protected {
		t.Fatalf("unexpected applicantRosterRead shape: %+v", lensByName["applicantRosterRead"])
	}
	if got := len(Package.WeaverTargets); got != 0 {
		t.Fatalf("expected 0 weaverTargets, got %d", got)
	}
	if got := len(Package.LoomPatterns); got != 0 {
		t.Fatalf("expected 0 loomPatterns, got %d", got)
	}
}

// TestPackage_NoScans mirrors the known-key discipline guard: every script must
// read only by known key, never a prefix scan.
func TestPackage_NoScans(t *testing.T) {
	for name, src := range map[string]string{
		"loftspaceListing":   loftspaceListingDDLScript,
		"loftspaceOwnership": loftspaceOwnershipDDLScript,
	} {
		for _, forbidden := range []string{"KVListKeys", "list_keys", "scan(", "keys_with_prefix"} {
			if strings.Contains(src, forbidden) {
				t.Errorf("%s script must not reference prefix-scan helper %q", name, forbidden)
			}
		}
	}
}

// TestPackage_OwnershipScriptGuards pins the ownership script's load-bearing
// invariants: the landlord must be a vtx.identity, the unit a class=location
// vtx.unit, the link the management relation, and the per-pair link read on
// demand (kv.Read) so a re-assign / revive / no-op is deterministic.
func TestPackage_OwnershipScriptGuards(t *testing.T) {
	src := loftspaceOwnershipDDLScript
	for _, want := range []string{
		`vertex_parts(landlord, "landlord", "identity")`, // landlord must be vtx.identity
		`vertex_parts(unit, "unit", "unit")`,             // unit must be vtx.unit
		"UnknownLandlord",                                // alive-identity guard
		"NotAUnit",                                       // class=location guard
		".manages.unit.",                                 // the management link key shape
		"kv.Read(link_key)",                              // on-demand per-pair read
		"make_link_revive_occ",                           // revive-after-remove (CAS)
		"make_link_tombstone",                            // RemoveUnitOwner
	} {
		if !strings.Contains(src, want) {
			t.Errorf("loftspaceOwnership script must reference %q", want)
		}
	}
}

// TestPackage_ScriptGuards pins the load-bearing invariants: the target must be
// a vtx.unit of class=location, the status enum, and the unconditioned-upsert
// idiom (op update, no expectedRevision) so a listing can be re-published.
func TestPackage_ScriptGuards(t *testing.T) {
	src := loftspaceListingDDLScript
	for _, want := range []string{
		`vtx.unit.<NanoID>`, // unit key-shape guard
		"NotAUnit",          // class=location guard
		"require_live_unit",
		"available, pending, leased", // status enum
		`"op": "update"`,             // unconditioned upsert (no CreateOnly)
	} {
		if !strings.Contains(src, want) {
			t.Errorf("loftspaceListing script must reference %q", want)
		}
	}
}
