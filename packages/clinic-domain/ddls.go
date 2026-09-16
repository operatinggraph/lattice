package clinicdomain

import (
	"strings"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// providerRoleKey is identity-domain's "provider" role key, computed
// deterministically (pkgmgr.RoleID mirrors what the installer mints at
// install time — no KV read required). BindProviderIdentity's script pins
// its holdsRole grant against this literal rather than trusting any live
// vtx.role.* the caller supplies — mirrors identity-domain's own
// consumerRoleKey pin (identity-domain/ddls.go:17): the grant matrix already
// restricts who can call the op, but the op's OWN script should not be
// steerable into granting a different role to the bound identity.
var providerRoleKey = "vtx.role." + pkgmgr.RoleID("identity-domain", "provider")

// Canonical names. Three vertexType DDLs own the op scripts (each op is admitted
// by EXACTLY ONE vertexType DDL — the operationType→script index drops an op
// claimed by two, so no overlap is allowed there). Four aspectType DDLs are
// step-6 write gates only (the Processor keys permittedCommands on the MUTATION
// document's class; aspectType DDLs are excluded from script selection), mirroring
// loftspace-domain's listing/address split.
//
// Aspect classes are clinic-namespaced (patientDemographics, providerProfile,
// appointmentSchedule, appointmentStatus) so the globally-unique canonicalName
// namespace (Contract #1 §1.5) is not polluted by generic words like "status" /
// "profile". The aspect's LOCAL NAME (the key segment a lens hops, e.g.
// vtx.patient.<id>.demographics) stays clean — the executor resolves
// node.<localName>.data.<field> by the key segment, independent of the class — so
// the lenses read p.demographics / a.status while the gate keys on the namespaced
// class.
const (
	patientVertexDDL     = "patient"
	providerVertexDDL    = "provider"
	appointmentVertexDDL = "appointment"

	demographicsAspectDDL   = "patientDemographics"
	profileAspectDDL        = "providerProfile"
	scheduleAspectDDL       = "appointmentSchedule"
	statusAspectDDL         = "appointmentStatus"
	hoursAspectDDL          = "providerHours"
	timeOffAspectDDL        = "providerTimeOff"
	encounterAspectDDL      = "appointmentEncounter"
	documentationAspectDDL  = "appointmentDocumentation"
	siteAssignmentAspectDDL = "appointmentSiteAssignment"

	providerSlotClaimAspectDDL   = "providerSlotClaim"
	patientSlotClaimAspectDDL    = "patientSlotClaim"
	patientSelfDayClaimAspectDDL = "patientSelfDayClaim"

	identityPatientClaimAspectDDL  = "identityPatientClaim"
	patientIdentityClaimAspectDDL  = "patientIdentityClaim"
	providerIdentityClaimAspectDDL = "providerIdentityClaim"
	identityProviderClaimAspectDDL = "identityProviderClaim"

	// clinicalRecordRetentionClass is the canonicalName of the retention-class key
	// holder this package declares (see RetentionClasses). The .encounter aspect's
	// Custody names it — the holder the clinical record's DEK is custodied on,
	// never the patient's own identity.
	clinicalRecordRetentionClass = "clinicalRecord"
)

// DDLs returns the package's seven DDL meta-vertex declarations:
//
//   - patient (vertexType) — owns CreatePatient + TombstonePatient.
//   - provider (vertexType) — owns CreateProvider + TombstoneProvider.
//   - appointment (vertexType) — owns CreateAppointment + SetAppointmentStatus +
//     TombstoneAppointment.
//   - patientDemographics / providerProfile / appointmentSchedule /
//     appointmentStatus (aspectType) — step-6 write gates for the four aspects.
//
// Architectural rules (binding — the known-key discipline of location-domain /
// loftspace-domain):
//
//   - The scripts read ONLY by known key. CreateAppointment validates BOTH link
//     endpoints (patient + provider) by the keys the caller lists in
//     ContextHint.Reads; the Tombstone/SetAppointmentStatus ops validate their
//     target by its key. No prefix scans, no adjacency lookups, no lens reads.
//   - CreateAppointment's endpoints MUST be alive AND the right class (patient /
//     provider): a dead or wrong-class endpoint is never wired (structured
//     ScriptError) — endpoint-class validation is at the op, not a downstream
//     untyped cypher match.
//
// Every aspect is NON-sensitive: .demographics carries only the patient's
// fullName — the display label the roster lenses need — never contact PII.
// Real contact (email/phone) is Vault-plane PII that belongs on a vtx.identity,
// never a bare vtx.patient aspect (step-6's sensitiveAspectScope forbids a
// sensitive aspect on a non-identity vertex anyway). CreatePatient accepts an
// optional pre-minted identityKey and wires an identifiedBy link to it — the
// caller (the FE) mints the identity carrying the sensitive contact first via
// identity-domain's CreateUnclaimedIdentity, mirroring loftspace-app's
// applicant flow (clinic-domain-design.md: "vtx.identity + an identifiedBy
// link — not a rework"). Two CreateOnly guard aspects make the pairing
// mutually exclusive, because the identifiedBy link key is
// (patient, identity)-composite and so stops neither direction on its own:
// identityPatientClaim, keyed solely on the identity, makes a SECOND patient
// claiming that identity collide, and patientIdentityClaim, keyed solely on
// the patient, makes a second identity binding that patient collide.
// BindPatientIdentity is the same wiring applied AFTER the fact — the second
// half of the registration ceremony for a patient typed in with a name alone
// — and it moves the name off .demographics onto the identity's sensitive
// .name aspect so the shred reaches it. No backfill for
// pre-existing patients (Vault's full-stack-reset delivery boundary covers
// the migration). Display of the linked contact rides a later Secure-Lens
// protected model (Fire 5 of the Vault crypto-shredding design); only the
// link is wired here.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		patientVertexTypeDDL(),
		providerVertexTypeDDL(),
		appointmentVertexTypeDDL(),
		clinicSiteVertexTypeDDL(),
		clinicSiteAssignmentVertexTypeDDL(),
		demographicsAspectTypeDDL(),
		profileAspectTypeDDL(),
		scheduleAspectTypeDDL(),
		statusAspectTypeDDL(),
		hoursAspectTypeDDL(),
		timeOffAspectTypeDDL(),
		providerSlotClaimAspectTypeDDL(),
		patientSlotClaimAspectTypeDDL(),
		patientSelfDayClaimAspectTypeDDL(),
		encounterAspectTypeDDL(),
		documentationAspectTypeDDL(),
		siteAssignmentAspectTypeDDL(),
		identityPatientClaimAspectTypeDDL(),
		patientIdentityClaimAspectTypeDDL(),
		clinicSiteProfileAspectTypeDDL(),
		providerIdentityClaimAspectTypeDDL(),
		identityProviderClaimAspectTypeDDL(),
	}
}

func patientVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     patientVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreatePatient", "TombstonePatient", "BackfillPatientRegistration", "BindPatientIdentity", "UnbindPatientIdentity"},
		Description: "Clinic patient DDL. Vertex shape: vtx.patient.<NanoID>, class=patient, root data = {} " +
			"(minimal, D5 — the data lives in the .demographics aspect + the optional identifiedBy link). " +
			"CreatePatient mints the patient + writes the .demographics aspect {fullName (required)} atomically; " +
			"an optional identityKey wires lnk.patient.<id>.identifiedBy.identity.<identityId> to a pre-minted " +
			"vtx.identity (validated alive + class=identity) carrying the patient's sensitive contact, and claims " +
			"a CreateOnly vtx.identity.<identityId>.patientClaim guard aspect — a second, DIFFERENT patient passing " +
			"the same identityKey is rejected (IdentityAlreadyClaimed). CreatePatient also RECORDS WHERE the " +
			"registration happened: it enumerates the caller's live worksAt links once and writes one " +
			"lnk.patient.<id>.registeredAtSite.<type>.<id> per workplace (patient registeredAtSite building), so a " +
			"just-registered patient sits in the front-desk world of the building it was registered at before any " +
			"appointment exists. The site is a FACT of the registration recorded on the patient, never a live walk of " +
			"where the registrar works today — a staffer who later transfers buildings moves no patient. A caller " +
			"that works nowhere (an operator, a console, a provider who only practicesAt) records no site and " +
			"registers normally. TombstonePatient soft-deletes one. The " +
			".demographics aspect is NON-sensitive (it attaches to a patient, not an identity); real contact PII " +
			"lives on the linked identity, the Vault plane's unit. BackfillPatientRegistration is an operator-only, " +
			"manual repair for a patient whose .demographics aspect carries no registeredAt (the roster lenses' " +
			"presence filter hides such a patient from every actor): it upserts registeredAt onto that aspect, " +
			"preserving fullName, and no-ops cleanly if registeredAt is already set. BindPatientIdentity is the " +
			"second half of the registration ceremony for a patient registered with a NAME ALONE: given that " +
			"patient and a pre-minted identity (both validated alive + class), it wires the same identifiedBy link " +
			"CreatePatient's identityKey branch wires, claims both exclusivity guards (patientClaim on the identity, " +
			"identityClaim on the patient), and MOVES the name — off the plaintext .demographics fullName and onto " +
			"the identity's sensitive .name aspect, so the shred that destroys the person's email and phone reaches " +
			"their name too. The identity must be UNCLAIMED: a login somebody already signed in to (state=claimed) " +
			"or one a merge retired (state=merged) is refused IdentityNotUnclaimed, which is what keeps front-desk's " +
			"standing grant from attaching a patient's chart to a login the desk itself holds. It further refuses a " +
			"patient that already carries an identity (PatientAlreadyIdentified), an identity another patient already " +
			"claimed (IdentityAlreadyClaimed), and a patient whose .demographics carries no fullName to move " +
			"(NothingToBind). UnbindPatientIdentity is the operator-only repair for a chart connected to the WRONG " +
			"login, and the exact inverse: it requires that live identifiedBy pair (NotBound otherwise) and an " +
			"identity nobody has claimed yet (IdentityClaimed otherwise — unbinding a claimed login would strand the " +
			"person who signed in with it), then tombstones the link and both guard aspects and moves the name BACK " +
			"onto .demographics from the identity's .name, restoring the pre-bind shape so the patient can be " +
			"connected to the right login instead.",
		Script: patientDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"fullName":{"type":"string","description":"The patient's full name (CreatePatient; required)."},` +
			`"identityKey":{"type":"string","description":"vtx.identity.<NanoID> of a pre-minted identity carrying the patient's sensitive contact (CreatePatient; optional — BindPatientIdentity / UnbindPatientIdentity; required. Validated alive + class=identity; wires or releases the identifiedBy link)."},` +
			`"patientId":{"type":"string","description":"Optional bare NanoID for the new patient vertex (CreatePatient); absent → minted."},` +
			`"patientKey":{"type":"string","description":"vtx.patient.<NanoID> of an existing patient (TombstonePatient, BindPatientIdentity, UnbindPatientIdentity; required, validated alive)."},` +
			`"patient":{"type":"string","description":"vtx.patient.<NanoID> of an existing patient (BackfillPatientRegistration; required, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.patient.<NanoID> the operation wrote — or, for BindPatientIdentity / UnbindPatientIdentity, the lnk.patient.<id>.identifiedBy.identity.<id> key it minted or released."}}}`,
		FieldDescription: map[string]string{
			"fullName":    "The patient's full name. Stored on the .demographics aspect (CreatePatient; required).",
			"identityKey": "Full vtx.identity.<NanoID> key of a pre-minted identity to link (CreatePatient; optional — BindPatientIdentity / UnbindPatientIdentity; required). Must be alive + class=identity; the bind wires the identifiedBy link and claims a patientClaim guard aspect on the identity (rejected if another patient already claimed it, or if the identity is not still unclaimed), the unbind releases both. Absent on CreatePatient → the patient has no linked identity.",
			"patientId":   "Optional bare NanoID (no dots / key segments) for the new patient vertex (vtx.patient.<patientId>). Absent → minted with nanoid.new().",
			"patientKey":  "Full vtx.patient.<NanoID> key of an existing patient vertex — to tombstone (TombstonePatient), to connect to a login identity (BindPatientIdentity; validated alive + class=patient, rejected if it already carries a live identityClaim), or to disconnect from one (UnbindPatientIdentity; the identifiedBy link to the named identity must be live).",
			"patient":     "Full vtx.patient.<NanoID> key of an existing patient vertex to backfill (BackfillPatientRegistration; required, validated alive).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreatePatient — register a patient",
				Payload: map[string]any{"fullName": "Alice Rivera"},
				ExpectedOutcome: "Mints vtx.patient.<NanoID> (class=patient, root {}) + the .demographics aspect " +
					"{fullName}, plus one lnk.patient.<id>.registeredAtSite.<type>.<id> per building the caller " +
					"worksAt at that instant (none when the caller works nowhere). Accepts an optional " +
					"bare-NanoID patientId. Returns primaryKey (the patient key).",
			},
			{
				Name:    "CreatePatient — register a patient with linked contact identity",
				Payload: map[string]any{"fullName": "Alice Rivera", "identityKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Mints the patient as above, plus lnk.patient.<id>.identifiedBy.identity.<identityId> " +
					"to the supplied identity (rejected if that identity is absent, tombstoned, or the wrong class) " +
					"and a CreateOnly patientClaim guard aspect on the identity (rejected if a DIFFERENT patient " +
					"already claimed it). The registeredAtSite links are recorded either way.",
			},
			{
				Name:            "TombstonePatient — remove a patient",
				Payload:         map[string]any{"patientKey": "vtx.patient.<NanoID>"},
				ExpectedOutcome: "Soft-deletes the patient vertex. Returns primaryKey. Rejects an absent / already-dead patient.",
			},
			{
				Name:    "BackfillPatientRegistration — repair a pre-2026-08-08 patient missing registeredAt",
				Payload: map[string]any{"patient": "vtx.patient.<NanoID>"},
				ExpectedOutcome: "Upserts .demographics with registeredAt set (preserving fullName if present); " +
					"no-ops if registeredAt is already set. Operator-only.",
			},
			{
				Name:    "BindPatientIdentity — connect a name-only patient to a login",
				Payload: map[string]any{"patientKey": "vtx.patient.<NanoID>", "identityKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Mints lnk.patient.<id>.identifiedBy.identity.<identityId> (returned as primaryKey), " +
					"a patientClaim on the identity and an identityClaim on the patient, and MOVES " +
					"the name: .demographics is rewritten without fullName and the identity's sensitive .name aspect " +
					"is upserted with it. Rejects an identity that is not still unclaimed, a patient already carrying " +
					"an identity, an identity another patient already claimed, an absent/tombstoned/wrong-class " +
					"endpoint, and a patient with no fullName to move.",
			},
			{
				Name:    "UnbindPatientIdentity — repair a chart connected to the wrong login",
				Payload: map[string]any{"patientKey": "vtx.patient.<NanoID>", "identityKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Tombstones lnk.patient.<id>.identifiedBy.identity.<identityId> (returned as primaryKey) " +
					"and both guard aspects, and moves the name BACK: .demographics is rewritten {registeredAt, fullName} " +
					"from the identity's .name. Operator-only. Rejects a pair with no live identifiedBy link (NotBound) " +
					"and an identity somebody has already claimed (IdentityClaimed).",
			},
		},
	}
}

// identityPatientClaimAspectTypeDDL declares the .patientClaim aspect ATTACHED
// onto an identity-domain vtx.identity (the clinic-reminders idiom of a package
// adding an aspect onto another package's vertex type — clinic-reminders/ddls.go's
// .reminder marker on clinic-domain's own appointment vertex). It is the global
// exclusivity guard for CreatePatient's optional identityKey: the .demographics
// aspect + identifiedBy link alone let two DIFFERENT patients both pass the same
// identityKey (the identifiedBy link key is (patient, identity)-composite, never
// identity-only), so two roster rows would decrypt and display the same person's
// contact. A CreateOnly aspect keyed SOLELY on the identity closes that gap —
// mirroring providerSlotClaim/patientSlotClaim's per-cell existence-marker lock,
// just with one claimant instead of one cell. Declaration-only; NON-sensitive (no
// data, so step-6's sensitiveAspectScope never fires on it either way).
func identityPatientClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     identityPatientClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreatePatient", "BindPatientIdentity", "UnbindPatientIdentity"},
		Description: "Identity patient-claim guard aspect (clinic-domain, attached onto an identity-domain vertex). " +
			"Stored as vtx.identity.<NanoID>.patientClaim (class identityPatientClaim) = {} — a pure existence marker, " +
			"no relationship field. CreatePatient (identityKey branch) and BindPatientIdentity each write ONE per " +
			"claimed identityKey: the key ITSELF (identical regardless of WHICH patient is claiming) is " +
			"the lock — a second, different patient passing the same identityKey collides at commit " +
			"(RevisionConflict), never a silent double-claim. Created when absent, OCC-revived when " +
			"present-but-tombstoned, so an identity UnbindPatientIdentity released can be claimed again. " +
			"Declaration-only: no op handler (the patient vertexType DDL's own script writes it).",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. Exclusivity is enforced by the KEY (the identity), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "identity patient-claim guard aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.identity.<NanoID>.patientClaim; claimed by CreatePatient's identityKey wiring or by BindPatientIdentity, tombstoned by UnbindPatientIdentity and OCC-revived by the next claim. A second, different patient claiming the same identity while the claim is live is rejected.",
			},
		},
	}
}

// patientIdentityClaimAspectTypeDDL declares the .identityClaim guard aspect on
// the PATIENT side of a patient↔identity pair — the entity-keyed half of the
// pairing's mutual exclusivity, of which identityPatientClaimAspectTypeDDL above
// is the identity-keyed half. Stored as vtx.patient.<NanoID>.identityClaim
// (class patientIdentityClaim) = {} — a pure existence marker, mirroring
// providerIdentityClaim's shape on the provider side.
//
// TWO ops write it: CreatePatient stamps it when the registration supplies an
// identityKey, and BindPatientIdentity stamps it when a name-only patient is
// connected to a login afterwards. A patient reaches exactly one of those paths,
// so the deterministic key never has two live writers racing for it — and if one
// ever did, a create on a key at revision 0 commits exactly once.
//
// The marker is not what refuses a bind on EVERY already-identified patient, only
// on the ones carrying it. A patient CreatePatient identified before this aspect
// existed has no marker and never gets one (there is no backfill); what refuses a
// bind on that population is NothingToBind — CreatePatient's identityKey branch
// already moved the name onto the identity, leaving .demographics with no fullName
// for the bind to move. Both populations are refused, by different mechanisms, and
// only the marker-carrying one is refused before the name is ever read.
//
// UnbindPatientIdentity tombstones the marker, so its writers create it only when
// absent and OCC-revive it when it is present-but-tombstoned: a repaired patient
// must be re-connectable, and a plain create against a key that has EVER been
// minted conflicts forever.
func patientIdentityClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     patientIdentityClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreatePatient", "BindPatientIdentity", "UnbindPatientIdentity"},
		Description: "Patient identity-claim guard aspect. Stored as vtx.patient.<NanoID>.identityClaim " +
			"(class patientIdentityClaim) = {} — a pure existence marker, no relationship field. " +
			"CreatePatient (identityKey branch) and BindPatientIdentity each write ONE per claimed patientKey: " +
			"the key ITSELF is the lock — a second, different identity binding the SAME patient collides at " +
			"commit (RevisionConflict), never a silent double-bind. Created when absent, OCC-revived when " +
			"present-but-tombstoned, so a patient UnbindPatientIdentity repaired can be connected again. " +
			"Declaration-only: no op handler (the patient vertexType DDL's own script writes it).",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. Exclusivity is enforced by the KEY (the patient), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "patient identity-claim guard aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.patient.<NanoID>.identityClaim; claimed by CreatePatient's identityKey wiring or by BindPatientIdentity, tombstoned by UnbindPatientIdentity and OCC-revived by the next bind. A second identity binding the same patient while the claim is live is rejected.",
			},
		},
	}
}

// providerIdentityClaimAspectTypeDDL declares the .identityClaim guard aspect
// on the PROVIDER side of a BindProviderIdentity pair — the entity-keyed half
// of the bind's mutual-exclusivity guard (identityProviderClaimAspectTypeDDL
// below is the identity-keyed half). Stored as vtx.provider.<NanoID>.identityClaim
// (class providerIdentityClaim) = {} — a pure existence marker, mirroring
// identityPatientClaim's shape above. BindProviderIdentity writes ONE per
// claimed providerKey, CreateOnly: the key ITSELF is the lock — a second,
// different identity binding the SAME provider collides at commit
// (RevisionConflict), never a silent double-bind. Declaration-only: no op
// handler (BindProviderIdentity's script, owned by the provider vertexType
// DDL, writes it).
func providerIdentityClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     providerIdentityClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"BindProviderIdentity"},
		Description: "Provider identity-claim guard aspect. Stored as vtx.provider.<NanoID>.identityClaim " +
			"(class providerIdentityClaim) = {} — a pure existence marker, no relationship field. " +
			"BindProviderIdentity writes ONE per claimed providerKey, CreateOnly: the key ITSELF is the lock — " +
			"a second, different identity binding the SAME provider collides at commit (RevisionConflict), never " +
			"a silent double-bind. Declaration-only: no op handler (BindProviderIdentity's script, owned by the " +
			"provider vertexType DDL, writes it).",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. Exclusivity is enforced by the KEY (the provider), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "provider identity-claim guard aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.provider.<NanoID>.identityClaim; claimed once by BindProviderIdentity. A second, different identity binding the same provider is rejected.",
			},
		},
	}
}

// identityProviderClaimAspectTypeDDL declares the .providerClaim aspect
// ATTACHED onto an identity-domain vtx.identity — the identity-keyed half of
// BindProviderIdentity's mutual-exclusivity guard, mirroring
// identityPatientClaimAspectTypeDDL's cross-package aspect-attachment shape
// (clinic-reminders' .reminder-onto-appointment idiom) exactly, just keyed
// "providerClaim" instead of "patientClaim". Stored as
// vtx.identity.<NanoID>.providerClaim (class identityProviderClaim) = {} — a
// pure existence marker. BindProviderIdentity writes ONE per claimed
// identityKey, CreateOnly: the key ITSELF (identical regardless of WHICH
// provider is claiming) is the lock — a second, different provider passing
// the same identityKey collides at commit (RevisionConflict), never a silent
// double-bind. Declaration-only: no op handler (BindProviderIdentity's
// script, owned by the provider vertexType DDL, writes it).
func identityProviderClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     identityProviderClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"BindProviderIdentity"},
		Description: "Identity provider-claim guard aspect (clinic-domain, attached onto an identity-domain vertex). " +
			"Stored as vtx.identity.<NanoID>.providerClaim (class identityProviderClaim) = {} — a pure existence " +
			"marker, no relationship field. BindProviderIdentity writes ONE per claimed identityKey, CreateOnly: the " +
			"key ITSELF (identical regardless of WHICH provider is claiming) is the lock — a second, different " +
			"provider passing the same identityKey collides at commit (RevisionConflict), never a silent " +
			"double-bind. Declaration-only: no op handler (BindProviderIdentity's script, owned by the provider " +
			"vertexType DDL, writes it).",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. Exclusivity is enforced by the KEY (the identity), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "identity provider-claim guard aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.identity.<NanoID>.providerClaim; claimed once by BindProviderIdentity's identityKey wiring. A second, different provider claiming the same identity is rejected.",
			},
		},
	}
}

func providerVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     providerVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateProvider", "TombstoneProvider", "SetProviderProfile", "SetProviderHours", "SetProviderTimeOff", "BindProviderIdentity"},
		Description: "Clinic provider DDL. Vertex shape: vtx.provider.<NanoID>, class=provider, root data = {} " +
			"(minimal, D5 — the data lives in the .profile aspect). CreateProvider mints the provider + writes the " +
			".profile aspect {fullName (required), specialty (required), credentials?, bio?} atomically. " +
			"SetProviderProfile edits an existing provider's .profile — it REPLACES the aspect with the supplied " +
			"{fullName (required), specialty (required), credentials?, bio?} (the editor seeds the form from the " +
			"projected profile so a replace edits the live set; fullName + specialty stay required so the roster " +
			"lens never loses the provider). TombstoneProvider soft-deletes one. SetProviderHours upserts the .hours availability aspect " +
			"{windows: [{day (0=Sun..6=Sat), openSec, closeSec}]} (UTC seconds-of-day) — the opt-in recurring-weekly " +
			"business-hours windows CreateAppointment / RescheduleAppointment enforce (an out-of-hours booking is " +
			"rejected OutsideHours); an absent .hours aspect or windows=[] means the provider is unconstrained. " +
			"SetProviderTimeOff upserts the .timeOff exceptions aspect {ranges: [{from, to, reason?}]} (RFC3339 UTC " +
			"instants) — the opt-in date-specific blackout layer on top of the recurring hours (vacation / holiday / " +
			"out-sick): a booking overlapping any blocked range is rejected ProviderUnavailable, even if it falls " +
			"inside the weekly .hours; an absent .timeOff aspect or ranges=[] means no blackouts. BindProviderIdentity " +
			"binds an existing provider to a pre-minted vtx.identity (both validated alive + typed): it mints " +
			"lnk.provider.<id>.identifiedBy.identity.<id> (provider identifiedBy identity, Contract #1 §1.1), claims a " +
			"CreateOnly guard aspect on EACH side (.identityClaim on the provider, .providerClaim on the identity — " +
			"mutually exclusive: one identity per provider, one clinic provider per identity), and idempotently grants " +
			"the identity-domain `provider` role via holdsRole (mirrors ClaimIdentity's consumer grant; a link already " +
			"alive is left untouched rather than re-created).",
		Script: providerDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"fullName":{"type":"string","description":"The provider's full name (CreateProvider / SetProviderProfile; required)."},` +
			`"specialty":{"type":"string","description":"The provider's clinical specialty, e.g. Cardiology (CreateProvider / SetProviderProfile; required)."},` +
			`"credentials":{"type":"string","description":"Post-nominal credentials, e.g. MD (CreateProvider / SetProviderProfile; optional)."},` +
			`"bio":{"type":"string","description":"Short provider bio (CreateProvider / SetProviderProfile; optional)."},` +
			`"providerId":{"type":"string","description":"Optional bare NanoID for the new provider vertex (CreateProvider); absent → minted."},` +
			`"providerKey":{"type":"string","description":"vtx.provider.<NanoID> of an existing provider (TombstoneProvider / SetProviderProfile / SetProviderHours / SetProviderTimeOff / BindProviderIdentity; required, validated alive)."},` +
			`"windows":{"type":"array","description":"Availability windows (SetProviderHours; required). Each {day:0-6 (Sun=0), openSec:0-86400, closeSec:0-86400} with openSec<closeSec; UTC seconds-of-day. An empty array clears the constraint.","items":{"type":"object","properties":{"day":{"type":"integer"},"openSec":{"type":"integer"},"closeSec":{"type":"integer"}}}},` +
			`"ranges":{"type":"array","description":"Time-off blackout ranges (SetProviderTimeOff; required). Each {from, to, reason?} with from/to RFC3339 UTC instants and from<to. A booking overlapping any range is rejected (ProviderUnavailable). An empty array clears all blackouts.","items":{"type":"object","properties":{"from":{"type":"string"},"to":{"type":"string"},"reason":{"type":"string"}}}},` +
			`"identityKey":{"type":"string","description":"vtx.identity.<NanoID> of a pre-minted identity to bind to the provider (BindProviderIdentity; required, validated alive + class=identity)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.provider.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"fullName":    "The provider's full name. Stored on the .profile aspect (CreateProvider / SetProviderProfile; required).",
			"specialty":   "The provider's clinical specialty (e.g. Cardiology). Stored on the .profile aspect (CreateProvider / SetProviderProfile; required).",
			"credentials": "Optional post-nominal credentials (e.g. MD, RN). Stored on the .profile aspect when present (CreateProvider / SetProviderProfile).",
			"bio":         "Optional short provider bio. Stored on the .profile aspect when present (CreateProvider / SetProviderProfile).",
			"providerId":  "Optional bare NanoID (no dots / key segments) for the new provider vertex. Absent → minted with nanoid.new().",
			"providerKey": "Full vtx.provider.<NanoID> key of an existing provider vertex (TombstoneProvider tombstones it; SetProviderProfile edits its profile; SetProviderHours sets its recurring availability; SetProviderTimeOff sets its date-specific blackouts; BindProviderIdentity binds it to a login identity).",
			"windows":     "Availability windows (SetProviderHours). A list of {day:0-6 (Sun=0), openSec, closeSec} where openSec/closeSec are UTC seconds-of-day (0..86400) and openSec<closeSec. An empty list clears the constraint (provider becomes unconstrained).",
			"ranges":      "Time-off blackout ranges (SetProviderTimeOff). A list of {from, to, reason?} where from/to are RFC3339 UTC instants and from<to. A booking whose [start,end) overlaps any range is rejected (ProviderUnavailable) even when it falls inside the weekly .hours. An empty list clears all blackouts.",
			"identityKey": "Full vtx.identity.<NanoID> key of a pre-minted identity to bind (BindProviderIdentity; required). Must be alive + class=identity; wires the identifiedBy link, claims CreateOnly guard aspects on BOTH sides (rejected if either side is already bound), and idempotently grants the identity the provider role.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateProvider — register a provider",
				Payload: map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology", "credentials": "MD"},
				ExpectedOutcome: "Mints vtx.provider.<NanoID> (class=provider, root {}) + the .profile aspect " +
					"{fullName, specialty, credentials}. Returns primaryKey (the provider key).",
			},
			{
				Name: "SetProviderProfile — edit an existing provider's profile",
				Payload: map[string]any{
					"providerKey": "vtx.provider.<NanoID>",
					"fullName":    "Dr. Samira Okafor",
					"specialty":   "Cardiology",
					"credentials": "MD, FACC",
				},
				ExpectedOutcome: "Validates the provider is alive + class=provider, then REPLACES " +
					"vtx.provider.<NanoID>.profile with {fullName, specialty, credentials?, bio?}. fullName + " +
					"specialty are required (the roster lens keys on fullName); an omitted credentials/bio clears " +
					"that field. Returns primaryKey (the provider key).",
			},
			{
				Name: "SetProviderHours — Mon/Wed 09:00–17:00 UTC",
				Payload: map[string]any{
					"providerKey": "vtx.provider.<NanoID>",
					"windows": []any{
						map[string]any{"day": 1, "openSec": 32400, "closeSec": 61200},
						map[string]any{"day": 3, "openSec": 32400, "closeSec": 61200},
					},
				},
				ExpectedOutcome: "Validates the provider is alive + class=provider and each window (day 0-6, " +
					"0<=openSec<closeSec<=86400), then upserts vtx.provider.<NanoID>.hours {windows}. Subsequent " +
					"CreateAppointment / RescheduleAppointment reject a booking outside these windows (OutsideHours). " +
					"windows=[] clears the constraint.",
			},
			{
				Name: "SetProviderTimeOff — block a vacation week",
				Payload: map[string]any{
					"providerKey": "vtx.provider.<NanoID>",
					"ranges": []any{
						map[string]any{"from": "2026-07-06T00:00:00Z", "to": "2026-07-13T00:00:00Z", "reason": "Vacation"},
					},
				},
				ExpectedOutcome: "Validates the provider is alive + class=provider and each range (from/to RFC3339 " +
					"UTC, from<to), then upserts vtx.provider.<NanoID>.timeOff {ranges}. Subsequent CreateAppointment / " +
					"RescheduleAppointment reject a booking overlapping any range (ProviderUnavailable), even inside the " +
					"weekly .hours. ranges=[] clears all blackouts.",
			},
			{
				Name:    "BindProviderIdentity — bind a provider to its login identity",
				Payload: map[string]any{"providerKey": "vtx.provider.<NanoID>", "identityKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Validates both endpoints alive + typed, mints lnk.provider.<id>.identifiedBy.identity.<id>, " +
					"claims CreateOnly guard aspects on both sides (rejected if either side is already bound), and " +
					"idempotently grants the identity the provider role via holdsRole. Returns primaryKey (the identifiedBy link key).",
			},
		},
	}
}

func appointmentVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     appointmentVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "CorrectAppointmentStatus", "MarkPastDueNoShow", "BackfillAppointmentSite", "SetAppointmentSite", "RecordEncounter", "TombstoneAppointment"},
		Description: "Clinic appointment DDL. Vertex shape: vtx.appointment.<NanoID>, class=appointment, root data = " +
			"{} (minimal, D5). CreateAppointment validates the patient (class=patient) + provider (class=provider) " +
			"are alive, then atomically mints the appointment + the .schedule aspect {startsAt, endsAt, remindAt, reason?} + " +
			"the .status aspect {value: scheduled} + the forPatient link (appointment→patient) + the withProvider " +
			"link (appointment→provider). Both links follow Contract #1 §1.1 (the later-arriving appointment is the " +
			"source). RescheduleAppointment rewrites the .schedule aspect with new startsAt/endsAt (re-deriving " +
			"remindAt = startsAt − 24h so the clinic-reminders @at re-arms for a not-yet-sent reminder), leaving the " +
			"links untouched; a confirmed or checkedIn visit returns to scheduled (the confirmation was for the old " +
			"date, and a recorded arrival must not exempt a moved visit from the past-due sweep — the " +
			"clinic.appointmentRescheduled event carries statusReset: true), a scheduled one is re-stamped " +
			"unchanged in the same batch (so a concurrent patient confirm of the old date conflicts under OCC), " +
			"a never-set status is left absent; an omitted reason clears it (the caller carries the existing reason); a " +
			"terminal appointment is never moved (TerminalStatus). " +
			"SetAppointmentStatus upserts the .status aspect to one of {scheduled, confirmed, checkedIn, completed, " +
			"cancelled, noShow}, with an optional audit note (a cancel / no-show reason, stored on .status distinct " +
			"from the .schedule visit reason). completed / noShow are accepted only once the visit has started " +
			"(op.submittedAt at or after .schedule.startsAt; NotYetStarted before) — a staff cancel carries no clock. " +
			"Transitioning to noShow also stores a noShowFeeCents amount on .status " +
			"(caller-supplied positive number, or a 2500 default when omitted) — the billing consequence a no-show " +
			"otherwise lacked; clinic-ledger's clinicNoShowSettlement lens bills the fee's presence on the current " +
			"status, whichever op wrote it, with a DebitAccount charge. A patient may cancel or confirm their OWN " +
			"appointment (the consumer scope=self grant, status=cancelled or confirmed; any other value is AuthDenied). " +
			"A patient's own cancel reads a three-state clock against .schedule.startsAt: at or after startsAt " +
			"it is refused VisitStarted (the front desk records the outcome); inside the 24-hour late-cancel window " +
			"(startsAt − 24h, the reminder lead) it lands with lateCancel: true and the 2500 no-show fee on .status; " +
			"earlier it is free. A patient's own confirm is refused AuthDenied once the desk has checked the patient " +
			"in (a self write never undoes the desk's record) and VisitStarted at or after startsAt; inside the " +
			"24-hour window it lands (confirming is what the reminder asks for), and confirmed → confirmed is " +
			"idempotent. A patient's own RescheduleAppointment reads the same clock: VisitStarted once started, " +
			"LateReschedule inside the window (cancel, or call the desk — a free late move would sidestep the fee). " +
			"Staff paths are unaffected. The terminal statuses {cancelled, completed, noShow} are FINAL: " +
			"re-setting the same terminal value is idempotent (a cancelled re-set carries lateCancel + noShowFeeCents " +
			"forward), but changing a terminal status to a different one is " +
			"rejected (TerminalStatus) so a finished / cancelled visit cannot silently revert; non-terminal statuses " +
			"move freely. CorrectAppointmentStatus{appointmentKey, status, note, noShowFeeCents?} is the explicit repair for a WRONG " +
			"terminal call — the move SetAppointmentStatus refuses (an auto no-show on a patient who was actually " +
			"seen). It transitions ONLY between the terminal values (NotTerminal if the appointment never reached one; " +
			"InvalidArgument for a non-terminal target; NotYetStarted for a completed / noShow target ahead of the " +
			"visit's startsAt), touches no slot-claim cells and no patientSelfDayClaim (the first terminal transition " +
			"already released them, so it takes no provider/patient), and REQUIRES an audit note. A correction onto " +
			"noShow carries noShowFeeCents exactly as SetAppointmentStatus does (caller-supplied positive number or the " +
			"2500 default), so the ledger charges it; a correction onto completed / cancelled writes no fee, and " +
			"clinic-ledger's missing_reversal gap reverses a charge already posted for the prior noShow or late " +
			"cancel — the waiver. It records the " +
			"overwritten value as .status.correctedFrom and emits clinic.appointmentStatusCorrected. Staff-only " +
			"(operator / front-of-house / the appointment's own bound provider, workplace-confined exactly as " +
			"SetAppointmentStatus's staff path) — no patient self-service scope. It never re-opens a terminal " +
			"appointment to scheduled/confirmed/checkedIn: that would re-claim released cells against whatever has " +
			"been booked since, and is out of scope. RecordEncounter is refused VisitNotHeld against a cancelled or noShow appointment (a visit " +
			"that did not take place cannot be documented) and NotYetStarted ahead of .schedule.startsAt (the same " +
			"inclusive, soft-clock boundary SetAppointmentStatus's terminal transitions read) — completed / scheduled " +
			"/ confirmed / checkedIn past their start document normally; this op never writes .status. It is " +
			"record-or-amend over two sibling aspects along the sensitivity boundary: .encounter — the " +
			"raw clinical record {summary, assessment, plan, superseded}, SENSITIVE, its DEK custodied on the clinicalRecord " +
			"retention class (never on the patient's identity), readable only through the clinicEncountersRead Secure Lens, which decrypts it at projection for the treating provider — and .documentation " +
			"— the OPERATIONAL, non-PHI signals {documentedAt, amendedAt?, followUpRequested, " +
			"followUpDate?} that the clinicAppointments lens DOES project (presence-of-documentation + " +
			"follow-up scheduling, never the clinical content). With no record yet (no live .documentation carrying " +
			"a documentedAt) it writes the first record: documentedAt = op.submittedAt, superseded = []. With a " +
			"record present it AMENDS: the current text moves onto superseded with the instant it was recorded " +
			"(the prior amendedAt, else documentedAt), documentedAt is preserved as when the visit was FIRST " +
			"documented, amendedAt = op.submittedAt says when the current text was recorded, and the follow-up " +
			"signals are the payload's. An amendment that changes nothing (same three texts, same followUpRequested, " +
			"same normalized followUpDate) writes nothing and emits nothing; a change to the follow-up signals alone " +
			"rewrites .documentation (amendedAt carried, .encounter not written); past 40 superseded versions " +
			"(MAX_ENCOUNTER_AMENDMENTS) a text amendment is refused AmendmentLimit; a text longer than 4000 bytes is InvalidArgument. TombstoneAppointment soft-deletes the appointment, releasing its held cells and day claim unless it already reached a terminal status (that transition released them, and another visit may hold them since). The " +
			"clinic's booking grid is a mandatory 15-minute cadence (:00/:15/:30/:45; SlotGridViolation if startsAt/endsAt " +
			"misalign, AppointmentTooLong past 24h/96 cells): CreateAppointment AND RescheduleAppointment discretize " +
			"[startsAt,endsAt) into its covered 15-minute cells and CLAIM a deterministic slot-claim aspect per cell on " +
			"BOTH the provider and patient hub vertices (vtx.provider.<p>.slot<cellcode> / vtx.patient.<pt>.slot<cellcode>) " +
			"— the write-path CreateOnly/expectedRevision conditioning on each cell key IS the double-book lock (no read-time " +
			"enumeration, no serialization epoch): a live claim on any covered cell rejects with SlotConflict (provider) or " +
			"PatientDoubleBook (patient) (Capability-KV §06 — the op's own Starlark logic). RescheduleAppointment releases " +
			"the cells the appointment no longer needs and claims the new ones in the same atomic batch (a collision leaves " +
			"the original booking fully intact); SetAppointmentStatus releases all held cells on a terminal transition " +
			"(cancelled/completed/noShow). A patient booking for THEMSELVES (the consumer scope=self path) additionally " +
			"holds ONE open visit per provider per UTC calendar day: CreateAppointment on that path records " +
			".schedule.selfBooked = true and claims a patientSelfDayClaim existence marker on the patient hub " +
			"(vtx.patient.<pt>.selfday<yyyymmdd><providerId>, the same CreateOnly-is-the-lock idiom as the cells), so a " +
			"second self-booked open visit with the same provider on the same day is rejected SelfBookingLimit; the front " +
			"desk is unrestricted (its bookings never claim a day). The claim follows the visit: RescheduleAppointment " +
			"moves a self-booked visit's claim to the new day (SelfBookingLimit if that day is already held), and every " +
			"terminal transition / tombstone releases it alongside the cells. Both also enforce the provider's opt-in availability windows (the .hours aspect, " +
			"set by SetProviderHours): a booking outside a provider's business hours is rejected (OutsideHours); a provider " +
			"with no .hours is unconstrained. Both also enforce the provider's opt-in date-specific time-off (the .timeOff " +
			"aspect, set by SetProviderTimeOff): a booking overlapping any blackout range is rejected (ProviderUnavailable), " +
			"even when it falls inside the weekly .hours; a provider with no .timeOff is unrestricted. Both also reject a " +
			"startsAt at or before op.submittedAt " +
			"(ScheduleInPast) — a soft past-time guard (submittedAt is caller-supplied; the host clock is " +
			"not exposed to Starlark). CreateAppointment also accepts an optional leaseAppKey (mirrors " +
			"wellness-domain's CreateBooking resident-rate check): when the leaseapp is alive, carries a " +
			".tenancy aspect with no endedAt, and its applicant identity matches the patient's own identifiedBy identity, " +
			"a residentVisit link (appointment→leaseapp) is written — a mismatch or absent lease falls " +
			"through silently, never a hard failure. CreateAppointment also accepts an optional site (vtx.building.<NanoID>, " +
			"a location-domain building carrying a clinicSite .site profile): when supplied, the building must be alive + " +
			"a vtx.building.<NanoID> key AND the provider must practicesAt it (the clinicSiteAssignment link) — a wrong-typed key or " +
			"a provider not assigned to that site is REJECTED (UnknownSite / NotALocation / ProviderNotAtSite; unlike " +
			"leaseAppKey this is a hard requirement once supplied, not a silent fall-through), and an atSite link " +
			"(appointment→building) is written. Omitted site records no site (backward-compatible; does not yet gate " +
			"hours — a follow-up design decision, per the multi-site design note). MarkPastDueNoShow{appointmentKey} is " +
			"the orchestration-internal SetAppointmentStatus(noShow) counterpart clinic-reminders' pastDueAppointments " +
			"Weaver target dispatches once a non-terminal appointment's .schedule.endsAt passes with no staff status " +
			"update: a no-op if the appointment already reached a terminal status by dispatch time (an at-least-once " +
			"race, never clobbers a legitimate completed/cancelled outcome), if its .schedule.endsAt is still ahead " +
			"of op.submittedAt (the visit was moved after the lapse it was armed on — the sweep re-arms on the new " +
			"date), OR if the provider's .timeOff covers the " +
			"visit (the missed visit is the provider's unavailability, not the patient's no-show, so front desk " +
			"resolves it manually instead of the sweep silently blaming the patient), otherwise the SAME .status " +
			"upsert (deliberately with NO noShowFeeCents — the automated sweep marks a documentation lapse, not a " +
			"billed missed visit; only a staff-observed SetAppointmentStatus(noShow) bills) + held-cell release as " +
			"SetAppointmentStatus's terminal branch minus the fee. It resolves " +
			"provider/patient LIVE off the appointment's own withProvider/forPatient links (the bounded, exactly-one-link " +
			"read appointment_provider/appointment_patient already use elsewhere in this script) rather than requiring " +
			"them as caller-supplied + link-validated params like SetAppointmentStatus does — Weaver's directOp dispatch " +
			"can only template row.<column> / row.<column>.<aspect> keys into ContextHint.Reads (Contract #10 §10.8), " +
			"which cannot express the withProvider/forPatient LINK read SetAppointmentStatus's caller-supplied path " +
			"requires, so this op has no human caller to supply them. BackfillAppointmentSite{appointmentKey} is the " +
			"orchestration-internal auto-remediation twin of CreateAppointment's own site-writing branch, dispatched by " +
			"the clinicSiteBackfill Weaver target's missing_site gap (lenses.go) for a LIVE appointment carrying no " +
			"atSite link — the pre-existing corpus CreateAppointment minted before a site was ever supplied, plus any " +
			"future appointment booked without one. A no-op (empty mutations/events) if the appointment already carries " +
			"a live atSite link (another dispatch already won, or a redelivery). Otherwise it resolves the appointment's " +
			"own provider LIVE off its withProvider link (appointment_provider, the same bounded read MarkPastDueNoShow " +
			"uses above) and looks up that provider's practicesAt sites (sites_for_provider), keeping only those whose " +
			"building is still alive (TombstoneLocation cascades onto no practicesAt link, so a decommissioned site " +
			"lingers on the provider). When EXACTLY ONE live site " +
			"comes back, it writes the atSite link — the identical mutation CreateAppointment's own site branch writes. " +
			"When ZERO or TWO-OR-MORE live sites come back, it is ambiguous which site this appointment belongs to and the op " +
			"never guesses: it no-ops cleanly, the same sole-site fallback semantics the booking UI's own client-side " +
			"site auto-fill applies (cmd/clinic-app/web/app.js). Such an appointment stays missing_site forever, which " +
			"is harmless — the gap is idempotently re-dispatched and cleanly no-ops every time, exactly the " +
			"non-convergence posture cafe-domain's own BackfillTabStaleAt/missing_staleat gap relies on for a case that " +
			"can never resolve on its own. SetAppointmentSite{appointmentKey, site} is the human-facing manual " +
			"counterpart, letting a person CHOOSE among a provider's live sites when BackfillAppointmentSite's " +
			"exactly-one-site rule can't (two-or-more sites) — it is not a way to invent a practicesAt " +
			"relationship that doesn't exist: a provider at ZERO sites, or now tombstoned, still needs " +
			"AssignProviderSite run first, since site is HARD-validated exactly like CreateAppointment's site " +
			"branch (require_site_membership: alive + a vtx.building.<NanoID> key AND the appointment's own " +
			"provider practicesAt it). Confinement mirrors RescheduleAppointment (operator / workplace / the " +
			"appointment's own bound provider), not BackfillAppointmentSite (which has no human caller to " +
			"confine). A no-op if the appointment already carries a live atSite link — reassigning an already-set " +
			"site is out of scope. Writes the same atSite link mutation as BackfillAppointmentSite, plus a " +
			"CreateOnly .siteAssignment guard aspect in the same atomic batch (siteAssignmentAspectTypeDDL) so two " +
			"concurrent callers choosing different sites for the same appointment can't both commit — the " +
			"loser's whole batch rejects.",
		Script: appointmentDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"patient":{"type":"string","description":"vtx.patient.<NanoID> the appointment is for (CreateAppointment / RescheduleAppointment; required; on create validated alive + class=patient, on reschedule/terminal-SetAppointmentStatus/TombstoneAppointment it must be the appointment's actual patient, validated via the forPatient link)."},` +
			`"provider":{"type":"string","description":"vtx.provider.<NanoID> the appointment is with (CreateAppointment / RescheduleAppointment; required, validated alive + class=provider; on reschedule/terminal-SetAppointmentStatus/TombstoneAppointment it must be the appointment's actual provider, validated via the withProvider link)."},` +
			`"startsAt":{"type":"string","description":"Appointment start, RFC3339 (CreateAppointment / RescheduleAppointment; required). Caller supplies canonical UTC, aligned to the clinic's 15-minute booking grid (:00/:15/:30/:45; SlotGridViolation otherwise)."},` +
			`"endsAt":{"type":"string","description":"Appointment end, RFC3339 (CreateAppointment / RescheduleAppointment; required). Caller supplies canonical UTC, aligned to the 15-minute grid; span capped at 96 cells / 24h (AppointmentTooLong)."},` +
			`"reason":{"type":"string","description":"Visit reason / chief complaint (CreateAppointment / RescheduleAppointment; optional — on RescheduleAppointment an omitted reason clears it)."},` +
			`"leaseAppKey":{"type":"string","description":"Optional vtx.leaseapp.<NanoID> the patient claims residency under (CreateAppointment; optional). Checked against the lease's applicationFor link matching the patient's identifiedBy identity — a mismatch falls through with no residentVisit link, never a hard failure."},` +
			`"site":{"type":"string","description":"vtx.building.<NanoID> clinic site the appointment is booked at. Optional on CreateAppointment; required on SetAppointmentSite (no-op if the appointment already carries one). When supplied, validated alive + a vtx.building.<NanoID> key AND that the provider practicesAt it (clinicSiteAssignment) — a mismatch is REJECTED, not a silent fall-through. Writes an atSite link (appointment→building)."},` +
			`"appointmentId":{"type":"string","description":"Optional bare NanoID for the new appointment vertex (CreateAppointment); absent → minted."},` +
			`"appointmentKey":{"type":"string","description":"vtx.appointment.<NanoID> of an existing appointment (RescheduleAppointment / SetAppointmentStatus / CorrectAppointmentStatus / MarkPastDueNoShow / BackfillAppointmentSite / SetAppointmentSite / TombstoneAppointment; required, validated alive)."},` +
			`"status":{"type":"string","enum":["scheduled","confirmed","checkedIn","completed","cancelled","noShow"],"description":"New status (SetAppointmentStatus; required). Transitioning TO a terminal value (completed/cancelled/noShow) for the first time also requires provider + patient (to release the held slot-claim cells; omitted on a non-terminal transition or an idempotent same-value re-set). CorrectAppointmentStatus also requires it, restricted to the three terminal values."},` +
			`"note":{"type":"string","description":"Audit note for the transition, e.g. a cancel / no-show reason (SetAppointmentStatus; optional). REQUIRED on CorrectAppointmentStatus — a correction rewrites a record already treated as final. Stored on .status, distinct from the .schedule visit reason; an omitted note carries none."},` +
			`"noShowFeeCents":{"type":"number","description":"Optional no-show fee in integer cents, only meaningful when status is noShow (SetAppointmentStatus / CorrectAppointmentStatus; optional, must be > 0 when supplied). Defaults to 2500 when omitted. Stored on .status; clinic-ledger's clinicNoShowSettlement lens reads it to post a DebitAccount charge against the patient's ledger account. A patient's own cancel inside the 24-hour late-cancel window stores the 2500 default itself (with lateCancel: true) — never caller-supplied on that path."},` +
			`"summary":{"type":"string","maxLength":4000,"description":"Visit summary / clinical note (RecordEncounter; required). RAW clinical content, stored SENSITIVE on .encounter — DEK custodied on the clinicalRecord retention class; reaches a reader only through the clinicEncountersRead Secure Lens, decrypted at projection for the treating provider."},` +
			`"assessment":{"type":"string","maxLength":4000,"description":"Clinical assessment / diagnosis (RecordEncounter; optional). RAW PHI, stored SENSITIVE on .encounter — decrypted at projection into clinicEncountersRead for the treating provider only."},` +
			`"plan":{"type":"string","maxLength":4000,"description":"Treatment plan / orders (RecordEncounter; optional). RAW PHI, stored SENSITIVE on .encounter — decrypted at projection into clinicEncountersRead for the treating provider only. The clinical reason for any follow-up belongs here, not in the operational followUp fields."},` +
			`"followUpRequested":{"type":"boolean","description":"Whether the visit calls for a follow-up (RecordEncounter; optional, default false). OPERATIONAL, non-PHI — stored on .documentation, projected (the existence of a follow-up, like an appointment time)."},` +
			`"followUpDate":{"type":"string","format":"date","x-visibleWhen":{"field":"followUpRequested","equals":true},"description":"Suggested follow-up date, RFC3339 / date (RecordEncounter; required when followUpRequested is true, otherwise ignored). OPERATIONAL, non-PHI — stored on .documentation, projected only when followUpRequested is true."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.appointment.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"patient":           "Full vtx.patient.<NanoID> key the appointment is for. CreateAppointment validates it is alive + class=patient, writes the forPatient link, and claims a patientSlotClaim aspect per covered 15-minute cell. RescheduleAppointment / a terminal SetAppointmentStatus / TombstoneAppointment also require it (the appointment's actual patient, validated via the forPatient link) to release/claim cells against the patient's other bookings (PatientDoubleBook).",
			"provider":          "Full vtx.provider.<NanoID> key the appointment is with. CreateAppointment validates it is alive + class=provider, writes the withProvider link, and claims a providerSlotClaim aspect per covered 15-minute cell. RescheduleAppointment / a terminal SetAppointmentStatus / TombstoneAppointment also require it (the appointment's actual provider, validated via the withProvider link) to release/claim cells against the provider's other bookings (SlotConflict).",
			"startsAt":          "Appointment start (RFC3339, canonical UTC). Stored on the .schedule aspect (CreateAppointment / RescheduleAppointment; required). Must be in the future relative to op.submittedAt — a past / now startsAt is rejected (ScheduleInPast). Must align to the 15-minute grid (SlotGridViolation).",
			"endsAt":            "Appointment end (RFC3339, canonical UTC). Stored on the .schedule aspect (CreateAppointment / RescheduleAppointment; required). Must align to the 15-minute grid; span capped at 96 cells / 24h (AppointmentTooLong).",
			"reason":            "Optional visit reason / chief complaint. Stored on the .schedule aspect when present (CreateAppointment / RescheduleAppointment; on RescheduleAppointment an omitted reason clears it).",
			"leaseAppKey":       "Optional full vtx.leaseapp.<NanoID> key the patient claims residency under (CreateAppointment). Verified via the lease's applicationFor link matching the patient's own identifiedBy identity before writing a residentVisit link (appointment→leaseapp); a mismatch or absent lease silently omits the link.",
			"site":              "Full vtx.building.<NanoID> clinic site key. Optional on CreateAppointment; required on SetAppointmentSite (no-op if the appointment already carries a live one). Validated alive + a vtx.building.<NanoID> key AND that the provider practicesAt it (clinicSiteAssignment link) — rejected (UnknownSite / NotALocation / ProviderNotAtSite) if either check fails, not a silent fall-through. Writes an atSite link (appointment→building).",
			"appointmentId":     "Optional bare NanoID (no dots / key segments) for the new appointment vertex. Absent → minted with nanoid.new().",
			"appointmentKey":    "Full vtx.appointment.<NanoID> key of an existing appointment (RescheduleAppointment rewrites its .schedule; SetAppointmentStatus / CorrectAppointmentStatus / MarkPastDueNoShow / BackfillAppointmentSite / SetAppointmentSite validate it alive + class=appointment; TombstoneAppointment validates it alive).",
			"status":            "New appointment status, one of {scheduled, confirmed, checkedIn, completed, cancelled, noShow} (SetAppointmentStatus; required). The first transition to a terminal value also requires provider + patient. CorrectAppointmentStatus requires it too, restricted to the three terminal values.",
			"note":              "Audit note recorded with a status transition (e.g. a cancel / no-show reason). Optional on SetAppointmentStatus, REQUIRED on CorrectAppointmentStatus. Stored on the .status aspect, distinct from the .schedule visit reason; omitted → no note.",
			"noShowFeeCents":    "Optional no-show fee in integer cents (SetAppointmentStatus / CorrectAppointmentStatus, only meaningful when status is noShow; must be > 0 when supplied, defaults to 2500 when omitted). Stored on the .status aspect; read by clinic-ledger's clinicNoShowSettlement lens to post a DebitAccount charge. A patient's own late cancel (inside startsAt − 24h) stores the 2500 default with lateCancel: true.",
			"summary":           "Required visit summary / clinical note (RecordEncounter). RAW clinical content stored SENSITIVE on .encounter — DEK custodied on the clinicalRecord retention class; reaches a reader only through the clinicEncountersRead Secure Lens, decrypted at projection for the treating provider.",
			"assessment":        "Optional clinical assessment / diagnosis (RecordEncounter). RAW PHI stored SENSITIVE on .encounter — decrypted at projection into clinicEncountersRead for the treating provider only.",
			"plan":              "Optional treatment plan / orders (RecordEncounter). RAW PHI stored SENSITIVE on .encounter — decrypted at projection into clinicEncountersRead for the treating provider only. The clinical reason for a follow-up lives here, not in the operational followUp fields.",
			"followUpRequested": "Optional boolean (default false): does this visit need a follow-up (RecordEncounter)? OPERATIONAL, non-PHI — stored on .documentation, projected into clinicAppointments (the existence of a follow-up, like an appointment time, is not clinical content).",
			"followUpDate":      "Suggested follow-up date (RFC3339 / date) (RecordEncounter; required when followUpRequested is true, MissingFollowUpDate otherwise). OPERATIONAL, non-PHI — stored on .documentation, projected only when followUpRequested is true.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "CreateAppointment — book a patient with a provider",
				Payload: map[string]any{
					"patient":  "vtx.patient.<patientNanoID>",
					"provider": "vtx.provider.<providerNanoID>",
					"startsAt": "2026-07-01T15:00:00Z",
					"endsAt":   "2026-07-01T15:30:00Z",
					"reason":   "Annual checkup",
				},
				ExpectedOutcome: "Validates the patient (class=patient) + provider (class=provider) are alive and " +
					"startsAt/endsAt align to the 15-minute grid. Atomically commits vtx.appointment.<NanoID> (root {}) + " +
					".schedule {startsAt, endsAt, remindAt, reason} (remindAt = startsAt − 24h, derived) + .status " +
					"{value: scheduled} + the forPatient + withProvider links + one providerSlotClaim/patientSlotClaim " +
					"aspect per covered 15-minute cell. On the consumer scope=self path .schedule also carries selfBooked: true " +
					"and one patientSelfDayClaim aspect (vtx.patient.<pt>.selfday<yyyymmdd><providerId>) is claimed; the " +
					"desk path records neither. Returns primaryKey (the appointment key). Rejects with ScriptError " +
					"if the patient or provider is absent / dead / the wrong class, a misaligned start/end " +
					"(SlotGridViolation), a provider double-book (SlotConflict), a patient double-book across " +
					"providers (PatientDoubleBook), or — self path only — a second open self-booked visit with the same " +
					"provider on the same UTC day (SelfBookingLimit).",
			},
			{
				Name: "RescheduleAppointment — move an appointment to a new time",
				Payload: map[string]any{
					"appointmentKey": "vtx.appointment.<NanoID>",
					"provider":       "vtx.provider.<providerNanoID>",
					"patient":        "vtx.patient.<patientNanoID>",
					"startsAt":       "2026-07-02T16:00:00Z",
					"endsAt":         "2026-07-02T16:30:00Z",
					"reason":         "Annual checkup",
				},
				ExpectedOutcome: "Validates the appointment is alive + class=appointment, that the passed provider / " +
					"patient are its actual provider / patient (via the withProvider / forPatient links), and that the new " +
					"startsAt/endsAt align to the 15-minute grid, then rewrites the .schedule aspect {startsAt, endsAt, " +
					"remindAt, reason?} with the new times — re-deriving remindAt = startsAt − 24h (canonical UTC) so the " +
					"clinic-reminders @at re-arms for a not-yet-sent reminder. In the same atomic batch it releases the " +
					"provider/patient slot-claim cells the appointment no longer needs and claims the newly-covered ones, " +
					"conflict-checked against both the provider's book (SlotConflict) and the patient's book " +
					"(PatientDoubleBook) — a collision leaves the original booking's claims fully intact. A self-booked visit " +
					"(.schedule.selfBooked, carried forward unchanged) moving to another UTC day releases its old day's " +
					"patientSelfDayClaim and claims the new day's in the same batch (SelfBookingLimit if the patient already " +
					"holds that day with this provider — for any mover, staff included); a same-day move touches no day " +
					"claim. The forPatient / " +
					"withProvider links are untouched; a confirmed or checkedIn .status is reset to {value: scheduled} in " +
					"the same batch (the event carries statusReset: true), a scheduled one is re-stamped unchanged, an " +
					"absent one is left alone. An " +
					"omitted reason clears it (the caller carries the existing reason). Returns primaryKey.",
			},
			{
				Name:    "SetAppointmentStatus — confirm an appointment",
				Payload: map[string]any{"appointmentKey": "vtx.appointment.<NanoID>", "status": "confirmed"},
				ExpectedOutcome: "Validates the appointment is alive + class=appointment, then upserts the .status aspect " +
					"{value: confirmed} (unconditioned — re-runnable). Returns primaryKey. Rejects a status outside the enum.",
			},
			{
				Name: "SetAppointmentStatus — mark a no-show",
				Payload: map[string]any{"appointmentKey": "vtx.appointment.<NanoID>", "status": "noShow",
					"provider": "vtx.provider.<providerNanoID>", "patient": "vtx.patient.<patientNanoID>"},
				ExpectedOutcome: "Validates the appointment is alive + provider/patient match its withProvider/forPatient links " +
					"and that the visit has started (op.submittedAt at or after .schedule.startsAt; NotYetStarted otherwise), " +
					"then upserts the .status aspect {value: noShow, noShowFeeCents: 2500} (the default, since noShowFeeCents " +
					"was omitted) and releases the appointment's held slot-claim cells. Emits clinic.appointmentStatusSet. " +
					"clinic-ledger's clinicNoShowSettlement lens picks up the fee and posts a DebitAccount charge once the " +
					"patient has a ledger account. Returns primaryKey.",
			},
			{
				Name: "CorrectAppointmentStatus — the no-show who was actually seen",
				Payload: map[string]any{"appointmentKey": "vtx.appointment.<NanoID>", "status": "completed",
					"note": "Patient was present and seen; auto no-show was wrong."},
				ExpectedOutcome: "Validates the appointment is alive + class=appointment and that the caller is the " +
					"operator, the appointment's own bound provider, or workplace-confined to one of its sites. " +
					"Requires the CURRENT status to be terminal (NotTerminal otherwise — the first terminal " +
					"transition is SetAppointmentStatus's job) and the target status to be one of the three terminal " +
					"values (InvalidArgument otherwise — this op never re-opens an appointment). Requires a note. " +
					"Upserts .status {value: completed, note, correctedFrom: noShow} — no slot-claim cell or self-day " +
					"claim moves, since the first terminal transition already released them — and no noShowFeeCents, so " +
					"clinic-ledger's missing_reversal gap credits back the fee the wrong no-show charged. (A " +
					"correction onto noShow instead carries noShowFeeCents: caller-supplied or the 2500 default, " +
					"charged the same way.) Emits clinic.appointmentStatusCorrected. Returns primaryKey.",
			},
			{
				Name:    "MarkPastDueNoShow — Weaver-dispatched auto no-show (pastDueAppointments target)",
				Payload: map[string]any{"appointmentKey": "vtx.appointment.<NanoID>"},
				ExpectedOutcome: "Validates the appointment is alive + class=appointment. If it already reached a terminal " +
					"status (completed/cancelled/noShow) by dispatch time, no-ops (a legitimate at-least-once race — never " +
					"clobbers a real outcome); likewise no-ops if the provider's .timeOff covers the visit (the provider " +
					"was unavailable — front desk resolves it manually instead of the sweep blaming the patient). " +
					"Otherwise resolves provider/patient LIVE off the appointment's own " +
					"withProvider/forPatient links, upserts the .status aspect {value: noShow, note: \"Auto no-show: " +
					"appointment ended without a status update\"} (deliberately no noShowFeeCents — only a " +
					"staff-observed SetAppointmentStatus(noShow), a noShow correction, or the patient's own late " +
					"cancel bills), and releases the held slot-claim " +
					"cells — the same effect as a staff-submitted SetAppointmentStatus(noShow) minus the fee and the " +
					"caller-supplied provider/patient params a human dispatcher would send. Emits clinic.appointmentStatusSet{auto: true}. " +
					"Submitted under Weaver's service-actor authority only (clinic-reminders' pastDueAppointments target); " +
					"no human/consumer caller.",
			},
			{
				Name:    "BackfillAppointmentSite — backfill a missing atSite link (orchestration-internal)",
				Payload: map[string]any{"appointmentKey": "vtx.appointment.<NanoID>"},
				ExpectedOutcome: "Validates the appointment is alive + class=appointment. No-ops cleanly (empty " +
					"mutations/events) if it already carries a live atSite link. Otherwise resolves its provider LIVE " +
					"off the withProvider link and looks up that provider's practicesAt sites, counting only those whose " +
					"building is still alive: when exactly one comes " +
					"back, writes the atSite link (the same mutation CreateAppointment's own site branch writes) and " +
					"returns primaryKey as that LINK key (the op's only mutation, the AssignProviderSite convention); " +
					"when zero or two-or-more come back, no-ops cleanly rather than guess. Submitted under Weaver's " +
					"service-actor authority only (clinicSiteBackfill target); no human/consumer caller.",
			},
			{
				Name:    "SetAppointmentSite — a staffer supplies the site BackfillAppointmentSite couldn't resolve",
				Payload: map[string]any{"appointmentKey": "vtx.appointment.<NanoID>", "site": "vtx.building.<NanoID>"},
				ExpectedOutcome: "Validates the appointment is alive + class=appointment, and (for a non-operator caller) " +
					"that the actor worksAt one of the appointment's own sites OR is the appointment's own bound provider. " +
					"No-ops cleanly if the appointment already carries a live atSite link — reassigning an already-set " +
					"site is out of scope. Otherwise validates site alive + a vtx.building.<NanoID> key AND the " +
					"appointment's own provider practicesAt it (ProviderNotAtSite if not), writes the atSite link (the " +
					"same mutation BackfillAppointmentSite/CreateAppointment's site branch write), and returns primaryKey " +
					"as that LINK key. Submitted by the operator, front-of-house staff, or the appointment's own bound " +
					"provider.",
			},
			{
				Name: "RecordEncounter — document a completed visit",
				Payload: map[string]any{
					"appointmentKey":    "vtx.appointment.<NanoID>",
					"summary":           "Patient seen for annual checkup; vitals normal.",
					"assessment":        "Essential hypertension, well-controlled.",
					"plan":              "Continue current medication; recheck in 6 months.",
					"followUpRequested": true,
					"followUpDate":      "2027-01-15T15:00:00Z",
				},
				ExpectedOutcome: "Validates the appointment is alive + class=appointment, then records or amends two sibling aspects: .encounter — " +
					"the RAW clinical record {summary, assessment, plan, superseded} (an unfilled optional written as \"\"; superseded = [] on the first record), SENSITIVE, DEK custodied on the clinicalRecord retention " +
					"class and readable only through the clinicEncountersRead Secure Lens — and .documentation — the OPERATIONAL signals {documentedAt (= op.submittedAt, canonical " +
					"UTC, when the visit was FIRST documented), followUpRequested, followUpDate?} that the clinicAppointments lens DOES project. A second submission " +
					"with different content is an amendment: the prior text is appended to superseded with its recordedAt, documentedAt is preserved, amendedAt = op.submittedAt " +
					"is recorded; an identical re-submit writes nothing; a follow-up-only change rewrites .documentation alone; past 40 superseded versions a text amendment is refused AmendmentLimit. Returns primaryKey.",
			},
		},
	}
}

// demographicsAspectTypeDDL declares the .demographics aspect (class
// patientDemographics) — the step-6 write gate for CreatePatient. Declaration-only
// (the script lives on the patient vertexType DDL). NON-sensitive: it attaches to
// a vtx.patient, not an identity — which is precisely why an IDENTIFIED patient's
// name does not live here (it would outlive that person's ShredIdentityKey) and
// why the aspect carries registeredAt, a field that identifies nobody, as the
// presence signal the lenses' ghost-vertex filters test.
func demographicsAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     demographicsAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreatePatient", "BackfillPatientRegistration", "BindPatientIdentity", "UnbindPatientIdentity"},
		Description: "Patient demographics aspect (clinic). Stored as vtx.patient.<NanoID>.demographics (class " +
			"patientDemographics) = {registeredAt, fullName?}. NON-sensitive (it attaches to a patient, not an " +
			"identity) and carries no contact PII — that lives on the identity CreatePatient's optional identityKey " +
			"links via identifiedBy. fullName is present ONLY for a patient with no such identity: when there IS " +
			"one, the name is written to that identity's own sensitive .name aspect instead, so ShredIdentityKey " +
			"destroys it along with the person's email and phone rather than leaving it here in plaintext to " +
			"identify their retained clinical record (retention-class-key-custody-design.md §8.7, fork F3(b)). A " +
			"patient with no identity has no key to shred, so no erasure promise their plaintext name could " +
			"defeat. registeredAt is always present and identifies nobody, which is why the clinicPatients / " +
			"clinicPatientsRead ghost-vertex filters test it rather than the name. Written by CreatePatient " +
			"(whose patient vertexType DDL owns the script), upserted by BackfillPatientRegistration for a " +
			"patient minted before registeredAt became always-present (2026-08-08, 7eb4c72f), rewritten " +
			"WITHOUT fullName by BindPatientIdentity, which moves the name onto the identity it connects, and " +
			"rewritten WITH it again by UnbindPatientIdentity, which moves the name back off a login connected in " +
			"error (both rewrites carry registeredAt forward unchanged — it is the fact of the registration, not " +
			"of the bind — and both are conditioned on the revision the script read the aspect at, so a concurrent " +
			"writer loses rather than being silently overwritten); this aspect-type DDL is the step-6 write gate. " +
			"Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"registeredAt":{"type":"string","description":"When the patient was registered (RFC3339, canonical UTC, = op.submittedAt). Always present; identifies nobody; the lenses' presence filter reads it."},` +
			`"fullName":{"type":"string","description":"The patient's full name — present ONLY for a patient with no linked identity. An identified patient's name lives on that identity's sensitive .name aspect."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"registeredAt": "When the patient was registered (canonical-UTC RFC3339, derived from op.submittedAt). Always written; non-identifying; the presence signal the projection lenses filter on.",
			"fullName":     "The patient's full name, written here ONLY when the patient has no identifiedBy identity. With an identity, the name goes to that identity's sensitive .name aspect so a shred reaches it.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "patient demographics aspect (walk-in, no identity)",
				Payload:         map[string]any{"registeredAt": "2026-08-08T17:04:00Z", "fullName": "Alice Rivera"},
				ExpectedOutcome: "Stored as vtx.patient.<NanoID>.demographics; written by CreatePatient. An identified patient's aspect carries registeredAt alone.",
			},
		},
	}
}

// profileAspectTypeDDL declares the .profile aspect (class providerProfile) — the
// step-6 write gate for CreateProvider + SetProviderProfile. Declaration-only;
// NON-sensitive.
func profileAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     profileAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateProvider", "SetProviderProfile"},
		Description: "Provider profile aspect (clinic). Stored as vtx.provider.<NanoID>.profile (class " +
			"providerProfile) = {fullName, specialty, credentials?, bio?}. Non-sensitive. Written by " +
			"CreateProvider (mints it) and SetProviderProfile (replaces it) — both owned by the provider " +
			"vertexType DDL script; this aspect-type DDL is the step-6 write gate. Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"fullName":{"type":"string"},"specialty":{"type":"string"},"credentials":{"type":"string"},"bio":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"fullName":    "The provider's full name.",
			"specialty":   "The provider's clinical specialty.",
			"credentials": "Post-nominal credentials.",
			"bio":         "Short provider bio.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "provider profile aspect",
				Payload:         map[string]any{"fullName": "Dr. Sam Okafor", "specialty": "Cardiology", "credentials": "MD"},
				ExpectedOutcome: "Stored as vtx.provider.<NanoID>.profile; written by CreateProvider.",
			},
		},
	}
}

// scheduleAspectTypeDDL declares the .schedule aspect (class appointmentSchedule)
// — the step-6 write gate for CreateAppointment. Declaration-only; NON-sensitive.
func scheduleAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     scheduleAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateAppointment", "RescheduleAppointment"},
		Description: "Appointment schedule aspect (clinic). Stored as vtx.appointment.<NanoID>.schedule (class " +
			"appointmentSchedule) = {startsAt, endsAt, remindAt, reason?, selfBooked?}. Non-sensitive. Written by CreateAppointment " +
			"(initial) and RescheduleAppointment (new times; selfBooked carried forward) — whose appointment vertexType DDL owns the script; this " +
			"aspect-type DDL is the step-6 write gate. Declaration-only: no op handler. remindAt = startsAt − 24h is a " +
			"precomputed reminder deadline the " +
			"clinic-reminders package's convergence lens reads (it is not a caller input). CreateAppointment " +
			"conflict-checks the booking by claiming a slot-claim aspect per covered 15-minute cell on the provider and " +
			"patient hubs (double-book rejection) and the provider's opt-in .hours availability windows (OutsideHours " +
			"rejection). selfBooked = true records that the visit was booked on the consumer scope=self path (the " +
			"patient themselves, never the desk) — the fact the patientSelfDayClaim lock and its release are keyed on.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"startsAt":{"type":"string"},"endsAt":{"type":"string"},"remindAt":{"type":"string"},"reason":{"type":"string"},"selfBooked":{"type":"boolean"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"startsAt":   "Appointment start (RFC3339).",
			"endsAt":     "Appointment end (RFC3339).",
			"remindAt":   "Precomputed reminder deadline (RFC3339, canonical UTC) = startsAt − 24h. Derived by CreateAppointment, not a caller input; the clinic-reminders convergence lens projects it as freshUntil to arm the @at reminder timer.",
			"reason":     "Visit reason / chief complaint.",
			"selfBooked": "true when the visit was booked on the consumer scope=self path (the patient's own login, self-scoped); absent on a front-desk / operator booking. Recorded by CreateAppointment, carried unchanged by RescheduleAppointment; it selects whether the visit holds a patientSelfDayClaim for its provider + UTC day.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "appointment schedule aspect",
				Payload:         map[string]any{"startsAt": "2026-07-01T15:00:00Z", "endsAt": "2026-07-01T15:30:00Z", "remindAt": "2026-06-30T15:00:00Z", "reason": "Annual checkup"},
				ExpectedOutcome: "Stored as vtx.appointment.<NanoID>.schedule; written by CreateAppointment (which derives remindAt = startsAt − 24h).",
			},
		},
	}
}

// statusAspectTypeDDL declares the .status aspect (class appointmentStatus) — the
// step-6 write gate for CreateAppointment (initial), SetAppointmentStatus
// (staff transitions), CorrectAppointmentStatus (terminal→terminal repair), and
// MarkPastDueNoShow (the Weaver-dispatched auto no-show). Declaration-only;
// NON-sensitive.
func statusAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     statusAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateAppointment", "SetAppointmentStatus", "CorrectAppointmentStatus", "MarkPastDueNoShow", "RescheduleAppointment"},
		Description: "Appointment status aspect (clinic). Stored as vtx.appointment.<NanoID>.status (class " +
			"appointmentStatus) = {value ∈ scheduled|confirmed|checkedIn|completed|cancelled|noShow, note?, " +
			"noShowFeeCents?, lateCancel?, correctedFrom?}. Non-sensitive. Written by CreateAppointment (initial scheduled), SetAppointmentStatus " +
			"(transitions, with an optional audit note — a cancel / no-show reason, distinct from the .schedule visit " +
			"reason — and a noShowFeeCents amount when transitioning to noShow (caller-supplied or a 2500 default) or " +
			"when a patient cancels their own visit inside the 24-hour late-cancel window (lateCancel: true, the 2500 " +
			"default; a same-value cancelled re-set carries both forward)), CorrectAppointmentStatus (a terminal→terminal " +
			"repair of a wrong final call, which requires the note, additionally records correctedFrom — the terminal " +
			"value it overwrote — and carries noShowFeeCents onto a noShow exactly as the first transition does, none " +
			"onto completed / cancelled), and MarkPastDueNoShow " +
			"(the same noShow transition, Weaver-dispatched once a non-terminal " +
			"appointment's endsAt passes unattended, deliberately fee-less), and RescheduleAppointment (a " +
			"confirmed or checkedIn visit that is moved is reset to {value: scheduled} — the confirmation was for " +
			"the old date, and a recorded arrival must not exempt a moved visit from the past-due sweep; the note " +
			"is dropped with the transition it belonged to) — whose appointment vertexType DDL " +
			"owns every script here; this aspect-type DDL is the step-6 write gate. The fee's PRESENCE on the current " +
			"value is what clinic-ledger's clinicNoShowSettlement bills, whichever writer set it, and its absence on a " +
			"charged appointment is what it reverses. Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"value":{"type":"string","enum":["scheduled","confirmed","checkedIn","completed","cancelled","noShow"]},"note":{"type":"string"},"noShowFeeCents":{"type":"number"},` +
			`"lateCancel":{"type":"boolean"},"correctedFrom":{"type":"string","enum":["cancelled","completed","noShow"]}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"value":          "Appointment status: scheduled | confirmed | checkedIn | completed | cancelled | noShow.",
			"note":           "Audit note recorded with a status transition (e.g. a cancel / no-show reason). Optional on SetAppointmentStatus, required on CorrectAppointmentStatus.",
			"noShowFeeCents": "Optional no-show fee in integer cents, present when value is noShow (staff-set or a correction: caller-supplied positive number, or a 2500 default when omitted) or cancelled by the patient inside the 24-hour late-cancel window (lateCancel: true, the 2500 default). Its presence on the current value is what clinic-ledger bills; its absence on a charged appointment (a correction to completed / cancelled) is what it reverses.",
			"lateCancel":     "true when the patient cancelled their own visit inside the 24-hour late-cancel window (submitted at or after startsAt − 24h) — the cancel carries the no-show fee. Absent otherwise; a same-value cancelled re-set carries it forward.",
			"correctedFrom":  "The terminal status this correction overwrote, present only on a CorrectAppointmentStatus write — the only trace of the wrong call once the upsert lands.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "appointment status aspect",
				Payload:         map[string]any{"value": "confirmed"},
				ExpectedOutcome: "Stored as vtx.appointment.<NanoID>.status; written by CreateAppointment / SetAppointmentStatus, reset to scheduled by RescheduleAppointment when a confirmed / checkedIn visit is moved.",
			},
		},
	}
}

// providerSlotClaimAspectTypeDDL declares the .slot<cellcode> aspect (class
// providerSlotClaim) — a deterministic per-15-minute-cell existence marker on the
// provider hub. The step-6 write gate for CreateAppointment / RescheduleAppointment /
// SetAppointmentStatus / MarkPastDueNoShow / TombstoneAppointment (create / release /
// re-claim). Declaration-only; NON-sensitive.
// One aspect per occupied grid cell, created ON DEMAND — never pre-seeded by
// CreateProvider, so there is no "must exist before declared read" constraint (a
// claim aspect need not pre-exist for any provider). Its data is {} — a pure
// existence marker, no
// relationship field: the key ITSELF (identical across two competing bookings for
// the same cell) is the lock — CreateOnly/expectedRevision at commit is the safety
// property, not the lazy kv.Read that picks the mutation verb (design
// clinic-booking-write-path-slot-claims-design.md §2.1/§2.4).
func providerSlotClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     providerSlotClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "MarkPastDueNoShow", "TombstoneAppointment"},
		Description: "Provider 15-minute slot-claim aspect (clinic). Stored as vtx.provider.<NanoID>.slot<cellcode> " +
			"(class providerSlotClaim) = {} — a pure existence marker, no relationship field. <cellcode> is the cell's " +
			"canonical whole-second UTC start with '-'/':' stripped and lowercased (e.g. 2026-07-03T09:00:00Z → " +
			"slot20260703t090000z). CreateAppointment claims one per covered cell (CreateOnly — the key collision across " +
			"two concurrent bookings for the same cell IS the double-book lock: SlotConflict on commit-time rejection); " +
			"RescheduleAppointment releases cells the move no longer needs and claims the newly-covered ones in the same " +
			"atomic batch; SetAppointmentStatus / MarkPastDueNoShow tombstone all held cells on a terminal transition " +
			"(cancelled/completed/noShow), freeing them; TombstoneAppointment tombstones all held cells on a hard " +
			"delete, regardless of status. Non-sensitive; created on demand, no CreateProvider init needed. " +
			"Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. The claim's job is done by the KEY (hub + deterministic cellcode), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "provider slot-claim aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.provider.<NanoID>.slot<cellcode>; claimed by CreateAppointment/RescheduleAppointment, released by RescheduleAppointment (vacated cells) / SetAppointmentStatus (terminal transition).",
			},
		},
	}
}

// hoursAspectTypeDDL declares the .hours availability aspect (class providerHours)
// — the step-6 write gate for SetProviderHours. Declaration-only (the script lives
// on the provider vertexType DDL). NON-sensitive. The aspect is OPT-IN: a provider
// with no .hours (or windows=[]) is unconstrained, so this is backward-compatible
// with providers created before the capability shipped. CreateAppointment /
// RescheduleAppointment read it on demand (kv.Read, §2.5 — NOT a declared/OCC read:
// hours are config, not a concurrency serialization point) to reject a booking
// outside a provider's windows (OutsideHours). Windows are UTC seconds-of-day so
// the membership test is exact integer arithmetic over time.weekday /
// time.seconds_of_day (no mixed-width "HH:MM" lexical hazard).
func hoursAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     hoursAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"SetProviderHours"},
		Description: "Provider availability-hours aspect (clinic). Stored as vtx.provider.<NanoID>.hours (class " +
			"providerHours) = {windows: [{day (0=Sun..6=Sat), openSec, closeSec}]} where openSec/closeSec are UTC " +
			"seconds-of-day (0..86400) with openSec<closeSec. Non-sensitive. OPT-IN: an absent aspect or windows=[] " +
			"means the provider is unconstrained. Written ONLY by SetProviderHours (whose provider vertexType DDL " +
			"owns the script); this aspect-type DDL is the step-6 write gate. Read on demand by CreateAppointment / " +
			"RescheduleAppointment to enforce the windows (OutsideHours rejection). Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"windows":{"type":"array","items":{"type":"object","properties":{"day":{"type":"integer"},"openSec":{"type":"integer"},"closeSec":{"type":"integer"}}}}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"windows": "Availability windows: a list of {day:0-6 (Sun=0), openSec, closeSec} (UTC seconds-of-day). An appointment is admitted only if its [start,end] falls inside one window on its weekday.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "provider hours aspect",
				Payload:         map[string]any{"windows": []any{map[string]any{"day": 1, "openSec": 32400, "closeSec": 61200}}},
				ExpectedOutcome: "Stored as vtx.provider.<NanoID>.hours; written by SetProviderHours; enforced by CreateAppointment / RescheduleAppointment.",
			},
		},
	}
}

// timeOffAspectTypeDDL declares the .timeOff exceptions aspect (class
// providerTimeOff) — the step-6 write gate for SetProviderTimeOff. Declaration-only
// (the script lives on the provider vertexType DDL). NON-sensitive (operational, not
// PHI). The aspect is OPT-IN: a provider with no .timeOff (or ranges=[]) has no
// blackouts, so this is backward-compatible with providers created before the
// capability shipped. It is the date-specific blackout LAYER on top of the recurring
// weekly .hours: a booking must satisfy BOTH (inside an .hours window AND outside
// every .timeOff range). CreateAppointment / RescheduleAppointment read it on demand
// (kv.Read, §2.5 — NOT a declared/OCC read: time-off is config, not a concurrency
// serialization point) to reject a booking overlapping any blocked range
// (ProviderUnavailable). Ranges are canonical-UTC RFC3339 instants so the half-open
// overlap test is the same lexical-==-chronological compare CreateAppointment uses
// for double-book detection.
func timeOffAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     timeOffAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"SetProviderTimeOff"},
		Description: "Provider time-off exceptions aspect (clinic). Stored as vtx.provider.<NanoID>.timeOff (class " +
			"providerTimeOff) = {ranges: [{from, to, reason?}]} where from/to are canonical-UTC RFC3339 instants with " +
			"from<to. Non-sensitive (operational, not PHI). OPT-IN: an absent aspect or ranges=[] means no blackouts. " +
			"The date-specific blackout LAYER on top of the recurring weekly .hours — a booking must be inside an .hours " +
			"window AND outside every .timeOff range. Written ONLY by SetProviderTimeOff (whose provider vertexType DDL " +
			"owns the script); this aspect-type DDL is the step-6 write gate. Read on demand by CreateAppointment / " +
			"RescheduleAppointment to reject a booking overlapping any range (ProviderUnavailable). Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"ranges":{"type":"array","items":{"type":"object","properties":{"from":{"type":"string"},"to":{"type":"string"},"reason":{"type":"string"}}}}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"ranges": "Time-off blackout ranges: a list of {from, to, reason?} (RFC3339 UTC instants, from<to). A booking whose [start,end) overlaps any range is rejected (ProviderUnavailable).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "provider time-off aspect",
				Payload:         map[string]any{"ranges": []any{map[string]any{"from": "2026-07-06T00:00:00Z", "to": "2026-07-13T00:00:00Z", "reason": "Vacation"}}},
				ExpectedOutcome: "Stored as vtx.provider.<NanoID>.timeOff; written by SetProviderTimeOff; enforced by CreateAppointment / RescheduleAppointment.",
			},
		},
	}
}

// patientSlotClaimAspectTypeDDL declares the .slot<cellcode> aspect on a PATIENT
// (class patientSlotClaim) — the symmetric analog of providerSlotClaimAspectTypeDDL.
// Catches a patient booked with TWO DIFFERENT providers at the same instant, which
// the provider-side claim set alone cannot see (each provider's cells are disjoint
// keys). Declaration-only; NON-sensitive; carries no relationship field.
func patientSlotClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     patientSlotClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "MarkPastDueNoShow", "TombstoneAppointment"},
		Description: "Patient 15-minute slot-claim aspect (clinic). Stored as vtx.patient.<NanoID>.slot<cellcode> " +
			"(class patientSlotClaim) = {} — a pure existence marker, no relationship field. Same cellcode derivation, " +
			"claim/release lifecycle, and CreateOnly-is-the-lock property as providerSlotClaim, contended on the patient " +
			"hub — it catches a patient double-booked across TWO DIFFERENT providers at the same instant, which the " +
			"provider-side claim set alone cannot see (each provider's cells are disjoint keys). Non-sensitive; created on " +
			"demand. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. The claim's job is done by the KEY (hub + deterministic cellcode), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "patient slot-claim aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.patient.<NanoID>.slot<cellcode>; claimed by CreateAppointment/RescheduleAppointment, released by RescheduleAppointment (vacated cells) / SetAppointmentStatus (terminal transition).",
			},
		},
	}
}

// patientSelfDayClaimAspectTypeDDL declares the .selfday<yyyymmdd><providerId>
// aspect on a PATIENT (class patientSelfDayClaim) — the one-open-self-booked-
// visit-per-provider-per-day lock. Same CreateOnly-is-the-lock property as the
// slot claims, keyed on the visit's provider + the UTC calendar day of its
// startsAt rather than on a 15-minute cell. Only a visit booked on the consumer
// scope=self path (.schedule.selfBooked) ever claims one; a front-desk booking
// never does, so the desk is unrestricted. Declaration-only; NON-sensitive;
// carries no relationship field.
func patientSelfDayClaimAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     patientSelfDayClaimAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "MarkPastDueNoShow", "TombstoneAppointment"},
		Description: "Patient self-booking day-claim aspect (clinic). Stored as vtx.patient.<NanoID>.selfday<yyyymmdd><providerId> " +
			"(class patientSelfDayClaim) = {} — a pure existence marker, no relationship field; the KEY is the claim: " +
			"the patient hub + the UTC calendar day of the visit's startsAt (8 digits) + the provider's NanoID. Claimed " +
			"by CreateAppointment on the consumer scope=self path only (the desk's bookings never claim) and by " +
			"RescheduleAppointment when a self-booked visit moves to another day (the old day's claim is released in the " +
			"same batch); a live claim rejects the booking SelfBookingLimit — one open self-booked visit per provider per " +
			"day. Released (unconditioned tombstone) by SetAppointmentStatus's first terminal transition, " +
			"MarkPastDueNoShow, and TombstoneAppointment, alongside the slot-claim cells; a tombstoned claim is OCC-revived " +
			"by the next self booking on that day. Non-sensitive; created on demand. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. The claim's job is done by the KEY (patient hub + UTC day + provider id), never by a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "patient self-booking day-claim aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.patient.<NanoID>.selfday<yyyymmdd><providerId>; claimed by CreateAppointment (self path) / RescheduleAppointment (cross-day move of a self-booked visit), released by RescheduleAppointment (the vacated day) / SetAppointmentStatus (terminal transition) / MarkPastDueNoShow / TombstoneAppointment.",
			},
		},
	}
}

// encounterAspectTypeDDL declares the .encounter aspect (class appointmentEncounter)
// — the raw clinical record, the step-6 write gate for RecordEncounter (whose
// appointment vertexType DDL owns the script). Declaration-only.
//
// SENSITIVE, custodied on the clinicalRecord retention class (RetentionClasses)
// rather than on the patient's own identity: the record's retention obligation
// outlives any one patient's erasure request, so its DEK lives on a holder the
// patient's ShredIdentityKey cannot reach — after that shred the record is still
// readable, pseudonymized, its link to the now-unrecoverable identity intact but
// resolving to nothing (retention-class-key-custody-design.md §6.4). Step 6.5
// encrypts the WHOLE aspect data map, which is why the operational post-visit
// signals (documentedAt / followUpRequested / followUpDate) live on the sibling
// .documentation aspect instead of here — a non-sensitive field sharing this
// aspect's data would be encrypted along with the PHI and unreadable to the
// plain lenses that need it (clinicAppointments et al.). This aspect carries
// ONLY the clinical content, and clinicEncountersRead is its one reader — a Secure
// Lens that decrypts it at projection for the treating provider.
func encounterAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     encounterAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"RecordEncounter"},
		Sensitive:         true,
		Custody:           pkgmgr.CustodySpec{Kind: pkgmgr.CustodyKindRetentionClass, RetentionClass: clinicalRecordRetentionClass},
		Description: "Appointment encounter aspect (clinic). Stored as vtx.appointment.<NanoID>.encounter (class " +
			"appointmentEncounter) = {summary, assessment, plan, superseded} — the raw clinical record, written by RecordEncounter (an unfilled optional is written as the empty string, so the plaintext shape is fixed and clinicEncountersRead's per-field secure columns never see a missing field) " +
			"(whose appointment vertexType DDL owns the script). summary / assessment / plan are the CURRENT text; " +
			"superseded is the record's history — one {summary, assessment, plan, recordedAt} per amendment, in order, " +
			"the text each amendment replaced and the instant that text was recorded ([] on the first record; bounded " +
			"at 40 entries, AmendmentLimit past it). The history is retained in the record, not rendered: " +
			"clinicEncountersRead projects the current text only. SENSITIVE: its DEK is custodied on the clinicalRecord " +
			"retention-class holder (RetentionClasses), not on the patient's identity — the record survives " +
			"ShredIdentityKey on its patient as a pseudonymized retained record, rather than becoming unrecoverable " +
			"alongside the patient's directly-identifying .name/.email/.phone. The operational, non-PHI post-visit " +
			"signals (documentedAt / followUpRequested / followUpDate) live on the sibling .documentation aspect, which " +
			"is what the clinicAppointments/clinicAppointmentsRead/providerAppointmentsRead lenses project. This aspect's " +
			"content reaches a reader only through clinicEncountersRead, the provider-anchored Secure Lens that decrypts " +
			"it at projection. Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"summary":{"type":"string"},"assessment":{"type":"string"},"plan":{"type":"string"},` +
			`"superseded":{"type":"array","items":{"type":"object","properties":{"summary":{"type":"string"},"assessment":{"type":"string"},"plan":{"type":"string"},"recordedAt":{"type":"string"}}}}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"summary":    "Current visit summary / clinical note (RAW PHI — decrypted at projection into clinicEncountersRead for the treating provider).",
			"assessment": "Current clinical assessment / diagnosis (RAW PHI — decrypted at projection into clinicEncountersRead for the treating provider). Written as \"\" when unfilled.",
			"plan":       "Current treatment plan / orders (RAW PHI — decrypted at projection into clinicEncountersRead for the treating provider). Written as \"\" when unfilled.",
			"superseded": "The record's history, oldest first: one {summary, assessment, plan, recordedAt} per amendment — the text that amendment replaced, and recordedAt the instant that text was recorded (the prior amendedAt, or documentedAt for the first version). [] on the first record; at most 40 entries (AmendmentLimit). RAW PHI, encrypted with the rest of the aspect; never projected.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "appointment encounter aspect — first record",
				Payload:         map[string]any{"summary": "Annual checkup, vitals normal.", "assessment": "Essential hypertension, well-controlled.", "plan": "", "superseded": []any{}},
				ExpectedOutcome: "Stored ENCRYPTED as vtx.appointment.<NanoID>.encounter, written by RecordEncounter, DEK custodied on the clinicalRecord retention-class holder. Rendered back to the treating provider through clinicEncountersRead, which decrypts it at projection.",
			},
			{
				Name: "appointment encounter aspect — amended once",
				Payload: map[string]any{"summary": "Annual checkup, vitals normal; BP re-taken 128/82.", "assessment": "Essential hypertension, well-controlled.", "plan": "",
					"superseded": []any{map[string]any{"summary": "Annual checkup, vitals normal.", "assessment": "Essential hypertension, well-controlled.", "plan": "", "recordedAt": "2026-07-01T15:30:00Z"}}},
				ExpectedOutcome: "The current text after one amendment; the first version sits under superseded with the instant it was recorded (the sibling .documentation's documentedAt). The sibling .documentation carries amendedAt for this write.",
			},
		},
	}
}

// documentationAspectTypeDDL declares the .documentation aspect (class
// appointmentDocumentation) — the operational, non-PHI half of the post-visit
// record, the step-6 write gate for RecordEncounter (whose appointment
// vertexType DDL owns the script, alongside the sensitive .encounter aspect).
// Declaration-only.
//
// NON-sensitive, no custody: this aspect exists because step 6.5 encrypts an
// entire aspect's data map, so a non-sensitive field sharing the sensitive
// .encounter aspect's data would be encrypted along with the clinical content
// and unreadable to every plain lens — the aspect-level sensitivity boundary
// forces the split. clinicAppointments, clinicAppointmentsRead,
// providerAppointmentsRead (clinic-domain) and followUpReminders
// (clinic-reminders) all read this aspect for the presence-of-documentation and
// follow-up-scheduling signals.
func documentationAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     documentationAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"RecordEncounter"},
		Description: "Appointment documentation aspect (clinic). Stored as vtx.appointment.<NanoID>.documentation " +
			"(class appointmentDocumentation) = {documentedAt, amendedAt?, followUpRequested, followUpDate?} — the OPERATIONAL, " +
			"non-PHI half of the post-visit record, written by RecordEncounter (whose appointment vertexType DDL owns " +
			"the script) alongside the sensitive .encounter aspect. It is a SEPARATE aspect from .encounter because " +
			"step 6.5 encrypts an entire aspect's data map: a non-sensitive field sharing the sensitive aspect's data " +
			"would be encrypted along with the clinical content and unreadable to every plain lens, so the " +
			"aspect-level sensitivity boundary forces this split. Consumed by clinicAppointments, " +
			"clinicAppointmentsRead, and providerAppointmentsRead (clinic-domain's own lenses) and by " +
			"clinic-reminders' followUpReminders. documentedAt is when the visit was FIRST documented and is " +
			"preserved across amendments; amendedAt is when the current TEXT was recorded, present once the text " +
			"has been amended (a follow-up-only change carries it). Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"documentedAt":{"type":"string"},"amendedAt":{"type":"string"},"followUpRequested":{"type":"boolean"},"followUpDate":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"documentedAt":      "When the visit was FIRST documented (RFC3339, = the first RecordEncounter's op.submittedAt; preserved by every amendment). Operational — projected. A non-null documentedAt IS the \"visit documented\" presence signal.",
			"amendedAt":         "When the current TEXT was recorded (RFC3339, = the text-amending RecordEncounter's op.submittedAt; a change to the follow-up signals alone carries it unchanged). Absent until the text is first amended. Operational — projected.",
			"followUpRequested": "Whether a follow-up is needed. Operational — projected.",
			"followUpDate":      "Suggested follow-up date (RFC3339 / date). Operational — projected when followUpRequested.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "appointment documentation aspect — first record",
				Payload:         map[string]any{"documentedAt": "2026-07-01T15:30:00Z", "followUpRequested": false},
				ExpectedOutcome: "Stored as vtx.appointment.<NanoID>.documentation; written by RecordEncounter. Read by clinicAppointments and followUpReminders.",
			},
			{
				Name:            "appointment documentation aspect — amended",
				Payload:         map[string]any{"documentedAt": "2026-07-01T15:30:00Z", "amendedAt": "2026-07-02T09:10:00Z", "followUpRequested": false},
				ExpectedOutcome: "The same record after an amendment: documentedAt unchanged (first documented), amendedAt the amending op's submittedAt. The sibling .encounter's superseded list holds the replaced text.",
			},
		},
	}
}

// siteAssignmentAspectTypeDDL declares the .siteAssignment aspect (class
// appointmentSiteAssignment) — a pure existence marker, no relationship field
// (mirrors providerSlotClaim's own doc comment), written ONLY by
// SetAppointmentSite alongside its atSite link. Unlike CreateAppointment's and
// BackfillAppointmentSite's own site-writing branches — each naturally
// serialized (CreateAppointment's site arrives with the vertex itself, before
// any caller could name the not-yet-existing appointmentKey; BackfillAppointmentSite
// dispatches once per Weaver gap row) — SetAppointmentSite's site is
// caller-CHOSEN, so two concurrent callers picking DIFFERENT sites for the
// same still-site-less appointment would both pass the "no live atSite link
// yet" read and both commit a DIFFERENT, non-colliding atSite link key
// (lnk.appointment.<a>.atSite.building.<site>) — the target segment differs,
// so CreateOnly on the link key alone cannot be the lock. This aspect's key
// is target-INDEPENDENT (fixed per appointment), so CreateOnly on IT is: the
// loser's whole batch (link + this aspect, one atomic commit) rejects at
// commit, exactly the claim_cell double-book lock applied to a
// single-winner-not-a-cell resource. Declaration-only; NON-sensitive.
func siteAssignmentAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     siteAssignmentAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"SetAppointmentSite"},
		Description: "Appointment site-assignment guard aspect (clinic). Stored as " +
			"vtx.appointment.<NanoID>.siteAssignment (class appointmentSiteAssignment) = {} — a pure existence " +
			"marker, no relationship field (the atSite LINK is the actual site relationship; this aspect exists " +
			"only to serialize concurrent writers). Written by SetAppointmentSite (CreateOnly) in the SAME atomic " +
			"batch as the atSite link it accompanies: two concurrent SetAppointmentSite calls choosing DIFFERENT " +
			"sites for the same still-site-less appointment both see no live atSite link and both attempt to " +
			"commit, but this aspect's key is the SAME for both (unlike the atSite link's, which varies by chosen " +
			"site) — CreateOnly commits it exactly once, and the loser's whole batch (including its atSite link) " +
			"rejects. Never tombstoned or rewritten once created. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"data": "Always {} — a pure existence marker. The lock is the KEY (fixed per appointment), never a field in data.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "appointment site-assignment guard aspect",
				Payload:         map[string]any{},
				ExpectedOutcome: "Stored as vtx.appointment.<NanoID>.siteAssignment; written once by SetAppointmentSite, alongside the atSite link, to serialize concurrent callers.",
			},
		},
	}
}

// patientDDLScript handles CreatePatient + TombstonePatient +
// BackfillPatientRegistration + BindPatientIdentity + UnbindPatientIdentity.
// Known-key reads only, plus CreatePatient's one bounded worksAt enumeration.
// CreatePatient mints the patient vertex + the .demographics aspect atomically
// (CreateOnly, so a crash-retry with the same patientId collapses on the Contract
// #4 tracker). Root data stays {} (D5).
//
// BindPatientIdentity wires the identifiedBy link CreatePatient's identityKey
// branch would have wired, claims the same two exclusivity guards, and moves the
// name from .demographics onto the identity's sensitive .name aspect — one batch,
// so no commit ever has the patient identified while the plaintext copy survives.
// UnbindPatientIdentity is its inverse, operator-only: it releases the link and
// both guards and moves the name back, restoring the pre-bind shape exactly, so a
// chart connected to the wrong login is repairable rather than permanent.
//
// Both binds take an identity only while it is UNCLAIMED — nobody has signed in
// with it yet. That is the whole confinement on the standing front-desk grant: a
// claimed login belongs to a person, and connecting a stranger's chart to it (or
// taking one away from it) would hand its holder that chart's protected rows
// through patientIdentityReadGrants. It mirrors identity-domain's own standing
// front/back-of-house writes, which are confined the same way and for the same
// reason — the domain-shaped boundary is the state machine.
const patientDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert(vtx_key, local_name, cls, data):
    # Unconditioned update: create-if-absent / overwrite-if-present. Used for
    # the patient's name on their identity, which CreateUnclaimedIdentity has
    # usually already written with the same value.
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_update_occ(vtx_key, local_name, cls, data, expected_revision):
    # An update PINNED to the revision the script read the aspect at. A batch is
    # atomic within itself, which says nothing about a DIFFERENT op writing the
    # same key between this script's hydration and its commit -- the .demographics
    # rewrites read the aspect's CONTENT to decide what to write, so an
    # unconditioned update would silently swallow whatever landed in that window
    # (a BackfillPatientRegistration's registeredAt, say). The revision comes from
    # the DECLARED read: a caller that omits the declaration gets no hydrated
    # document and the branch faults before it can write.
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data},
            "expectedRevision": expected_revision}

def make_aspect_revive_occ(vtx_key, local_name, cls, data, expected_revision):
    # Re-claim of a TOMBSTONED aspect: an update keyed on the tombstone's own
    # revision, never a create. A Lattice tombstone is soft -- the key still
    # occupies its subject -- so make_aspect's create asserts revision 0 and
    # conflicts forever against any key that has EVER been minted. The guard
    # aspects UnbindPatientIdentity releases must be re-claimable, or a repaired
    # patient could never be connected again. Mirrors revive_link's OCC shape.
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data},
            "expectedRevision": expected_revision}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def make_tombstone(key):
    return {"op": "tombstone", "key": key}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def bare_nanoid_or_mint(p, name):
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_parts_or_none(key):
    # parts_of's non-failing twin, for a key whose SHAPE is not the caller's
    # promise. op.actor is a platform-owned value, not payload, and it is not
    # always a person: a service or console actor can carry a key of another
    # type entirely, and kv.Links REJECTS a hub that is not vtx.<type>.<id>.
    # Refusing a registration over the shape of who submitted it would break
    # every such dispatch path, so an unparseable key simply enumerates nothing.
    # It also screens each ENUMERATED target before that target becomes a
    # segment of a link key this op writes.
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx" or parts[1] == "" or parts[2] == "":
        return None, None
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None:
        return None
    if not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_typed(state, key, name, want_class):
    # Endpoint validation: the linked vertex MUST be alive AND the expected
    # class. A dead or wrong-class identityKey is never wired.
    if not vertex_alive(state, key):
        fail("UnknownEndpoint: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != want_class:
        fail("WrongClass: " + name + ": " + key + " has class " + str(cls) + ", required " + want_class)

def read_identity_state(state, identity_key):
    # identity-domain's own .state aspect {value: unclaimed|claimed|merged}, read
    # from the hydrated snapshot. Absent reads as None, which every caller treats
    # as "not unclaimed" -- an undeclared read is indistinguishable from a missing
    # aspect, so the only safe reading of silence is refusal.
    aspect_key = identity_key + ".state"
    if aspect_key not in state:
        return None
    doc = state[aspect_key]
    if doc == None or (hasattr(doc, "isDeleted") and doc.isDeleted):
        return None
    if doc.data == None:
        return None
    return doc.data.get("value")

def require_unclaimed_identity(state, identity_key, code):
    # The confinement on the patient↔login binds. Front-of-house holds these
    # grants STANDING and scope=any, with no workplace to confine against (a
    # patient is practice-wide), so without this any desk actor could point a
    # patient's chart at a login THEY hold and read that patient's protected rows
    # through patientIdentityReadGrants + RLS. Restricting the target to an
    # identity in state=unclaimed is the domain-shaped boundary -- the same one
    # identity-domain draws around its own standing front/back-of-house write
    # (RecordIdentityPII): a login nobody has claimed yet is by construction one
    # the walk-in ceremony just minted, and the moment a person signs in with it
    # the desk's reach over it ends. It also closes the merge case for free: a
    # MergeIdentity loser is state=merged, alive, and its actor is redirected to
    # the winner, so binding one would attach a chart to a login the winner reads.
    #
    # The desk still holds the claim secret it minted a moment earlier, which is
    # the residual this does NOT close -- see permissions.go's note.
    #
    # read-posture: (a) declared in contextHint.reads by both binds' dispatchers.
    # A correctness read, not a probe: an absent declaration hydrates nothing,
    # this reads None, and the op is refused rather than proceeding unguarded.
    identity_state = read_identity_state(state, identity_key)
    if identity_state != "unclaimed":
        fail(code + ": " + identity_key + " is " + str(identity_state) +
             "; only a freshly minted, unclaimed identity can be connected to a patient")

def claim_identity(identity_key):
    # Global exclusivity guard (Capability-KV §06 — pure existence-uniqueness, no
    # list needed): at most one patient may claim a given identity at a time.
    # kv.Read here is LAZY (§2.5 idiom, same as the appointment DDL's claim_cell)
    # for the LIVE case — it only picks the error message; the safety property is
    # the atomic batch's create-only conditioning at commit: two DIFFERENT patients
    # passing the same identityKey both read it absent and both emit op:create for
    # the IDENTICAL key, but a create on a key at revision 0 commits exactly once —
    # the loser's whole batch RevisionConflicts (fail closed, never a silent
    # double-claim).
    #
    # The read is load-bearing for the TOMBSTONED case: UnbindPatientIdentity
    # releases this claim, and a create can never land on a key that has been
    # minted before, so a released identity is re-claimed by an OCC-conditioned
    # revive keyed on the tombstone's own revision.
    # read-posture: (d) declared in contextHint.optionalReads by CreatePatient's
    # and BindPatientIdentity's dispatchers (cmd/clinic-app/web/app.js)
    existing = kv.Read(identity_key + ".patientClaim")
    if existing != None and not existing.isDeleted:
        fail("IdentityAlreadyClaimed: " + identity_key + " is already linked to another patient")
    if existing != None:
        return make_aspect_revive_occ(identity_key, "patientClaim", "identityPatientClaim", {}, existing.revision)
    return make_aspect(identity_key, "patientClaim", "identityPatientClaim", {})

def make_patient_identity_claim(pkey):
    # Entity-keyed guard, the mirror of claim_identity above: at most one identity
    # may bind THIS patient at a time -- the provider side's
    # claim_provider_identity idiom, keyed on the PATIENT. The identifiedBy link
    # key is (patient, identity)-composite and therefore cannot stop a second
    # identity being wired to a patient that already has one; this key, which
    # names only the patient, is what collides.
    #
    # The read-free create form, for CreatePatient only: it mints the patient in
    # this same batch (op:create on the root, which a pre-existing patient key
    # would itself collide on), so the aspect provably has no prior value -- not
    # even a tombstone -- and there is nothing to read. A create regardless of
    # racing: on a key at revision 0 a create commits exactly once, so a racing
    # second claimant's whole batch RevisionConflicts (fail closed).
    return make_aspect(pkey, "identityClaim", "patientIdentityClaim", {})

def claim_patient_identity(pkey):
    # The read-checking form, for a bind onto a patient that ALREADY EXISTS, whose
    # claim may be live (refuse), tombstoned by a prior UnbindPatientIdentity
    # (revive, so a repaired chart can be connected to the right login), or absent
    # (create).
    # ABSENT is the common first-bind case -- and, for one population, it is NOT
    # the same as "not yet identified". A patient CreatePatient identified before
    # this marker existed carries none and never will (no backfill), so it reaches
    # this branch and is refused further down instead, by NothingToBind: that
    # branch already moved its name onto the identity, leaving .demographics with
    # nothing for a bind to give a second login. Both populations are refused; only
    # the marker-carrying one is refused here, before the name is read.
    #
    # read-posture: (d) declared in contextHint.optionalReads by
    # BindPatientIdentity's dispatcher (cmd/clinic-app/web/app.js's
    # Connect-a-login ceremony).
    existing = kv.Read(pkey + ".identityClaim")
    if existing != None and not existing.isDeleted:
        fail("PatientAlreadyIdentified: " + pkey + " is already linked to an identity")
    if existing != None:
        return make_aspect_revive_occ(pkey, "identityClaim", "patientIdentityClaim", {}, existing.revision)
    return make_patient_identity_claim(pkey)

REGISTRATION_SITE_PAGE_LIMIT = 20

def registration_site_mutations(pid, pkey, actor_key):
    # The buildings the registering staffer worksAt AT THIS INSTANT, recorded on
    # the patient as its own links -- a time fact of the registration, not a
    # pointer to be re-walked. clinicPatientsRead reads these links directly, so
    # a staffer who later transfers buildings carries no patient with them: the
    # old desk keeps every patient it registered and the new desk inherits none.
    # A lens that walked the registrar's CURRENT worksAt instead would re-anchor
    # the whole history on every transfer, in both directions.
    #
    # The patient (later-arriving) is the source, the pre-existing location the
    # target (Contract #1 §1.1). Sentence: "patient registeredAtSite building".
    # The target's TYPE segment comes from the enumerated link, not a literal:
    # worksAt is location-domain's edge and its target is whatever location type
    # that domain wired, while the roster's arm filters on :building.
    #
    # ONE bounded enumeration and no follow-up read per target: the lens revalidates
    # every recorded site at READ time (Contract #1 filters a tombstoned vertex out
    # of every walk), so proving the building alive here would only duplicate a
    # check that must happen at read time anyway. A worksAt link tombstoned by
    # UnwireWorksAt is skipped -- kv.Links returns it with isDeleted set rather
    # than omitting it.
    #
    # Truncation at page 1 records FEWER sites, never more, and fewer anchors is
    # a narrower read: no cursor loop, on a relation whose degree is the handful
    # of buildings one person works at.
    _, actor_id = vertex_parts_or_none(actor_key)
    if actor_id == None:
        return []
    # read-posture: (e) relation=worksAt epoch=none -- a single bounded enumeration
    # off the platform-supplied actor key, never a keyspace scan. A workplace wired
    # concurrently with this op is simply not part of the registration this op is
    # recording; the fact is whatever held when the patient was typed in.
    wpage, _ = kv.Links(actor_key, "worksAt", "out", None, REGISTRATION_SITE_PAGE_LIMIT)
    site_mutations = []
    for lk in wpage:
        if lk.isDeleted:
            continue
        site_type, site_id = vertex_parts_or_none(lk.targetVertex)
        if site_id == None:
            continue
        # worksAt's own key is (identity, location)-deterministic, so this
        # enumeration names each target at most once and the derived link key is
        # unique within the batch.
        site_lnk = "lnk.patient." + pid + ".registeredAtSite." + site_type + "." + site_id
        site_mutations.append(make_link(site_lnk, pkey, lk.targetVertex, "registeredAtSite", "registeredAtSite", {}))
    return site_mutations

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreatePatient":
        full_name = required_string(p, "fullName")
        pid = bare_nanoid_or_mint(p, "patientId")
        pkey = "vtx.patient." + pid
        # registeredAt is the aspect's ALWAYS-present, non-identifying field, and
        # it is what the lenses' ghost-vertex filters test. They used to test
        # fullName, which stops being present on an identified patient below — a
        # filter keyed on a field that legitimately moves away would silently
        # drop every identified patient out of the roster.
        demo = {"registeredAt": time.rfc3339_utc(op.submittedAt)}
        mutations = [make_vtx(pkey, "patient", {})]
        # Optional identifiedBy link to a pre-minted identity carrying the
        # patient's sensitive contact (Vault plane). The patient (later-
        # arriving) is the source, the pre-existing identity is the target
        # (Contract #1 §1.1). Sentence: "patient identifiedBy identity".
        identity_key = optional_string(p, "identityKey")
        if identity_key != None:
            _, identity_id = parts_of(identity_key, "identityKey", "identity")
            require_live_typed(state, identity_key, "identityKey", "identity")
            identified_by_lnk = "lnk.patient." + pid + ".identifiedBy.identity." + identity_id
            mutations.append(make_link(identified_by_lnk, pkey, identity_key, "identifiedBy", "identifiedBy", {}))
            # Mutual exclusivity, both sides: at most one patient may ever wire
            # the SAME identity (else two roster rows would decrypt/display the
            # same person's contact), and at most one identity may ever bind THIS
            # patient -- which is also what makes a later BindPatientIdentity
            # refuse a patient that was registered with a login already.
            mutations.append(claim_identity(identity_key))
            mutations.append(make_patient_identity_claim(pkey))
            # The NAME goes on the person, not on the clinic's record of them
            # (retention-class-key-custody-design.md §8.7, fork F3(b)). A name
            # left plaintext on .demographics outlives the ShredIdentityKey that
            # destroys the same person's email and phone, so a shredded patient's
            # retained clinical record stays identified — exactly the duplication
            # the clinicalRecord class's obligation forbids. identity-domain's
            # name aspect DDL carries an intentionally EMPTY permittedCommands
            # ("any identity-anchored writer is allowed") and stores {value},
            # which is the shape written here. An UPSERT, not a create: the FE
            # mints the identity with this same name a moment earlier, so the
            # aspect normally already exists and this rewrites it identically.
            mutations.append(make_aspect_upsert(identity_key, "name", "name", {"value": full_name}))
        else:
            # No identity means no key to shred, and so no erasure whose promise
            # a plaintext name could defeat: an unidentified patient is outside
            # the erasure plane rather than a hole in it. The name stays here,
            # where the front desk can still find a walk-in nobody holds contact
            # details for.
            demo["fullName"] = full_name
        mutations.append(make_aspect(pkey, "demographics", "patientDemographics", demo))
        # WHERE this registration happened, recorded on the patient at write
        # time. clinicPatientsRead anchors the roster row on these buildings, so
        # the desk that typed the patient in reads them immediately instead of
        # waiting for someone to book a first appointment (a patient with no
        # appointment otherwise carries its own self-anchor alone, which no staff
        # grant matches).
        mutations = mutations + registration_site_mutations(pid, pkey, op.actor)
        events = [{"class": "clinic.patientCreated", "data": {"patientKey": pkey}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": pkey}}

    if ot == "TombstonePatient":
        pkey = required_string(p, "patientKey")
        parts_of(pkey, "patientKey", "patient")
        if not vertex_alive(state, pkey):
            fail("UnknownPatient: " + pkey)
        mutations = [make_tombstone(pkey)]
        events = [{"class": "clinic.patientTombstoned", "data": {"patientKey": pkey}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": pkey}}

    if ot == "BackfillPatientRegistration":
        # A one-time historical-data repair, operator-only and manual — unlike
        # BackfillAppointmentSite's Weaver-auto-remediation twin, this gap
        # cannot recur: registeredAt became an ALWAYS-present field on
        # .demographics only as of the 2026-08-08 CreatePatient change
        # (7eb4c72f); every patient minted since already carries it. A
        # patient minted BEFORE that change carries no registeredAt and is
        # silently excluded from clinicPatientsRead / clinicPatientReadGrants'
        # presence filter forever — roster-empty for every actor, including
        # the WildcardAnchor holder, not only a workplace-anchored one
        # (verticals.md "worksAt front-desk staffer's patient-context
        # switcher is empty"). A standing auto-remediation loop would be the
        # wrong shape for a defect that only ever affects pre-existing rows.
        pkey = required_string(p, "patient")
        parts_of(pkey, "patient", "patient")
        if not vertex_alive(state, pkey):
            fail("UnknownPatient: " + pkey)
        # Operator-only is enforced at the grant plane (GrantsTo: ["operator"]
        # in permissions.go), mirroring BindProviderIdentity — this script has
        # no in-script role-walk helper of its own to duplicate.
        # read-posture: (a) declared in contextHint.reads by the operator
        # tool's own dispatcher — this is the read that decides whether the
        # op is a no-op, never just an error-message pick.
        demo_key = pkey + ".demographics"
        if demo_key not in state:
            fail("MissingDemographics: " + pkey + " has no .demographics aspect to backfill")
        demo = state[demo_key]
        if demo == None or (hasattr(demo, "isDeleted") and demo.isDeleted):
            fail("MissingDemographics: " + pkey + " has no .demographics aspect to backfill")
        if demo.data.get("registeredAt") != None:
            # Already backfilled (or never needed it) — no-op cleanly rather
            # than reject, mirroring BackfillAppointmentSite's own
            # already-present no-op. No primaryKey: an empty write footprint
            # has nothing for the reply-constraint to validate it against.
            return {"mutations": [], "events": [], "response": {}}
        # Rebuilt from the two fields .demographics can ever carry (mirrors
        # CreatePatient's own literal construction above) rather than copying
        # demo.data wholesale — safer than relying on a dict-copy builtin this
        # engine's own precedent never exercises.
        merged = {"registeredAt": time.rfc3339_utc(op.submittedAt)}
        existing_full_name = demo.data.get("fullName")
        if existing_full_name != None:
            merged["fullName"] = existing_full_name
        mutations = [make_aspect_upsert(pkey, "demographics", "patientDemographics", merged)]
        events = [{"class": "clinic.patientRegistrationBackfilled", "data": {"patientKey": pkey}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": pkey}}

    if ot == "BindPatientIdentity":
        pkey = required_string(p, "patientKey")
        _, pid = parts_of(pkey, "patientKey", "patient")
        require_live_typed(state, pkey, "patientKey", "patient")

        identity_key = required_string(p, "identityKey")
        _, identity_id = parts_of(identity_key, "identityKey", "identity")
        require_live_typed(state, identity_key, "identityKey", "identity")
        require_unclaimed_identity(state, identity_key, "IdentityNotUnclaimed")

        # patient identifiedBy identity (Contract #1 §1.1: the later-arriving
        # patient is the source, the pre-existing identity is the target).
        # Sentence: "patient identifiedBy identity". The SAME key CreatePatient's
        # identityKey branch mints, so a bound patient is indistinguishable from
        # one registered with a login -- every reader (clinicPatientsRead's
        # identifiedBy walk, patientIdentityReadGrants, the appointment DDL's
        # self-scope check) sees one shape, not two.
        identified_by_lnk = "lnk.patient." + pid + ".identifiedBy.identity." + identity_id
        mutations = [make_link(identified_by_lnk, pkey, identity_key, "identifiedBy", "identifiedBy", {})]

        # Mutual exclusivity, both sides: at most one patient ever claims THIS
        # identity, and at most one identity ever binds THIS patient. The second
        # of the pair is what refuses a patient that already has a login.
        mutations.append(claim_identity(identity_key))
        mutations.append(claim_patient_identity(pkey))

        # MOVE the name onto the person. A patient registered with a name alone
        # carries it plaintext on .demographics, outside the Vault plane: the
        # ShredIdentityKey that destroys this person's email and phone would never
        # reach it, so their retained clinical record would stay identified
        # (retention-class-key-custody-design.md §8.7, fork F3(b)). Connecting the
        # login is the moment that gap closes, so the name moves in the same batch
        # as the link -- there is never a commit where the patient is identified
        # AND still carries the plaintext copy.
        #
        # read-posture: (a) declared in contextHint.reads by this op's dispatcher
        # (cmd/clinic-app/web/app.js's Connect-a-login ceremony). This read decides
        # WHAT is written, and the rewrite is conditioned on the revision it was
        # read at, so declaring it is what makes a concurrent .demographics write
        # lose rather than be silently overwritten.
        demo_key = pkey + ".demographics"
        if demo_key not in state:
            fail("MissingDemographics: " + pkey + " has no .demographics aspect to read the name from")
        demo = state[demo_key]
        if demo == None or (hasattr(demo, "isDeleted") and demo.isDeleted):
            fail("MissingDemographics: " + pkey + " has no .demographics aspect to read the name from")
        full_name = demo.data.get("fullName")
        if full_name == None or type(full_name) != type("") or len(full_name.strip()) == 0:
            fail("NothingToBind: " + pkey + " carries no .demographics fullName to move onto the identity")
        full_name = full_name.strip()
        registered_at = demo.data.get("registeredAt")
        if registered_at == None:
            # Rebuilding without it would leave {} -- and every roster lens filters
            # on registeredAt being present, so the patient would vanish from the
            # read model for good. BackfillPatientRegistration is the repair; run
            # it first, then bind.
            fail("MissingRegistration: " + pkey + " has no .demographics registeredAt; run BackfillPatientRegistration first")

        # Rebuilt from the two fields .demographics can ever carry (the same
        # literal construction CreatePatient and BackfillPatientRegistration use),
        # minus fullName. registeredAt is carried FORWARD, never restamped: it is
        # the time fact of the registration, not of this bind. Pinned to the
        # revision the read above saw, so a BackfillPatientRegistration landing in
        # the hydration window conflicts instead of being overwritten.
        mutations.append(make_aspect_update_occ(pkey, "demographics", "patientDemographics",
                                                {"registeredAt": registered_at}, demo.revision))
        # The name's new home, written exactly as CreatePatient's identity branch
        # writes it: identity-domain's name aspect is SENSITIVE and stores
        # {value}, so this commits as a ciphertext envelope under the identity's
        # own DEK. An UPSERT, not a create -- the FE mints the identity carrying
        # this same name a moment earlier, so the aspect normally already exists.
        # It is deliberately NOT declared as a read: a declared sensitive read is
        # decrypted before the script runs, and the write needs no prior value.
        mutations.append(make_aspect_upsert(identity_key, "name", "name", {"value": full_name}))

        events = [{"class": "clinic.patientIdentityBound",
                   "data": {"patientKey": pkey, "identityKey": identity_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": identified_by_lnk}}

    if ot == "UnbindPatientIdentity":
        # The repair for a chart connected to the WRONG login. BindPatientIdentity
        # is otherwise a one-way door: it moves the name out of the clinic's own
        # aspect and stamps two guards that refuse every subsequent bind, so a desk
        # that picked the wrong patient row left that patient permanently attached
        # to a stranger's login, with no verb anywhere in the platform to undo it.
        # Operator-only (permissions.go): it is a repair, not desk work.
        pkey = required_string(p, "patientKey")
        _, pid = parts_of(pkey, "patientKey", "patient")
        require_live_typed(state, pkey, "patientKey", "patient")

        identity_key = required_string(p, "identityKey")
        _, identity_id = parts_of(identity_key, "identityKey", "identity")
        require_live_typed(state, identity_key, "identityKey", "identity")

        # The pair must ACTUALLY be bound. The op names both endpoints, so nothing
        # but this read establishes that the identity being released is the one the
        # patient is attached to -- without it a caller could tombstone the guards
        # of an unrelated pairing and free an identity somebody else holds.
        #
        # read-posture: (d) declared in contextHint.optionalReads by this op's
        # dispatcher, deliberately rather than (a): a never-minted link declared
        # REQUIRED faults at hydration with an opaque miss, while an optional
        # declaration makes absence a script-visible branch and lets the operator
        # read the actual answer -- this pair is not bound. The safety direction is
        # unchanged either way, because absence REFUSES: a caller that declares
        # nothing reads nothing here and gets NotBound, never a silent unbind.
        # Tombstoned reaches the same refusal (a pair already unbound once).
        identified_by_lnk = "lnk.patient." + pid + ".identifiedBy.identity." + identity_id
        if not vertex_alive(state, identified_by_lnk):
            fail("NotBound: " + pkey + " is not linked to " + identity_key)

        # An identity someone has already SIGNED IN to is not repairable this way.
        # Unbinding it would tombstone the .patientClaim and move the name off the
        # login its holder authenticates with, leaving a live person's account
        # nameless and re-claimable by the next bind. The wrong-login mistake this
        # op repairs is caught at the desk, long before the person claims anything.
        # A claimed login the WRONG person holds is identity-domain's
        # RevokeIdentityClaim to repair: operator-only, it cuts off every
        # credential bound to the identity, returns it to unclaimed, and arms a
        # fresh secret -- the chart stays bound to it throughout, so the real
        # patient ends up claiming the very login this op left in place.
        require_unclaimed_identity(state, identity_key, "IdentityClaimed")

        # The name comes BACK. .name is SENSITIVE, so a declared read is decrypted
        # at hydration under the identity's own DEK -- which succeeds precisely
        # because the guard above proved the identity alive and unclaimed. (A
        # shredded key would fault the hydration and reject the op: fail closed,
        # and the right answer, since there would be no name left to give back.)
        # read-posture: (a) declared in contextHint.reads by this op's dispatcher.
        name_key = identity_key + ".name"
        if not vertex_alive(state, name_key):
            fail("MissingIdentityName: " + identity_key + " carries no .name to move back onto " + pkey)
        name_doc = state[name_key]
        full_name = name_doc.data.get("value") if name_doc.data != None else None
        if full_name == None or type(full_name) != type("") or len(full_name.strip()) == 0:
            fail("MissingIdentityName: " + identity_key + " carries no .name to move back onto " + pkey)
        full_name = full_name.strip()

        # registeredAt still comes off .demographics -- the bind carried it forward
        # rather than dropping it, so the unbind can rebuild the exact pre-bind
        # shape {registeredAt, fullName} instead of inventing a registration time.
        # read-posture: (a) declared in contextHint.reads by this op's dispatcher.
        demo_key = pkey + ".demographics"
        if not vertex_alive(state, demo_key):
            fail("MissingDemographics: " + pkey + " has no .demographics aspect to move the name back onto")
        demo = state[demo_key]
        registered_at = demo.data.get("registeredAt") if demo.data != None else None
        if registered_at == None:
            fail("MissingRegistration: " + pkey + " has no .demographics registeredAt; run BackfillPatientRegistration first")

        mutations = [
            make_tombstone(identified_by_lnk),
            make_aspect_update_occ(pkey, "demographics", "patientDemographics",
                                   {"registeredAt": registered_at, "fullName": full_name}, demo.revision),
        ]

        # Release both guards, so the patient can be connected to the RIGHT login
        # and the identity can be claimed by the patient it actually belongs to.
        # Each is tombstoned only where one is live: the identityClaim is absent on
        # a patient CreatePatient identified before that marker existed, and
        # tombstoning a key that carries no live document is not a repair, it is a
        # write against a document that is not there.
        # read-posture: (d) declared in contextHint.optionalReads by this op's
        # dispatcher -- a pre-marker patient legitimately has no identityClaim.
        if vertex_alive(state, identity_key + ".patientClaim"):
            mutations.append(make_tombstone(identity_key + ".patientClaim"))
        if vertex_alive(state, pkey + ".identityClaim"):
            mutations.append(make_tombstone(pkey + ".identityClaim"))

        # The identity keeps its own .name: it is the login's own display name,
        # written by CreateUnclaimedIdentity before this clinic ever touched it,
        # and blanking it would damage a vertex this op is only detaching from.
        # The patient's plaintext copy is restored above, which is what the roster
        # reads; the identity's copy stays inside the shred's reach either way.
        events = [{"class": "clinic.patientIdentityUnbound",
                   "data": {"patientKey": pkey, "identityKey": identity_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": identified_by_lnk}}

    fail("patient DDL: unknown operationType: " + ot)
`

