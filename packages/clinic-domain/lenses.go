package clinicdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// ClinicAppointmentsBucket is the NATS-KV read model the clinicAppointments lens
// projects into. It is the **P5 query surface** for "what appointments exist" — a
// clinic FE reads THIS projected bucket (one row per appointment, keyed by the
// appointment key, each carrying patientKey / providerKey for client-side scoping
// of "my appointments" / "provider schedule"), never Core KV
// (lattice-architecture.md P5 — lenses are the only application query surface).
// The Refractor auto-creates the bucket on lens load.
const ClinicAppointmentsBucket = "clinic-appointments"

// ClinicProvidersBucket is the NATS-KV read model the clinicProviders lens
// projects into — the **P5 query surface** for "who can I book with": the booking
// UI reads THIS bucket (one row per named provider, keyed by the provider key) to
// render a human-readable provider picker, never Core KV.
const ClinicProvidersBucket = "clinic-providers"

// ClinicPatientsBucket is the NATS-KV read model the clinicPatients lens projects
// into — the **P5 query surface** for "who are the patients": the clinic FE reads
// THIS bucket (one row per named patient, keyed by the patient key) to render the
// patient-context switcher and to scope a patient's appointments, never Core KV.
// It projects the patient NAME only — DOB / contact are the PHI the deferred Vault
// plane owns and are deliberately NOT fanned into a read model here.
const ClinicPatientsBucket = "clinic-patients"

// ClinicSitesBucket is the NATS-KV read model the clinicSites lens projects
// into — the **P5 query surface** for "what clinic sites exist": a site
// directory / (a later increment's) site-scoped booking picker reads THIS
// bucket (one row per named site, keyed by the location-domain building key),
// never Core KV.
const ClinicSitesBucket = "clinic-sites"

// ClinicProviderSitesBucket is the NATS-KV read model the providerSites lens
// projects into — the **P5 query surface** for "which providers practice at
// which sites": one row per (provider, site) pair, mirroring identity-
// hygiene's duplicateCandidates shape (composite IntoKey, DiffRetraction).
const ClinicProviderSitesBucket = "clinic-provider-sites"

// ClinicSiteBackfillTarget is the §10.8 TargetID == the clinicSiteBackfill
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads
// (targets.go), the cafeStaleTabSettlement idiom applied to this package's
// own appointment shape.
const ClinicSiteBackfillTarget = "clinicSiteBackfill"

