package clinicdomain

import (
	"os"
	"path/filepath"
	"slices"
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

// TestPackage_DDLs pins the nineteen DDLs: five vertexType owners (patient,
// provider, appointment, clinicSite, clinicSiteAssignment) and fourteen
// aspectType step-6 gates (ten attach to patient/provider/appointment
// vertices; identityPatientClaim and identityProviderClaim attach onto an
// identity-domain vertex, the clinic-reminders idiom; clinicSiteProfile
// attaches onto a location-domain building, the loftspace-domain
// aspect-contribution idiom; providerIdentityClaim attaches onto the
// package's own provider vertex). Every aspect DDL is NON-sensitive and names
// ONLY its writer op(s) in permittedCommands EXCEPT appointmentEncounter,
// which is SENSITIVE and custodied on the clinicalRecord retention class —
// see TestPackage_EncounterAspectIsSensitiveAndCustodied — the one deliberate
// exception this loop excludes and tests separately.
func TestPackage_DDLs(t *testing.T) {
	if got := len(Package.DDLs); got != 19 {
		t.Fatalf("expected 19 DDLs, got %d", got)
	}

	byName := map[string]pkgmgr.DDLSpec{}
	for _, d := range Package.DDLs {
		byName[d.CanonicalName] = d
	}

	vertexCmds := map[string][]string{
		"patient":              {"CreatePatient", "TombstonePatient"},
		"provider":             {"CreateProvider", "TombstoneProvider", "SetProviderProfile", "SetProviderHours", "SetProviderTimeOff", "BindProviderIdentity"},
		"appointment":          {"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "MarkPastDueNoShow", "BackfillAppointmentSite", "RecordEncounter", "TombstoneAppointment"},
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
		"patientDemographics":      {"CreatePatient"},
		"providerProfile":          {"CreateProvider", "SetProviderProfile"},
		"appointmentSchedule":      {"CreateAppointment", "RescheduleAppointment"},
		"appointmentStatus":        {"CreateAppointment", "SetAppointmentStatus", "MarkPastDueNoShow"},
		"providerHours":            {"SetProviderHours"},
		"providerTimeOff":          {"SetProviderTimeOff"},
		"providerSlotClaim":        {"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "MarkPastDueNoShow", "TombstoneAppointment"},
		"patientSlotClaim":         {"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "MarkPastDueNoShow", "TombstoneAppointment"},
		"appointmentDocumentation": {"RecordEncounter"},
		"identityPatientClaim":     {"CreatePatient"},
		"clinicSiteProfile":        {"SetSiteProfile"},
		"providerIdentityClaim":    {"BindProviderIdentity"},
		"identityProviderClaim":    {"BindProviderIdentity"},
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

// TestPackage_EncounterAspectIsSensitiveAndCustodied is the regression guard for
// the clinical-record custody posture: the appointmentEncounter DDL must declare
// Sensitive + a retentionClass Custody naming clinicalRecord, and the package
// must declare exactly that retention class with the eraseOnExpiry policy. A
// silent loss of either — Sensitive flipping back to false, or Custody being
// dropped/retargeted — would fall back to committing the raw clinical record as
// PLAINTEXT (Sensitive false) or reject at install (Custody naming an undeclared
// class), so this pins both halves of the declaration together.
func TestPackage_EncounterAspectIsSensitiveAndCustodied(t *testing.T) {
	byName := map[string]pkgmgr.DDLSpec{}
	for _, d := range Package.DDLs {
		byName[d.CanonicalName] = d
	}
	enc, ok := byName["appointmentEncounter"]
	if !ok {
		t.Fatalf("missing appointmentEncounter aspectType DDL")
	}
	if !enc.Sensitive {
		t.Fatalf("appointmentEncounter must be Sensitive (it carries the raw clinical record)")
	}
	if enc.Custody.Kind != pkgmgr.CustodyKindRetentionClass {
		t.Fatalf("appointmentEncounter Custody.Kind = %q, want %q", enc.Custody.Kind, pkgmgr.CustodyKindRetentionClass)
	}
	if enc.Custody.RetentionClass != "clinicalRecord" {
		t.Fatalf("appointmentEncounter Custody.RetentionClass = %q, want %q", enc.Custody.RetentionClass, "clinicalRecord")
	}

	if got := len(Package.RetentionClasses); got != 1 {
		t.Fatalf("expected exactly 1 retention class, got %d", got)
	}
	rc := Package.RetentionClasses[0]
	if rc.CanonicalName != "clinicalRecord" {
		t.Fatalf("retention class CanonicalName = %q, want %q", rc.CanonicalName, "clinicalRecord")
	}
	if rc.Policy != pkgmgr.RetentionPolicyEraseOnExpiry {
		t.Fatalf("retention class Policy = %q, want %q", rc.Policy, pkgmgr.RetentionPolicyEraseOnExpiry)
	}
	if rc.RetentionPeriod == "" {
		t.Fatalf("retention class RetentionPeriod must be declared (it is declarative, but an unstated schedule is unauditable)")
	}
	if rc.Description == "" {
		t.Fatalf("retention class Description must be declared")
	}
}

// TestPackage_DocumentationAspectIsNotSensitive proves the split's whole point:
// the operational .documentation aspect carries no custody at all, so the plain
// (unencrypted-read) lenses that project it can actually read it. A regression
// that flips Sensitive on this DDL would make clinicAppointments' operational
// columns unreadable by every non-Vault-aware lens.
func TestPackage_DocumentationAspectIsNotSensitive(t *testing.T) {
	byName := map[string]pkgmgr.DDLSpec{}
	for _, d := range Package.DDLs {
		byName[d.CanonicalName] = d
	}
	doc, ok := byName["appointmentDocumentation"]
	if !ok {
		t.Fatalf("missing appointmentDocumentation aspectType DDL")
	}
	if doc.Sensitive {
		t.Fatalf("appointmentDocumentation must NOT be sensitive — it is the plain-lens-readable half of the split")
	}
	if doc.Custody.Kind != "" || doc.Custody.RetentionClass != "" {
		t.Fatalf("appointmentDocumentation must declare no custody, got %+v", doc.Custody)
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

// TestPackage_Permissions pins the exact role SET on every grant. Each
// (operationType, scope) pair carries exactly ONE permission row (Contract
// #8 §8.1 permTag — a duplicate pair would collapse onto the same
// vtx.permission.<id> key), so widening an op's grantees means widening its
// EXISTING row, never adding a second one at the same scope. Four postures
// are distinguished and each is load-bearing:
//
//   - operator-only (scope any) — the roster admin surface.
//   - operator + frontOfHouse + provider, all on the SAME scope=any row
//     (SetProviderHours/SetProviderTimeOff/RescheduleAppointment/
//     SetAppointmentStatus) — the front-desk schedule beat, widened so a
//     bound provider also reaches their OWN availability/appointments
//     (confined in-script to the caller's own identifiedBy-bound provider).
//     BindProviderIdentity (the role-minting bind ceremony) stays
//     operator-only — it is not provider- or front-desk-reachable.
//   - operator + provider, on the SAME scope=any row (RecordEncounter) — the
//     one clinical verb a clinician owns, confined in-script to the caller's
//     own identifiedBy-bound provider's appointments, same as above.
//   - consumer (scope self) — patient self-booking / self-reschedule /
//     self-cancel.
//
// Plus the thirteen projection lenses and the location-domain dependency.
func TestPackage_Permissions(t *testing.T) {
	type wantGrant struct {
		scope     string
		grantsTo  []string
		fulfilled bool
	}
	op := func(roles ...string) []*wantGrant {
		return []*wantGrant{{scope: "any", grantsTo: roles}}
	}
	operatorOnly := func() []*wantGrant { return op("operator") }
	wantPerms := map[string][]*wantGrant{
		"CreatePatient": op("operator", "frontOfHouse"), "TombstonePatient": operatorOnly(),
		"CreateProvider": operatorOnly(), "TombstoneProvider": operatorOnly(),
		"SetProviderProfile": operatorOnly(),
		// A permission's identity is its (operationType, scope) pair (Contract
		// #8 §8.1) — the provider widening lands on the SAME row as operator's,
		// never a second scope=any row (validatePermissionIdentityUniqueness
		// rejects that as a duplicate before any KV write).
		"SetProviderHours":   op("operator", "provider"),
		"SetProviderTimeOff": op("operator", "provider"),
		"CreateAppointment":  {{scope: "any", grantsTo: []string{"operator", "frontOfHouse"}}, {scope: "self", grantsTo: []string{"consumer"}}},
		// The staff-widened front-desk ops — now also widened to the bound provider.
		"RescheduleAppointment": {{scope: "any", grantsTo: []string{"operator", "frontOfHouse", "provider"}}, {scope: "self", grantsTo: []string{"consumer"}}},
		"SetAppointmentStatus":  {{scope: "any", grantsTo: []string{"operator", "frontOfHouse", "provider"}}, {scope: "self", grantsTo: []string{"consumer"}}},
		"RecordEncounter":       op("operator", "provider"), "TombstoneAppointment": operatorOnly(),
		"SetSiteProfile": operatorOnly(), "AssignProviderSite": operatorOnly(), "RemoveProviderSite": operatorOnly(),
		"BindProviderIdentity": {{scope: "any", grantsTo: []string{"operator"}}},
		// Weaver-only auto no-show (clinic-reminders' pastDueAppointments target's
		// only caller) — operator authority, the RecordAppointmentReminder idiom.
		"MarkPastDueNoShow": operatorOnly(),
		// Weaver-only site backfill (this package's own clinicSiteBackfill
		// target's only caller) — operator authority, the MarkPastDueNoShow idiom.
		"BackfillAppointmentSite": operatorOnly(),
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
		matched := false
		for _, g := range grants {
			if g.scope == perm.Scope && slices.Equal(g.grantsTo, perm.GrantsTo) {
				g.fulfilled = true
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("%s: unexpected (scope=%q, grantsTo=%v)", perm.OperationType, perm.Scope, perm.GrantsTo)
		}
	}
	for opType, grants := range wantPerms {
		for _, g := range grants {
			if !g.fulfilled {
				t.Fatalf("missing permission for op %q (scope=%s, grantsTo=%v)", opType, g.scope, g.grantsTo)
			}
		}
	}

	if got := Package.Depends; len(got) != 1 || got[0] != "location-domain" {
		t.Fatalf("expected Depends=[location-domain], got %v", got)
	}

	if got := len(Package.Lenses); got != 13 {
		t.Fatalf("expected 13 lenses, got %d", got)
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
	if l, ok := lensByName["clinicSiteBackfill"]; !ok ||
		l.Adapter != "nats-kv" || l.Bucket != "weaver-targets" || l.ProjectionKind != "actorAggregate" ||
		l.Output == nil || l.Output.AnchorType != "appointment" {
		t.Fatalf("unexpected clinicSiteBackfill shape: %+v", lensByName["clinicSiteBackfill"])
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
	if l, ok := lensByName["providerIdentityReadGrants"]; !ok ||
		l.Adapter != "postgres" || !l.GrantTable || l.Protected || !l.DiffRetraction || l.GrantSource != "cap-read.provider.clinic" {
		t.Fatalf("unexpected providerIdentityReadGrants shape: %+v", lensByName["providerIdentityReadGrants"])
	}
	if l, ok := lensByName["patientIdentityReadGrants"]; !ok ||
		l.Adapter != "postgres" || !l.GrantTable || l.Protected || !l.DiffRetraction || l.GrantSource != "cap-read.patient.clinic" {
		t.Fatalf("unexpected patientIdentityReadGrants shape: %+v", lensByName["patientIdentityReadGrants"])
	}
	// The two identity-bridge producers must stay on DISTINCT grant_sources:
	// each retracts only its own rows, so sharing one would make either
	// producer's diff revoke the other's grants wholesale.
	if lensByName["patientIdentityReadGrants"].GrantSource == lensByName["providerIdentityReadGrants"].GrantSource {
		t.Fatal("the patient and provider identity-bridge producers share a grant_source")
	}
	if got := len(Package.WeaverTargets); got != 1 {
		t.Fatalf("expected 1 weaverTarget, got %d", got)
	}
	if got := len(Package.OpMetas); got != 7 {
		t.Errorf("OpMetas: got %d, want 7", got)
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
		`make_aspect_upsert(appt_key, "encounter", "appointmentEncounter"`, // RecordEncounter upserts the sensitive .encounter half
		`make_aspect_upsert(appt_key, "documentation", "appointmentDocumentation"`, // RecordEncounter upserts the operational .documentation half
		`clinic.appointmentEncounterRecorded`,                                      // RecordEncounter event
		`"documentedAt": time.rfc3339_utc(op.submittedAt)`,                         // operational documentedAt derived from submittedAt
		`cannot record encounter on appointment `,                                  // RecordEncounter's standing provider-binding guard
	} {
		if !strings.Contains(appointmentDDLScript, want) {
			t.Errorf("appointment script must reference %q", want)
		}
	}

	// The clinical-record PHI discipline: the clinicAppointments lens projects the
	// OPERATIONAL documentation signals but NEVER the raw clinical content. summary /
	// assessment / plan must not appear in any RETURN projection (the .demographics
	// name-only precedent applied to the split — the sensitive .encounter half is
	// never projected by any lens).
	for _, projected := range []string{"a.documentation.data.documentedAt", "a.documentation.data.followUpRequested", "a.documentation.data.followUpDate"} {
		if !strings.Contains(clinicAppointmentsSpec, projected) {
			t.Errorf("clinicAppointments must project the operational documentation signal %q", projected)
		}
	}
	for _, phi := range []string{"encounter.data.summary", "encounter.data.assessment", "encounter.data.plan"} {
		if strings.Contains(clinicAppointmentsSpec, phi) {
			t.Errorf("clinicAppointments must NOT project the raw clinical PHI field %q (SENSITIVE, custodied on the clinicalRecord retention class)", phi)
		}
	}

	// A patient's NAME is PHI: it must NEVER be PROJECTED by an OPEN (unauthenticated,
	// nats-kv) lens — a localhost reader dumping the bucket would otherwise learn a
	// named person is a patient here, itself a disclosure. Patient names are projected
	// ONLY into the Protected, RLS-scoped clinicPatientsRead / clinicAppointmentsRead
	// lenses. (Provider directory names are a separate, deliberately-public roster the
	// booking UI needs and stay in clinicProviders.) The `... AS` form is the RETURN
	// projection; a `WHERE p.demographics.data.fullName <> null` presence filter reads
	// the aspect without projecting its value and is fine.
	for name, spec := range map[string]string{"clinicAppointmentsSpec": clinicAppointmentsSpec, "clinicPatientsSpec": clinicPatientsSpec} {
		if strings.Contains(spec, "p.demographics.data.fullName AS") {
			t.Errorf("open lens %s must NOT project the patient name (PHI); it belongs only in the Protected clinicPatientsRead", name)
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
		`BindProviderIdentity`,                   // bind op handler
		`.identifiedBy.identity.`,                // provider identifiedBy identity link shape
		`ProviderAlreadyBound`,                   // entity-keyed exclusivity guard
		`IdentityAlreadyBoundToProvider`,         // identity-keyed exclusivity guard
		`.holdsRole.role.`,                       // idempotent provider-role grant
		`actor_holds_operator`,                   // the standing operator exemption
		`actor_bound_to_provider`,                // the standing provider-binding guard
		`AuthDenied`,                             // the standing guard's denial
	} {
		if !strings.Contains(providerDDLScript, want) {
			t.Errorf("provider script must reference %q", want)
		}
	}

	// The bind op's role-key pin must actually be substituted at package-init
	// (providerDDLScriptTemplate carries the placeholder; providerDDLScript is
	// its ReplaceAll'd product) — a regression here would leave the literal
	// placeholder in the shipped script, minting a holdsRole link whose target
	// is the string "__EXPECTED_PROVIDER_ROLE_KEY__" instead of a real role key.
	if !strings.Contains(providerDDLScriptTemplate, "__EXPECTED_PROVIDER_ROLE_KEY__") {
		t.Errorf("providerDDLScriptTemplate must carry the __EXPECTED_PROVIDER_ROLE_KEY__ placeholder")
	}
	if strings.Contains(providerDDLScript, "__EXPECTED_PROVIDER_ROLE_KEY__") {
		t.Errorf("providerDDLScript must not contain the unsubstituted __EXPECTED_PROVIDER_ROLE_KEY__ placeholder")
	}
	if !strings.Contains(providerDDLScript, providerRoleKey) {
		t.Errorf("providerDDLScript must contain the substituted providerRoleKey literal %q", providerRoleKey)
	}

	// The appointment script's standing guard gains a THIRD binder (the bound
	// provider), alongside operator/workplace — RescheduleAppointment and
	// SetAppointmentStatus both extend the same standing branch.
	for _, want := range []string{
		`def appointment_provider(appt_id)`,                                // factored-out provider resolution (no double read)
		`def sites_for_provider(provider)`,                                 // factored-out site walk
		`def actor_bound_to_appointment_provider`,                          // the third standing binder
		`actor_bound_to_appointment_provider(op.actor, standing_provider)`, // wired into both standing branches
	} {
		if !strings.Contains(appointmentDDLScript, want) {
			t.Errorf("appointment script must reference %q", want)
		}
	}
}
