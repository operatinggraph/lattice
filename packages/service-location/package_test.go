package servicelocation

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

var slOps = []string{
	"WireResidesIn", "UnwireResidesIn",
	"WireWorksAt", "UnwireWorksAt",
	"WireAvailableAt", "UnwireAvailableAt",
	"WireUnavailableAt", "UnwireUnavailableAt",
	"WirePermitsOperation", "UnwirePermitsOperation",
}

// TestPackage_DDLAndOps pins the single serviceLocation DDL, its ten link
// commands, the ten operator-scoped permission grants, the two
// dependencies (location-domain + service-domain), and that the package owns
// NO vertex DDLs of its own and NO roles/weaver/loom/opMetas.
func TestPackage_DDLAndOps(t *testing.T) {
	if got := len(Package.DDLs); got != 1 {
		t.Fatalf("expected 1 DDL, got %d", got)
	}
	ddl := Package.DDLs[0]
	if ddl.CanonicalName != "serviceLocation" {
		t.Fatalf("DDL[0] canonicalName = %q, want serviceLocation", ddl.CanonicalName)
	}
	if ddl.Class != "meta.ddl.vertexType" {
		t.Fatalf("DDL[0] class = %q, want meta.ddl.vertexType", ddl.Class)
	}

	wantCmds := map[string]bool{}
	for _, op := range slOps {
		wantCmds[op] = false
	}
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

	wantPerms := map[string]bool{}
	for _, op := range slOps {
		wantPerms[op] = false
	}
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

	wantDeps := map[string]bool{"location-domain": false, "service-domain": false}
	if got := len(Package.Depends); got != len(wantDeps) {
		t.Fatalf("expected %d dependencies, got %d (%v)", len(wantDeps), got, Package.Depends)
	}
	for _, d := range Package.Depends {
		if _, ok := wantDeps[d]; !ok {
			t.Fatalf("unexpected dependency %q", d)
		}
		wantDeps[d] = true
	}
	for d, seen := range wantDeps {
		if !seen {
			t.Fatalf("missing dependency %q", d)
		}
	}

	// Scheme package: no vertex types, roles, weaver target, loom pattern, or
	// op-meta of its own (it references location-domain + service-domain).
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
}

// TestPackage_Lens pins the capabilityServiceAccess lens: an actorAggregate
// into the shared capability-kv bucket, keyed cap.svc.{actorSuffix}, body
// column serviceAccess, emptyBehavior delete (absence = denial).
func TestPackage_Lens(t *testing.T) {
	if got := len(Package.Lenses); got != 2 {
		t.Fatalf("expected 2 lenses, got %d", got)
	}
	l := Package.Lenses[0]
	if l.CanonicalName != "capabilityServiceAccess" {
		t.Fatalf("lens canonicalName = %q, want capabilityServiceAccess", l.CanonicalName)
	}
	if l.Bucket != "capability-kv" {
		t.Fatalf("lens bucket = %q, want capability-kv", l.Bucket)
	}
	if l.Engine != "full" {
		t.Fatalf("lens engine = %q, want full", l.Engine)
	}
	if l.ProjectionKind != "actorAggregate" {
		t.Fatalf("lens projectionKind = %q, want actorAggregate", l.ProjectionKind)
	}
	if l.Output == nil {
		t.Fatalf("lens must declare an Output descriptor")
	}
	if l.Output.AnchorType != "identity" {
		t.Fatalf("Output.AnchorType = %q, want identity", l.Output.AnchorType)
	}
	if l.Output.OutputKeyPattern != "cap.svc.{actorSuffix}" {
		t.Fatalf("Output.OutputKeyPattern = %q, want cap.svc.{actorSuffix}", l.Output.OutputKeyPattern)
	}
	if l.Output.EmptyBehavior != "delete" {
		t.Fatalf("Output.EmptyBehavior = %q, want delete", l.Output.EmptyBehavior)
	}
	if len(l.Output.BodyColumns) != 1 || l.Output.BodyColumns[0] != "serviceAccess" {
		t.Fatalf("Output.BodyColumns = %v, want [serviceAccess]", l.Output.BodyColumns)
	}
}