// Lenses returns the package's projection lenses. Most are flat projections
// (no aggregation / WITH), so OPTIONAL-matched neighbour bindings are live
// directly in RETURN and the §4-B1 "WITH-drop" hazard does not apply.
// clinicPatientsReadSpec is the exception — its WITH re-projects p and id
// explicitly (see its own doc comment), so the hazard is avoided by carrying
// the needed bindings through rather than by skipping WITH. Aspect fields are
// read by the documented node.<aspect>.data.<field> form (the same access
// loftspace-domain / lease-signing use), including neighbour aspect-hops
// (lease-signing reads id.ssn.data.value off an OPTIONAL-matched identity the
// same way).
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "clinicAppointments",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        ClinicAppointmentsBucket,
			Engine:        "full",
			Spec:          clinicAppointmentsSpec,
		},
		{
			CanonicalName: "clinicProviders",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        ClinicProvidersBucket,
			Engine:        "full",
			Spec:          clinicProvidersSpec,
		},
		{
			CanonicalName: "clinicPatients",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        ClinicPatientsBucket,
			Engine:        "full",
			Spec:          clinicPatientsSpec,
		},
		{
			CanonicalName: "clinicSites",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        ClinicSitesBucket,
			Engine:        "full",
			Spec:          clinicSitesSpec,
		},
		{
			CanonicalName:  "providerSites",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         ClinicProviderSitesBucket,
			Engine:         "full",
			Spec:           providerSitesSpec,
			IntoKey:        []string{"provider_id", "site_id"},
			DiffRetraction: true,
		},
		{
			// clinicSiteBackfill — the missing_site convergence lens
			// (cafeStaleTabSettlement's idiom, cafe-domain/lenses.go, applied
			// to this package's own appointment shape): flags a LIVE
			// appointment carrying no atSite link, dispatching
			// BackfillAppointmentSite (ddls.go, targets.go) to backfill the
			// link CreateAppointment's own optional site branch would have
			// written had a site been supplied at booking time. See
			// clinicSiteBackfillSpec below for the non-convergence safety
			// note (an appointment whose provider practises at zero or
			// several sites stays missing_site forever, harmlessly).
			CanonicalName:  ClinicSiteBackfillTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           clinicSiteBackfillSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "appointment",
				OutputKeyPattern: ClinicSiteBackfillTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_site", "entityKey", "appointmentKey", "status"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
		{
			// clinicAppointmentsRead — the protected Postgres read model for the
			// patient-facing "My Appointments" view (D1.5, mirroring D1.3 Fire 2's
			// applicant-self milestone). Contract #6 §6.14: protected-by-default,
			// one authz_anchors set of bare-NanoID match tokens per row, RLS
			// returning only rows the reading actor is granted.
			//
			// This is the PATIENT-SELF audience only. cmd/clinic-app's
			// handleAppointments today lists the unprotected clinicAppointments
			// NATS-KV bucket and lets ANY caller pass `?patient=<any patient>` to
			// read that patient's full appointment history — including the
			// operational post-visit signals (documentedAt/followUpRequested) —
			// with no authentication at all. handleMyAppointments (D1.5) replaces
			// that vector for the patient's own view: RLS scopes the read to the
			// verified JWT subject, so a caller cannot request another patient's
			// rows. The PROVIDER audience moved to providerAppointmentsRead below
			// (a provider-self anchor); the clinic-wide STAFF views (follow-ups
			// worklist, the "All providers" schedule aggregate) moved to
			// cmd/clinic-app's handleStaffAppointments, reading THIS SAME table
			// (no per-row anchor needed for staff — the reserved WildcardAnchor
			// grant, D1 design §3.4 M5, matches every row regardless of its
			// authz_anchors; see internal/bootstrap.
			// CapabilityReadWildcardGrantsLensDefinition).
			//
			// authz_anchors = [nanoIdFromKey(p.key)] — the patient's OWN
			// NanoID (never a linked contact identity's — cmd/clinic-app mints
			// the JWT subject as the patient's own bare NanoID, app.js's
			// bareId(state.patient)). The platform's base cap-read self-anchor
			// (D1.1) only ever matches class=identity, so it does NOT grant a
			// patient (class=patient) its own anchor — clinicPatientReadGrants
			// below is clinic-domain's own cap-read.clinic.patient producer that
			// closes that gap; without it this table's rows are unreadable by
			// anyone but a WildcardAnchor holder.
			//
			// Adapter postgres + Protected: Refractor provisions the RLS table
			// (FORCE ROW LEVEL SECURITY + the policy) from Columns at activation,
			// mirroring lease-signing's leaseApplicationsRead exactly. DSN is
			// left empty: Refractor resolves it from REFRACTOR_PG_DSN.
			//
			// forPatient is a REQUIRED match (the anchor walk) so an appointment
			// with no patient link projects NO row — fail-closed, never a null
			// authz_anchor. withProvider stays OPTIONAL (a display-only neighbour,
			// like leaseApplicationsRead's unit walk), matching the existing
			// clinicAppointments lens's null-safety for an incomplete appointment.
			CanonicalName: "clinicAppointmentsRead",
			Class:         "meta.lens",
			Adapter:       "postgres",
			Table:         "read_clinic_appointments",
			Engine:        "full",
			Spec:          clinicAppointmentsReadSpec,
			Protected:     true,
			IntoKey:       []string{"appointment_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "entity_key", Type: "text"},
				{Name: "starts_at", Type: "text"},
				{Name: "ends_at", Type: "text"},
				{Name: "reason", Type: "text"},
				{Name: "status", Type: "text"},
				{Name: "status_note", Type: "text"},
				{Name: "patient_key", Type: "text"},
				{Name: "patient_name", Type: "text"},
				{Name: "unlinked_patient_name", Type: "text"},
				{Name: "provider_key", Type: "text"},
				{Name: "provider_name", Type: "text"},
				{Name: "provider_specialty", Type: "text"},
				{Name: "site_key", Type: "text"},
				{Name: "site_name", Type: "text"},
				{Name: "reminder_sent_at", Type: "text"},
				{Name: "follow_up_reminder_sent_at", Type: "text"},
				{Name: "documented_at", Type: "text"},
				{Name: "follow_up_requested", Type: "boolean"},
				{Name: "follow_up_date", Type: "text"},
			},
			SecureColumns: []pkgmgr.SecureColumn{
				{Column: "patient_name", HolderTypes: []string{"identity"}, Field: "value"},
			},
		},
		{
			// providerAppointmentsRead — the protected Postgres read model for the
			// provider-facing "My Schedule" view (D1.5, closing the provider vector
			// cmd/clinic-app's handleAppointments doc-comment flagged: `?provider=`
			// let ANY caller read a named provider's full schedule — including
			// every patient's name and the post-visit documentedAt/followUpRequested
			// signals — with no authentication at all). Mirrors
			// landlordLeaseApplicationsRead's Increment 2 shape (a self-anchor
			// table), but — unlike the loftspace/lease-signing landlord case —
			// a provider is NOT an identity (class=provider), so it needs its
			// own grant producer, same as clinicAppointmentsRead's patient
			// anchor: clinicProviderReadGrants below.
			//
			// The clinic-wide staff views read clinicAppointmentsRead ABOVE (via
			// handleStaffAppointments' wildcard grant), not this provider-anchored
			// table — a staff actor's wildcard grant matches every protected
			// table, so no separate staff projection is needed here.
			//
			// authz_anchors = [nanoIdFromKey(pr.key)] — the provider's own NanoID.
			//
			// withProvider is a REQUIRED match (the anchor walk) so an appointment
			// with no provider link projects NO row — fail-closed, mirroring
			// clinicAppointmentsRead's REQUIRED forPatient walk. forPatient stays
			// OPTIONAL: a display-only neighbour, not the anchor.
			CanonicalName: "providerAppointmentsRead",
			Class:         "meta.lens",
			Adapter:       "postgres",
			Table:         "read_provider_appointments",
			Engine:        "full",
			Spec:          providerAppointmentsReadSpec,
			Protected:     true,
			IntoKey:       []string{"appointment_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "entity_key", Type: "text"},
				{Name: "starts_at", Type: "text"},
				{Name: "ends_at", Type: "text"},
				{Name: "reason", Type: "text"},
				{Name: "status", Type: "text"},
				{Name: "status_note", Type: "text"},
				{Name: "patient_key", Type: "text"},
				{Name: "patient_name", Type: "text"},
				{Name: "unlinked_patient_name", Type: "text"},
				{Name: "provider_key", Type: "text"},
				{Name: "provider_name", Type: "text"},
				{Name: "provider_specialty", Type: "text"},
				{Name: "site_key", Type: "text"},
				{Name: "site_name", Type: "text"},
				{Name: "reminder_sent_at", Type: "text"},
				{Name: "follow_up_reminder_sent_at", Type: "text"},
				{Name: "documented_at", Type: "text"},
				{Name: "follow_up_requested", Type: "boolean"},
				{Name: "follow_up_date", Type: "text"},
			},
			SecureColumns: []pkgmgr.SecureColumn{
				{Column: "patient_name", HolderTypes: []string{"identity"}, Field: "value"},
			},
		},
		{
			// clinicPatientsRead — the protected Postgres read model for the
			// clinic-wide patient-context switcher (D1.5, mirroring the staff
			// wildcard increment: handleStaffAppointments / providerAppointmentsRead
			// above). cmd/clinic-app's handlePatients used to list the unprotected
			// clinicPatients NATS-KV bucket and serve every named patient's full
			// name to ANY caller with no authentication at all — a clinic-wide
			// membership-disclosure PHI dump (which patients exist at this clinic).
			// handleStaffPatients replaces that vector, reading THIS table as a
			// JWT-authenticated actor.
			//
			// Each row anchors on its own patient's NanoID plus every WORKPLACE
			// building a provider practises at, for a provider the patient has
			// an appointment with (mirrors clinicAppointmentsRead's own
			// practicesAt anchor exactly, one hop further out from patient to
			// appointment to provider) — service-location's staffReadGrants
			// (cap-read.staff) grants a front-desk actor a token per building
			// they worksAt, so a worksAt-anchored front-desk identity now
			// matches every patient whose care touches its building, not only
			// the reserved WildcardAnchor holder. Three kinds of actor match a
			// row: the WildcardAnchor holder (the whole roster), a
			// worksAt-anchored front-desk actor for a shared building (this
			// fix), and, via patientIdentityReadGrants, the signed-in identity
			// the row's own patient is identifiedBy. The roster as a WHOLE
			// still has no owner but a single row does, and a person being
			// able to find their own record is what lets a patient session
			// know which patient it is.
			//
			// NAME comes straight off .demographics (non-sensitive). email/phone
			// are SECURE columns (Contract #3 §3.10, Vault Fire 5 — the
			// Secure-Lens decrypt-at-projection primitive, mirroring
			// landlordLeaseApplicationsRead's applicant_email/applicant_phone
			// exactly): the OPTIONAL-matched identifiedBy identity's sensitive
			// .email/.phone aspects are RETURNed as ciphertext envelopes whole
			// (id.<aspect>.data) and decrypted at projection into this table.
			// Decrypted contact therefore reaches the same three readers as
			// the row itself: the WildcardAnchor holder, a worksAt-anchored
			// front-desk actor sharing a workplace building with the
			// patient's care — both the front desk this contact belongs to
			// operationally — and the person the row is about, reading their
			// own email and phone. A patient with no
			// identifiedBy link (identityKey null) or a shredded identity
			// projects null email/phone — never an error (right-to-erasure and
			// the pre-Vault/no-backfill posture both fall through the same
			// null path).
			//
			// DiffRetraction: true — clinicPatientsReadSpec's WITH (the
			// authz_anchors dedup) takes this rule out of the anchor-delete
			// fast path: anchorProjectionShape rejects any WITH-bearing query
			// wholesale (internal/refractor/ruleengine/full/anchor_delete.go),
			// so a TombstonePatient event can no longer resolve a Delete key
			// read-free and would otherwise leave the tombstoned patient's
			// row — carrying decrypted name/email/phone PHI — stale in
			// read_clinic_patients forever. Fire 3's target-diff retraction is
			// the alternative for exactly this shape (mirrors
			// loftspace-domain's landlordUnitsRead, packages/loftspace-domain/
			// lenses.go, same Protected+postgres+WITH-blocks-anchor-delete
			// shape); this query is genuinely unanchored (no $actorKey — a
			// whole-roster scan), which is what
			// ValidateUnanchoredForDiffRetraction requires, and both the
			// Postgres and Protected adapters implement adapter.KeyLister, so
			// the mechanism activates rather than installing dark.
			//
			// Cost: DiffRetraction disables both anchor seeding (rulestate.go)
			// and the plain-derivation narrowing licence (anchor_derivation_
			// plain.go) for this lens, so every reacting event — appointment,
			// building, identity, patient, or provider — now re-evaluates the
			// whole patient corpus plus a full ListKeys() of
			// read_clinic_patients, versus the single-patient seed the old
			// comprehension-only spec got. Priced against the roster sizes
			// this vertical carries (reference-vertical scale, not a claim
			// about how far it scales), not a defect.
			CanonicalName:  "clinicPatientsRead",
			Class:          "meta.lens",
			Adapter:        "postgres",
			Table:          "read_clinic_patients",
			Engine:         "full",
			Spec:           clinicPatientsReadSpec,
			Protected:      true,
			DiffRetraction: true,
			IntoKey:        []string{"patient_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "entity_key", Type: "text"},
				{Name: "patient_key", Type: "text"},
				{Name: "name", Type: "text"},
				{Name: "unlinked_name", Type: "text"},
				{Name: "identity_key", Type: "text"},
				{Name: "email", Type: "text"},
				{Name: "phone", Type: "text"},
			},
			SecureColumns: []pkgmgr.SecureColumn{
				{Column: "name", HolderTypes: []string{"identity"}, Field: "value"},
				{Column: "email", HolderTypes: []string{"identity"}, Field: "value"},
				{Column: "phone", HolderTypes: []string{"identity"}, Field: "value"},
			},
		},
		{
			// clinicEncountersRead — the protected Postgres read model that renders
			// the clinical record back to the provider who documented it. It is the
			// ONLY read path to .encounter's content: the aspect is sensitive, its
			// DEK is custodied on the clinicalRecord retention-class holder, and a
			// plain lens copying it would project the ciphertext envelope verbatim.
			//
			// Secure columns with HolderTypes ["retentionclass"], not ["identity"]:
			// custody follows the retention obligation rather than the patient, so
			// the record outlives the patient's erasure. The decryptor takes custody
			// from the ciphertext's own keyId; HolderTypes is this lens's statement
			// of which holders it agreed to render, so a subject-custodied ciphertext
			// arriving in one of these columns is refused rather than opened.
			//
			// The class-key destruction reaches these rows through the declaration,
			// not through the cypher: no lens can bind the holder vertex (custody is
			// declared, never linked), so the in-band .piiKey CDC scrub that serves
			// clinicPatientsRead's identity columns cannot fire here.
			// cmd/refractor's holderTypeRebuildTargets enumerates every running lens
			// declaring the destroyed holder's type and rebuilds it, which is why
			// HolderTypes is the load-bearing declaration and not documentation.
			//
			// PROVIDER-anchored, mirroring providerAppointmentsRead exactly:
			// withProvider is the REQUIRED anchor walk (an appointment with no
			// provider projects no row — fail-closed, never a null authz_anchor) and
			// authz_anchors carries the provider's own bare NanoID, which
			// clinicProviderReadGrants already produces. No workplace token: unlike
			// the appointment schedule, the clinical note is not front-desk material,
			// so the row reaches the treating provider and a WildcardAnchor holder
			// only. forPatient stays OPTIONAL and contributes patient_key alone —
			// the patient's NAME is deliberately absent, so the plaintext note does
			// not sit beside an identifier in a second table; a reader that needs it
			// joins read_provider_appointments on appointment_id.
			//
			// The WHERE presence filter keys off the NON-sensitive sibling: the two
			// aspects are written in one batch by RecordEncounter, so a documentedAt
			// is exactly the set of appointments carrying a record, and an
			// undocumented appointment produces no PHI-columned row at all.
			CanonicalName: "clinicEncountersRead",
			Class:         "meta.lens",
			Adapter:       "postgres",
			Table:         "read_clinic_encounters",
			Engine:        "full",
			Spec:          clinicEncountersReadSpec,
			Protected:     true,
			IntoKey:       []string{"appointment_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "entity_key", Type: "text"},
				{Name: "patient_key", Type: "text"},
				{Name: "provider_key", Type: "text"},
				{Name: "documented_at", Type: "text"},
				{Name: "summary", Type: "text"},
				{Name: "assessment", Type: "text"},
				{Name: "plan", Type: "text"},
			},
			SecureColumns: []pkgmgr.SecureColumn{
				{Column: "summary", HolderTypes: []string{"retentionclass"}, Field: "summary"},
				{Column: "assessment", HolderTypes: []string{"retentionclass"}, Field: "assessment"},
				{Column: "plan", HolderTypes: []string{"retentionclass"}, Field: "plan"},
			},
		},
		{
			// clinicIdentitiesRead — the protected Postgres identity-name lens
			// that resolves a signed-in actor's OWN name. cmd/clinic-app renders
			// "Signed in as <name>" off the patient roster (clinicPatientsRead)
			// and the provider directory (clinicProviders); an identity that is
			// neither — every front-desk staffer — has no row in either, so the
			// header falls back to the raw key. NAME ONLY, mirroring
			// cafe-domain's cafeIdentitiesRead and wellness-domain's
			// wellnessIdentitiesRead SECURE LENS (Contract #3 §3.10) — the
			// identity `name` is a sensitive aspect, so Core KV holds only its
			// ciphertext envelope, and the cypher RETURNs the envelope whole for
			// Refractor to decrypt at projection time.
			//
			// SELF-ANCHORED: each row's authz_anchors carries the identity's OWN
			// bare NanoID, so the platform's base cap-read self-grant
			// (internal/bootstrap.CapabilityReadGrantsLensDefinition — every
			// actor's actor_id == anchor_id == its own key, and it matches
			// class=identity, which IS this lens's anchor type) lets a signed-in
			// staffer, patient, or provider read their own row with no extra
			// grant declaration. That is the landlordUnitsRead idiom, not this
			// package's indirect patientIdentityReadGrants /
			// providerIdentityReadGrants producers: those exist because a
			// patient/provider ENTITY is a different vertex class from the login
			// that reads its rows, whereas here the anchor IS the identity.
			//
			// Self-anchor ONLY, unlike the café/wellness siblings, which fan a
			// lease's covering buildings into the same set so a front-desk actor
			// resolves OTHER people's names too. The names a clinic front desk
			// needs about other people already have their own protected paths —
			// clinicPatientsRead carries the patient roster with its own
			// workplace fan-out, and clinicProviders is a public directory — and
			// there is no lease-shaped walk from a bare identity to a clinic
			// building to fan out over. A WildcardAnchor holder still reads
			// every row.
			CanonicalName: "clinicIdentitiesRead",
			Class:         "meta.lens",
			Adapter:       "postgres",
			Table:         "read_clinic_identities",
			Engine:        "full",
			Spec:          clinicIdentitiesReadSpec,
			Protected:     true,
			IntoKey:       []string{"identity_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "identity_key", Type: "text"},
				{Name: "name", Type: "text"},
			},
			SecureColumns: []pkgmgr.SecureColumn{
				{Column: "name", HolderTypes: []string{"identity"}, Field: "value"},
			},
		},
		{
			// clinicPatientReadGrants — the cap-read.clinic.patient GrantTable
			// producer that closes the gap flagged live (0-of-1 read):
			// clinicAppointmentsRead's authz_anchors anchors on the PATIENT
			// vertex's own bare NanoID (nanoIdFromKey(patient.key)), and
			// cmd/clinic-app mints the JWT subject as that SAME patient NanoID
			// (app.js's bareId(state.patient) — the patient is its own RLS
			// actor, never a linked contact identity). The platform's base
			// cap-read self-anchor producer (internal/bootstrap.
			// CapabilityReadGrantsLensDefinition) only MATCHes class=identity,
			// so a patient — a DIFFERENT vertex class — never receives a grant:
			// My Appointments was permanently empty for every patient.
			//
			// This is the package-level cap-read.<domain> producer the base
			// lens's doc comment anticipates (internal/bootstrap/lenses.go
			// "Each package ships its own cap-read.<domain> ... lens for the
			// relationships it owns") — clinic-domain is the first package to
			// ship one. Mirrors CapabilityReadGrantsLensDefinition's shape
			// exactly (a plain, non-actorAggregate GrantTable projection), just
			// self-anchored on class=patient instead of class=identity.
			//
			// grant_source = 'cap-read.clinic.patient', disjoint from the core
			// producer's 'cap-read.root' 'cap-read' and from
			// clinicProviderReadGrants' 'cap-read.clinic.provider' below — each
			// producer retracts only its own grant_source rows (§6.14).
			// RETRACTION is automatic: TombstonePatient's anchor-tombstone
			// resolves nanoIdFromKey(p.key) read-free, so the self-grant is
			// revoked the same way the base identity self-grant is.
			CanonicalName: "clinicPatientReadGrants",
			Class:         "meta.lens",
			Adapter:       "postgres",
			GrantTable:    true,
			Engine:        "full",
			Spec:          clinicPatientReadGrantsSpec,
		},
		{
			// clinicProviderReadGrants — providerAppointmentsRead's sibling
			// producer, self-anchoring class=provider the same way
			// clinicPatientReadGrants self-anchors class=patient (see its doc
			// comment for the full gap analysis: providerAppointmentsRead's
			// authz_anchors is the provider's own NanoID, and cmd/clinic-app
			// mints a provider's JWT subject the same way — bareId(providerKey)
			// — so "My Schedule" was equally permanently empty).
			//
			// grant_source = 'cap-read.clinic.provider'.
			CanonicalName: "clinicProviderReadGrants",
			Class:         "meta.lens",
			Adapter:       "postgres",
			GrantTable:    true,
			Engine:        "full",
			Spec:          clinicProviderReadGrantsSpec,
		},
		{
			// providerIdentityReadGrants — the cap-read.provider.clinic Path A
			// producer for the PROVIDER HAT (persona-worlds-design.md Fire W0
			// §3.2/§3.3): once BindProviderIdentity links a login identity to a
			// clinic provider entity, the BOUND LOGIN inherits the provider
			// vertex's own anchor tokens — providerAppointmentsRead's
			// authz_anchors is the provider's bare NanoID (see
			// clinicProviderReadGrants above) — so this producer is what lets
			// the IDENTITY (not just the entity) match that anchor: without
			// it, a real human logging in as their bound identity would see
			// an empty "My Schedule" even though the provider ENTITY's rows
			// are correctly anchored.
			//
			// Mirrors service-location's staffReadGrants shape EXACTLY
			// (service-location/lenses.go), not clinicPatientReadGrants /
			// clinicProviderReadGrants above: those two are ENTITY-as-actor
			// self-anchors (actor_id == anchor_id == the entity's own bare
			// NanoID, no role or link walk), while this one is an
			// ACTOR-DIFFERENT-FROM-ANCHOR producer — actor_id is the BOUND
			// IDENTITY's NanoID, anchor_id is the PROVIDER ENTITY's NanoID.
			// Rows stay unchanged (the row shape is identical to
			// staffReadGrants' {actor_id, anchor_id, grant_source} triple);
			// clinicProviderReadGrants (the entity-as-actor producer) remains
			// for nothing new — there is no caller today minting a JWT
			// subject directly on a provider's own bare NanoID, and retiring
			// it is out of this fire's scope.
			//
			// Deliberately UNANCHORED (no $actorKey) — DiffRetraction compares
			// this query's full result set against the target's live key set,
			// which is sound only when the result set is already the complete
			// current truth (Refractor's ValidateUnanchoredForDiffRetraction
			// enforces this at activation; staffReadGrants' own doc comment
			// explains why an $actorKey-scoped variant would retract every
			// OTHER provider's grant on its first event). The grant exists
			// only while BOTH links do (holdsRole provider + identifiedBy), so
			// retraction cannot ride an anchor tombstone — an unbind
			// tombstones a LINK, not a vertex, so DiffRetraction's
			// shrinking-row-set IS the revocation transport, scoped to this
			// producer's own grant_source (GrantSource, below).
			CanonicalName:  "providerIdentityReadGrants",
			Class:          "meta.lens",
			Adapter:        "postgres",
			GrantTable:     true,
			GrantSource:    "cap-read.provider.clinic",
			DiffRetraction: true,
			Engine:         "full",
			Spec:           providerIdentityReadGrantsSpec,
		},
		{
			// patientIdentityReadGrants — providerIdentityReadGrants' patient
			// sibling, and the producer that makes a patient's LOGIN the
			// principal of their own protected reads. Without it, a person who
			// signs in and whose identity is the target of a patient's
			// identifiedBy link matches no grant row at all, and every
			// patient-anchored read (clinicAppointmentsRead via
			// /api/my-appointments, /api/my-visit-series, their own roster row)
			// returns empty — the reads are wired, the grant simply is not
			// there. Same actor-different-from-anchor shape, same
			// DiffRetraction transport (an unbind tombstones the link, not a
			// vertex), same own-grant_source scoping.
			CanonicalName:  "patientIdentityReadGrants",
			Class:          "meta.lens",
			Adapter:        "postgres",
			GrantTable:     true,
			GrantSource:    "cap-read.patient.clinic",
			DiffRetraction: true,
			Engine:         "full",
			Spec:           patientIdentityReadGrantsSpec,
		},
	}
}

