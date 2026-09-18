package wellnessreminders

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The booking-change notice: a member whose seat was handed over from the
// waitlist, or whose class was moved to a new time, is told once per change.
// The wellnessBookingChangeNotices lens (lenses.go) projects the two
// level-triggered gaps; RecordBookingChangeNotice is the directOp its
// playbook (targets.go) dispatches, and the .changeNotice marker it writes is
// what closes them. The op mirrors RecordBookingReminder (ddls.go): the same
// Weaver-actor guard, the same declared-read posture, the same
// external.notification egress off its own outbox. The bridge's replyOp for
// that egress, RecordBookingChangeNotification, is owned by wellness-domain
// (the lower package both this op and the domain's own call-off emission name).
//
//   - bookingChangeNoticeOp (vertexType) — owns the RecordBookingChangeNotice
//     script. Mints NO vertex of its own type; it writes the .changeNotice
//     aspect on an existing wellness-domain booking (the freshnessMarker idiom).
//   - bookingChangeNotice (aspectType) — declares .changeNotice = {promotedFor?,
//     movedFor?, sentAt} and admits RecordBookingChangeNotice as its writer, so
//     the Processor's step-6 validator permits the marker write.
//     Declaration-only: no op handler.
const (
	changeNoticeOpDDL     = "bookingChangeNoticeOp"
	changeNoticeAspectDDL = "bookingChangeNotice"

	// changeNoticeOp is the single operation the wellnessBookingChangeNotices
	// playbook dispatches, for both of its gaps.
	changeNoticeOp = "RecordBookingChangeNotice"
)

// changeNoticeDDLs returns the two DDL meta-vertices (op handler + aspect
// gate) for the booking-change notice.
func changeNoticeDDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		recordChangeNoticeVertexTypeDDL(),
		changeNoticeAspectTypeDDL(),
	}
}

