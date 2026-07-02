package bespokecontracts

import "github.com/asolgan/lattice/internal/pkgmgr"

// OpMetas declares CreateClause discoverable as a forOperation target
// (the lease-signing idiom — functionally optional here since CreateClause
// is never an assignTask target, but declared for uniform discoverability).
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: "CreateClause"},
	}
}

// Permissions returns the package's permission vertices + grants. The
// trusted-tool app submits CreateClause when a landlord/operator installs a
// bespoke provision on a signed lease — the same operator-grant idiom
// loftspace-ledger's CreateAccount uses.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateClause",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreateClause (installs a bespoke-contract clause governing a lease and charging a ledger account).",
			GrantsTo:      []string{"operator"},
		},
	}
}
