package wellnessreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8). TargetID == the wellnessBookingReminders lens's
// OutputKeyPattern prefix (the §10.2↔§10.8 binding); LensRef resolves to
// that lens's in-batch NanoID at install.
//
// The single gap → remediation:
//
//   - missing_reminder → directOp(RecordBookingReminder) over the booking.
//     The op writes the .reminder.sentAt marker, closing the gap. directOp
//     (not a Loom pattern) because a reminder is a single op — no
//     multi-step externalTask flow — mirroring clinic-reminders' identical
//     appointmentReminders target.
//
// Params{bookingKey: row.entityKey, remindedFor: row.startsAt} routes the
// candidate booking key + the startsAt this reminder is for into the op's
// payload, and Reads[row.entityKey] routes the key into the op's
// ContextHint.Reads (the liveness-guard hydration). remindedFor lets the op
// record WHICH startsAt it reminded for, so a later ReassignSession time
// move re-opens the gate and re-arms the reminder. Both entityKey and
// startsAt are wellnessBookingReminders BodyColumns — the §10.2↔§10.8
// column seam.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: WellnessBookingRemindersTarget,
			LensRef:  "wellnessBookingReminders",
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_reminder": {
					Action:    "directOp",
					Operation: reminderOp,
					Params:    map[string]string{"bookingKey": "row.entityKey", "remindedFor": "row.startsAt"},
					Reads:     []string{"row.entityKey"},
				},
			},
		},
	}
}
