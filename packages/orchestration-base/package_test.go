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

func TestPackage_OneDDLTwoLensesFourPermissions(t *testing.T) {
	if got := len(Package.DDLs); got != 1 {
		t.Fatalf("expected 1 DDL, got %d", got)
	}
	if got := Package.DDLs[0].CanonicalName; got != "task" {
		t.Fatalf("DDL canonicalName = %q, want task", got)
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
	if got := len(Package.Permissions); got != 4 {
		t.Fatalf("expected 4 permissions, got %d", got)
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
			t.Fatalf("unexpected permission op %q", p.OperationType)
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
