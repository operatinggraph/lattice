package leasesigning

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// Grant matrix:
//
//	CreateLeaseApplication          → operator
//	CreateLeaseApplication (self)   → consumer
//	CreateLeaseServiceInstance      → operator
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
//   - SetRenewalTerms / VerifyGuarantor / SignRenewal — REQUIRED: the three
//     assignTask operations the renewalComplete goal's actions catalog binds
//     (renewal_targets.go); the Weaver Actuator resolves forOperation to each
//     op-meta when it creates the remediation task. CancelRenewal is task-less
//     (a directOp/operator action, never an assignTask target), so its meta is
//     owed to S1 rather than to forOperation resolution. All four carry full
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
				// NotReadyToApprove check). The unit + its .listing are NOT declared
				// here at all — the script resolves them itself from the
				// application's own appliesToUnit link (a class-(e) follow-up,
				// scripts.go), never a payload field, so there is no
				// only-present-on-approve field left to build a malformed key
				// around on a decline (Standard §readTemplateDebt).
				OptionalReads: []string{
					"{payload.leaseAppKey}.decision",
					"{payload.leaseAppKey}.tenancy",
					"{payload.leaseAppKey}.signature",
				},
			},
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
				`"leaseTermMonths":{"type":"integer","title":"Lease term (months)","description":"Requested lease term in months. Required when moveInDate is supplied."},` +
				`"requestedRent":{"type":"number","title":"Monthly rent","description":"Optional rent the applicant is offering."}},` +
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
				// never be a required read.
				OptionalReads: []string{
					"lnk.identity.{payload.applicant:id}.appliedToUnit.unit.{payload.unit:id}",
				},
			},
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
				// The guard link being freed may already be tombstoned.
				OptionalReads: []string{
					"lnk.identity.{payload.applicant:id}.appliedToUnit.unit.{payload.unit:id}",
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
				`"references":{"type":"integer","title":"References","description":"Number of references offered. Optional."},` +
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
				"references":            "Optional. How many references you are offering — the landlord sees the count.",
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
				},
			},
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
			},
		},
		// Engine legs — externalTask instanceOp/replyOp/dispatchOp — that
		// exist only so forOperation resolves.
		{OperationType: "CreateLeaseServiceInstance"},
		{OperationType: "RecordLeaseServiceOutcome"},
		{OperationType: "RecordServiceDispatch"},
		{OperationType: "CreateLeaseDocInstance"},
		{OperationType: "RecordLeaseDocOutcome"},
	}
}
