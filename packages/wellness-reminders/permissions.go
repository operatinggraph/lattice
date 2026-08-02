package wellnessreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions grants RecordBookingReminder to the `operator` role (scope any)
// — Weaver's service actor dispatches the directOp under operator authority,
// mirroring clinic-reminders' identical operator-grant idiom for
// RecordAppointmentReminder. No new capability surface: the trusted-tool
// operator already holds standing permission.
func Permissions() []pkgmgr.PermissionSpec {
	perms := []pkgmgr.PermissionSpec{
		{
			OperationType: reminderOp,
			Scope:         "any",
			Note:          "Grants the operator the right to submit RecordBookingReminder operations (orchestration-internal: the wellnessBookingReminders directOp playbook, dispatched by Weaver's service actor).",
			GrantsTo:      []string{"operator"},
		},
	}
	return append(perms, notificationPermissions()...)
}

// OpMetas makes RecordBookingReminder / RecordBookingReminderNotification
// forOperation-resolvable for discoverability (Loupe's op-submit forms, a
// future Loom binding). Both are orchestration-internal (their playbooks /
// the bridge dispatch them directly), so this meta is not load-bearing for
// dispatch — declared for parity with clinic-reminders.
func OpMetas() []pkgmgr.OpMetaSpec {
	metas := []pkgmgr.OpMetaSpec{
		{OperationType: reminderOp},
	}
	return append(metas, notificationOpMetas()...)
}
