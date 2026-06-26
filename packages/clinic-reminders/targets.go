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
// Params{appointmentKey: row.entityKey} routes the candidate appointment key into
// the op's payload, and Reads[row.entityKey] routes it into the op's
// ContextHint.Reads (the liveness-guard hydration). entityKey is an
// appointmentReminders BodyColumn — the §10.2↔§10.8 column seam (cross-checked by
// TestClinicReminders_PlaybookColumnsMatchLens).
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{{
		TargetID: AppointmentRemindersTarget,
		LensRef:  "appointmentReminders",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_reminder": {
				Action:    "directOp",
				Operation: reminderOp,
				Params:    map[string]string{"appointmentKey": "row.entityKey"},
				Reads:     []string{"row.entityKey"},
			},
		},
	}}
}
