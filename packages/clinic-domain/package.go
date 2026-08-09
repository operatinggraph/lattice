// Package clinicdomain is the clinic-domain Capability Package — the bookable
// foundation of the clinic vertical (patient / provider / appointment).
//
// Unlike loftspace-domain (which adds aspects onto location-domain's units),
// clinic-domain is SELF-CONTAINED: it owns three vertex types, mirroring
// location-domain's "own your domain's vertex types" precedent.
//
//	vtx.patient.<id>      class=patient      root {}   .demographics {fullName}   (no contact PII — see identifiedBy)
//	vtx.provider.<id>     class=provider     root {}   .profile {fullName, specialty, credentials?, bio?}
//	vtx.appointment.<id>  class=appointment  root {}   .schedule      {startsAt, endsAt, remindAt, reason?}
//	                                                    .status        {value ∈ scheduled|confirmed|checkedIn|completed|cancelled|noShow, note?}
//	                                                    .encounter     {summary, assessment, plan} (SENSITIVE, DEK custodied on the
//	                                                                    clinicalRecord retention class, never on the patient's identity;
//	                                                                    read back only through clinicEncountersRead, decrypted at projection)
//	                                                    .documentation {documentedAt, followUpRequested, followUpDate?} (operational,
//	                                                                    non-PHI, projected)
//	lnk.appointment.<id>.forPatient.patient.<id>       (appointment → patient, later-arriving source)
//	lnk.appointment.<id>.withProvider.provider.<id>    (appointment → provider, later-arriving source)
//	lnk.patient.<id>.identifiedBy.identity.<id>        (patient → identity, optional — wired by CreatePatient's identityKey)
//	lnk.provider.<id>.identifiedBy.identity.<id>       (provider → identity, wired by BindProviderIdentity — the provider hat's login binding)
//
// The clinic's booking grid is a mandatory 15-minute cadence: double-book
// detection is a WRITE-PATH deterministic-key claim, not read-time link
// enumeration. Both the provider and the patient hub carry one .slot<cellcode>
// existence-marker aspect (providerSlotClaim / patientSlotClaim) per occupied
// 15-minute cell — the SAME key across two competing bookings for that cell, so
// CreateOnly/expectedRevision conditioning at commit IS the double-book lock (see
// clinic-booking-write-path-slot-claims-design.md).
//
// Fourteen ops (known-key kv.Read only — no kv.Links enumeration, no raw prefix
// scans — save the two bounded single-relation kv.Links reads §2.5 class (e)
// already sanctions: appointment_provider/appointment_patient):
//
//	CreatePatient / TombstonePatient
//	CreateProvider / TombstoneProvider / SetProviderProfile (full-replace upsert of .profile) /
//	  SetProviderHours (upsert the opt-in .hours weekly windows) /
//	  SetProviderTimeOff (upsert the opt-in .timeOff blackout ranges) /
//	  BindProviderIdentity (identifiedBy + mutual-exclusivity claims + idempotent provider-role grant)
//	CreateAppointment (mints the appointment + .schedule + .status{scheduled} + both links, validating
//	                   patient + provider alive + class, and rejecting a past time / out-of-hours /
//	                   time-off / provider-double-book / patient-double-book) / RescheduleAppointment
//	                   (rewrite .schedule with new times — same guard chain — re-deriving remindAt so the
//	                   @at reminder re-arms) / SetAppointmentStatus (upsert .status{value, note?}) /
//	                   RecordEncounter (upsert two sibling aspects — .encounter, the SENSITIVE raw
//	                   clinical record {summary, assessment, plan}, DEK custodied on the clinicalRecord
//	                   retention class, projected only by clinicEncountersRead; and .documentation, the operational
//	                   {documentedAt, followUpRequested, followUpDate?} the lens DOES project) /
//	                   MarkPastDueNoShow (Weaver-dispatched only — clinic-reminders'
//	                   pastDueAppointments target's directOp, once a non-terminal appointment's
//	                   endsAt passes unattended; same noShow effect as SetAppointmentStatus, provider/
//	                   patient resolved live instead of caller-supplied) / BackfillAppointmentSite
//	                   (Weaver-dispatched only — this package's own clinicSiteBackfill target's
//	                   directOp, backfilling the atSite link CreateAppointment's own optional site
//	                   branch would have written) / TombstoneAppointment
//
// Three PROJECTION lenses are the P5 query surface a clinic FE reads (never Core
// KV): clinicAppointments (one row per appointment, joined to patient + provider),
// clinicProviders (the provider roster / booking picker), and clinicPatients (the
// patient-context switcher — NAME only, no PHI).
//
// OUT of scope (the separate deferred items this vertical FORCES, not implements):
//   - PHI / sensitive aspects + Vault / crypto-shred, PARTIALLY wired. Most
//     aspects here stay NON-sensitive in the step-6 sense (patient/appointment
//     are not identity vertices, so step-6's sensitiveAspectScope forbids a
//     sensitive aspect on them anyway): .demographics carries only fullName.
//     Sensitive contact (email/phone) is CreatePatient's optional identityKey
//     — a pre-minted vtx.identity (identity-domain's CreateUnclaimedIdentity)
//     linked via identifiedBy, the Vault plane's crypto-shreddable unit.
//     Contact DISPLAY is wired: clinicPatientsRead's email/phone are
//     Secure-Lens columns (Vault Fire 5), decrypted at projection from the
//     linked identity for the staff-wildcard audience only. The post-visit
//     clinical record (.encounter — summary / assessment / plan) is the ONE
//     exception to the non-sensitive rule: it is SENSITIVE, its DEK custodied
//     on the package's own clinicalRecord retention class (a
//     vtx.retentionclass.<NanoID> holder, not the patient's identity) —
//     encrypted at rest, and projected — decrypted, to the treating provider
//     alone — only by the clinicEncountersRead Secure Lens. That custody
//     choice is deliberate: the record's retention obligation outlives any one
//     patient's erasure request, so ShredIdentityKey on the patient leaves the
//     record readable. That survival is NOT yet pseudonymous — .demographics'
//     fullName is plaintext outside the identity the shred reaches, and
//     projects onto the same read-model rows as the visit, so a shredded
//     patient's retained record is still identified; RetentionClasses'
//     Description states both the obligation and this open gap.
//     Clinical-note DISPLAY (rendering
//     the decrypted content to a clinician) remains the still-deferred Vault
//     plane work; RecordEncounter's own .documentation aspect projects only
//     the operational, non-PHI signals (documentation presence + follow-up
//     scheduling) so the read models stay Vault-gated for the clinical
//     content itself.
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

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:             "clinic-domain",
	Depends:          []string{"location-domain"},
	Version:          "0.30.0",
	Description:      "Clinic bookable domain: patient / provider / appointment vertex types + their aspects and links, written by Create*/SetAppointmentStatus/RecordEncounter/Tombstone* ops. RecordEncounter captures the post-visit clinical record split across two sibling aspects along the sensitivity boundary: .encounter (RAW PHI, SENSITIVE, DEK custodied on the package's own clinicalRecord retention class rather than the patient's identity — the record survives the patient's erasure — read back only through the clinicEncountersRead Secure Lens, which decrypts it at projection for the treating provider) and .documentation (the operational documentation/follow-up signals the lens does project). Multi-site: SetSiteProfile writes a `.site` aspect {name} onto a location-domain vtx.building; AssignProviderSite/RemoveProviderSite own the provider→building `practicesAt` link (create/revive-CAS/no-op idempotency, mirrors loftspace-domain's AssignUnitOwner). BindProviderIdentity binds a provider to a login identity (identifiedBy + CreateOnly guards on both sides + an idempotent grant of the identity-domain `provider` role); SetProviderHours, SetProviderTimeOff, SetAppointmentStatus, RescheduleAppointment, and RecordEncounter additionally grant `provider` scope=any, confined in-script to the caller's OWN bound provider (persona-worlds-design.md Fire W0). clinicProviders also projects each provider's optional identifiedBy identityKey (public — a provider directory entry is already public, unlike patient PHI) so a signed-in provider session can resolve its own name. Fourteen projection lenses (clinicAppointments, clinicProviders, clinicPatients, clinicSites, providerSites, clinicAppointmentsRead, providerAppointmentsRead, clinicPatientsRead, clinicEncountersRead, clinicPatientReadGrants, clinicProviderReadGrants, providerIdentityReadGrants, patientIdentityReadGrants, clinicSiteBackfill) are the P5 read models a clinic FE reads; clinicAppointmentsRead, providerAppointmentsRead, clinicPatientsRead, and clinicEncountersRead are PROTECTED Postgres read models (Contract #6 §6.14 RLS, D1.5) — patient-self, provider-self, and patient-self-plus-workplace-plus-staff-wildcard respectively (each roster row anchors on its own patient PLUS every building a provider it has an appointment with practises at, so a worksAt-anchored front-desk actor reads every patient touching its building via service-location's staffReadGrants, a WildcardAnchor holder reads the whole roster, and a patient reads only their own record). clinicPatientReadGrants / clinicProviderReadGrants are the package's own cap-read.clinic.patient / cap-read.clinic.provider GrantTable self-anchor producers, closing the gap the platform base cap-read self-anchor leaves (it only ever matches class=identity, never class=patient/class=provider); providerIdentityReadGrants and patientIdentityReadGrants are the sibling actor-different-from-anchor producers that let a signed-in login inherit the anchor tokens of the clinic entity it is bound to — a BindProviderIdentity-bound provider and an identifiedBy-bound patient respectively (mirrors service-location's staffReadGrants shape). They are what make a person's LOGIN the principal of their protected reads instead of the clinic entity standing in as its own actor. clinicPatientsRead's email/phone are Secure-Lens columns (Contract #3 §3.10, Vault Fire 5): decrypted at projection from the patient's optional identifiedBy identity, null for a patient with no linked identity or a shredded one. clinicEncountersRead is the package's second Secure Lens and the ONLY read path to .encounter's clinical content: provider-anchored (the treating provider and a WildcardAnchor holder, never the front desk), its summary/assessment/plan columns declare holder type retentionclass, so the ciphertext is opened under the clinicalRecord class key and a ShredRetentionClassKey nulls all three. CreateAppointment, RescheduleAppointment, and SetAppointmentStatus ALSO grant consumer scope=self (real-actor-write-auth-e2e idiom): a real patient books, reschedules, or cancels their own appointment through the Gateway, gated on the named/actual patient's identifiedBy link resolving to the caller's own identity; SetAppointmentStatus's self grant is further restricted in-script to status=cancelled only. MarkPastDueNoShow is the Weaver-dispatched-only counterpart to SetAppointmentStatus(noShow) — clinic-reminders' pastDueAppointments target's directOp, dispatched once a non-terminal appointment's endsAt passes with no staff status update; it resolves provider/patient LIVE off the appointment's own links (rather than caller-supplied + link-validated, which Weaver's directOp Reads templating cannot express) and no-ops if the appointment already reached a terminal status by dispatch time. BackfillAppointmentSite is this package's own Weaver-dispatched-only auto-remediation twin of CreateAppointment's own site-writing branch — the clinicSiteBackfill target's directOp, dispatched for a live appointment carrying no atSite link; it backfills the link only when the appointment's provider practicesAt EXACTLY ONE site, no-opping cleanly (never guessing) when that provider practises at zero or several. Depends on location-domain (the multi-site building).",
	DDLs:             DDLs(),
	Lenses:           Lenses(),
	Permissions:      Permissions(),
	WeaverTargets:    WeaverTargets(),
	OpMetas:          OpMetas(),
	RetentionClasses: RetentionClasses(),
}