// clinicAppointmentsSpec projects one row per appointment, walking forPatient and
// withProvider (each 0..1, so the row stays one-per-anchor — 0..1 × 0..1 = 1, the
// §10.2 shape). The 0..1 cardinality is enforced by the OP, not the cypher:
// CreateAppointment writes exactly one forPatient + one withProvider link
// (deterministic CreateOnly keys), and no op adds a second of either — so this
// stays a clean flat (no-WITH) projection. A future op that could attach a second
// link of the same relation would own re-introducing a fan-out guard. The per-row
// key column is `key` (the appointment key, the IntoKey
// default), so the read model is keyed by vtx.appointment.<id>; patientKey /
// providerKey repeat the joined endpoints in the body so a reader can scope to
// "my appointments" (by patient) or a "provider schedule" (by provider) by OPAQUE
// KEY. The patient is projected by key ONLY — a patient's name is PHI (a localhost
// reader of this open, unauthenticated lens would otherwise learn a named person is
// a patient here) and is projected solely into the Protected, RLS-scoped
// clinicAppointmentsRead / clinicPatientsRead lenses. The provider neighbour columns
// (providerName / providerSpecialty) are the deliberately-public directory the
// booking UI renders and are null when the withProvider link is absent (the reader
// treats them as absent). reminderSentAt is a null-safe
// read of the appointment's .reminder aspect (written by the clinic-reminders package
// when the @at reminder fires): it is null until a reminder is sent, and null whenever
// clinic-reminders is not installed — a soft read-model surfacing, never a build
// dependency (the engine reads the aspect by key-shape; clinic-domain installs alone).
// followUpReminderSentAt is the same null-safe soft read of the appointment's
// .followUpReminder aspect (written by clinic-reminders when the at-the-date follow-up
// @at reminder fires) — null until a follow-up reminder fires and null whenever
// clinic-reminders is not installed.
//
// documentedAt / followUpRequested / followUpDate are the OPERATIONAL, non-PHI
// signals of the appointment's .documentation aspect (written by RecordEncounter
// alongside the sensitive .encounter aspect). The RAW clinical content (summary /
// assessment / plan) lives on .encounter and is DELIBERATELY NOT projected — it is
// SENSITIVE PHI (its DEK custodied on the clinicalRecord retention class), the same
// name-only discipline clinicPatients applies to .demographics. A non-null
// documentedAt IS the "visit documented" presence signal (mirrors reminderSentAt);
// followUpDate is null unless a follow-up was requested. All null until a visit is
// documented (and whenever no .documentation aspect exists), null-safe by key-shape.
const clinicAppointmentsSpec = `MATCH (a:appointment)
OPTIONAL MATCH (a)-[:forPatient]->(p:patient)
OPTIONAL MATCH (a)-[:withProvider]->(pr:provider)
OPTIONAL MATCH (a)-[:atSite]->(site:building)
RETURN
  a.key AS key,
  a.key AS appointmentKey,
  a.schedule.data.startsAt AS startsAt,
  a.schedule.data.endsAt AS endsAt,
  a.schedule.data.reason AS reason,
  a.status.data.value AS status,
  a.status.data.note AS statusNote,
  p.key AS patientKey,
  pr.key AS providerKey,
  pr.profile.data.fullName AS providerName,
  pr.profile.data.specialty AS providerSpecialty,
  site.key AS siteKey,
  site.site.data.name AS siteName,
  a.reminder.data.sentAt AS reminderSentAt,
  a.followUpReminder.data.sentAt AS followUpReminderSentAt,
  a.documentation.data.documentedAt AS documentedAt,
  a.documentation.data.followUpRequested AS followUpRequested,
  a.documentation.data.followUpDate AS followUpDate`

