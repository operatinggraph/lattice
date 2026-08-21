package loftspacedomain

import (
	"os"
	"path/filepath"
	"slices"
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

// TestPackage_Permissions pins the exact (op, scope) → roles matrix and nothing
// else, and the location-domain dependency. The scope=self row is what makes
// the landlord path reachable, and the ABSENCE of one on AssignUnitOwner /
// RemoveUnitOwner is load-bearing: those ops confer management, so a
// self-scoped grant on either would let any signed-in identity make itself the
// landlord of any unit.
func TestPackage_Permissions(t *testing.T) {
	type grant struct {
		op    string
		scope string
	}
	wantPerms := map[grant][]string{
		{"SetListing", "any"}:        {"operator"},
		{"SetUnitAddress", "any"}:    {"operator"},
		{"SetListingStatus", "any"}:  {"operator"},
		{"AssignUnitOwner", "any"}:   {"operator"},
		{"RemoveUnitOwner", "any"}:   {"operator"},
		{"SetListingStatus", "self"}: {"consumer"},
	}
	if got := len(Package.Permissions); got != len(wantPerms) {
		t.Fatalf("expected %d permissions, got %d", len(wantPerms), got)
	}
	seen := map[grant]bool{}
	for _, perm := range Package.Permissions {
		g := grant{perm.OperationType, perm.Scope}
		want, ok := wantPerms[g]
		if !ok {
			t.Fatalf("unexpected permission %q scope %q", perm.OperationType, perm.Scope)
		}
		if seen[g] {
			t.Fatalf("duplicate permission %q scope %q", perm.OperationType, perm.Scope)
		}
		seen[g] = true
		if len(perm.GrantsTo) != len(want) {
			t.Fatalf("%s/%s grantsTo = %v, want %v", perm.OperationType, perm.Scope, perm.GrantsTo, want)
		}
		for i, role := range want {
			if perm.GrantsTo[i] != role {
				t.Fatalf("%s/%s grantsTo = %v, want %v", perm.OperationType, perm.Scope, perm.GrantsTo, want)
			}
		}
	}
	for g := range wantPerms {
		if !seen[g] {
			t.Fatalf("missing permission for op %q scope %q", g.op, g.scope)
		}
	}

	if len(Package.Depends) != 1 || Package.Depends[0] != "location-domain" {
		t.Fatalf("expected Depends [location-domain], got %v", Package.Depends)
	}

	// Three projection lenses (availableListings — the P5 read model for listed
	// units; applicantRosterRead — the PROTECTED Postgres identity roster,
	// D1.5, and a SECURE LENS: the sensitive identity name decrypts at
	// projection time, so no unprotected roster surface exists; landlordUnitsRead
	// — the PROTECTED, landlord-anchored occupancy model, portfolio-pulse Inc 2);
	// no role, weaver target, or loom pattern; one op-meta (pinned below).
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
	if l, ok := lensByName["applicantRosterRead"]; !ok ||
		l.Adapter != "postgres" || l.Table != "read_loftspace_identities" || !l.Protected {
		t.Fatalf("unexpected applicantRosterRead shape: %+v", lensByName["applicantRosterRead"])
	}
	roster := lensByName["applicantRosterRead"]
	if len(roster.SecureColumns) != 1 ||
		roster.SecureColumns[0].Column != "name" ||
		!slices.Equal(roster.SecureColumns[0].HolderTypes, []string{"identity"}) ||
		roster.SecureColumns[0].Field != "value" {
		t.Fatalf("unexpected applicantRosterRead SecureColumns: %+v", roster.SecureColumns)
	}
	if !strings.Contains(roster.Spec, "i.name.data.ct <> null") {
		t.Fatalf("applicantRosterRead WHERE must key on ciphertext presence (i.name.data.ct), got: %s", roster.Spec)
	}
	if !strings.Contains(roster.Spec, "i.name.data           AS name") {
		t.Fatalf("applicantRosterRead must RETURN the whole name envelope (i.name.data) for the secure decryptor, got: %s", roster.Spec)
	}
	units, ok := lensByName["landlordUnitsRead"]
	if !ok || units.Adapter != "postgres" || units.Table != "read_landlord_units" ||
		!units.Protected || !units.DiffRetraction {
		t.Fatalf("unexpected landlordUnitsRead shape: %+v", units)
	}
	if len(units.IntoKey) != 2 || units.IntoKey[0] != "unit_id" || units.IntoKey[1] != "landlord_id" {
		t.Fatalf("landlordUnitsRead IntoKey = %v, want [unit_id landlord_id]", units.IntoKey)
	}
	if !strings.Contains(units.Spec, "<-[:manages]-(landlord:identity)") {
		t.Fatalf("landlordUnitsRead must walk the manages link from unit to landlord, got: %s", units.Spec)
	}
	if got := len(Package.WeaverTargets); got != 0 {
		t.Fatalf("expected 0 weaverTargets, got %d", got)
	}
	if got := len(Package.LoomPatterns); got != 0 {
		t.Fatalf("expected 0 loomPatterns, got %d", got)
	}
	// One op-meta: SetListingStatus, the package's only user-facing op (the
	// other four are operator-only, which the trusted admin tool dispatches
	// itself). It must stay a FULL descriptor — a bare meta would satisfy the
	// count while leaving the op unrenderable, which is the S1 hole the
	// Standard exists to close.
	if got := len(Package.OpMetas); got != 4 {
		t.Fatalf("expected 4 opMetas, got %d", got)
	}
	meta := Package.OpMetas[0]
	if meta.OperationType != "SetListingStatus" {
		t.Fatalf("opMeta = %s, want SetListingStatus", meta.OperationType)
	}
	if meta.Presentation == nil || meta.Presentation.Title == "" ||
		meta.InputSchema == "" || len(meta.FieldDescriptions) == 0 || meta.Dispatch == nil {
		t.Fatalf("SetListingStatus must carry a FULL descriptor, got %+v", meta)
	}
	// The landlord path is scope=self, so the descriptor must say so — naming
	// "standing" here would send a descriptor-driven client down the
	// operator path and get it refused.
	if meta.Dispatch.AuthContext != "self" {
		t.Fatalf("SetListingStatus authContext = %q, want self", meta.Dispatch.AuthContext)
	}
	if meta.Dispatch.Class != loftspaceListingDDL || meta.Dispatch.TargetType != "unit" {
		t.Fatalf("unexpected SetListingStatus dispatch: %+v", meta.Dispatch)
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
		`parts_of(landlord, "landlord", "identity")`, // landlord must be vtx.identity
		`parts_of(unit, "unit", "unit")`,             // unit must be vtx.unit
		"UnknownLandlord",                            // alive-identity guard
		"NotAUnit",                                   // class=location guard
		".manages.unit.",                             // the management link key shape
		"kv.Read(link_key)",                          // on-demand per-pair read
		"make_link_revive_occ",                       // revive-after-remove (CAS)
		"make_link_tombstone",                        // RemoveUnitOwner
	} {
		if !strings.Contains(src, want) {
			t.Errorf("loftspaceOwnership script must reference %q", want)
		}
	}
}

// TestPackage_OwnershipActorBinding pins the SHAPE of the actor binding, not
// just its presence. The two ops CONFER management, so the guard default-denies
// and the only exemption is the operator ROLE resolved from the graph. Keying
// the exemption on `not op.authTargetValidated` instead would read as "only the
// operator gets here" and be false: the service path never inspects a target,
// and a task grant whose scopedTo vertex was tombstoned projects an empty target
// that matches an empty authContext and authorizes with the bit still false.
// Both call sites must also precede the alive checks, so a caller who manages
// nothing cannot probe for a unit's existence, and the revoke path must be
// self-only off the operator role.
func TestPackage_OwnershipActorBinding(t *testing.T) {
	src := loftspaceOwnershipDDLScript
	for _, want := range []string{
		"if actor_holds_operator(op.actor):",                               // the ONLY exemption
		`kv.Read("lnk.identity." + actor_id + ".manages.unit." + unit_id)`, // op.actor's own link
		"AuthDenied: ", // the denial class
		`enforce_manages(unit_id, "cannot confer management of "`, // AssignUnitOwner
		`enforce_manages(unit_id, "cannot revoke management of "`, // RemoveUnitOwner
		"and landlord != op.actor:",                               // revoke is self-only
	} {
		if !strings.Contains(src, want) {
			t.Errorf("loftspaceOwnership script must reference %q", want)
		}
	}
	for _, forbidden := range []string{"op.authTargetValidated", "op.authContextTarget"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("the ownership probe must not read %s: a validated target proves the target was "+
				"CHECKED, and both the service path and an empty-target task grant authorize with that "+
				"bit false — exempting on it would let either confer management unbound", forbidden)
		}
	}
	assign := strings.Index(src, `enforce_manages(unit_id, "cannot confer management of "`)
	alive := strings.Index(src, "if not vertex_alive(state, landlord):")
	if assign < 0 || alive < 0 || assign > alive {
		t.Errorf("the ownership probe must answer before the alive checks (probe at %d, alive at %d)", assign, alive)
	}
}

// TestPackage_ScriptGuards pins the load-bearing invariants: the target must be
// a vtx.unit of class=location, the status enum, and the unconditioned-upsert
// idiom (op update, no expectedRevision) so a listing can be re-published.
func TestPackage_ScriptGuards(t *testing.T) {
	src := loftspaceListingDDLScript
	for _, want := range []string{
		// The unit key-shape guard, pinned as the CALL rather than as a
		// substring of the message it fails with: parts_of composes that message
		// from want_type, so an error-string assertion would pass just as well
		// with want_type dropped — which is the guard, not the prose.
		`parts_of(unit, "unit", "unit")`,
		"NotAUnit", // class=location guard
		"require_live_unit",
		"available, pending, leased", // status enum
		`"op": "update"`,             // unconditioned upsert (no CreateOnly)
	} {
		if !strings.Contains(src, want) {
			t.Errorf("loftspaceListing script must reference %q", want)
		}
	}
}
