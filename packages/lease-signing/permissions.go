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
			OperationType: "SignLease",
			Scope:         "any",
			Note:          "Grants the operator the right to submit SignLease; the applicant signs via the ephemeral task grant (§10.7).",
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
//   - CreateLeaseServiceInstance / RecordLeaseServiceOutcome — declared for
//     discoverability + the manifest cross-check. The engine resolves the
//     externalTask instanceOp/replyOp from the step strings directly (not via
//     forOperation), so these are hygiene, not strictly required.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: "SignLease"},
		{OperationType: "RecordIdentityPII"},
		{OperationType: "CreateLeaseServiceInstance"},
		{OperationType: "RecordLeaseServiceOutcome"},
	}
}
