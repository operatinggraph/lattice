package privacyoperatorgrant

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions grants ShredIdentityKey to the `operator` role.
//
// Scope "any": ShredIdentityKey is a directOp — target-less (no
// authContext.target); auth keys on operationType + actor, exactly like
// privacy-base's RecordShredFinalization grant and orchestration-base's
// MarkExpired. Granting to `operator` reaches every operator-role holder: the
// primordial admin (Loupe's operator actor — the intended F12 shred-proof
// submitter) and the platform engine service actors (Loom / Weaver / Bridge /
// object-store-manager), which Andrew accepted (2026-07-04) as the cost of the
// role-wide grant over a narrower Loupe-only role.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "ShredIdentityKey",
			Scope:         "any",
			Note: "Authorizes the operator role to submit ShredIdentityKey (right-to-erasure). Ships separately " +
				"from privacy-base so the grant is an explicit, revertible deployment decision " +
				"(vault-crypto-shredding-design.md; loupe-platform-edges-ux.md §6 ask F; Andrew 2026-07-04).",
			GrantsTo: []string{"operator"},
		},
	}
}
