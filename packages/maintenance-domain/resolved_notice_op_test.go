// RecordWorkOrderResolvedNotice + RecordWorkOrderResolvedNotification
// integration tests — the two ops' write paths driven end to end through a
// real Processor, on the harness integration_test.go builds. The work order
// root and its .report / .resolution aspects are SEEDED directly rather than
// minted through ReportIssue / ResolveWorkOrder: the notice op reads only the
// root (vertex_alive), .report (reportedBy, summary), .resolution
// (resolvedAt, resolvedBy, notes) and its own .resolvedNotice marker, so the
// fixtures carry exactly those, in the shape the two writers leave. The
// convergence PREDICATE (when the gap opens/closes) is proven on the real
// engine in resolved_notice_lens_test.go.
package maintenancedomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const (
	rnResolvedAt = "2026-07-21T11:30:00Z"
	rnSubmitted  = "2026-07-21T11:30:05Z"
	rnReporter   = "vtx.identity.BBMANTRNREPQRTERHJKM"
	rnResolver   = "vtx.identity.BBMANTRNRESQLVERHJKM"
)

// rnSeedOrder seeds a work order root (live or tombstoned) with a .report
// stamping rnReporter and, when resolvedBy is non-empty, a .resolution
// stamped rnResolvedAt by resolvedBy.
func rnSeedOrder(t *testing.T, ctx context.Context, conn *substrate.Conn, woKey string, alive bool, resolvedBy string) {
	t.Helper()
	put := func(key string, doc map[string]any) {
		b, _ := json.Marshal(doc)
		if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	put(woKey, map[string]any{"class": "workorder", "isDeleted": !alive, "data": map[string]any{}})
	put(woKey+".report", map[string]any{"class": "workOrderReport", "vertexKey": woKey, "localName": "report", "isDeleted": false,
		"data": map[string]any{"summary": "Kitchen tap is dripping", "priority": "normal", "reportedAt": "2026-07-21T09:00:00Z", "reportedBy": rnReporter}})
	if resolvedBy != "" {
		put(woKey+".resolution", map[string]any{"class": "workOrderResolution", "vertexKey": woKey, "localName": "resolution", "isDeleted": false,
			"data": map[string]any{"notes": "Replaced the washer.", "resolvedAt": rnResolvedAt, "resolvedBy": resolvedBy}})
	}
}

// rnSubmit drives one RecordWorkOrderResolvedNotice as `actor` with the exact
// declared-read posture the workOrderResolvedNotices target dispatches under
// (Reads: root, .report, .resolution; OptionalReads: .resolvedNotice). Class
// is LEFT EMPTY so the Processor's operationType→class reverse index resolves
// the handler.
func rnSubmit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	actor, label, woKey, changeRef string, want processor.MessageOutcome) (*processor.OperationEnvelope, *processor.OperationReply) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "RecordWorkOrderResolvedNotice",
		Actor:         actor,
		SubmittedAt:   rnSubmitted,
		Payload:       json.RawMessage(`{"workOrderKey":"` + woKey + `","changeRef":"` + changeRef + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{woKey, woKey + ".report", woKey + ".resolution"},
			OptionalReads: []string{woKey + ".resolvedNotice"},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != want {
		t.Fatalf("%s: outcome = %v, want %v (reply: %+v)", label, outcome, want, reply.Error)
	}
	return env, reply
}

// rnOutboxEvents reads the op's own transactional outbox and returns every
// external.notification payload it emitted plus the full event-class list.
func rnOutboxEvents(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID string) ([]map[string]any, []string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(requestID))
	if err != nil {
		t.Fatalf("read outbox aspect: %v", err)
	}
	ob, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect: %v", err)
	}
	var notifs []map[string]any
	var seen []string
	for _, e := range ob.Data.Events {
		seen = append(seen, e.EventType)
		if e.EventType == "external.notification" {
			notifs = append(notifs, e.Payload)
		}
	}
	return notifs, seen
}

