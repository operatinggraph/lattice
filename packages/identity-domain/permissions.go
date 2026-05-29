package identitydomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// Permissions returns the 3 identity-domain permission vertices.
//
// Grant matrix:
//
//	CreateUnclaimedIdentity → frontOfHouse, backOfHouse, operator
//	UpdateIdentityState     → operator
//	ClaimIdentity (self)    → consumer
//
// Scope `self` for ClaimIdentity: platformPermissions[] match is
// exact-operationType only; scope enforcement happens in the Starlark
// `ClaimIdentity` branch (one-credential-one-identity via credentialindex).
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateUnclaimedIdentity",
			Scope:         "any",
			Note:          "Grants the right to create an unclaimed identity vertex.",
			GrantsTo:      []string{"frontOfHouse", "backOfHouse", "operator"},
		},
		{
			OperationType: "UpdateIdentityState",
			Scope:         "any",
			Note:          "Grants the right to advance an identity through its state machine.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "ClaimIdentity",
			Scope:         "self",
			Note:          "Grants the right to claim an identity (scope=self via credentialindex).",
			GrantsTo:      []string{"consumer"},
		},
	}
}
