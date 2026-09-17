package wellnessreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbooks (Contract
// #10 §10.8). Each TargetID == its lens's OutputKeyPattern prefix (the
// §10.2↔§10.8 binding); LensRef resolves to that lens's in-batch NanoID at
// install.
//
// wellnessBookingReminders — the single gap → remediation:
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
//
// wellnessBookingChangeNotices — two gaps, one op:
//
//   - missing_promotion_notice → directOp(RecordBookingChangeNotice, kind:
//     promoted, changeRef: row.promotedAt). The op writes
//     .changeNotice.promotedFor = promotedAt, closing the gap for good
//     (promotedAt never changes).
//   - missing_move_notice → directOp(RecordBookingChangeNotice, kind: moved,
//     changeRef: row.startsAt). The op writes .changeNotice.movedFor =
//     startsAt, closing the gap until the class moves again.
//
// `kind` is a plain string literal in the Params bag (definition.go's
// third arm — dispatched byte-for-byte, the same way pastdue.go passes
// "noShow"); changeRef is templated off the row column that IS the change,
// so the op can re-check it against the live aspect and refuse a stale row.
// Reads declares what the script hard-requires: the booking root
// (vertex_alive), its .status (the booked check + promotedAt) and the
// session's .schedule (startsAt — the move check and every notice's
// params). The booking's .changeNotice is an OptionalRead: absent for the
// first notice of either kind (the create branch), present after (the
// OCC-pinned update carrying the other kind's field). Class pins the
// bookingChangeNoticeOp DDL: RecordBookingChangeNotice is unique to this
// package today, but an unpinned directOp fails closed (MissingClass)
// forever the moment any other installed package claims the same
// operationType, so it is pinned regardless — the defensive shape
// wellness-domain's own targets use. No maxretries column is declared: the
// default budget of 3 is right for a notice, and the class-over conjunct
// closing the column retires any exhausted-budget latch.
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
		{
			TargetID: WellnessBookingChangeNoticesTarget,
			Description: "A member whose seat was handed over from the waitlist is told once that they are in. " +
				"A member whose class was moved to a new time is told once per move.",
			LensRef: "wellnessBookingChangeNotices",
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_promotion_notice": {
					Action:        "directOp",
					Operation:     changeNoticeOp,
					Class:         changeNoticeOpDDL,
					Params:        map[string]string{"bookingKey": "row.entityKey", "sessionKey": "row.sessionKey", "kind": "promoted", "changeRef": "row.promotedAt"},
					Reads:         []string{"row.entityKey", "row.entityKey.status", "row.sessionKey.schedule"},
					OptionalReads: []string{"row.entityKey.changeNotice"},
				},
				"missing_move_notice": {
					Action:        "directOp",
					Operation:     changeNoticeOp,
					Class:         changeNoticeOpDDL,
					Params:        map[string]string{"bookingKey": "row.entityKey", "sessionKey": "row.sessionKey", "kind": "moved", "changeRef": "row.startsAt"},
					Reads:         []string{"row.entityKey", "row.entityKey.status", "row.sessionKey.schedule"},
					OptionalReads: []string{"row.entityKey.changeNotice"},
				},
			},
		},
		pastDueBookingsTarget(),
	}
}