// clinicProvidersSpec projects one row per NAMED provider — the human-readable
// roster the booking UI renders so a patient picks a provider by name + specialty
// instead of a raw vtx.provider.<id> key. The WHERE keeps only providers carrying
// a `.profile` aspect (the `<> null` aspect-presence idiom availableListings
// uses). The per-row key is the provider key (the IntoKey default); `providerKey`
// repeats it in the body. specialty / credentials / bio are projected so the
// provider EDITOR UI can read-modify-write the full profile (SetProviderProfile
// replaces the whole .profile, so the form seeds every editable field from here).
//
// timeOff projects the provider's opt-in .timeOff aspect's `ranges` array verbatim
// (a list of {from, to, reason?} canonical-UTC RFC3339 ranges written by
// SetProviderTimeOff), null when the provider has declared no blackouts. It is a
// non-scalar projection — the engine returns the array value, which the read model
// stores as JSON — so the time-off MANAGER UI can read-modify-write the current
// ranges (SetProviderTimeOff replaces the whole list) and the booking picker can
// warn about a blocked date. The op (CreateAppointment / RescheduleAppointment,
// enforce_time_off) stays the authority; this is the display surface only.
//
// hours projects the provider's opt-in .hours aspect's `windows` array verbatim
// (a list of {day 0-6, openSec, closeSec} UTC seconds-of-day written by
// SetProviderHours), null when the provider has set no availability windows. Like
// timeOff it is a non-scalar projection — the booking picker reads it (together
// with timeOff and the provider's existing appointments) to compute and suggest
// the open slots for a chosen date. The op (enforce_hours) stays the authority;
// this is the display surface only.
//
// identifiedBy is OPTIONAL — a provider with no bound login (BindProviderIdentity
// never ran) projects a null identityKey. Unlike clinicPatientsRead's identical
// walk, this is the OPEN (unprotected) lens: a provider's identity binding is not
// PHI (the directory already projects their real name/specialty/bio publicly), so
// it needs no RLS — the FE reads it to resolve a signed-in provider session's own
// name (renderSignedInAs) exactly as clinicPatientsRead's identityKey lets a
// signed-in patient session resolve its own.
const clinicProvidersSpec = `MATCH (pr:provider)
WHERE pr.profile.data.fullName <> null
OPTIONAL MATCH (pr)-[:identifiedBy]->(id:identity)
RETURN
  pr.key AS key,
  pr.key AS providerKey,
  pr.profile.data.fullName AS name,
  pr.profile.data.specialty AS specialty,
  pr.profile.data.credentials AS credentials,
  pr.profile.data.bio AS bio,
  pr.timeOff.data.ranges AS timeOff,
  pr.hours.data.windows AS hours,
  id.key AS identityKey`

