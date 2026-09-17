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
// rather than by withholding the grant. The scope=any grant is the one read
// here; the consumer's self grant is its own pin below.
func TestReportIssue_GrantedToBothStaffRoles(t *testing.T) {
	want := map[string]bool{"operator": false, "frontOfHouse": false, "backOfHouse": false}
	for _, p := range Permissions() {
		if p.OperationType != "ReportIssue" || p.Scope != "any" {
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

// TestReportIssue_ConsumerSelfGrantIsScopeSelfOnly pins the tenant leg's
// grant shape: consumer reaches ReportIssue on exactly one grant, and it is
// scope=self — the capability plane then proves the target is the caller, and
// the script's require_residence proves the caller lives at the unit. A
// consumer on a scope=any grant would be a staff-shaped holder with no worksAt
// link to confine it, and the self leg would never run for them.
func TestReportIssue_ConsumerSelfGrantIsScopeSelfOnly(t *testing.T) {
	selfGrants := 0
	for _, p := range Permissions() {
		if p.OperationType != "ReportIssue" {
			continue
		}
		holdsConsumer := false
		for _, role := range p.GrantsTo {
			if role == "consumer" {
				holdsConsumer = true
			}
		}
		switch {
		case p.Scope == "self":
			selfGrants++
			if !holdsConsumer || len(p.GrantsTo) != 1 {
				t.Errorf("ReportIssue scope=self grants %v — the self leg is the consumer's alone", p.GrantsTo)
			}
		case holdsConsumer:
			t.Errorf("ReportIssue scope=%s grants consumer — the tenant reaches the op on scope=self only, bound to residence", p.Scope)
		}
	}
	if selfGrants != 1 {
		t.Fatalf("ReportIssue declares %d scope=self grants, want exactly 1 (consumer)", selfGrants)
	}
}

// TestWorkOrderQueueTarget_QueuesToBackOfHouse pins the gap's endpoint: the
// missing_task gap is an assignTask on ResolveWorkOrder whose Queue is the
// deterministic backOfHouse role key — the literal the showcase seed computes
// — and whose Target is the anchor's own key. A gap that named an Assignee, or
// a queue derived from the row, would be a different design.
func TestWorkOrderQueueTarget_QueuesToBackOfHouse(t *testing.T) {
	targets := WeaverTargets()
	if len(targets) != 1 || targets[0].TargetID != WorkOrderQueueTarget {
		t.Fatalf("WeaverTargets = %+v, want exactly the %s target", targets, WorkOrderQueueTarget)
	}
	ga, ok := targets[0].Gaps["missing_task"]
	if !ok {
		t.Fatal("workOrderQueue declares no missing_task gap")
	}
	if ga.Action != "assignTask" || ga.Operation != "ResolveWorkOrder" {
		t.Errorf("missing_task = %s %s, want assignTask ResolveWorkOrder", ga.Action, ga.Operation)
	}
	if want := "vtx.role." + pkgmgr.RoleID("identity-domain", "backOfHouse"); ga.Queue != want {
		t.Errorf("missing_task.Queue = %q, want the deterministic backOfHouse role key %q", ga.Queue, want)
	}
	if ga.Assignee != "" {
		t.Errorf("missing_task.Assignee = %q, want empty — the task is queued for the role, not assigned", ga.Assignee)
	}
	if ga.Target != "row.entityKey" {
		t.Errorf("missing_task.Target = %q, want row.entityKey (the anchor work order)", ga.Target)
	}
	if len(ga.Reads) != 0 || len(ga.OptionalReads) != 0 || len(ga.Enumerations) != 0 {
		t.Errorf("missing_task declares reads %v / optionalReads %v / enumerations %v — the queue arm derives its own declared set", ga.Reads, ga.OptionalReads, ga.Enumerations)
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
	if got, want := len(Package.Lenses), 1; got != want {
		t.Errorf("Lenses: got %d, want %d — the workOrderQueue convergence lens; work orders are READ through the verticals' own lenses", got, want)
	}
	if got, want := len(Package.Permissions), 3; got != want {
		t.Errorf("Permissions: got %d, want %d", got, want)
	}
	if got, want := len(Package.OpMetas), 2; got != want {
		t.Errorf("OpMetas: got %d, want %d", got, want)
	}
	if got, want := len(Package.Roles), 0; got != want {
		t.Errorf("Roles: got %d, want %d", got, want)
	}
	if got, want := len(Package.WeaverTargets), 1; got != want {
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
	if len(Package.Lenses) == 1 && Package.Lenses[0].CanonicalName != WorkOrderQueueTarget {
		t.Errorf("Lenses[0]: got %s, want %s", Package.Lenses[0].CanonicalName, WorkOrderQueueTarget)
	}
	if len(Package.WeaverTargets) == 1 && Package.WeaverTargets[0].TargetID != WorkOrderQueueTarget {
		t.Errorf("WeaverTargets[0]: got %s, want %s", Package.WeaverTargets[0].TargetID, WorkOrderQueueTarget)
	}
	wantPerms := []struct{ op, scope string }{{"ReportIssue", "any"}, {"ReportIssue", "self"}, {"ResolveWorkOrder", "any"}}
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