// TestPackage_StaffReadGrantsLens pins the cap-read.staff producer's posture.
// Every assertion here is load-bearing for the grant's confinement, so a silent
// edit to any of them is a security regression, not a refactor.
func TestPackage_StaffReadGrantsLens(t *testing.T) {
	var l pkgmgr.LensSpec
	for _, cand := range Package.Lenses {
		if cand.CanonicalName == "staffReadGrants" {
			l = cand
		}
	}
	if l.CanonicalName == "" {
		t.Fatal("staffReadGrants lens not declared")
	}
	if l.Adapter != "postgres" || !l.GrantTable {
		t.Fatalf("staffReadGrants must be a postgres GrantTable lens, got adapter=%q grantTable=%v", l.Adapter, l.GrantTable)
	}
	// The grant is derived from links, not from a vertex whose tombstone could
	// carry the retraction — the target-diff IS the revocation transport, and it
	// only confines to this producer's rows via the declared source.
	if !l.DiffRetraction {
		t.Error("staffReadGrants must declare DiffRetraction: an unwire tombstones the LINK, so no anchor tombstone ever retracts the grant")
	}
	if l.GrantSource != "cap-read.staff" {
		t.Errorf("GrantSource = %q, want cap-read.staff — it scopes retraction to this producer's own rows in the shared actor_read_grants", l.GrantSource)
	}
	if !strings.Contains(l.Spec, "'cap-read.staff'") {
		t.Error("the cypher's projected grant_source must match the declared GrantSource, or the adapter rejects every row")
	}
	// DiffRetraction compares the target's FULL live key set against this query's
	// FULL result set; an $actorKey seed would make the result set a per-actor
	// slice and retract every other staff actor's grants on the first event.
	if strings.Contains(l.Spec, "$actorKey") {
		t.Error("staffReadGrants must be unanchored — a $actorKey seed makes the target-diff retract other actors' grants")
	}
	// Both links required: the grant must not outlive either half.
	for _, want := range []string{"holdsRole", "worksAt", "frontOfHouse", "(b:building)"} {
		if !strings.Contains(l.Spec, want) {
			t.Errorf("staffReadGrants cypher missing %q", want)
		}
	}
	if strings.Contains(l.Spec, "OPTIONAL MATCH") {
		t.Error("neither the role nor the worksAt walk may be OPTIONAL — an optional half would grant on one link alone")
	}
}

// TestPackage_NoScans mirrors the known-key discipline guard the other
// packages enforce: the script must read only by known key.
func TestPackage_NoScans(t *testing.T) {
	src := Package.DDLs[0].Script
	for _, forbidden := range []string{"KVListKeys", "list_keys", "scan(", "keys_with_prefix"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("serviceLocation script must not reference prefix-scan helper %q", forbidden)
		}
	}
}

// TestPackage_ScriptGuards pins the load-bearing endpoint-class guards the wire
// ops enforce: the location-class guard, the service-template guard (.template
// discriminator + the instanceOf-absence the lens relies on is op-validated as
// a template here), the op-meta guard, and the relation names.
func TestPackage_ScriptGuards(t *testing.T) {
	src := Package.DDLs[0].Script
	for _, want := range []string{
		"residesIn", "worksAt", "availableAt", "unavailableAt", "permitsOperation",
		"NotALocation", "NotATemplate", "NotAnOpMeta", "require_live_location",
		"require_live_service_template", "require_live_opmeta", ".template",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("serviceLocation script must reference %q", want)
		}
	}
}

// TestPackage_LensCypher pins the corrected lens cypher invariants: the
// as-built availableAt/unavailableAt directions (service is the inbound side),
// the fresh-var multi-level exclusion existential, and the instanceOf-absence
// template guard.
func TestPackage_LensCypher(t *testing.T) {
	src := Package.Lenses[0].Spec
	for _, want := range []string{
		"$actorKey",
		"residesIn",
		"containedIn*0..",
		"<-[:availableAt]-(svc:service)",         // service is the INBOUND side (not inverted) + :service guard
		"<-[:unavailableAt]-(svc)",               // exclusion, same direction
		"NOT (svc)-[:instanceOf]->",              // template guard (instances carry instanceOf)
		"NOT (loc0)-[:containedIn*0..]->(exLoc)", // per-chain exclusion anchored on the granting residence
		"exLoc",                                  // the fresh exclusion location var
		"operationType <> null",                  // allowedOperations drops ops with no operationType
		"permitsOperation",
		"serviceAccess",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("capabilityServiceAccess cypher must contain %q", want)
		}
	}
	// serviceClass is not projected — the residence scheme has no use for it and
	// it could only carry the bare root class "service" (the rich discriminator
	// is in the .class aspect a cypher cannot reach).
	if strings.Contains(src, "serviceClass") {
		t.Errorf("the residence scheme must not project serviceClass (it can only be the inert bare root class)")
	}
	// The exclusion walks via a fresh exLoc, never the bound availability loc —
	// reusing the matched loc would over-grant (§6.10 item 1).
	if strings.Contains(src, "(loc)<-[:unavailableAt]-(svc)") {
		t.Errorf("exclusion must use a fresh exLoc, not the bound loc — bound-loc over-grants (§6.10 item 1)")
	}
	// The exclusion anchors on the granting residence loc0; it must NOT re-seed
	// from identity across the actor's whole residence set (that over-suppresses —
	// a service unavailableAt one residence wrongly removed from all). residesIn
	// therefore appears exactly once: the positive match.
	if n := strings.Count(src, "residesIn"); n != 1 {
		t.Errorf("exclusion must anchor on loc0 (per residence chain), not re-walk residesIn from identity; residesIn count = %d, want 1", n)
	}
	// worksAt is PURE TOPOLOGY and must never appear in this cypher. residesIn is
	// the authorization-bearing spine — it is the left edge of this join, so
	// everything reachable from it is GRANTED. worksAt exists only to say where a
	// staff actor's world composes from and where their workplace-anchored read
	// grants derive. Wiring it in here would silently mean "working at a building
	// entitles you to every service available there" — an over-grant nobody
	// requested and one that would be invisible at the call site. Staff authority
	// is role grants (cap.roles), never a consequence of employment topology.
	if strings.Contains(src, "worksAt") {
		t.Errorf("capabilityServiceAccess must NOT reference worksAt — the workplace spine is not an authorization input; service access derives from residesIn/availableAt only")
	}
}
