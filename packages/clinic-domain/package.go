// Package clinicdomain is the clinic-domain Capability Package — the bookable
// foundation of the clinic vertical (patient / provider / appointment).
//
// Unlike loftspace-domain (which adds aspects onto location-domain's units),
// clinic-domain is SELF-CONTAINED: it owns three vertex types, mirroring
// location-domain's "own your domain's vertex types" precedent.
//
//	vtx.patient.<id>      class=patient      root {}   .demographics {fullName, dob?, email?, phone?}
//	vtx.provider.<id>     class=provider     root {}   .profile {fullName, specialty, credentials?, bio?}
//	vtx.appointment.<id>  class=appointment  root {}   .schedule  {startsAt, endsAt, remindAt, reason?}
//	                                                    .status    {value ∈ scheduled|confirmed|checkedIn|completed|cancelled|noShow, note?}
//	                                                    .encounter {summary, assessment?, plan? (RAW PHI, never projected);
//	                                                                documentedAt, followUpRequested, followUpDate? (operational, projected)}
//	lnk.appointment.<id>.forPatient.patient.<id>       (appointment → patient, later-arriving source)
//	lnk.appointment.<id>.withProvider.provider.<id>    (appointment → provider, later-arriving source)
//	lnk.provider.<id>.hasBooking.appointment.<id>      (provider → appointment, hub-sourced; the enumeration prefix)
//	lnk.patient.<id>.hasBooking.appointment.<id>       (patient → appointment, hub-sourced)
//
// The booking TOPOLOGY lives in the hub-sourced hasBooking links (enumerated at
// write time via kv.Links, Contract #2 §2.5.1 — a bounded prefix). Both patient and
// provider also carry a {epoch:0}-initialized .bookingGuard scalar, the OCC
// serialization point the double-book guards bump (topology→links, lock→epoch).
//
// Twelve ops (known-key kv.Read + the one sanctioned bounded kv.Links enumeration,
// no raw prefix scans):
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
//   - PHI / sensitive aspects + Vault / crypto-shred. All aspects here are
//     NON-sensitive in the step-6 sense (patient/appointment are not identity
//     vertices, so step-6's sensitiveAspectScope forbids a sensitive aspect on them
//     anyway). DOB / contact (.demographics) AND the post-visit clinical record
//     (.encounter — summary / assessment / plan) are stored plain under the
//     trusted-tool posture and DELIBERATELY NOT projected into any read model; real
//     PHI handling + right-to-be-forgotten + clinical-content DISPLAY is the deferred
//     Vault plane (clinic is its forcing function — patient-record deletion and
//     clinical-note display are its validating flows). RecordEncounter captures the
//     record now and projects ONLY the operational, non-PHI signals (documentation
//     presence + follow-up scheduling) so the clinical display stays Vault-gated.
//   - @every scheduling — genuinely unneeded here. Recurring *availability* (a
//     provider's weekly hours) is NOT a timer: .hours stores a static weekly
//     template (windows: [{day, openSec, closeSec}]) enforced at op time
//     (CreateAppointment / RescheduleAppointment), with no schedule to arm. A
//     recurring *visit series* (a patient on a standing cadence) is a genuinely
//     different, timer-like need — built as a package-level rolling-@at convergence
//     series in the sibling clinic-reminders package (visitseries.go), NOT @every;
//     see clinic-recurring-visit-series-design.md §3 for why @every (a per-entity
//     substrate schedule) is the wrong tool for a per-series recurring deadline. Op-time
//     double-book rejection (CreateAppointment + RescheduleAppointment, by enumerating
//     the hasBooking links serialized on the .bookingGuard epoch) and provider
//     business-hours rejection (the opt-in .hours windows) ARE enforced here, via "the
//     operation's own Starlark logic" (§06's sanctioned path).
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
// Install via `lattice-pkg install packages/clinic-domain`. Self-contained — no
// package dependency. See _bmad-output/implementation-artifacts/clinic-domain-design.md.
package clinicdomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "clinic-domain",
	Version:     "0.10.0",
	Description: "Clinic bookable domain: patient / provider / appointment vertex types + their aspects and links, written by Create*/SetAppointmentStatus/RecordEncounter/Tombstone* ops. RecordEncounter captures the post-visit clinical record (.encounter — RAW PHI never projected, plus operational documentation/follow-up signals the lens does project). Five projection lenses (clinicAppointments, clinicProviders, clinicPatients, clinicAppointmentsRead, providerAppointmentsRead) are the P5 read models a clinic FE reads; clinicAppointmentsRead and providerAppointmentsRead are the patient-self and provider-self PROTECTED Postgres read models (Contract #6 §6.14 RLS, D1.5). Self-contained — no package dependency.",
	DDLs:        DDLs(),
	Lenses:      Lenses(),
	Permissions: Permissions(),
}
