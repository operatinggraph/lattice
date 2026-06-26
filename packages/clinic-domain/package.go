// Package clinicdomain is the clinic-domain Capability Package — the bookable
// foundation of the clinic vertical (patient / provider / appointment).
//
// Unlike loftspace-domain (which adds aspects onto location-domain's units),
// clinic-domain is SELF-CONTAINED: it owns three vertex types, mirroring
// location-domain's "own your domain's vertex types" precedent.
//
//	vtx.patient.<id>      class=patient      root {}   .demographics {fullName, dob?, email?, phone?}
//	vtx.provider.<id>     class=provider     root {}   .profile {fullName, specialty, credentials?, bio?}
//	vtx.appointment.<id>  class=appointment  root {}   .schedule {startsAt, endsAt, remindAt, reason?}
//	                                                    .status   {value ∈ scheduled|confirmed|completed|cancelled|noShow}
//	lnk.appointment.<id>.forPatient.patient.<id>       (appointment → patient, later-arriving source)
//	lnk.appointment.<id>.withProvider.provider.<id>    (appointment → provider, later-arriving source)
//
// Eight ops (all known-key-reads, no prefix scans):
//
//	CreatePatient / TombstonePatient
//	CreateProvider / TombstoneProvider
//	CreateAppointment (mints the appointment + .schedule + .status{scheduled} + both links, validating
//	                   patient + provider alive + class) / RescheduleAppointment (rewrite .schedule with new
//	                   times, re-deriving remindAt so the @at reminder re-arms) / SetAppointmentStatus
//	                   (upsert .status) / TombstoneAppointment
//
// Two PROJECTION lenses are the P5 query surface a clinic FE reads (never Core
// KV): clinicAppointments (one row per appointment, joined to patient + provider)
// and clinicProviders (the provider roster / booking picker).
//
// OUT of scope (the separate deferred items this vertical FORCES, not implements):
//   - PHI / sensitive aspects + Vault / crypto-shred. All aspects here are
//     NON-sensitive (patient is not an identity vertex, so step-6's
//     sensitiveAspectScope forbids a sensitive aspect on it anyway). DOB / contact
//     are stored plain under the trusted-tool posture; real PHI handling +
//     right-to-be-forgotten is the deferred Vault plane (clinic is its forcing
//     function — patient-record deletion is its validating flow).
//   - Availability / double-book / provider-hours enforcement (Capability-KV §06
//     defers temporal/uniqueness). CreateAppointment records a requested time; it
//     does NOT reject an overlapping or out-of-hours slot.
//   - Recurring @every reminders / availability (@every has no consumer; §10.4
//     ships @at one-shot). One-shot @at appointment reminders ("remind 24h before")
//     ARE built — in the sibling clinic-reminders package, which reads the .schedule
//     remindAt this DDL precomputes. The recurring-availability case still needs @every.
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
	Version:     "0.1.0",
	Description: "Clinic bookable domain: patient / provider / appointment vertex types + their aspects and links, written by Create*/SetAppointmentStatus/Tombstone* ops. Two projection lenses (clinicAppointments, clinicProviders) are the P5 read models a clinic FE reads. Self-contained — no package dependency.",
	DDLs:        DDLs(),
	Lenses:      Lenses(),
	Permissions: Permissions(),
}