// clinicPatientsSpec projects one row per NAMED patient by OPAQUE KEY only — no
// name. This open (unauthenticated) roster carries patient keys for key-based
// scoping; a patient's NAME is PHI (the fact a named person is a patient here is
// itself a disclosure) and is projected ONLY into the Protected, RLS-scoped
// clinicPatientsRead lens (staff-anchored). The WHERE keeps only patients carrying a
// `.demographics` aspect (the `<> null` aspect-presence idiom) so a ghost vertex
// with no profile does not roster — the presence test reads the aspect but does not
// project it.
const clinicPatientsSpec = `MATCH (p:patient)
WHERE p.demographics.data.registeredAt <> null
RETURN
  p.key AS key,
  p.key AS patientKey`

// clinicSitesSpec projects one row per NAMED clinic site — a location-domain
// building carrying a `.site` aspect (SetSiteProfile). Same flat no-WITH shape
// as clinicProviders/clinicPatients. The WHERE keeps only buildings carrying a
// name (the `<> null` aspect-presence idiom). The per-row key is the building
// key (the IntoKey default); `siteKey` repeats it in the body — the site
// directory / (a later increment's) site-scoped booking picker reads this.
const clinicSitesSpec = `MATCH (b:building)
WHERE b.site.data.name <> null
RETURN
  b.key AS key,
  b.key AS siteKey,
  b.site.data.name AS name`