// recordChangeNoticeVertexTypeDDL owns the RecordBookingChangeNotice script.
// The op is the directOp the wellnessBookingChangeNotices playbook dispatches
// when missing_promotion_notice or missing_move_notice opens. It re-checks
// the change the row named against the LIVE aspect before telling anyone —
// promotedAt = changeRef for a promotion, the session's .schedule.startsAt =
// changeRef for a move — and refuses StaleChange when the row it was
// dispatched from has been outrun (a class moved twice between projection
// and dispatch is told about the CURRENT time on the next dispatch, never the
// intermediate one). The .changeNotice write is a create when the marker is
// absent and a BARE update when it exists: the marker is a declared
// optionalRead, so the Processor conditions the update on the step-4
// revision (§3.2) as a defaulted, retry-eligible condition — two kinds
// converging on one booking re-execute on conflict and carry each other's
// field, rather than one being rejected outright by an explicit CAS.
func recordChangeNoticeVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     changeNoticeOpDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{changeNoticeOp},
		Description: "Booking-change notice op handler (wellness-reminders). RecordBookingChangeNotice{bookingKey, " +
			"sessionKey, kind: promoted|moved|instructor|room, changeRef} tells a member once about one change to their " +
			"confirmed seat and records that it did: it writes vtx.booking.<NanoID>.changeNotice = {promotedFor?, " +
			"movedFor?, instructorFor?, roomFor?, sentAt} (class bookingChangeNotice) on a LIVE `booked` booking, " +
			"setting the dispatched kind's field to changeRef (promotedFor / movedFor / instructorFor / roomFor) and " +
			"carrying the other kinds' fields forward, and emits " +
			"external.notification off its own outbox (instanceKey = idempotencyKey = externalRef = " +
			"<bookingKey>:<kind>:<changeRef>) to the bridge's \"notification\" adapter; wellness-domain's " +
			"RecordBookingChangeNotification records the outcome. It is the directOp the wellnessBookingChangeNotices " +
			"§10.8 playbook dispatches for all four of that lens's gaps (missing_promotion_notice with changeRef = " +
			"row.promotedAt; missing_move_notice with changeRef = row.startsAt; missing_instructor_notice with " +
			"changeRef = row.instructorChangedAt; missing_room_notice with changeRef = row.studioChangedAt). Reads " +
			"[bookingKey, bookingKey.status, " +
			"sessionKey, sessionKey.schedule] and optionally [bookingKey.changeNotice]: it liveness-guards the booking " +
			"(UnknownBooking) and the session (UnknownSession — a class being called off is told by " +
			"ReleaseOrphanedBooking's own notice, never here), requires .status.value = booked (InvalidState), and " +
			"re-checks the change against the " +
			"live aspect — .status.promotedAt = changeRef for a promotion, .schedule.startsAt for a move, " +
			".schedule.instructorChangedAt for an instructor change, .schedule.studioChangedAt for a room change — " +
			"refusing StaleChange when the dispatched row has been outrun, so a stale row is refused, not trusted. The " +
			"marker write is a create when the aspect is absent and a bare update on the hydrated key when it exists " +
			"(§3.2-conditioned on the step-4 revision, retry-eligible in-process), so a promotion notice and a move " +
			"notice converging on one booking re-execute on conflict and never drop each other's field. " +
			"Submitted under Weaver's service-actor authority only. Mints NO vertex of its own type.",
		Script: recordChangeNoticeScript,
		InputSchema: `{"type":"object","properties":` +
			`{"bookingKey":{"type":"string","description":"vtx.booking.<NanoID> whose seat changed (required; validated alive and booked). The caller MUST list it and bookingKey.status in ContextHint.Reads."},` +
			`"sessionKey":{"type":"string","description":"vtx.session.<NanoID> the booking is for (required; validated alive — a called-off class sends nothing from here; its .schedule aspect carries the startsAt the move check and the notice params read). The caller MUST list sessionKey and sessionKey.schedule in ContextHint.Reads."},` +
			`"kind":{"type":"string","enum":["promoted","moved","instructor","room"],"description":"Which change this notice is for: promoted (the seat was handed over from the waitlist), moved (the class time changed), instructor (who leads the class changed — a sub, a clear, or a first assignment) or room (the class moved to another studio). Required."},` +
			`"changeRef":{"type":"string","description":"The value that identifies WHICH change: the .status.promotedAt instant for kind=promoted, the session's current .schedule.startsAt for kind=moved, its .schedule.instructorChangedAt for kind=instructor, its .schedule.studioChangedAt for kind=room (RFC3339, canonical UTC). Required; refused StaleChange when it no longer matches the live aspect."}},` +
			`"required":["bookingKey","sessionKey","kind","changeRef"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.booking.<NanoID> the change-notice marker was written on."}}}`,
		FieldDescription: map[string]string{
			"bookingKey": "Full vtx.booking.<NanoID> key whose seat changed. RecordBookingChangeNotice validates it is alive and booked, then writes the .changeNotice aspect on it. The caller MUST list this key and bookingKey.status in ContextHint.Reads.",
			"sessionKey": "Full vtx.session.<NanoID> key of the class this booking is for. RecordBookingChangeNotice validates it is alive (a called-off class is told by the release's own notice, never here) and reads its .schedule aspect for the startsAt the move check compares and the notice carries. The caller MUST list sessionKey and sessionKey.schedule in ContextHint.Reads.",
			"kind":       "promoted, moved, instructor or room — which change this notice tells the member about, and which .changeNotice field (promotedFor / movedFor / instructorFor / roomFor) records it.",
			"changeRef":  "The change's own identifier, re-checked against the live aspect before anything is sent: .status.promotedAt for promoted, the session's .schedule.startsAt for moved, its .schedule.instructorChangedAt for instructor, its .schedule.studioChangedAt for room. Recorded verbatim so the lens's equality closes the gap, and a later change (a new value) reopens it.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "RecordBookingChangeNotice — a seat handed over from the waitlist",
				Payload: map[string]any{"bookingKey": "vtx.booking.<NanoID>", "sessionKey": "vtx.session.<NanoID>", "kind": "promoted", "changeRef": "2026-09-16T20:41:00Z"},
				ExpectedOutcome: "Validates the booking is alive and booked and that .status.promotedAt = changeRef, then writes " +
					"vtx.booking.<NanoID>.changeNotice = {promotedFor: 2026-09-16T20:41:00Z, sentAt: op.submittedAt} (carrying any " +
					"existing movedFor), emits wellness.bookingChangeNoticeSent and external.notification keyed " +
					"vtx.booking.<NanoID>:promoted:2026-09-16T20:41:00Z, and returns primaryKey.",
			},
			{
				Name:    "RecordBookingChangeNotice — a class moved to a new time",
				Payload: map[string]any{"bookingKey": "vtx.booking.<NanoID>", "sessionKey": "vtx.session.<NanoID>", "kind": "moved", "changeRef": "2026-10-01T15:00:00Z"},
				ExpectedOutcome: "Validates the booking is alive and booked and that the session's .schedule.startsAt = changeRef, then " +
					"writes movedFor: 2026-10-01T15:00:00Z (carrying any existing promotedFor) and emits the notice keyed " +
					"vtx.booking.<NanoID>:moved:2026-10-01T15:00:00Z. Refuses StaleChange if the class has moved again since " +
					"the row was projected — the next dispatch carries the current time.",
			},
			{
				Name:    "RecordBookingChangeNotice — a class handed to a sub",
				Payload: map[string]any{"bookingKey": "vtx.booking.<NanoID>", "sessionKey": "vtx.session.<NanoID>", "kind": "instructor", "changeRef": "2026-09-18T20:00:00Z"},
				ExpectedOutcome: "Validates the booking is alive and booked and that the session's .schedule.instructorChangedAt = " +
					"changeRef, then writes instructorFor: 2026-09-18T20:00:00Z (carrying the other kinds' fields) and emits the " +
					"notice keyed vtx.booking.<NanoID>:instructor:2026-09-18T20:00:00Z, naming who leads now (or nobody). A " +
					"room change is the same with kind=room over .schedule.studioChangedAt.",
			},
		},
	}
}