// rnRequireNotice asserts the outbox carries exactly one external.notification
// keyed <woKey>:resolved:<changeRef> beside maintenance.workOrderResolvedNoticeSent,
// addressed to the notification adapter with this package's replyOp and the
// recipient + order-identifying params.
func rnRequireNotice(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID, woKey, changeRef string) {
	t.Helper()
	notifs, seen := rnOutboxEvents(t, ctx, conn, requestID)
	if len(notifs) != 1 {
		t.Fatalf("want exactly one external.notification, got %d (events: %v)", len(notifs), seen)
	}
	var sent bool
	for _, e := range seen {
		if e == "maintenance.workOrderResolvedNoticeSent" {
			sent = true
		}
	}
	if !sent {
		t.Fatalf("maintenance.workOrderResolvedNoticeSent not emitted (events: %v)", seen)
	}
	n := notifs[0]
	wantRef := woKey + ":resolved:" + changeRef
	for _, field := range []string{"instanceKey", "externalRef", "idempotencyKey"} {
		if got, _ := n[field].(string); got != wantRef {
			t.Fatalf("external.notification %s = %q, want %q", field, got, wantRef)
		}
	}
	if got, _ := n["adapter"].(string); got != "notification" {
		t.Fatalf("adapter = %q, want notification", got)
	}
	if got, _ := n["replyOp"].(string); got != "RecordWorkOrderResolvedNotification" {
		t.Fatalf("replyOp = %q, want RecordWorkOrderResolvedNotification", got)
	}
	params, _ := n["params"].(map[string]any)
	for field, want := range map[string]string{
		"workOrderKey": woKey, "reporterKey": rnReporter, "changeType": "resolved", "changeRef": changeRef,
		"summary": "Kitchen tap is dripping", "notes": "Replaced the washer.",
	} {
		if got, _ := params[field].(string); got != want {
			t.Fatalf("params.%s = %q, want %q (params: %v)", field, got, want, params)
		}
	}
}

// rnRequireRefusal pins a refusal by its named reason, and that no marker and
// no outbox (no external.notification) were written for it.
func rnRequireRefusal(t *testing.T, ctx context.Context, conn *substrate.Conn, env *processor.OperationEnvelope, reply *processor.OperationReply, woKey, wantReason string) {
	t.Helper()
	if reply.Error == nil || !strings.Contains(reply.Error.Message, wantReason) {
		t.Fatalf("want a %s rejection, got %+v", wantReason, reply.Error)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, woKey+".resolvedNotice"); err == nil {
		t.Fatalf("a refused notice must write NO .resolvedNotice marker")
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(env.RequestID)); err == nil {
		t.Fatalf("a refused notice must leave NO outbox aspect (no external.notification)")
	}
}

func rnSetup(t *testing.T, durable string) (context.Context, *substrate.Conn, *processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	ctx, conn := setupMaintenanceEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, mdWeaverCapDoc())
	cp, cons := mdPipeline(t, ctx, conn, durable)
	return ctx, conn, cp, cons
}

// TestRecordWorkOrderResolvedNotice_WritesMarkerAndEmits is the ADMIT path:
// submitted as Weaver's dispatch actor with changeRef = the live resolvedAt,
// the op writes .resolvedNotice = {resolvedFor, sentAt} and emits exactly one
// external.notification keyed <woKey>:resolved:<resolvedAt> carrying the
// reporter as recipient.
func TestRecordWorkOrderResolvedNotice_WritesMarkerAndEmits(t *testing.T) {
	ctx, conn, cp, cons := rnSetup(t, "mdrnadmit")
	woKey := "vtx.workorder.BBMANTRNADMTHJKMNPQR"
	rnSeedOrder(t, ctx, conn, woKey, true, rnResolver)

	env, reply := rnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "mdrn00000000000001", woKey, rnResolvedAt, processor.OutcomeAccepted)
	if reply.PrimaryKey != woKey {
		t.Fatalf("primaryKey = %q, want %q", reply.PrimaryKey, woKey)
	}
	marker := mdReadDoc(t, ctx, conn, woKey+".resolvedNotice")
	if cls, _ := marker["class"].(string); cls != "workOrderResolvedNotice" {
		t.Fatalf("resolvedNotice class = %q, want workOrderResolvedNotice", cls)
	}
	md, _ := marker["data"].(map[string]any)
	if got, _ := md["resolvedFor"].(string); got != rnResolvedAt {
		t.Fatalf("resolvedNotice resolvedFor = %q, want %q", got, rnResolvedAt)
	}
	if got, _ := md["sentAt"].(string); got != rnSubmitted {
		t.Fatalf("resolvedNotice sentAt = %q, want the op's submittedAt %q", got, rnSubmitted)
	}
	if len(md) != 2 {
		t.Fatalf("resolvedNotice data = %v, want exactly {resolvedFor, sentAt}", md)
	}
	rnRequireNotice(t, ctx, conn, env.RequestID, woKey, rnResolvedAt)
}