// providerSitesSpec projects one row per (provider, site) practicesAt pair —
// a provider may practice at many sites, a site may host many providers, so
// this is a SEPARATE join lens rather than an array column folded into
// clinicProviders (mirrors identity-hygiene's duplicateCandidates shape:
// nats-kv full engine, no $actorKey, composite IntoKey [provider_id, site_id],
// DiffRetraction so an unassign — RemoveProviderSite tombstoning the
// practicesAt link — retracts the row instead of leaving it stale).
const providerSitesSpec = `MATCH (pr:provider)-[:practicesAt]->(b:building)
RETURN
  nanoIdFromKey(pr.key) AS provider_id,
  nanoIdFromKey(b.key)  AS site_id,
  pr.key                AS providerKey,
  b.key                 AS siteKey,
  pr.profile.data.fullName AS providerName,
  b.site.data.name         AS siteName`

// clinicSiteBackfillSpec is the missing_site convergence cypher: one row per
// appointment, mirroring cafe-domain's staleTabSettlementSpec shape exactly
// (MATCH the anchor by $actorKey, OPTIONAL MATCH the neighbour whose absence
// is the gap). An appointment with no atSite link (OPTIONAL MATCH binds
// site=null) is missing_site; one that already carries the link is not — the
// same aspect/link-presence idiom clinicAppointmentsSpec's own OPTIONAL MATCH
// (atSite) uses, just turned into a boolean gap column instead of a display
// field.
//
// Non-convergence safety (mirrors cafe-domain's own missing_staleat note,
// lenses.go): BackfillAppointmentSite (ddls.go) only ever writes the atSite
// link when the appointment's provider practicesAt EXACTLY ONE site — zero
// (an unassigned or dead provider) or two-or-more (genuinely ambiguous which
// site) both leave the op a clean no-op. Such an appointment's missing_site
// stays true forever, so Weaver re-dispatches BackfillAppointmentSite against
// it on every convergence pass — harmlessly: each dispatch is an idempotent
// no-op (empty mutations/events), never a retry that could clobber anything
// or accumulate side effects, exactly the posture cafe-domain's own
// permanently-unresolvable BackfillTabStaleAt case relies on. No retry-count
// column is needed here (unlike missing_settle's maxretries_settle, which
// bounds a DIFFERENT kind of gap — cafe-domain's own SettleStaleTab keeps
// retrying a tab that COULD converge but hasn't yet); this gap's two
// terminal, permanently-open shapes are distinguished from a genuinely
// transient one by the SAME idempotent-no-op property, not a counter.
const clinicSiteBackfillSpec = `MATCH (a:appointment {key: $actorKey})
OPTIONAL MATCH (a)-[:atSite]->(site:building)
RETURN
  a.key AS actorKey,
  a.key AS entityKey,
  a.key AS appointmentKey,
  a.status.data.value AS status,
  (site.key = null) AS missing_site,
  (site.key = null) AS violating
`

