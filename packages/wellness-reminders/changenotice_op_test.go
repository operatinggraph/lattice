// RecordBookingChangeNotice integration tests for the wellness-reminders
// Capability Package — the op's write path driven end to end through a real
// Processor, on the harness reminder_op_test.go builds (setupWellRemEnv,
// wrSeedBooking, wrSeedSession, wrReadDoc).
//
// The booking root and its .status aspect are SEEDED directly rather than
// minted through wellness-domain's CreateBooking / PromoteWaitlistedBookings:
// the op reads only the root (vertex_alive), .status (booked + promotedAt +
// className), the session's .schedule (startsAt) and its own .changeNotice
// marker, so the fixtures carry exactly those.
package wellnessreminders_test

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
	cnPromotedAt = "2025-12-20T20:41:00Z"
	cnStartsAt   = "2026-07-01T15:00:00Z"
	cnClassName  = "Vinyasa Flow"
)

// wrSeedBookingStatus writes a .status aspect (class bookingStatus,
// wellness-domain's own class name) on a seeded booking, carrying the fields
// RecordBookingChangeNotice reads: value, and optionally promotedAt and
// className.
func wrSeedBookingStatus(t *testing.T, ctx context.Context, conn *substrate.Conn, bookingKey string, data map[string]any) {
	t.Helper()
	key := bookingKey + ".status"
	doc := map[string]any{"class": "bookingStatus", "vertexKey": bookingKey, "localName": "status", "isDeleted": false, "data": data}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed booking status %s: %v", key, err)
	}
}

// wrSeedChangeNotice writes a pre-existing .changeNotice marker, the state a
// second notice of the other kind must carry forward.
func wrSeedChangeNotice(t *testing.T, ctx context.Context, conn *substrate.Conn, bookingKey string, data map[string]any) {
	t.Helper()
	key := bookingKey + ".changeNotice"
	doc := map[string]any{"class": "bookingChangeNotice", "vertexKey": bookingKey, "localName": "changeNotice", "isDeleted": false, "data": data}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed change notice %s: %v", key, err)
	}
}

// cnSeedPromotedSeat seeds a live booked seat with a promotedAt + className
// and its session at cnStartsAt — the fixture every vector starts from.
func cnSeedPromotedSeat(t *testing.T, ctx context.Context, conn *substrate.Conn, bookingID, sessionID string) (string, string) {
	t.Helper()
	bookingKey := wrSeedBooking(t, ctx, conn, bookingID)
	wrSeedBookingStatus(t, ctx, conn, bookingKey, map[string]any{
		"value": "booked", "promotedAt": cnPromotedAt, "className": cnClassName, "classStartsAt": "2026-06-30T15:00:00Z"})
	sessionKey := wrSeedSession(t, ctx, conn, sessionID, cnStartsAt)
	return bookingKey, sessionKey
}

// cnSubmit drives one RecordBookingChangeNotice as `actor` with the exact
// declared-read posture the wellnessBookingChangeNotices target dispatches
// under (Reads: booking root, .status, session root, session .schedule;
// OptionalReads: the booking's .changeNotice). Class is LEFT EMPTY so the Processor's
// operationType→class reverse index is what resolves the handler (the
// target's Class pin names the same bookingChangeNoticeOp DDL; the unpinned
// path is the stricter one to prove, since it is the one an ambiguous
// operationType would break).
func cnSubmit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer,
	actor, label, bookingKey, sessionKey, kind, changeRef string, want processor.MessageOutcome) (*processor.OperationEnvelope, *processor.OperationReply) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "RecordBookingChangeNotice",
		Actor:         actor,
		SubmittedAt:   wrSubmittedAnchor,
		Payload: json.RawMessage(`{"bookingKey":"` + bookingKey + `","sessionKey":"` + sessionKey +
			`","kind":"` + kind + `","changeRef":"` + changeRef + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{bookingKey, bookingKey + ".status", sessionKey, sessionKey + ".schedule"},
			OptionalReads: []string{bookingKey + ".changeNotice"},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != want {
		t.Fatalf("%s: outcome = %v, want %v (reply: %+v)", label, outcome, want, reply.Error)
	}
	return env, reply
}

// cnOutboxNotifications reads the op's own transactional outbox and returns
// every external.notification payload it emitted plus the full event-class
// list, so a vector can assert exactly one notice went out.
func cnOutboxNotifications(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID string) ([]map[string]any, []string) {
	t.Helper()
	outboxKey := processor.OutboxAspectKey(requestID)
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, outboxKey)
	if err != nil {
		t.Fatalf("read outbox aspect %s: %v", outboxKey, err)
	}
	ob, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect %s: %v", outboxKey, err)
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

