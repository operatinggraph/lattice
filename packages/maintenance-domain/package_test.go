package maintenancedomain

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition keeps manifest.yaml and the Go
// Definition in lockstep (the wellness-domain / cafe-domain precedent): the
// install reads the Definition, but the manifest is the human-facing
// declaration, and a drift between the two is a silent install hazard.
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

// TestOpMetas_DispatchClassMatchesOwningDDL mirrors clinic-domain /
// wellness-domain's guard of the same name: Dispatch.Class must be the owning
// vertexType DDL's CanonicalName (the Contract #2 §2.1 envelope `class`
// DDL-hint a real client submission uses), never the vertical name.
func TestOpMetas_DispatchClassMatchesOwningDDL(t *testing.T) {
	classForOp := map[string]string{}
	for _, d := range DDLs() {
		if d.Class != "meta.ddl.vertexType" {
			continue
		}
		for _, op := range d.PermittedCommands {
			classForOp[op] = d.CanonicalName
		}
	}
	for _, m := range OpMetas() {
		if m.Dispatch == nil {
			continue
		}
		want := classForOp[m.OperationType]
		if want == "" {
			t.Fatalf("%s: no owning vertexType DDL found in PermittedCommands", m.OperationType)
		}
		if m.Dispatch.Class != want {
			t.Errorf("%s: Dispatch.Class = %q, want %q (owning DDL's CanonicalName)", m.OperationType, m.Dispatch.Class, want)
		}
	}
}

// TestResolveWorkOrder_HasNoStandingStaffGrant pins the design decision that
// looks like an omission: the maintenance tech reaches ResolveWorkOrder only
// through the ephemeral grant of the task queued to their role, never a
// standing role grant. Granting `backOfHouse` here would hand every holder
// every work order in the building and make the claim ceremony decorative —
// so a future fire that "fixes" the missing grant has to delete this test and
// read why first.
func TestResolveWorkOrder_HasNoStandingStaffGrant(t *testing.T) {
	for _, p := range Permissions() {
		if p.OperationType != "ResolveWorkOrder" {
			continue
		}
		for _, role := range p.GrantsTo {
			if role != "operator" {
				t.Errorf("ResolveWorkOrder grants %q — it must be operator-only; the performer reaches it via the task's ephemeral grant (facet-staff-worlds-design.md §6 F5)", role)
			}
		}
	}
}

// TestReportIssue_GrantedToBothStaffRoles pins the other half: reporting is
// standing staff work, and it is confined by the script's workplace guard
// rather than by withholding the grant.
func TestReportIssue_GrantedToBothStaffRoles(t *testing.T) {
	want := map[string]bool{"operator": false, "frontOfHouse": false, "backOfHouse": false}
	for _, p := range Permissions() {
		if p.OperationType != "ReportIssue" {
			continue
		}
		for _, role := range p.GrantsTo {
			if _, ok := want[role]; !ok {
				t.Errorf("ReportIssue grants unexpected role %q", role)
				continue
			}
			want[role] = true
		}
	}
	for role, seen := range want {
		if !seen {
			t.Errorf("ReportIssue does not grant %q", role)
		}
	}
}

// TestPackage_StructurePins pins every declared element by count and canonical
// name (Vertical Package Standard S6, loftspace-domain/package_test.go idiom). A
// declaration added or dropped without a deliberate edit here reds this test
// rather than reaching an install, where the same change is a silent capability
// or read-model shift.
//
// This package is the canonical source for the five guard idioms (S4), so its
// shape is copied by every other package's author — a drift here propagates by
// imitation, which is exactly why it is pinned.
func TestPackage_StructurePins(t *testing.T) {
	if got, want := len(Package.DDLs), 3; got != want {
		t.Errorf("DDLs: got %d, want %d", got, want)
	}
	if got, want := len(Package.Lenses), 0; got != want {
		t.Errorf("Lenses: got %d, want %d — this package projects nothing; its work orders are read through the verticals", got, want)
	}
	if got, want := len(Package.Permissions), 2; got != want {
		t.Errorf("Permissions: got %d, want %d", got, want)
	}
	if got, want := len(Package.OpMetas), 2; got != want {
		t.Errorf("OpMetas: got %d, want %d", got, want)
	}
	if got, want := len(Package.Roles), 0; got != want {
		t.Errorf("Roles: got %d, want %d", got, want)
	}
	if got, want := len(Package.WeaverTargets), 0; got != want {
		t.Errorf("WeaverTargets: got %d, want %d", got, want)
	}
	if got, want := len(Package.LoomPatterns), 0; got != want {
		t.Errorf("LoomPatterns: got %d, want %d", got, want)
	}
	if len(Package.Depends) != 1 || Package.Depends[0] != "location-domain" {
		t.Errorf("Depends: got %v, want [location-domain]", Package.Depends)
	}

	wantDDLs := []struct{ name, class string }{
		{"workOrder", "meta.ddl.vertexType"},
		{"workOrderReport", "meta.ddl.aspectType"},
		{"workOrderResolution", "meta.ddl.aspectType"},
	}
	for i, want := range wantDDLs {
		if i >= len(Package.DDLs) {
			break
		}
		got := Package.DDLs[i]
		if got.CanonicalName != want.name || got.Class != want.class {
			t.Errorf("DDLs[%d]: got %s/%s, want %s/%s", i, got.CanonicalName, got.Class, want.name, want.class)
		}
	}
	wantPerms := []struct{ op, scope string }{{"ReportIssue", "any"}, {"ResolveWorkOrder", "any"}}
	for i, want := range wantPerms {
		if i >= len(Package.Permissions) {
			break
		}
		got := Package.Permissions[i]
		if got.OperationType != want.op || got.Scope != want.scope {
			t.Errorf("Permissions[%d]: got %s/%s, want %s/%s", i, got.OperationType, got.Scope, want.op, want.scope)
		}
	}
}