// clinicPatientsReadSpec is the protected Postgres read model's cypher for the
// clinic-wide patient roster (D1.5, the staff-wildcard increment; Vault Fire 5
// added the identifiedBy contact columns). Same WHERE guard as
// clinicPatientsSpec (only NAMED patients project). authz_anchors carries the
// row's own patient NanoID plus the WORKPLACE token of every DISTINCT building
// a provider practises at, for a provider this patient has an appointment
// with — the same self-plus-workplace anchor concept clinicAppointmentsReadSpec
// establishes with its own `[(pr)-[:practicesAt]->(b) | ...]` comprehension,
// one hop further out (patient -> appointment -> provider -> building), though
// this query folds that fan-out through WITH + collect(DISTINCT) rather than
// projecting a comprehension directly (see below) — so a front-desk actor's
// cap-read.staff grant (service-location's staffReadGrants, anchored on the
// building it worksAt) now matches every patient whose care touches that
// building, not only the reserved WildcardAnchor holder. Three kinds of actor
// match: the WildcardAnchor grant (the whole roster), a worksAt-anchored
// front-desk actor for a shared building, and, via patientIdentityReadGrants
// below, the signed-in identity that patient is identifiedBy (its own row
// only — see clinicPatientsRead's doc comment).
//
// The workplace fan-out is an OPTIONAL MATCH folded back to one row per
// (patient, identity) pair by WITH p, id, collect(DISTINCT ...) — one row per
// patient in the common single-identifiedBy case, pre-existing multiplicity
// this WITH neither introduces nor fixes if a patient ever carries more than
// one identifiedBy link (ddls.go documents that link key as
// (patient, identity)-composite) — combining two separately precedented
// idioms rather than mirroring one: privacy-base's
// `WITH i, count(DISTINCT c.key) AS boundInResidue` (packages/privacy-base/
// lenses.go) carries a bound node through WITH alongside a DISTINCT
// aggregate, and edge-manifest's edgeIdentitySpec collect(DISTINCT {...})
// (packages/edge-manifest/lenses.go) dedupes a fan-out into a projected
// array — not a plain MATCH left un-folded: a MATCH binding the
// appointment/provider hops that carried straight into RETURN would fan a
// multi-appointment patient into one row per appointment, colliding on the
// single-valued patient_id IntoKey.
// WITH's implicit GROUP BY on its non-aggregating items (p, id) is what
// re-collapses those fanned rows to one per patient before RETURN ever runs,
// and collect() drops nulls (internal/refractor/ruleengine/full/aggregate.go),
// so a patient with no appointments yields buildingAnchors = [] rather than
// [null]. DISTINCT is load-bearing, not cosmetic: without it a patient's
// authz_anchors grows by one entry per appointment forever (an RLS array
// `unnest` on every staff roster read pays the cost of every duplicate).
//
// identifiedBy is OPTIONAL — a patient created before its contact was minted,
// or one with no contact at all, has no linked identity, so identityKey /
// emailEnv / phoneEnv all project null together (the Secure-Lens decryptor's
// null-ciphertext-column path, never the null-identity-key error path — see
// internal/refractor/pipeline/secure.go decryptColumn). The shred's piiKey CDC
// event re-scans this UNANCHORED lens the same way it does
// landlordLeaseApplicationsRead, so a shredded patient's contact scrubs to
// null on the next projection touch.
const clinicPatientsReadSpec = `MATCH (p:patient)
WHERE p.demographics.data.registeredAt <> null
OPTIONAL MATCH (p)-[:identifiedBy]->(id:identity)
OPTIONAL MATCH (p)<-[:forPatient]-(a:appointment)-[:withProvider]->(pr:provider)-[:practicesAt]->(b:building)
WITH p, id, collect(DISTINCT nanoIdFromKey(b.key)) AS buildingAnchors
RETURN
  nanoIdFromKey(p.key)         AS patient_id,
  p.key                        AS entity_key,
  p.key                        AS patient_key,
  id.name.data                 AS name,
  p.demographics.data.fullName AS unlinked_name,
  id.key                       AS identity_key,
  id.email.data                AS email,
  id.phone.data                AS phone,
  [nanoIdFromKey(p.key)] + buildingAnchors
                               AS authz_anchors
`

// clinicAppointmentsReadSpec is the PATIENT-anchored protected Postgres read
// model's cypher (D1.5). forPatient is REQUIRED (the anchor walk) — an
// appointment with no patient link projects no row, fail-closed, never a null
// authz_anchor (mirrors leaseApplicationsReadSpec's REQUIRED applicant walk).
// withProvider stays OPTIONAL: it is a display-only neighbour, not the anchor.
// Same column surface as clinicAppointmentsSpec (the unprotected lens) so the
// migrated "My Appointments" view keeps full display parity.
//
// authz_anchors carries the patient's own NanoID plus the WORKPLACE token — the
// building the appointment's provider practises at — so front-desk staff working
// that building read the row through service-location's staffReadGrants
// (facet-staff-worlds-design.md §3.5). Declaring a building token IS the
// declaration that these rows are workplace-readable.
//
// The workplace half is a pattern COMPREHENSION, not a second array element, and
// the difference is load-bearing: a walk that finds no building yields a NULL
// element, which ProtectedAdapter.toStringSlice rejects — failing the whole row's
// upsert, so an appointment whose provider practises nowhere would vanish for its
// own patient too. The comprehension yields [] instead. A missing building must
// cost a row its staff visibility, never its existence.
const clinicAppointmentsReadSpec = `MATCH (a:appointment)
MATCH (a)-[:forPatient]->(p:patient)
OPTIONAL MATCH (a)-[:withProvider]->(pr:provider)
OPTIONAL MATCH (a)-[:atSite]->(site:building)
OPTIONAL MATCH (p)-[:identifiedBy]->(pid:identity)
RETURN
  nanoIdFromKey(a.key)                   AS appointment_id,
  a.key                                  AS entity_key,
  a.schedule.data.startsAt               AS starts_at,
  a.schedule.data.endsAt                 AS ends_at,
  a.schedule.data.reason                 AS reason,
  a.status.data.value                    AS status,
  a.status.data.note                     AS status_note,
  p.key                                  AS patient_key,
  pid.name.data                          AS patient_name,
  p.demographics.data.fullName           AS unlinked_patient_name,
  pr.key                                 AS provider_key,
  pr.profile.data.fullName               AS provider_name,
  pr.profile.data.specialty              AS provider_specialty,
  site.key                               AS site_key,
  site.site.data.name                    AS site_name,
  a.reminder.data.sentAt                 AS reminder_sent_at,
  a.followUpReminder.data.sentAt         AS follow_up_reminder_sent_at,
  a.documentation.data.documentedAt      AS documented_at,
  a.documentation.data.followUpRequested AS follow_up_requested,
  a.documentation.data.followUpDate      AS follow_up_date,
  [nanoIdFromKey(p.key)] + [(pr)-[:practicesAt]->(b:building) | nanoIdFromKey(b.key)]
                                         AS authz_anchors
`

// providerAppointmentsReadSpec is the PROVIDER-anchored protected Postgres read
// model's cypher (D1.5). withProvider is REQUIRED (the anchor walk) — an
// appointment with no provider link projects no row, fail-closed, never a null
// authz_anchor (mirrors clinicAppointmentsReadSpec's REQUIRED forPatient walk).
// forPatient stays OPTIONAL: a display-only neighbour, not the anchor. Same
// column surface as clinicAppointmentsReadSpec so the provider's "My Schedule"
// view keeps full display parity with "My Appointments".
const providerAppointmentsReadSpec = `MATCH (a:appointment)
MATCH (a)-[:withProvider]->(pr:provider)
OPTIONAL MATCH (a)-[:forPatient]->(p:patient)
OPTIONAL MATCH (a)-[:atSite]->(site:building)
OPTIONAL MATCH (p)-[:identifiedBy]->(pid:identity)
RETURN
  nanoIdFromKey(a.key)                   AS appointment_id,
  a.key                                  AS entity_key,
  a.schedule.data.startsAt               AS starts_at,
  a.schedule.data.endsAt                 AS ends_at,
  a.schedule.data.reason                 AS reason,
  a.status.data.value                    AS status,
  a.status.data.note                     AS status_note,
  p.key                                  AS patient_key,
  pid.name.data                          AS patient_name,
  p.demographics.data.fullName           AS unlinked_patient_name,
  pr.key                                 AS provider_key,
  pr.profile.data.fullName               AS provider_name,
  pr.profile.data.specialty              AS provider_specialty,
  site.key                               AS site_key,
  site.site.data.name                    AS site_name,
  a.reminder.data.sentAt                 AS reminder_sent_at,
  a.followUpReminder.data.sentAt         AS follow_up_reminder_sent_at,
  a.documentation.data.documentedAt      AS documented_at,
  a.documentation.data.followUpRequested AS follow_up_requested,
  a.documentation.data.followUpDate      AS follow_up_date,
  [nanoIdFromKey(pr.key)]                AS authz_anchors
`

