package identityhygiene

import "github.com/asolgan/lattice/internal/pkgmgr"

// Permissions returns the `MergeIdentity` permission vertex + its grant
// to the operator role. The grant link is built at install time by the
// pkgmgr installer; the role canonical name `operator` is resolved by
// the cmd/lattice-pkg dispatcher to the seeded RoleOperator NanoID from
// `lattice.bootstrap.json`.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "MergeIdentity",
			Scope:         "any",
			Note:          "Grants the holder the right to merge two duplicate-candidate identities.",
			GrantsTo:      []string{"operator"},
		},
	}
}
