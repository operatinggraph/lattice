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
// PurgeIdentityDedupFootprint carries the same posture one step further out:
// its submitters are the pattern's fourth step AND the erasure target's gap
// action, so it is reached by identity.system.loom and identity.system.weaver
// alike — both operator via holdsRole. Like the seal it ships no OpMeta
// descriptor, and it refuses any subject without a live erasureRequested
// marker of that class, so the grant confers nothing a completed seal has not
// already exercised.
//
// SealIdentityForErasureComplete is the only one of the four whose submitter is
// the Weaver ALONE — it is the identityErasureComplete target's terminal gap
// action and no Loom step runs it, because the pattern cannot know when the
// convergent tail has drained. Same operator grant at scope:any for the same
// mechanical reason (grants attach to roles, and identity.system.weaver reaches
// operator via holdsRole). The grant is unusually inert for a scope:any one: the
// op writes nothing unless the identity carries a real erasureRequested marker,
// a shredded envelope whose Vault destruction and projection nullification have
// both landed, and no live residue link on any of five relations — so an actor
// holding it can attest an erasure that already happened and nothing else.
//
// Granting to operator does reach the other operator-equivalent service actors
// (Bridge, object-store-manager, privacy) as well as human operators. That is
// the same breadth identity-hygiene's MergeIdentity grant carries, and it
// confers no authority a completed shred has not already exercised: the op
// fail-closes unless ShredIdentityKey has committed for that identity.
//
// ShredRetentionClassKey ships NO grant either, and for a reason that only
// LOOKS like the one below. ShredIdentityKey is ungranted because erasure is a
// subject's request and a deployment's consent decision. ShredRetentionClassKey
// is ungranted because it is the data CONTROLLER's scheduled act: it destroys
// every record a class holds at once, for subjects who never asked for anything
// and may have no idea the class exists. It is the widest-blast-radius verb in
// this package, and a deployment should have to say so on purpose.
//
// Withholding the grant is a DEFAULT, and the platform now supplies the
// boundary underneath it. `operator` holds CreatePermission and
// GrantPermission at scope:any (packages/rbac-domain), and CreatePermission
// takes operationType as a free string with no allow-list, so an operator can
// still mint themselves a ShredRetentionClassKey permission in two ops — but
// the resulting vertex is stamped `data.origin: "runtime"` at mint, the
// capabilityRoles lens projects that stamp onto the grant entry, and
// ShredRetentionClassKey is in the core-owned reserved set
// (`reservedOperationTypes`, internal/processor/step3_auth_capability.go).
// Step 3 refuses any runtime-origin entry naming it and raises a
// `reserved-operation-grant-rejected` Health alert, so the self-mint route
// does not reach the verb and does not go unseen.
//
// What the reservation does NOT do is forbid the verb: a package remains free
// to declare a PermissionSpec for it, because an installer-minted vertex is
// stamped `origin: "package"` and passes the same check. That asymmetry is the
// point — reaching this verb should be a deliberate, manifest-recorded,
// uninstallable deployment decision, and privacy-base withholds the grant by
// choice rather than by platform inability.
//
// Its finalization sibling above IS granted: recording that an
// already-committed destruction finished confers no authority to start one.
//
// ShredIdentityKey itself deliberately ships NO grant here: right-to-erasure
// is an operator/consent decision whose grant posture belongs to the
// deployment (a vertical package or an explicit operator provisioning step),
// not a platform default — privacy-base only defines the mechanism. The seal
// differs because it is not the decision: it records one already taken, and
// it is inert until a shred has committed (the op fail-closes otherwise), so
// it can carry no erasure authority the shred did not already exercise.
//
// That posture is what STEP 1 of this package's own identityErasure pattern
// rests on, and it is worth naming here rather than leaving to be rediscovered:
// packages/privacy-operator-grant ships the ShredIdentityKey grant to operator
// at scope:any as an explicit, revertible deployment decision (Andrew
// 2026-07-04), and identity.system.loom reaches operator by holdsRole exactly
// as it does for the three grants below. A deployment installing privacy-base
// WITHOUT privacy-operator-grant therefore gets a pattern whose steps 2 through
// 4 are authorized and whose first is not — the instance fails its step-1
// deadline probe rather than running an unauthorized erasure, which is the
// right direction to fail, but the grant is a precondition of the pattern doing
// anything at all, not an optional extra.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "RecordShredFinalization",
			Scope:         "any",
			Note:          "Authorizes the privacy service actor (identity.system.privacy, operator-equivalent) to durably record crypto-shred finalization progress (vault-crypto-shredding-design.md Fire 4b).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "RecordRetentionClassShredFinalization",
			Scope:         "any",
			Note: "Authorizes the privacy service actor (identity.system.privacy, operator-equivalent) to durably record retention-class key-destruction progress (retention-class-key-custody-design.md §4.3/§4.4). " +
				"[no-op-meta: engine-op — submitted by the privacy-worker after Vault.ShredKey and by the Refractor after every affected secure lens is rebuilt; no person chooses it from a form, and it refuses outright unless a ShredRetentionClassKey has already committed for that holder.]",
			GrantsTo: []string{"operator"},
		},
		{
			OperationType: "SealIdentityForErasure",
			Scope:         "any",
			Note: "Authorizes the Loom service actor (operator-equivalent) to seal an already-shredded identity for erasure, closing its write path (erasure-orchestration-design.md §6). " +
				"[no-op-meta: engine-op — the second step of the identityErasure Loom pattern; no person chooses it from a form, and it refuses outright unless a shred has already committed.]",
			GrantsTo: []string{"operator"},
		},
		{
			OperationType: "PurgeIdentityDedupFootprint",
			Scope:         "any",
			Note: "Authorizes the Loom and Weaver service actors (operator-equivalent) to sweep an erasure-sealed identity's dedup footprint — its owned identityindex vertices and its duplicateOf pair links (erasure-orchestration-design.md §5.4 step 4). " +
				"[no-op-meta: engine-op — the fourth step of the identityErasure Loom pattern and a gap action of the identityErasureComplete target; no person chooses it from a form, and it refuses outright unless the identity carries a live erasureRequested marker of that class.]",
			GrantsTo: []string{"operator"},
		},
		{
			OperationType: "SealIdentityForErasureComplete",
			Scope:         "any",
			Note: "Authorizes the Weaver service actor (operator-equivalent) to write an identity's erasure-completion attestation after re-verifying its residue in the same commit (erasure-orchestration-design.md §7.2). " +
				"[no-op-meta: engine-op — the terminal gap action of the identityErasureComplete target; no person chooses it from a form, and it refuses outright unless the erasure has already converged on every relation it walks.]",
			GrantsTo: []string{"operator"},
		},
	}
}
