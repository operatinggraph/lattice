package servicelocation

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the ten permission vertices + their grants. Every link
// op is granted to the `operator` role (scope any) — the residence /
// availability / workplace topology is operator-provisioned. worksAt is
// deliberately no exception: who works where is an employment fact the
// operator asserts. A staff-writable worksAt would let a staff actor wire
// their own workplace and thereby mint their own workplace-anchored read
// grants, which is the whole confinement the staff read spine rests on.
// The role canonical name `operator` is resolved by cmd/lattice-pkg to the
// seeded NanoID from lattice.bootstrap.json.
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
		mk("WireResidesIn"),
		mk("UnwireResidesIn"),
		mk("WireWorksAt"),
		mk("UnwireWorksAt"),
		mk("WireAvailableAt"),
		mk("UnwireAvailableAt"),
		mk("WireUnavailableAt"),
		mk("UnwireUnavailableAt"),
		mk("WirePermitsOperation"),
		mk("UnwirePermitsOperation"),
	}
}
