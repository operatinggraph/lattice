package maintenancedomain

import (
	"os"
	"path/filepath"
	"strings"
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
// read why first. The scope=any grant is the one read here; the landlord's
// scope=self grant is its own pin below.
func TestResolveWorkOrder_HasNoStandingStaffGrant(t *testing.T) {
	for _, p := range Permissions() {
		if p.OperationType != "ResolveWorkOrder" || p.Scope != "any" {
			continue
		}
		for _, role := range p.GrantsTo {
			if role != "operator" {
				t.Errorf("ResolveWorkOrder grants %q — it must be operator-only; the performer reaches it via the task's ephemeral grant (facet-staff-worlds-design.md §6 F5)", role)
			}
		}
	}
}

// TestResolveWorkOrder_ConsumerSelfGrantIsScopeSelfOnly pins the landlord
// leg's grant shape: consumer reaches ResolveWorkOrder on exactly one grant,
// and it is scope=self — the capability plane then proves the target is the
// caller, and the script's require_manages_unit proves the caller manages
// the order's unit. A consumer on a scope=any grant would be a staff-shaped
// holder with no worksAt link to confine it, and the self leg would never
// run for them.
func TestResolveWorkOrder_ConsumerSelfGrantIsScopeSelfOnly(t *testing.T) {
	selfGrants := 0
	for _, p := range Permissions() {
		if p.OperationType != "ResolveWorkOrder" {
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
				t.Errorf("ResolveWorkOrder scope=self grants %v — the self leg is the consumer's alone", p.GrantsTo)
			}
		case holdsConsumer:
			t.Errorf("ResolveWorkOrder scope=%s grants consumer — the landlord reaches the op on scope=self only, bound to management", p.Scope)
		}
	}
	if selfGrants != 1 {
		t.Fatalf("ResolveWorkOrder declares %d scope=self grants, want exactly 1 (consumer)", selfGrants)
	}
}

// TestOrchestrationOps_OperatorOnly pins the three orchestration-internal
// ops to the operator role on scope=any and nothing else: LinkWorkOrderReporter
// and RecordWorkOrderResolvedNotice are Weaver's directOps, and
// RecordWorkOrderResolvedNotification is the bridge's replyOp. A role grant
// on any of them would offer a human a verb only an engine dispatches.
func TestOrchestrationOps_OperatorOnly(t *testing.T) {
	want := map[string]int{"LinkWorkOrderReporter": 0, "RecordWorkOrderResolvedNotice": 0, "RecordWorkOrderResolvedNotification": 0}
	for _, p := range Permissions() {
		if _, ok := want[p.OperationType]; !ok {
			continue
		}
		want[p.OperationType]++
		if p.Scope != "any" || len(p.GrantsTo) != 1 || p.GrantsTo[0] != "operator" {
			t.Errorf("%s: scope=%s grants %v, want scope=any to operator only", p.OperationType, p.Scope, p.GrantsTo)
		}
	}
	for op, n := range want {
		if n != 1 {
			t.Errorf("%s: %d grants, want exactly 1", op, n)
		}
	}
}