// changeNoticeAspectTypeDDL declares the .changeNotice aspect (class
// bookingChangeNotice) — the step-6 write gate for RecordBookingChangeNotice.
// Declaration-only (the script lives on the bookingChangeNoticeOp vertexType
// DDL). NON-sensitive: it carries only instants (no PII), on a vtx.booking
// (not an identity), so step-6's sensitiveAspectScope does not fire.
func changeNoticeAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     changeNoticeAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{changeNoticeOp},
		Description: "Booking change-notice marker aspect (wellness-reminders). Stored as vtx.booking.<NanoID>.changeNotice " +
			"(class bookingChangeNotice) = {promotedFor?, movedFor?, instructorFor?, roomFor?, sentAt}. Non-sensitive. Written ONLY by " +
			"RecordBookingChangeNotice (whose bookingChangeNoticeOp vertexType DDL owns the script) as a create-or-update " +
			"carrying the other kinds' fields forward; " +
			"this aspect-type DDL is the step-6 write gate. Declaration-only: no op handler. promotedFor = the " +
			".status.promotedAt the promotion notice was for (equality closes missing_promotion_notice, once — " +
			"promotedAt never changes); movedFor = the session startsAt the last move notice was for (equality closes " +
			"missing_move_notice; a further move reopens it); instructorFor = the .schedule.instructorChangedAt the last " +
			"instructor notice was for, roomFor = the .schedule.studioChangedAt the last room notice was for (each " +
			"equality closes its gap; a further change reopens it). Created at the first notice, carried per kind, never " +
			"reset; dies with the booking.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"promotedFor":{"type":"string","description":"The .status.promotedAt instant (RFC3339, canonical UTC) the promotion notice was for."},` +
			`"movedFor":{"type":"string","description":"The session startsAt (RFC3339, canonical UTC) the last move notice was for."},` +
			`"instructorFor":{"type":"string","description":"The session's .schedule.instructorChangedAt (RFC3339, canonical UTC) the last instructor notice was for."},` +
			`"roomFor":{"type":"string","description":"The session's .schedule.studioChangedAt (RFC3339, canonical UTC) the last room notice was for."},` +
			`"sentAt":{"type":"string","description":"RFC3339 instant the latest notice of either kind was recorded (the op's submittedAt, canonical UTC)."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"promotedFor":   "The .status.promotedAt instant the promotion notice was for. promotedFor = promotedAt closes the promotion gap.",
			"movedFor":      "The session startsAt the last move notice was for. movedFor = the current startsAt closes the move gap; a further ReassignSession reopens it.",
			"instructorFor": "The session's .schedule.instructorChangedAt the last instructor notice was for. Equality closes the instructor gap; a further swap, clear or assignment reopens it.",
			"roomFor":       "The session's .schedule.studioChangedAt the last room notice was for. Equality closes the room gap; a further room move reopens it.",
			"sentAt":        "RFC3339 instant the latest notice of either kind was recorded (op.submittedAt, canonical UTC).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "booking change-notice marker aspect",
				Payload:         map[string]any{"promotedFor": "2026-09-16T20:41:00Z", "movedFor": "2026-10-01T15:00:00Z", "instructorFor": "2026-09-18T20:00:00Z", "sentAt": "2026-09-18T20:00:05Z"},
				ExpectedOutcome: "Stored as vtx.booking.<NanoID>.changeNotice; written by RecordBookingChangeNotice.",
			},
		},
	}
}

// recordChangeNoticeScript handles RecordBookingChangeNotice. It reads the
// booking ROOT and its .status aspect (declared reads) to assert the seat is
// alive and `booked`, the session ROOT (declared read) to assert the class
// is not being called off, the session's .schedule (declared read) for the
// startsAt the move check compares and every notice carries, and the
// booking's own .changeNotice (declared optionalRead) so the write can carry
// the other kind's field. The change is re-checked
// against the live aspect before anything is emitted: a row Weaver
// dispatched from is a snapshot, and a promotion or move it names that the
// graph has since outrun is refused StaleChange rather than told.
const recordChangeNoticeScript = `
def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def optional_param(p, name):
    # A row column the target templates as a param: null when the walk it
    # comes from bound nothing, else its string.
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        return None
    return v.strip()

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