// TestRecordBookingChangeNotice_PromotedWritesMarkerAndEmits is the ADMIT
// path for a promotion: submitted as Weaver's dispatch actor with changeRef =
// the live promotedAt, the op writes .changeNotice = {promotedFor, sentAt}
// (no movedFor — nothing to carry) and emits exactly one
// external.notification keyed <bookingKey>:promoted:<promotedAt>, whose
// params carry the class time and name the member is being told about.
func TestRecordBookingChangeNotice_PromotedWritesMarkerAndEmits(t *testing.T) {
	ctx, conn := setupWellRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "wrcnprom", Instance: "wr-cnprom",
	})
	bookingKey, sessionKey := cnSeedPromotedSeat(t, ctx, conn, "WRbookCPHJKMNPQRSTVW", "WRsessCPHJKMNPQRSTVW")

	env, reply := cnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "wrcnprom0001", bookingKey, sessionKey, "promoted", cnPromotedAt, processor.OutcomeAccepted)
	if reply.PrimaryKey != bookingKey {
		t.Fatalf("primaryKey = %q, want %q", reply.PrimaryKey, bookingKey)
	}

	marker := wrReadDoc(t, ctx, conn, bookingKey+".changeNotice")
	if cls, _ := marker["class"].(string); cls != "bookingChangeNotice" {
		t.Fatalf("changeNotice class = %q, want bookingChangeNotice", cls)
	}
	md, _ := marker["data"].(map[string]any)
	if got, _ := md["promotedFor"].(string); got != cnPromotedAt {
		t.Fatalf("changeNotice promotedFor = %q, want %q", got, cnPromotedAt)
	}
	if got, _ := md["sentAt"].(string); got != wrSubmittedAnchor {
		t.Fatalf("changeNotice sentAt = %q, want %q", got, wrSubmittedAnchor)
	}
	if _, has := md["movedFor"]; has {
		t.Fatalf("a first promotion notice must carry no movedFor; got %v", md)
	}

	notifs, seen := cnOutboxNotifications(t, ctx, conn, env.RequestID)
	if len(notifs) != 1 {
		t.Fatalf("want exactly one external.notification, got %d (events: %v)", len(notifs), seen)
	}
	var sentEvent bool
	for _, e := range seen {
		if e == "wellness.bookingChangeNoticeSent" {
			sentEvent = true
		}
	}
	if !sentEvent {
		t.Fatalf("wellness.bookingChangeNoticeSent not emitted (events: %v)", seen)
	}
	n := notifs[0]
	wantRef := bookingKey + ":promoted:" + cnPromotedAt
	for _, field := range []string{"instanceKey", "externalRef", "idempotencyKey"} {
		if got, _ := n[field].(string); got != wantRef {
			t.Fatalf("external.notification %s = %q, want %q", field, got, wantRef)
		}
	}
	if got, _ := n["adapter"].(string); got != "notification" {
		t.Fatalf("adapter = %q, want notification", got)
	}
	if got, _ := n["replyOp"].(string); got != "RecordBookingChangeNotification" {
		t.Fatalf("replyOp = %q, want RecordBookingChangeNotification (wellness-domain's audit replyOp)", got)
	}
	params, _ := n["params"].(map[string]any)
	for field, want := range map[string]string{
		"bookingKey": bookingKey, "changeType": "promoted", "changeRef": cnPromotedAt,
		"sessionKey": sessionKey, "startsAt": cnStartsAt, "className": cnClassName,
	} {
		if got, _ := params[field].(string); got != want {
			t.Fatalf("params.%s = %q, want %q (params: %v)", field, got, want, params)
		}
	}
}