// TestRecordWorkOrderResolvedNotice_RedeliveryIsABareUpdate: a second
// dispatch for the same resolution (a Weaver redelivery before the row
// re-projected) lands as a BARE update on the existing marker — the revision
// advances, the values are unchanged, and the notification is re-emitted
// under the SAME externalRef so the adapter dedups it.
func TestRecordWorkOrderResolvedNotice_RedeliveryIsABareUpdate(t *testing.T) {
	ctx, conn, cp, cons := rnSetup(t, "mdrnredeliver")
	woKey := "vtx.workorder.BBMANTRNREDLHJKMNPQR"
	rnSeedOrder(t, ctx, conn, woKey, true, rnResolver)

	_, first := rnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "mdrn00000000000002", woKey, rnResolvedAt, processor.OutcomeAccepted)
	firstRev := first.Revisions[woKey+".resolvedNotice"]
	if firstRev == 0 {
		t.Fatalf("first notice recorded no revision for the marker: %+v", first.Revisions)
	}

	env, second := rnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "mdrn00000000000003", woKey, rnResolvedAt, processor.OutcomeAccepted)
	if rev := second.Revisions[woKey+".resolvedNotice"]; rev <= firstRev {
		t.Fatalf("resolvedNotice revision = %d, want > the first %d (an update over the existing marker, not a refused create)", rev, firstRev)
	}
	md, _ := mdReadDoc(t, ctx, conn, woKey+".resolvedNotice")["data"].(map[string]any)
	if got, _ := md["resolvedFor"].(string); got != rnResolvedAt {
		t.Fatalf("resolvedNotice resolvedFor = %q after the redelivery, want %q carried", got, rnResolvedAt)
	}
	rnRequireNotice(t, ctx, conn, env.RequestID, woKey, rnResolvedAt)
}

// TestRecordWorkOrderResolvedNotice_StaleChangeRefused — the row Weaver
// dispatched from names a resolvedAt the live aspect does not carry, or the
// order is not resolved at all. Each is refused StaleChange, writes no marker
// and sends nothing: a stale row is refused, not trusted.
func TestRecordWorkOrderResolvedNotice_StaleChangeRefused(t *testing.T) {
	ctx, conn, cp, cons := rnSetup(t, "mdrnstale")

	t.Run("resolvedAt differs from changeRef", func(t *testing.T) {
		woKey := "vtx.workorder.BBMANTRNSTLAHJKMNPQR"
		rnSeedOrder(t, ctx, conn, woKey, true, rnResolver)
		env, reply := rnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "mdrn00000000000004", woKey, "2026-07-21T11:00:00Z", processor.OutcomeRejected)
		rnRequireRefusal(t, ctx, conn, env, reply, woKey, "StaleChange")
	})
	t.Run("not resolved", func(t *testing.T) {
		woKey := "vtx.workorder.BBMANTRNSTLBHJKMNPQR"
		rnSeedOrder(t, ctx, conn, woKey, true, "")
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID("mdrn00000000000005"),
			Lane:          processor.LaneDefault,
			OperationType: "RecordWorkOrderResolvedNotice",
			Actor:         bootstrap.WeaverIdentityKey,
			SubmittedAt:   rnSubmitted,
			Payload:       json.RawMessage(`{"workOrderKey":"` + woKey + `","changeRef":"` + rnResolvedAt + `"}`),
			// .resolution declared optional here so the absence reaches the
			// script's own StaleChange rather than a step-4 hydration miss.
			ContextHint: &processor.ContextHint{
				Reads:         []string{woKey, woKey + ".report"},
				OptionalReads: []string{woKey + ".resolution", woKey + ".resolvedNotice"},
			},
		}
		outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
		if outcome != processor.OutcomeRejected {
			t.Fatalf("outcome = %v, want Rejected", outcome)
		}
		rnRequireRefusal(t, ctx, conn, env, reply, woKey, "StaleChange")
	})
}

// TestRecordWorkOrderResolvedNotice_SelfResolvedRefused — the reporter
// resolved their own order: refused SelfResolved, no marker, no send. The
// lens's resolvedBy <> reporterKey conjunct never dispatches this shape; the
// op re-checks it on the live aspect regardless.
func TestRecordWorkOrderResolvedNotice_SelfResolvedRefused(t *testing.T) {
	ctx, conn, cp, cons := rnSetup(t, "mdrnself")
	woKey := "vtx.workorder.BBMANTRNSELFHJKMNPQR"
	rnSeedOrder(t, ctx, conn, woKey, true, rnReporter)

	env, reply := rnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "mdrn00000000000006", woKey, rnResolvedAt, processor.OutcomeRejected)
	rnRequireRefusal(t, ctx, conn, env, reply, woKey, "SelfResolved")
}

