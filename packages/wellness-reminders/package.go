// Package wellnessreminders is the wellness vertical's first ORCHESTRATION —
// an @at convergence over wellness-domain's booking/session, mirroring
// clinic-reminders' appointment-reminder half (the follow-up reminder and
// recurring visit series are clinic-specific — wellness has no encounter /
// follow-up concept). Before this package, a member who booked a class a
// week out heard nothing until the no-show mark landed (verticals.md "A
// booked class never reminds anyone").
//
// It is the convergence sibling of the projection-only wellness-domain (the
// clinic-domain → clinic-reminders layering): wellness-domain owns the
// booking + its .status aspect (and the session + its .schedule aspect,
// including the remindAt deadline this package's lens reads); this package
// ATTACHES the reminder machinery — the .reminder marker aspect — onto the
// booking. The anchor is the BOOKING, not the session, because a class has
// MANY bookers (unlike clinic's one appointment ↔ one patient), so each
// booker needs their own independent reminder + notification.
//
//	vtx.booking.<id>.reminder = {sentAt, remindedFor}  (class bookingReminder — this package)
//	op RecordBookingReminder{bookingKey, remindedFor?}  (writes .reminder on a live booking)
//	lens wellnessBookingReminders (weaver-target, full)  (freshUntil = the session's remindAt; remindedFor <> startsAt gate)
//	playbook missing_reminder → directOp(RecordBookingReminder, remindedFor: row.startsAt)
//
// It also ships the auto-no-show closer (pastdue.go), mirroring
// clinic-reminders' pastDueAppointments: a class whose session has ended
// with the booking still `booked` never got a staff attendance mark, so
// wellness-ledger's wellnessNoShowSettlement fee never had a status to key
// on (verticals.md "a past class never closes out").
//
//	lens pastDueBookings (weaver-target, full)  (freshUntil = the session's endsAt; status='booked' AND endsAt<=$now gate)
//	playbook missing_noshow_transition → directOp(SetBookingAttendance, bookingKey: row.entityKey, session: row.sessionKey, status: "noShow")
//
// The reminder mechanism INVERTS lease-signing's freshness re-open, exactly
// like clinic-reminders: it projects freshUntil = a deadline (the session's
// .schedule.remindAt wellness-domain precomputes = startsAt − 24h) so
// Weaver's @at temporal lane fires at the deadline → MarkExpired re-touches
// the booking → the row re-projects with a fresh $now → the gap OPENS →
// Weaver dispatches the directOp → the marker records the deadline it
// reminded for → the gate (remindedFor = the deadline) closes. A
// ReassignSession that moves the class's startsAt re-opens the gate and
// re-arms the reminder. See wellnessBookingRemindersSpec (lenses.go).
//
// The reminder op also fires the actual notification send off its own
// transactional outbox to the bridge's "notification" adapter
// (notifications.go — RecordBookingReminderNotification records the outcome
// as an audit-only aspect; it does NOT gate the convergence lens above,
// which stays keyed on .reminder unchanged).
//
// Depends wellness-domain (the booking/session vertex types + the session's
// .schedule.remindAt) + orchestration-base (MarkExpired / the
// freshnessExpiry marker the @at firing writes). Install via
// `lattice-pkg install packages/wellness-reminders` after both.
package wellnessreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "wellness-reminders",
	Version: "0.3.3",
	Description: "Wellness class reminder (the wellness vertical's first orchestration): the .reminder marker aspect " +
		"+ RecordBookingReminder op, the wellnessBookingReminders weaver-target convergence lens (freshUntil = the " +
		"booking's session .schedule.remindAt deadline arms the @at timer; the gap opens at the deadline) — the " +
		"§10.8 playbook dispatches the directOp. Inverts lease-signing's freshness re-open, mirroring " +
		"clinic-reminders' appointment-reminder half. Also fires external.notification off its own outbox to the " +
		"bridge's \"notification\" adapter; RecordBookingReminderNotification records the outcome. Also ships the " +
		"pastDueBookings weaver-target convergence lens (freshUntil = the session's .schedule.endsAt; gap opens once " +
		"a `booked` booking's class ends) — the §10.8 playbook directOps wellness-domain's own SetBookingAttendance " +
		"(status: noShow), mirroring clinic-reminders' pastDueAppointments. Depends wellness-domain + orchestration-base.",
	Depends:       []string{"wellness-domain", "orchestration-base"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
	OpMetas:       OpMetas(),
}
