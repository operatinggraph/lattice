package clinicreminders

import (
	"os"
	"path/filepath"
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

// TestPackage_DDLs pins the fourteen DDLs — the appointment-reminder pair
// (appointmentReminderOp vertexType + appointmentReminder aspectType), the
// follow-up-reminder pair (followUpReminderOp + followUpReminder), the two
// notification-outcome replyOp pairs (appointmentReminderNotificationOp +
// appointmentReminderNotification, followUpReminderNotificationOp +
// followUpReminderNotification), and the visit-series group (visitseries
// vertexType + its five aspectType gates: definition, progress, paused, the
// per-patient+provider active-series guard, and the site-assignment guard that
// serializes concurrent SetVisitSeriesSite callers). Each op vertexType owns its
// Record*/Start*/Advance* script; each aspectType is the step-6 write gate and
// MUST be NON-sensitive (a timestamp / cadence / guard pointer on an
// appointment/visitseries/patient, not an identity).
func TestPackage_DDLs(t *testing.T) {
	if got := len(Package.DDLs); got != 14 {
		t.Fatalf("expected 14 DDLs, got %d", got)
	}
	// Eleven op-metas: the five visit-series ops a human triggers carry a full
	// descriptor, the rest stay bare for forOperation resolution alone. A
	// dropped meta would surface only as an op that quietly stops being
	// offerable, so the count is what catches it.
	if got := len(Package.OpMetas); got != 11 {
		t.Fatalf("expected 11 opMetas, got %d", got)
	}
	byName := map[string]pkgmgr.DDLSpec{}
	for _, d := range Package.DDLs {
		byName[d.CanonicalName] = d
	}

	pairs := []struct{ opName, aspName, cmd string }{
		{"appointmentReminderOp", "appointmentReminder", "RecordAppointmentReminder"},
		{"followUpReminderOp", "followUpReminder", "RecordFollowUpReminder"},
		{"appointmentReminderNotificationOp", "appointmentReminderNotification", "RecordAppointmentReminderNotification"},
		{"followUpReminderNotificationOp", "followUpReminderNotification", "RecordFollowUpReminderNotification"},
	}
	for _, pr := range pairs {
		op, ok := byName[pr.opName]
		if !ok {
			t.Fatalf("missing %s vertexType DDL", pr.opName)
		}
		if op.Class != "meta.ddl.vertexType" {
			t.Fatalf("%s class = %q, want meta.ddl.vertexType", pr.opName, op.Class)
		}
		if len(op.PermittedCommands) != 1 || op.PermittedCommands[0] != pr.cmd {
			t.Fatalf("%s permittedCommands = %v, want [%s]", pr.opName, op.PermittedCommands, pr.cmd)
		}

		asp, ok := byName[pr.aspName]
		if !ok {
			t.Fatalf("missing %s aspectType DDL", pr.aspName)
		}
		if asp.Class != "meta.ddl.aspectType" {
			t.Fatalf("%s class = %q, want meta.ddl.aspectType", pr.aspName, asp.Class)
		}
		if asp.Sensitive {
			t.Fatalf("%s must NOT be sensitive (it attaches to a non-identity vertex; step-6 sensitiveAspectScope would reject it)", pr.aspName)
		}
		if len(asp.PermittedCommands) != 1 || asp.PermittedCommands[0] != pr.cmd {
			t.Fatalf("%s permittedCommands = %v, want [%s]", pr.aspName, asp.PermittedCommands, pr.cmd)
		}
	}

	series, ok := byName["visitseries"]
	if !ok {
		t.Fatalf("missing visitseries vertexType DDL")
	}
	if series.Class != "meta.ddl.vertexType" {
		t.Fatalf("visitseries class = %q, want meta.ddl.vertexType", series.Class)
	}
	wantCmds := map[string]bool{
		"StartVisitSeries": false, "PauseVisitSeries": false, "ResumeVisitSeries": false,
		"EndVisitSeries": false, "AdvanceVisitSeries": false,
		"BackfillVisitSeriesSite": false, "SetVisitSeriesSite": false,
	}
	if len(series.PermittedCommands) != len(wantCmds) {
		t.Fatalf("visitseries permittedCommands = %v, want %d entries", series.PermittedCommands, len(wantCmds))
	}
	for _, c := range series.PermittedCommands {
		if _, ok := wantCmds[c]; !ok {
			t.Fatalf("visitseries: unexpected permittedCommand %q", c)
		}
		wantCmds[c] = true
	}
	for c, seen := range wantCmds {
		if !seen {
			t.Fatalf("visitseries: missing permittedCommand %q", c)
		}
	}

	seriesAspects := []struct{ name, cmd string }{
		{"visitSeriesDefinition", "StartVisitSeries"},
		{"visitSeriesProgress", "StartVisitSeries"},
		{"visitSeriesPaused", "PauseVisitSeries"},
		{"visitSeriesGuard", "StartVisitSeries"},
		{"visitSeriesSiteAssignment", "SetVisitSeriesSite"},
	}
	for _, sa := range seriesAspects {
		asp, ok := byName[sa.name]
		if !ok {
			t.Fatalf("missing %s aspectType DDL", sa.name)
		}
		if asp.Class != "meta.ddl.aspectType" {
			t.Fatalf("%s class = %q, want meta.ddl.aspectType", sa.name, asp.Class)
		}
		if asp.Sensitive {
			t.Fatalf("%s must NOT be sensitive", sa.name)
		}
		found := false
		for _, c := range asp.PermittedCommands {
			if c == sa.cmd {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s permittedCommands = %v, want to include %s", sa.name, asp.PermittedCommands, sa.cmd)
		}
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

// TestPackage_Permissions pins the eleven ops at scope=any. Six are operator-only;
// the four front-desk Follow-ups-tab ops (Start/Pause/Resume/EndVisitSeries) plus
// SetVisitSeriesSite grant {operator, frontOfHouse} — the script's workplace guard
// confines the front-desk leg. AdvanceVisitSeries and BackfillVisitSeriesSite stay
// operator-only (both are Weaver's directOps).
func TestPackage_Permissions(t *testing.T) {
	// operationType -> the exact GrantsTo set expected.
	want := map[string][]string{
		"RecordAppointmentReminder":             {"operator"},
		"RecordFollowUpReminder":                {"operator"},
		"RecordAppointmentReminderNotification": {"operator"},
		"RecordFollowUpReminderNotification":    {"operator"},
		"StartVisitSeries":                      {"operator", "frontOfHouse"},
		"PauseVisitSeries":                      {"operator", "frontOfHouse"},
		"ResumeVisitSeries":                     {"operator", "frontOfHouse"},
		"EndVisitSeries":                        {"operator", "frontOfHouse"},
		"AdvanceVisitSeries":                    {"operator"},
		"SetVisitSeriesSite":                    {"operator", "frontOfHouse"},
		"BackfillVisitSeriesSite":               {"operator"},
	}
	seen := map[string]bool{}
	if len(Package.Permissions) != len(want) {
		t.Fatalf("expected %d permissions, got %d", len(want), len(Package.Permissions))
	}
	for _, p := range Package.Permissions {
		wantGrants, ok := want[p.OperationType]
		if !ok {
			t.Fatalf("unexpected permission op %q", p.OperationType)
		}
		if p.Scope != "any" {
			t.Fatalf("%s scope = %q, want any", p.OperationType, p.Scope)
		}
		if len(p.GrantsTo) != len(wantGrants) {
			t.Fatalf("%s GrantsTo = %v, want %v", p.OperationType, p.GrantsTo, wantGrants)
		}
		for i, g := range wantGrants {
			if p.GrantsTo[i] != g {
				t.Fatalf("%s GrantsTo = %v, want %v", p.OperationType, p.GrantsTo, wantGrants)
			}
		}
		seen[p.OperationType] = true
	}
	for op := range want {
		if !seen[op] {
			t.Fatalf("missing permission for %q", op)
		}
	}
}

// TestPackage_NoScans mirrors the known-key discipline: neither op script may use a
// prefix scan.
func TestPackage_NoScans(t *testing.T) {
	scripts := map[string]string{
		"recordReminderScript":                     recordReminderScript,
		"recordFollowUpReminderScript":             recordFollowUpReminderScript,
		"recordReminderNotificationScript":         recordReminderNotificationScript,
		"recordFollowUpReminderNotificationScript": recordFollowUpReminderNotificationScript,
		"visitSeriesScript":                        visitSeriesScript,
	}
	for name, script := range scripts {
		for _, forbidden := range []string{"KVListKeys", "list_keys", "scan(", "keys_with_prefix"} {
			if strings.Contains(script, forbidden) {
				t.Errorf("%s must not reference prefix-scan helper %q", name, forbidden)
			}
		}
	}
}

// TestClinicReminders_PlaybookColumnsMatchLens guards the §10.2↔§10.8 column seam
// for EVERY weaver target: each target's LensRef must resolve to a package lens with
// an Output descriptor whose OutputKeyPattern the target's TargetID prefixes, every
// gap KEY must be a BodyColumn, and every row.<col> the playbook names (the directOp
// Params + Reads) must be a BodyColumn — otherwise Weaver dispatches against a column
// that does not exist.
func TestClinicReminders_PlaybookColumnsMatchLens(t *testing.T) {
	lensByName := map[string]pkgmgr.LensSpec{}
	for _, l := range Package.Lenses {
		lensByName[l.CanonicalName] = l
	}
	if len(Package.WeaverTargets) != 5 {
		t.Fatalf("expected 5 weaverTargets, got %d", len(Package.WeaverTargets))
	}
	for _, wt := range Package.WeaverTargets {
		lens, ok := lensByName[wt.LensRef]
		if !ok || lens.Output == nil {
			t.Fatalf("target %s: lensRef %q resolves to no lens with an Output descriptor", wt.TargetID, wt.LensRef)
		}
		cols := map[string]bool{}
		for _, c := range lens.Output.BodyColumns {
			cols[c] = true
		}
		if pat := lens.Output.OutputKeyPattern; len(pat) < len(wt.TargetID) || pat[:len(wt.TargetID)] != wt.TargetID {
			t.Fatalf("targetId %q must prefix the %s lens OutputKeyPattern %q", wt.TargetID, wt.LensRef, pat)
		}
		checkRowRef := func(where, v string) {
			if rest, ok := strings.CutPrefix(v, "row."); ok {
				if !cols[rest] {
					t.Errorf("%s references row.%s but %s projects no such BodyColumn (have %v)", where, rest, wt.LensRef, lens.Output.BodyColumns)
				}
			}
		}
		for gapKey, ga := range wt.Gaps {
			if !cols[gapKey] {
				t.Errorf("target %s gap key %q is not a %s BodyColumn (have %v)", wt.TargetID, gapKey, wt.LensRef, lens.Output.BodyColumns)
			}
			for k, v := range ga.Params {
				checkRowRef("target "+wt.TargetID+" gap "+gapKey+" param "+k, v)
			}
			for _, r := range ga.Reads {
				checkRowRef("target "+wt.TargetID+" gap "+gapKey+" read", r)
			}
		}
	}
}
