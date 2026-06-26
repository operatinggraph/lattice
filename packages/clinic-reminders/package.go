// Package clinicreminders is the clinic vertical's first ORCHESTRATION — one-shot
// @at appointment reminders ("remind ~24h before"), the build-ready slice the
// backlog flagged (recurring availability genuinely needs @every and stays a
// separate §10.4-amendment-gated item).
//
// It is the convergence sibling of the projection-only clinic-domain (the
// location-domain → lease-signing layering): clinic-domain owns the appointment +
// its .schedule/.status aspects; clinic-reminders ATTACHES the reminder machinery.
//
//	vtx.appointment.<id>.reminder = {sentAt}            (class appointmentReminder — this package)
//	op RecordAppointmentReminder{appointmentKey}        (writes .reminder.sentAt on a live appointment)
//	lens appointmentReminders (weaver-target, full)     (freshUntil = remindAt; missing_reminder/violating gate)
//	playbook missing_reminder → directOp(RecordAppointmentReminder)
//
// The mechanism INVERTS lease-signing's freshness re-open. lease projects
// freshUntil to RE-OPEN a converged gap at a deadline; this projects
// freshUntil = remindAt (the .schedule.remindAt clinic-domain precomputes =
// startsAt − 24h) so Weaver's @at temporal lane fires at the deadline →
// MarkExpired re-touches the appointment → the row re-projects with a fresh $now →
// missing_reminder OPENS → Weaver dispatches directOp(RecordAppointmentReminder) →
// .reminder.sentAt closes the gap. See appointmentRemindersSpec + the design doc
// _bmad-output/implementation-artifacts/clinic-reminders-design.md.
//
// The actual notification channel (email/SMS) is the deferred real-adapter work;
// recording the reminder fact + the FE surfacing it is the demonstrable slice.
//
// Depends clinic-domain (the appointment + .schedule.remindAt) + orchestration-base
// (MarkExpired / the freshnessExpiry marker the @at firing writes). Install via
// `lattice-pkg install packages/clinic-reminders` after both.
package clinicreminders

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "clinic-reminders",
	Version: "0.1.0",
	Description: "Clinic appointment reminders (the clinic vertical's first orchestration): the .reminder aspect + " +
		"RecordAppointmentReminder op, the appointmentReminders weaver-target convergence lens (freshUntil = remindAt " +
		"arms the @at reminder timer; missing_reminder opens at the deadline), and the §10.8 playbook dispatching " +
		"directOp(RecordAppointmentReminder). Inverts lease-signing's freshness re-open. Depends clinic-domain + " +
		"orchestration-base.",
	Depends:       []string{"clinic-domain", "orchestration-base"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
	OpMetas:       OpMetas(),
}
