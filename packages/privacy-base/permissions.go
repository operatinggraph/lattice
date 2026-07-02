package privacybase

import "github.com/asolgan/lattice/internal/pkgmgr"

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
// ShredIdentityKey itself deliberately ships NO grant here: right-to-erasure
// is an operator/consent decision whose grant posture belongs to the
// deployment (a vertical package or an explicit operator provisioning step),
// not a platform default — privacy-base only defines the mechanism.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "RecordShredFinalization",
			Scope:         "any",
			Note:          "Authorizes the privacy service actor (identity.system.privacy, operator-equivalent) to durably record crypto-shred finalization progress (vault-crypto-shredding-design.md Fire 4b).",
			GrantsTo:      []string{"operator"},
		},
	}
}
