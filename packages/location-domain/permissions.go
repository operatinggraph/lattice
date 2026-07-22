package locationdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the four permission vertices + their grants. Every
// operation is granted to the `operator` role (scope any). The role canonical
// name `operator` is resolved by cmd/lattice-pkg to the seeded NanoID from
// lattice.bootstrap.json.
func Permissions() []pkgmgr.PermissionSpec {
	mk := func(op string) pkgmgr.PermissionSpec {
		return pkgmgr.PermissionSpec{
			OperationType: op,
			Scope:         "any",
			Note:          "Grants the operator the right to submit " + op + " operations.",
			GrantsTo:      []string{"operator"},
		}
	}
	return []pkgmgr.PermissionSpec{
		mk("CreateLocation"),
		mk("TombstoneLocation"),
		mk("WireContainedIn"),
		mk("UnwireContainedIn"),
		mk("SetLocationPresentation"),
	}
}
