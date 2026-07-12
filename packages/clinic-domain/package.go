// Package clinicdomain is the clinic-domain Capability Package — the bookable
// foundation of the clinic vertical (patient / provider / appointment).
//
// Unlike loftspace-domain (which adds aspects onto location-domain's units),
// clinic-domain is SELF-CONTAINED: it owns three vertex types, mirroring
// location-domain's "own your domain's vertex types" precedent.
//
//	vtx.patient.<id>      class=patient      root {}   .demographics {fullName}   (no contact PII — see identifiedBy)
//	vtx.provider.<id>     class=provider     root {}   .profile {fullName, specialty, credentials?, bio?}
//	vtx.appointment.<id>  class=appointment  root {}   .schedule  {startsAt, endsAt, remindAt, reason?}
//	                                                    .status    {value ∈ scheduled|confirmed|checkedIn|completed|cancelled|noShow, note?}
//	                                                    .encounter {summary, assessment?, plan? (RAW PHI, never projected);
//	                                                                documentedAt, followUpRequested, followUpDate? (operational, projected)}
//	lnk.appointment.<id>.forPatient.patient.<id>       (appointment → patient, later-arriving source)
//	lnk.appointment.<id>.withProvider.provider.<id>    (appointment → provider, later-arriving source)
//	lnk.patient.<id>.identifiedBy.identity.<id>        (patient → identity, optional — wired by CreatePatient's identityKey)
//
// The clinic's booking grid is a mandatory 15-minute cadence: double-book
// detection is a WRITE-PATH deterministic-key claim, not read-time link
// enumeration. Both the provider and the patient hub carry one .slot<cellcode>
// existence-marker aspect (providerSlotClaim / patientSlotClaim) per occupied
// 15-minute cell — the SAME key across two competing bookings for that cell, so
// CreateOnly/expectedRevision conditioning at commit IS the double-book lock (see
// clinic-booking-write-path-slot-claims-design.md).
//
// Twelve ops (known-key kv.Read only — no kv.Links enumeration, no raw prefix
// scans):
//
//	CreatePatient / TombstonePatient
//	CreateProvider / TombstoneProvider / SetProviderProfile (full-replace upsert of .profile) /
//	  SetProviderHours (upsert the opt-in .hours weekly windows) /
//	  SetProviderTimeOff (upsert the opt-in .timeOff blackout ranges)
//	CreateAppointment (mints the appointment + .schedule + .status{scheduled} + both links, validating
//	                   patient + provider alive + class, and rejecting a past time / out-of-hours /
//	                   time-off / provider-double-book / patient-double-book) / RescheduleAppointment
//	                   (rewrite .schedule with new times — same guard chain — re-deriving remindAt so the
//	                   @at reminder re-arms) / SetAppointmentStatus (upsert .status{value, note?}) /
//	                   RecordEncounter (upsert .encounter — the post-visit clinical record: RAW PHI
//	                   {summary, assessment?, plan?} captured plaintext-for-now and NEVER projected, plus
//	                   the operational {documentedAt, followUpRequested, followUpDate?} the lens DOES
//	                   project) / TombstoneAppointment
//
// Three PROJECTION lenses are the P5 query surface a clinic FE reads (never Core
// KV): clinicAppointments (one row per appointment, joined to patient + provider),
// clinicProviders (the provider roster / booking picker), and clinicPatients (the
// patient-context switcher — NAME only, no PHI).
//
// OUT of scope (the separate deferred items this vertical FORCES, not implements):
//   - PHI / sensitive aspects + Vault / crypto-shred, PARTIALLY wired. All
//     aspects here stay NON-sensitive in the step-6 sense (patient/appointment
//     are not identity vertices, so step-6's sensitiveAspectScope forbids a
//     sensitive aspect on them anyway): .demographics carries only fullName.
//     Sensitive contact (email/phone) is CreatePatient's optional identityKey
//     — a pre-minted vtx.identity (identity-domain's CreateUnclaimedIdentity)
//     linked via identifiedBy, the Vault plane's crypto-shreddable unit.
//     Contact DISPLAY is wired: clinicPatientsRead's email/phone are
//     Secure-Lens columns (Vault Fire 5), decrypted at projection from the
//     linked identity for the staff-wildcard audience only. The post-visit
//     clinical record (.encounter — summary / assessment / plan) is still
//     stored plain under the trusted-tool posture and DELIBERATELY NOT
//     projected into any read model; right-to-be-forgotten for it +
//     clinical-note display remain the still-deferred Vault plane work
//     (clinic remains its forcing function for that piece). RecordEncounter
//     captures the record now and projects ONLY the operational, non-PHI
//     signals (documentation presence + follow-up scheduling) so the clinical
//     display stays Vault-gated.
//   - @every scheduling — genuinely unneeded here. Recurring *availability* (a
//     provider's weekly hours) is NOT a timer: .hours stores a static weekly
//     template (windows: [{day, openSec, closeSec}]) enforced at op time
//     (CreateAppointment / RescheduleAppointment), with no schedule to arm. A
//     recurring *visit series* (a patient on a standing cadence) is a genuinely
//     different, timer-like need — built as a package-level rolling-@at convergence
//     series in the sibling clinic-reminders package (visitseries.go), NOT @every;
//     see clinic-recurring-visit-series-design.md §3 for why @every (a per-entity
//     substrate schedule) is the wrong tool for a per-series recurring deadline. Op-time
//     double-book rejection (CreateAppointment + RescheduleAppointment, by claiming a
//     deterministic per-15-minute-cell slot-claim aspect on the provider and patient
//     hubs) and provider business-hours rejection (the opt-in .hours windows) ARE
//     enforced here, via "the operation's own Starlark logic" (§06's sanctioned path).
//   - One-shot @at appointment reminders ("remind 24h before") ARE built — in the
//     sibling clinic-reminders package, which reads the .schedule remindAt this DDL
//     precomputes.
//   - A Weaver convergence lens / orchestrated clinic workflow IN THIS PACKAGE
//     (clinic-domain stays projection-only); the clinic-reminders sibling package
//     owns the appointment-reminder convergence lens + its directOp playbook.
//   - Cascade-on-tombstone. Tombstone{Patient,Provider,Appointment} soft-deletes
//     ONLY the named vertex root — its aspects and incident links are left in
//     place (the projection lenses anchor on the live root, so a tombstoned
//     vertex simply drops from the read model and its orphaned aspects/links are
//     not surfaced). This matches location-domain / lease-signing: there is no
//     platform owner-tombstone-cascade trigger (it is the deferred GC item), so
//     no package builds a bespoke one.
//
// Multi-site: a fourth vertexType DDL (clinicSite) + one aspectType DDL
// (clinicSiteProfile) contribute a `.site` aspect {name} onto a location-domain
// vtx.building (SetSiteProfile), and a fifth vertexType DDL (clinicSiteAssignment)
// owns the provider→building `practicesAt` LINK (AssignProviderSite /
// RemoveProviderSite) — mirroring loftspace-domain's aspect-contribution
// (loftspaceListing) + link-contribution (loftspaceOwnership) pattern onto
// location-domain's place graph exactly, including the create/revive-CAS/no-op
// idempotency ownership.go uses. This package now DEPENDS on location-domain.
//
// Install via `lattice-pkg install packages/location-domain` THEN
// `lattice-pkg install packages/clinic-domain`. See
// _bmad-output/implementation-artifacts/clinic-domain-design.md +
// clinic-multisite-design.md.
package clinicdomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "clinic-domain",
	Depends:     []string{"location-domain"},
	Version:     "0.17.0",
	Description: "Clinic bookable domain: patient / provider / appointment vertex types + their aspects and links, written by Create*/SetAppointmentStatus/RecordEncounter/Tombstone* ops. RecordEncounter captures the post-visit clinical record (.encounter — RAW PHI never projected, plus operational documentation/follow-up signals the lens does project). Multi-site: SetSiteProfile writes a `.site` aspect {name} onto a location-domain vtx.building; AssignProviderSite/RemoveProviderSite own the provider→building `practicesAt` link (create/revive-CAS/no-op idempotency, mirrors loftspace-domain's AssignUnitOwner). Ten projection lenses (clinicAppointments, clinicProviders, clinicPatients, clinicSites, providerSites, clinicAppointmentsRead, providerAppointmentsRead, clinicPatientsRead, clinicPatientReadGrants, clinicProviderReadGrants) are the P5 read models a clinic FE reads; clinicAppointmentsRead, providerAppointmentsRead, and clinicPatientsRead are PROTECTED Postgres read models (Contract #6 §6.14 RLS, D1.5) — patient-self, provider-self, and staff-wildcard-only respectively. clinicPatientReadGrants / clinicProviderReadGrants are the package's own cap-read.clinic.patient / cap-read.clinic.provider GrantTable self-anchor producers, closing the gap the platform base cap-read self-anchor leaves (it only ever matches class=identity, never class=patient/class=provider). clinicPatientsRead's email/phone are Secure-Lens columns (Contract #3 §3.10, Vault Fire 5): decrypted at projection from the patient's optional identifiedBy identity, null for a patient with no linked identity or a shredded one. CreateAppointment, RescheduleAppointment, and SetAppointmentStatus ALSO grant consumer scope=self (real-actor-write-auth-e2e idiom): a real patient books, reschedules, or cancels their own appointment through the Gateway, gated on the named/actual patient's identifiedBy link resolving to the caller's own identity; SetAppointmentStatus's self grant is further restricted in-script to status=cancelled only. Depends on location-domain (the multi-site building).",
	DDLs:        DDLs(),
	Lenses:      Lenses(),
	Permissions: Permissions(),
}
