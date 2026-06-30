// Package clinicreminders is the clinic vertical's first ORCHESTRATION — one-shot
// @at reminders over clinic-domain's appointment. Two convergences: the ~24h-ahead
// appointment reminder ("remind before the visit"), and the at-the-date follow-up
// reminder ("a documented visit's requested follow-up is due") so a follow-up does
// not silently fall through (the build-ready slices the backlog flagged; recurring
// availability genuinely needs @every and stays a separate §10.4-amendment-gated
// item).
//
// It is the convergence sibling of the projection-only clinic-domain (the
// location-domain → lease-signing layering): clinic-domain owns the appointment +
// its .schedule/.status/.encounter aspects; clinic-reminders ATTACHES the reminder
// machinery — the .reminder / .followUpReminder marker aspects (the follow-up half
// in followups.go).
//
//	vtx.appointment.<id>.reminder = {sentAt, remindedFor}  (class appointmentReminder — this package)
//	op RecordAppointmentReminder{appointmentKey, remindedFor?}  (writes .reminder on a live appointment)
//	lens appointmentReminders (weaver-target, full)     (freshUntil = remindAt; remindedFor <> startsAt gate)
//	playbook missing_reminder → directOp(RecordAppointmentReminder, remindedFor: row.startsAt)
//
//	vtx.appointment.<id>.followUpReminder = {sentAt, remindedFor}  (class followUpReminder — this package)
//	op RecordFollowUpReminder{appointmentKey, remindedFor?}  (writes .followUpReminder on a live appointment)
//	lens followUpReminders (weaver-target, full)   (freshUntil = followUpDate; remindedFor <> followUpDate gate)
//	playbook missing_followup_reminder → directOp(RecordFollowUpReminder, remindedFor: row.followUpDate)
//
// The mechanism INVERTS lease-signing's freshness re-open. lease projects
// freshUntil to RE-OPEN a converged gap at a deadline; these project freshUntil = a
// deadline (the .schedule.remindAt clinic-domain precomputes = startsAt − 24h, or
// the .encounter.followUpDate a documented visit requested) so Weaver's @at temporal
// lane fires at the deadline → MarkExpired re-touches the appointment → the row
// re-projects with a fresh $now → the gap OPENS → Weaver dispatches the directOp →
// the marker records the deadline it reminded for → the gate (remindedFor = the
// deadline) closes. A reschedule (appointment) or re-documentation (follow-up) that
// moves the deadline re-opens the gate and re-arms the reminder. See
// appointmentRemindersSpec / followUpRemindersSpec + the design doc
// _bmad-output/implementation-artifacts/clinic-reminders-design.md.
//
// The actual notification channel (email/SMS) is the deferred real-adapter work;
// recording the reminder fact + the FE surfacing it is the demonstrable slice.
//
// Depends clinic-domain (the appointment + .schedule.remindAt / .encounter.followUpDate)
// + orchestration-base (MarkExpired / the freshnessExpiry marker the @at firing
// writes). Install via `lattice-pkg install packages/clinic-reminders` after both.
package clinicreminders

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "clinic-reminders",
	Version: "0.1.0",
	Description: "Clinic appointment & follow-up reminders (the clinic vertical's first orchestration): the .reminder / " +
		".followUpReminder marker aspects + RecordAppointmentReminder / RecordFollowUpReminder ops, the appointmentReminders " +
		"+ followUpReminders weaver-target convergence lenses (freshUntil = the .schedule.remindAt / .encounter.followUpDate " +
		"deadline arms the @at timer; the gap opens at the deadline), and the §10.8 playbooks dispatching the directOps. " +
		"Inverts lease-signing's freshness re-open. Depends clinic-domain + orchestration-base.",
	Depends:       []string{"clinic-domain", "orchestration-base"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
	OpMetas:       OpMetas(),
}