# Each kind names the .changeNotice field that records it and the stamp on
# the session's schedule it is re-checked against (a promotion's stamp is on
# the booking's own .status instead).
CHANGE_KINDS = ["promoted", "moved", "instructor", "room"]
KIND_FIELDS = {"promoted": "promotedFor", "moved": "movedFor", "instructor": "instructorFor", "room": "roomFor"}
KIND_STAMPS = {"moved": "startsAt", "instructor": "instructorChangedAt", "room": "studioChangedAt"}

def required_kind(p):
    kind = required_string(p, "kind")
    if kind not in CHANGE_KINDS:
        fail("InvalidArgument: kind: must be one of promoted, moved, instructor, room; got " + kind)
    return kind

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordBookingChangeNotice":
        # actor-guard: (primordial) restricted to Weaver's dispatch actor, see
        # declared-read-scope-authorization-design.md §12. The grant behind this
        # op is operator/Scope:"any", which admits every operator-role holder —
        # far wider than the one engine that dispatches the
        # wellnessBookingChangeNotices directOp playbook. bookingKey arrives off
        # the payload and is forwarded in the external.notification body the
        # bridge turns into a real message to the client, so a wider submitter
        # set is a forged send: an arbitrary operator naming any booking it
        # likes and having the platform notify that client. First statement in
        # the branch: it also denies the payload-shape and vertex-alive oracles
        # beneath it.
        if op.actor != primordialActor["weaver"]:
            fail("AuthDenied: RecordBookingChangeNotice is restricted to Weaver's dispatch actor; got " + op.actor)

        booking_key = required_string(p, "bookingKey")
        parts_of(booking_key, "bookingKey", "booking")
        session_key = required_string(p, "sessionKey")
        parts_of(session_key, "sessionKey", "session")
        kind = required_kind(p)
        change_ref = required_string(p, "changeRef")

        # Liveness guard: never tell anyone about an absent/tombstoned booking.
        # The op hydrates [bookingKey] (ContextHint.Reads), so the root is in state.
        if not vertex_alive(state, booking_key):
            fail("UnknownBooking: " + booking_key + " is absent or tombstoned; no notice sent")

        # Session liveness guard: a row projected before TombstoneSession and
        # dispatched after it must not tell the member "you're in" or "your
        # class moved" about a class being called off — ReleaseOrphanedBooking's
        # own notice (wellness-domain) covers that seat. The op hydrates
        # [sessionKey] (ContextHint.Reads), so the root is in state.
        if not vertex_alive(state, session_key):
            fail("UnknownSession: " + session_key + " is absent or tombstoned; the call-off notice is the release's own")

        # Only a confirmed seat is told: a waitlisted booker has no seat that
        # was moved, and attended / noShow means the class already happened.
        # read-posture: (a) declared in contextHint.reads — the
        # wellnessBookingChangeNotices target's Reads list (targets.go)
        # declares bookingKey.status.
        status = kv.Read(booking_key + ".status")
        if status == None or status.isDeleted:
            fail("InvalidState: " + booking_key + ".status is absent; cannot tell a seat with no status")
        if status.data.get("value") != "booked":
            fail("InvalidState: " + booking_key + " is " + str(status.data.get("value")) + ", not booked; no notice sent")

        # No ClassAlreadyStarted guard here, where recordReminderScript's sits:
        # a promotion after the class has started is refused upstream
        # (PromoteWaitlistedBookings and CancelBooking both refuse
        # SessionInPast), so a promotion notice never reaches a started class;
        # and a move notice for a class the desk moves mid-session is still a
        # true fact the member should hear. The class-over conjunct on the lens
        # is what bounds both.
        #
        # The session's current time, for the move check and for every notice's
        # params (a promotion notice tells the member WHEN the class is too).
        # read-posture: (a) declared in contextHint.reads — the
        # wellnessBookingChangeNotices target's Reads list (targets.go)
        # declares sessionKey.schedule.
        schedule = kv.Read(session_key + ".schedule")
        if schedule == None or schedule.isDeleted:
            fail("MissingSchedule: " + session_key + ".schedule is absent; cannot tell a seat about a class with no time")
        starts_at = schedule.data.get("startsAt")
        if starts_at == None:
            fail("MissingSchedule: " + session_key + ".schedule carries no startsAt")

        # The change is re-checked against the LIVE aspect, never trusted off
        # the row: the row is a projection snapshot, and a promotion or move it
        # names that the graph has since outrun is refused, not told. The next
        # dispatch carries the current value.
        if kind == "promoted":
            promoted_at = status.data.get("promotedAt")
            if promoted_at == None:
                fail("StaleChange: " + booking_key + " carries no promotedAt; nothing to tell")
            if promoted_at != change_ref:
                fail("StaleChange: " + booking_key + " promotedAt " + promoted_at + " is not the dispatched changeRef " + change_ref)
        else:
            # moved re-checks startsAt; instructor / room re-check the stamp
            # ReassignSession recorded for that change. A stamp the schedule
            # does not carry is a row the graph has outrun (or never had).
            stamp_field = KIND_STAMPS[kind]
            stamp = schedule.data.get(stamp_field)
            if stamp == None:
                fail("StaleChange: " + session_key + ".schedule carries no " + stamp_field + "; nothing to tell")
            if stamp != change_ref:
                fail("StaleChange: " + session_key + " " + stamp_field + " " + stamp + " is not the dispatched changeRef " + change_ref +
                     "; the class changed again since the row was projected")

        sent_at = time.rfc3339_utc(op.submittedAt)

        # The marker carries EVERY kind's field: notices of different kinds
        # converging on one booking each set their own field and carry the
        # others' forward, so no write erases another's evidence.
        # read-posture: (d) declared optionalReads at wellnessBookingChangeNotices
        # dispatch (the create-or-update branch below).
        existing = kv.Read(booking_key + ".changeNotice")
        marker = {}
        if existing != None and not existing.isDeleted:
            for k in CHANGE_KINDS:
                carried = existing.data.get(KIND_FIELDS[k])
                if carried != None:
                    marker[KIND_FIELDS[k]] = carried
        marker[KIND_FIELDS[kind]] = change_ref
        marker["sentAt"] = sent_at

        marker_key = booking_key + ".changeNotice"
        marker_doc = {"class": "bookingChangeNotice", "vertexKey": booking_key,
                      "localName": "changeNotice", "isDeleted": False, "data": marker}
        if existing != None:
            # A BARE update on the hydrated key (a logically-deleted marker is
            # still a live KV envelope, revived through the same path): the
            # Processor conditions it on the step-4 revision the optionalRead
            # observed (Contract #3 §3.2) and records the condition as
            # defaulted, which is what makes a conflict retry-eligible
            # in-process — re-hydrate, re-execute, carry the winner's field.
            # That is how two concurrent kinds converge on one marker. An
            # explicit expectedRevision would be an unretried CAS: the loser
            # is rejected outright and waits out the mark lease instead.
            marker_mut = {"op": "update", "key": marker_key, "document": marker_doc}
        else:
            marker_mut = {"op": "create", "key": marker_key, "document": marker_doc}

        events = [{"class": "wellness.bookingChangeNoticeSent",
                   "data": {"bookingKey": booking_key, "kind": kind, "changeRef": change_ref, "sentAt": sent_at}}]

        # Fire the actual notification send off this op's own transactional
        # outbox. The external ref keys on (bookingKey, kind, changeRef): a
        # redelivery of the SAME change reuses the key so the adapter dedups,
        # while a second move (a new startsAt) mints a fresh key and sends
        # again — the same reopen semantics the .changeNotice marker has.
        ext_ref = booking_key + ":" + kind + ":" + change_ref
        params = {"bookingKey": booking_key, "changeType": kind, "changeRef": change_ref,
                  "sessionKey": session_key, "startsAt": starts_at}
        class_name = status.data.get("className")
        if class_name != None:
            params["className"] = class_name
        # An instructor notice says who leads now (an un-led class names
        # nobody); a room notice says where the class meets. Both arrive off
        # the dispatching row -- the lens's ledBy / atStudio walks -- so the
        # script reads no link of its own.
        if kind == "instructor":
            params["instructorName"] = optional_param(p, "instructorName")
        if kind == "room":
            params["studioName"] = optional_param(p, "studioName")
        events.append({"class": "external.notification",
                       "data": {"instanceKey": ext_ref, "adapter": "notification",
                                "replyOp": "RecordBookingChangeNotification",
                                "externalRef": ext_ref, "idempotencyKey": ext_ref,
                                "params": params}})

        return {"mutations": [marker_mut], "events": events,
                "response": {"primaryKey": booking_key}}

    fail("bookingChangeNoticeOp DDL: unknown operationType: " + ot)
`

// changeNoticePermissions grants RecordBookingChangeNotice to the `operator`
// role (scope any) — Weaver's service actor dispatches the directOp under
// operator authority; the script's own actor guard narrows the effective
// submitter set to Weaver's dispatch actor.
func changeNoticePermissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: changeNoticeOp,
			Scope:         "any",
			Note:          "Grants the operator the right to submit RecordBookingChangeNotice operations (orchestration-internal: the wellnessBookingChangeNotices directOp playbook, dispatched by Weaver's service actor).",
			GrantsTo:      []string{"operator"},
		},
	}
}

// changeNoticeOpMetas makes RecordBookingChangeNotice forOperation-resolvable
// for discoverability; the playbook dispatches it directly, so the meta is
// not load-bearing for dispatch.
func changeNoticeOpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: changeNoticeOp},
	}
}
