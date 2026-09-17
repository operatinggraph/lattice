package clinicreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbooks (Contract #10
// §10.8). Each TargetID == its lens's OutputKeyPattern prefix (the
// §10.2↔§10.8 binding); LensRef resolves to that lens's in-batch NanoID at install.
//
// appointmentReminders' single gap → remediation:
//
//   - missing_reminder → directOp(RecordAppointmentReminder) over the appointment.
//     The op writes the .reminder.sentAt marker, closing the gap. directOp (not a
//     Loom pattern) because a reminder is a single op — no multi-step externalTask
//     flow — exactly the objectLiveness → TombstoneObject GC precedent.
//
// appointmentChangeNotices' two gaps → one remediation op:
//
//   - missing_cancel_notice / missing_move_notice → directOp(RecordAppointmentChangeNotice)
//     with kind = the literal and changeRef = row.statusAt / row.movedAt, the
//     recorded moment of the change the lens keyed the gap on. Reads route the
//     root (liveness), .status and .schedule (the op's live re-check); the
//     .changeNotice marker is an OptionalRead — absent on the first notice — so
//     the op can carry the other kind's field across a bare update. Every
//     row.<col> named here is an appointmentChangeNotices BodyColumn, and every
//     Params column is one the gap's own conjunct requires non-null.
//
// Params{appointmentKey: row.entityKey, remindedFor: row.startsAt} routes the
// candidate appointment key + the startsAt this reminder is for into the op's
// payload, and Reads[row.entityKey, row.entityKey.schedule] routes the root
// key (the liveness-guard hydration) AND the .schedule aspect (the
// already-started guard, ddls.go's RecordAppointmentReminder — §7 role (c))
// into the op's ContextHint.Reads. remindedFor lets the op record WHICH
// startsAt it reminded for, so a later reschedule (startsAt moves) re-opens
// the gate and re-arms the reminder. Both entityKey and startsAt are
// appointmentReminders BodyColumns — the §10.2↔§10.8 column seam (cross-checked
// by TestClinicReminders_PlaybookColumnsMatchLens).
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: AppointmentRemindersTarget,
			Description: "Every upcoming appointment that has not been cancelled gets a reminder about a day before " +
				"it starts. Rescheduling re-arms the reminder so the patient is told the new time.",
			LensRef: "appointmentReminders",
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_reminder": {
					Action:    "directOp",
					Operation: reminderOp,
					Params:    map[string]string{"appointmentKey": "row.entityKey", "remindedFor": "row.startsAt"},
					Reads:     []string{"row.entityKey", "row.entityKey.schedule"},
				},
			},
		},
		{
			TargetID: AppointmentChangeNoticesTarget,
			Description: "A patient whose visit the desk cancelled is told once. A patient whose visit the desk moved " +
				"to a new time is told once per move.",
			LensRef: "appointmentChangeNotices",
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_cancel_notice": {
					Action:        "directOp",
					Operation:     changeNoticeOp,
					Class:         changeNoticeOpDDL,
					Params:        map[string]string{"appointmentKey": "row.entityKey", "kind": "cancelled", "changeRef": "row.statusAt"},
					Reads:         []string{"row.entityKey", "row.entityKey.status", "row.entityKey.schedule"},
					OptionalReads: []string{"row.entityKey.changeNotice"},
				},
				"missing_move_notice": {
					Action:        "directOp",
					Operation:     changeNoticeOp,
					Class:         changeNoticeOpDDL,
					Params:        map[string]string{"appointmentKey": "row.entityKey", "kind": "moved", "changeRef": "row.movedAt"},
					Reads:         []string{"row.entityKey", "row.entityKey.status", "row.entityKey.schedule"},
					OptionalReads: []string{"row.entityKey.changeNotice"},
				},
			},
		},
		followUpRemindersTarget(),
		visitSeriesDueTarget(),
		visitSeriesSiteBackfillTarget(),
		pastDueAppointmentsTarget(),
	}
}
