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

func TestPackage_OneDDLOneLensOnePermission(t *testing.T) {
	if got := len(Package.DDLs); got != 1 {
		t.Fatalf("expected 1 DDL, got %d", got)
	}
	if got := Package.DDLs[0].CanonicalName; got != "task" {
		t.Fatalf("DDL canonicalName = %q, want task", got)
	}
	if got := len(Package.Lenses); got != 1 {
		t.Fatalf("expected 1 lens, got %d", got)
	}
	if got := Package.Lenses[0].CanonicalName; got != "capabilityEphemeral" {
		t.Fatalf("lens canonicalName = %q, want capabilityEphemeral", got)
	}
	if got := len(Package.Permissions); got != 1 {
		t.Fatalf("expected 1 permission, got %d", got)
	}
}

func TestPackage_TaskDDLOnlyCreateTask(t *testing.T) {
	cmds := Package.DDLs[0].PermittedCommands
	if len(cmds) != 1 || cmds[0] != "CreateTask" {
		t.Fatalf("permittedCommands = %v, want [CreateTask]", cmds)
	}
}

// TestPackage_CreateTaskGrantedToOperator pins the grantee role (A6).
func TestPackage_CreateTaskGrantedToOperator(t *testing.T) {
	p := Package.Permissions[0]
	if p.OperationType != "CreateTask" {
		t.Fatalf("permission op = %q, want CreateTask", p.OperationType)
	}
	if len(p.GrantsTo) != 1 || p.GrantsTo[0] != "operator" {
		t.Fatalf("CreateTask grantsTo = %v, want [operator]", p.GrantsTo)
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
