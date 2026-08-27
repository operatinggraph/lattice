package wellnessreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The auto-no-show sibling of the class reminder: a class that has ended
// with no staff status update leaves its booking sitting `booked` forever,
// since SetBookingAttendance is staff/instructor-submitted only — the
// verticals.md gap ("ten of eleven live bookings sit on classes that already
// ended and every one still reads booked"). This convergence closes that gap
// the same way clinic-reminders' pastDueAppointments does — an @at
// convergence over wellness-domain's booking — binding DIRECTLY to the
// booking's session .schedule.endsAt (the unroutedTasks/pastDueAppointments
// idiom: a mutable business timestamp IS the staleness threshold, no
// separate marker needed).
//
//	lens pastDueBookings (weaver-target, full)  (freshUntil = the session's endsAt; status='booked' AND endsAt<=$now gate)
//	playbook missing_noshow_transition → directOp(SetBookingAttendance, bookingKey: row.entityKey, session: row.sessionKey, status: "noShow", noShowFeeCents: "0")
//
// Unlike clinic's MarkPastDueNoShow, this dispatches wellness-domain's own
// SetBookingAttendance directly rather than through a dedicated op:
// SetBookingAttendance's operator path takes only bookingKey/session/status
// — plain row columns, no CALLER-SUPPLIED param validated against a LINK
// (clinic's provider/patient problem) — so the §10.8 playbook can express it
// without a new operationType. Only `booked` bookings are eligible: a
// `waitlisted` booker never held a confirmed seat, so there is no attendance
// to record once the class ends (mirroring wellnessBookingRemindersSpec's
// same status='booked' restriction); `attended`/`noShow` are already
// terminal. Because this playbook shares SetBookingAttendance with the staff
// path rather than getting its own dedicated op (clinic's MarkPastDueNoShow
// vs SetAppointmentStatus split), it must explicitly route
// noShowFeeCents:"0" — a caller-supplied 0 is SetBookingAttendance's signal
// for "documentation lapse, not a billable no-show" (ddls.go), so this sweep
// never bills the default $25; a staff-observed SetBookingAttendance call
// (which omits the field) still does. Once dispatched, wellness-ledger's
// existing wellnessNoShowSettlement lens (unchanged by this package) reads
// noShowFeeCents when present and positive to post the account charge — this
// closes only the missing status transition, not the billing gate, which
// already converges on its own.
const (
	// PastDueBookingsTarget is the §10.8 TargetID == the pastDueBookings
	// lens's OutputKeyPattern prefix (the §10.2↔§10.8 binding Weaver reads).
	PastDueBookingsTarget = "pastDueBookings"
)

// pastDueBookingsLens is the auto-no-show convergence lens.
func pastDueBookingsLens() pkgmgr.LensSpec {
	return pkgmgr.LensSpec{
		CanonicalName:  "pastDueBookings",
		Class:          "meta.lens",
		Adapter:        "nats-kv",
		Bucket:         "weaver-targets",
		Engine:         "full",
		Spec:           pastDueBookingsSpec,
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "booking",
			OutputKeyPattern: "pastDueBookings.{actorSuffix}",
			BodyColumns:      []string{"violating", "missing_noshow_transition", "entityKey", "freshUntil", "endsAt", "status", "sessionKey", "bookerKey"},
			EmptyBehavior:    "delete",
			KeyColumn:        "entityId",
		},
	}
}

// pastDueBookingsSpec is the one-row-per-booking auto-no-show convergence
// cypher. Like pastDueAppointmentsSpec, freshUntil binds DIRECTLY to the
// session's .schedule.endsAt — no derived lead-offset deadline. While endsAt
// is in the future the lens arms a one-shot @at at endsAt; once it passes,
// the gap OPENS (the violating row drives dispatch, not a timer wake-up).
//
// The gate (status = 'booked' AND endsAt <= $now):
//
//   - status = 'booked' — the seat was never resolved to attended or noShow.
//     `waitlisted` never held a seat (no attendance to record); `attended` /
//     `noShow` are already terminal — either way SetBookingAttendance's
//     own re-mark path (not this lens) is what corrects a wrong call.
//   - endsAt <= $now — the class's scheduled end has passed with no status
//     update recorded.
//
// One-row-per-anchor: forSession / bookedBy are 0..1 (CreateBooking /
// JoinWaitlist write exactly one of each), so the OPTIONAL walks do not fan
// out. sessionKey / bookerKey / status are INFORMATIONAL (operator/FE
// observability) — the playbook does not template them (SetBookingAttendance
// resolves the session live via its own declared read); only entityKey +
// sessionKey + freshUntil + the two bools are load-bearing for dispatch +
// the temporal lane (sessionKey doubles as the dispatched op's `session`
// param).
const pastDueBookingsSpec = `MATCH (b:booking {key: $actorKey})
OPTIONAL MATCH (b)-[:forSession]->(se:session)
OPTIONAL MATCH (b)-[:bookedBy]->(id:identity)
RETURN
  b.key AS actorKey,
  b.key AS entityKey,
  se.key AS sessionKey,
  se.schedule.data.endsAt AS endsAt,
  b.status.data.value AS status,
  id.key AS bookerKey,
  CASE WHEN (b.status.data.value = 'booked') AND (se.schedule.data.endsAt > $now) THEN se.schedule.data.endsAt ELSE null END AS freshUntil,
  ((b.status.data.value = 'booked') AND (se.schedule.data.endsAt <= $now)) AS missing_noshow_transition,
  ((b.status.data.value = 'booked') AND (se.schedule.data.endsAt <= $now)) AS violating`

// pastDueBookingsTarget returns the §10.8 playbook for the auto-no-show
// convergence: the single missing_noshow_transition gap →
// directOp(SetBookingAttendance) over the booking, routing the candidate
// booking + its session (SetBookingAttendance requires both — bookingKey to
// identify the booking, session to resolve the class's schedule + confine an
// instructor caller, though the operator path this target dispatches under
// never hits that branch). Reads covers both vertex roots: SetBookingAttendance's
// own declared reads are the booking's .status (rate/seat/booker/session
// carry-forward) and the session's .schedule (the SessionNotStarted guard) —
// both satisfied by declaring the two row-templated vertex keys themselves,
// the same coverage clinic's MarkPastDueNoShow gets from declaring only
// row.entityKey (Contract #10 §10.8: a declared read covers every aspect
// under that vertex, not just the root).
func pastDueBookingsTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: PastDueBookingsTarget,
		Description: "No confirmed booking stays unresolved once its class has ended. A seat still marked booked " +
			"after the class finishes is recorded as a no-show — a documentation lapse (nobody at the desk " +
			"checked the member in), not a staff-observed missed visit, so this sweep marks the booking noShow " +
			"WITHOUT billing a fee (explicit noShowFeeCents:0); only a staff-observed SetBookingAttendance call " +
			"bills the default $25.",
		LensRef: "pastDueBookings",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_noshow_transition": {
				Action:    "directOp",
				Operation: "SetBookingAttendance",
				Params:    map[string]string{"bookingKey": "row.entityKey", "session": "row.sessionKey", "status": "noShow", "noShowFeeCents": "0"},
				Reads:     []string{"row.entityKey", "row.sessionKey"},
			},
		},
	}
}
