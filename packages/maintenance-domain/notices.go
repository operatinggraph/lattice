package maintenancedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// The resolved notice: a reporter whose work order someone else resolved is
// told once, keyed on the resolution's own stamp. The workOrderResolvedNotices
// lens (lenses.go) projects the level-triggered gap; RecordWorkOrderResolvedNotice
// is the directOp its playbook (targets.go) dispatches, and the .resolvedNotice
// marker it writes is what closes it. The op mirrors clinic-reminders'
// RecordAppointmentChangeNotice: the same Weaver-actor guard, the same
// declared-read posture, the same StaleChange re-check against the live
// aspect, the same create-or-bare-update marker, the same external.notification
// egress off its own outbox. The bridge's replyOp for that egress,
// RecordWorkOrderResolvedNotification, mirrors clinic's
// RecordAppointmentChangeNotification, including its externalRef parse.
//
// Both ops are owned by the workOrder vertexType DDL (ddls.go — an op is
// admitted by exactly one vertexType DDL, and this package has one); their
// branches live in execute_notice below, which the vertex script's execute
// tail-calls. The two aspect-type DDLs here are the step-6 write gates:
//
//   - workOrderResolvedNotice — .resolvedNotice = {resolvedFor, sentAt}, written
//     by RecordWorkOrderResolvedNotice.
//   - workOrderResolvedNotification — .resolvedNotification = {status, sentAt},
//     written by RecordWorkOrderResolvedNotification (audit only; gates no lens).
const (
	resolvedNoticeAspectDDL       = "workOrderResolvedNotice"
	resolvedNotificationAspectDDL = "workOrderResolvedNotification"

	resolvedNoticeOp       = "RecordWorkOrderResolvedNotice"
	resolvedNotificationOp = "RecordWorkOrderResolvedNotification"
)

// workOrderResolvedNoticeAspectTypeDDL declares the .resolvedNotice aspect
// (class workOrderResolvedNotice) — the step-6 write gate for
// RecordWorkOrderResolvedNotice. Declaration-only. NON-sensitive: it carries
// only instants (no PII), on a vtx.workorder (not an identity).
func workOrderResolvedNoticeAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     resolvedNoticeAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{resolvedNoticeOp},
		Description: "Work-order resolved-notice marker aspect. Stored as vtx.workorder.<NanoID>.resolvedNotice " +
			"(class workOrderResolvedNotice) = {resolvedFor, sentAt}. Non-sensitive. Written ONLY by " +
			"RecordWorkOrderResolvedNotice (whose workOrder vertexType DDL owns the script) as a create when absent " +
			"and a bare update on the hydrated key when present; this aspect-type DDL is the step-6 write gate. " +
			"Declaration-only: no op handler. resolvedFor = the .resolution.resolvedAt the notice was for — equality " +
			"with the live resolvedAt closes the workOrderResolvedNotices gap. A resolution is terminal, so the " +
			"marker is written once per order in practice; dies with the work order.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"resolvedFor":{"type":"string","description":"The .resolution.resolvedAt instant (RFC3339, canonical UTC) the notice was for."},` +
			`"sentAt":{"type":"string","description":"RFC3339 instant the notice was recorded (the op's submittedAt, canonical UTC)."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"resolvedFor": "The .resolution.resolvedAt instant the notice was for. resolvedFor = resolvedAt closes the gap.",
			"sentAt":      "RFC3339 instant the notice was recorded (op.submittedAt, canonical UTC).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "work-order resolved-notice marker aspect",
				Payload:         map[string]any{"resolvedFor": "2026-07-21T11:30:00Z", "sentAt": "2026-07-21T11:30:05Z"},
				ExpectedOutcome: "Stored as vtx.workorder.<NanoID>.resolvedNotice; written by RecordWorkOrderResolvedNotice.",
			},
		},
	}
}

// workOrderResolvedNotificationAspectTypeDDL declares the .resolvedNotification
// aspect (class workOrderResolvedNotification) — the step-6 write gate for
// RecordWorkOrderResolvedNotification. Declaration-only. NON-sensitive: a
// status and a timestamp, on a vtx.workorder.
func workOrderResolvedNotificationAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     resolvedNotificationAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{resolvedNotificationOp},
		Description: "Work-order resolved-notification-outcome aspect. Stored as vtx.workorder.<NanoID>.resolvedNotification " +
			"(class workOrderResolvedNotification) = {status, sentAt}. Non-sensitive. Written ONLY by " +
			"RecordWorkOrderResolvedNotification (unconditioned update — latest outcome wins); declaration-only, " +
			"no op handler. Audit/observability marker — gates no lens.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"status":{"type":"string","description":"The adapter's terminal verdict (completed|failed)."},` +
			`"sentAt":{"type":"string","description":"RFC3339 instant the outcome was recorded (the replyOp's submittedAt, canonical UTC)."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"status": "The adapter's terminal verdict (completed|failed).",
			"sentAt": "RFC3339 instant the outcome was recorded (op.submittedAt, canonical UTC).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "work-order resolved-notification-outcome aspect",
				Payload:         map[string]any{"status": "completed", "sentAt": "2026-07-21T11:30:08Z"},
				ExpectedOutcome: "Stored as vtx.workorder.<NanoID>.resolvedNotification; written by RecordWorkOrderResolvedNotification.",
			},
		},
	}
}

// workOrderNoticeScript is the notice half of the workOrder vertex DDL's
// script, appended to workOrderDDLScript (ddls.go): execute_notice handles
// RecordWorkOrderResolvedNotice and RecordWorkOrderResolvedNotification and is
// the vertex script's tail call, so every helper ddls.go defines
// (required_string, parts_of, vertex_alive, make_aspect) is in scope here.
//
// RecordWorkOrderResolvedNotice reads the work-order ROOT (declared read) to
// assert the order is alive, its .report and .resolution (declared reads) for
// the recipient and the re-check, and its own .resolvedNotice (declared
// optionalRead) for the create-or-update branch. The change is re-checked
// against the LIVE aspect before anything is emitted: the row Weaver
// dispatched from is a snapshot, and a resolvedAt it names that the graph does
// not carry is refused StaleChange rather than told.
//
// RecordWorkOrderResolvedNotification reads NOTHING from state (the bridge
// submits no ContextHint.Reads): externalRef is split into the work-order key
// (refused unless it is a vtx.workorder.<NanoID> — the token is minted by the
// emitter and echoed by the bridge, so the shape is validated here, at the
// write), the kind and the change reference, and the .resolvedNotification
// aspect is written as an UNCONDITIONED update — create-if-absent,
// overwrite-if-present — so a redelivered reply lands cleanly with no OCC pin
// needed. It runs no liveness check: a reply is an audit record of a send that
// already happened, and an order tombstoned after its notice went out must
// still be able to record how that send ended.
const workOrderNoticeScript = `
OUTCOME_STATUSES = ["completed", "failed"]

