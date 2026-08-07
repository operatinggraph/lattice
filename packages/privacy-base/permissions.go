package privacybase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// RecordShredFinalization is posted by the identity.system.privacy service
// actor (the Fire-4b shred-finalization listeners in internal/privacyworker
// and internal/refractor/keyshredded), which is operator-equivalent
// (holdsRole → operator, exactly like the Loom/Weaver/Bridge/objmgr service
// actors), so it is granted to operator at scope:any — the same
// operator-grant idiom orchestration-base's MarkExpired uses for Weaver's
// temporal-lane submissions. The op is target-less (no authContext.target —
// the directOp posture); auth keys on operationType + actor.
//
// SealIdentityForErasure is granted the same way and for the same reason: its
// submitter is the Loom step that runs it as the identityErasure pattern's
// second step, under identity.system.loom, which holds operator via holdsRole
// — and grants attach to roles, not to actor keys, so operator is how a
// service actor is reached at all. (No Weaver gap dispatches this op: §7.2's
// erasure target dispatches SealIdentityForErasureComplete, a different verb.)
// It ships NO OpMeta descriptor: this is machinery, with no person-facing
// affordance, and an operator submitting it by hand is exercising the same
// authority they already hold over MergeIdentity.
//
// Granting to operator does reach the other operator-equivalent service actors
// (Bridge, object-store-manager, privacy) as well as human operators. That is
// the same breadth identity-hygiene's MergeIdentity grant carries, and it
// confers no authority a completed shred has not already exercised: the op
// fail-closes unless ShredIdentityKey has committed for that identity.
//
// ShredIdentityKey itself deliberately ships NO grant here: right-to-erasure
// is an operator/consent decision whose grant posture belongs to the
// deployment (a vertical package or an explicit operator provisioning step),
// not a platform default — privacy-base only defines the mechanism. The seal
// differs because it is not the decision: it records one already taken, and
// it is inert until a shred has committed (the op fail-closes otherwise), so
// it can carry no erasure authority the shred did not already exercise.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "RecordShredFinalization",
			Scope:         "any",
			Note:          "Authorizes the privacy service actor (identity.system.privacy, operator-equivalent) to durably record crypto-shred finalization progress (vault-crypto-shredding-design.md Fire 4b).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "SealIdentityForErasure",
			Scope:         "any",
			Note: "Authorizes the Loom service actor (operator-equivalent) to seal an already-shredded identity for erasure, closing its write path (erasure-orchestration-design.md §6). " +
				"[no-op-meta: engine-op — the second step of the identityErasure Loom pattern; no person chooses it from a form, and it refuses outright unless a shred has already committed.]",
			GrantsTo: []string{"operator"},
		},
	}
}
