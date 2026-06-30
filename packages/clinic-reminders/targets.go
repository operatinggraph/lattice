package clinicreminders

import "github.com/asolgan/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract #10
// §10.8). TargetID == the appointmentReminders lens's OutputKeyPattern prefix (the
// §10.2↔§10.8 binding); LensRef resolves to that lens's in-batch NanoID at install.
//
// The single gap → remediation:
//
//   - missing_reminder → directOp(RecordAppointmentReminder) over the appointment.
//     The op writes the .reminder.sentAt marker, closing the gap. directOp (not a
//     Loom pattern) because a reminder is a single op — no multi-step externalTask
//     flow — exactly the objectLiveness → TombstoneObject GC precedent.
//
// Params{appointmentKey: row.entityKey, remindedFor: row.startsAt} routes the
// candidate appointment key + the startsAt this reminder is for into the op's
// payload, and Reads[row.entityKey] routes the key into the op's ContextHint.Reads
// (the liveness-guard hydration). remindedFor lets the op record WHICH startsAt it
// reminded for, so a later reschedule (startsAt moves) re-opens the gate and
// re-arms the reminder. Both entityKey and startsAt are appointmentReminders
// BodyColumns — the §10.2↔§10.8 column seam (cross-checked by
// TestClinicReminders_PlaybookColumnsMatchLens).
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: AppointmentRemindersTarget,
			LensRef:  "appointmentReminders",
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_reminder": {
					Action:    "directOp",
					Operation: reminderOp,
					Params:    map[string]string{"appointmentKey": "row.entityKey", "remindedFor": "row.startsAt"},
					Reads:     []string{"row.entityKey"},
				},
			},
		},
		followUpRemindersTarget(),
	}
}
