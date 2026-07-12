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

// TestPackage_DDLs pins the sixteen DDLs: five vertexType owners (patient,
// provider, appointment, clinicSite, clinicSiteAssignment) and eleven aspectType
// step-6 gates (nine attach to patient/provider/appointment vertices;
// identityPatientClaim attaches onto an identity-domain vertex, the clinic-
// reminders idiom; clinicSiteProfile attaches onto a location-domain building,
// the loftspace-domain aspect-contribution idiom). All aspect DDLs MUST be
// NON-sensitive, and each names ONLY its writer op(s) in permittedCommands.
func TestPackage_DDLs(t *testing.T) {
	if got := len(Package.DDLs); got != 16 {
		t.Fatalf("expected 16 DDLs, got %d", got)
	}

	byName := map[string]pkgmgr.DDLSpec{}
	for _, d := range Package.DDLs {
		byName[d.CanonicalName] = d
	}

	vertexCmds := map[string][]string{
		"patient":              {"CreatePatient", "TombstonePatient"},
		"provider":             {"CreateProvider", "TombstoneProvider", "SetProviderProfile", "SetProviderHours", "SetProviderTimeOff"},
		"appointment":          {"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "RecordEncounter", "TombstoneAppointment"},
		"clinicSite":           {"SetSiteProfile"},
		"clinicSiteAssignment": {"AssignProviderSite", "RemoveProviderSite"},
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
		"patientDemographics":  {"CreatePatient"},
		"providerProfile":      {"CreateProvider", "SetProviderProfile"},
		"appointmentSchedule":  {"CreateAppointment", "RescheduleAppointment"},
		"appointmentStatus":    {"CreateAppointment", "SetAppointmentStatus"},
		"providerHours":        {"SetProviderHours"},
		"providerTimeOff":      {"SetProviderTimeOff"},
		"providerSlotClaim":    {"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "TombstoneAppointment"},
		"patientSlotClaim":     {"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "TombstoneAppointment"},
		"appointmentEncounter": {"RecordEncounter"},
		"identityPatientClaim": {"CreatePatient"},
		"clinicSiteProfile":    {"SetSiteProfile"},
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
			t.Fatalf("%s must NOT be sensitive", name)
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

// TestPackage_Permissions pins every op granted to operator (scope any) —
// except CreateAppointment, RescheduleAppointment, and SetAppointmentStatus,
// which ALSO carry a consumer scope=self grant (patient self-booking /
// self-reschedule / self-cancel) — plus the ten projection lenses and the
// location-domain dependency.
func TestPackage_Permissions(t *testing.T) {
	type wantGrant struct {
		scope     string
		grantsTo  string
		fulfilled bool
	}
	wantPerms := map[string][]*wantGrant{
		"CreatePatient": {{scope: "any", grantsTo: "operator"}}, "TombstonePatient": {{scope: "any", grantsTo: "operator"}},
		"CreateProvider": {{scope: "any", grantsTo: "operator"}}, "TombstoneProvider": {{scope: "any", grantsTo: "operator"}},
		"SetProviderProfile": {{scope: "any", grantsTo: "operator"}}, "SetProviderHours": {{scope: "any", grantsTo: "operator"}}, "SetProviderTimeOff": {{scope: "any", grantsTo: "operator"}},
		"CreateAppointment": {{scope: "any", grantsTo: "operator"}, {scope: "self", grantsTo: "consumer"}}, "RescheduleAppointment": {{scope: "any", grantsTo: "operator"}, {scope: "self", grantsTo: "consumer"}},
		"SetAppointmentStatus": {{scope: "any", grantsTo: "operator"}, {scope: "self", grantsTo: "consumer"}}, "RecordEncounter": {{scope: "any", grantsTo: "operator"}}, "TombstoneAppointment": {{scope: "any", grantsTo: "operator"}},
		"SetSiteProfile": {{scope: "any", grantsTo: "operator"}}, "AssignProviderSite": {{scope: "any", grantsTo: "operator"}}, "RemoveProviderSite": {{scope: "any", grantsTo: "operator"}},
	}
	wantCount := 0
	for _, grants := range wantPerms {
		wantCount += len(grants)
	}
	if got := len(Package.Permissions); got != wantCount {
		t.Fatalf("expected %d permissions, got %d", wantCount, got)
	}
	for _, perm := range Package.Permissions {
		grants, ok := wantPerms[perm.OperationType]
		if !ok {
			t.Fatalf("unexpected permission for %q", perm.OperationType)
		}
		if len(perm.GrantsTo) != 1 {
			t.Fatalf("%s grantsTo = %v, want exactly one role", perm.OperationType, perm.GrantsTo)
		}
		matched := false
		for _, g := range grants {
			if g.scope == perm.Scope && g.grantsTo == perm.GrantsTo[0] {
				g.fulfilled = true
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("%s: unexpected (scope=%q, grantsTo=%v)", perm.OperationType, perm.Scope, perm.GrantsTo)
		}
	}
	for op, grants := range wantPerms {
		for _, g := range grants {
			if !g.fulfilled {
				t.Fatalf("missing permission for op %q (scope=%s, grantsTo=%s)", op, g.scope, g.grantsTo)
			}
		}
	}

	if got := Package.Depends; len(got) != 1 || got[0] != "location-domain" {
		t.Fatalf("expected Depends=[location-domain], got %v", got)
	}

	if got := len(Package.Lenses); got != 10 {
		t.Fatalf("expected 10 lenses, got %d", got)
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
	if l, ok := lensByName["clinicSites"]; !ok ||
		l.Adapter != "nats-kv" || l.Bucket != ClinicSitesBucket {
		t.Fatalf("unexpected clinicSites shape: %+v", lensByName["clinicSites"])
	}
	if l, ok := lensByName["providerSites"]; !ok ||
		l.Adapter != "nats-kv" || l.Bucket != ClinicProviderSitesBucket || !l.DiffRetraction ||
		len(l.IntoKey) != 2 || l.IntoKey[0] != "provider_id" || l.IntoKey[1] != "site_id" {
		t.Fatalf("unexpected providerSites shape: %+v", lensByName["providerSites"])
	}
	if l, ok := lensByName["clinicAppointmentsRead"]; !ok ||
		l.Adapter != "postgres" || l.Table != "read_clinic_appointments" || !l.Protected {
		t.Fatalf("unexpected clinicAppointmentsRead shape: %+v", lensByName["clinicAppointmentsRead"])
	}
	if l, ok := lensByName["providerAppointmentsRead"]; !ok ||
		l.Adapter != "postgres" || l.Table != "read_provider_appointments" || !l.Protected {
		t.Fatalf("unexpected providerAppointmentsRead shape: %+v", lensByName["providerAppointmentsRead"])
	}
	if l, ok := lensByName["clinicPatientsRead"]; !ok ||
		l.Adapter != "postgres" || l.Table != "read_clinic_patients" || !l.Protected {
		t.Fatalf("unexpected clinicPatientsRead shape: %+v", lensByName["clinicPatientsRead"])
	}
	if l, ok := lensByName["clinicPatientReadGrants"]; !ok ||
		l.Adapter != "postgres" || !l.GrantTable || l.Protected {
		t.Fatalf("unexpected clinicPatientReadGrants shape: %+v", lensByName["clinicPatientReadGrants"])
	}
	if l, ok := lensByName["clinicProviderReadGrants"]; !ok ||
		l.Adapter != "postgres" || !l.GrantTable || l.Protected {
		t.Fatalf("unexpected clinicProviderReadGrants shape: %+v", lensByName["clinicProviderReadGrants"])
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
	for _, src := range []string{patientDDLScript, providerDDLScript, appointmentDDLScript, clinicSiteDDLScript, clinicSiteAssignmentDDLScript} {
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
		`lnk.appointment.`,                                                 // link direction (appointment is source)
		`.forPatient.patient.`,                                             // forPatient link shape
		`.withProvider.provider.`,                                          // withProvider link shape
		`make_aspect_upsert(appt_key, "status"`,                            // SetAppointmentStatus upsert
		`make_aspect_upsert(appt_key, "schedule"`,                          // RescheduleAppointment rewrites .schedule
		`clinic.appointmentRescheduled`,                                    // RescheduleAppointment event
		`enforce_hours(provider, starts_at, ends_at)`,                      // both ops enforce provider hours
		`OutsideHours`,                                                     // the availability-window rejection
		`time.weekday(starts_at)`,                                          // weekday membership
		`time.seconds_of_day(starts_at)`,                                   // time-of-day membership
		`enforce_time_off(provider, starts_at, ends_at)`,                   // both ops enforce provider time-off
		`ProviderUnavailable`,                                              // the time-off-overlap rejection
		`PatientDoubleBook`,                                                // patient-side double-book rejection (across providers)
		`SlotConflict`,                                                     // provider-side double-book rejection
		`SlotGridViolation`,                                                // the 15-minute-grid alignment guard
		`AppointmentTooLong`,                                               // the 96-cell / 24h span cap
		`def enforce_grid(starts_at, ends_at)`,                             // grid-alignment guard
		`def slot_cells(starts_at, ends_at)`,                               // cell discretization
		`def slot_cellcode(cell_start)`,                                    // deterministic cell-key derivation
		`def claim_cell(hub, cellcode, cls, conflict_code, who)`,           // write-path claim (CreateOnly IS the lock)
		`def release_cells_mutations(provider, patient, sched)`,            // cell release on terminal transition / tombstone
		`def require_matching_provider(appt_id, provider)`,                 // provider identity validated via withProvider link
		`def require_matching_patient(appt_id, patient)`,                   // patient identity validated via forPatient link
		`WrongProvider`,                                                    // reschedule/terminal ops validate the passed provider
		`WrongPatient`,                                                     // reschedule validates the passed patient via the forPatient link
		`ot == "RecordEncounter"`,                                          // the clinical-record op handler
		`make_aspect_upsert(appt_key, "encounter", "appointmentEncounter"`, // RecordEncounter upserts .encounter
		`clinic.appointmentEncounterRecorded`,                              // RecordEncounter event
		`"documentedAt": time.rfc3339_utc(op.submittedAt)`,                 // operational documentedAt derived from submittedAt
	} {
		if !strings.Contains(appointmentDDLScript, want) {
			t.Errorf("appointment script must reference %q", want)
		}
	}

	// The clinical-record PHI discipline: the clinicAppointments lens projects the
	// OPERATIONAL encounter signals but NEVER the raw clinical content. summary /
	// assessment / plan must not appear in any RETURN projection (the .demographics
	// name-only precedent applied to .encounter; the Vault plane owns display).
	for _, projected := range []string{"a.encounter.data.documentedAt", "a.encounter.data.followUpRequested", "a.encounter.data.followUpDate"} {
		if !strings.Contains(clinicAppointmentsSpec, projected) {
			t.Errorf("clinicAppointments must project the operational encounter signal %q", projected)
		}
	}
	for _, phi := range []string{"encounter.data.summary", "encounter.data.assessment", "encounter.data.plan"} {
		if strings.Contains(clinicAppointmentsSpec, phi) {
			t.Errorf("clinicAppointments must NOT project the raw clinical PHI field %q (Vault-plane deferred)", phi)
		}
	}

	// The provider script owns SetProviderHours (the .hours writer) + its validation.
	for _, want := range []string{
		`SetProviderHours`,                       // op handler
		`make_aspect_upsert(prkey, "hours"`,      // upserts the .hours aspect
		`require_int_in(w, "day", 0, 6)`,         // weekday range validation
		`require_int_in(w, "openSec", 0, 86400)`, // seconds-of-day range validation
		`clinic.providerHoursSet`,                // event
		`SetProviderTimeOff`,                     // time-off op handler
		`make_aspect_upsert(prkey, "timeOff"`,    // upserts the .timeOff aspect
		`clinic.providerTimeOffSet`,              // time-off event
		`SetProviderProfile`,                     // profile-edit op handler
		`make_aspect_upsert(prkey, "profile"`,    // replaces the .profile aspect
		`clinic.providerProfileSet`,              // profile-edit event
	} {
		if !strings.Contains(providerDDLScript, want) {
			t.Errorf("provider script must reference %q", want)
		}
	}
}
