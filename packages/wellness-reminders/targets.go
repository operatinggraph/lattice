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
// Params{bookingKey: row.entityKey, sessionKey: row.sessionKey, remindedFor:
// row.startsAt} routes the candidate booking key, its session (already a
// projected wellnessBookingReminders column — the row's OPTIONAL forSession
// walk), and the startsAt this reminder is for into the op's payload, and
// Reads[row.entityKey, row.sessionKey.schedule] routes the booking root
// (the liveness-guard hydration) AND the session's .schedule aspect (the
// already-started guard, ddls.go's RecordBookingReminder — §7 role (c); the
// deadline lives on the session neighbour, not the booking) into the op's
// ContextHint.Reads. remindedFor lets the op record WHICH startsAt it
// reminded for, so a later ReassignSession time move re-opens the gate and
// re-arms the reminder. entityKey, sessionKey and startsAt are all
// wellnessBookingReminders BodyColumns — the §10.2↔§10.8 column seam.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: WellnessBookingRemindersTarget,
			Description: "Every confirmed booking on an upcoming class gets a reminder about a day before the class " +
				"starts. Moving the class to a new time re-arms the reminder.",
			LensRef: "wellnessBookingReminders",
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_reminder": {
					Action:    "directOp",
					Operation: reminderOp,
					Params:    map[string]string{"bookingKey": "row.entityKey", "sessionKey": "row.sessionKey", "remindedFor": "row.startsAt"},
					Reads:     []string{"row.entityKey", "row.sessionKey.schedule"},
				},
			},
		},
		pastDueBookingsTarget(),
	}
}
