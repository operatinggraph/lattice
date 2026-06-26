package clinicreminders

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

// TestPackage_DDLs pins the two DDLs: the appointmentReminderOp vertexType (owns
// the RecordAppointmentReminder script) and the appointmentReminder aspectType
// (the step-6 write gate). The aspect MUST be NON-sensitive (it carries only a
// timestamp and attaches to an appointment, not an identity).
func TestPackage_DDLs(t *testing.T) {
	if got := len(Package.DDLs); got != 2 {
		t.Fatalf("expected 2 DDLs, got %d", got)
	}
	byName := map[string]pkgmgr.DDLSpec{}
	for _, d := range Package.DDLs {
		byName[d.CanonicalName] = d
	}

	op, ok := byName["appointmentReminderOp"]
	if !ok {
		t.Fatal("missing appointmentReminderOp vertexType DDL")
	}
	if op.Class != "meta.ddl.vertexType" {
		t.Fatalf("appointmentReminderOp class = %q, want meta.ddl.vertexType", op.Class)
	}
	if len(op.PermittedCommands) != 1 || op.PermittedCommands[0] != "RecordAppointmentReminder" {
		t.Fatalf("appointmentReminderOp permittedCommands = %v, want [RecordAppointmentReminder]", op.PermittedCommands)
	}

	asp, ok := byName["appointmentReminder"]
	if !ok {
		t.Fatal("missing appointmentReminder aspectType DDL")
	}
	if asp.Class != "meta.ddl.aspectType" {
		t.Fatalf("appointmentReminder class = %q, want meta.ddl.aspectType", asp.Class)
	}
	if asp.Sensitive {
		t.Fatal("appointmentReminder must NOT be sensitive (it attaches to a non-identity vertex; step-6 sensitiveAspectScope would reject it)")
	}
	if len(asp.PermittedCommands) != 1 || asp.PermittedCommands[0] != "RecordAppointmentReminder" {
		t.Fatalf("appointmentReminder permittedCommands = %v, want [RecordAppointmentReminder]", asp.PermittedCommands)
	}
}

// TestPackage_Depends pins the dependency chain (clinic-domain for the appointment
// + .schedule.remindAt; orchestration-base for MarkExpired / the freshnessExpiry
// re-touch the @at firing writes).
func TestPackage_Depends(t *testing.T) {
	want := map[string]bool{"clinic-domain": false, "orchestration-base": false}
	if len(Package.Depends) != len(want) {
		t.Fatalf("expected %d deps, got %v", len(want), Package.Depends)
	}
	for _, d := range Package.Depends {
		if _, ok := want[d]; !ok {
			t.Fatalf("unexpected dependency %q", d)
		}
		want[d] = true
	}
	for d, seen := range want {
		if !seen {
			t.Fatalf("missing dependency %q", d)
		}
	}
}

// TestPackage_Permissions pins the single op granted to operator (scope any).
func TestPackage_Permissions(t *testing.T) {
	if len(Package.Permissions) != 1 {
		t.Fatalf("expected 1 permission, got %d", len(Package.Permissions))
	}
	p := Package.Permissions[0]
	if p.OperationType != "RecordAppointmentReminder" || p.Scope != "any" ||
		len(p.GrantsTo) != 1 || p.GrantsTo[0] != "operator" {
		t.Fatalf("unexpected permission: %+v", p)
	}
}

// TestPackage_NoScans mirrors the known-key discipline: the op script must use no
// prefix scan.
func TestPackage_NoScans(t *testing.T) {
	for _, forbidden := range []string{"KVListKeys", "list_keys", "scan(", "keys_with_prefix"} {
		if strings.Contains(recordReminderScript, forbidden) {
			t.Errorf("clinic-reminders script must not reference prefix-scan helper %q", forbidden)
		}
	}
}

// TestClinicReminders_PlaybookColumnsMatchLens guards the §10.2↔§10.8 column seam:
// every row.<col> template the playbook names (the directOp Params + Reads) must be
// a BodyColumn the appointmentReminders lens projects, and every gap KEY must be a
// BodyColumn — otherwise Weaver dispatches against a column that does not exist.
func TestClinicReminders_PlaybookColumnsMatchLens(t *testing.T) {
	if len(Package.Lenses) != 1 || Package.Lenses[0].Output == nil {
		t.Fatalf("expected 1 lens with an Output descriptor")
	}
	cols := map[string]bool{}
	for _, c := range Package.Lenses[0].Output.BodyColumns {
		cols[c] = true
	}
	if len(Package.WeaverTargets) != 1 {
		t.Fatalf("expected 1 weaverTarget, got %d", len(Package.WeaverTargets))
	}
	wt := Package.WeaverTargets[0]
	if pat := Package.Lenses[0].Output.OutputKeyPattern; len(pat) < len(wt.TargetID) || pat[:len(wt.TargetID)] != wt.TargetID {
		t.Fatalf("targetId %q must prefix the lens OutputKeyPattern %q", wt.TargetID, pat)
	}
	checkRowRef := func(where, v string) {
		if rest, ok := strings.CutPrefix(v, "row."); ok {
			if !cols[rest] {
				t.Errorf("%s references row.%s but appointmentReminders projects no such BodyColumn (have %v)", where, rest, Package.Lenses[0].Output.BodyColumns)
			}
		}
	}
	for gapKey, ga := range wt.Gaps {
		if !cols[gapKey] {
			t.Errorf("gap key %q is not a lens BodyColumn (have %v)", gapKey, Package.Lenses[0].Output.BodyColumns)
		}
		for k, v := range ga.Params {
			checkRowRef("gap "+gapKey+" param "+k, v)
		}
		for _, r := range ga.Reads {
			checkRowRef("gap "+gapKey+" read", r)
		}
	}
}