// TestRecordBookingChangeNotice_MovedCarriesPromotedFor — a promotion notice
// already recorded promotedFor; a move notice on the same booking must set
// movedFor AND carry promotedFor forward, or the promotion gap would reopen
// and the member would be told twice. The write lands as a bare update on
// the seeded marker (revision advances from the seed's 1), conditioned by the
// Processor on the revision the declared optionalRead observed
// (TestRecordBookingChangeNotice_MarkerUpdateIsBare pins the shape).
func TestRecordBookingChangeNotice_MovedCarriesPromotedFor(t *testing.T) {
	ctx, conn := setupWellRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "wrcnmove", Instance: "wr-cnmove",
	})
	bookingKey, sessionKey := cnSeedPromotedSeat(t, ctx, conn, "WRbookCMHJKMNPQRSTVW", "WRsessCMHJKMNPQRSTVW")
	wrSeedChangeNotice(t, ctx, conn, bookingKey, map[string]any{"promotedFor": cnPromotedAt, "sentAt": "2025-12-20T20:41:05Z"})
	seeded, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, bookingKey+".changeNotice")
	if err != nil {
		t.Fatalf("read seeded marker: %v", err)
	}

	env, reply := cnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "wrcnmove0001", bookingKey, sessionKey, "moved", cnStartsAt, processor.OutcomeAccepted)

	marker := wrReadDoc(t, ctx, conn, bookingKey+".changeNotice")
	md, _ := marker["data"].(map[string]any)
	if got, _ := md["movedFor"].(string); got != cnStartsAt {
		t.Fatalf("changeNotice movedFor = %q, want %q", got, cnStartsAt)
	}
	if got, _ := md["promotedFor"].(string); got != cnPromotedAt {
		t.Fatalf("changeNotice promotedFor = %q, want the seeded %q carried forward (a move notice must not erase the promotion notice)", got, cnPromotedAt)
	}
	if got, _ := md["sentAt"].(string); got != wrSubmittedAnchor {
		t.Fatalf("changeNotice sentAt = %q, want the move notice's own %q", got, wrSubmittedAnchor)
	}
	if rev := reply.Revisions[bookingKey+".changeNotice"]; rev <= seeded.Revision {
		t.Fatalf("changeNotice revision = %d, want > the seeded %d (an update over the existing marker, not a fresh create)", rev, seeded.Revision)
	}

	notifs, seen := cnOutboxNotifications(t, ctx, conn, env.RequestID)
	if len(notifs) != 1 {
		t.Fatalf("want exactly one external.notification, got %d (events: %v)", len(notifs), seen)
	}
	wantRef := bookingKey + ":moved:" + cnStartsAt
	if got, _ := notifs[0]["externalRef"].(string); got != wantRef {
		t.Fatalf("externalRef = %q, want %q", got, wantRef)
	}
	params, _ := notifs[0]["params"].(map[string]any)
	if got, _ := params["changeType"].(string); got != "moved" {
		t.Fatalf("params.changeType = %q, want moved", got)
	}
	if got, _ := params["startsAt"].(string); got != cnStartsAt {
		t.Fatalf("params.startsAt = %q, want %q", got, cnStartsAt)
	}
}

// TestRecordBookingChangeNotice_StaleChangeRefused — the row Weaver
// dispatched from named a promotedAt the live aspect no longer carries (here:
// a different instant). The op refuses StaleChange, writes no marker and
// sends nothing: a stale row is refused, not trusted.
func TestRecordBookingChangeNotice_StaleChangeRefused(t *testing.T) {
	ctx, conn := setupWellRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "wrcnstale", Instance: "wr-cnstale",
	})
	bookingKey, sessionKey := cnSeedPromotedSeat(t, ctx, conn, "WRbookCSHJKMNPQRSTVW", "WRsessCSHJKMNPQRSTVW")

	t.Run("promoted", func(t *testing.T) {
		_, reply := cnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "wrcnstale001", bookingKey, sessionKey, "promoted", "2025-12-20T20:40:00Z", processor.OutcomeRejected)
		if reply.Error == nil || !strings.Contains(reply.Error.Message, "StaleChange") {
			t.Fatalf("want a StaleChange rejection, got %+v", reply.Error)
		}
	})
	t.Run("moved", func(t *testing.T) {
		_, reply := cnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "wrcnstale002", bookingKey, sessionKey, "moved", "2026-07-02T15:00:00Z", processor.OutcomeRejected)
		if reply.Error == nil || !strings.Contains(reply.Error.Message, "StaleChange") {
			t.Fatalf("want a StaleChange rejection, got %+v", reply.Error)
		}
	})
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, bookingKey+".changeNotice"); err == nil {
		t.Fatalf("a refused notice must write NO .changeNotice marker")
	}
}

