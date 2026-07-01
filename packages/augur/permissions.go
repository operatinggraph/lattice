package augur

import "github.com/asolgan/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// All four ops are operator-driven — Weaver (the directOp dispatcher / the
// augurDispatch two-op fire), the bridge service actor, and the human reviewer
// are operator-equivalent (holdsRole → operator, exactly like the
// orchestration-base service actors), so each op is granted to operator at
// scope:any — the same operator-grant idiom orchestration-base / lease-signing
// use for their instanceOp/replyOp pairs:
//
//   - CreateAugurReasoningClaim — Weaver submits this directOp (Option F) when a
//     gap escalates; it mints the claim vertex write-ahead of the reasoning call
//     and emits external.augur for the bridge.
//   - RecordProposal — the bridge's service actor submits the replyOp that
//     records the proposal verdict.
//   - ReviewProposal — a human operator submits the verdict that flips a pending
//     proposal to approved | rejected (re-validated on approve).
//   - RecordProposalDispatch — Weaver submits this flip as the second op of the
//     augurDispatch target's two-op dispatch (design Fire 2b §3.3), recording
//     whether the proposed remediation fired.
//
// All are target-less for auth (the directOp/replyOp posture, Contract #10
// §10.4): auth keys on operationType + actor, so the operator grant authorizes
// the submit. Weaver also holds the `system` lane (the protected kernel-actor
// lane grant, Contract #2 §2.3) under which it dispatches the directOp.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateAugurReasoningClaim",
			Scope:         "any",
			Note:          "Authorizes Weaver (operator-equivalent, holdsRole → operator) to submit the directOp that mints the Augur claim vertex + emits external.augur (Option F; escalation-dispatch addendum §7).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "RecordProposal",
			Scope:         "any",
			Note:          "Authorizes the bridge replyOp (identity:bridge, operator-equivalent) to record an Augur proposal vertex (design §3.2 / §5).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "ReviewProposal",
			Scope:         "any",
			Note:          "Authorizes a human operator to render the approve/reject verdict on a pending Augur proposal (design §3.2). The reviewer is the trusted submitting actor (op.actor); the verdict is recorded on the proposal's .review aspect + a reviewedBy link.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "RecordProposalDispatch",
			Scope:         "any",
			Note:          "Authorizes Weaver (operator-equivalent) to submit the dispatched-flip that closes the augurDispatch target's two-op dispatch (design Fire 2b §3.3): approved → dispatched | invalid.",
			GrantsTo:      []string{"operator"},
		},
	}
}