// TestRecordWorkOrderResolvedNotice_NonWeaverActorRefusedFirst proves the
// GUARD ORDER: the primordial actor-guard runs before the payload-shape,
// liveness and re-check oracles, so a non-Weaver actor is refused on actor
// grounds even when it names a TOMBSTONED order with a STALE changeRef. The
// operator actor holds the operator role AND the identical Scope:"any"
// grant, so step 3 authorizes it; only the script's actor check stops it
// from having the platform notify an arbitrary reporter about an arbitrary
// order.
func TestRecordWorkOrderResolvedNotice_NonWeaverActorRefusedFirst(t *testing.T) {
	ctx, conn, cp, cons := rnSetup(t, "mdrnforged")
	doc := mdOperatorCapDoc()
	doc.PlatformPermissions = append(doc.PlatformPermissions, processor.PlatformPermission{OperationType: "RecordWorkOrderResolvedNotice", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, doc)
	woKey := "vtx.workorder.BBMANTRNFQRGHJKMNPQR"
	rnSeedOrder(t, ctx, conn, woKey, false, rnResolver)

	env, reply := rnSubmit(t, ctx, conn, cp, cons, mdActorKey, "mdrn00000000000007", woKey, "2026-07-21T11:00:00Z", processor.OutcomeRejected)
	rnRequireRefusal(t, ctx, conn, env, reply, woKey, "AuthDenied")
	if !strings.Contains(reply.Error.Message, "Weaver's dispatch actor") {
		t.Fatalf("the denial must name the actor guard, got %q", reply.Error.Message)
	}
	for _, later := range []string{"UnknownWorkOrder", "StaleChange", "InvalidArgument"} {
		if strings.Contains(reply.Error.Message, later) {
			t.Fatalf("the actor guard must fire BEFORE the %s oracle; got %q", later, reply.Error.Message)
		}
	}
}

// TestRecordWorkOrderResolvedNotice_TombstonedOrderRefused proves the
// liveness guard: a TOMBSTONED order (present in KV, isDeleted=true, its
// aspects still carrying the resolution — so hydration succeeds and the
// guard, not a hydration miss, is what fires) is refused UnknownWorkOrder.
func TestRecordWorkOrderResolvedNotice_TombstonedOrderRefused(t *testing.T) {
	ctx, conn, cp, cons := rnSetup(t, "mdrndead")
	woKey := "vtx.workorder.BBMANTRNDEADHJKMNPQR"
	rnSeedOrder(t, ctx, conn, woKey, false, rnResolver)

	env, reply := rnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "mdrn00000000000008", woKey, rnResolvedAt, processor.OutcomeRejected)
	rnRequireRefusal(t, ctx, conn, env, reply, woKey, "UnknownWorkOrder")
}

// rnSubmitReply drives one RecordWorkOrderResolvedNotification as the
// operator actor with NO ContextHint — exactly as the bridge submits a
// replyOp — and returns the outcome + reply.
func rnSubmitReply(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	label, externalRef, status, result string) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "RecordWorkOrderResolvedNotification",
		Actor:         mdActorKey,
		SubmittedAt:   rnSubmitted,
		Payload:       json.RawMessage(`{"externalRef":"` + externalRef + `","status":"` + status + `","result":"` + result + `"}`),
	}
	return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
}