// clinicEncountersReadSpec is the PROVIDER-anchored protected Postgres read
// model's cypher for the clinical record itself — the only projection of
// .encounter's content anywhere. withProvider is REQUIRED (the anchor walk),
// forPatient is OPTIONAL and display-only, exactly as providerAppointmentsRead
// splits them.
//
// The three PHI columns all RETURN the SAME expression, a.encounter.data — the
// whole ciphertext envelope ({ct, nonce, keyId}) — three times under three
// aliases. That is the Secure-Lens calling convention: the cypher hands the
// decryptor an envelope per column and the declaration's Field names which key
// of the decrypted plaintext each alias keeps. Nothing here can read the
// plaintext; the column is ciphertext until pipeline.SecureDecryptor rewrites it.
//
// The presence filter reads the non-sensitive sibling rather than .encounter
// itself: RecordEncounter writes both aspects in one batch, so documentedAt is
// present exactly when a record exists, and testing a ciphertext envelope for
// nullity would couple the row set to the encryption envelope's shape.
const clinicEncountersReadSpec = `MATCH (a:appointment)
MATCH (a)-[:withProvider]->(pr:provider)
WHERE a.documentation.data.documentedAt <> null
OPTIONAL MATCH (a)-[:forPatient]->(p:patient)
RETURN
  nanoIdFromKey(a.key)              AS appointment_id,
  a.key                             AS entity_key,
  p.key                             AS patient_key,
  pr.key                            AS provider_key,
  a.documentation.data.documentedAt AS documented_at,
  a.encounter.data                  AS summary,
  a.encounter.data                  AS assessment,
  a.encounter.data                  AS plan,
  [nanoIdFromKey(pr.key)]           AS authz_anchors
`

// clinicIdentitiesReadSpec projects one row per NAMED identity — the roster
// clinic-app resolves the signed-in actor's own name against. The WHERE keeps
// only identities carrying a `.name` aspect via ciphertext presence
// (`i.name.data.ct <> null` — there is no plaintext `value` field at rest, so
// testing the envelope's own field is the presence test), mirroring
// cafe-domain's cafeIdentitiesReadSpec. The `name` column RETURNs the whole
// envelope for the Secure-Lens decryptor, exactly as clinicPatientsReadSpec's
// own `id.name.data` column does. authz_anchors carries the identity's OWN
// bare NanoID and nothing else — see the Lenses() declaration above for why
// that self-anchor, with no workplace fan-out, is the right shape here.
const clinicIdentitiesReadSpec = `MATCH (i:identity)
WHERE i.name.data.ct <> null
RETURN
  nanoIdFromKey(i.key)   AS identity_id,
  i.key                  AS identity_key,
  i.name.data            AS name,
  [nanoIdFromKey(i.key)] AS authz_anchors
`

// clinicPatientReadGrantsSpec is the cap-read.clinic.patient GrantTable
// producer's cypher — a plain, non-actorAggregate self-anchor projection
// mirroring internal/bootstrap.CapabilityReadGrantsLensDefinition exactly
// (MATCH the vertex, RETURN its own bare NanoID as both actor_id and
// anchor_id), just scoped to class=patient instead of class=identity. See
// clinicPatientReadGrants' doc comment (lenses.go) for why patient/provider
// need their own producer: the platform base self-anchor only ever matches
// class=identity.
const clinicPatientReadGrantsSpec = `MATCH (p:patient)
RETURN
  nanoIdFromKey(p.key)        AS actor_id,
  nanoIdFromKey(p.key)        AS anchor_id,
  'cap-read.clinic.patient'   AS grant_source
`

// clinicProviderReadGrantsSpec is clinicPatientReadGrantsSpec's provider
// sibling — self-anchors class=provider instead of class=patient.
const clinicProviderReadGrantsSpec = `MATCH (pr:provider)
RETURN
  nanoIdFromKey(pr.key)       AS actor_id,
  nanoIdFromKey(pr.key)       AS anchor_id,
  'cap-read.clinic.provider'  AS grant_source
`

// providerIdentityReadGrantsSpec projects one grant row per (bound login
// identity, clinic provider) pair — the provider hat's read-grant producer
// (persona-worlds-design.md Fire W0 §3.2/§3.3). Both MATCHes are REQUIRED, so
// a row exists only while the identity BOTH holds the identity-domain
// `provider` role AND is the target of a clinic provider's identifiedBy
// link: drop either and the pair stops being derived, and the target-diff
// revokes the grant (mirrors service-location's staffReadGrantsSpec exactly
// — see providerIdentityReadGrants' doc comment above for the shape
// rationale). The role is matched by canonicalName, the same way
// rbac-domain's capabilityRoleIndex / staffReadGrantsSpec read a role's name.
const providerIdentityReadGrantsSpec = `MATCH (i:identity)-[:holdsRole]->(r:role)
WHERE r.canonicalName.data.value = 'provider'
MATCH (pr:provider)-[:identifiedBy]->(i)
RETURN
  nanoIdFromKey(i.key)       AS actor_id,
  nanoIdFromKey(pr.key)      AS anchor_id,
  'cap-read.provider.clinic' AS grant_source
`

// patientIdentityReadGrantsSpec projects one grant row per (login identity,
// clinic patient) pair — the patient hat's read-grant producer, the sibling of
// providerIdentityReadGrantsSpec above. It is what makes a patient's own
// LOGIN, rather than the patient vertex standing in as its own actor, the
// principal of the patient-anchored protected reads: clinicPatientReadGrants
// grants the patient NanoID to itself, which only ever helped while a token
// could be minted with the patient vertex's id as its subject.
//
// Unlike the provider producer there is no role MATCH. Provider-hood is a
// ROLE, so the grant must not outlive it; being the person a patient record is
// about is not a role at all — it is exactly what identifiedBy asserts, and a
// patient who has not (yet) been granted `consumer` still owns their own
// record. Roles gate the OPS a person may submit; anchors gate the ROWS they
// may read, and those are different questions.
//
// Retraction rides DiffRetraction, not an anchor tombstone, for the same
// reason the provider producer does: unbinding tombstones the identifiedBy
// LINK, not either vertex, so the shrinking row set is the revocation
// transport, scoped to this producer's own grant_source.
const patientIdentityReadGrantsSpec = `MATCH (p:patient)-[:identifiedBy]->(i:identity)
RETURN
  nanoIdFromKey(i.key)      AS actor_id,
  nanoIdFromKey(p.key)      AS anchor_id,
  'cap-read.patient.clinic' AS grant_source
`