// TestRecordBookingChangeNotice_NonWeaverActorDenied is the DENY half of the
// primordial actor guard: wrStaffActorKey holds the operator role AND the
// identical Scope:"any" grant, so step 3 authorizes it; only the script's
// `op.actor != primordialActor["weaver"]` check stops it from having the
// platform notify an arbitrary member about an arbitrary seat. The payload is
// the one the admit test commits, differing in the actor alone.
func TestRecordBookingChangeNotice_NonWeaverActorDenied(t *testing.T) {
	ctx, conn := setupWellRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "wrcnforged", Instance: "wr-cnforged",
	})
	bookingKey, sessionKey := cnSeedPromotedSeat(t, ctx, conn, "WRbookCFHJKMNPQRSTVW", "WRsessCFHJKMNPQRSTVW")

	_, reply := cnSubmit(t, ctx, conn, cp, cons, wrStaffActorKey, "wrcnforged01", bookingKey, sessionKey, "promoted", cnPromotedAt, processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied") {
		t.Fatalf("want an AuthDenied rejection, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "Weaver's dispatch actor") {
		t.Fatalf("the denial must name the actor guard, got %q", reply.Error.Message)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, bookingKey+".changeNotice"); err == nil {
		t.Fatalf("a denied notice op must write NO .changeNotice marker")
	}
}

// TestRecordBookingChangeNotice_NotBookedRefused — a seat that is not
// `booked` (here: waitlisted, with a promotedAt the lens would never
// project a gap for) is refused InvalidState. The op re-checks the status
// itself rather than trusting the row: the lens gates on booked, but a seat
// cancelled between projection and dispatch must not be told.
func TestRecordBookingChangeNotice_NotBookedRefused(t *testing.T) {
	ctx, conn := setupWellRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "wrcnwait", Instance: "wr-cnwait",
	})
	bookingKey := wrSeedBooking(t, ctx, conn, "WRbookCWHJKMNPQRSTVW")
	wrSeedBookingStatus(t, ctx, conn, bookingKey, map[string]any{"value": "waitlisted", "promotedAt": cnPromotedAt, "className": cnClassName})
	sessionKey := wrSeedSession(t, ctx, conn, "WRsessCWHJKMNPQRSTVW", cnStartsAt)

	_, reply := cnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, "wrcnwait0001", bookingKey, sessionKey, "promoted", cnPromotedAt, processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidState") {
		t.Fatalf("want an InvalidState rejection, got %+v", reply.Error)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, bookingKey+".changeNotice"); err == nil {
		t.Fatalf("a refused notice must write NO .changeNotice marker")
	}
}

// wrTombstoneSession flips a seeded session root to isDeleted — the state
// TombstoneSession leaves behind for a called-off class, while the booking
// (and the row projected off it) still exist until the release drains them.
func wrTombstoneSession(t *testing.T, ctx context.Context, conn *substrate.Conn, sessionKey string) {
	t.Helper()
	doc := map[string]any{"class": "session", "isDeleted": true, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, sessionKey, b); err != nil {
		t.Fatalf("tombstone session %s: %v", sessionKey, err)
	}
}

// TestRecordBookingChangeNotice_TombstonedSessionRefused — a row projected
// before TombstoneSession and dispatched after it: the session root is
// logically deleted, its .schedule still carries the startsAt the row named.
// The op refuses UnknownSession before any change is re-checked, writes no
// marker and sends nothing — a called-off class is told by
// ReleaseOrphanedBooking's own notice, never a "you're in" or "moved" from
// here. Both kinds.
func TestRecordBookingChangeNotice_TombstonedSessionRefused(t *testing.T) {
	ctx, conn := setupWellRemEnv(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "wrcntomb", Instance: "wr-cntomb",
	})
	bookingKey, sessionKey := cnSeedPromotedSeat(t, ctx, conn, "WRbookCTHJKMNPQRSTVW", "WRsessCTHJKMNPQRSTVW")
	wrTombstoneSession(t, ctx, conn, sessionKey)

	for _, tc := range []struct{ kind, changeRef, label string }{
		{"promoted", cnPromotedAt, "wrcntomb0001"},
		{"moved", cnStartsAt, "wrcntomb0002"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			env, reply := cnSubmit(t, ctx, conn, cp, cons, bootstrap.WeaverIdentityKey, tc.label, bookingKey, sessionKey, tc.kind, tc.changeRef, processor.OutcomeRejected)
			if reply.Error == nil || !strings.Contains(reply.Error.Message, "UnknownSession") {
				t.Fatalf("want an UnknownSession rejection, got %+v", reply.Error)
			}
			if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(env.RequestID)); err == nil {
				t.Fatalf("a refused notice must leave NO outbox aspect (no external.notification)")
			}
		})
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, bookingKey+".changeNotice"); err == nil {
		t.Fatalf("a refused notice must write NO .changeNotice marker")
	}
}
