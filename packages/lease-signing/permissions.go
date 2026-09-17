package leasesigning

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// Grant matrix:
//
//	CreateLeaseApplication          → operator
//	CreateLeaseApplication (self)   → consumer
//	CreateLeaseServiceInstance      → operator
//	TombstoneSupersededLeaseServiceInstance → operator
//	RecordLeaseServiceOutcome       → operator
//	RecordServiceDispatch           → operator
//	CreateLeaseDocInstance          → operator
//	RecordLeaseDocOutcome           → operator
//	SignLease                       → operator
//	WithdrawLeaseApplication        → operator
//	WithdrawLeaseApplication (self) → consumer
//	DecideLeaseApplication          → operator, frontOfHouse
//	DecideLeaseApplication (self)   → consumer
//	SetApplicantProfile             → operator
//	SetApplicantProfile (self)      → consumer
//	OpenRenewal                     → operator
//	SetRenewalTerms                 → operator
//	SetRenewalTerms (self)          → consumer
//	VerifyGuarantor                 → operator
//	VerifyGuarantor (self)          → consumer
//	SignRenewal                     → operator
//	CancelRenewal                   → operator
//	CancelRenewal (self)            → consumer
//	ReassignLeaseUnit               → operator
//	EndTenancy                      → operator
//	RecordApplicationLoss           → operator
//
// The orchestrator-submitted ops are operator-driven (the same operator-grant
// idiom service-domain / orchestration-base use):
//   - CreateLeaseApplication — the installer / test / orchestrator starts an
//     application.
//   - CreateLeaseServiceInstance — Loom's relay actor (operator-equivalent)
//     submits the externalTask instanceOp.
//   - RecordLeaseServiceOutcome — the bridge's service actor
//     (operator-equivalent) submits the replyOp.
//   - RecordServiceDispatch — the bridge's service actor (operator-equivalent)
//     submits the dispatchOp on a Pending adapter outcome.
//   - SignLease — the assignTask target. The applicant performs it at runtime
//     authorized by the §10.7 ephemeral task grant (scoped to the specific
//     application); a standing operator grant covers the direct-write /
//     orchestrator path 14.4 exercises. (Q6: a user-facing consumer grant for
//     the real applicant path is an additive refinement — the ephemeral task
//     grant is the runtime authorization either way.)
//   - CreateLeaseApplication also grants `consumer`, scope=self
//     (real-actor-write-auth-e2e design §3.4): a real applicant applies for
//     themselves through the Gateway. `authContext.target == actor` is
//     checked at step 3 (Contract #6, mirroring identity-domain's
//     ClaimIdentity); the Starlark script separately requires
//     payload.applicant == actor, since step 3 never sees the payload.
//   - SetApplicantProfile and WithdrawLeaseApplication also grant `consumer`,
//     scope=self (persona-worlds §7.2 applicant-hat grants audit): both are
//     self-service applicant ops (record my qualification profile, withdraw my
//     own application), so a signed-in applicant submits them AS THEMSELF, not
//     via the trusted-tool operator mint. Step 3 checks authContext.target ==
//     actor; the script closes the payload gap the CreateLeaseApplication row
//     already closes — it requires the acting identity to be the application's
//     own applicant, verified via the deterministic applicationFor link, so a
//     consumer can only act on their own application. The operator (scope=any,
//     no authContext) path is unchanged.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateLeaseApplication",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreateLeaseApplication operations.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "CreateLeaseApplication",
			Scope:         "self",
			Note:          "Grants a consumer the right to create their OWN lease application (payload.applicant == actor).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "CreateLeaseServiceInstance",
			Scope:         "any",
			Note:          "Grants the operator (Loom's relay actor) the right to submit the externalTask instanceOp.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "TombstoneSupersededLeaseServiceInstance",
			Scope:         "any",
			Note:          "Grants the operator the right to retire a lease service instance superseded by a later completed one on the same subject + family — never a person-facing action (BackfillPatientRegistration precedent, clinic-domain). Weaver's service actor holds the operator role and is the DURABLE submitter: the supersededBackgroundChecks convergence target dispatches this op as a directOp off a lens row that has already proven the pair (bgcheck-supersession-convergence-rule-design.md). An operator or trusted tool may run it by hand for a one-off repair. Either way the script re-proves both instances' ownership by this package's own leaseServiceInstance type authority — the predecessor's from a declared read, the successor's from a bounded instanceOf enumeration (Contract #2 §2.5 class (e)), which every submitter declares as contextHint.enumerations — so the grant's width buys no reach into another package's instances. Loom holds the same role and is refused in-script: it mints instances and never retires them.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "RecordLeaseServiceOutcome",
			Scope:         "any",
			Note:          "Grants the operator (the bridge's service actor) the right to submit the externalTask replyOp.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "RecordServiceDispatch",
			Scope:         "any",
			Note:          "Grants the operator (the bridge's service actor) the right to submit the externalTask dispatchOp on a Pending outcome.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "CreateLeaseDocInstance",
			Scope:         "any",
			Note:          "Grants the operator (Loom's relay actor) the right to submit the docGen externalTask instanceOp.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "RecordLeaseDocOutcome",
			Scope:         "any",
			Note:          "Grants the operator (the bridge's service actor) the right to submit the docGen externalTask replyOp.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "SignLease",
			Scope:         "any",
			Note:          "Grants the operator the right to submit SignLease; the applicant signs via the ephemeral task grant (§10.7).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "WithdrawLeaseApplication",
			Scope:         "any",
			Note:          "Grants the operator the right to submit WithdrawLeaseApplication (the applicant cancels / backs out of an application via the trusted-tool app).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "WithdrawLeaseApplication",
			Scope:         "self",
			Note:          "Grants a consumer the right to withdraw their OWN lease application (the acting identity is the application's applicant — verified via the applicationFor link).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "DecideLeaseApplication",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit DecideLeaseApplication (approve / decline an application via the trusted-tool app — the human gate the listing-flip waits behind; the front-desk \"applications to review\" beat).",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "DecideLeaseApplication",
			Scope:         "self",
			Note:          "Grants a landlord the right to decide an application on a unit they MANAGE (the acting identity is signed in as itself; the script walks the application's own appliesToUnit link to the unit and requires the acting identity's manages link).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "SetApplicantProfile",
			Scope:         "any",
			Note:          "Grants the operator the right to submit SetApplicantProfile (the applicant records their qualification profile via the trusted-tool app — income / employment / references / co-applicant / guarantor; same operator model as SignLease).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "SetApplicantProfile",
			Scope:         "self",
			Note:          "Grants a consumer the right to record the qualification profile on their OWN lease application (the acting identity is the application's applicant — verified via the applicationFor link).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "BackfillLeaseTerms",
			Scope:         "any",
			Note:          "Grants the operator alone the right to backfill requestedRent onto an approved lease application that carries none — dispatched automatically by leaseRentSettlement's missing_terms gap (semantic-contracts), and runnable by hand; never a person-facing action (BackfillPatientRegistration precedent, clinic-domain).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "ReassignLeaseUnit",
			Scope:         "any",
			Note:          "Grants the operator alone the right to re-point a lease application's appliesToUnit link — the repair for a lease whose unit was tombstoned (TombstoneLocation does not cascade; the SetMenuItemLocation / ReassignSession repair shape); a tenancy's unit is never a front-desk call.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "OpenRenewal",
			Scope:         "any",
			Note:          "Grants the operator (Weaver's service actor) the right to submit OpenRenewal — the directOp the leaseExpiry target dispatches (the SetListingStatus cross-package directOp precedent).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "SetRenewalTerms",
			Scope:         "any",
			Note:          "Grants the operator the right to submit SetRenewalTerms; the landlord sets it via the §10.7 ephemeral task grant (same operator model as SignLease/DecideLeaseApplication).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "SetRenewalTerms",
			Scope:         "self",
			Note:          "Grants a landlord the right to set the terms of a renewal cycle on a unit they MANAGE (the script walks renewal→renews→leaseapp→appliesToUnit to the unit and requires the acting identity's manages link).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "VerifyGuarantor",
			Scope:         "any",
			Note:          "Grants the operator the right to submit VerifyGuarantor; the landlord performs it via the §10.7 ephemeral task grant (same operator model as SignLease).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "VerifyGuarantor",
			Scope:         "self",
			Note:          "Grants a landlord the right to re-verify the guarantor on a renewal cycle for a unit they MANAGE (the script walks renewal→renews→leaseapp→appliesToUnit to the unit and requires the acting identity's manages link BEFORE any applicant profile is read).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "SignRenewal",
			Scope:         "any",
			Note:          "Grants the operator the right to submit SignRenewal; the tenant signs via the §10.7 ephemeral task grant (same operator model as SignLease).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "CancelRenewal",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CancelRenewal — the landlord's task-LESS terminal decline (no assignTask leg; a direct operator/trusted-tool action, same posture as WithdrawLeaseApplication).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "CancelRenewal",
			Scope:         "self",
			Note:          "Grants a landlord the right to decline a renewal cycle on a unit they MANAGE (the script walks renewal→renews→leaseapp→appliesToUnit to the unit and requires the acting identity's manages link).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "EndTenancy",
			Scope:         "any",
			Note:          "Grants the operator (Weaver's service actor) the right to submit EndTenancy — the directOp the tenancyEnd target dispatches once a signed, approved tenancy's leaseEnd has lapsed with no open renewal (the OpenRenewal / SetListingStatus cross-package directOp precedent); an operator may also run it by hand. Never a person-facing action: the term ends on its own recorded date, and the op refuses NotYetEnded ahead of it.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "GiveNotice",
			Scope:         "any",
			Note:          "Grants the operator the right to submit GiveNotice by hand (the trusted-tool posture WithdrawLeaseApplication carries): a recorded notice ends the tenancy early on its move-out date. The script records givenBy = operator on this path.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "GiveNotice",
			Scope:         "self",
			Note:          "Grants a consumer the right to give notice on a lease they hold — as the TENANT (the acting identity is the application's applicant, verified via the deterministic applicationFor link keyed on the actor) or as the LANDLORD (the acting identity manages the application's own unit, verified via the manages link — the script walks appliesToUnit and requires it, AuthDenied otherwise). Both probes bind the platform-validated self path; the one that admits the caller is the recorded givenBy.",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "RecordApplicationLoss",
			Scope:         "any",
			Note:          "Grants the operator (Weaver's service actor) the right to submit RecordApplicationLoss — the directOp leaseApplicationComplete's missing_lossRecorded gap dispatches once an undecided application's unit has leased to another applicant (the EndTenancy precedent); an operator may also run it by hand via the CLI under the primordial admin. Never a person-facing action: the loss is recorded on the application as .decision = lost, the op refuses UnitNotLeased against a unit that is not leased, and any recorded decision makes it a no-op.",
			GrantsTo:      []string{"operator"},
		},
	}
}

