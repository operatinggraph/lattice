package clinicdomain

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

// TestPackage_DDLs pins the nine DDLs: three vertexType owners (patient,
// provider, appointment) and six aspectType step-6 gates. The aspect DDLs MUST
// be NON-sensitive (they attach to patient/provider/appointment vertices, not an
// identity — a sensitive aspect there would trip step-6's sensitiveAspectScope),
// and each names ONLY its writer op(s) in permittedCommands.
func TestPackage_DDLs(t *testing.T) {
	if got := len(Package.DDLs); got != 9 {
		t.Fatalf("expected 9 DDLs, got %d", got)
	}

	byName := map[string]pkgmgr.DDLSpec{}
	for _, d := range Package.DDLs {
		byName[d.CanonicalName] = d
	}

	vertexCmds := map[string][]string{
		"patient":     {"CreatePatient", "TombstonePatient"},
		"provider":    {"CreateProvider", "TombstoneProvider", "SetProviderHours"},
		"appointment": {"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "TombstoneAppointment"},
	}
	for name, wantCmds := range vertexCmds {
		vertex, ok := byName[name]
		if !ok {
			t.Fatalf("missing %s vertexType DDL", name)
		}
		if vertex.Class != "meta.ddl.vertexType" {
			t.Fatalf("%s class = %q, want meta.ddl.vertexType", name, vertex.Class)
		}
		want := map[string]bool{}
		for _, c := range wantCmds {
			want[c] = false
		}
		for _, c := range vertex.PermittedCommands {
			if _, ok := want[c]; !ok {
				t.Fatalf("unexpected %s command %q", name, c)
			}
			want[c] = true
		}
		for c, seen := range want {
			if !seen {
				t.Fatalf("%s missing command %q (have %v)", name, c, vertex.PermittedCommands)
			}
		}
	}

	aspectWriters := map[string][]string{
		"patientDemographics": {"CreatePatient"},
		"providerProfile":     {"CreateProvider"},
		"appointmentSchedule": {"CreateAppointment", "RescheduleAppointment"},
		"appointmentStatus":   {"CreateAppointment", "SetAppointmentStatus"},
		"providerBookings":    {"CreateProvider", "CreateAppointment", "RescheduleAppointment"},
		"providerHours":       {"SetProviderHours"},
	}
	for name, wantCmds := range aspectWriters {
		asp, ok := byName[name]
		if !ok {
			t.Fatalf("missing %s aspectType DDL", name)
		}
		if asp.Class != "meta.ddl.aspectType" {
			t.Fatalf("%s class = %q, want meta.ddl.aspectType", name, asp.Class)
		}
		if asp.Sensitive {
			t.Fatalf("%s must NOT be sensitive (it attaches to a non-identity vertex; step-6 sensitiveAspectScope would reject it)", name)
		}
		if len(asp.PermittedCommands) != len(wantCmds) {
			t.Fatalf("%s permittedCommands = %v, want %v", name, asp.PermittedCommands, wantCmds)
		}
		want := map[string]bool{}
		for _, c := range wantCmds {
			want[c] = false
		}
		for _, c := range asp.PermittedCommands {
			if _, ok := want[c]; !ok {
				t.Fatalf("%s unexpected permittedCommand %q (want %v)", name, c, wantCmds)
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

// TestPackage_NoCommandOverlapAcrossVertexTypes guards the operationType→script
// index invariant: an op admitted by two-or-more vertexType DDLs is dropped from
// the index (fail closed), so no op may be claimed by more than one vertexType
// DDL. (Aspect-type DDLs are excluded from the index, so their overlaps are fine.)
func TestPackage_NoCommandOverlapAcrossVertexTypes(t *testing.T) {
	seen := map[string]string{}
	for _, d := range Package.DDLs {
		if d.Class != "meta.ddl.vertexType" {
			continue
		}
		for _, cmd := range d.PermittedCommands {
			if prev, ok := seen[cmd]; ok {
				t.Fatalf("op %q claimed by two vertexType DDLs (%s and %s) — would be dropped from the script index", cmd, prev, d.CanonicalName)
			}
			seen[cmd] = d.CanonicalName
		}
	}
}

// TestPackage_Permissions pins all nine ops granted to operator (scope any) and
// nothing else, plus the three projection lenses and no package dependency.
func TestPackage_Permissions(t *testing.T) {
	wantPerms := map[string]bool{
		"CreatePatient": false, "TombstonePatient": false,
		"CreateProvider": false, "TombstoneProvider": false, "SetProviderHours": false,
		"CreateAppointment": false, "RescheduleAppointment": false,
		"SetAppointmentStatus": false, "TombstoneAppointment": false,
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

	if len(Package.Depends) != 0 {
		t.Fatalf("expected no Depends (self-contained), got %v", Package.Depends)
	}

	if got := len(Package.Lenses); got != 3 {
		t.Fatalf("expected 3 lenses, got %d", got)
	}
	lensByName := map[string]pkgmgr.LensSpec{}
	for _, l := range Package.Lenses {
		lensByName[l.CanonicalName] = l
	}
	if l, ok := lensByName["clinicAppointments"]; !ok ||
		l.Adapter != "nats-kv" || l.Bucket != ClinicAppointmentsBucket {
		t.Fatalf("unexpected clinicAppointments shape: %+v", lensByName["clinicAppointments"])
	}
	if l, ok := lensByName["clinicProviders"]; !ok ||
		l.Adapter != "nats-kv" || l.Bucket != ClinicProvidersBucket {
		t.Fatalf("unexpected clinicProviders shape: %+v", lensByName["clinicProviders"])
	}
	if l, ok := lensByName["clinicPatients"]; !ok ||
		l.Adapter != "nats-kv" || l.Bucket != ClinicPatientsBucket {
		t.Fatalf("unexpected clinicPatients shape: %+v", lensByName["clinicPatients"])
	}
	if got := len(Package.WeaverTargets); got != 0 {
		t.Fatalf("expected 0 weaverTargets, got %d", got)
	}
	if got := len(Package.LoomPatterns); got != 0 {
		t.Fatalf("expected 0 loomPatterns, got %d", got)
	}
}

// TestPackage_NoScans mirrors the known-key discipline guard: no script may use a
// prefix scan.
func TestPackage_NoScans(t *testing.T) {
	for _, src := range []string{patientDDLScript, providerDDLScript, appointmentDDLScript} {
		for _, forbidden := range []string{"KVListKeys", "list_keys", "scan(", "keys_with_prefix"} {
			if strings.Contains(src, forbidden) {
				t.Errorf("clinic script must not reference prefix-scan helper %q", forbidden)
			}
		}
	}
}

// TestPackage_ScriptGuards pins the load-bearing invariants: the appointment
// endpoints must be the right class, the status enum, the link direction
// (appointment is the source), and the unconditioned-upsert idiom for status.
func TestPackage_ScriptGuards(t *testing.T) {
	for _, want := range []string{
		`require_live_typed`, // endpoint alive + class
		`WrongClass`,         // endpoint-class guard
		"scheduled, confirmed, checkedIn, completed, cancelled, noShow", // status enum
		`lnk.appointment.`,                            // link direction (appointment is source)
		`.forPatient.patient.`,                        // forPatient link shape
		`.withProvider.provider.`,                     // withProvider link shape
		`make_aspect_upsert(appt_key, "status"`,       // SetAppointmentStatus upsert
		`make_aspect_upsert(appt_key, "schedule"`,     // RescheduleAppointment rewrites .schedule
		`clinic.appointmentRescheduled`,               // RescheduleAppointment event
		`enforce_hours(provider, starts_at, ends_at)`, // both ops enforce provider hours
		`OutsideHours`,                                // the availability-window rejection
		`time.weekday(starts_at)`,                     // weekday membership
		`time.seconds_of_day(starts_at)`,              // time-of-day membership
	} {
		if !strings.Contains(appointmentDDLScript, want) {
			t.Errorf("appointment script must reference %q", want)
		}
	}

	// The provider script owns SetProviderHours (the .hours writer) + its validation.
	for _, want := range []string{
		`SetProviderHours`,                       // op handler
		`make_aspect_upsert(prkey, "hours"`,      // upserts the .hours aspect
		`require_int_in(w, "day", 0, 6)`,         // weekday range validation
		`require_int_in(w, "openSec", 0, 86400)`, // seconds-of-day range validation
		`clinic.providerHoursSet`,                // event
	} {
		if !strings.Contains(providerDDLScript, want) {
			t.Errorf("provider script must reference %q", want)
		}
	}
}
