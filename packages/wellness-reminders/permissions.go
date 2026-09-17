package wellnessreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions grants RecordBookingReminder, RecordBookingReminderNotification
// and RecordBookingChangeNotice to the `operator` role (scope any) — Weaver's
// service actor dispatches the directOps under operator authority, mirroring
// clinic-reminders' identical operator-grant idiom for
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
	perms = append(perms, notificationPermissions()...)
	return append(perms, changeNoticePermissions()...)
}

// OpMetas makes RecordBookingReminder / RecordBookingReminderNotification /
// RecordBookingChangeNotice forOperation-resolvable for discoverability
// (Loupe's op-submit forms, a future Loom binding). All three are
// orchestration-internal (their playbooks / the bridge dispatch them
// directly), so this meta is not load-bearing for dispatch — declared for
// parity with clinic-reminders.
func OpMetas() []pkgmgr.OpMetaSpec {
	metas := []pkgmgr.OpMetaSpec{
		{OperationType: reminderOp},
	}
	metas = append(metas, notificationOpMetas()...)
	return append(metas, changeNoticeOpMetas()...)
}