// OpMetas declares the op-meta vertices that make ops forOperation-resolvable.
//
//   - SignLease — REQUIRED: the assignTask operation the §10.8 playbook binds;
//     the Weaver Actuator resolves forOperation to its op-meta when it creates
//     the remediation task. Its absence would break the missing_signature gap.
//   - RecordIdentityPII carries NO entry here. identity-domain owns that op's
//     DDL and now declares its own full descriptor, and both engines index
//     op-metas into a flat operationType map off the corpus-wide vtx.meta.>
//     CDC — so a second meta for the same op would resolve last-writer-wins.
//     The onboarding pattern's userTask still resolves forOperation, to
//     identity-domain's meta, which this package Depends on.
//   - CreateLeaseServiceInstance / RecordLeaseServiceOutcome /
//     RecordServiceDispatch / CreateLeaseDocInstance / RecordLeaseDocOutcome —
//     declared for discoverability + the manifest cross-check. The engine
//     resolves the externalTask instanceOp/replyOp from the step strings
//     directly (and the bridge selects the dispatchOp from the event body), not
//     via forOperation, so these are hygiene, not strictly required.
//   - SetRenewalTerms / VerifyGuarantor / SignRenewal / SetApplicantProfile —
//     REQUIRED: the four assignTask operations the renewalComplete goal's
//     actions catalog binds (renewal_targets.go); the Weaver Actuator resolves
//     forOperation to each op-meta when it creates the remediation task.
//     SetApplicantProfile's descriptor (below) stays AuthContext "self": the
//     tenant completes the task from loftspace-app's own profile form under
//     their consumer scope=self grant, and the app retires the task through
//     CompleteTask — the task's ephemeral grant is minted but never the path
//     the form submits on. CancelRenewal is task-less
//     (a directOp/operator action, never an assignTask target), so its meta is
//     owed to S1 rather than to forOperation resolution. All five carry full
//     descriptors below: SignRenewal's tenant leg is a real loftspace-app
//     screen (web/app.js — the Tasks-inbox completion modal, and the "Sign
//     renewal" button on the tenant's own renewal card), which is the app-seam
//     rule (vertical-package-standard.md §15) — a shipped screen is proof a person
//     triggers the op, whatever its grant roles (permissions.go grants only
//     `operator`; the tenant reaches it via the §10.7 ephemeral task grant
//     alone, so its Dispatch.AuthContext is "task", not "standing" — the
//     RecordIdentityPII precedent, identity-domain/opmetas.go).
//
// Every op a human triggers carries a FULL descriptor (S1) — Presentation +
// InputSchema + FieldDescriptions + Dispatch. The audience slice is narrower
// than the owning DDL's merged InputSchema: a descriptor describes ONE op's
// fields, not the whole vertex type's. The remaining bare `{OperationType}`
// entries are engine/adapter legs — externalTask instanceOp/replyOp/dispatchOp
// — that exist only so forOperation resolves. SignLease is NOT among them: it
// is an assignTask target a real person completes from loftspace-app's task
// modal, so it carries a full (single-confirm) descriptor below, and the
// modal renders from that descriptor rather than from a hand-built form.
//
// Dispatch.AuthContext names the SELF path wherever an op carries both a
// standing staff grant and a consumer scope=self grant (clinic-domain's
// CreateAppointment / RescheduleAppointment / SetAppointmentStatus idiom): a
// staff FE hardcodes its own dispatch, so the descriptor exists to let a
// descriptor-driven client walk the path it cannot otherwise infer. The
// declared Reads/OptionalReads mirror what the bespoke LoftSpace FE proves in
// production (cmd/loftspace-app/web/app.js) — required (a) reads for the
// validation links each script verifies, absence-tolerant (d) reads for the
// aspects a first submission legitimately lacks.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		// DecideLeaseApplication is the front-desk demo beat ("Applications to
		// review"), and the first op meta here to carry the full descriptor
		// vocabulary: a staff client builds the entire submission — form, labels,
		// declared reads — from this vertex alone.
		//
		// The op carries TWO grants: operator/frontOfHouse at scope=any, and a
		// landlord at scope=self. A descriptor names one dispatch, so it names
		// the SELF path (clinic's dual-grant idiom): the staff FE hardcodes its
		// own standing submission and needs no descriptor to do it, whereas the
		// landlord path is the one a descriptor-driven client cannot infer.
		// A "standing" descriptor tells a client to send no authContext object
		// at all, which puts a landlord on the staff path and gets them refused.
		//
		// The landlord's manages probe is deliberately NOT declared below: the
		// unit is not knowable until the application's own appliesToUnit link
		// resolves, so require_manages reads it as an annotated class-(e)
		// follow-up (scripts.go) rather than a pre-declared key.
		{
			OperationType: "DecideLeaseApplication",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Decide a lease application",
				ShortLabel:  "Decide",
				Description: "Approve or decline an application. The decision is final once recorded.",
				Icon:        "clipboard",
				Tone:        "primary",
				SubmitLabel: "Record decision",
				Group:       "Front desk",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the application being decided."},` +
				`"decision":{"type":"string","title":"Decision","enum":["approved","declined"],"description":"The decision. Terminal once recorded."},` +
				`"reason":{"type":"string","title":"Reason","description":"Why the application was declined. Ignored on an approve."}},` +
				`"required":["leaseAppKey","decision"]}`,
			FieldDescriptions: map[string]string{
				"leaseAppKey": "The application being decided — filled from the application in view, not typed.",
				"decision":    "Approve or decline. TERMINAL: the same value re-submits harmlessly, but a different value is rejected, so a decision can never silently flip.",
				"reason":      "Optional rationale shown to the applicant on a decline, and kept as a fair-housing record. Ignored on an approve.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "leaseapp",
				AuthContext: "self",
				TargetField: "leaseAppKey",
				TargetType:  "leaseapp",
				Reads:       []string{"{payload.leaseAppKey}"},
				// Each of these is genuinely absence-tolerant, which is why none is
				// a required Read: .decision and .tenancy are absent on a first
				// decision (they ARE the read-before-write terminal and create-only
				// guards), .signature is absent on an unsigned application (the
				// NotReadyToApprove check). .terms is absent on a bare
				// applicant+unit application that never supplied moveInDate — the
				// FIRST approve's .tenancy derivation falls back to the unit's own
				// .listing wherever .terms carries nothing. .decidedProfileSnapshot
				// is absent on a first decision too — it is ITS OWN create-only
				// guard (scripts.go), declared here (not derived from the .decision
				// read) so a concurrent double-decide's losing create can
				// gracefully retry/no-op at commit (commit_path.go's
				// absentConditionedCreates) instead of hard-rejecting the whole
				// batch. .profile / .underwritingParties / .applicationSignals are
				// absent whenever a landlord decides before the applicant ever
				// submits a profile — the snapshot then captures an empty/partial
				// record rather than failing the decision. The unit + its .listing
				// are NOT declared here at all — the script resolves them itself
				// from the application's own appliesToUnit link (a class-(e)
				// follow-up, scripts.go), never a payload field, so there is no
				// only-present-on-approve field left to build a malformed key
				// around on a decline (Standard §readTemplateDebt).
				OptionalReads: []string{
					"{payload.leaseAppKey}.decision",
					"{payload.leaseAppKey}.tenancy",
					"{payload.leaseAppKey}.terms",
					"{payload.leaseAppKey}.signature",
					"{payload.leaseAppKey}.decidedProfileSnapshot",
					"{payload.leaseAppKey}.profile",
					"{payload.leaseAppKey}.underwritingParties",
					"{payload.leaseAppKey}.applicationSignals",
				},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator). The unit
				// resolution (leaseapp_unit — the confinement's own subject,
				// and the first approve's .listing source) walks the
				// application's appliesToUnit link; the first decline's guard
				// release (free_applied_to_unit_guard) walks its applicationFor
				// link for the applicant.
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
					{Hub: "{payload.leaseAppKey}", Relation: "appliesToUnit", Direction: "out"},
					{Hub: "{payload.leaseAppKey}", Relation: "applicationFor", Direction: "out"},
				},
			},
			// refusal-courtesy(facet): BadDecision: unreachable — decision is schema.enum ["approved","declined"], rendered by the generic form as a select over the enum (cmd/facet/web/app.js renderField), so no other value can be submitted.
			// refusal-courtesy(facet): DecisionFinal, NotReadyToApprove, NoListing, InvalidTerms: none — no VisibleWhen or entity lens column carries decision/signature/listing state; Facet offers Decide on every leaseapp row.
			// refusal-courtesy(facet): MoveInBeforeAvailable: none — the move-in is the applicant's own recorded .terms, not a field of this form, and no entity lens column pairs it with the unit's listing.availableFrom; the refusal names both days.
		},
		// The applicant's own three legs. Each is granted to consumer at
		// scope=self, so each is a form a real person fills in.
		{
			OperationType: "CreateLeaseApplication",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Apply for this unit",
				ShortLabel:  "Apply",
				Description: "Submit your own application to lease a unit.",
				Icon:        "clipboard",
				Tone:        "primary",
				SubmitLabel: "Submit application",
				Group:       "My applications",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"applicant":{"type":"string","description":"vtx.identity.<NanoID> of the applicant — your own identity."},` +
				`"unit":{"type":"string","description":"vtx.unit.<NanoID> of the unit being applied for."},` +
				`"moveInDate":{"type":"string","format":"date","title":"Move-in date","description":"Requested move-in date. Optional; supplying it requires leaseTermMonths."},` +
				`"leaseTermMonths":{"type":"integer","minimum":1,"title":"Lease term (months)","description":"Requested lease term in months. Required when moveInDate is supplied."},` +
				`"requestedRent":{"type":"number","minimum":1,"title":"Monthly rent","description":"Optional rent the applicant is offering."}},` +
				`"required":["applicant","unit"]}`,
			FieldDescriptions: map[string]string{
				"applicant":       "Your own identity — filled from the session, never typed. The scope=self grant requires it to equal the acting identity.",
				"unit":            "The unit being applied for — filled from the listing in view, not typed.",
				"moveInDate":      "When you would like to move in. Optional, but supplying it also requires a lease term; together they record your requested terms.",
				"leaseTermMonths": "How many months you are asking to lease for. Required only alongside a move-in date.",
				"requestedRent":   "Optional — the rent you are offering, when it differs from the listed rent.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "leaseapp",
				AuthContext: "self",
				TargetField: "unit",
				TargetType:  "unit",
				// The scope=self grant already requires applicant == the acting
				// identity, so the value was never the visitor's to type — the
				// client fills it from the session and renders no field for it
				// (the help's "filled from the session" promise, made real).
				ContextParams: map[string]string{"applicant": "{actor}"},
				Reads:         []string{"{payload.applicant}", "{payload.unit}"},
				// The per-(applicant, unit) guard link is absent on a first
				// application and tombstoned after a withdraw — its absence is
				// exactly the condition that permits the create, so it can
				// never be a required read. The unit's listing rent (rent
				// fallback when the applicant offers none, scripts.go) is
				// absent on a unit with no listing yet — same absence-
				// tolerant idiom SetApplicantProfile already declares for the
				// identical key.
				OptionalReads: []string{
					"lnk.identity.{payload.applicant:id}.appliedToUnit.unit.{payload.unit:id}",
					"{payload.unit}.listing",
				},
			},
			// refusal-courtesy(facet): DuplicateApplication: none — no entity lens column projects the caller's own existing applications against a unit; the guard-link race is invisible to the picker.
			// refusal-courtesy(facet): InvalidTerms: cap — leaseTermMonths/requestedRent above declare "minimum":1, and the generic form renders min= from it (cmd/facet/web/app.js renderField) with step="1" for the integer.
			// refusal-courtesy(facet): MoveInBeforeAvailable: none — the InputSchema carries no per-row minimum (the floor is the unit's own listing.availableFrom, a value the generic form cannot bind a date control to); the refusal names the available day.
		},
		{
			OperationType: "WithdrawLeaseApplication",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Withdraw application",
				ShortLabel:  "Withdraw",
				Description: "Back out of an application you submitted. Frees you to apply for the same unit again later.",
				Icon:        "clipboard",
				Tone:        "destructive",
				SubmitLabel: "Withdraw",
				Group:       "My applications",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the application being withdrawn."},` +
				`"unit":{"type":"string","title":"Unit","description":"vtx.unit.<NanoID> the application is for — verified against the application's own appliesToUnit link."},` +
				`"applicant":{"type":"string","description":"vtx.identity.<NanoID> of the applicant — verified against the application's own applicationFor link."}},` +
				`"required":["leaseAppKey","unit","applicant"]}`,
			FieldDescriptions: map[string]string{
				"leaseAppKey": "The application being withdrawn — filled from the application in view, not typed.",
				"unit":        "The unit the application is for. Verified against the application's own link, so a mismatched value is rejected rather than trusted.",
				"applicant":   "Your own identity. Verified against the application's own applicationFor link — you can only withdraw your own application.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "leaseapp",
				AuthContext: "self",
				TargetField: "leaseAppKey",
				TargetType:  "leaseapp",
				// The applicant is verified against the application's own
				// applicationFor link, and only the acting identity's own value
				// can pass it — so the client fills it from the session and
				// renders no field for it. The unit stays user-supplied until
				// an entity lens projects it as a fillable column.
				ContextParams: map[string]string{"applicant": "{actor}"},
				// Both validation links are required: the script verifies the
				// unit and the applicant against the application's OWN links
				// rather than trusting the payload, so their absence is a
				// caller error, not a tolerable miss.
				Reads: []string{
					"{payload.leaseAppKey}",
					"lnk.leaseapp.{payload.leaseAppKey:id}.appliesToUnit.unit.{payload.unit:id}",
					"lnk.leaseapp.{payload.leaseAppKey:id}.applicationFor.identity.{payload.applicant:id}",
				},
				// The guard link being freed may already be tombstoned; the
				// decision is absent on the undecided application (the normal
				// withdraw) and refuses the withdraw when it reads approved.
				OptionalReads: []string{
					"lnk.identity.{payload.applicant:id}.appliedToUnit.unit.{payload.unit:id}",
					"{payload.leaseAppKey}.decision",
				},
			},
			// refusal-courtesy(facet): ApplicantMismatch: unreachable — ContextParams sets applicant:"{actor}" (substituteTemplate's `actor` case, cmd/facet/web/app.js), overriding any typed value at submit.
			// refusal-courtesy(facet): UnitMismatch: none — InputSchema's "unit" is a plain string with no x-entityRef/contextParam, so Facet renders it as free text a caller can type.
			// refusal-courtesy(facet): AlreadyApproved: none — no VisibleWhen or entity lens column distinguishes an approved (executed-lease) application from an undecided one; Facet offers Withdraw on every leaseapp row.
		},
		{
			OperationType: "ReassignLeaseUnit",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Move an application to another unit",
				ShortLabel:  "Move unit",
				Description: "Re-point a lease application at a different unit — the repair for one whose unit was retired.",
				Icon:        "clipboard",
				Tone:        "primary",
				SubmitLabel: "Move application",
				Group:       "Operator repairs",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"leaseAppKey":{"type":"string","x-entityRef":"leaseapp","description":"vtx.leaseapp.<NanoID> of the application to re-point."},` +
				`"newUnitKey":{"type":"string","title":"New unit","x-entityRef":"unit","description":"vtx.unit.<NanoID> of the unit to re-point the application at."}},` +
				`"required":["leaseAppKey","newUnitKey"]}`,
			FieldDescriptions: map[string]string{
				"leaseAppKey": "The application being re-pointed.",
				"newUnitKey":  "The unit the application should apply to instead. A dead unit is the repair case; a live unit is an ordinary move.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class: "leaseapp",
				// standing, not "self": this op carries no scope=self grant
				// (operator-only repair), so it has no authContext target to
				// bind — the CorrectAppointmentStatus posture (clinic-domain).
				AuthContext: "standing",
				TargetField: "leaseAppKey",
				TargetType:  "leaseapp",
				Reads: []string{
					"{payload.leaseAppKey}",
					"{payload.newUnitKey}",
				},
				// The new appliesToUnit link is absent on the common case (the
				// application has never applied to this unit before), so it can
				// never be a required read. .decision and .tenancy decide
				// whether the application is TERMINAL (declined / lost /
				// tenancy ended — re-pointed only, its guard left alone) and
				// are absent on the undecided, never-approved application.
				OptionalReads: []string{
					"lnk.leaseapp.{payload.leaseAppKey:id}.appliesToUnit.unit.{payload.newUnitKey:id}",
					"{payload.leaseAppKey}.decision",
					"{payload.leaseAppKey}.tenancy",
				},
				// The script resolves the CURRENT appliesToUnit target and the
				// applicant's applicationFor endpoint itself (never payload
				// fields, the leaseapp_unit resolver's own forgery-resistance
				// rationale), so a descriptor-driven client walks both here
				// rather than trusting a payload-templated hub it cannot form
				// ahead of dispatch.
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{payload.leaseAppKey}", Relation: "appliesToUnit", Direction: "out"},
					{Hub: "{payload.leaseAppKey}", Relation: "applicationFor", Direction: "out"},
				},
			},
		},
		{
			// EndTenancy is dispatched by Weaver off the tenancyEnd target and
			// carries a standing operator grant alone — the ReassignLeaseUnit
			// posture: no scope=self path, so "standing" and no authContext
			// target to bind. The descriptor exists so a by-hand operator
			// submission (Loupe) and any descriptor-driven dispatcher declare
			// the same reads the target's playbook routes
			// (tenancy_end_targets.go): the application and its .tenancy as
			// REQUIRED reads, and its .notice as an OPTIONAL one. The first two
			// are fail-closed (a) reads on purpose — the gap only opens on a
			// leaseapp that has a tenancy, and the script reads the aspect from
			// hydration (never on demand), so an undeclared .tenancy is a
			// NoTenancy refusal, not a lazy GET. The .notice is genuinely
			// absence-tolerant (a lease with no notice is the common case) and
			// read from hydration the same way: declared and present, the term
			// ends on the recorded move-out; undeclared, it ends at leaseEnd.
			OperationType: "EndTenancy",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "End a lease term",
				ShortLabel:  "End tenancy",
				Description: "Record that a lease term ended on its end date, or on its recorded move-out when notice was given, and free the tenant to apply for the unit again. Refused before that date; a no-op once recorded.",
				Icon:        "clipboard",
				Tone:        "primary",
				SubmitLabel: "Record term end",
				Group:       "Operator repairs",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"leaseAppKey":{"type":"string","x-entityRef":"leaseapp","description":"vtx.leaseapp.<NanoID> of the application whose lease term ended."}},` +
				`"required":["leaseAppKey"]}`,
			FieldDescriptions: map[string]string{
				"leaseAppKey": "The application whose lease term ended. Its .tenancy.leaseEnd — or the earlier .notice.moveOutAt when notice was given — is the date recorded as endedAt; a term that has not reached it yet is refused.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "leaseapp",
				AuthContext: "standing",
				TargetField: "leaseAppKey",
				TargetType:  "leaseapp",
				Reads: []string{
					"{payload.leaseAppKey}",
					"{payload.leaseAppKey}.tenancy",
				},
				OptionalReads: []string{
					"{payload.leaseAppKey}.notice",
				},
				// Recording the end frees the per-(applicant, unit) guard
				// link: the script resolves the unit and the applicant off
				// the application's OWN links (scripts.go leaseapp_unit +
				// free_applied_to_unit_guard), never a payload field, so a
				// descriptor-driven client declares both walks.
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{payload.leaseAppKey}", Relation: "appliesToUnit", Direction: "out"},
					{Hub: "{payload.leaseAppKey}", Relation: "applicationFor", Direction: "out"},
				},
			},
		},
		{
			// GiveNotice carries the WithdrawLeaseApplication grant pair —
			// operator at scope=any and consumer at scope=self — and its
			// descriptor names the SELF path (the dual-grant idiom above),
			// which here admits TWO hats through one op: the tenant, proven by
			// the deterministic applicationFor link keyed on the actor (the
			// SetApplicantProfile probe), or else the landlord, proven by
			// require_manages on the application's own unit (the
			// DecideLeaseApplication probe). The tenant link is the only
			// self-scoped read a client can form ahead of dispatch, so it is
			// the declared OptionalRead; the landlord walk is the script's own
			// (e) enumeration off appliesToUnit, undeclarable client-side.
			// .tenancy and .signature are REQUIRED reads — a notice is only
			// ever given on an approved, signed lease, and the script reads
			// both from hydration; .notice is OPTIONAL (absent on every first
			// notice — its declared absence is what conditions the create).
			OperationType: "GiveNotice",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Give notice",
				ShortLabel:  "Give notice",
				Description: "Record the date you move out. Your lease ends on that date instead of its end date, and it cannot be renewed once notice is given.",
				Icon:        "clipboard",
				Tone:        "destructive",
				SubmitLabel: "Record move-out",
				Group:       "My lease",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"leaseAppKey":{"type":"string","x-entityRef":"leaseapp","description":"vtx.leaseapp.<NanoID> of the lease you are giving notice on."},` +
				`"moveOutDate":{"type":"string","format":"date","title":"Move-out date","description":"The date you move out (YYYY-MM-DD, read as midnight UTC). Today or later, after the lease started, before it ends."}},` +
				`"required":["leaseAppKey","moveOutDate"]}`,
			FieldDescriptions: map[string]string{
				"leaseAppKey": "The lease you are giving notice on — filled from the lease in view, not typed. You must be its tenant, or a landlord of its unit.",
				"moveOutDate": "The date you move out. Today or later, after the lease started and before its end date; a move-out on or after the end date needs no notice.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "leaseapp",
				AuthContext: "self",
				TargetField: "leaseAppKey",
				TargetType:  "leaseapp",
				Reads: []string{
					"{payload.leaseAppKey}",
					"{payload.leaseAppKey}.tenancy",
					"{payload.leaseAppKey}.signature",
				},
				OptionalReads: []string{
					"{payload.leaseAppKey}.notice",
					"lnk.leaseapp.{payload.leaseAppKey:id}.applicationFor.identity.{actor:id}",
				},
				// The landlord probe's walk: when the tenant link is absent on
				// the self path, require_manages resolves the unit from the
				// application's own appliesToUnit link (leaseapp_unit) and
				// reads the acting identity's manages link to it — the manages
				// key is a follow-up off the walk, undeclarable up front.
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{payload.leaseAppKey}", Relation: "appliesToUnit", Direction: "out"},
				},
			},
			// refusal-courtesy(facet): NoTenancy, LeaseNotSigned, TenancyEnded, NoticeAlreadyGiven: none — no VisibleWhen or entity lens column projects a leaseapp's tenancy, signature, end or notice (edge-manifest's selfAnchors carries the leaseapp key alone), so Facet offers Give notice on every leaseapp row.
			// refusal-courtesy(facet): MoveOutBeforeToday, MoveOutBeforeStart, MoveOutAfterEnd: none — the bounds are the op's own submittedAt and the lease's recorded leaseStart / leaseEnd, none of which the InputSchema can state as a static minimum/maximum for the generic date control.
		},
		{
			// RecordApplicationLoss is dispatched by Weaver off
			// leaseApplicationComplete's missing_lossRecorded gap and carries a
			// standing operator grant alone — the EndTenancy posture: no
			// scope=self path, so "standing" and no authContext target to bind.
			// The descriptor exists so a by-hand operator submission (the CLI
			// under the primordial admin, as EndTenancy — Loupe's console
			// identity holds consoleOperator, not operator) and any
			// descriptor-driven dispatcher declare the same reads the
			// target's playbook routes (targets.go): the application as a
			// REQUIRED read, and its .decision as an OPTIONAL one — absent is
			// the gap's own premise, and the declared absence is what conditions
			// the write CreateOnly. The unit is never a payload field: the script
			// walks the application's own appliesToUnit link and reads that
			// unit's .listing as a follow-up, so a descriptor-driven client
			// declares the enumeration rather than a hub it cannot form ahead
			// of dispatch.
			OperationType: "RecordApplicationLoss",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Record an application's loss",
				ShortLabel:  "Record loss",
				Description: "Record that an undecided application lost its unit to another applicant, freeing them to apply for it again. Refused unless the unit is leased; a no-op once any decision is recorded.",
				Icon:        "clipboard",
				Tone:        "primary",
				SubmitLabel: "Record loss",
				Group:       "Operator repairs",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"leaseAppKey":{"type":"string","x-entityRef":"leaseapp","description":"vtx.leaseapp.<NanoID> of the application whose unit went to another applicant."}},` +
				`"required":["leaseAppKey"]}`,
			FieldDescriptions: map[string]string{
				"leaseAppKey": "The application that lost its unit. Its own appliesToUnit link names the unit; that unit must be leased, and the application must carry no decision yet.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:         "leaseapp",
				AuthContext:   "standing",
				TargetField:   "leaseAppKey",
				TargetType:    "leaseapp",
				Reads:         []string{"{payload.leaseAppKey}"},
				OptionalReads: []string{"{payload.leaseAppKey}.decision"},
				// appliesToUnit resolves the unit whose leased status is the
				// premise; applicationFor resolves the applicant whose
				// per-(applicant, unit) guard the recorded loss frees
				// (scripts.go free_applied_to_unit_guard).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{payload.leaseAppKey}", Relation: "appliesToUnit", Direction: "out"},
					{Hub: "{payload.leaseAppKey}", Relation: "applicationFor", Direction: "out"},
				},
			},
		},
		{
			OperationType: "SetApplicantProfile",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Complete your application details",
				ShortLabel:  "Details",
				Description: "Provide the income, employment and reference details a landlord decides on. Re-submittable.",
				Icon:        "clipboard",
				Tone:        "primary",
				SubmitLabel: "Submit details",
				Group:       "My applications",
			},
			// The raw financials are captured but NEVER projected — the op
			// derives the landlord-facing signals (incomeToRentMet,
			// employmentVerified, referenceCount, guarantorIncomeToRentMet)
			// and the lens shows only those. This schema describes the INPUT
			// fields; none of them reads back.
			InputSchema: `{"type":"object","properties":` +
				`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of your application."},` +
				`"unit":{"type":"string","title":"Unit","description":"vtx.unit.<NanoID> the application is for — verified against the appliesToUnit link."},` +
				`"annualIncome":{"type":"number","title":"Annual income","description":"Gross annual income."},` +
				`"employmentStatus":{"type":"string","title":"Employment","description":"Employment status."},` +
				`"employerName":{"type":"string","title":"Employer","description":"Employer name. Optional."},` +
				`"references":{"type":"array","items":{"type":"string"},"title":"References","description":"References offered, one free-text entry each. Optional."},` +
				`"hasCoApplicant":{"type":"boolean","title":"Applying with someone?","description":"Whether a co-applicant is joining. Optional."},` +
				`"coApplicantName":{"type":"string","title":"Co-applicant's name","description":"Co-applicant's name. Optional."},` +
				`"coApplicantContact":{"type":"string","title":"Co-applicant's contact","description":"Co-applicant's contact. Optional."},` +
				`"hasGuarantor":{"type":"boolean","title":"Backed by a guarantor?","description":"Whether a guarantor is backing the application. Optional."},` +
				`"guarantorName":{"type":"string","title":"Guarantor's name","description":"Guarantor's name. Optional."},` +
				`"guarantorRelationship":{"type":"string","title":"Guarantor's relationship","description":"Guarantor's relationship to you. Optional."},` +
				`"guarantorAnnualIncome":{"type":"number","title":"Guarantor's annual income","description":"Guarantor's gross annual income. Optional."}},` +
				`"required":["leaseAppKey","unit","annualIncome","employmentStatus"]}`,
			FieldDescriptions: map[string]string{
				"leaseAppKey":           "Your application — filled from the application in view, not typed.",
				"unit":                  "The unit the application is for. Verified against the application's own appliesToUnit link.",
				"annualIncome":          "Your gross annual income. Used to derive whether income meets 3x the unit's rent; the figure itself is never shown to the landlord.",
				"employmentStatus":      "Your employment status. Used to derive an employment-verified signal.",
				"employerName":          "Optional. Kept as part of the application record; never projected.",
				"references":            "Optional. The references you are offering, one per entry — the landlord sees only the count.",
				"hasCoApplicant":        "Optional. Whether someone is applying jointly with you.",
				"coApplicantName":       "Optional. Only meaningful alongside a co-applicant.",
				"coApplicantContact":    "Optional. Only meaningful alongside a co-applicant.",
				"hasGuarantor":          "Optional. Whether a guarantor backs your application. A landlord may then verify them.",
				"guarantorName":         "Optional. Only meaningful alongside a guarantor.",
				"guarantorRelationship": "Optional. How the guarantor is related to you.",
				"guarantorAnnualIncome": "Optional. Used to derive whether the guarantor's income meets 3x rent; the figure itself is never shown.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "leaseapp",
				AuthContext: "self",
				TargetField: "leaseAppKey",
				TargetType:  "leaseapp",
				// The applicationFor link is keyed on the ACTING identity —
				// it is what the in-script owner guard consults to bind the
				// actor to their own application.
				Reads: []string{
					"{payload.leaseAppKey}",
					"lnk.leaseapp.{payload.leaseAppKey:id}.appliesToUnit.unit.{payload.unit:id}",
					"lnk.leaseapp.{payload.leaseAppKey:id}.applicationFor.identity.{actor:id}",
				},
				// A unit with no listing yet falls through to an unknown
				// income-to-rent signal rather than failing the submission.
				OptionalReads: []string{"{payload.unit}.listing"},
			},
			// refusal-courtesy(facet): UnitMismatch: none — InputSchema's "unit" is a plain string with no x-entityRef/contextParam, so Facet renders it as free text a caller can type.
		},
		// The landlord's three renewal legs. Each is consumer scope=self,
		// bound in-script by the acting identity's manages link.
		{
			OperationType: "SetRenewalTerms",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Set renewal terms",
				ShortLabel:  "Set terms",
				Description: "Set the rent and term for a renewal cycle on a unit you manage.",
				Icon:        "clipboard",
				Tone:        "primary",
				SubmitLabel: "Set terms",
				Group:       "Renewals",
			},
			// rentAmount carries a machine-readable `minimum`, not just the prose
			// "must be greater than zero": the script's own guard
			// (renewal_scripts.go — `InvalidArgument: rentAmount: required
			// positive number`) is the enforcer, and a descriptor-driven client
			// can only pre-empt it inline if the bound is a field a renderer
			// reads. Both shipped renderers do (cmd/facet/web/app.js emits
			// min=/max= from minimum/maximum; so does loftspace's catalog form),
			// so declaring it here is what keeps a landlord from learning that 0
			// is invalid only from a server round-trip.
			//
			// termMonths deliberately declares NO minimum: its real floor is the
			// package's renewal window, a policy value baked into the script at
			// init, so any constant here would be a magic number that agrees
			// with the guard only by luck — the class of drift this vocabulary
			// exists to end.
			InputSchema: `{"type":"object","properties":` +
				`{"renewalKey":{"type":"string","description":"vtx.renewal.<NanoID> of the renewal cycle."},` +
				`"rentAmount":{"type":"number","minimum":1,"title":"Monthly rent","description":"Monthly rent for the renewed term. Must be greater than zero."},` +
				`"termMonths":{"type":"integer","title":"Term (months)","description":"Renewed lease term in whole months. Must be at least the package's renewal window."}},` +
				`"required":["renewalKey","rentAmount","termMonths"]}`,
			FieldDescriptions: map[string]string{
				"renewalKey": "The renewal cycle — filled from the renewal in view, not typed.",
				"rentAmount": "Monthly rent for the renewed term.",
				"termMonths": "Whole months only — a fractional value is rejected rather than silently truncated. Must not be shorter than the renewal window, which would reopen the next cycle the moment this one signs.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "renewal",
				AuthContext: "self",
				TargetField: "renewalKey",
				TargetType:  "renewal",
				Reads:       []string{"{payload.renewalKey}"},
				// Absent until the tenant signs — its presence is what locks
				// the terms, so absence is the normal case.
				OptionalReads: []string{"{payload.renewalKey}.renewalSignature"},
			},
			// refusal-courtesy(facet): TermsLocked: none — no VisibleWhen or entity lens column distinguishes an open, unsigned renewal from one already signed/cancelled/complete; Facet offers Set terms on every row.
			// refusal-courtesy(facet): InvalidTermMonths: cap — the script raises it only for a non-integer term (a too-low integer is InvalidArgument), and schema type "integer" makes the generic form render step="1" (cmd/facet/web/app.js renderField), so the control admits integers only.
		},
		{
			OperationType: "VerifyGuarantor",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Verify tenant's guarantor",
				ShortLabel:  "Verify guarantor",
				Description: "Record that you rechecked the guarantor backing a renewal.",
				Icon:        "clipboard",
				Tone:        "neutral",
				SubmitLabel: "Verify guarantor",
				Group:       "Renewals",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"renewalKey":{"type":"string","description":"vtx.renewal.<NanoID> of the renewal cycle."},` +
				`"method":{"type":"string","title":"How you verified","description":"How the guarantor was verified, e.g. phone call, updated pay stub. Optional."}},` +
				`"required":["renewalKey"]}`,
			FieldDescriptions: map[string]string{
				"renewalKey": "The renewal cycle — filled from the renewal in view, not typed.",
				"method":     "Optional free text recording how you verified, kept alongside the verification timestamp.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "renewal",
				AuthContext: "self",
				TargetField: "renewalKey",
				TargetType:  "renewal",
				// leaseApp/applicant are the renewal's own facts, never the
				// landlord's to type: the script re-derives both from the
				// renewal's renews link and the application's applicationFor
				// link and rejects a mismatch, so a typed value could only ever
				// agree with what the graph already knows, or fail. They are
				// declared here instead: the client fills each from the renewal
				// row it opened the form from and renders no field for either,
				// which is why neither appears in InputSchema's properties or
				// its required list above.
				//
				// `{context.<field>}` names a column of the CALLER's own
				// companion row (form.mjs's doc comment), not this lens's SQL —
				// renewalsReadSpec's actual RETURN aliases are snake_case
				// (`lease_app`, renewal_lenses.go), which is not what resolves
				// here. What resolves is loftspace-app's `renewalRow` JSON shape
				// (renewals.go: `LeaseApp string \`json:"leaseApp"\``), the same
				// row `SetRenewalTerms`/`CancelRenewal`'s catalog form already
				// reads by name — `tenant` is that struct's spelling of the
				// identity this op's payload calls `applicant`. A future
				// contextParam on this lens must name the CLIENT's row shape,
				// not the lens's own RETURN alias.
				ContextParams: map[string]string{
					"leaseApp":  "{context.leaseApp}",
					"applicant": "{context.tenant}",
				},
				// Both link reads are required — the script verifies the pair
				// before it trusts the leaseApp's applicationSignals. Both
				// resolve off the contextParams above, which a client fills
				// BEFORE it substitutes any read template.
				Reads: []string{
					"{payload.renewalKey}",
					"lnk.renewal.{payload.renewalKey:id}.renews.leaseapp.{payload.leaseApp:id}",
					"lnk.leaseapp.{payload.leaseApp:id}.applicationFor.identity.{payload.applicant:id}",
				},
				// Absence is a script-visible branch, not a hydration fault: an
				// application whose profile was never (re)submitted since the
				// three-way split shipped has no .applicationSignals, and the
				// script refuses with ApplicationSignalsMissing (fail closed —
				// absent signals means UNKNOWN, not "no guarantor").
				OptionalReads: []string{"{payload.leaseApp}.applicationSignals"},
			},
			// refusal-courtesy(facet): ApplicantMismatch, LeaseAppMismatch: hide — the `{context.*}` contextParams above are the staff app's row vocabulary; Facet's opButton refuses to offer an op whose contextParam head it has no case for (unrecognisedContextTemplate).
			// refusal-courtesy(facet): NoGuarantorToVerify, ApplicationSignalsMissing: none — no entity lens column projects hasGuarantor or whether .applicationSignals exists; Facet offers Verify guarantor on every renewal row.
		},
		{
			OperationType: "CancelRenewal",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Decline this renewal",
				ShortLabel:  "Decline renewal",
				Description: "Decline a renewal cycle on a unit you manage. Terminal — a declined cycle is not reopened.",
				Icon:        "clipboard",
				Tone:        "destructive",
				SubmitLabel: "Decline renewal",
				Group:       "Renewals",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"renewalKey":{"type":"string","description":"vtx.renewal.<NanoID> of the renewal cycle being declined."},` +
				`"reason":{"type":"string","title":"Reason","description":"Why the renewal is being declined. Optional."}},` +
				`"required":["renewalKey"]}`,
			FieldDescriptions: map[string]string{
				"renewalKey": "The renewal cycle — filled from the renewal in view, not typed.",
				"reason":     "Optional rationale, recorded with the decline. TERMINAL: a declined cycle counts as this cycle's renewal and is not reopened by the expiry sweep.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "renewal",
				AuthContext: "self",
				TargetField: "renewalKey",
				TargetType:  "renewal",
				Reads:       []string{"{payload.renewalKey}"},
				// A signed cycle cannot be cancelled; absence is the normal
				// case, so this can never be a required read.
				OptionalReads: []string{"{payload.renewalKey}.renewalSignature"},
			},
			// refusal-courtesy(facet): TermsLocked: none — no VisibleWhen or entity lens column distinguishes an already-signed renewal from an open one; Facet offers Decline on every renewal row.
		},
		// SignRenewal is the tenant's completion leg — the write-path mirror of
		// VerifyGuarantor/SetRenewalTerms above, but a DIFFERENT voice: the
		// landlord ops are consumer scope=self (a landlord acting on a unit
		// they manage), while SignRenewal carries no scope=self grant at all
		// (permissions.go) — the tenant reaches it only via the §10.7 ephemeral
		// task grant the renewalComplete goal's assignTask leg mints
		// (renewal_targets.go), so Dispatch.AuthContext is "task", not "self"
		// or "standing" (the RecordIdentityPII precedent,
		// identity-domain/opmetas.go). leaseApp/applicant cannot come from the
		// task itself (assignTask carries only assignee/scopedTo/forOperation,
		// §10.5), so Dispatch.ContextParams below sources them the way a
		// descriptor-driven client sources every other field of "the entity
		// being viewed": from the renewalsRead lens row for the renewal the
		// task's scopedTo names. Reads/OptionalReads are this op's own
		// read-posture declaration — the two validation links (renews,
		// applicationFor), the required .tenancy read (renewal_scripts.go
		// SignRenewal reads it via state[], so it is class-(a) despite being
		// conditionally reached), and three absence-tolerant class-(d)
		// ordering probes (.terms, .applicationSignals,
		// .guarantorVerification).
		{
			OperationType: "SignRenewal",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Sign your lease renewal",
				ShortLabel:  "Sign renewal",
				Description: "Sign your renewal, extending your lease on the agreed terms. Final once signed.",
				Icon:        "clipboard",
				Tone:        "primary",
				SubmitLabel: "Sign renewal",
				Group:       "Renewals",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"renewalKey":{"type":"string","description":"vtx.renewal.<NanoID> of the renewal cycle being signed — the task's own subject."}},` +
				`"required":["renewalKey"]}`,
			FieldDescriptions: map[string]string{
				"renewalKey": "The renewal cycle being signed — filled from the task's own scopedTo subject, never typed.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "renewal",
				AuthContext: "task",
				TargetField: "renewalKey",
				TargetType:  "renewal",
				// The tenant signs a renewal they can already see, and leaseApp
				// and applicant are that renewal's own facts: the script rejects
				// a value disagreeing with the renews / applicationFor links
				// (LeaseAppMismatch / ApplicantMismatch), so a typed value could
				// only ever agree with what the graph already knows, or fail.
				// Declaring them here is what makes this form a single confirm
				// button: the client fills both from the renewal row and renders
				// no field for either, so neither appears in InputSchema above.
				//
				// `{context.<field>}` names a column of the CALLER's own
				// companion row (form.mjs's doc comment), not this lens's SQL —
				// renewalsReadSpec's actual RETURN aliases are snake_case
				// (`lease_app`, renewal_lenses.go), which is not what resolves
				// here. What resolves is loftspace-app's `renewalRow` JSON shape
				// (renewals.go: `LeaseApp string \`json:"leaseApp"\``), the same
				// row `SetRenewalTerms`/`CancelRenewal`'s catalog form already
				// reads by name — `tenant` is that struct's spelling of the
				// identity this op's payload calls `applicant`. A future
				// contextParam on this lens must name the CLIENT's row shape,
				// not the lens's own RETURN alias.
				ContextParams: map[string]string{
					"leaseApp":  "{context.leaseApp}",
					"applicant": "{context.tenant}",
				},
				// Every entry naming leaseApp or applicant resolves off the
				// contextParams above, which a client fills BEFORE it
				// substitutes any read template.
				Reads: []string{
					"{payload.renewalKey}",
					"lnk.renewal.{payload.renewalKey:id}.renews.leaseapp.{payload.leaseApp:id}",
					"lnk.leaseapp.{payload.leaseApp:id}.applicationFor.identity.{payload.applicant:id}",
					"{payload.leaseApp}.tenancy",
				},
				// Each is genuinely absence-tolerant: .terms is absent until the
				// landlord sets terms (NotReadyToSign), .applicationSignals is
				// absent only when SetApplicantProfile was never (re)submitted
				// since the three-way split shipped (ApplicationSignalsMissing —
				// fail-closed, never "no guarantor"), and .guarantorVerification
				// is absent until the landlord verifies one (GuarantorNotVerified,
				// checked only when .applicationSignals says hasGuarantor).
				OptionalReads: []string{
					"{payload.renewalKey}.terms",
					"{payload.leaseApp}.applicationSignals",
					"{payload.renewalKey}.guarantorVerification",
					// Absent on every lease that never gave notice; present, the
					// script refuses NoticeGiven.
					"{payload.leaseApp}.notice",
				},
			},
			// refusal-courtesy(facet): ApplicantMismatch, LeaseAppMismatch: hide — as VerifyGuarantor above: the `{context.*}` contextParams are the staff app's row vocabulary, and Facet's opButton does not offer an op whose contextParam head it cannot resolve (unrecognisedContextTemplate).
			// refusal-courtesy(facet): RenewalNotOpen, NotReadyToSign, ApplicationSignalsMissing, GuarantorNotVerified, NoTenancy, TenancyEnded, NoticeGiven: none — no VisibleWhen or entity lens column projects a renewal's status, terms, guarantor state, or its leaseapp's tenancy or notice.
		},
		// SignLease is the applicant's own leg of the convergence: the
		// assignTask target that closes missing_signature (targets.go). It is a
		// SINGLE CONFIRM — leaseAppKey is the task's own subject, so the form
		// has no field to render at all and the whole descriptor exists to say
		// "press this button, and here is the envelope it sends".
		//
		// AuthContext is "task", and that is the only value that WORKS for the
		// person who actually signs: the grant matrix above gives SignLease to
		// `operator` alone, so an applicant holds no standing grant and reaches
		// the op solely through the §10.7 ephemeral task grant, which step 3
		// matches on {task, target}. A "standing" descriptor would send no
		// authContext and be refused every time — the RecordIdentityPII /
		// SignRenewal precedent, for the same reason in all three cases.
		//
		// Reads is the single subject key the script hydrates to prove the
		// application is alive. The `.signature` aspect is deliberately NOT
		// declared: it is absent on every first sign (its absence IS the
		// condition that permits the write), so it could only ever be an
		// absence-tolerant read, and the CreateOnly guard on the aspect already
		// rejects a second sign without it.
		{
			OperationType: "SignLease",
			// refusal-courtesy(facet): AlreadySigned, UnitNoLongerAvailable: none — no VisibleWhen or entity lens column projects a leaseapp's signature or decision/unit-status state; Facet offers Sign lease on every task for this op regardless.
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Sign your lease",
				ShortLabel:  "Sign lease",
				Description: "Sign the lease for the application you were approved on. Once only — a signed application cannot be re-signed.",
				Icon:        "clipboard",
				Tone:        "primary",
				SubmitLabel: "Sign lease",
				Group:       "My applications",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the application being signed — the task's own subject."}},` +
				`"required":["leaseAppKey"]}`,
			FieldDescriptions: map[string]string{
				"leaseAppKey": "The application being signed — filled from the task's own scopedTo subject, never typed. There is nothing else to fill in: signing IS the whole operation.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "leaseapp",
				AuthContext: "task",
				TargetField: "leaseAppKey",
				TargetType:  "leaseapp",
				Reads:       []string{"{payload.leaseAppKey}"},
				// .decision is absence-tolerant (the common not-yet-decided
				// case) -- the script re-verifies the unit is still available
				// to THIS application (unit leased to a rival, or tombstoned)
				// before honoring an already-dispatched grant, scripts.go.
				OptionalReads: []string{"{payload.leaseAppKey}.decision"},
				// The script resolves both the applied-to unit (leaseapp_unit,
				// the re-verification above) and the applicant (to snapshot
				// .tenantName) via the application's OWN links, never a
				// payload field -- the same forgery-resistance rationale as
				// ReassignLeaseUnit's own walks, which this mirrors exactly.
				// A descriptor-driven client walks both here rather than
				// trusting a payload-templated hub it cannot form ahead of
				// dispatch.
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{payload.leaseAppKey}", Relation: "appliesToUnit", Direction: "out"},
					{Hub: "{payload.leaseAppKey}", Relation: "applicationFor", Direction: "out"},
				},
			},
		},
		// Engine legs — externalTask instanceOp/replyOp/dispatchOp — that
		// exist only so forOperation resolves.
		{OperationType: "CreateLeaseServiceInstance"},
		{OperationType: "RecordLeaseServiceOutcome"},
		{OperationType: "RecordServiceDispatch"},
		{
			// Also an engine leg (operator/Scope:"any", granted above to
			// Loom's relay actor alone via the script's own actor-guard) — no
			// Presentation: it is never offered to a person, so S1's
			// descriptorGaps / lint-package-standard's checkReadTemplates
			// human-facing gate does not apply. InputSchema exists here only
			// so `required` can GUARANTEE subjectKey is present, which is
			// what lets Dispatch below template a key around it
			// (lint-package-standard's checkReadTemplates: a template built
			// around a payload field nothing guarantees present is an
			// authoring error).
			OperationType: "CreateLeaseDocInstance",
			InputSchema: `{"type":"object","properties":` +
				`{"instanceKey":{"type":"string"},"subjectKey":{"type":"string"},"adapter":{"type":"string"},` +
				`"replyOp":{"type":"string"},"params":{"type":"object"}},` +
				`"required":["instanceKey","subjectKey","adapter","replyOp"]}`,
			Dispatch: &pkgmgr.OpDispatchSpec{
				// tenantName is a SUBJECT-own aspect the leaseDocument
				// pattern templates into egressReads (Loom's
				// inferExternalTaskReads), so it is a floored egress key, not
				// a floored plain read (descriptor_floor.go's precedence
				// note 3): its PRESENCE still authors a $sensitiveRef the
				// same as an undeclared op would, and only its ABSENCE
				// becomes tolerant (EgressAbsenceTolerant) instead of
				// HydrationMiss — a signed application with no .tenantName
				// snapshot renders the bare applicant key, rather than
				// failing the whole dispatch.
				OptionalReads: []string{"{payload.subjectKey}.tenantName"},
			},
		},
		{OperationType: "RecordLeaseDocOutcome"},
	}
}
