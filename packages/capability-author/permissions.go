package capabilityauthor

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// All three ops are granted to `operator` at scope:any — the same
// operator-grant idiom augur/orchestration-base/lease-signing use for their
// instanceOp/replyOp pairs:
//
//   - RequestCapabilityAuthoring — a human operator submits this. The design's
//     standing posture narrows this to a distinct `identity.ai.*` agent's own
//     grant once that agent identity is seeded (Architecture Item 4) —
//     granting to `operator` here is an interim widening this fire accepts (a
//     human requesting authoring is itself a legitimate, human-in-the-loop-safe
//     action; it is the AI's narrower grant that remains to be seeded).
//   - CreateAuthoringClaim — Loom's relay actor (operator-equivalent), the same
//     idiom lease-signing's CreateLeaseServiceInstance uses.
//   - RecordCapabilityProposal — the trusted submitter that has already run the
//     §5 materializer (the bridge, in the full design); modeled here as
//     operator-equivalent, mirroring augur's RecordProposal.
//   - ReviewCapabilityProposal — a human operator submits the verdict that
//     flips a pending proposal to approved/rejected (design §3.3), mirroring
//     augur's ReviewProposal.
//   - MarkCapabilityProposalApplied — the operator submits this after
//     separately applying the proposal through the existing F-004
//     InstallPackage/UpgradePackage op (design §3.5); it only records the
//     applied-flip, never installs/upgrades anything itself.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "RequestCapabilityAuthoring",
			Scope:         "any",
			Note:          "Authorizes an operator to request AI-authored capability authoring (design §3.3). Narrows to a dedicated identity.ai.* grant once that agent identity is seeded.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "CreateAuthoringClaim",
			Scope:         "any",
			Note:          "Authorizes Loom's relay actor to submit the escalation-dispatch instanceOp (design §3.4).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "RecordCapabilityProposal",
			Scope:         "any",
			Note:          "Authorizes the trusted submitter that has already run the §5 deterministic-validation materializer (the bridge, in the full design) to record a capability proposal verdict.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "ReviewCapabilityProposal",
			Scope:         "any",
			Note:          "Authorizes a human operator to approve or reject a pending capability proposal (design §3.3).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "MarkCapabilityProposalApplied",
			Scope:         "any",
			Note:          "Authorizes a human operator to record that an approved proposal has been applied via a separate F-004 install/upgrade (design §3.5).",
			GrantsTo:      []string{"operator"},
		},
	}
}

// OpMetas declares the op-meta vertices that make ops forOperation-resolvable.
// The engine resolves the externalTask instanceOp/replyOp from the Loom step
// strings directly (mirrors lease-signing's CreateLeaseServiceInstance /
// RecordLeaseServiceOutcome) — these entries are hygiene + the manifest
// cross-check, not strictly required for dispatch.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: "RequestCapabilityAuthoring"},
		{OperationType: "CreateAuthoringClaim"},
		{OperationType: "RecordCapabilityProposal"},
		{OperationType: "ReviewCapabilityProposal"},
		{OperationType: "MarkCapabilityProposalApplied"},
	}
}