def required_status(p):
    st = required_string(p, "status")
    if st not in OUTCOME_STATUSES:
        fail("InvalidArgument: status: must be one of completed, failed; got " + st)
    return st

NOTICE_KINDS = ["resolved"]

def split_external_ref(ref):
    # Keys carry dots, never colons, so the first ':' ends the key; the kind
    # carries neither, so the second ':' ends it; the changeRef is an RFC3339
    # instant that carries its own colons, so it is whatever is left -- split
    # at most twice.
    idx1 = ref.find(":")
    if idx1 <= 0:
        fail("InvalidArgument: externalRef: required <workOrderKey>:resolved:<changeRef>; got " + ref)
    wkey = ref[:idx1]
    # Only a vtx.workorder.<NanoID> is a place this op may write: the bridge
    # echoes externalRef verbatim, so the key it recovers is shaped by
    # whoever minted the token, and an aspect on any other vertex type would
    # be an unguarded cross-type write under the operator grant.
    parts_of(wkey, "externalRef", "workorder")
    rest = ref[idx1 + 1:]
    idx2 = rest.find(":")
    if idx2 <= 0:
        fail("InvalidArgument: externalRef: required <workOrderKey>:resolved:<changeRef>; got " + ref)
    kind = rest[:idx2]
    change_ref = rest[idx2 + 1:]
    if kind not in NOTICE_KINDS:
        fail("InvalidArgument: externalRef: kind must be resolved; got " + kind)
    if len(change_ref) == 0:
        fail("InvalidArgument: externalRef: changeRef segment is empty; got " + ref)
    return wkey, kind, change_ref