func rnGrantReply(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	doc := mdOperatorCapDoc()
	doc.PlatformPermissions = append(doc.PlatformPermissions, processor.PlatformPermission{OperationType: "RecordWorkOrderResolvedNotification", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, doc)
}

// rnRequireNotification asserts the .resolvedNotification aspect carries
// exactly {status, sentAt}.
func rnRequireNotification(t *testing.T, ctx context.Context, conn *substrate.Conn, woKey, status string) {
	t.Helper()
	doc := mdReadDoc(t, ctx, conn, woKey+".resolvedNotification")
	if cls, _ := doc["class"].(string); cls != "workOrderResolvedNotification" {
		t.Fatalf("resolvedNotification class = %q, want workOrderResolvedNotification", cls)
	}
	d, _ := doc["data"].(map[string]any)
	for field, want := range map[string]string{"status": status, "sentAt": rnSubmitted} {
		if got, _ := d[field].(string); got != want {
			t.Fatalf("resolvedNotification %s = %q, want %q (data: %v)", field, got, want, d)
		}
	}
	if len(d) != 2 {
		t.Fatalf("resolvedNotification data = %v, want exactly {status, sentAt}", d)
	}
}

// TestRecordWorkOrderResolvedNotification_LandsAndOverwrites drives the
// replyOp the bridge posts after its adapter Executes: no Reads (the bridge
// submits none), the .resolvedNotification aspect lands off the three-way
// split externalRef (the changeRef carries its own colons), the domain event
// is emitted, and a redelivered reply overwrites — the latest outcome wins.
// It lands on a tombstoned order too: the reply is an audit record of a send
// that already happened.
func TestRecordWorkOrderResolvedNotification_LandsAndOverwrites(t *testing.T) {
	ctx, conn, cp, cons := rnSetup(t, "mdrnnotif")
	rnGrantReply(t, ctx, conn)
	woKey := "vtx.workorder.BBMANTRNNTFAHJKMNPQR"
	rnSeedOrder(t, ctx, conn, woKey, true, rnResolver)

	outcome, reply := rnSubmitReply(t, ctx, conn, cp, cons, "mdrn00000000000009", woKey+":resolved:"+rnResolvedAt, "completed", "notification sent")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("first reply: outcome = %v, want Accepted (reply: %+v)", outcome, reply.Error)
	}
	if reply.PrimaryKey != woKey {
		t.Fatalf("primaryKey = %q, want %q", reply.PrimaryKey, woKey)
	}
	rnRequireNotification(t, ctx, conn, woKey, "completed")
	_, seen := rnOutboxEvents(t, ctx, conn, reply.RequestID)
	var recorded bool
	for _, e := range seen {
		if e == "maintenance.workOrderResolvedNotificationRecorded" {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("maintenance.workOrderResolvedNotificationRecorded not emitted (events: %v)", seen)
	}

	outcome, reply = rnSubmitReply(t, ctx, conn, cp, cons, "mdrn00000000000010", woKey+":resolved:"+rnResolvedAt, "failed", "SMS gateway timeout")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("redelivered reply: outcome = %v, want Accepted — a bare upsert, never a once-only create (reply: %+v)", outcome, reply.Error)
	}
	rnRequireNotification(t, ctx, conn, woKey, "failed")

	dead := "vtx.workorder.BBMANTRNNTFBHJKMNPQR"
	rnSeedOrder(t, ctx, conn, dead, false, rnResolver)
	outcome, reply = rnSubmitReply(t, ctx, conn, cp, cons, "mdrn00000000000011", dead+":resolved:"+rnResolvedAt, "completed", "notification sent")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("reply on a tombstoned order: outcome = %v, want Accepted (reply: %+v)", outcome, reply.Error)
	}
	rnRequireNotification(t, ctx, conn, dead, "completed")
}

// TestRecordWorkOrderResolvedNotification_RejectsMalformedRef — an
// externalRef whose key is not a vtx.workorder.<NanoID>, whose kind is not
// resolved, or which has no third segment is refused InvalidArgument: the
// bridge echoes the token verbatim, so the shape is validated at the write.
func TestRecordWorkOrderResolvedNotification_RejectsMalformedRef(t *testing.T) {
	ctx, conn, cp, cons := rnSetup(t, "mdrnnotifbad")
	rnGrantReply(t, ctx, conn)

	for i, ref := range []string{
		"vtx.identity.BBMANTRNNTFCHJKMNPQR:resolved:" + rnResolvedAt,
		"vtx.workorder.BBMANTRNNTFCHJKMNPQR.report:resolved:" + rnResolvedAt,
		"vtx.workorder.BBMANTRNNTFCHJKMNPQR:cancelled:" + rnResolvedAt,
		"vtx.workorder.BBMANTRNNTFCHJKMNPQR:resolved",
		"vtx.workorder.BBMANTRNNTFCHJKMNPQR:" + rnResolvedAt,
	} {
		outcome, reply := rnSubmitReply(t, ctx, conn, cp, cons, "mdrnbad000000000"+string(rune('a'+i)), ref, "completed", "x")
		if outcome != processor.OutcomeRejected {
			t.Fatalf("%q: outcome = %v, want Rejected", ref, outcome)
		}
		if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument") {
			t.Fatalf("%q: want an InvalidArgument rejection, got %+v", ref, reply.Error)
		}
	}
}
