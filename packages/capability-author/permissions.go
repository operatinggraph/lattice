package capabilityauthor

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// Every op is granted to `operator` at scope:any — the same operator-grant
// idiom augur/orchestration-base/lease-signing use for their instanceOp/replyOp
// pairs:
//
//   - RequestCapabilityAuthoring — a human operator submits this. The design's
//     standing posture narrows this to a distinct `identity.ai.*` agent's own
//     grant once that agent identity is seeded (Architecture Item 4) —
//     granting to `operator` here is an accepted interim widening (a
//     human requesting authoring is itself a legitimate, human-in-the-loop-safe
//     action; it is the AI's narrower grant that remains to be seeded).
//   - CreateAuthoringClaim — Loom's relay actor (operator-equivalent), the same
//     idiom lease-signing's CreateLeaseServiceInstance uses.
//   - SubmitCapabilityProposal — a human operator submits an artifact they
//     composed themselves (the Weaver Target Studio's canvas), having run the
//     §5 materializer client-side first. Same trust posture as
//     RecordCapabilityProposal: this grant lets a caller put an artifact into
//     the review queue, never through it — ReviewCapabilityProposal's human
//     gate is unchanged and unbypassable from here.
//   - RecordCapabilityProposal — the trusted submitter that has already run the
//     §5 materializer (the bridge, in the full design); modeled here as
//     operator-equivalent, mirroring augur's RecordProposal.
//   - RecordAuthoringDispatch — the trusted submitter (the bridge, once its
//     real adapter returns Pending) records the pending-dispatch marker for
//     an in-flight escalation call. Same operator-equivalent posture as
//     RecordCapabilityProposal, mirroring lease-signing's RecordServiceDispatch.
//   - ReviewCapabilityProposal — a human operator submits the verdict that
//     flips a pending proposal to approved/rejected (design §3.3), mirroring
//     augur's ReviewProposal.
//   - MarkCapabilityProposalApplied — the operator submits this after
//     separately applying the proposal through the existing F-004
//     InstallPackage/UpgradePackage op (design §3.5); it only records the
//     applied-flip, never installs/upgrades anything itself.
//   - RecordCapabilityInstallReceipt — the same operator apply path submits
//     this between the F-004 install/upgrade and the applied-flip, recording
//     which commit produced the package. It writes one create-only aspect on
//     the proposal's own vertex and installs nothing, so it grants no reach
//     the mark-applied grant does not already carry.
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
			OperationType: "SubmitCapabilityProposal",
			Scope:         "any",
			Note:          "Authorizes a human operator to submit a capability artifact they composed themselves into the review queue, with no authoring-claim indirection (weaver-target-studio-design.md §6.4). The submitted §5 verdict is self-attested (the submitter is also the party a grant-kind scope check constrains), so the containment that matters is the APPROVE-time re-validation: cmd/loupe's freshCapabilityVerdict and cmd/lattice/capability's freshApprovalVerdict re-run pkgmgr.ValidateCapabilityArtifact server-side and, for kind=grant, read the requester's LIVE held permissions — which for this op is op.actor, the real submitter. The kernel's protected-key guard is NOT that backstop: it skips creates, and a grant artifact installs new vertices.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "RecordCapabilityProposal",
			Scope:         "any",
			Note:          "Authorizes the trusted submitter that has already run the §5 deterministic-validation materializer (the bridge, in the full design) to record a capability proposal verdict.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "RecordAuthoringDispatch",
			Scope:         "any",
			Note:          "Authorizes the trusted submitter (the bridge, once its real capabilityAuthor adapter returns Pending) to record the pending-dispatch marker for an in-flight escalation call (design §3.4, the async DispatchOp — mirrors lease-signing's RecordServiceDispatch).",
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
		{
			OperationType: "RecordCapabilityInstallReceipt",
			Scope:         "any",
			Note:          "Authorizes a human operator to record which install/upgrade commit produced an approved proposal's package — {packageKey, installRequestId, recordedAt} on a create-only .install aspect (capability-proposal-install-receipt-design.md §2). Same guards as MarkCapabilityProposalApplied (approved-only, live name-matched package); it flips no state and installs nothing.",
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
		{OperationType: "SubmitCapabilityProposal"},
		{OperationType: "RecordCapabilityProposal"},
		{OperationType: "RecordAuthoringDispatch"},
		{OperationType: "ReviewCapabilityProposal"},
		{OperationType: "MarkCapabilityProposalApplied"},
		{OperationType: "RecordCapabilityInstallReceipt"},
	}
}
