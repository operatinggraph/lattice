package leasesigning

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
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
			OperationType: "DecideLeaseApplication",
			Scope:         "any",
			Note:          "Grants the operator and front-of-house staff the right to submit DecideLeaseApplication (approve / decline an application via the trusted-tool app — the human gate the listing-flip waits behind; the front-desk \"applications to review\" beat).",
			GrantsTo:      []string{"operator", "frontOfHouse"},
		},
		{
			OperationType: "SetApplicantProfile",
			Scope:         "any",
			Note:          "Grants the operator the right to submit SetApplicantProfile (the applicant records their qualification profile via the trusted-tool app — income / employment / references / co-applicant / guarantor; same operator model as SignLease).",
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
			OperationType: "VerifyGuarantor",
			Scope:         "any",
			Note:          "Grants the operator the right to submit VerifyGuarantor; the landlord performs it via the §10.7 ephemeral task grant (same operator model as SignLease).",
			GrantsTo:      []string{"operator"},
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
	}
}

// OpMetas declares the op-meta vertices that make ops forOperation-resolvable.
//
//   - SignLease — REQUIRED: the assignTask operation the §10.8 playbook binds;
//     the Weaver Actuator resolves forOperation to its op-meta when it creates
//     the remediation task. Its absence would break the missing_signature gap.
//   - RecordIdentityPII — REQUIRED: the onboarding pattern's userTask step
//     resolves forOperation to this op-meta live at submit (Loom's opMetaKey).
//     identity-domain ships the op but no op-meta for it, so the onboarding
//     pattern declares it here.
//   - CreateLeaseServiceInstance / RecordLeaseServiceOutcome /
//     RecordServiceDispatch / CreateLeaseDocInstance / RecordLeaseDocOutcome —
//     declared for discoverability + the manifest cross-check. The engine
//     resolves the externalTask instanceOp/replyOp from the step strings
//     directly (and the bridge selects the dispatchOp from the event body), not
//     via forOperation, so these are hygiene, not strictly required.
//   - SetRenewalTerms / VerifyGuarantor / SignRenewal — REQUIRED: the three
//     assignTask operations the renewalComplete goal's actions catalog binds
//     (renewal_targets.go); the Weaver Actuator resolves forOperation to each
//     op-meta when it creates the remediation task. CancelRenewal is
//     task-less (a directOp/operator action, never an assignTask target) so it
//     needs no op-meta.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		// DecideLeaseApplication is the front-desk demo beat ("Applications to
		// review"), and the first op meta here to carry the full descriptor
		// vocabulary: a staff client builds the entire submission — form, labels,
		// declared reads — from this vertex alone.
		//
		// authContext "standing" is the fourth and oldest authorization case: the
		// caller sends no authContext object at all, because their authority is a
		// standing role grant rather than a self / service / task relationship.
		// Every operator FE has always submitted this way; naming it lets a
		// data-driven client do the same instead of special-casing the absence.
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
				`"decision":{"type":"string","enum":["approved","declined"],"description":"The decision. Terminal once recorded."},` +
				`"reason":{"type":"string","description":"Why the application was declined. Ignored on an approve."},` +
				`"unit":{"type":"string","description":"vtx.unit.<NanoID> this application is for. Required on the FIRST approve."}},` +
				`"required":["leaseAppKey","decision"]}`,
			FieldDescriptions: map[string]string{
				"leaseAppKey": "The application being decided — filled from the application in view, not typed.",
				"decision":    "Approve or decline. TERMINAL: the same value re-submits harmlessly, but a different value is rejected, so a decision can never silently flip.",
				"reason":      "Optional rationale shown to the applicant on a decline, and kept as a fair-housing record. Ignored on an approve.",
				"unit":        "The unit this application is for. Required on the first approve, which stamps the tenancy dates read off the unit's listing; not needed on a decline or a re-approve.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "leaseapp",
				AuthContext: "standing",
				TargetField: "leaseAppKey",
				TargetType:  "leaseapp",
				Reads:       []string{"{payload.leaseAppKey}"},
				// Each of these is genuinely absence-tolerant, which is why none is
				// a required Read: .decision and .tenancy are absent on a first
				// decision (they ARE the read-before-write terminal and create-only
				// guards), .signature is absent on an unsigned application (the
				// NotReadyToApprove check), and the unit pair is consulted only on
				// the first approve. A decline that declared them required would
				// fail on keys that are correctly missing.
				OptionalReads: []string{
					"{payload.leaseAppKey}.decision",
					"{payload.leaseAppKey}.tenancy",
					"{payload.leaseAppKey}.signature",
					"lnk.leaseapp.{payload.leaseAppKey:id}.appliesToUnit.unit.{payload.unit:id}",
					"{payload.unit}",
					"{payload.unit}.listing",
				},
			},
		},
		{OperationType: "SignLease"},
		{OperationType: "RecordIdentityPII"},
		{OperationType: "CreateLeaseServiceInstance"},
		{OperationType: "RecordLeaseServiceOutcome"},
		{OperationType: "RecordServiceDispatch"},
		{OperationType: "CreateLeaseDocInstance"},
		{OperationType: "RecordLeaseDocOutcome"},
		{OperationType: "SetApplicantProfile"},
		{OperationType: "SetRenewalTerms"},
		{OperationType: "VerifyGuarantor"},
		{OperationType: "SignRenewal"},
	}
}
