package capabilityauthor

import "github.com/asolgan/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// Both ops are granted to `operator` at scope:any — the same operator-grant
// idiom augur/orchestration-base use for their instanceOp/replyOp pairs:
//
//   - RequestCapabilityAuthoring — a human operator submits this in Increment
//     1. The design's standing posture narrows this to a distinct
//     `identity.ai.*` agent's own grant once the escalation-dispatch increment
//     seeds that agent identity (Architecture Item 4) — granting to `operator`
//     here is an interim widening this increment accepts (a human requesting
//     authoring is itself a legitimate, human-in-the-loop-safe action; it is
//     the AI's narrower grant that remains to be seeded).
//   - RecordCapabilityProposal — the trusted submitter that has already run the
//     §5 materializer (the bridge, in the full design); modeled here as
//     operator-equivalent, mirroring augur's RecordProposal.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "RequestCapabilityAuthoring",
			Scope:         "any",
			Note:          "Authorizes an operator to request AI-authored capability authoring (design §3.3). Narrows to a dedicated identity.ai.* grant once the escalation-dispatch increment lands.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "RecordCapabilityProposal",
			Scope:         "any",
			Note:          "Authorizes the trusted submitter that has already run the §5 deterministic-validation materializer (the bridge, in the full design) to record a capability proposal verdict.",
			GrantsTo:      []string{"operator"},
		},
	}
}