// TestWorkOrderDDL_PermitsEveryOp pins the vertexType DDL's PermittedCommands
// to the five ops the package scripts: an op missing here is dropped by the
// operationType→script index and unroutable, and one owned by a second
// vertexType DDL is dropped the same way.
func TestWorkOrderDDL_PermitsEveryOp(t *testing.T) {
	want := []string{"ReportIssue", "ResolveWorkOrder", "LinkWorkOrderReporter", "RecordWorkOrderResolvedNotice", "RecordWorkOrderResolvedNotification"}
	var got []string
	for _, d := range DDLs() {
		if d.Class == "meta.ddl.vertexType" {
			if d.CanonicalName != "workOrder" {
				t.Errorf("vertexType DDL %q: the package owns exactly one (workOrder)", d.CanonicalName)
			}
			got = d.PermittedCommands
		}
	}
	if len(got) != len(want) {
		t.Fatalf("workOrder PermittedCommands = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("PermittedCommands[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Each aspect-type write gate admits exactly its one writer.
	aspectWriter := map[string]string{
		"workOrderReport":               "ReportIssue",
		"workOrderResolution":           "ResolveWorkOrder",
		"workOrderResolvedNotice":       "RecordWorkOrderResolvedNotice",
		"workOrderResolvedNotification": "RecordWorkOrderResolvedNotification",
	}
	for _, d := range DDLs() {
		if d.Class != "meta.ddl.aspectType" {
			continue
		}
		w, ok := aspectWriter[d.CanonicalName]
		if !ok {
			t.Errorf("aspectType DDL %q is not one this test knows", d.CanonicalName)
			continue
		}
		if len(d.PermittedCommands) != 1 || d.PermittedCommands[0] != w {
			t.Errorf("%s PermittedCommands = %v, want [%s]", d.CanonicalName, d.PermittedCommands, w)
		}
	}
}

// TestReportIssue_ReportedByLinkKeyNamesTheIdentityType pins the link key
// ReportIssue and LinkWorkOrderReporter write to the reporter's Contract #1
// vertex type (identity). The adjacency index derives a walk's far endpoint
// from the link key's type segment and the engine rebuilds vtx.<type>.<id>
// from it, so a key spelled with any other target segment would bind from
// the identity side only and never from the work order's — while every lens
// fixture here, which builds its keys from the vertex type, would still pass
// (semantic-contracts' TestMintClause_GovernsLinkKeyNamesTheLeaseappType
// shape).
func TestReportIssue_ReportedByLinkKeyNamesTheIdentityType(t *testing.T) {
	const want = `"lnk.workorder." + wid + ".reportedBy.identity."`
	if n := strings.Count(workOrderDDLScript, want); n != 2 {
		t.Fatalf("workOrderDDLScript writes %s %d times, want 2 (ReportIssue's mint and LinkWorkOrderReporter's backfill)", want, n)
	}
	if strings.Contains(workOrderDDLScript, `".reportedBy.reporter."`) || strings.Contains(workOrderDDLScript, `".reportedBy.actor."`) {
		t.Fatal("the reportedBy link's target segment must be the identity vertex type")
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
	target := targetByID(t, WorkOrderQueueTarget)
	ga, ok := target.Gaps["missing_task"]
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

func targetByID(t *testing.T, id string) pkgmgr.WeaverTargetSpec {
	t.Helper()
	for _, tg := range WeaverTargets() {
		if tg.TargetID == id {
			return tg
		}
	}
	t.Fatalf("WeaverTargets declares no %s target", id)
	return pkgmgr.WeaverTargetSpec{}
}

// TestWorkOrderQueueTarget_BackfillsTheReporterLink pins the reporter gap's
// dispatch: a directOp LinkWorkOrderReporter on the workOrder DDL whose one
// Param is the anchor's own key and whose Reads are the anchor and its
// .report — the two keys the op reads from hydration. No OptionalReads: the
// link key it mints is data-derived (from the stamp), never declared.
func TestWorkOrderQueueTarget_BackfillsTheReporterLink(t *testing.T) {
	ga, ok := targetByID(t, WorkOrderQueueTarget).Gaps["missing_reporter"]
	if !ok {
		t.Fatal("workOrderQueue declares no missing_reporter gap")
	}
	if ga.Action != "directOp" || ga.Operation != "LinkWorkOrderReporter" || ga.Class != "workOrder" {
		t.Errorf("missing_reporter = %s %s class %q, want directOp LinkWorkOrderReporter class workOrder", ga.Action, ga.Operation, ga.Class)
	}
	if len(ga.Params) != 1 || ga.Params["workOrderKey"] != "row.entityKey" {
		t.Errorf("missing_reporter.Params = %v, want {workOrderKey: row.entityKey}", ga.Params)
	}
	if want := []string{"row.entityKey", "row.entityKey.report"}; !equalStrings(ga.Reads, want) {
		t.Errorf("missing_reporter.Reads = %v, want %v", ga.Reads, want)
	}
	if len(ga.OptionalReads) != 0 {
		t.Errorf("missing_reporter.OptionalReads = %v, want none", ga.OptionalReads)
	}
}

// TestStaleWorkOrderTasksTarget_CancelsThroughOrchestrationBase pins the
// stale-task gap to orchestration-base's own CancelTask{taskKey} on the
// "task" DDL, Reads the anchor alone — lease-signing's staleUserTasks
// dispatch verbatim, so no new op and no new permission.
func TestStaleWorkOrderTasksTarget_CancelsThroughOrchestrationBase(t *testing.T) {
	ga, ok := targetByID(t, StaleWorkOrderTasksTarget).Gaps["missing_cancellation"]
	if !ok {
		t.Fatal("staleWorkOrderTasks declares no missing_cancellation gap")
	}
	if ga.Action != "directOp" || ga.Operation != "CancelTask" || ga.Class != "task" {
		t.Errorf("missing_cancellation = %s %s class %q, want directOp CancelTask class task", ga.Action, ga.Operation, ga.Class)
	}
	if len(ga.Params) != 1 || ga.Params["taskKey"] != "row.entityKey" {
		t.Errorf("missing_cancellation.Params = %v, want {taskKey: row.entityKey}", ga.Params)
	}
	if !equalStrings(ga.Reads, []string{"row.entityKey"}) || len(ga.OptionalReads) != 0 {
		t.Errorf("missing_cancellation reads %v / optionalReads %v, want [row.entityKey] / none", ga.Reads, ga.OptionalReads)
	}
	for _, p := range Permissions() {
		if p.OperationType == "CancelTask" {
			t.Errorf("this package grants CancelTask — it is orchestration-base's, already operator-granted platform-wide")
		}
	}
}

// TestWorkOrderResolvedNoticesTarget_DispatchDeclaresTheOpsReads pins the
// notice gap's dispatch: the changeRef Param is the anchor's own resolvedAt
// (bound non-null by the gap's conjunct), the recipient is never a Param,
// Reads are the anchor, .report and .resolution, and .resolvedNotice is the
// one OptionalRead (absent on the first notice).
func TestWorkOrderResolvedNoticesTarget_DispatchDeclaresTheOpsReads(t *testing.T) {
	ga, ok := targetByID(t, WorkOrderResolvedNoticesTarget).Gaps["missing_resolved_notice"]
	if !ok {
		t.Fatal("workOrderResolvedNotices declares no missing_resolved_notice gap")
	}
	if ga.Action != "directOp" || ga.Operation != "RecordWorkOrderResolvedNotice" || ga.Class != "workOrder" {
		t.Errorf("missing_resolved_notice = %s %s class %q, want directOp RecordWorkOrderResolvedNotice class workOrder", ga.Action, ga.Operation, ga.Class)
	}
	if len(ga.Params) != 2 || ga.Params["workOrderKey"] != "row.entityKey" || ga.Params["changeRef"] != "row.resolvedAt" {
		t.Errorf("missing_resolved_notice.Params = %v, want {workOrderKey: row.entityKey, changeRef: row.resolvedAt}", ga.Params)
	}
	if _, has := ga.Params["reporterKey"]; has {
		t.Error("the reporter rides off the OPTIONAL hop and is read by the op from .report — never a Param")
	}
	if want := []string{"row.entityKey", "row.entityKey.report", "row.entityKey.resolution"}; !equalStrings(ga.Reads, want) {
		t.Errorf("missing_resolved_notice.Reads = %v, want %v", ga.Reads, want)
	}
	if want := []string{"row.entityKey.resolvedNotice"}; !equalStrings(ga.OptionalReads, want) {
		t.Errorf("missing_resolved_notice.OptionalReads = %v, want %v", ga.OptionalReads, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
	if got, want := len(Package.DDLs), 5; got != want {
		t.Errorf("DDLs: got %d, want %d", got, want)
	}
	if got, want := len(Package.Lenses), 4; got != want {
		t.Errorf("Lenses: got %d, want %d — three convergence lenses and the reporter's own read model; a landlord's view of work orders is the vertical's own lens", got, want)
	}
	if got, want := len(Package.Permissions), 7; got != want {
		t.Errorf("Permissions: got %d, want %d", got, want)
	}
	if got, want := len(Package.OpMetas), 4; got != want {
		t.Errorf("OpMetas: got %d, want %d", got, want)
	}
	if got, want := len(Package.Roles), 0; got != want {
		t.Errorf("Roles: got %d, want %d", got, want)
	}
	if got, want := len(Package.WeaverTargets), 3; got != want {
		t.Errorf("WeaverTargets: got %d, want %d", got, want)
	}
	if got, want := len(Package.LoomPatterns), 0; got != want {
		t.Errorf("LoomPatterns: got %d, want %d", got, want)
	}
	if len(Package.Depends) != 2 || Package.Depends[0] != "location-domain" || Package.Depends[1] != "orchestration-base" {
		t.Errorf("Depends: got %v, want [location-domain orchestration-base] — the stale-task target dispatches orchestration-base's CancelTask by its task DDL class", Package.Depends)
	}

	wantDDLs := []struct{ name, class string }{
		{"workOrder", "meta.ddl.vertexType"},
		{"workOrderReport", "meta.ddl.aspectType"},
		{"workOrderResolution", "meta.ddl.aspectType"},
		{"workOrderResolvedNotice", "meta.ddl.aspectType"},
		{"workOrderResolvedNotification", "meta.ddl.aspectType"},
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
	wantLenses := []string{WorkOrderQueueTarget, StaleWorkOrderTasksTarget, WorkOrderResolvedNoticesTarget, ReporterWorkOrdersReadLens}
	for i, want := range wantLenses {
		if i >= len(Package.Lenses) {
			break
		}
		if got := Package.Lenses[i].CanonicalName; got != want {
			t.Errorf("Lenses[%d]: got %s, want %s", i, got, want)
		}
	}
	wantTargets := []string{WorkOrderQueueTarget, StaleWorkOrderTasksTarget, WorkOrderResolvedNoticesTarget}
	for i, want := range wantTargets {
		if i >= len(Package.WeaverTargets) {
			break
		}
		if got := Package.WeaverTargets[i].TargetID; got != want {
			t.Errorf("WeaverTargets[%d]: got %s, want %s", i, got, want)
		}
	}
	wantMetas := []string{"ResolveWorkOrder", "ReportIssue", "RecordWorkOrderResolvedNotice", "RecordWorkOrderResolvedNotification"}
	for i, want := range wantMetas {
		if i >= len(Package.OpMetas) {
			break
		}
		if got := Package.OpMetas[i].OperationType; got != want {
			t.Errorf("OpMetas[%d]: got %s, want %s", i, got, want)
		}
		if i >= 2 && Package.OpMetas[i].Dispatch != nil {
			t.Errorf("OpMetas[%d] %s carries a Dispatch — the notice ops are bare metas, dispatched by their playbook / the bridge, never a form", i, want)
		}
	}
	wantPerms := []struct{ op, scope string }{
		{"ReportIssue", "any"}, {"ReportIssue", "self"}, {"ResolveWorkOrder", "any"}, {"ResolveWorkOrder", "self"},
		{"LinkWorkOrderReporter", "any"}, {"RecordWorkOrderResolvedNotice", "any"}, {"RecordWorkOrderResolvedNotification", "any"},
	}
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
