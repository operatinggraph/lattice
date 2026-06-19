package orchestrationbase

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

func TestPackage_DDLsLensesPermissions(t *testing.T) {
	if got := len(Package.DDLs); got != 4 {
		t.Fatalf("expected 4 DDLs, got %d", got)
	}
	ddlNames := map[string]bool{}
	for _, d := range Package.DDLs {
		ddlNames[d.CanonicalName] = true
	}
	for _, want := range []string{"task", "loomLifecycle", "freshnessMarker", "freshnessExpiry"} {
		if !ddlNames[want] {
			t.Fatalf("missing DDL %q (have %v)", want, ddlNames)
		}
	}
	if got := Package.DDLs[0].CanonicalName; got != "task" {
		t.Fatalf("DDL[0] canonicalName = %q, want task", got)
	}
	if got := len(Package.Lenses); got != 2 {
		t.Fatalf("expected 2 lenses, got %d", got)
	}
	lensNames := map[string]bool{}
	for _, l := range Package.Lenses {
		lensNames[l.CanonicalName] = true
	}
	for _, want := range []string{"capabilityEphemeral", "myTasks"} {
		if !lensNames[want] {
			t.Fatalf("missing lens %q (have %v)", want, lensNames)
		}
	}
	if got := len(Package.Permissions); got != 8 {
		t.Fatalf("expected 8 permissions, got %d", got)
	}
}

// TestPackage_MarkExpiredDDL pins the generic freshness-marker DDL (RF#3): a
// vertexType DDL admitting only MarkExpired, an UNCONDITIONED-update marker
// write (no expectedRevision in the script), a companion freshnessExpiry
// aspect-type DDL that admits MarkExpired (the step-6 gate), a non-sensitive
// marker aspect, an operator grant, and NO concrete type literal in the script.
func TestPackage_MarkExpiredDDL(t *testing.T) {
	var marker, aspect *pkgmgr.DDLSpec
	for i := range Package.DDLs {
		switch Package.DDLs[i].CanonicalName {
		case "freshnessMarker":
			marker = &Package.DDLs[i]
		case "freshnessExpiry":
			aspect = &Package.DDLs[i]
		}
	}
	if marker == nil {
		t.Fatal("freshnessMarker DDL missing")
	}
	if aspect == nil {
		t.Fatal("freshnessExpiry aspect DDL missing")
	}

	// The marker is a vertexType DDL admitting exactly MarkExpired.
	if marker.Class != "meta.ddl.vertexType" {
		t.Fatalf("freshnessMarker class = %q, want meta.ddl.vertexType", marker.Class)
	}
	if len(marker.PermittedCommands) != 1 || marker.PermittedCommands[0] != "MarkExpired" {
		t.Fatalf("freshnessMarker permittedCommands = %v, want [MarkExpired]", marker.PermittedCommands)
	}

	// The marker write must be an UNCONDITIONED update (no expectedRevision) so
	// the eager re-open survives a SECOND lapse (C2). A create, or an
	// expectedRevision, would conflict on the standing marker.
	if !strings.Contains(marker.Script, `"op": "update"`) {
		t.Error("freshnessMarker marker write must be an update")
	}
	if strings.Contains(marker.Script, "expectedRevision") {
		t.Error("freshnessMarker marker write must be UNCONDITIONED (no expectedRevision) — else the 2nd lapse conflicts")
	}
	if strings.Contains(marker.Script, `"op": "create"`) {
		t.Error("freshnessMarker marker write must NOT be a create — it conflicts on the 2nd lapse")
	}

	// Type-agnostic: the script must name no concrete anchor type.
	for _, forbidden := range []string{"leaseapp", "leaseApp", "identity", "service"} {
		if strings.Contains(marker.Script, forbidden) {
			t.Errorf("freshnessMarker script must name no concrete type; found %q", forbidden)
		}
	}

	// The aspect DDL admits MarkExpired (the step-6 gate) and is NOT sensitive
	// (so the marker attaches to any vertex type, not just identities).
	if aspect.Class != "meta.ddl.aspectType" {
		t.Fatalf("freshnessExpiry class = %q, want meta.ddl.aspectType", aspect.Class)
	}
	if len(aspect.PermittedCommands) != 1 || aspect.PermittedCommands[0] != "MarkExpired" {
		t.Fatalf("freshnessExpiry permittedCommands = %v, want [MarkExpired]", aspect.PermittedCommands)
	}
	if aspect.Sensitive {
		t.Error("freshnessExpiry must NOT be sensitive (it attaches to any vertex type)")
	}

	// The operator grant authorizes Weaver's service actor.
	var granted bool
	for _, p := range Package.Permissions {
		if p.OperationType != "MarkExpired" {
			continue
		}
		granted = true
		if p.Scope != "any" {
			t.Fatalf("MarkExpired scope = %q, want any", p.Scope)
		}
		if len(p.GrantsTo) != 1 || p.GrantsTo[0] != "operator" {
			t.Fatalf("MarkExpired grantsTo = %v, want [operator]", p.GrantsTo)
		}
	}
	if !granted {
		t.Fatal("missing MarkExpired permission grant")
	}
}

