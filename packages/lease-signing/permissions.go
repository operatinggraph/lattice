package leasesigning

import "github.com/asolgan/lattice/internal/pkgmgr"

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
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateLeaseApplication",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreateLeaseApplication operations.",
			GrantsTo:      []string{"operator"},
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
			Note:          "Grants the operator the right to submit DecideLeaseApplication (the landlord approves / declines an application via the trusted-tool app — the human gate the listing-flip waits behind; same operator model as SignLease).",
			GrantsTo:      []string{"operator"},
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
//     RecordServiceDispatch — declared for discoverability + the manifest
//     cross-check. The engine resolves the externalTask instanceOp/replyOp from
//     the step strings directly (and the bridge selects the dispatchOp from the
//     event body), not via forOperation, so these are hygiene, not strictly
//     required.
//   - SetRenewalTerms / VerifyGuarantor / SignRenewal — REQUIRED: the three
//     assignTask operations the renewalComplete goal's actions catalog binds
//     (renewal_targets.go); the Weaver Actuator resolves forOperation to each
//     op-meta when it creates the remediation task. CancelRenewal is
//     task-less (a directOp/operator action, never an assignTask target) so it
//     needs no op-meta.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: "SignLease"},
		{OperationType: "RecordIdentityPII"},
		{OperationType: "CreateLeaseServiceInstance"},
		{OperationType: "RecordLeaseServiceOutcome"},
		{OperationType: "RecordServiceDispatch"},
		{OperationType: "SetApplicantProfile"},
		{OperationType: "SetRenewalTerms"},
		{OperationType: "VerifyGuarantor"},
		{OperationType: "SignRenewal"},
	}
}
