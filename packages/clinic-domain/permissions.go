package clinicdomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// Permissions grants every clinic-domain op to the `operator` role (scope any).
// The role canonical name `operator` is resolved by cmd/lattice-pkg to the seeded
// NanoID from lattice.bootstrap.json — identical to loftspace-domain. No new
// capability surface: the trusted-tool operator already holds standing permission.
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
		mk("CreatePatient"),
		mk("TombstonePatient"),
		mk("CreateProvider"),
		mk("TombstoneProvider"),
		mk("CreateAppointment"),
		mk("RescheduleAppointment"),
		mk("SetAppointmentStatus"),
		mk("TombstoneAppointment"),
	}
}