// providerDDLScript handles CreateProvider + TombstoneProvider +
// SetProviderProfile/Hours/TimeOff + BindProviderIdentity. Same idioms as the
// patient script. providerDDLScript is derived from providerDDLScriptTemplate
// by pinning the placeholder — identity-domain's own "provider" role key —
// to its real, deterministic value (see providerRoleKey above): mirrors
// identity-domain's own identityDDLScript/__EXPECTED_CONSUMER_ROLE_KEY__ pin.
var providerDDLScript = strings.ReplaceAll(providerDDLScriptTemplate, "__EXPECTED_PROVIDER_ROLE_KEY__", providerRoleKey)

const providerDDLScriptTemplate = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert(vtx_key, local_name, cls, data):
    # Unconditioned update: create-if-absent / overwrite-if-present (the .hours
    # aspect is opt-in, so SetProviderHours may be the first writer).
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def revive_link(key, source, target, cls, local_name, data):
    # Re-grant of a TOMBSTONED link: an update, not a create. A create asserts
    # revision 0 (step 8) and the tombstone sits at a later revision, so a
    # create RevisionConflicts forever — a re-bound provider whose prior grant
    # was tombstoned could never be re-granted the role. The key is already a
    # declared optionalReads read (posture d), so its revision is hydrated for
    # the update's OCC. Mirrors service-location's revive_link.
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def make_tombstone(key):
    return {"op": "tombstone", "key": key}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def bare_nanoid_or_mint(p, name):
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None or not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_typed(state, key, name, want_class):
    # Endpoint validation: the linked vertex MUST be alive AND the expected
    # class. A dead or wrong-class providerKey/identityKey is never wired.
    if not vertex_alive(state, key):
        fail("UnknownEndpoint: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != want_class:
        fail("WrongClass: " + name + ": " + key + " has class " + str(cls) + ", required " + want_class)

ROLE_PAGE_LIMIT = 50
MAX_ROLE_PAGES = 4

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not from a compile-time constant: the primordial
    # role ids are loaded at runtime (bootstrap.LoadPrimordialNanoIDs) while a
    # package's Definition -- and so its script text -- is built at package-init,
    # so no substitution can see the operator id. The walk mirrors the kernel's
    # own root-grant lens exactly (internal/bootstrap/lenses.go: MATCH (identity)
    # -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator') --
    # the same idiom the appointment DDL's workplace guard uses.
    #
    # Paginated: a role beyond page 1 must not read as "not held" -- the walk
    # follows the cursor up to MAX_ROLE_PAGES pages before giving up, and
    # giving up still denies (fail-closed).
    cursor = None
    for _page in range(MAX_ROLE_PAGES):
        # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
        # roles, so this is never a keyspace scan. A role granted concurrently with
        # this write is not a race worth closing: it can only widen authority, and
        # the confined branch is the safe one.
        page, cursor = kv.Links(actor_key, "holdsRole", "out", cursor, ROLE_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the enumeration
            # above (data-derived key -- the role is unknown until it resolves).
            cn = kv.Read(lk.targetVertex + ".canonicalName")
            if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
                return True
        if cursor == None:
            return False
    return False

def actor_bound_to_provider(actor_key, provider_key):
    # The standing provider-binding guard: an actor identifiedBy-bound to
    # THIS SPECIFIC provider may manage its own hours/time-off even without
    # an operator grant -- complementary to actor_holds_operator, never a
    # replacement (mirrors the appointment DDL's actor_holds_operator/
    # enforce_workplace framing: two binders, each covering the path the
    # other cannot see).
    _, actor_id = parts_of(actor_key, "actor", "identity")
    _, target_provider_id = parts_of(provider_key, "providerKey", "provider")
    # read-posture: (d) declared in contextHint.optionalReads by the standing
    # caller's dispatcher (probing whether THIS actor is bound to the TARGET
    # provider; absent -> AuthDenied, mirroring claim-style absence-tolerance)
    lnk = kv.Read("lnk.provider." + target_provider_id + ".identifiedBy.identity." + actor_id)
    return lnk != None and not lnk.isDeleted

def require_int_in(w, name, lo, hi):
    # Each window arrives as a dict (a nested JSON object). day / openSec / closeSec
    # must be integers in range. Whole-number JSON decodes to a Starlark int.
    if type(w) != type({}):
        fail("InvalidArgument: windows: each window must be an object; got " + type(w))
    v = w.get(name)
    if v == None:
        fail("InvalidArgument: windows: " + name + ": required")
    if type(v) != type(0):
        fail("InvalidArgument: windows: " + name + ": must be an integer; got " + type(v))
    if v < lo or v > hi:
        fail("InvalidArgument: windows: " + name + ": must be in [" + str(lo) + ", " + str(hi) + "]; got " + str(v))
    return v

def claim_provider_identity(provider_key):
    # Entity-keyed guard: at most one identity may ever bind THIS provider
    # (nothing releases the claim, so it is never tombstoned) -- mirrors
    # claim_identity's exclusivity idiom (patient DDL, ddls.go's
    # patientDDLScript), keyed on the PROVIDER side of the pair instead of
    # the identity side. kv.Read here is LAZY (§2.5 idiom) -- it only picks
    # the error message; the safety property is the atomic batch's CreateOnly
    # conditioning at commit: two DIFFERENT identities passing the same
    # providerKey both read it absent and both emit op:create for the
    # IDENTICAL key, but CreateOnly on a key at revision 0 commits exactly
    # once -- the loser's whole batch RevisionConflicts (fail closed, never a
    # silent double-bind).
    # read-posture: (d) declared in contextHint.optionalReads by
    # BindProviderIdentity's dispatcher (no cmd/<app> FE submits this op yet
    # this fire -- W1/W5 land it; absence is the common first-bind case)
    existing = kv.Read(provider_key + ".identityClaim")
    if existing != None:
        fail("ProviderAlreadyBound: " + provider_key + " is already bound to another identity")
    return make_aspect(provider_key, "identityClaim", "providerIdentityClaim", {})

def claim_identity_provider(identity_key):
    # Identity-keyed guard: at most one clinic provider may ever bind THIS
    # identity (nothing releases the claim, so it is never tombstoned) --
    # mirrors claim_identity's exclusivity idiom (patient DDL,
    # patientDDLScript) and its cross-package aspect-attachment shape
    # (identityPatientClaim) exactly, just keyed "providerClaim".
    # read-posture: (d) declared in contextHint.optionalReads by
    # BindProviderIdentity's dispatcher (no cmd/<app> FE submits this op yet
    # this fire -- W1/W5 land it; absence is the common first-bind case)
    existing = kv.Read(identity_key + ".providerClaim")
    if existing != None:
        fail("IdentityAlreadyBoundToProvider: " + identity_key + " is already bound to another clinic provider")
    return make_aspect(identity_key, "providerClaim", "identityProviderClaim", {})

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateProvider":
        full_name = required_string(p, "fullName")
        specialty = required_string(p, "specialty")
        prid = bare_nanoid_or_mint(p, "providerId")
        prkey = "vtx.provider." + prid
        profile = {"fullName": full_name, "specialty": specialty}
        credentials = optional_string(p, "credentials")
        if credentials != None:
            profile["credentials"] = credentials
        bio = optional_string(p, "bio")
        if bio != None:
            profile["bio"] = bio
        mutations = [
            make_vtx(prkey, "provider", {}),
            make_aspect(prkey, "profile", "providerProfile", profile),
        ]
        events = [{"class": "clinic.providerCreated", "data": {"providerKey": prkey}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": prkey}}

    if ot == "TombstoneProvider":
        prkey = required_string(p, "providerKey")
        parts_of(prkey, "providerKey", "provider")
        if not vertex_alive(state, prkey):
            fail("UnknownProvider: " + prkey)
        mutations = [make_tombstone(prkey)]
        events = [{"class": "clinic.providerTombstoned", "data": {"providerKey": prkey}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": prkey}}

    if ot == "SetProviderHours":
        prkey = required_string(p, "providerKey")
        parts_of(prkey, "providerKey", "provider")
        if not vertex_alive(state, prkey):
            fail("UnknownProvider: " + prkey)
        cls = class_of(state, prkey)
        if cls != "provider":
            fail("WrongClass: providerKey: " + prkey + " has class " + str(cls) + ", required provider")
        # Standing binder: operator passes unconditionally; otherwise the
        # actor must be identifiedBy-bound to THIS provider (a provider sets
        # only their OWN hours). Two binders, complementary, mirroring the
        # appointment DDL's actor_holds_operator/require_workplace framing.
        if not actor_holds_operator(op.actor):
            if not actor_bound_to_provider(op.actor, prkey):
                fail("AuthDenied: " + op.actor + " may not set hours for provider " + prkey)
        # windows is required (pass [] to clear the constraint). Each window is
        # {day:0-6 (Sun=0), openSec, closeSec} in UTC seconds-of-day, openSec<closeSec.
        if not hasattr(p, "windows"):
            fail("InvalidArgument: windows: required (use [] to clear)")
        windows = getattr(p, "windows")
        if type(windows) != type([]):
            fail("InvalidArgument: windows: must be a list")
        clean = []
        for w in windows:
            day = require_int_in(w, "day", 0, 6)
            open_sec = require_int_in(w, "openSec", 0, 86400)
            close_sec = require_int_in(w, "closeSec", 0, 86400)
            if not (open_sec < close_sec):
                fail("InvalidArgument: windows: openSec must be < closeSec; got openSec=" + str(open_sec) + " closeSec=" + str(close_sec))
            clean.append({"day": day, "openSec": open_sec, "closeSec": close_sec})
        # Unconditioned upsert of the WHOLE .hours aspect (create-if-absent — it is
        # opt-in, CreateProvider does not init it). No OCC: hours are config, not a
        # write-path claim key.
        mutations = [make_aspect_upsert(prkey, "hours", "providerHours", {"windows": clean})]
        events = [{"class": "clinic.providerHoursSet",
                   "data": {"providerKey": prkey, "windowCount": len(clean)}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": prkey}}

    if ot == "SetProviderTimeOff":
        prkey = required_string(p, "providerKey")
        parts_of(prkey, "providerKey", "provider")
        if not vertex_alive(state, prkey):
            fail("UnknownProvider: " + prkey)
        cls = class_of(state, prkey)
        if cls != "provider":
            fail("WrongClass: providerKey: " + prkey + " has class " + str(cls) + ", required provider")
        # Standing binder: operator passes unconditionally; otherwise the
        # actor must be identifiedBy-bound to THIS provider (a provider sets
        # only their OWN time off). Two binders, complementary, mirroring the
        # appointment DDL's actor_holds_operator/require_workplace framing.
        if not actor_holds_operator(op.actor):
            if not actor_bound_to_provider(op.actor, prkey):
                fail("AuthDenied: " + op.actor + " may not set time off for provider " + prkey)
        # ranges is required (pass [] to clear all blackouts). Each range is
        # {from, to, reason?} — from/to RFC3339 UTC instants with from<to. Normalize
        # both to canonical whole-second UTC (time.rfc3339_utc — pure, no clock read)
        # so the stored ranges compare lexically == chronologically (the overlap test
        # CreateAppointment runs against them is sound for any caller offset).
        if not hasattr(p, "ranges"):
            fail("InvalidArgument: ranges: required (use [] to clear)")
        ranges = getattr(p, "ranges")
        if type(ranges) != type([]):
            fail("InvalidArgument: ranges: must be a list")
        clean = []
        for r in ranges:
            if type(r) != type({}):
                fail("InvalidArgument: ranges: each range must be an object; got " + type(r))
            rf = r.get("from")
            rt = r.get("to")
            if rf == None or type(rf) != type("") or len(rf.strip()) == 0:
                fail("InvalidArgument: ranges: from: required non-empty RFC3339 string")
            if rt == None or type(rt) != type("") or len(rt.strip()) == 0:
                fail("InvalidArgument: ranges: to: required non-empty RFC3339 string")
            cf = time.rfc3339_utc(rf.strip())
            ct = time.rfc3339_utc(rt.strip())
            if not (cf < ct):
                fail("InvalidArgument: ranges: from must be < to; got from=" + cf + " to=" + ct)
            cr = {"from": cf, "to": ct}
            reason = r.get("reason")
            if reason != None and type(reason) == type("") and len(reason.strip()) > 0:
                cr["reason"] = reason.strip()
            clean.append(cr)
        # Unconditioned upsert of the WHOLE .timeOff aspect (create-if-absent — it is
        # opt-in, CreateProvider does not init it). No OCC: time-off is config, not a
        # write-path claim key.
        mutations = [make_aspect_upsert(prkey, "timeOff", "providerTimeOff", {"ranges": clean})]
        events = [{"class": "clinic.providerTimeOffSet",
                   "data": {"providerKey": prkey, "rangeCount": len(clean)}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": prkey}}

    if ot == "SetProviderProfile":
        prkey = required_string(p, "providerKey")
        parts_of(prkey, "providerKey", "provider")
        if not vertex_alive(state, prkey):
            fail("UnknownProvider: " + prkey)
        cls = class_of(state, prkey)
        if cls != "provider":
            fail("WrongClass: providerKey: " + prkey + " has class " + str(cls) + ", required provider")
        full_name = required_string(p, "fullName")
        specialty = required_string(p, "specialty")
        profile = {"fullName": full_name, "specialty": specialty}
        credentials = optional_string(p, "credentials")
        if credentials != None:
            profile["credentials"] = credentials
        bio = optional_string(p, "bio")
        if bio != None:
            profile["bio"] = bio
        # Unconditioned upsert REPLACING the whole .profile aspect (it always exists —
        # CreateProvider mints it). The editor seeds the form from the projected
        # profile (fullName/specialty/credentials/bio all carried by clinicProviders),
        # so a replace edits the live set; fullName + specialty stay required so the
        # provider never loses the fields its roster lens (WHERE fullName <> null) and
        # booking picker depend on. Mirrors SetProviderHours / SetProviderTimeOff.
        mutations = [make_aspect_upsert(prkey, "profile", "providerProfile", profile)]
        events = [{"class": "clinic.providerProfileSet", "data": {"providerKey": prkey}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": prkey}}

    if ot == "BindProviderIdentity":
        prkey = required_string(p, "providerKey")
        _, provider_id = parts_of(prkey, "providerKey", "provider")
        require_live_typed(state, prkey, "providerKey", "provider")

        identity_key = required_string(p, "identityKey")
        _, identity_id = parts_of(identity_key, "identityKey", "identity")
        require_live_typed(state, identity_key, "identityKey", "identity")

        # provider identifiedBy identity (Contract #1 §1.1: the later-arriving
        # provider is the source, the pre-existing identity is the target).
        # Sentence: "provider identifiedBy identity". Mirrors the patient
        # identifiedBy mint verbatim (patient DDL, patientDDLScript).
        identified_by_lnk = "lnk.provider." + provider_id + ".identifiedBy.identity." + identity_id
        mutations = [make_link(identified_by_lnk, prkey, identity_key, "identifiedBy", "identifiedBy", {})]

        # Mutual exclusivity, both sides (doubles CreatePatient's single-sided
        # claim_identity): at most one identity ever binds THIS provider, and
        # at most one clinic provider ever binds THIS identity.
        mutations.append(claim_provider_identity(prkey))
        mutations.append(claim_identity_provider(identity_key))

        # Grant the provider role, exactly as ClaimIdentity grants consumer
        # (identity-domain's identityDDLScript) -- but IDEMPOTENT (mirrors
        # rbac AssignRole's state-check branch, rbac-domain ddls.go:337-339):
        # a holdsRole link already alive is left untouched rather than
        # re-created, so a defensive re-bind ceremony never collides with its
        # own prior grant.
        provider_role_key = "__EXPECTED_PROVIDER_ROLE_KEY__"
        provider_role_id = provider_role_key[len("vtx.role."):]
        holds_role_lnk = "lnk.identity." + identity_id + ".holdsRole.role." + provider_role_id
        # read-posture: (d) declared in contextHint.optionalReads by
        # BindProviderIdentity's dispatcher (no cmd/<app> FE submits this op
        # yet this fire -- W1/W5 land it; absence is the common first-bind
        # case, mirroring rbac's AssignRole idempotency check)
        existing_role_grant = kv.Read(holds_role_lnk)
        if existing_role_grant == None:
            mutations.append(make_link(holds_role_lnk, identity_key, provider_role_key, "holdsRole", "holdsRole", {}))
        elif existing_role_grant.isDeleted:
            mutations.append(revive_link(holds_role_lnk, identity_key, provider_role_key, "holdsRole", "holdsRole", {}))

        events = [{"class": "clinic.providerIdentityBound",
                   "data": {"providerKey": prkey, "identityKey": identity_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": identified_by_lnk}}

    fail("provider DDL: unknown operationType: " + ot)
`

// appointmentDDLScript handles CreateAppointment + RescheduleAppointment +
// SetAppointmentStatus + TombstoneAppointment. CreateAppointment validates BOTH
// endpoints (patient + provider) alive + class, then atomically mints the
// appointment vertex + the .schedule + .status{scheduled} aspects + the forPatient
// + withProvider links (Contract #1 §1.1 — the later-arriving appointment is the
// source). RescheduleAppointment rewrites the .schedule aspect with new times
// (re-deriving remindAt = startsAt − 24h), an unconditioned upsert that leaves the
// links + status untouched. SetAppointmentStatus is an unconditioned upsert of the
// .status aspect (no read-merge — status is its own aspect).
//
// Double-book detection is WRITE-PATH deterministic-key claims, not read-time
// enumeration (design clinic-booking-write-path-slot-claims-design.md): the
// clinic's booking grid is a mandatory 15-minute cadence, so [startsAt,endsAt)
// discretizes losslessly into a finite set of grid cells. CreateAppointment claims
// one providerSlotClaim / patientSlotClaim aspect per covered cell on the provider
// and patient hubs (vtx.<hub>.<slotcellcode>, deterministic — the SAME key across
// two competing bookings for the same cell); the key collision at commit
// (CreateOnly / expectedRevision) IS the lock, not a read+epoch pair. Reschedule
// releases the cells the move no longer needs and claims the newly-covered ones in
// the SAME atomic batch (a collision leaves the original booking's claims intact);
// SetAppointmentStatus and TombstoneAppointment release all held cells on a
// terminal transition / hard delete (recomputed from .schedule + the caller-
// supplied, link-validated provider/patient — never a stored back-reference).
const appointmentDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert(vtx_key, local_name, cls, data):
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert_occ(vtx_key, local_name, cls, data, expected_revision):
    # Like make_aspect_upsert but carries an explicit expectedRevision so the
    # commit applies an OCC condition (an update with no expectedRevision commits
    # UNCONDITIONED — step8_commit.go). The bookings-index serialization point.
    m = make_aspect_upsert(vtx_key, local_name, cls, data)
    m["expectedRevision"] = expected_revision
    return m

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def make_tombstone(key):
    return {"op": "tombstone", "key": key}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v


def optional_number(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or (type(v) != type(0) and type(v) != type(0.0)):
        return None
    return v
def bare_nanoid_or_mint(p, name):
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

# --- workplace write confinement (facet-staff-worlds-design.md §3.5) ---------
#
# A staff actor may write only inside the location it worksAt. Three properties
# make this sound; each is a trap a simpler form falls into.
#
# 1. The exemption is ROLE-derived, never worksAt-derived. Exempting "an actor
#    with no worksAt link" would be perverse: UnwireWorksAt would WIDEN a staff
#    member's write surface from one building to everywhere. The exemption is
#    holding the primordial 'operator' role -- the same walk the kernel projects
#    its own root grant from (internal/bootstrap/lenses.go: MATCH (identity)
#    -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator'), so
#    an actor that is genuinely root necessarily has it. Everyone else is
#    confined, and an actor holding no roles at all is confined to nothing.
#
# 2. A tombstoned link is ABSENT. kv.Read returns the tombstone DOCUMENT rather
#    than None (step4_hydrate routes only ErrKeyNotFound to knownAbsent), and
#    UnwireWorksAt tombstones rather than deletes, so the '== None' form the
#    cafe/clinic self-guards use would let a moved-on staff member keep writing.
#
# 3. The location is resolved from the TARGET's own topology, never from a
#    payload field -- a caller cannot forge which building it is writing at.
ROLE_PAGE_LIMIT = 50
MAX_ROLE_PAGES = 4
WORKPLACE_PARENT_PAGE_LIMIT = 20
MAX_PARENT_PAGES = 4
WORKPLACE_MAX_DEPTH = 8
WORKPLACE_MAX_NODES = 64
# A provider practises at a handful of sites at most, but ALL of them are the
# confining set (sites_for_provider), so this walks every page rather than
# stopping at the first.
PROVIDER_SITE_PAGE_LIMIT = 20
MAX_PROVIDER_SITE_PAGES = 4

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not from a compile-time constant: the primordial
    # role ids are loaded at runtime (bootstrap.LoadPrimordialNanoIDs) while a
    # package's Definition -- and so its script text -- is built at package-init,
    # so no substitution can see the operator id. The walk mirrors the kernel's
    # own root-grant lens exactly (internal/bootstrap/lenses.go: MATCH (identity)
    # -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator').
    #
    # Paginated: a role beyond page 1 must not read as "not held" -- the walk
    # follows the cursor up to MAX_ROLE_PAGES pages before giving up, and
    # giving up still denies (fail-closed).
    cursor = None
    for _page in range(MAX_ROLE_PAGES):
        # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
        # roles, so this is never a keyspace scan. A role granted concurrently with
        # this write is not a race worth closing: it can only widen authority, and
        # the confined branch is the safe one.
        page, cursor = kv.Links(actor_key, "holdsRole", "out", cursor, ROLE_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the enumeration
            # above (data-derived key -- the role is unknown until it resolves).
            cn = kv.Read(lk.targetVertex + ".canonicalName")
            if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
                return True
        if cursor == None:
            return False
    return False

def worksAt_covers(actor_id, location_key):
    # Answers "does this actor worksAt this location, or any LIVE location that
    # contains it?" -- a BREADTH-first walk up the containedIn topology, testing
    # the actor's deterministic worksAt link at every node. The location itself
    # is tested first, so a staff member wired to an exact unit matches too; one
    # wired to any containing building matches everything containedIn it.
    #
    # A tombstoned link OR VERTEX is absent. kv.Read returns the tombstone
    # document rather than None (step4_hydrate routes only ErrKeyNotFound to
    # knownAbsent), and UnwireWorksAt / TombstoneLocation tombstone rather than
    # delete, so isDeleted is tested explicitly in three places: the worksAt
    # link, each containedIn link, and every location VERTEX the walk stands on.
    # The vertex test is what stops a DECOMMISSIONED location from still
    # conferring authority -- TombstoneLocation does not cascade to containedIn
    # links (location-domain), so those links stay live and only the vertex's own
    # isDeleted marks it gone, while the read side stops dead there (the full
    # engine's fetchNode yields nothing for a soft-deleted node). Transiting one
    # would grant a write the reader would never show.
    #
    # It is tested on EVERY node, the caller-supplied one included, not just on
    # ancestors: a guard where a dead ancestor confers nothing but a dead
    # starting location confers everything would be exactly the kind of
    # inconsistency the next reader copies wrongly.
    #
    # EVERY parent is followed, not one per level: containment is a DAG. A walk
    # that kept a single parent would deny a staffer wired to whichever branch it
    # happened to discard, while a read-side lens projecting a covering set
    # unions every branch of [:containedIn*0..7] (cafe-domain's and
    # wellness-domain's coveringLocations are the two that do).
    #
    # Bounded three ways so an op-time guard cannot fan out: WORKPLACE_MAX_DEPTH
    # levels (0..7, the read side's hop range), WORKPLACE_PARENT_PAGE_LIMIT
    # parents per node, and WORKPLACE_MAX_NODES distinct nodes overall, a node
    # never being enqueued twice. Exhausting a bound falls through to the final
    # 'return False' -- a DENIAL, never an escape. The node budget is the one
    # bound the read side does not share (its walk caps hops, not nodes), so a
    # containment tree wide enough to exhaust it denies a write the reader would
    # show; it is set far above any real topology, and it fails closed.
    if location_key == None:
        return False
    frontier = [location_key]
    seen = [location_key]
    for _ in range(WORKPLACE_MAX_DEPTH):
        if len(frontier) == 0:
            return False
        parents = []
        for cur in frontier:
            parts = cur.split(".")
            if len(parts) != 3:
                # Not walkable. Stops its OWN branch rather than aborting the
                # walk, so one malformed ancestor cannot deny a sibling branch
                # that would have matched. A malformed location_key still
                # denies: nothing else is queued, so the frontier empties.
                continue
            # read-posture: (e) per-candidate follow-up read off the containedIn
            # enumeration below -- the location VERTEX, so a tombstoned one
            # neither confers a match nor is walked through.
            node = kv.Read(cur)
            if node == None or node.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the same
            # enumeration (data-derived key -- the ancestor chain is not
            # knowable client-side, so it cannot be pre-declared).
            lnk = kv.Read("lnk.identity." + actor_id + ".worksAt." + parts[1] + "." + parts[2])
            if lnk != None and not lnk.isDeleted:
                return True
            # Paginated: a parent beyond page 1 must not read as "no more
            # parents" -- the walk follows the cursor up to MAX_PARENT_PAGES
            # pages before moving on, same as actor_holds_operator's role walk.
            cursor = None
            for _page in range(MAX_PARENT_PAGES):
                # read-posture: (e) relation=containedIn epoch=none -- a location has
                # at most a few parents; containment is provisioned topology, not
                # written concurrently with this op.
                page, cursor = kv.Links(cur, "containedIn", "out", cursor, WORKPLACE_PARENT_PAGE_LIMIT)
                for lk in page:
                    if lk.isDeleted:
                        continue
                    nxt = lk.targetVertex
                    if nxt in seen:
                        continue
                    if len(seen) >= WORKPLACE_MAX_NODES:
                        continue
                    # Charged to the budget at ENQUEUE, so the node count bounds the
                    # walk's reads exactly rather than to within a page, and an
                    # ancestor reachable from several branches is visited once.
                    seen.append(nxt)
                    parents.append(nxt)
                if cursor == None:
                    break
        frontier = parents
    return False

def workplace_exempt():
    # The cheap half of require_workplace, callable BEFORE a domain resolver
    # runs. Starlark evaluates arguments eagerly, so
    # require_workplace(resolve(x), ...) would walk the target's topology even
    # for root -- wasted reads, and worse, a malformed key anywhere in that walk
    # raises where the op previously succeeded. Call sites therefore gate on
    # this; require_workplace re-checks it anyway, so a site that forgets the
    # gate is still CORRECT, only slower.
    #
    # The self-service exemption keys on op.authTargetValidated -- the platform
    # bit that is true only where step 3 CHECKED authContext.target, which for
    # this package's ops means the consumer's scope=self grant (step 3 denies
    # scope=self unless target == actor). Nothing a caller sends can set it.
    # A caller-visible predicate cannot stand in for it: step 3 authorizes a
    # scope=ANY grant WITHOUT inspecting authContext.target
    # (step3_auth_capability.go: the "any" case returns Authorized immediately)
    # and the Gateway forwards the client's authContext verbatim, so a staff
    # caller holding scope=any can attach any target it likes -- including its
    # own actor key, which satisfies an equality test and skips workplace
    # confinement entirely.
    return op.authTargetValidated or actor_holds_operator(op.actor)

def require_workplace(location_keys, what):
    # Binds the STANDING path only -- operator and staff role grants. A scope=self
    # caller is bound instead by its own op's ownership probe (the applicationFor /
    # identifiedBy indirection): a resident legitimately holds no worksAt link,
    # and confining them by a rule written for staff would deny every
    # self-service write. The two guards are complementary, not alternatives --
    # each binds the path the other cannot see.
    #
    # The self-service exemption keys on op.authTargetValidated, mirroring
    # workplace_exempt -- the cheap pre-gate this function deliberately
    # re-checks, so a call site that skips it is still correct, only slower.
    if op.authTargetValidated:
        return
    enforce_workplace(location_keys, what)

def enforce_workplace(location_keys, what):
    # require_workplace minus the validated-target exemption, for a
    # resource-scoped op that has already checked for itself that the validated
    # target names the resource being acted on. Past that check the caller is an
    # ordinary staff member and must clear the worksAt walk like any other.
    #
    # location_keys is a LIST of candidate locations, and covering ANY ONE of
    # them authorizes the write: a target can legitimately sit at several places
    # at once (a provider practises at two buildings), and staff at either one
    # are equally entitled to it. An empty list -- a target whose location
    # cannot be resolved at all -- is a DENIAL for anyone but an operator, so an
    # unwired topology fails closed rather than falling open.
    if actor_holds_operator(op.actor):
        return
    _, actor_id = parts_of(op.actor, "actor", "identity")
    for loc in location_keys:
        if loc != None and worksAt_covers(actor_id, loc):
            return
    fail("AuthDenied: " + op.actor + " does not worksAt any location covering " +
         str(location_keys) + "; " + what)

def enforce_workplace_confined(location_keys, what):
    # clinic-domain-specific variant of enforce_workplace (deliberately a
    # DIFFERENT name, not a divergent body under the pinned one -- S10 pins
    # require_workplace/enforce_workplace/workplace_exempt corpus-wide, and
    # this function carries package-specific policy those may not: it drops
    # the actor_holds_operator recheck enforce_workplace itself performs.
    #
    # Every call site below computes
    # op.authTargetValidated or actor_holds_operator(op.actor) inline, once,
    # BEFORE deciding whether to reach this function at all -- same gate
    # workplace_exempt() computes, same short-circuit (a target-validated
    # self-service caller never reaches actor_holds_operator), same
    # protection against evaluating an expensive location_keys resolver
    # (sites_for_provider / appointment_sites, real multi-hop kv.Links work
    # in this package, unlike cafe-domain's b997ff2a precedent) for an
    # operator or validated caller. This function is therefore reachable
    # ONLY on the confined path (neither validated nor operator) and does
    # not re-derive that: actor_holds_operator's role-walk is a paginated
    # NATS round trip, and the workplace_exempt()+enforce_workplace() pair
    # pays it TWICE per confined write -- a fully redundant round trip on
    # exactly the path most likely to blow the Starlark wall (live:
    # CreateAppointment/SetAppointmentStatus at 198-375ms against the 250ms
    # budget, clinic-domain 2026-08-29, front-desk verticals triage).
    #
    # location_keys is a LIST of candidate locations, and covering ANY ONE of
    # them authorizes the write, mirroring enforce_workplace exactly minus
    # the operator recheck; an empty list is a DENIAL, so an unwired
    # topology fails closed.
    _, actor_id = parts_of(op.actor, "actor", "identity")
    for loc in location_keys:
        if loc != None and worksAt_covers(actor_id, loc):
            return
    fail("AuthDenied: " + op.actor + " does not worksAt any location covering " +
         str(location_keys) + "; " + what)

def appointment_provider(appt_id):
    # The appointment's OWN provider, resolved from the graph (never a
    # caller-supplied payload field -- a caller cannot forge which provider
    # it is writing against). Factored out of appointment_sites below so the
    # standing provider-binding guard (actor_bound_to_appointment_provider)
    # can resolve the same provider without a second read.
    # read-posture: (e) relation=withProvider epoch=none -- an appointment
    # carries exactly one withProvider link, so this is never a keyspace scan.
    ppage, _ = kv.Links("vtx.appointment." + appt_id, "withProvider", "out", None, 1)
    provider = None
    for lk in ppage:
        if not lk.isDeleted:
            provider = lk.targetVertex
    return provider

def appointment_patient(appt_id):
    # Symmetric analog of appointment_provider over the forPatient link — used by
    # MarkPastDueNoShow, which has no caller-supplied patient to validate (its only
    # caller is Weaver's directOp dispatch, never a human).
    # read-posture: (e) relation=forPatient epoch=none -- an appointment carries
    # exactly one forPatient link, so this is never a keyspace scan.
    ppage, _ = kv.Links("vtx.appointment." + appt_id, "forPatient", "out", None, 1)
    patient = None
    for lk in ppage:
        if not lk.isDeleted:
            patient = lk.targetVertex
    return patient

def vertex_live(key):
    # Is this vertex present AND not tombstoned? The standalone form of the
    # vertex test worksAt_covers performs inline at every node of its bounded
    # walk, for the resolvers that walk THROUGH a vertex to produce that walk's
    # input -- a provider, a studio, a lease. Those hops are invisible to
    # worksAt_covers: by the time it runs the dead vertex has already been
    # transited and only its live locations remain, so the confinement it
    # computes is the dead entity's ex-topology.
    #
    # A tombstone is a DOCUMENT, not an absence. kv.Read returns it rather than
    # None (step4_hydrate routes only ErrKeyNotFound to knownAbsent), so the
    # '== None' test alone reads a tombstoned vertex as live. Both halves are
    # required, and a None key answers False so a caller that resolved nothing
    # takes the same denying branch as one that resolved something dead.
    #
    # Distinct from vertex_alive(state, key), which answers the same question
    # from the operation's DECLARED contextHint.reads. The keys here are
    # data-derived -- resolved from a link mid-walk, so unknowable client-side
    # and undeclarable -- and only a live read can see them.
    #
    if key == None:
        return False
    # read-posture: (e) one bounded read per candidate. At the sites this exists
    # for, the key is data-derived -- resolved from a kv.Links enumeration
    # mid-walk, so unknowable client-side and undeclarable. A resolver cannot
    # see which caller it has, and some callers reach it with a payload key a
    # declared read has already proved live; there this is a redundant re-proof,
    # not a second class of access. Screening at the resolver rather than per
    # call site is what keeps the rule uniform.
    node = kv.Read(key)
    return node != None and not node.isDeleted

def sites_for_provider(provider):
    # A provider's practicesAt sites -- the buildings clinicAppointmentsRead
    # anchors its workplace read token on, so write confinement and read
    # confinement resolve through exactly the same edge. provider may be None
    # (an appointment whose withProvider link is absent), which yields [].
    # The provider VERTEX, not just a non-None key: TombstoneProvider
    # soft-deletes it with no cascade onto practicesAt, so a dead provider would
    # otherwise still hand back the sites it no longer practises at.
    if not vertex_live(provider):
        return []
    # A provider practises at a handful of sites at most, but ALL of them are
    # the confining set (staff at any one of a provider's sites are equally
    # entitled to that provider's appointments), so this walks every page
    # rather than stopping at the first.
    cursor = None
    sites = []
    for _page in range(MAX_PROVIDER_SITE_PAGES):
        # read-posture: (e) relation=practicesAt epoch=none (a site assigned
        # concurrently with this write can only WIDEN the confining set, never
        # narrow it, so the confined branch stays the safe one) -- bounded,
        # never a keyspace scan.
        spage, cursor = kv.Links(provider, "practicesAt", "out", cursor, PROVIDER_SITE_PAGE_LIMIT)
        for lk in spage:
            if not lk.isDeleted:
                sites.append(lk.targetVertex)
        if cursor == None:
            break
    return sites

def appointment_sites(appt_id, provider):
    # Takes the provider the caller already resolved (appointment_provider),
    # so the withProvider listing that names it runs once per execution
    # rather than once per caller plus once here.
    sites = sites_for_provider(provider)
    if sites:
        return sites
    # TombstoneProvider soft-deletes with no cascade onto practicesAt, so
    # sites_for_provider's vertex_live gate now returns [] for a tombstoned
    # provider -- stranding staff who need to manage an appointment that
    # predates the tombstone. Fall back to the appointment's own atSite link:
    # CreateAppointment validates it alive + provider-assigned at write time,
    # so it names a real site independent of the provider's current status,
    # never a caller-forged one. worksAt_covers re-proves the building itself
    # is alive before trusting it, exactly as it does for a live provider's
    # sites.
    # read-posture: (e) relation=atSite epoch=none -- CreateAppointment writes
    # at most one atSite link per appointment, so this is a single bounded
    # enumeration off the appointment key already proven alive by the caller,
    # never a keyspace scan.
    apage, _ = kv.Links("vtx.appointment." + appt_id, "atSite", "out", None, 1)
    for lk in apage:
        if not lk.isDeleted:
            return [lk.targetVertex]
    return []

def actor_bound_to_appointment_provider(actor_key, provider):
    # The THIRD standing binder (beside operator/workplace, require_workplace's
    # own doc frames those two as complementary): a provider-role actor may
    # accept/reschedule/set-status on the SPECIFIC appointment its own
    # identifiedBy binding covers, independent of any worksAt/workplace link.
    # provider may be None (an appointment whose withProvider link is absent),
    # which never matches.
    if provider == None:
        return False
    _, actor_id = parts_of(actor_key, "actor", "identity")
    _, provider_id = parts_of(provider, "provider", "provider")
    # read-posture: (e) per-candidate follow-up read off the withProvider
    # enumeration the caller already performed (the provider key is
    # data-derived from that listing, not a caller-supplied payload field) --
    # probing whether ITS OWN actor is this appointment's bound provider;
    # absent -> falls through to require_workplace, never a hard failure.
    lnk = kv.Read("lnk.provider." + provider_id + ".identifiedBy.identity." + actor_id)
    return lnk != None and not lnk.isDeleted

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None:
        return None
    if not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_typed(state, key, name, want_class):
    # Endpoint validation: the endpoint MUST be alive AND the expected class. A
    # dead or wrong-class endpoint is never wired into an appointment.
    if not vertex_alive(state, key):
        fail("UnknownEndpoint: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != want_class:
        fail("WrongClass: " + name + ": " + key + " has class " + str(cls) + ", required " + want_class)

def key_type_of(key):
    # The type segment of a 3-segment vtx.<type>.<NanoID> key, or None for any
    # other shape (an aspect key, a link key, a malformed string).
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        return None
    return parts[1]

def doc_class(doc):
    if doc == None or not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

# The classes a live location-domain BUILDING may carry: its own key type.
BUILDING_CLASSES = ["building"]

def require_site_membership(site_key, provider):
    # Optional site association (Increment 2 — see the multi-site design note).
    # Unlike leaseAppKey, once supplied this is a HARD requirement, not a silent
    # fall-through: the site must be alive + a vtx.building.<NanoID> key AND the
    # provider must practicesAt it (the clinicSiteAssignment link), or the whole
    # op rejects. Both reads are on-demand (kv.Read) rather than a state[]
    # snapshot, mirroring clinicSite/clinicSiteAssignment's own scripts.
    #
    # BOTH the key and the class are checked, and each catches what the other
    # cannot. The KEY's type segment is what distinguishes a building from a
    # unit at all, and a practicesAt link's target is a building. The CLASS
    # is what proves location-domain minted the vertex: a foreign package
    # writing vtx.building.<id> with a class of its own passes the key check
    # and must still be refused.
    # read-posture: (d) declared optionalReads by CreateAppointment's dispatcher
    # (cmd/clinic-app/web/app.js submitAppointment) — site is optional overall,
    # so a hard-required declared read would be wrong when it's omitted.
    site_doc = kv.Read(site_key)
    if site_doc == None or site_doc.isDeleted:
        fail("UnknownSite: site: " + site_key + " is absent or tombstoned")
    if key_type_of(site_key) != "building":
        fail("NotALocation: site: " + site_key + " is not a vtx.building.<NanoID> key, required building")
    if doc_class(site_doc) not in BUILDING_CLASSES:
        fail("NotALocation: site: " + site_key + " has class " + str(doc_class(site_doc)) + ", required building")
    _, provider_id = parts_of(provider, "provider", "provider")
    _, site_id = parts_of(site_key, "site", "building")
    link_key = "lnk.provider." + provider_id + ".practicesAt.building." + site_id
    # read-posture: (d) declared optionalReads by CreateAppointment's dispatcher
    # (cmd/clinic-app/web/app.js submitAppointment) — mirrors AssignProviderSite's
    # own on-demand read of the same deterministic per-pair link key.
    link_doc = kv.Read(link_key)
    if link_doc == None or link_doc.isDeleted:
        fail("ProviderNotAtSite: provider " + provider + " does not practicesAt site " + site_key)

def enforce_hours(provider, starts_at, ends_at):
    # Opt-in provider availability windows (Capability-KV §06 — the op's own Starlark
    # logic). Read the provider's .hours aspect on demand (kv.Read, §2.5 — NOT a
    # declared/OCC read: hours are config, not the booking serialization point). An
    # absent / deleted aspect or windows=[] means UNCONSTRAINED (backward-compatible
    # with providers created before this capability). Otherwise the appointment's
    # [start, end] must sit inside ONE window on its UTC weekday. Times are exact
    # integers (time.weekday 0=Sun..6=Sat, time.seconds_of_day 0..86399) so the
    # membership test is integer arithmetic — no mixed-width string-compare hazard.
    # read-posture: (c) config — deliberately unsnapshotted (out of OCC so an
    # hours edit never conflicts a concurrent booking commit)
    hours = kv.Read(provider + ".hours")
    if hours == None or hours.isDeleted:
        return
    windows = hours.data.get("windows")
    if windows == None or type(windows) != type([]) or len(windows) == 0:
        return
    sw = time.weekday(starts_at)
    ew = time.weekday(ends_at)
    ss = time.seconds_of_day(starts_at)
    es = time.seconds_of_day(ends_at)
    if sw != ew:
        fail("OutsideHours: appointment spans more than one UTC day (start weekday " + str(sw) + ", end weekday " + str(ew) + "); book within a single availability window")
    for w in windows:
        if type(w) != type({}):
            continue
        d = w.get("day")
        o = w.get("openSec")
        c = w.get("closeSec")
        if d == None or o == None or c == None:
            continue
        if d == sw and o <= ss and es <= c:
            return
    fail("OutsideHours: provider " + provider + " is not available at the requested time (UTC weekday " + str(sw) + ", " + str(ss) + "s-" + str(es) + "s of day); no matching availability window")

def time_off_overlap(provider, starts_at, ends_at):
    # Opt-in provider date-specific time-off (Capability-KV §06 — the op's own
    # Starlark logic): read the provider's .timeOff aspect on demand (kv.Read,
    # §2.5 — config, not the booking serialization point) and return the first
    # blocked [from, to) range overlapping [starts_at, ends_at), or None if none
    # does. An absent / deleted aspect or ranges=[] means NO blackouts (backward-
    # compatible with providers created before this capability). Ranges are
    # canonical-UTC RFC3339 (lexical == chronological); half-open overlap test
    # (a.start < b.end AND b.start < a.end) — back-to-back (appt ending exactly at
    # a range's from, or starting exactly at its to) does NOT overlap, so a
    # booking up to the start of a blackout (or from its end) is allowed.
    # read-posture: (c) config — deliberately unsnapshotted (out of OCC so a
    # time-off edit never conflicts a concurrent booking commit)
    off = kv.Read(provider + ".timeOff")
    if off == None or off.isDeleted:
        return None
    ranges = off.data.get("ranges")
    if ranges == None or type(ranges) != type([]) or len(ranges) == 0:
        return None
    for r in ranges:
        if type(r) != type({}):
            continue
        rf = r.get("from")
        rt = r.get("to")
        if rf == None or rt == None:
            continue
        if starts_at < rt and rf < ends_at:
            return r
    return None

def enforce_time_off(provider, starts_at, ends_at):
    r = time_off_overlap(provider, starts_at, ends_at)
    if r != None:
        fail("ProviderUnavailable: provider " + provider + " is on time-off " + r.get("from") + "/" + r.get("to") + "; requested " + starts_at + "/" + ends_at)

def enforce_future(starts_at, submitted_at):
    # Soft past-time guard (Capability-KV §06 — the op's own Starlark logic). The
    # booking MUST start strictly after op.submittedAt: a past / now booking is
    # almost always a mistake AND its remindAt = startsAt − 24h would also be past,
    # so the clinic-reminders @at lane would silently never fire a useful reminder.
    # submittedAt is caller-supplied (the host clock is intentionally NOT exposed to
    # Starlark, starlark_runner.go), so this is a SOFT guard appropriate to the
    # trusted single-identity model — not a hard temporal authority. Normalize it to
    # canonical whole-second UTC (time.rfc3339_utc — pure, no clock read) so the
    # compare is sound for any offset; canonical-UTC RFC3339 compares lexically ==
    # chronologically.
    submitted = time.rfc3339_utc(submitted_at)
    if not (submitted < starts_at):
        fail("ScheduleInPast: startsAt " + starts_at + " is not in the future (submitted " + submitted + ")")

def refuse_before_start(appt_key, sched, submitted_at, verb):
    # The visit-has-started clock shared by every op that needs the visit to
    # already be underway: a missing/deleted schedule or one with no startsAt
    # is a state the caller never validated for (InvalidState); otherwise the
    # boundary is inclusive (submitted AT startsAt has started) and soft
    # (submitted_at is caller-supplied, normalized to canonical UTC; the
    # stored startsAt is canonical UTC, so the compare is lexical ==
    # chronological — the same guard enforce_future reads for the opposite
    # direction).
    if sched == None or sched.isDeleted or sched.data.get("startsAt") == None:
        fail("InvalidState: " + appt_key + ".schedule is missing startsAt; cannot " + verb)
    submitted = time.rfc3339_utc(submitted_at)
    if submitted < sched.data.get("startsAt"):
        fail("NotYetStarted: appointment " + appt_key + " starts at " + sched.data.get("startsAt") + " (submitted " + submitted + "); cannot " + verb + " before the visit starts")

def enforce_started(appt_key, status, sched, submitted_at):
    # A visit is completed or missed only once it has started: both outcomes are
    # facts about the scheduled time having passed (a noShow bills its fee at once
    # via clinic-ledger's noShowSettlement), so a future visit is refused
    # NotYetStarted — on the first terminal transition (SetAppointmentStatus) and
    # on a terminal→terminal correction alike, or a cancel-then-correct would
    # reach the same outcome by the side door. Cancel carries no clock here — it is
    # the legitimate before-the-visit terminal for staff; a patient's own cancel
    # reads self_visit_clock instead. verb is "mark " + status — the refusal text
    # SetAppointmentStatus's callers and pins read.
    if status not in ("completed", "noShow"):
        return
    refuse_before_start(appt_key, sched, submitted_at, "mark " + status)

# The late-cancellation window, expressed as the negative offset from the
# appointment's own startsAt that opens it — the reminder lead: the reminder
# that says "your visit is tomorrow" (remindAt = startsAt − 24h, the
# clinic-reminders lane) is the last free-cancel moment. Inside the window a
# patient's own cancel still lands but owes the no-show fee, and a patient's
# own reschedule is refused (self_visit_clock); staff paths carry no window.
LATE_CANCEL_WINDOW_OFFSET = "-24h"

# The no-show fee written when no caller-supplied noShowFeeCents is given —
# the staff-observed noShow, the noShow correction, and the patient's own late
# cancel all default to it.
DEFAULT_NO_SHOW_FEE_CENTS = 2500

def self_visit_clock(appt_key, sched, submitted_at):
    # The three-state clock a self-scoped (patient) cancel / reschedule reads
    # against the visit's own startsAt: "started" (submitted at or after
    # startsAt — the front desk records the outcome, noShow with its fee or
    # completed), "late" (inside the LATE_CANCEL_WINDOW_OFFSET window — a
    # cancel owes the no-show fee, a reschedule is refused), or "open"
    # (before the window — as free as the staff path). Same soft submittedAt
    # guard as enforce_started (caller-supplied, normalized to canonical UTC;
    # the stored startsAt is canonical UTC and rfc3339_add re-emits canonical
    # whole-second UTC, so both compares are lexical == chronological). Both
    # boundaries are inclusive on the stricter side: submitted AT startsAt has
    # started, submitted AT the window's opening instant is late.
    if sched == None or sched.isDeleted or sched.data.get("startsAt") == None:
        fail("InvalidState: " + appt_key + ".schedule is missing startsAt; cannot read the visit clock")
    starts_at = sched.data.get("startsAt")
    submitted = time.rfc3339_utc(submitted_at)
    if submitted >= starts_at:
        return "started"
    if submitted >= time.rfc3339_add(starts_at, LATE_CANCEL_WINDOW_OFFSET):
        return "late"
    return "open"

def no_show_fee_cents(p):
    # The fee a noShow status carries: caller-supplied noShowFeeCents (must be
    # positive) or DEFAULT_NO_SHOW_FEE_CENTS when omitted. Shared by
    # SetAppointmentStatus and CorrectAppointmentStatus so every writer of a
    # noShow validates and defaults identically; stored on .status for
    # clinic-ledger's clinicNoShowSettlement lens to post the charge.
    fee_cents = optional_number(p, "noShowFeeCents")
    if fee_cents == None:
        return DEFAULT_NO_SHOW_FEE_CENTS
    if fee_cents <= 0:
        fail("InvalidArgument: noShowFeeCents: must be a positive number")
    return fee_cents

def optional_bool(p, name):
    # Default False (absent / null / non-bool → False).
    if not hasattr(p, name):
        return False
    v = getattr(p, name)
    if v == None or type(v) != type(True):
        return False
    return v

def normalize_follow_up_date(s):
    # The clinic FE captures followUpDate as an HTML <input type=date> → a
    # date-only "YYYY-MM-DD". A downstream follow-up reminder's @at timer needs a
    # full RFC3339 instant (Weaver's temporal lane parses the lens freshUntil as
    # RFC3339, and time.rfc3339_utc itself rejects a bare date), so a date-only
    # value is anchored to 09:00:00Z — "the morning of" the follow-up date. A caller
    # that already supplies a full RFC3339 instant is normalized to canonical UTC so
    # the stored followUpDate is byte-stable for the remindedFor <> followUpDate
    # convergence compare. Storing a full instant is backward-compatible: the FE
    # slices followUpDate to its first 10 chars (the date) everywhere it renders it.
    if "T" not in s and len(s) == 10:
        s = s + "T09:00:00Z"
    return time.rfc3339_utc(s)

APPOINTMENT_STATUSES = ["scheduled", "confirmed", "checkedIn", "completed", "cancelled", "noShow"]
TERMINAL_STATUSES = ["cancelled", "completed", "noShow"]

# The bound on each of RecordEncounter's three clinical texts, in BYTES
# (Starlark len() on a string counts bytes; the descriptor's maxLength is the
# same number in characters, so a multi-byte character is charged more than
# once here). It is what sizes the history cap below.
ENCOUNTER_FIELD_MAX_BYTES = 4000

# The most superseded versions one visit's clinical record carries: each text
# amendment appends the text it replaces to .encounter's superseded list, and
# the list lives inside the one encrypted aspect, so the whole plaintext must
# stay inside one NATS message. Byte arithmetic: a version is at most three
# ENCOUNTER_FIELD_MAX_BYTES texts, 12,000 B; the current text plus 40
# superseded versions is 41 × 12,000 B ≈ 492 KB of plaintext (JSON keys and
# recordedAt stamps are noise beside it); step 6.5's envelope base64-encodes
# the ciphertext (×4/3, ≈ ×1.34 with the JSON around it) ≈ 660 KB — under
# NATS's 1 MiB max_payload with room for the batch's other mutations, the
# tracker and the outbox aspect. Past the bound the op refuses AmendmentLimit.
MAX_ENCOUNTER_AMENDMENTS = 40

GRID_MINUTES_STR = ["00", "15", "30", "45"]
GRID_STEP = "15m"
MAX_SLOT_CELLS = 96  # 24h of 15-minute cells -- a generous backstop, not an expected ceiling

def required_status(p):
    s = required_string(p, "status")
    if s not in APPOINTMENT_STATUSES:
        fail("InvalidArgument: status: must be one of scheduled, confirmed, checkedIn, completed, cancelled, noShow; got " + s)
    return s

def enforce_grid(starts_at, ends_at):
    # The clinic's booking grid is a mandatory 15-minute cadence (product decision,
    # Andrew 2026-07-02): every legal appointment boundary must sit on a cell edge, so
    # slot_cells' discretization below is lossless. Canonical whole-second UTC is
    # fixed-width (YYYY-MM-DDTHH:MM:SSZ, 20 chars), so the minute/second fields are
    # sliced directly rather than adding a new time builtin.
    for label, t in [("startsAt", starts_at), ("endsAt", ends_at)]:
        if len(t) != 20:
            fail("SlotGridViolation: " + label + ": must be a canonical whole-second UTC instant; got " + t)
        if t[17:19] != "00" or t[14:16] not in GRID_MINUTES_STR:
            fail("SlotGridViolation: " + label + " must align to the clinic's 15-minute booking grid (:00/:15/:30/:45); got " + t)

def slot_cells(starts_at, ends_at):
    # Enumerate the half-open [starts_at, ends_at) interval's covered 15-minute grid
    # cells (Capability-KV §06 — the op's own Starlark logic). Both endpoints are
    # grid-aligned (enforce_grid), so every cell boundary is exact. Starlark has no
    # while-loop, so a bounded for-range + a fail-closed "still more" check enumerates
    # the whole set without ever silently truncating (MAX_SLOT_CELLS is a generous
    # 24h backstop, not an expected ceiling).
    cells = []
    cur = starts_at
    for _i in range(MAX_SLOT_CELLS + 1):
        if not (cur < ends_at):
            return cells
        cells.append(cur)
        cur = time.rfc3339_add(cur, GRID_STEP)
    fail("AppointmentTooLong: appointment spans more than " + str(MAX_SLOT_CELLS) + " 15-minute slots (24h); shorten the interval")

def slot_cellcode(cell_start):
    # A localName-legal ([a-z][a-zA-Z0-9]*) encoding of a cell's canonical UTC start —
    # deterministic and identical across every writer competing for the same cell.
    return cell_start.replace("-", "").replace(":", "").lower()

def claim_cell(hub, cellcode, cls, conflict_code, who):
    key = hub + ".slot" + cellcode
    # kv.Read here is LAZY (§2.5 idiom, same as enforce_hours/enforce_time_off) — it
    # only decides which mutation verb to emit (create / CAS-revive / reject); it is
    # NOT itself the safety property. The safety property is the atomic batch's
    # CreateOnly / expectedRevision conditioning at commit: two concurrent claims for
    # the same cell both read it absent and both emit op:create, but CreateOnly on a
    # key with revision 0 commits exactly once — the loser's whole batch rejects
    # (RevisionConflict), the Processor retries, and the retry's kv.Read now sees the
    # winner's live cell and fails closed.
    # read-posture: (d) optionalReads — derived server-side by this script's
    # own derive_reads(op) for CreateAppointment/RescheduleAppointment.
    existing = kv.Read(key)
    if existing != None and not existing.isDeleted:
        fail(conflict_code + ": " + who + " " + hub + " slot " + cellcode + " is already booked")
    if existing != None and existing.isDeleted:
        return make_aspect_upsert_occ(hub, "slot" + cellcode, cls, {}, existing.revision)
    return make_aspect(hub, "slot" + cellcode, cls, {})

def self_day_localname(starts_at, provider_id):
    # The localName of a patient's self-booking day claim: "selfday" + the UTC
    # calendar day of the visit's startsAt as 8 digits + the provider's NanoID.
    # starts_at is canonical whole-second UTC (time.rfc3339_utc, fixed-width
    # YYYY-MM-DDTHH:MM:SSZ), so the day is the first 10 chars with the dashes
    # dropped; the NanoID alphabet is [A-Za-z1-9], so the whole localName is
    # legal ([a-z][a-zA-Z0-9]*). Deterministic and identical across every
    # writer competing for the same (patient, provider, day).
    return "selfday" + starts_at[0:10].replace("-", "") + provider_id

def claim_self_day(patient, starts_at, provider_id):
    # One open self-booked visit per provider per UTC day: the claim is a pure
    # existence marker on the patient hub, and — exactly as claim_cell — the
    # kv.Read here only decides which mutation verb to emit (create / OCC-revive /
    # reject); the safety property is the batch's CreateOnly / expectedRevision
    # conditioning at commit, so two concurrent self bookings for the same day
    # commit exactly once. Only the consumer scope=self path ever calls this
    # (CreateAppointment's self branch; RescheduleAppointment moving a
    # selfBooked visit across days) — the desk's bookings never claim a day.
    local = self_day_localname(starts_at, provider_id)
    # read-posture: (d) optionalReads — derived server-side by this script's
    # own derive_reads(op) for CreateAppointment/RescheduleAppointment.
    existing = kv.Read(patient + "." + local)
    if existing != None and not existing.isDeleted:
        fail("SelfBookingLimit: patient " + patient + " already holds an open self-booked visit with provider vtx.provider." + provider_id + " on " + starts_at[0:10] + "; cancel it first or pick another day (the front desk can book more)")
    if existing != None and existing.isDeleted:
        return make_aspect_upsert_occ(patient, local, "patientSelfDayClaim", {}, existing.revision)
    return make_aspect(patient, local, "patientSelfDayClaim", {})

def require_matching_provider(appt_id, provider):
    # Validates the caller-supplied provider is THIS appointment's actual provider by
    # reading the deterministic withProvider link (kv.Read, §2.5) — a live link proves
    # the relationship. Used wherever an op must recompute the appointment's cell set
    # (Reschedule / terminal SetAppointmentStatus / TombstoneAppointment) without a
    # stored back-reference (Contract #1: no relationship-as-key-list in aspect data).
    _, provider_id = parts_of(provider, "provider", "provider")
    with_provider_lnk = "lnk.appointment." + appt_id + ".withProvider.provider." + provider_id
    # read-posture: (a) declared in contextHint.reads by every caller's dispatcher —
    # RescheduleAppointment (cmd/clinic-app/web/app.js submitReschedule),
    # SetAppointmentStatus's terminal branch (setStatus), TombstoneAppointment
    # (packages/clinic-domain/integration_test.go clSubmit calls, its only caller)
    wp = kv.Read(with_provider_lnk)
    if wp == None or wp.isDeleted:
        fail("WrongProvider: provider " + provider + " is not the provider of appointment vtx.appointment." + appt_id)
    return provider_id

def require_matching_patient(appt_id, patient):
    # Symmetric analog of require_matching_provider over the forPatient link.
    _, patient_id = parts_of(patient, "patient", "patient")
    for_patient_lnk = "lnk.appointment." + appt_id + ".forPatient.patient." + patient_id
    # read-posture: (a) declared in contextHint.reads by every caller's dispatcher —
    # RescheduleAppointment (cmd/clinic-app/web/app.js submitReschedule),
    # SetAppointmentStatus's terminal branch (setStatus), TombstoneAppointment
    # (packages/clinic-domain/integration_test.go clSubmit calls, its only caller)
    fp = kv.Read(for_patient_lnk)
    if fp == None or fp.isDeleted:
        fail("WrongPatient: patient " + patient + " is not the patient of appointment vtx.appointment." + appt_id)
    return patient_id

def release_cells_mutations(provider, patient, sched):
    # Recompute the appointment's held cells from its OWN .schedule aspect (never a
    # stored back-reference) and tombstone both hubs' claim aspects for each — an
    # UNCONDITIONED tombstone (no expectedRevision): a stale-tombstone race here can
    # only ever free a cell a step early, never silently keep two live claims open,
    # so it is not a correctness hole (design §2.6). A self-booked visit
    # (.schedule.selfBooked) also holds the patient's day claim for its provider
    # (claim_self_day); it is released here the same unconditioned way, so every
    # caller of this seam — SetAppointmentStatus's first terminal transition,
    # MarkPastDueNoShow, TombstoneAppointment — frees the day with the cells.
    # Returns [] if the schedule is missing/malformed (defensive — should not
    # happen for a live appointment).
    if sched == None or sched.isDeleted:
        return []
    s_starts = sched.data.get("startsAt")
    s_ends = sched.data.get("endsAt")
    if s_starts == None or s_ends == None:
        return []
    out = []
    for c in slot_cells(s_starts, s_ends):
        cc = slot_cellcode(c)
        out.append(make_tombstone(provider + ".slot" + cc))
        out.append(make_tombstone(patient + ".slot" + cc))
    if sched.data.get("selfBooked"):
        _, provider_id = parts_of(provider, "provider", "provider")
        out.append(make_tombstone(patient + "." + self_day_localname(s_starts, provider_id)))
    return out

def valid_vertex_key(key, want_type):
    # Lenient key-shape check for a pre-pass that must never fault (objects-base's
    # derive_reads sets this precedent): a malformed/wrong-type key derives
    # nothing rather than raising, leaving execute()'s own parts_of to fault the
    # real InvalidArgument. Returns (True, id) or (False, None).
    if key == None or type(key) != type(""):
        return False, None
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx" or parts[1] != want_type or parts[2] == "":
        return False, None
    return True, parts[2]

def derive_reads(op):
    # Contract #2 §2.5 class (g), for CreateAppointment, RescheduleAppointment
    # and TombstoneAppointment. CreateAppointment/RescheduleAppointment's
    # providerSlotClaim/patientSlotClaim cells are entirely a function of the
    # payload (provider/patient/startsAt/endsAt — both required on both ops,
    # unlike wellness's ReassignSession, which this pattern deliberately does
    # NOT extend to: see wellness-domain/ddls.go's derive_reads doc comment),
    # so the caller no longer has to declare them (cmd/clinic-app/web/app.js's
    # book-submit and reschedule-submit paths stop computing them in the same
    # change — slotClaimKeys/slotCells/slotCellCode themselves stay, still used
    # by the unrelated cross-app CreateBooking submit). Mirrors this script's
    # own slot_cells/slot_cellcode exactly.
    #
    # The patient/provider ROOTS ride the same declaration on CreateAppointment:
    # require_live_typed(state, key, ...) below decides UnknownEndpoint by
    # testing key not in state, which cannot tell "genuinely absent" from "never
    # declared or derived" apart, so an undeclared submitter would see a live
    # endpoint refused as unknown.
    #
    # RescheduleAppointment's OLD cells — and a self-booked visit's OLD day
    # claim — need no declaration at all: the script releases them via an
    # unconditioned tombstone (release_cells_mutations / the cross-day move
    # in execute()), never a kv.Read. Its appointment root, .status, .schedule and the
    # withProvider/forPatient links it re-validates ride this declaration too —
    # each is a pure function of payload.appointmentKey/provider/patient, and
    # .schedule's own upsert (execute(), below) is a bare update auto-
    # conditioned on the step-4 hydrated revision only for a key the operation
    # declared (Contract #3 §3.2): an undeclared submitter would get a live read
    # and an unconditioned write, so two concurrent reschedules could each
    # commit against the same prior schedule. Only the NEW span's cells ever
    # reach claim_cell's kv.Read, so declaring the full new-span set (a superset
    # of to_claim) is exact, not merely safe.
    #
    # The patient's self-booking day claim for the new startsAt's UTC day +
    # provider (claim_self_day) rides the same declaration on both ops. Only
    # the consumer scope=self path ever reads it (CreateAppointment's self
    # branch; RescheduleAppointment moving a selfBooked visit across days), so
    # for the desk path it is a superset — a declared key the script never
    # reads, harmless.
    #
    # TombstoneAppointment reads only keys that are a pure function of
    # payload.appointmentKey/provider/patient: the appointment root, its
    # .status (terminal ⇒ the cells and day claim were already released and
    # must not be released again) and .schedule (the cell set to release), and
    # the withProvider/forPatient links it validates. Its releases are
    # unconditioned tombstones, never reads.
    ot = op.operationType
    if ot != "CreateAppointment" and ot != "RescheduleAppointment" and ot != "TombstoneAppointment":
        return {}
    p = op.payload
    # optional_string, never required_string: a malformed/empty/whitespace-only
    # field derives nothing rather than faulting the pre-pass — execute()'s own
    # required_string still raises the real InvalidArgument (objects-base's
    # derive_reads sets this precedent).
    provider = optional_string(p, "provider")
    patient = optional_string(p, "patient")
    starts_at_raw = optional_string(p, "startsAt")
    ends_at_raw = optional_string(p, "endsAt")

    keys = []
    provider_ok, provider_id = valid_vertex_key(provider, "provider")
    if provider_ok:
        keys.append(provider)
    patient_ok, patient_id = valid_vertex_key(patient, "patient")
    if patient_ok:
        keys.append(patient)

    if ot == "RescheduleAppointment" or ot == "TombstoneAppointment":
        appt_key = optional_string(p, "appointmentKey")
        appt_ok, appt_id = valid_vertex_key(appt_key, "appointment")
        if appt_ok:
            keys.append(appt_key)
            keys.append(appt_key + ".status")
            keys.append(appt_key + ".schedule")
            if provider_ok:
                keys.append("lnk.appointment." + appt_id + ".withProvider.provider." + provider_id)
            if patient_ok:
                keys.append("lnk.appointment." + appt_id + ".forPatient.patient." + patient_id)
    if ot == "TombstoneAppointment":
        if len(keys) == 0:
            return {}
        return {"optionalReads": keys}

    if provider == None or patient == None or starts_at_raw == None or ends_at_raw == None:
        if len(keys) == 0:
            return {}
        return {"optionalReads": keys}
    starts_at = time.rfc3339_utc(starts_at_raw)
    ends_at = time.rfc3339_utc(ends_at_raw)
    if not (starts_at < ends_at):
        if len(keys) == 0:
            return {}
        return {"optionalReads": keys}
    # slot_cells fails (AppointmentTooLong) past MAX_SLOT_CELLS -- bounding by
    # the identical 24h ceiling and returning {} defers that rejection to
    # execute()'s own clean error, mirroring wellness-domain's derive_reads.
    if ends_at > time.rfc3339_add(starts_at, "24h"):
        if len(keys) == 0:
            return {}
        return {"optionalReads": keys}
    cells = slot_cells(starts_at, ends_at)
    for c in cells:
        cc = slot_cellcode(c)
        keys.append(provider + ".slot" + cc)
        keys.append(patient + ".slot" + cc)
    if provider_ok and patient_ok:
        keys.append(patient + "." + self_day_localname(starts_at, provider_id))
    if len(keys) == 0:
        return {}
    return {"optionalReads": keys}

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateAppointment":
        patient = required_string(p, "patient")
        _, patient_id = parts_of(patient, "patient", "patient")
        provider = required_string(p, "provider")
        _, provider_id = parts_of(provider, "provider", "provider")
        # Both endpoints alive + the right class (endpoint validation at the op).
        require_live_typed(state, patient, "patient", "patient")
        require_live_typed(state, provider, "provider", "provider")

        # Staff-standing confinement (frontOfHouse's scope=any grant): a front-desk
        # actor may book only with a provider practising at a building it worksAt --
        # the same withProvider -> practicesAt confinement RescheduleAppointment /
        # SetAppointmentStatus apply, resolved here off the PAYLOAD provider
        # (validated alive + class=provider just above) since no appointment exists
        # yet. No-op for operator (actor_holds_operator) and for the consumer self-book
        # path (scope=self, so op.authTargetValidated holds), which the identifiedBy
        # probe below binds instead. No bound-provider branch: a provider role holds no
        # CreateAppointment grant (providers accept/reschedule their own
        # appointments, never originate them), so the third binder cannot apply here.
        # workplace-exempt: (ownership-bound) the identifiedBy probe below
        # requires the target to be this patient's own linked identity.
        if not (op.authTargetValidated or actor_holds_operator(op.actor)):
            enforce_workplace_confined(sites_for_provider(provider), "cannot book an appointment with provider " + provider)

        # Patient-self (consumer's scope=self grant only): step 3 authorizes
        # scope=self by checking authContext.target == actor (Contract #6), but
        # the op's endpoint is the PATIENT vertex, not an identity — step 3 never
        # sees the payload and has no notion of "this patient's identity" anyway.
        # The script closes the gap by requiring the target identity to be THIS
        # patient's linked identity (lnk.patient.<id>.identifiedBy.identity.<id>).
        # A consumer whose own identity is not the patient's identifiedBy link is
        # rejected, even if they satisfy step 3 by naming themselves as
        # authContext.target. Empty for the standing operator grant (scope=any
        # never sets authContext), so this check is a no-op there — operator
        # keeps booking on behalf of any patient, exactly as its own grant
        # (unconstrained by scope) allows.
        # authcontext-target: (ownership) the target must be this patient's own
        # identifiedBy identity, so a forged one only fails closed.
        if op.authContextTarget != "":
            _, target_identity_id = parts_of(op.authContextTarget, "authContextTarget", "identity")
            identified_by_lnk = "lnk.patient." + patient_id + ".identifiedBy.identity." + target_identity_id
            # The self-service caller (the only one that ever sets
            # authContextTarget) already knows both payload.patient and its own
            # authContext.target before submitting, so it computes this key
            # client-side and declares it — same as orchestration-base's
            # engine-dispatcher availability gate.
            # read-posture: (d) declared in contextHint.optionalReads by the
            # self-service caller
            identified_by = kv.Read(identified_by_lnk)
            if identified_by == None or identified_by.isDeleted:
                fail("AuthDenied: a patient may only book an appointment for themselves")

        # Normalize startsAt / endsAt to canonical whole-second UTC (time.rfc3339_utc
        # — a pure builtin, no clock read). This parse-validates the instants AND
        # makes the lexical RFC3339 compares the convergence lens relies on
        # (startsAt > $now, remindAt <= $now) sound for ANY caller offset / fractional
        # form, not only Z-suffixed input — the lease-signing normalization idiom.
        starts_at = time.rfc3339_utc(required_string(p, "startsAt"))
        ends_at = time.rfc3339_utc(required_string(p, "endsAt"))
        reason = optional_string(p, "reason")

        # A zero / negative-length booking is invalid (and would make every
        # half-open overlap test below vacuously false). Canonical-UTC strings
        # compare lexically == chronologically.
        if not (starts_at < ends_at):
            fail("InvalidArgument: endsAt: must be strictly after startsAt; got startsAt=" + starts_at + " endsAt=" + ends_at)

        # The booking must start in the future (relative to op.submittedAt) — a soft
        # past-time guard; see enforce_future.
        enforce_future(starts_at, op.submittedAt)

        # The clinic's mandatory 15-minute grid (SlotGridViolation if misaligned) —
        # checked before the availability/claim checks below so a malformed request
        # fails on the cheapest guard first.
        enforce_grid(starts_at, ends_at)

        # Provider availability windows (opt-in; OutsideHours if the booking falls
        # outside the provider's .hours). Checked before the slot-claim fan-out.
        enforce_hours(provider, starts_at, ends_at)

        # Provider date-specific time-off (opt-in; ProviderUnavailable if the booking
        # overlaps a blackout range) — the exception layer on top of the weekly hours.
        enforce_time_off(provider, starts_at, ends_at)

        # Discretize [startsAt, endsAt) into its covered 15-minute cells — lossless
        # under the grid constraint (AppointmentTooLong past 24h/96 cells).
        cells = slot_cells(starts_at, ends_at)

        appt_id = bare_nanoid_or_mint(p, "appointmentId")
        appt_key = "vtx.appointment." + appt_id

        # forPatient / withProvider: the appointment (later-arriving) is the
        # source, the pre-existing patient / provider is the target (Contract #1
        # §1.1). Sentences: "appointment forPatient patient", "appointment
        # withProvider provider".
        for_patient_lnk = "lnk.appointment." + appt_id + ".forPatient.patient." + patient_id
        with_provider_lnk = "lnk.appointment." + appt_id + ".withProvider.provider." + provider_id

        # remindAt = startsAt − 24h: the reminder deadline the clinic-reminders
        # convergence lens projects as freshUntil so the @at temporal lane fires a
        # reminder ~24h ahead. Precomputed at write time (time.rfc3339_add — a pure
        # builtin, no clock read) and emitted canonical UTC, so the lens needs no
        # date arithmetic — only the RFC3339 lexical compare. A booking < 24h out
        # yields a past remindAt → reminded immediately. rfc3339_add also parse-
        # validates startsAt as RFC3339 (fails closed on a malformed instant).
        sched = {"startsAt": starts_at, "endsAt": ends_at,
                 "remindAt": time.rfc3339_add(starts_at, "-24h")}
        if reason != None:
            sched["reason"] = reason

        # Who booked it is a fact recorded at the event: a visit booked on the
        # consumer scope=self path (the same selector the identifiedBy binding
        # above keys on — the self-service caller sets authContextTarget; a
        # scope=any caller that names the patient's own identity takes the
        # stricter branch too — never an exemption) records selfBooked = true
        # and holds the patient's one-open-self-booked-visit-per-provider-per-
        # day claim (claim_self_day; SelfBookingLimit if the day is already
        # held). The desk / operator path records nothing here and claims
        # nothing, so the desk is unrestricted. claim_self_day is called here,
        # ahead of the cell claims below, so a self booking that overlaps the
        # patient's own open visit with this provider reads SelfBookingLimit
        # rather than PatientDoubleBook — that precedence is by call order and
        # intended (the day rule is the broader statement of the same fact).
        self_day_mutation = None
        # authcontext-target: (selector) its presence selects the self-booking
        # limit — a stricter branch, never an exemption; ownership was proven
        # by the identifiedBy binding above.
        if op.authContextTarget != "":
            sched["selfBooked"] = True
            self_day_mutation = claim_self_day(patient, starts_at, provider_id)

        # Resident-visit confinement: an optional leaseAppKey, mirroring
        # wellness-domain's CreateBooking residentRate check, qualifies the
        # appointment for a residentVisit link (appointment→leaseapp) only
        # when ALL THREE hold: the leaseapp is alive, its .tenancy aspect is
        # present and not ended (the same signals residentRate uses — a
        # pending/declined application never qualifies, nor does a term that
        # has ended), and the leaseapp's
        # applicant identity is THIS patient's own identifiedBy identity. A
        # mismatch or absent lease falls through silently — leaseAppKey is a
        # confinement hint, never a hard requirement, exactly like
        # wellness's rate check.
        resident_visit_mutation = None
        lease_key = optional_string(p, "leaseAppKey")
        if lease_key != None:
            _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")
            # read-posture: (d) declared optionalReads by CreateAppointment's
            # dispatcher (resident-visit lookup; absent → falls through, never
            # a hard failure — mirrors wellness-domain's ddls.go).
            lease_doc = kv.Read(lease_key)
            lease_alive = lease_doc != None and not lease_doc.isDeleted
            # read-posture: (d) declared optionalReads by CreateAppointment's
            # dispatcher alongside the leaseapp itself — the first-approve signal
            # is absent on a pending/declined application, which falls through;
            # an endedAt (recorded by EndTenancy once the term ran out with no
            # signed renewal) means the patient has moved out, which falls
            # through the same way.
            tenancy_doc = kv.Read(lease_key + ".tenancy")
            tenancy_present = tenancy_doc != None and not tenancy_doc.isDeleted and tenancy_doc.data.get("endedAt") == None
            # Unlike wellness's CreateBooking (whose booker IS an identity,
            # supplied directly), CreateAppointment's caller supplies a
            # patient vertex, not an identity — the lease's applicant is
            # resolved via the sanctioned bounded op-time enumeration
            # (Contract #2 §2.5.1) rather than a declared key. A leaseapp
            # carries AT MOST one outbound applicationFor link
            # (lease-signing's one-applicant-per-application invariant), so
            # this is never a keyspace scan.
            # read-posture: (e) relation=applicationFor epoch=none (a lease
            # created concurrently with this appointment is not a race this
            # check needs to close — the silent fall-through is always safe)
            applicant_page, _ = kv.Links(lease_key, "applicationFor", "out", None, 1)
            applicant_id = None
            for lk in applicant_page:
                if not lk.isDeleted:
                    applicant_id = lk.targetVertex[len("vtx.identity."):]
            if lease_alive and tenancy_present and applicant_id != None:
                identified_by_lnk = "lnk.patient." + patient_id + ".identifiedBy.identity." + applicant_id
                # read-posture: (e) per-candidate follow-up read off the
                # enumeration above (data-derived key)
                idb_doc = kv.Read(identified_by_lnk)
                if idb_doc != None and not idb_doc.isDeleted:
                    resident_visit_lnk = "lnk.appointment." + appt_id + ".residentVisit.leaseapp." + lease_id
                    resident_visit_mutation = make_link(resident_visit_lnk, appt_key, lease_key, "residentVisit", "residentVisit", {})

        # Site association (Increment 2, optional): unlike leaseAppKey, a supplied
        # site is HARD-validated (require_site_membership fails closed) — the
        # provider must actually practicesAt the given site, or the whole op
        # rejects. Omitted → no atSite link, fully backward-compatible.
        site_link_mutation = None
        site_key = optional_string(p, "site")
        if site_key != None:
            require_site_membership(site_key, provider)
            _, site_id = parts_of(site_key, "site", "building")
            at_site_lnk = "lnk.appointment." + appt_id + ".atSite.building." + site_id
            site_link_mutation = make_link(at_site_lnk, appt_key, site_key, "atSite", "atSite", {})

        # Root data minimal (D5): {} on root. The patient / provider are links; the
        # schedule + status are aspects. One providerSlotClaim + one patientSlotClaim
        # per covered cell IS the double-book lock (write-path CreateOnly/
        # expectedRevision, not a read-time enumeration + serialization epoch).
        mutations = [
            make_vtx(appt_key, "appointment", {}),
            make_aspect(appt_key, "schedule", "appointmentSchedule", sched),
            make_aspect(appt_key, "status", "appointmentStatus", {"value": "scheduled"}),
            make_link(for_patient_lnk, appt_key, patient, "forPatient", "forPatient", {}),
            make_link(with_provider_lnk, appt_key, provider, "withProvider", "withProvider", {}),
        ]
        if resident_visit_mutation != None:
            mutations.append(resident_visit_mutation)
        if site_link_mutation != None:
            mutations.append(site_link_mutation)
        for c in cells:
            cc = slot_cellcode(c)
            mutations.append(claim_cell(provider, cc, "providerSlotClaim", "SlotConflict", "provider"))
        for c in cells:
            cc = slot_cellcode(c)
            mutations.append(claim_cell(patient, cc, "patientSlotClaim", "PatientDoubleBook", "patient"))
        if self_day_mutation != None:
            mutations.append(self_day_mutation)
        events = [{"class": "clinic.appointmentCreated",
                   "data": {"appointmentKey": appt_key, "patient": patient, "provider": provider}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    if ot == "RescheduleAppointment":
        appt_key = required_string(p, "appointmentKey")
        _, appt_id = parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        cls = class_of(state, appt_key)
        if cls != "appointment":
            fail("WrongClass: appointmentKey: " + appt_key + " has class " + str(cls) + ", required appointment")

        # Staff-standing confinement: the sites come from the appointment's OWN
        # withProvider -> practicesAt walk (falling back to its atSite link if
        # the provider is now tombstoned -- appointment_sites), never the
        # payload, so a caller cannot forge which building it is writing at.
        # No-op on the patient-self path, which the identifiedBy probe below
        # binds instead. The bound provider (identifiedBy-bound to THIS
        # appointment's own withProvider provider) is a THIRD standing binder,
        # alongside workplace: a provider need not also worksAt a building to
        # reschedule their own appointment.
        # workplace-exempt: (ownership-bound) the identifiedBy probe below
        # requires the target to be this appointment's patient's identity.
        if not (op.authTargetValidated or actor_holds_operator(op.actor)):
            standing_provider = appointment_provider(appt_id)
            if not actor_bound_to_appointment_provider(op.actor, standing_provider):
                enforce_workplace_confined(appointment_sites(appt_id, standing_provider), "cannot reschedule appointment " + appt_key)

        # The appointment's provider / patient — required so the move is conflict-
        # checked against each book exactly as CreateAppointment is, and so the OLD
        # cells can be released (without them a reschedule could silently move an
        # appointment INTO an occupied slot, or leave the vacated cells claimed
        # forever). Each is validated to be THIS appointment's actual provider /
        # patient via its withProvider / forPatient link (require_matching_provider /
        # require_matching_patient) — a live link proves the relationship AND that
        # the endpoint is real, once-validated (a wrong / fabricated endpoint would
        # otherwise recompute the wrong, e.g. empty, cell set and bypass the test).
        provider = required_string(p, "provider")
        patient = required_string(p, "patient")

        # Patient-self (consumer's scope=self grant only): the same gap-closing
        # check CreateAppointment's script runs (ddls.go ~1573) — step 3 only
        # proves authContext.target == actor, never that the target identity IS
        # this appointment's patient. Empty for the standing operator grant
        # (scope=any never sets authContext), so this is a no-op for staff.
        #
        # It binds on the patient the PAYLOAD names, which the forPatient check
        # below then proves is this appointment's — so ownership is established
        # without either endpoint check having run. That order is the guard: both
        # checks answer differently for a real endpoint than a wrong one, and a
        # clinic's provider roster is publicly listed, so a self-scoped caller who
        # could reach them on a stranger's appointment would learn which clinician
        # that stranger sees. Behind the binding, both only ever answer about an
        # appointment the caller already owns.
        # authcontext-target: (ownership) the target must be the identifiedBy
        # identity of the patient the payload names, so a forged one fails closed.
        if op.authContextTarget != "":
            _, self_patient_id = parts_of(patient, "patient", "patient")
            _, target_identity_id = parts_of(op.authContextTarget, "authContextTarget", "identity")
            identified_by_lnk = "lnk.patient." + self_patient_id + ".identifiedBy.identity." + target_identity_id
            # read-posture: (d) declared in contextHint.optionalReads by the
            # self-service caller
            identified_by = kv.Read(identified_by_lnk)
            if identified_by == None or identified_by.isDeleted:
                fail("AuthDenied: a patient may only reschedule their own appointment")

        patient_id = require_matching_patient(appt_id, patient)
        require_matching_provider(appt_id, provider)

        # New times: normalize to canonical whole-second UTC (parse-validates the
        # instants AND makes the convergence lens's lexical RFC3339 compares sound
        # for any caller offset) — exactly the CreateAppointment idiom.
        starts_at = time.rfc3339_utc(required_string(p, "startsAt"))
        ends_at = time.rfc3339_utc(required_string(p, "endsAt"))
        reason = optional_string(p, "reason")

        # A zero / negative-length booking is invalid (mirrors CreateAppointment; the
        # original reschedule lacked this guard).
        if not (starts_at < ends_at):
            fail("InvalidArgument: endsAt: must be strictly after startsAt; got startsAt=" + starts_at + " endsAt=" + ends_at)

        # The new time must start in the future (relative to op.submittedAt) — a soft
        # past-time guard; see enforce_future. A reschedule into the past is rejected
        # exactly as a create is.
        enforce_future(starts_at, op.submittedAt)

        # The clinic's mandatory 15-minute grid (SlotGridViolation if misaligned).
        enforce_grid(starts_at, ends_at)

        # Provider availability windows (opt-in; OutsideHours if the new time falls
        # outside the provider's .hours) — the move must land inside business hours too.
        enforce_hours(provider, starts_at, ends_at)

        # Provider date-specific time-off (opt-in; ProviderUnavailable if the new time
        # overlaps a blackout range) — the move must also avoid the provider's time-off.
        enforce_time_off(provider, starts_at, ends_at)

        # A terminal appointment (cancelled / completed / noShow) is never moved:
        # its cells were released at the terminal transition, so a move would
        # re-claim provider + patient cells for a visit nobody holds. Absence of
        # .status is the never-set (scheduled) case, so the read is absence-tolerant.
        # A concurrent cancel racing this read is the same unconditioned-upsert
        # posture SetAppointmentStatus's own TerminalStatus guard carries.
        # read-posture: (d) declared in contextHint.optionalReads by
        # RescheduleAppointment's dispatcher (cmd/clinic-app/web/app.js
        # submitReschedule)
        cur_status = kv.Read(appt_key + ".status")
        # A moved visit is on the schedule: a confirmed or checked-in one goes
        # back to scheduled — the confirmation was for the old date (the
        # re-armed reminder asks for it again), and a recorded arrival for a
        # visit moved to next week would exempt it from the past-due sweep, so
        # a later no-show would never be recorded or billed (status_reset says
        # so on the event; the note is dropped with the transition that
        # recorded it — neither carries a fee). A scheduled one is re-stamped
        # unchanged: every LIVE non-terminal .status is written, as a bare
        # update on a declared key conditioned on the hydrated revision, so a
        # patient's own confirm hydrated against the old schedule conflicts
        # under OCC instead of landing a confirmation on the moved visit.
        # An absent or tombstoned .status is left untouched.
        write_status = False
        status_reset = False
        if cur_status != None and not cur_status.isDeleted:
            cur_val = cur_status.data.get("value")
            if cur_val in TERMINAL_STATUSES:
                fail("TerminalStatus: appointment " + appt_key + " is " + str(cur_val) + " (terminal); cannot reschedule — cancelled/completed/noShow are final")
            write_status = True
            if cur_val in ("confirmed", "checkedIn"):
                status_reset = True

        # The appointment's CURRENT .schedule, read once for the two things below
        # that need it: the patient-self clock, and the release-old / claim-new
        # cell diff.
        # read-posture: (a) declared in contextHint.reads by RescheduleAppointment's
        # dispatcher (cmd/clinic-app/web/app.js submitReschedule)
        old_sched = kv.Read(appt_key + ".schedule")
        if old_sched == None or old_sched.isDeleted:
            fail("InvalidState: " + appt_key + ".schedule is missing; cannot reschedule")

        # Patient-self clock (self_visit_clock): once the visit has started the
        # front desk records its outcome, so the move is refused VisitStarted;
        # inside the late-cancel window the move is refused LateReschedule — a
        # free late move would be the side door around the late-cancel fee,
        # which lives on .status (a reschedule never writes it), so the patient
        # may cancel (and owe the fee) or call the desk. Staff move freely.
        # Ordered behind the ownership binding + endpoint checks above, so the
        # clock only ever answers about an appointment the caller owns.
        # authcontext-target: (selector) its presence selects the patient-self
        # clock — a stricter branch, never an exemption; ownership of the
        # appointment was proven by the identifiedBy binding above.
        if op.authContextTarget != "":
            clock = self_visit_clock(appt_key, old_sched, op.submittedAt)
            if clock == "started":
                fail("VisitStarted: appointment " + appt_key + " started at " + old_sched.data.get("startsAt") + " (submitted " + time.rfc3339_utc(op.submittedAt) + "); cannot reschedule once the visit has started — the front desk records the outcome")
            if clock == "late":
                fail("LateReschedule: appointment " + appt_key + " starts at " + old_sched.data.get("startsAt") + " (submitted " + time.rfc3339_utc(op.submittedAt) + "); within the 24-hour window a visit may be cancelled (the no-show fee applies) but not moved — call the front desk")

        # Release-old / claim-new, in the SAME atomic batch: the current .schedule
        # says which cells the appointment holds today; discretize both the old
        # and new intervals, and diff. Cells held by BOTH sets need no mutation —
        # they stay claimed straight through the move (no read/re-claim gap).
        old_starts = old_sched.data.get("startsAt")
        old_ends = old_sched.data.get("endsAt")
        if old_starts == None or old_ends == None:
            fail("InvalidState: " + appt_key + ".schedule is missing startsAt/endsAt; cannot reschedule")
        old_cells = slot_cells(old_starts, old_ends)
        new_cells = slot_cells(starts_at, ends_at)  # already grid-validated above

        to_release = [c for c in old_cells if c not in new_cells]
        to_claim = [c for c in new_cells if c not in old_cells]

        # Re-derive remindAt = startsAt − 24h so the clinic-reminders convergence
        # lens re-projects a fresh freshUntil and the @at temporal lane re-arms for
        # the NEW time (for a not-yet-sent reminder; the remindedFor term re-arms an
        # already-sent one).
        sched = {"startsAt": starts_at, "endsAt": ends_at,
                 "remindAt": time.rfc3339_add(starts_at, "-24h")}
        if reason != None:
            sched["reason"] = reason

        # selfBooked is a recorded fact about the booking event, carried
        # forward unchanged by every move (a desk move of a self-booked visit
        # keeps it self-booked; a self move of a desk-booked visit never makes
        # it self-booked). The day claim follows the visit: when a self-booked
        # visit moves to a different UTC calendar day, the old day's claim is
        # released (unconditioned tombstone, known-live like the old cells)
        # and the new day's is claimed (claim_self_day — SelfBookingLimit for
        # ANY mover, staff included, if the patient already holds that day
        # with this provider). A same-day move touches no claim.
        self_day_mutations = []
        if old_sched.data.get("selfBooked"):
            sched["selfBooked"] = True
            _, provider_id = parts_of(provider, "provider", "provider")
            if starts_at[0:10] != old_starts[0:10]:
                self_day_mutations.append(make_tombstone(patient + "." + self_day_localname(old_starts, provider_id)))
                self_day_mutations.append(claim_self_day(patient, starts_at, provider_id))

        # Unconditioned upsert of the WHOLE .schedule aspect (the caller round-trips
        # the reason; an omitted reason clears it; forPatient / withProvider links
        # untouched — the move keeps the same provider / patient; .status is
        # rewritten only by the reset above). Vacated
        # cells release via an UNCONDITIONED tombstone (known-live from old_cells, so
        # no read needed); newly-covered cells run through claim_cell exactly as
        # Create does (reject / CAS-revive / create). If ANY to_claim cell collides,
        # the WHOLE batch — including the to_release tombstones — is atomically
        # rejected, so a failed reschedule leaves the original booking's claims fully
        # intact (design §2.5).
        mutations = [make_aspect_upsert(appt_key, "schedule", "appointmentSchedule", sched)]
        if write_status:
            mutations.append(make_aspect_upsert(appt_key, "status", "appointmentStatus", {"value": "scheduled"}))
        for c in to_release:
            cc = slot_cellcode(c)
            mutations.append(make_tombstone(provider + ".slot" + cc))
            mutations.append(make_tombstone(patient + ".slot" + cc))
        for c in to_claim:
            cc = slot_cellcode(c)
            mutations.append(claim_cell(provider, cc, "providerSlotClaim", "SlotConflict", "provider"))
        for c in to_claim:
            cc = slot_cellcode(c)
            mutations.append(claim_cell(patient, cc, "patientSlotClaim", "PatientDoubleBook", "patient"))
        mutations = mutations + self_day_mutations
        event_data = {"appointmentKey": appt_key, "startsAt": starts_at, "endsAt": ends_at}
        if status_reset:
            event_data["statusReset"] = True
        events = [{"class": "clinic.appointmentRescheduled", "data": event_data}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    if ot == "SetAppointmentStatus":
        appt_key = required_string(p, "appointmentKey")
        _, appt_id = parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        cls = class_of(state, appt_key)
        if cls != "appointment":
            fail("WrongClass: appointmentKey: " + appt_key + " has class " + str(cls) + ", required appointment")

        # Staff-standing confinement: same withProvider -> practicesAt
        # derivation as RescheduleAppointment, including the atSite fallback
        # for a now-tombstoned provider (appointment_sites). Bound here rather
        # than on the terminal branch alone -- a non-terminal status write
        # (confirmed / checkedIn) is just as much a write into someone else's
        # building. The bound provider is a THIRD standing binder alongside
        # workplace, mirroring RescheduleAppointment.
        # workplace-exempt: (ownership-bound) the identifiedBy probe below
        # requires the target to be this appointment's patient's identity.
        if not (op.authTargetValidated or actor_holds_operator(op.actor)):
            standing_provider = appointment_provider(appt_id)
            if not actor_bound_to_appointment_provider(op.actor, standing_provider):
                enforce_workplace_confined(appointment_sites(appt_id, standing_provider), "cannot set status on appointment " + appt_key)

        status = required_status(p)

        # Patient-self (consumer's scope=self grant only): restricted to cancel
        # or confirm — a self-service patient may cancel or confirm their own
        # appointment but never mark checkedIn/completed/noShow (those stay
        # staff-only; this is a value restriction on TOP OF the identity
        # binding, since a self grant binds WHO but says nothing about WHICH
        # status). Checked, and identity bound, before the terminal/idempotent
        # branching below so a self-scoped caller must prove ownership even on
        # an idempotent re-cancel (empty for the standing operator grant —
        # scope=any never sets authContext). A self confirm is further gated
        # once the current status is known, below.
        # authcontext-target: (ownership) the target must be this appointment's
        # patient's identifiedBy identity, so a forged one only fails closed.
        if op.authContextTarget != "":
            if status not in ("cancelled", "confirmed"):
                fail("AuthDenied: a patient may only cancel or confirm their own appointment (status must be cancelled or confirmed)")
            self_patient = required_string(p, "patient")
            _, self_patient_id = parts_of(self_patient, "patient", "patient")
            _, target_identity_id = parts_of(op.authContextTarget, "authContextTarget", "identity")
            identified_by_lnk = "lnk.patient." + self_patient_id + ".identifiedBy.identity." + target_identity_id
            # read-posture: (d) declared in contextHint.optionalReads by the
            # self-service caller
            identified_by = kv.Read(identified_by_lnk)
            if identified_by == None or identified_by.isDeleted:
                fail("AuthDenied: a patient may only cancel or confirm their own appointment")
            # The binding proves the caller owns the patient they NAME; this
            # proves that patient is this appointment's. Ordered after the
            # binding because it answers differently for a real endpoint than a
            # wrong one — ahead of it, a self-scoped caller could walk a stranger's
            # appointment down to the patient it belongs to.
            require_matching_patient(appt_id, self_patient)

        # Terminal-status lifecycle guard: cancelled / completed / noShow are FINAL.
        # Re-setting the SAME terminal value is idempotent (re-run-safe under
        # at-least-once, and lets a noteless re-set clear a prior note — while a
        # cancelled re-set carries the late-cancel fee forward, below); changing a
        # terminal status to a DIFFERENT one is rejected — a finished / cancelled visit
        # must not silently revert (e.g. completed→scheduled, cancelled→completed). A
        # non-terminal current status (scheduled / confirmed / checkedIn) still moves
        # freely (including corrections). The current status is read lazily (kv.Read,
        # §2.5 idiom), NOT a declared / OCC read: this op is already an unconditioned
        # upsert with no cross-op serialization, so the guard matches its existing
        # single-op semantics (it closes the single-op invalid transition; concurrent
        # transitions race exactly as the upsert already did). Re-opening a terminal
        # appointment is a future explicit op, not a status flip.
        cur_val = None
        # read-posture: (d) declared in contextHint.optionalReads by
        # SetAppointmentStatus's dispatcher (cmd/clinic-app/web/app.js) — absence is
        # the legit first-set case (no status yet)
        cur_status = kv.Read(appt_key + ".status")
        if cur_status != None and not cur_status.isDeleted:
            cur_val = cur_status.data.get("value")
            if cur_val in TERMINAL_STATUSES and status != cur_val:
                fail("TerminalStatus: appointment " + appt_key + " is " + str(cur_val) + " (terminal); cannot transition to " + status + " — cancelled/completed/noShow are final")
        # A patient's own confirm, after the ownership binding above and the
        # current-status read. An already-confirmed visit is the EMPTY batch:
        # nothing is re-stamped (a staff-written note on .status stays as it
        # is) and no event is emitted — a re-confirm changes nothing, so it is
        # idempotent under at-least-once and reads no clock. primaryKey stays
        # OUT of the response (the write-footprint reply constraint). A FIRST
        # confirm is gated: a visit the desk has already checked in is not the
        # patient's to write over (the same posture the staff guard takes
        # toward another building's records), and a visit cannot be confirmed
        # once it has begun — self_visit_clock's "started"; "late" is fine,
        # confirming inside 24 h is exactly what the reminder invites. The
        # write is exactly {value: confirmed}: the payload's note is IGNORED
        # on this path (the audit note is a staff record — a patient's own
        # confirm never carries one). No fee, no cells move.
        # authcontext-target: (selector) its presence selects the patient-self
        # gates — a stricter branch, never an exemption; ownership was proven
        # by the identifiedBy binding + require_matching_patient above.
        if op.authContextTarget != "" and status == "confirmed":
            if cur_val == "confirmed":
                return {"mutations": [], "events": [], "response": {}}
            if cur_val == "checkedIn":
                fail("AuthDenied: appointment " + appt_key + " is checkedIn — the desk has already checked you in, so the visit cannot be confirmed over it")
            # read-posture: (a) declared in contextHint.reads by the self
            # dispatcher (cmd/clinic-app/web/app.js setStatus) — CreateAppointment
            # always writes .schedule, so its absence is a correctness error.
            self_sched = kv.Read(appt_key + ".schedule")
            if self_visit_clock(appt_key, self_sched, op.submittedAt) == "started":
                fail("VisitStarted: appointment " + appt_key + " started at " + self_sched.data.get("startsAt") + " (submitted " + time.rfc3339_utc(op.submittedAt) + "); a visit cannot be confirmed once it has begun — the front desk records the outcome")
            mutations = [make_aspect_upsert(appt_key, "status", "appointmentStatus", {"value": "confirmed"})]
            events = [{"class": "clinic.appointmentStatusSet",
                       "data": {"appointmentKey": appt_key, "status": "confirmed"}}]
            return {"mutations": mutations, "events": events,
                    "response": {"primaryKey": appt_key}}
        # Optional audit note (cancel / no-show reason for billing + records).
        # Stored on the .status aspect, distinct from the .schedule visit reason.
        # Omitted → the .status carries only {value} (an unconditioned upsert, so a
        # later transition without a note clears any prior note — intended: the note
        # belongs to the terminal cancel/no-show it was recorded with).
        status_data = {"value": status}
        note = optional_string(p, "note")
        if note != None:
            status_data["note"] = note
        # No-show fee (billing consequence for a missed visit) when transitioning
        # TO noShow: caller-supplied or the default (no_show_fee_cents) — same
        # unconditioned-upsert idiom as note above, stored on .status so the
        # clinicNoShowSettlement lens (clinic-ledger) can read it and post a
        # DebitAccount charge (clinic-ledger-design.md).
        if status == "noShow":
            status_data["noShowFeeCents"] = no_show_fee_cents(p)
        release = []
        # A FIRST terminal transition (non-terminal → terminal; a same-value re-set is
        # skipped — its cells were already released by the first transition) frees the
        # slot: recompute the held cells from .schedule and tombstone both hubs' claim
        # aspects (release_cells_mutations, design §2.6). provider/patient are
        # required here (not on non-terminal transitions) — there is no relationship
        # stored on the appointment to look them up by any other means (Contract #1: no
        # back-reference in aspect data), so the caller supplies them and they are
        # validated against the withProvider/forPatient links before use.
        if status in TERMINAL_STATUSES and status != cur_val:
            provider = required_string(p, "provider")
            require_matching_provider(appt_id, provider)
            patient = required_string(p, "patient")
            require_matching_patient(appt_id, patient)
            # The .schedule is read once here and serves the visit clocks
            # (enforce_started, self_visit_clock) and the cell release alike.
            # read-posture: (a) declared in contextHint.reads by SetAppointmentStatus's
            # dispatcher (cmd/clinic-app/web/app.js setStatus), only on the terminal branch
            sched = kv.Read(appt_key + ".schedule")
            enforce_started(appt_key, status, sched, op.submittedAt)
            # Patient-self clock on the first cancel (the only terminal value the
            # self path reaches): once the visit has started the front desk
            # records the outcome (noShow with its fee, or completed), so the
            # cancel is refused VisitStarted; inside the late-cancel window the
            # cancel lands but owes the no-show fee — the status carries
            # lateCancel + noShowFeeCents, which clinic-ledger's
            # clinicNoShowSettlement bills exactly as a staff-set noShow. A staff
            # cancel owes nothing (the clinic may be the one calling it off).
            # authcontext-target: (selector) its presence selects the patient-self
            # clock — a stricter branch, never an exemption; ownership was proven
            # by the identifiedBy binding + require_matching_patient above.
            if op.authContextTarget != "":
                clock = self_visit_clock(appt_key, sched, op.submittedAt)
                if clock == "started":
                    fail("VisitStarted: appointment " + appt_key + " started at " + sched.data.get("startsAt") + " (submitted " + time.rfc3339_utc(op.submittedAt) + "); the front desk records the outcome")
                if clock == "late":
                    status_data["lateCancel"] = True
                    status_data["noShowFeeCents"] = DEFAULT_NO_SHOW_FEE_CENTS
            release = release_cells_mutations(provider, patient, sched)
        elif status == "cancelled" and cur_val == "cancelled":
            # A same-value cancelled re-set (any caller) carries a late cancel's
            # lateCancel + noShowFeeCents forward from the current .status: the
            # unconditioned upsert would otherwise drop both and the ledger would
            # read the fee's absence as a waiver and reverse the charge. The note
            # keeps its clear-on-omit semantics. Waiving the fee is
            # CorrectAppointmentStatus(cancelled, note), whose own write carries
            # no fee.
            for carried in ["lateCancel", "noShowFeeCents"]:
                if cur_status.data.get(carried) != None:
                    status_data[carried] = cur_status.data.get(carried)
        mutations = [make_aspect_upsert(appt_key, "status", "appointmentStatus", status_data)] + release
        events = [{"class": "clinic.appointmentStatusSet",
                   "data": {"appointmentKey": appt_key, "status": status}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    if ot == "CorrectAppointmentStatus":
        # The explicit repair for a WRONG terminal call — the move
        # SetAppointmentStatus's TerminalStatus guard refuses (an auto no-show on a
        # patient who was actually seen). Scope is deliberately terminal→terminal
        # ONLY: re-opening to scheduled/confirmed/checkedIn would have to re-claim
        # the released slot cells against whatever has been booked since, a
        # different and larger feature. Within the terminal set no cell moves at
        # all — the FIRST terminal transition already released them — so this op
        # takes no provider/patient and writes only the .status aspect.
        appt_key = required_string(p, "appointmentKey")
        _, appt_id = parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        cls = class_of(state, appt_key)
        if cls != "appointment":
            fail("WrongClass: appointmentKey: " + appt_key + " has class " + str(cls) + ", required appointment")

        # Same staff-standing confinement SetAppointmentStatus applies to its
        # non-self path (operator / the appointment's own bound provider /
        # workplace), so a correction is a write into someone else's building on
        # exactly the terms an ordinary status write is.
        # workplace-exempt: (no-validated-path) this op grants no scope=self and
        # mints no task (operator/frontOfHouse/provider "any" only), so
        # op.authTargetValidated is never legitimately true here and only an
        # operator reaches the exemption — SetAppointmentSite's reasoning exactly.
        if not (op.authTargetValidated or actor_holds_operator(op.actor)):
            standing_provider = appointment_provider(appt_id)
            if not actor_bound_to_appointment_provider(op.actor, standing_provider):
                enforce_workplace_confined(appointment_sites(appt_id, standing_provider), "cannot correct status on appointment " + appt_key)

        status = required_status(p)
        if status not in TERMINAL_STATUSES:
            fail("InvalidArgument: status: CorrectAppointmentStatus only transitions between terminal statuses (cancelled/completed/noShow) — use SetAppointmentStatus for the first terminal transition")

        cur_val = None
        # read-posture: (d) declared in contextHint.optionalReads by
        # CorrectAppointmentStatus's dispatcher (cmd/clinic-app/web/app.js) —
        # absence means the appointment never reached a terminal status, which is
        # this op's own NotTerminal rejection below, not a correctness error.
        cur_status = kv.Read(appt_key + ".status")
        if cur_status != None and not cur_status.isDeleted:
            cur_val = cur_status.data.get("value")
        if cur_val not in TERMINAL_STATUSES:
            fail("NotTerminal: appointment " + appt_key + " is not in a terminal status; use SetAppointmentStatus for the first terminal transition")

        # The note is MANDATORY here, unlike SetAppointmentStatus's optional one:
        # this rewrites a record already treated as final, so the reason is part
        # of the write, not an extra.
        note = optional_string(p, "note")
        if note == None:
            fail("InvalidArgument: note: required for a status correction")

        # read-posture: (a) declared in contextHint.reads by CorrectAppointmentStatus's
        # dispatcher (the descriptor's Dispatch.Reads, cmd/clinic-app's catalog form)
        # — the visit's clock for a completed / noShow correction (enforce_started).
        enforce_started(appt_key, status, kv.Read(appt_key + ".schedule"), op.submittedAt)

        # correctedFrom keeps the overwritten value on the aspect itself — the
        # only trace of the wrong call once the upsert lands. A same-value
        # correction stays accepted (the re-runnable posture SetAppointmentStatus's
        # own terminal idempotency uses) and simply records the value unchanged.
        status_data = {"value": status, "note": note, "correctedFrom": cur_val}
        # A correction onto noShow carries the fee exactly as the first
        # transition does (no_show_fee_cents: caller-supplied or the default),
        # so clinic-ledger's clinicNoShowSettlement bills it. A correction onto
        # completed / cancelled writes no fee — that absence is the waiver: the
        # same lens reverses a charge whose status no longer carries one.
        if status == "noShow":
            status_data["noShowFeeCents"] = no_show_fee_cents(p)
        mutations = [make_aspect_upsert(appt_key, "status", "appointmentStatus", status_data)]
        events = [{"class": "clinic.appointmentStatusCorrected",
                   "data": {"appointmentKey": appt_key, "from": cur_val, "to": status}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    if ot == "MarkPastDueNoShow":
        # The Weaver-dispatched counterpart to SetAppointmentStatus(noShow) —
        # clinic-reminders' pastDueAppointments target's ONLY caller, once a
        # non-terminal appointment's .schedule.endsAt passes with no staff status
        # update. Deliberately its own operationType rather than a directOp against
        # SetAppointmentStatus itself: that op requires provider + patient as
        # CALLER-SUPPLIED params validated against the withProvider/forPatient LINKS
        # (require_matching_provider/patient), and Weaver's directOp dispatch can only
        # template row.<column> / row.<column>.<aspect> keys into ContextHint.Reads
        # (Contract #10 §10.8) — it cannot express that link read. This op instead
        # resolves provider/patient LIVE (appointment_provider/appointment_patient,
        # the same bounded read-posture (e) already used for the standing
        # provider-binding guard above), since it has no human caller to supply them.
        appt_key = required_string(p, "appointmentKey")
        _, appt_id = parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        cls = class_of(state, appt_key)
        if cls != "appointment":
            fail("WrongClass: appointmentKey: " + appt_key + " has class " + str(cls) + ", required appointment")

        # Idempotent no-op if the appointment already reached a terminal status by
        # dispatch time — a legitimate race under Weaver's at-least-once dispatch
        # (e.g. staff completed/cancelled it between the lens's last projection and
        # this dispatch): never clobber an already-final outcome with noShow.
        cur_val = None
        # read-posture: (d) declared in contextHint.optionalReads by this op's
        # only caller (the pastDueAppointments playbook) — a never-set status is
        # exactly the visit the sweep closes, so absence is a branch, not an
        # error.
        cur_status = kv.Read(appt_key + ".status")
        if cur_status != None and not cur_status.isDeleted:
            cur_val = cur_status.data.get("value")
        if cur_val in TERMINAL_STATUSES:
            # No mutation this pass, so primaryKey must stay OUT of response — the
            # write-footprint reply constraint (commit_path.go) rejects a
            # script-named primaryKey with no matching mutation.
            return {"mutations": [], "events": [], "response": {}}

        if cur_val == "checkedIn":
            # Recorded arrival is never swept to no-show: the desk has already
            # seen this patient, so an un-closed visit is open work for the
            # desk, not a documentation lapse the patient caused. Also guards
            # the same race the terminal no-op above guards: a dispatch that
            # lands after the desk's own late check-in must not clobber it.
            # The desk closes it via SetAppointmentStatus /
            # RescheduleAppointment once resolved.
            return {"mutations": [], "events": [], "response": {}}

        provider = appointment_provider(appt_id)
        patient = appointment_patient(appt_id)
        if provider == None or patient == None:
            fail("MissingBinding: appointment " + appt_key + " has no bound provider/patient; cannot auto no-show")

        # read-posture: (a) declared in contextHint.reads by the pastDueAppointments
        # playbook — same key SetAppointmentStatus's terminal branch reads, always
        # needed here (MarkPastDueNoShow is always terminal).
        schedule = kv.Read(appt_key + ".schedule")

        # A provider time-off range covering this visit means the provider was
        # never available to see the patient — the missed visit is not a
        # documentation lapse the patient caused, and the sweep must not
        # silently blame them for it. No-op cleanly (never guessing) rather
        # than marking noShow, the same posture BackfillAppointmentSite takes
        # for a gap it cannot resolve on its own: the gap stays open (Weaver
        # keeps retrying, harmlessly) and front desk closes it via the normal
        # SetAppointmentStatus / RescheduleAppointment ops once they've
        # rebooked or otherwise resolved the visit.
        if schedule != None and not schedule.isDeleted:
            starts_at = schedule.data.get("startsAt")
            ends_at = schedule.data.get("endsAt")
            # The visit the sweep is closing must actually have ended: a
            # dispatch that raced a RescheduleAppointment reads the MOVED
            # .schedule here (it is hydrated), and its endsAt is now ahead of
            # this dispatch's submittedAt — the lapse it was armed on belongs
            # to the old date. No-op; the lens re-arms at the new endsAt and
            # dispatches again if that one lapses. Same soft, inclusive
            # boundary the other clocks read (canonical whole-second UTC on
            # both sides, so lexical order is chronological).
            if ends_at != None and ends_at > time.rfc3339_utc(op.submittedAt):
                return {"mutations": [], "events": [], "response": {}}
            if starts_at != None and ends_at != None and time_off_overlap(provider, starts_at, ends_at) != None:
                return {"mutations": [], "events": [], "response": {}}

        # No noShowFeeCents here, deliberately: the automated sweep marks a
        # documentation lapse, not a missed visit (PO ruling,
        # verticals.md), so only a staff-observed SetAppointmentStatus(noShow)
        # bills. clinicNoShowSettlement's gaps both require feeCents > 0, so a
        # None value here simply never converges a charge — no downstream
        # change needed.
        status_data = {"value": "noShow", "note": "Auto no-show: appointment ended without a status update"}
        mutations = [make_aspect_upsert(appt_key, "status", "appointmentStatus", status_data)]
        mutations = mutations + release_cells_mutations(provider, patient, schedule)
        events = [{"class": "clinic.appointmentStatusSet",
                   "data": {"appointmentKey": appt_key, "status": "noShow", "auto": True}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    if ot == "BackfillAppointmentSite":
        # Orchestration-internal: clinicSiteBackfill's missing_site gap (lenses.go)
        # dispatches this for a LIVE appointment carrying no atSite link — the
        # pre-existing corpus CreateAppointment minted before a site was ever
        # supplied. Mirrors CreateAppointment's own site-writing branch, never a
        # read-side workaround (the BackfillTabStaleAt idiom, cafe-domain).
        appt_key = required_string(p, "appointmentKey")
        _, appt_id = parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        cls = class_of(state, appt_key)
        if cls != "appointment":
            fail("WrongClass: appointmentKey: " + appt_key + " has class " + str(cls) + ", required appointment")

        # Already has a live atSite link — another dispatch already won, or a
        # redelivery. No-op cleanly rather than reject, mirroring
        # BackfillTabStaleAt's own already-present no-op.
        # read-posture: (e) relation=atSite epoch=none -- an appointment carries
        # at most one atSite link (CreateAppointment/this op each write it at
        # most once), so this is a single bounded enumeration off the
        # appointment key already proven alive above, never a keyspace scan.
        apage, _ = kv.Links(appt_key, "atSite", "out", None, 1)
        for lk in apage:
            if not lk.isDeleted:
                return {"mutations": [], "events": [], "response": {}}

        provider = appointment_provider(appt_id)
        # Only LIVE sites count. TombstoneLocation (location-domain) cascades
        # onto no practicesAt link, so a provider moved off a decommissioned
        # site keeps a live link to a dead building; sites_for_provider hands
        # that link back because the confinement callers re-prove each
        # building themselves (worksAt_covers). Here the count IS the decision,
        # so the dead site must drop out before it is taken -- otherwise a
        # provider at one live site reads as two and every appointment of
        # theirs stays missing_site forever, while the providerSites read model
        # (and the booking UI's auto-fill on it) shows the one site. vertex_live
        # is the same per-candidate (e) re-proof worksAt_covers performs, and
        # the same building screen clinic-reminders' sites_for_provider applies
        # for its BackfillVisitSeriesSite pick (visitseries.go).
        sites = [s for s in sites_for_provider(provider) if vertex_live(s)]
        if len(sites) != 1:
            # Zero live sites (an unassigned or dead provider, or one whose only
            # sites are decommissioned) or two-or-more (which one this
            # appointment belongs to is genuinely ambiguous) — never guess, the
            # same sole-site fallback semantics the booking UI's own
            # client-side site auto-fill applies (submitBook, app.js). This
            # appointment stays missing_site forever, which is harmless: the
            # gap is idempotently re-dispatched and cleanly no-ops every time,
            # exactly the non-convergence posture cafe-domain's own
            # BackfillTabStaleAt/missing_staleat gap relies on for a case that
            # can never resolve on its own (lenses.go).
            return {"mutations": [], "events": [], "response": {}}

        site_key = sites[0]
        _, site_id = parts_of(site_key, "site", "building")
        at_site_lnk = "lnk.appointment." + appt_id + ".atSite.building." + site_id
        mutations = [make_link(at_site_lnk, appt_key, site_key, "atSite", "atSite", {})]
        events = [{"class": "clinic.appointmentSiteBackfilled", "data": {"appointmentKey": appt_key, "site": site_key}}]
        # primaryKey names the LINK key itself (the AssignProviderSite idiom,
        # site.go) rather than the appointment vertex: the write-footprint
        # reply constraint (commit_path.go) accepts a mutation's own key or its
        # 3-segment vertex root, and this op's only mutation is a link, which
        # has no vertex root of its own.
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": at_site_lnk}}

    if ot == "SetAppointmentSite":
        # The STAFF/provider manual counterpart to BackfillAppointmentSite's
        # auto-remediation. It closes the "2+ sites" ambiguity directly (a
        # human picks the right one); a provider at ZERO sites or now
        # tombstoned still needs AssignProviderSite run first (this op's own
        # require_site_membership below hard-requires a live practicesAt
        # link, same as CreateAppointment's site branch) — it is a manual
        # override for CHOOSING among a provider's live sites, not a way to
        # invent a practicesAt relationship that doesn't exist. Confinement
        # mirrors RescheduleAppointment exactly (operator / workplace / the
        # appointment's own bound provider), not BackfillAppointmentSite
        # (Weaver-dispatched-only, no human caller to confine).
        appt_key = required_string(p, "appointmentKey")
        _, appt_id = parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        cls = class_of(state, appt_key)
        if cls != "appointment":
            fail("WrongClass: appointmentKey: " + appt_key + " has class " + str(cls) + ", required appointment")

        provider = appointment_provider(appt_id)

        # workplace-exempt: (no-validated-path) SetAppointmentSite grants no
        # scope=self and mints no task (operator/frontOfHouse/provider "any"
        # grant only), so op.authTargetValidated is never legitimately true
        # here and only an operator reaches the exemption — identical
        # reasoning to BackfillAppointmentSite's own standing confinement,
        # RescheduleAppointment's block above being the scope=self exception.
        if not (op.authTargetValidated or actor_holds_operator(op.actor)):
            if not actor_bound_to_appointment_provider(op.actor, provider):
                enforce_workplace_confined(appointment_sites(appt_id, provider), "cannot set site on appointment " + appt_key)

        # Already has a live atSite link — reassignment is out of scope (the
        # gap this closes is "27 appointments carry NO site", never "the site
        # is wrong"); no-op cleanly rather than reject, mirroring
        # BackfillAppointmentSite's own already-present no-op.
        # read-posture: (e) relation=atSite epoch=none -- an appointment
        # carries at most one atSite link (CreateAppointment/
        # BackfillAppointmentSite/this op each write it at most once), so this
        # is a single bounded enumeration off the appointment key already
        # proven alive above, never a keyspace scan.
        apage, _ = kv.Links(appt_key, "atSite", "out", None, 1)
        for lk in apage:
            if not lk.isDeleted:
                return {"mutations": [], "events": [], "response": {}}

        if provider == None:
            fail("MissingBinding: appointment " + appt_key + " has no bound provider; cannot set site")
        site_key = required_string(p, "site")
        require_site_membership(site_key, provider)
        _, site_id = parts_of(site_key, "site", "building")
        at_site_lnk = "lnk.appointment." + appt_id + ".atSite.building." + site_id
        # Two concurrent SetAppointmentSite calls choosing DIFFERENT sites for
        # the same still-site-less appointment both pass the no-live-atSite
        # check above and would both commit a DIFFERENT, non-colliding link
        # key (the target segment varies with the chosen site) — CreateOnly
        # on the link alone cannot be the lock. siteAssignmentAspectTypeDDL's
        # .siteAssignment aspect exists exactly for this: its key is the SAME
        # regardless of chosen site, so CreateOnly on it, in the SAME atomic
        # batch as the link, makes the loser's whole commit reject.
        mutations = [
            make_link(at_site_lnk, appt_key, site_key, "atSite", "atSite", {}),
            make_aspect(appt_key, "siteAssignment", "appointmentSiteAssignment", {}),
        ]
        events = [{"class": "clinic.appointmentSiteSet", "data": {"appointmentKey": appt_key, "site": site_key}}]
        # primaryKey names the LINK key itself, mirroring BackfillAppointmentSite:
        # the write-footprint reply constraint accepts a mutation's own key or
        # its 3-segment vertex root, and the link has no vertex root of its own.
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": at_site_lnk}}

    if ot == "RecordEncounter":
        appt_key = required_string(p, "appointmentKey")
        _, appt_id = parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        cls = class_of(state, appt_key)
        if cls != "appointment":
            fail("WrongClass: appointmentKey: " + appt_key + " has class " + str(cls) + ", required appointment")

        # Payload shape, checked before any read: each of the three texts is
        # bounded at ENCOUNTER_FIELD_MAX_BYTES (the InputSchema's maxLength,
        # enforced here because the cap on the superseded history below is
        # sized in bytes and a bound the script does not hold is not a bound).
        # Starlark len() on a string counts BYTES, so a multi-byte character
        # spends more than one of them.
        summary = required_string(p, "summary")
        assessment = optional_string(p, "assessment")
        plan = optional_string(p, "plan")
        for field, text in [("summary", summary), ("assessment", assessment), ("plan", plan)]:
            if text != None and len(text) > ENCOUNTER_FIELD_MAX_BYTES:
                fail("InvalidArgument: " + field + " exceeds " + str(ENCOUNTER_FIELD_MAX_BYTES) + " bytes (" + str(len(text)) + ")")

        # Standing provider-binding guard, mirroring RescheduleAppointment /
        # SetAppointmentStatus: a bound provider may document THEIR OWN
        # appointment; anyone else falls back to the workplace walk, which a
        # provider (carrying no worksAt anchor) never clears, so an unbound
        # caller is denied rather than merely unconfined.
        # workplace-exempt: (ownership-bound) RecordEncounter carries no
        # self-service consumer grant, so op.authTargetValidated is never true
        # here; the gate below reduces to the operator check.
        if not (op.authTargetValidated or actor_holds_operator(op.actor)):
            standing_provider = appointment_provider(appt_id)
            if not actor_bound_to_appointment_provider(op.actor, standing_provider):
                enforce_workplace_confined(appointment_sites(appt_id, standing_provider), "cannot record encounter on appointment " + appt_key)

        # A visit that never took place cannot be documented: cancelled and
        # noShow are the two terminal outcomes that say so. completed / scheduled
        # / confirmed / checkedIn all document normally — the provider
        # documenting a started visit IS the evidence it happened, and this op
        # never writes .status, so closing the desk's record stays the desk's
        # job.
        # read-posture: (a) declared in contextHint.optionalReads by every
        # RecordEncounter dispatcher — absent (never set) is the legitimate
        # still-scheduled case, not a correctness error.
        cur_status = kv.Read(appt_key + ".status")
        if cur_status != None and not cur_status.isDeleted:
            cur_val = cur_status.data.get("value")
            if cur_val in ("cancelled", "noShow"):
                fail("VisitNotHeld: appointment " + appt_key + " is " + cur_val + "; a visit that did not take place cannot be documented")

        # A visit cannot be documented before it starts — the same inclusive,
        # soft-clock boundary enforce_started reads for completed/noShow.
        # read-posture: (a) declared in contextHint.reads by every
        # RecordEncounter dispatcher — CreateAppointment always writes
        # .schedule, so its absence here is a correctness error.
        sched = kv.Read(appt_key + ".schedule")
        refuse_before_start(appt_key, sched, op.submittedAt, "document")

        # The post-visit record splits across two aspects along the sensitivity
        # boundary: .encounter carries the raw clinical content (SENSITIVE, DEK
        # custodied on the clinicalRecord retention class — step 6.5 encrypts its
        # whole data map); .documentation carries the operational signals the
        # plain lenses project, which would be unreadable if they shared the
        # encrypted aspect. summary is the required visit note; assessment
        # (diagnosis) + plan (orders) are optional and stay off .documentation
        # entirely.
        # All three keys are ALWAYS written, an unfilled one as "". The record is
        # rendered back through clinicEncountersRead, whose secure columns each
        # name one field of the decrypted plaintext: a key the plaintext omits is
        # a spec/DDL mismatch to the decryptor, which redacts the column and
        # raises the privacy-critical alarm. Omitting an optional field would fire
        # that alarm on the ordinary case — a visit with no separate assessment —
        # and drown the signal it exists to carry.
        enc = {"summary": summary,
               "assessment": assessment if assessment != None else "",
               "plan": plan if plan != None else ""}
        # followUpRequested is OPERATIONAL, non-PHI (the existence of a
        # follow-up, like an appointment time — projected). The clinical REASON
        # for a follow-up lives in .encounter's plan field, RAW PHI decrypted at
        # projection into clinicEncountersRead for the treating provider only.
        # The follow-up signals are the payload's on every write — the form
        # re-sends them, and followUpReminders keeps reading the same two keys.
        doc = {"followUpRequested": optional_bool(p, "followUpRequested")}
        follow_date = optional_string(p, "followUpDate")
        if doc["followUpRequested"]:
            # A follow-up with no target date can never come due, so
            # followUpReminders (and the FE's own follow-up worklist) can
            # never act on it — require the date whenever one is requested.
            if follow_date == None:
                fail("MissingFollowUpDate: followUpDate is required when followUpRequested is true")
            # Normalized to a full canonical-UTC RFC3339 instant so the optional
            # clinic-reminders follow-up reminder can arm an @at timer at it.
            doc["followUpDate"] = normalize_follow_up_date(follow_date)

        # Record-or-amend. "A record exists" is what the lenses' own presence
        # rule says it is: a live .documentation carrying a documentedAt — never
        # .encounter's presence. Both prior aspects are hydrated (decrypted at
        # step 4 for the sensitive one), so an amendment can carry the text it
        # replaces; a record whose class key was destroyed faults the declared
        # read before this runs, and refusing to amend what cannot be read is
        # the honest outcome.
        # read-posture: (d) declared in contextHint.optionalReads by every
        # RecordEncounter dispatcher — absent is the first record.
        prior_doc = kv.Read(appt_key + ".documentation")
        # read-posture: (d) declared in contextHint.optionalReads by every
        # RecordEncounter dispatcher — absent is the first record.
        prior_enc = kv.Read(appt_key + ".encounter")
        # Every instant here derives from op.submittedAt, normalized to canonical
        # whole-second UTC (time.rfc3339_utc — pure, no clock read).
        now = time.rfc3339_utc(op.submittedAt)
        recorded = (prior_doc != None and not prior_doc.isDeleted
                    and prior_doc.data.get("documentedAt") not in (None, ""))
        write_encounter = True
        if not recorded:
            # The first record. documentedAt is when the visit was FIRST
            # documented — the presence signal the lenses surface; superseded
            # starts empty so the plaintext shape is fixed from the first
            # write.
            enc["superseded"] = []
            doc["documentedAt"] = now
        else:
            # A record exists. documentedAt is PRESERVED whatever follows.
            # The follow-up signals are the payload's on every write.
            prior_data = prior_doc.data
            prior_text = {"summary": "", "assessment": "", "plan": ""}
            superseded = []
            if prior_enc != None and not prior_enc.isDeleted:
                for field in ["summary", "assessment", "plan"]:
                    v = prior_enc.data.get(field)
                    prior_text[field] = v if v != None else ""
                prior_superseded = prior_enc.data.get("superseded")
                if prior_superseded != None:
                    superseded = list(prior_superseded)
            text_changed = (prior_text["summary"] != enc["summary"]
                            or prior_text["assessment"] != enc["assessment"]
                            or prior_text["plan"] != enc["plan"])
            follow_up_changed = (prior_data.get("followUpRequested") != doc["followUpRequested"]
                                 or prior_data.get("followUpDate") != doc.get("followUpDate"))
            # "Amended" is a claim that something changed: the same three
            # texts, the same followUpRequested and the same normalized
            # followUpDate write nothing and emit nothing. primaryKey stays OUT
            # of the response — the write-footprint reply constraint
            # (commit_path.go) rejects a script-named primaryKey with no
            # matching mutation.
            if not text_changed and not follow_up_changed:
                return {"mutations": [], "events": [], "response": {}}
            doc["documentedAt"] = prior_data.get("documentedAt")
            if text_changed:
                # A TEXT amendment: the current text moves onto the superseded
                # list with the instant it was recorded (the prior amendedAt
                # when the record has been amended before, else its
                # documentedAt), the new text becomes current, and amendedAt
                # says when the current text was recorded. A prior plaintext
                # written before superseded existed reads as an empty list.
                if len(superseded) >= MAX_ENCOUNTER_AMENDMENTS:
                    fail("AmendmentLimit: appointment " + appt_key + " already carries " + str(len(superseded)) + " superseded versions of its clinical record (the bound is " + str(MAX_ENCOUNTER_AMENDMENTS) + "); the record cannot be amended again")
                recorded_at = prior_data.get("amendedAt")
                if recorded_at == None:
                    recorded_at = prior_data.get("documentedAt")
                entry = {"summary": prior_text["summary"],
                         "assessment": prior_text["assessment"],
                         "plan": prior_text["plan"],
                         "recordedAt": recorded_at}
                enc["superseded"] = superseded + [entry]
                doc["amendedAt"] = now
            else:
                # Only the follow-up signals changed: the text stands as
                # recorded, so .encounter is not written (its history and its
                # revision do not move) and the prior amendedAt, if any, is
                # carried — it dates the current TEXT, which this write does
                # not touch.
                write_encounter = False
                if prior_data.get("amendedAt") != None:
                    doc["amendedAt"] = prior_data.get("amendedAt")
        # Each write is a bare update on a declared key, so the commit
        # conditions it on the hydrated revision: two concurrent amendments
        # cannot both build on the same prior. A text amendment and the first
        # record land both aspects in one batch; a follow-up-only change lands
        # .documentation alone. The .schedule / .status aspects + the
        # forPatient / withProvider links are untouched.
        mutations = []
        if write_encounter:
            mutations.append(make_aspect_upsert(appt_key, "encounter", "appointmentEncounter", enc))
        mutations.append(make_aspect_upsert(appt_key, "documentation", "appointmentDocumentation", doc))
        events = [{"class": "clinic.appointmentEncounterRecorded",
                   "data": {"appointmentKey": appt_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    if ot == "TombstoneAppointment":
        appt_key = required_string(p, "appointmentKey")
        _, appt_id = parts_of(appt_key, "appointmentKey", "appointment")
        if not vertex_alive(state, appt_key):
            fail("UnknownAppointment: " + appt_key)
        # A hard tombstone must free the appointment's held cells too — regardless of
        # its current status — else they stay claimed forever (a leak, not merely a
        # bound-maintenance nicety). provider/patient are required for the same reason
        # as SetAppointmentStatus's terminal path: no back-reference is stored on the
        # appointment, so the caller supplies + we validate them.
        provider = required_string(p, "provider")
        require_matching_provider(appt_id, provider)
        patient = required_string(p, "patient")
        require_matching_patient(appt_id, patient)
        # An appointment already in a terminal status released its cells AND
        # its self-booking day claim at that first terminal transition, and
        # those keys may since have been re-claimed by ANOTHER live visit (a
        # later booking on the freed cells; the next self booking on the freed
        # day, which OCC-revives the same claim key). Re-releasing them here
        # would tombstone that other visit's claims out from under it, so the
        # release runs only for a non-terminal appointment. Absence of .status
        # is the never-set (scheduled) case, so the read is absence-tolerant.
        # read-posture: (d) optionalReads — derived server-side by this script's
        # own derive_reads(op) for TombstoneAppointment.
        cur_status = kv.Read(appt_key + ".status")
        cur_val = None
        if cur_status != None and not cur_status.isDeleted:
            cur_val = cur_status.data.get("value")
        mutations = [make_tombstone(appt_key)]
        if cur_val not in TERMINAL_STATUSES:
            # read-posture: (d) optionalReads — derived server-side by this
            # script's own derive_reads(op) for TombstoneAppointment.
            mutations = mutations + release_cells_mutations(provider, patient, kv.Read(appt_key + ".schedule"))
        events = [{"class": "clinic.appointmentTombstoned", "data": {"appointmentKey": appt_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": appt_key}}

    fail("appointment DDL: unknown operationType: " + ot)
`

// aspectDeclarationOnlyScript is the declaration-only Starlark for the four
// aspect-type DDLs. The aspects are written by the vertexType DDLs' ops; these
// aspect-type DDLs are step-6 write gates only, never op handlers — they fail
// closed if dispatched.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`