// TestPackage_LoomLifecycleOps pins the three event-only lifecycle ops
// (Contract #10 §10.9) on the loomLifecycle DDL + their operator grants.
func TestPackage_LoomLifecycleOps(t *testing.T) {
	var lifecycle *pkgmgr.DDLSpec
	for i := range Package.DDLs {
		if Package.DDLs[i].CanonicalName == "loomLifecycle" {
			lifecycle = &Package.DDLs[i]
		}
	}
	if lifecycle == nil {
		t.Fatal("loomLifecycle DDL missing")
	}
	wantCmds := map[string]bool{"StartLoomPattern": false, "CompletePattern": false, "FailPattern": false}
	for _, c := range lifecycle.PermittedCommands {
		if _, ok := wantCmds[c]; !ok {
			t.Fatalf("unexpected loomLifecycle command %q", c)
		}
		wantCmds[c] = true
	}
	for c, seen := range wantCmds {
		if !seen {
			t.Fatalf("loomLifecycle missing command %q", c)
		}
	}
	// Event-only: the script must produce no mutations (empty list) for each branch.
	if strings.Contains(lifecycle.Script, `"op": "create"`) || strings.Contains(lifecycle.Script, `"op": "update"`) {
		t.Error("loomLifecycle ops must be event-only — no mutations")
	}
	wantPerms := map[string]bool{"StartLoomPattern": false, "CompletePattern": false, "FailPattern": false}
	for _, p := range Package.Permissions {
		if _, ok := wantPerms[p.OperationType]; !ok {
			continue
		}
		wantPerms[p.OperationType] = true
		if len(p.GrantsTo) != 1 || p.GrantsTo[0] != "operator" {
			t.Fatalf("%s grantsTo = %v, want [operator]", p.OperationType, p.GrantsTo)
		}
	}
	for op, seen := range wantPerms {
		if !seen {
			t.Fatalf("missing permission for lifecycle op %q", op)
		}
	}
}

func TestPackage_TaskDDLLifecycleCommands(t *testing.T) {
	cmds := Package.DDLs[0].PermittedCommands
	want := map[string]bool{"CreateTask": false, "ReAssignTask": false, "CompleteTask": false, "CancelTask": false}
	for _, c := range cmds {
		if _, ok := want[c]; !ok {
			t.Fatalf("unexpected permittedCommand %q", c)
		}
		want[c] = true
	}
	for c, seen := range want {
		if !seen {
			t.Fatalf("permittedCommands missing %q (have %v)", c, cmds)
		}
	}
}

// TestPackage_LifecycleOpsGrantedToOperator pins the grantee role for every
// lifecycle op (A3/A6).
func TestPackage_LifecycleOpsGrantedToOperator(t *testing.T) {
	want := map[string]bool{"CreateTask": false, "ReAssignTask": false, "CompleteTask": false, "CancelTask": false}
	for _, p := range Package.Permissions {
		if _, ok := want[p.OperationType]; !ok {
			continue // lifecycle ops are checked in TestPackage_LoomLifecycleOps
		}
		want[p.OperationType] = true
		if len(p.GrantsTo) != 1 || p.GrantsTo[0] != "operator" {
			t.Fatalf("%s grantsTo = %v, want [operator]", p.OperationType, p.GrantsTo)
		}
	}
	for op, seen := range want {
		if !seen {
			t.Fatalf("missing permission for op %q", op)
		}
	}
}

// TestPackage_EphemeralLensTargetsCapabilityKV asserts the lens projects to
// the shared primordial capability-kv bucket (disjoint cap.ephemeral.*
// prefix, Contract #6 §6.1) and inherits DEFAULT HARD delete (no deleteMode
// override exists on LensSpec, A3).
func TestPackage_EphemeralLensTargetsCapabilityKV(t *testing.T) {
	l := Package.Lenses[0]
	if l.Bucket != "capability-kv" {
		t.Fatalf("lens bucket = %q, want capability-kv", l.Bucket)
	}
	if l.Adapter != "nats-kv" {
		t.Fatalf("lens adapter = %q, want nats-kv", l.Adapter)
	}
	if l.Engine != "full" {
		t.Fatalf("lens engine = %q, want full", l.Engine)
	}
}

// TestPackage_EphemeralLensIsLinkSourced asserts the cypher walks the links
// (forOperation / scopedTo) and does NOT read the corrected anti-pattern
// fields (task.data.grantedOperationType / task.data.targetKey) — Contract
// #10 §10.1.
func TestPackage_EphemeralLensIsLinkSourced(t *testing.T) {
	spec := Package.Lenses[0].Spec
	for _, want := range []string{"assignedTo", "forOperation", "scopedTo", "reportsTo"} {
		if !strings.Contains(spec, want) {
			t.Errorf("ephemeral lens spec must walk %q", want)
		}
	}
	for _, forbidden := range []string{"grantedOperationType", "targetKey"} {
		if strings.Contains(spec, forbidden) {
			t.Errorf("ephemeral lens spec must NOT read the anti-pattern field %q", forbidden)
		}
	}
}

// TestPackage_TaskScriptNoScans mirrors the known-key discipline guard the
// other packages enforce.
func TestPackage_TaskScriptNoScans(t *testing.T) {
	src := Package.DDLs[0].Script
	for _, forbidden := range []string{"KVListKeys", "list_keys", "scan(", "keys_with_prefix"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("task script must not reference prefix-scan helper %q", forbidden)
		}
	}
}