def execute_notice(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordWorkOrderResolvedNotice":
        # actor-guard: (primordial) restricted to Weaver's dispatch actor, see
        # declared-read-scope-authorization-design.md §12. The grant behind
        # this op is operator/Scope:"any", which admits every operator-role
        # holder -- far wider than the one engine that dispatches the
        # workOrderResolvedNotices directOp playbook. workOrderKey arrives off
        # the payload and the reporter it resolves is forwarded in the
        # external.notification body the bridge turns into a real message, so
        # a wider submitter set is a forged send: an arbitrary operator naming
        # any order it likes and having the platform notify that reporter.
        # First statement in the branch: it also denies the payload-shape and
        # vertex-alive oracles beneath it.
        if op.actor != primordialActor["weaver"]:
            fail("AuthDenied: RecordWorkOrderResolvedNotice is restricted to Weaver's dispatch actor; got " + op.actor)

        wkey = required_string(p, "workOrderKey")
        parts_of(wkey, "workOrderKey", "workorder")
        change_ref = required_string(p, "changeRef")

        # Liveness guard: never tell anyone about an absent/tombstoned order.
        # The op hydrates [workOrderKey] (ContextHint.Reads), so the root is in state.
        if not vertex_alive(state, wkey):
            fail("UnknownWorkOrder: " + wkey + " is absent or tombstoned; no notice sent")

        # The live resolution: the re-check reads its resolvedAt, the notice
        # carries its notes, and the self-resolved refusal reads resolvedBy.
        # read-posture: (a) declared in contextHint.reads -- the
        # workOrderResolvedNotices target's Reads list (targets.go) declares
        # workOrderKey.resolution.
        resolution = kv.Read(wkey + ".resolution")
        if resolution == None or resolution.isDeleted:
            fail("StaleChange: " + wkey + ".resolution is absent; nothing to tell")
        resolved_at = resolution.data.get("resolvedAt")
        if resolved_at == None:
            fail("StaleChange: " + wkey + ".resolution carries no resolvedAt; nothing to tell")
        # The change is re-checked against the LIVE aspect, never trusted off
        # the row: the row is a projection snapshot. A resolution is terminal,
        # so a mismatch here is a row projected against state the graph does
        # not carry, refused rather than told.
        if resolved_at != change_ref:
            fail("StaleChange: " + wkey + " resolvedAt " + resolved_at + " is not the dispatched changeRef " + change_ref)
        resolved_by = resolution.data.get("resolvedBy")

        # The recipient is the .report stamp -- the value the reportedBy link
        # was minted from, so the two never disagree; the lens walks the link,
        # the op reads the declared aspect.
        # read-posture: (a) declared in contextHint.reads -- the
        # workOrderResolvedNotices target's Reads list (targets.go) declares
        # workOrderKey.report.
        report = kv.Read(wkey + ".report")
        if report == None or report.isDeleted:
            fail("StaleChange: " + wkey + ".report is absent; no reporter to tell")
        reporter = report.data.get("reportedBy")
        if reporter == None or type(reporter) != type("") or len(reporter) == 0:
            fail("StaleChange: " + wkey + ".report carries no reportedBy; no reporter to tell")
        # Nobody is told what they resolved themselves -- the lens's
        # resolvedBy <> reporterKey conjunct, re-checked here on the live
        # aspect so a row that outran it is refused rather than sent.
        if reporter == resolved_by:
            fail("SelfResolved: " + wkey + " was resolved by its own reporter; nothing to tell")

        sent_at = time.rfc3339_utc(op.submittedAt)

        # The marker carries ONE field beside sentAt: a resolution is terminal
        # and this loop tells one kind, so there is no sibling field to carry
        # forward (clinic's marker carries three kinds; this one has nothing
        # to preserve, and states so here rather than by omission).
        # read-posture: (d) declared optionalReads at workOrderResolvedNotices
        # dispatch (the create-or-update branch below).
        existing = kv.Read(wkey + ".resolvedNotice")
        marker = {"resolvedFor": change_ref, "sentAt": sent_at}

        marker_key = wkey + ".resolvedNotice"
        marker_doc = {"class": "workOrderResolvedNotice", "vertexKey": wkey,
                      "localName": "resolvedNotice", "isDeleted": False, "data": marker}
        if existing != None:
            # A BARE update on the hydrated key (a logically-deleted marker is
            # still a live KV envelope, revived through the same path): the
            # Processor conditions it on the step-4 revision the optionalRead
            # observed (Contract #3 §3.2) and records the condition as
            # defaulted, which is what makes a conflict retry-eligible
            # in-process. An explicit expectedRevision would be an unretried
            # CAS: the loser is rejected outright and waits out the mark lease.
            marker_mut = {"op": "update", "key": marker_key, "document": marker_doc}
        else:
            marker_mut = {"op": "create", "key": marker_key, "document": marker_doc}

        events = [{"class": "maintenance.workOrderResolvedNoticeSent",
                   "data": {"workOrderKey": wkey, "reporter": reporter, "changeRef": change_ref, "sentAt": sent_at}}]

        # Fire the actual notification send off this op's own transactional
        # outbox. The external ref keys on (workOrderKey, resolved, changeRef):
        # a redelivery of the SAME resolution reuses the key so the adapter
        # dedups. The recipient rides in params -- the bridge reads them as
        # opaque JSON.
        ext_ref = wkey + ":resolved:" + change_ref
        params = {"workOrderKey": wkey, "reporterKey": reporter, "changeType": "resolved",
                  "changeRef": change_ref, "summary": report.data.get("summary"),
                  "notes": resolution.data.get("notes")}
        events.append({"class": "external.notification",
                       "data": {"instanceKey": ext_ref, "adapter": "notification",
                                "replyOp": "RecordWorkOrderResolvedNotification",
                                "externalRef": ext_ref, "idempotencyKey": ext_ref,
                                "params": params}})

        return {"mutations": [marker_mut], "events": events,
                "response": {"primaryKey": wkey}}

    if ot == "RecordWorkOrderResolvedNotification":
        ext_ref = required_string(p, "externalRef")
        wkey, kind, change_ref = split_external_ref(ext_ref)
        status = required_status(p)
        sent_at = time.rfc3339_utc(op.submittedAt)

        marker_key = wkey + ".resolvedNotification"
        mutations = [
            {"op": "update", "key": marker_key,
             "document": {"class": "workOrderResolvedNotification", "vertexKey": wkey,
                          "localName": "resolvedNotification", "isDeleted": False,
                          "data": {"status": status, "sentAt": sent_at}}},
        ]
        events = [{"class": "maintenance.workOrderResolvedNotificationRecorded",
                   "data": {"workOrderKey": wkey, "kind": kind, "changeRef": change_ref, "status": status}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": wkey}}

    fail("UnknownOperation: " + ot)
`
