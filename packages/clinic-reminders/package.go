// Package clinicreminders is the clinic vertical's first ORCHESTRATION — @at
// convergences over clinic-domain's appointment/patient/provider. Three
// convergences: the ~24h-ahead appointment reminder ("remind before the visit"),
// the at-the-date follow-up reminder ("a documented visit's requested follow-up is
// due") so a follow-up does not silently fall through, and the recurring visit
// series ("a patient's next standing check-in is due") — a rolling generalization
// of the one-shot follow-up that re-arms its own next deadline instead of firing
// once (visitseries.go; no @every, no per-entity substrate schedule — see
// _bmad-output/implementation-artifacts/clinic-recurring-visit-series-design.md).
//
// It is the convergence sibling of the projection-only clinic-domain (the
// location-domain → lease-signing layering): clinic-domain owns the appointment +
// its .schedule/.status/.encounter aspects; clinic-reminders ATTACHES the reminder
// machinery — the .reminder / .followUpReminder marker aspects (the follow-up half
// in followups.go) — AND owns its own self-contained visitseries vertex type (the
// clinic-domain patient/provider idiom) for the recurring series.
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
//	vtx.visitseries.<id>  class=visitseries  .series {intervalDays, startAt, activeUntil?}  .progress {nextDueAt, occurrenceCount}  .paused? {value}  (this package, visitseries.go)
//	op StartVisitSeries / PauseVisitSeries / ResumeVisitSeries / AdvanceVisitSeries
//	lens visitSeriesDue (weaver-target, full)  (freshUntil = .progress.nextDueAt; re-arms forward on every advance, never converges to a permanent close)
//	playbook missing_series_advance → directOp(AdvanceVisitSeries, dueFor: row.nextDueAt, intervalDays: row.intervalDays, occurrenceCount: row.occurrenceCount)
//
// The reminder mechanism INVERTS lease-signing's freshness re-open. lease projects
// freshUntil to RE-OPEN a converged gap at a deadline; these project freshUntil = a
// deadline (the .schedule.remindAt clinic-domain precomputes = startsAt − 24h, or
// the .encounter.followUpDate a documented visit requested) so Weaver's @at temporal
// lane fires at the deadline → MarkExpired re-touches the appointment → the row
// re-projects with a fresh $now → the gap OPENS → Weaver dispatches the directOp →
// the marker records the deadline it reminded for → the gate (remindedFor = the
// deadline) closes. A reschedule (appointment) or re-documentation (follow-up) that
// moves the deadline re-opens the gate and re-arms the reminder. See
// appointmentRemindersSpec / followUpRemindersSpec + the design doc
// _bmad-output/implementation-artifacts/clinic-reminders-design.md. The visit-series
// lens applies the same freshness inversion but never permanently closes — each
// AdvanceVisitSeries rewrites nextDueAt to a new future deadline, rolling the series
// forward instead of converging (visitSeriesDueSpec, visitseries.go).
//
// The actual notification channel (email/SMS) is the deferred real-adapter work;
// recording the reminder fact + the FE surfacing it is the demonstrable slice.
//
// Depends clinic-domain (the appointment/patient/provider vertex types + the
// appointment's .schedule.remindAt / .encounter.followUpDate) + orchestration-base
// (MarkExpired / the freshnessExpiry marker the @at firing writes). Install via
// `lattice-pkg install packages/clinic-reminders` after both.
package clinicreminders

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "clinic-reminders",
	Version: "0.2.0",
	Description: "Clinic appointment & follow-up reminders + recurring visit series (the clinic vertical's " +
		"orchestration): the .reminder / .followUpReminder marker aspects + RecordAppointmentReminder / " +
		"RecordFollowUpReminder ops, the appointmentReminders + followUpReminders weaver-target convergence lenses " +
		"(freshUntil = the .schedule.remindAt / .encounter.followUpDate deadline arms the @at timer; the gap opens " +
		"at the deadline); and the visitseries vertex type + Start/Pause/Resume/AdvanceVisitSeries ops + the " +
		"visitSeriesDue rolling convergence lens (freshUntil re-arms forward on every advance instead of clearing " +
		"to a permanent close) — the §10.8 playbooks dispatch each gap's directOp. Inverts lease-signing's " +
		"freshness re-open. Depends clinic-domain + orchestration-base.",
	Depends:       []string{"clinic-domain", "orchestration-base"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
	OpMetas:       OpMetas(),
}
