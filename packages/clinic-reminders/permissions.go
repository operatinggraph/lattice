package clinicreminders

import "github.com/asolgan/lattice/internal/pkgmgr"

// Permissions grants RecordAppointmentReminder to the `operator` role (scope any)
// — Weaver's service actor dispatches the directOp under operator authority, the
// same operator-grant idiom objects-base uses for the GC-internal TombstoneObject.
// No new capability surface: the trusted-tool operator already holds standing
// permission. The role canonical name `operator` is resolved by cmd/lattice-pkg to
// the seeded NanoID from lattice.bootstrap.json.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: reminderOp,
			Scope:         "any",
			Note:          "Grants the operator the right to submit RecordAppointmentReminder operations (orchestration-internal: the appointmentReminders directOp playbook, dispatched by Weaver's service actor).",
			GrantsTo:      []string{"operator"},
		},
	}
}

// OpMetas makes RecordAppointmentReminder forOperation-resolvable for
// discoverability (Loupe's op-submit forms, a future Loom binding). The op is
// orchestration-internal (the appointmentReminders directOp dispatches it), so the
// meta is not load-bearing for dispatch — directOp submits the literal
// operationType — but is declared for parity with objects-base's GC op.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: reminderOp},
	}
}
