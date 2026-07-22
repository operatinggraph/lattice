package weaver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// provisionSchedules creates the core-schedules stream on the harness server,
// mirroring internal/bootstrap/primordial.go's config (AllowMsgSchedules,
// MaxMsgsPerSubject 1, file storage, limits retention).
func provisionSchedules(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	if _, err := conn.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:              "core-schedules",
		Subjects:          []string{"schedule.>"},
		Storage:           jetstream.FileStorage,
		Retention:         jetstream.LimitsPolicy,
		MaxMsgsPerSubject: 1,
		AllowMsgSchedules: true,
	}); err != nil {
		t.Fatalf("create core-schedules: %v", err)
	}
}

// scheduleMsg reads back the pending schedule message for one timer subject,
// or nil when none exists.
func scheduleMsg(t *testing.T, ctx context.Context, conn *substrate.Conn, subject string) *jetstream.RawStreamMsg {
	t.Helper()
	stream, err := conn.JetStream().Stream(ctx, "core-schedules")
	if err != nil {
		t.Fatalf("core-schedules stream handle: %v", err)
	}
	msg, err := stream.GetLastMsgForSubject(ctx, subject)
	if err != nil {
		return nil
	}
	return msg
}

func firedMessage(targetID, entityID string, payload map[string]any) substrate.Message {
	body, _ := json.Marshal(payload)
	return substrate.Message{
		Subject:      firedSubjectPrefix + targetID + "." + entityID,
		Body:         body,
		Sequence:     1,
		NumDelivered: 1,
	}
}

// TestDeriveTimerRequestID_Deterministic pins the §10.4 derivation: the same
// schedule subject + fire instant always reproduces the identical requestId (a
// redelivered firing collapses on the Contract #4 tracker), while a new fire
// instant (a re-armed timer) or another timer subject derives a new one.
func TestDeriveTimerRequestID_Deterministic(t *testing.T) {
	t.Parallel()
	subj := scheduleSubjectPrefix + "targetA.Lk2Pn6mQrtwzKbcXvP3T"
	fireAt := "2026-06-12T10:00:00Z"

	a := deriveTimerRequestID(subj, fireAt)
	b := deriveTimerRequestID(subj, fireAt)
	if a != b {
		t.Fatalf("same firing must derive the same requestId: %q vs %q", a, b)
	}
	if !substrate.IsValidNanoID(a) {
		t.Fatalf("derived requestId %q is not a canonical 20-char NanoID", a)
	}
	if rearmed := deriveTimerRequestID(subj, "2026-06-12T11:00:00Z"); rearmed == a {
		t.Fatalf("a re-armed timer (new fireAt) must derive a NEW requestId")
	}
	if other := deriveTimerRequestID(scheduleSubjectPrefix+"targetB.Lk2Pn6mQrtwzKbcXvP3T", fireAt); other == a {
		t.Fatalf("another timer subject must derive a different requestId")
	}
}

// TestHandleFiredTimer_RedeliveryDedup proves the §10.4 dedup seam: the same
// fired message delivered twice (NumDelivered 1 then 2) submits the SAME
// deterministic requestId both times — the Contract #4 tracker collapses the
// duplicate — and the op carries the {entityKey, targetId, expiredAt} payload
// with no authContext and no weaver-state mark.
func TestHandleFiredTimer_RedeliveryDedup(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureFresh"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_fresh": {Action: actionDirectOp, Operation: "FixFresh"}},
	})
	entityID := testNanoID(t)
	entityKey := "vtx.leaseApp." + entityID
	fireAt := "2026-06-12T10:00:00Z"
	// The read-before-act guard reads the current row: present, not renewed
	// (the firing's instant matches the row's deadline), so the op submits.
	putTargetRow(t, ctx, h, targetID, entityID, map[string]any{
		"entityKey": entityKey, "freshUntil": fireAt,
	})
	msg := firedMessage(targetID, entityID, map[string]any{
		"entityKey": entityKey, "targetId": targetID, "fireAt": fireAt,
	})
	wantRequestID := deriveTimerRequestID(scheduleSubjectPrefix+targetID+"."+entityID, fireAt)

	if dec := h.engine.handleFiredTimer(ctx, msg); dec != substrate.Ack {
		t.Fatalf("fired-timer conversion must Ack, got %v", dec)
	}
	first := h.nextOp(t)
	if first["operationType"] != opMarkExpired {
		t.Fatalf("operationType = %v, want %s", first["operationType"], opMarkExpired)
	}
	if first["requestId"] != wantRequestID {
		t.Fatalf("requestId = %v, want the §10.4-derived %v", first["requestId"], wantRequestID)
	}
	if _, has := first["authContext"]; has {
		t.Fatalf("MarkExpired must carry no authContext, got %v", first["authContext"])
	}
	payload, _ := first["payload"].(map[string]any)
	if payload["entityKey"] != entityKey || payload["targetId"] != targetID || payload["expiredAt"] != fireAt {
		t.Fatalf("payload = %v, want {entityKey:%s targetId:%s expiredAt:%s}", payload, entityKey, targetID, fireAt)
	}

	// Redelivery of the same firing: the same requestId again.
	redelivered := msg
	redelivered.NumDelivered = 2
	if dec := h.engine.handleFiredTimer(ctx, redelivered); dec != substrate.Ack {
		t.Fatalf("redelivered firing must Ack, got %v", dec)
	}
	second := h.nextOp(t)
	if second["requestId"] != wantRequestID {
		t.Fatalf("redelivered requestId = %v, want the same %v", second["requestId"], wantRequestID)
	}

	// A re-armed timer (new fireAt) is a genuinely new op.
	rearmed := firedMessage(targetID, entityID, map[string]any{
		"entityKey": entityKey, "targetId": targetID, "fireAt": "2026-06-12T11:00:00Z",
	})
	if dec := h.engine.handleFiredTimer(ctx, rearmed); dec != substrate.Ack {
		t.Fatalf("re-armed firing must Ack, got %v", dec)
	}
	third := h.nextOp(t)
	if third["requestId"] == wantRequestID {
		t.Fatalf("a re-armed timer must derive a NEW requestId")
	}

	// No weaver-state mark for any temporal conversion.
	keys, err := h.conn.KVListKeys(ctx, "weaver-state")
	if err != nil {
		t.Fatalf("list weaver-state: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("the temporal conversion must take no weaver-state mark, found %v", keys)
	}
}

// TestHandleFiredTimer_Edges walks the malformed-firing branches: every one
// Acks (redelivery cannot fix a stored payload), submits no op, and raises a
// keyed timer: Health issue — never silent (FR29).
func TestHandleFiredTimer_Edges(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)
	entityID := testNanoID(t)

	cases := []struct {
		name string
		msg  substrate.Message
	}{
		{"subject tail without an entity segment", substrate.Message{
			Subject: firedSubjectPrefix + "loneToken", Body: []byte(`{}`), Sequence: 1, NumDelivered: 1}},
		{"subject tail with a non-NanoID entity", substrate.Message{
			Subject: firedSubjectPrefix + "fixtureFresh.notanano", Body: []byte(`{}`), Sequence: 1, NumDelivered: 1}},
		{"unparseable payload", substrate.Message{
			Subject: firedSubjectPrefix + "fixtureFresh." + entityID, Body: []byte(`{not json`), Sequence: 1, NumDelivered: 1}},
		{"missing entityKey", firedMessage("fixtureFresh", entityID, map[string]any{
			"targetId": "fixtureFresh", "fireAt": "2026-06-12T10:00:00Z"})},
		{"missing fireAt", firedMessage("fixtureFresh", entityID, map[string]any{
			"entityKey": "vtx.leaseApp." + entityID, "targetId": "fixtureFresh"})},
		{"non-RFC3339 fireAt", firedMessage("fixtureFresh", entityID, map[string]any{
			"entityKey": "vtx.leaseApp." + entityID, "targetId": "fixtureFresh", "fireAt": "tomorrow"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if dec := h.engine.handleFiredTimer(ctx, tc.msg); dec != substrate.Ack {
				t.Fatalf("a malformed firing must Ack, got %v", dec)
			}
			h.requireNoOp(t)
			if !hasIssueCode(h.engine.issues.snapshot(), "TimerDataError") {
				t.Fatalf("a malformed firing must surface a TimerDataError Health issue")
			}
		})
	}
}

// TestHandleRow_SchedulingLeg walks the lane-3 scheduling-leg branches off a
// CDC row delivery: a future freshUntil publishes the per-target-per-entity
// @at schedule (violating or not, every delivery — replace-idempotent); a past
// instant publishes verbatim (an overdue @at fires immediately = correct
// immediate-expiry); a non-string or unparseable value and a missing entityKey
// surface a RowDataError and skip; a tombstone row never schedules.
func TestHandleRow_SchedulingLeg(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)
	provisionSchedules(t, ctx, h.conn)

	const targetID = "fixtureFresh"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_fresh": {Action: actionDirectOp, Operation: "FixFresh"}},
	})

	// Future freshUntil on a NON-violating row: schedules, no op.
	entityID := testNanoID(t)
	fireAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	freshUntil := fireAt.Format(time.RFC3339)
	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, map[string]any{
		"entityKey":  "vtx.leaseApp." + entityID,
		"violating":  false,
		"freshUntil": freshUntil,
	}, 5, 1))
	if dec != substrate.Ack {
		t.Fatalf("scheduling delivery must Ack, got %v", dec)
	}
	h.requireNoOp(t)
	schedSubject := scheduleSubjectPrefix + targetID + "." + entityID
	msg := scheduleMsg(t, ctx, h.conn, schedSubject)
	if msg == nil {
		t.Fatalf("a future freshUntil must publish a schedule message at %s", schedSubject)
	}
	if got := msg.Header.Get(substrate.ScheduleHeader); got != "@at "+freshUntil {
		t.Fatalf("schedule header = %q, want %q", got, "@at "+freshUntil)
	}
	wantTarget := firedSubjectPrefix + targetID + "." + entityID
	if got := msg.Header.Get(substrate.ScheduleTargetHeader); got != wantTarget {
		t.Fatalf("schedule target header = %q, want %q", got, wantTarget)
	}
	var p timerPayload
	if err := json.Unmarshal(msg.Data, &p); err != nil {
		t.Fatalf("schedule payload unparseable: %v", err)
	}
	if p.EntityKey != "vtx.leaseApp."+entityID || p.TargetID != targetID || p.FireAt != freshUntil {
		t.Fatalf("schedule payload = %+v, want {entityKey, targetId, fireAt} echoed", p)
	}

	// Past freshUntil: publishes verbatim (the server stores the overdue @at and
	// fires it immediately), with the deadline instant in the header/payload — no
	// data error, the payload carries the deadline (not "now") so the requestId
	// is deterministic.
	pastEntity := testNanoID(t)
	pastAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	dec = h.engine.handleRow(ctx, h.rowMessage(t, targetID, pastEntity, map[string]any{
		"entityKey":  "vtx.leaseApp." + pastEntity,
		"violating":  false,
		"freshUntil": pastAt,
	}, 6, 1))
	if dec != substrate.Ack {
		t.Fatalf("past-deadline delivery must Ack, got %v", dec)
	}
	pastSched := scheduleMsg(t, ctx, h.conn, scheduleSubjectPrefix+targetID+"."+pastEntity)
	if pastSched == nil {
		t.Fatalf("a past freshUntil must still publish (fires immediately)")
	}
	if got := pastSched.Header.Get(substrate.ScheduleHeader); got != "@at "+pastAt {
		t.Fatalf("past-deadline schedule header = %q, want %q (the deadline instant verbatim)", got, "@at "+pastAt)
	}
	if hasIssueCode(h.engine.issues.snapshot(), "RowDataError") {
		t.Fatalf("a past freshUntil is not a data error")
	}

	// Non-string freshUntil: RowDataError issue, no schedule.
	badEntity := testNanoID(t)
	dec = h.engine.handleRow(ctx, h.rowMessage(t, targetID, badEntity, map[string]any{
		"entityKey":  "vtx.leaseApp." + badEntity,
		"violating":  false,
		"freshUntil": 12345,
	}, 7, 1))
	if dec != substrate.Ack {
		t.Fatalf("bad-deadline delivery must Ack, got %v", dec)
	}
	if scheduleMsg(t, ctx, h.conn, scheduleSubjectPrefix+targetID+"."+badEntity) != nil {
		t.Fatalf("a non-string freshUntil must not schedule")
	}
	if !hasIssueCode(h.engine.issues.snapshot(), "RowDataError") {
		t.Fatalf("a non-string freshUntil must surface a RowDataError issue")
	}

	// Missing entityKey on a freshUntil row: same data-error surface, no schedule.
	h.engine.issues.clear(issueKeyData(targetID, freshUntilColumn))
	noKeyEntity := testNanoID(t)
	dec = h.engine.handleRow(ctx, h.rowMessage(t, targetID, noKeyEntity, map[string]any{
		"violating":  false,
		"freshUntil": fireAt.Format(time.RFC3339),
	}, 8, 1))
	if dec != substrate.Ack {
		t.Fatalf("missing-entityKey delivery must Ack, got %v", dec)
	}
	if scheduleMsg(t, ctx, h.conn, scheduleSubjectPrefix+targetID+"."+noKeyEntity) != nil {
		t.Fatalf("a freshUntil row without entityKey must not schedule")
	}
	if !hasIssueCode(h.engine.issues.snapshot(), "RowDataError") {
		t.Fatalf("a freshUntil row without entityKey must surface a RowDataError issue")
	}

	// Tombstone row (empty body): clearing runs, no schedule publish, no purge
	// of the pending timer.
	dec = h.engine.handleRow(ctx, substrate.Message{
		Subject:      h.engine.rowSubjectPrefix + targetID + "." + entityID,
		Body:         nil,
		Sequence:     9,
		NumDelivered: 1,
	})
	if dec != substrate.Ack {
		t.Fatalf("tombstone delivery must Ack, got %v", dec)
	}
	if scheduleMsg(t, ctx, h.conn, schedSubject) == nil {
		t.Fatalf("a tombstone must not purge the pending timer (no cancel in Phase 2)")
	}
}

// TestScheduleFreshness_ReservedFiredToken pins the firedToken refusal: a
// targetId equal to the reserved "fired" segment would make its pending
// schedule subject land inside the temporal consumer's fired filter, so the
// scheduling leg refuses it loudly (ScheduleConfigError) and never publishes.
func TestScheduleFreshness_ReservedFiredToken(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)
	provisionSchedules(t, ctx, h.conn)

	entityID := testNanoID(t)
	ok := h.engine.scheduleFreshness(ctx, firedToken, entityID, firedToken+"."+entityID, map[string]any{
		"entityKey":  "vtx.leaseApp." + entityID,
		"freshUntil": time.Now().Add(time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339),
	})
	if !ok {
		t.Fatalf("a reserved-token refusal must not defer the row (Ack, not Nak)")
	}
	if scheduleMsg(t, ctx, h.conn, scheduleSubjectPrefix+firedToken+"."+entityID) != nil {
		t.Fatalf("a reserved %q targetId must not publish a schedule", firedToken)
	}
	if !hasIssueCode(h.engine.issues.snapshot(), "ScheduleConfigError") {
		t.Fatalf("a reserved %q targetId must surface a ScheduleConfigError issue", firedToken)
	}
}

// TestScheduleFreshness_PublishFailure pins the bounded-retry posture: when the
// schedule publish fails (here: no core-schedules stream provisioned), the
// scheduling leg returns false (handleRow → NakWithDelay) and raises a
// SchedulePublishError Health issue — never a hot loop, never silent.
func TestScheduleFreshness_PublishFailure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx) // core-schedules deliberately NOT provisioned

	const targetID = "fixtureFresh"
	entityID := testNanoID(t)
	ok := h.engine.scheduleFreshness(ctx, targetID, entityID, targetID+"."+entityID, map[string]any{
		"entityKey":  "vtx.leaseApp." + entityID,
		"freshUntil": time.Now().Add(time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339),
	})
	if ok {
		t.Fatalf("a schedule-publish failure must return false (NakWithDelay)")
	}
	if !hasIssueCode(h.engine.issues.snapshot(), "SchedulePublishError") {
		t.Fatalf("a schedule-publish failure must surface a SchedulePublishError issue")
	}
}

// TestDisabled_FreshnessExpiryRecordedNoRemediation proves the narrowed
// disabled semantic (ECH-5 correction): a disabled target's already-armed
// freshness timer STILL records the freshness expiry (handleFiredTimer submits
// MarkExpired — state-recording bookkeeping), and a violating row STILL clears
// marks / arms timers, but runs NO remediation while disabled. On enable,
// remediation dispatches for whatever is still violating — without re-touching
// the row.
func TestDisabled_FreshnessExpiryRecordedNoRemediation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)
	provisionSchedules(t, ctx, h.conn)

	const targetID = "fixtureFresh"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_fresh": {Action: actionDirectOp, Operation: "FixFresh"}},
	})

	// Disable the target (in-memory set — the dispatch-skip authority on the
	// hot path).
	h.engine.disabled.set(targetID, true)

	entityID := testNanoID(t)
	entityKey := "vtx.leaseApp." + entityID
	firedAt := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	// The read-before-act guard reads the current row: present, not renewed.
	putTargetRow(t, ctx, h, targetID, entityID, map[string]any{
		"entityKey": entityKey, "freshUntil": firedAt,
	})

	// A fired freshness timer for the DISABLED target still records MarkExpired
	// (bookkeeping — not remediation).
	msg := firedMessage(targetID, entityID, map[string]any{
		"entityKey": entityKey, "targetId": targetID, "fireAt": firedAt,
	})
	if dec := h.engine.handleFiredTimer(ctx, msg); dec != substrate.Ack {
		t.Fatalf("fired timer for disabled target must Ack, got %v", dec)
	}
	expired := h.nextOp(t)
	if expired["operationType"] != opMarkExpired {
		t.Fatalf("disabled target's fired timer must still submit MarkExpired, got %v", expired["operationType"])
	}

	// A violating row while disabled: clears marks + arms a freshness timer
	// (bookkeeping), but creates NO mark and runs NO remediation.
	violRow := map[string]any{
		"entityKey":     entityKey,
		"violating":     true,
		"missing_fresh": true,
		"freshUntil":    time.Now().Add(time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339),
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, violRow, 1, 1)); dec != substrate.Ack {
		t.Fatalf("disabled violating row must Ack, got %v", dec)
	}
	h.requireNoOp(t)
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_fresh"); err != nil {
		t.Fatalf("get mark while disabled: %v", err)
	} else if found {
		t.Fatalf("no in-flight mark must be created while disabled")
	}
	// The freshness timer armed even while disabled (bookkeeping leg ran).
	if scheduleMsg(t, ctx, h.conn, scheduleSubjectPrefix+targetID+"."+entityID) == nil {
		t.Fatalf("a disabled target must still arm its freshness timer (bookkeeping)")
	}

	// Enable → remediation resumes; the SAME row redelivered dispatches (no
	// row re-touch needed beyond redelivery — state was kept current).
	h.engine.disabled.set(targetID, false)
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, violRow, 2, 1)); dec != substrate.Ack {
		t.Fatalf("post-enable dispatch must Ack, got %v", dec)
	}
	op := h.nextOp(t)
	if op["operationType"] != "FixFresh" {
		t.Fatalf("post-enable remediation op = %v, want FixFresh", op["operationType"])
	}
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_fresh"); err != nil || !found {
		t.Fatalf("mark must exist after post-enable remediation (err=%v, found=%v)", err, found)
	}
}

// putTargetRow writes a weaver-targets row the read-before-act guard reads
// (the engine's WeaverTargetsBucket, key <targetId>.<entityId>).
func putTargetRow(t *testing.T, ctx context.Context, h *handlerHarness, targetID, entityID string, row map[string]any) {
	t.Helper()
	body, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal target row: %v", err)
	}
	if _, err := h.conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: h.engine.cfg.WeaverTargetsBucket}); err != nil {
		t.Fatalf("create weaver-targets: %v", err)
	}
	if _, err := h.conn.KVPut(ctx, h.engine.cfg.WeaverTargetsBucket, targetID+"."+entityID, body); err != nil {
		t.Fatalf("put target row: %v", err)
	}
}

// TestHandleFiredTimer_ReadBeforeAct walks the read-before-act guard: an absent
// (deleted) entity row or a row re-armed with a strictly later freshUntil Ack
// WITHOUT submitting a MarkExpired; a present row whose target is unregistered
// (registry replay lag) NakWithDelays rather than dropping; a payload targetId
// disagreeing with the subject is dropped; a present, not-renewed registered
// row submits exactly one op.
func TestHandleFiredTimer_ReadBeforeAct(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureFresh"
	firedAt := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)

	// Present row but unregistered target (registry replay lag at startup):
	// NakWithDelay, do not drop a possibly-valid missed firing.
	unregEntity := testNanoID(t)
	putTargetRow(t, ctx, h, targetID, unregEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + unregEntity, "freshUntil": firedAt,
	})
	if dec := h.engine.handleFiredTimer(ctx, firedMessage(targetID, unregEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + unregEntity, "targetId": targetID, "fireAt": firedAt,
	})); dec != substrate.NakWithDelay {
		t.Fatalf("a firing for an unregistered (replay-lag) target must NakWithDelay, got %v", dec)
	}
	h.requireNoOp(t)

	// Register the target for the remaining cases.
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_fresh": {Action: actionDirectOp, Operation: "FixFresh"}},
	})

	// Deleted entity (no row): drop.
	goneEntity := testNanoID(t)
	if dec := h.engine.handleFiredTimer(ctx, firedMessage(targetID, goneEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + goneEntity, "targetId": targetID, "fireAt": firedAt,
	})); dec != substrate.Ack {
		t.Fatalf("a firing for an absent entity row must Ack, got %v", dec)
	}
	h.requireNoOp(t)

	// Renewed-while-down: the current row carries a strictly LATER freshUntil →
	// the firing is stale, suppress.
	renewedEntity := testNanoID(t)
	laterAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	putTargetRow(t, ctx, h, targetID, renewedEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + renewedEntity, "freshUntil": laterAt,
	})
	if dec := h.engine.handleFiredTimer(ctx, firedMessage(targetID, renewedEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + renewedEntity, "targetId": targetID, "fireAt": firedAt,
	})); dec != substrate.Ack {
		t.Fatalf("a firing superseded by a later freshUntil must Ack, got %v", dec)
	}
	h.requireNoOp(t)

	// Payload targetId disagrees with the subject-derived targetId: drop loudly.
	mismatchEntity := testNanoID(t)
	putTargetRow(t, ctx, h, targetID, mismatchEntity, map[string]any{"entityKey": "vtx.leaseApp." + mismatchEntity})
	if dec := h.engine.handleFiredTimer(ctx, firedMessage(targetID, mismatchEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + mismatchEntity, "targetId": "anotherTarget", "fireAt": firedAt,
	})); dec != substrate.Ack {
		t.Fatalf("a payload-targetId mismatch must Ack, got %v", dec)
	}
	h.requireNoOp(t)
	if !hasIssueCode(h.engine.issues.snapshot(), "TimerDataError") {
		t.Fatalf("a payload-targetId mismatch must surface a TimerDataError issue")
	}

	// Present, not-renewed row (same or earlier deadline): submit exactly one op.
	okEntity := testNanoID(t)
	putTargetRow(t, ctx, h, targetID, okEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + okEntity, "freshUntil": firedAt,
	})
	if dec := h.engine.handleFiredTimer(ctx, firedMessage(targetID, okEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + okEntity, "targetId": targetID, "fireAt": firedAt,
	})); dec != substrate.Ack {
		t.Fatalf("a present, not-renewed firing must Ack, got %v", dec)
	}
	op := h.nextOp(t)
	if op["operationType"] != opMarkExpired {
		t.Fatalf("operationType = %v, want %s", op["operationType"], opMarkExpired)
	}
	wantRequestID := deriveTimerRequestID(scheduleSubjectPrefix+targetID+"."+okEntity, firedAt)
	if op["requestId"] != wantRequestID {
		t.Fatalf("requestId = %v, want the §10.4-derived %v", op["requestId"], wantRequestID)
	}
}

// TestHandleFiredTimer_PastDeadlineRequestIDCollapse pins that a fired message
// carrying a PAST deadline derives the same deterministic requestId as the
// schedule that armed it — the payload's fireAt is the deadline instant, not
// "now", so a re-projected/republished past deadline collapses on the
// Contract #4 tracker rather than minting a new op per firing.
func TestHandleFiredTimer_PastDeadlineRequestIDCollapse(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureFresh"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_fresh": {Action: actionDirectOp, Operation: "FixFresh"}},
	})
	entityID := testNanoID(t)
	pastAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	putTargetRow(t, ctx, h, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "freshUntil": pastAt,
	})

	// The requestId an arming schedule would seed (subject + deadline instant).
	wantRequestID := deriveTimerRequestID(scheduleSubjectPrefix+targetID+"."+entityID, pastAt)

	msg := firedMessage(targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "targetId": targetID, "fireAt": pastAt,
	})
	if dec := h.engine.handleFiredTimer(ctx, msg); dec != substrate.Ack {
		t.Fatalf("a past-deadline firing must Ack, got %v", dec)
	}
	op := h.nextOp(t)
	if op["requestId"] != wantRequestID {
		t.Fatalf("a past-deadline firing must derive the deadline-seeded requestId %v, got %v", wantRequestID, op["requestId"])
	}

	// A redelivery (or a republished past deadline) reuses the same requestId.
	redelivered := msg
	redelivered.NumDelivered = 2
	if dec := h.engine.handleFiredTimer(ctx, redelivered); dec != substrate.Ack {
		t.Fatalf("a redelivered past-deadline firing must Ack, got %v", dec)
	}
	op2 := h.nextOp(t)
	if op2["requestId"] != wantRequestID {
		t.Fatalf("a redelivered past-deadline firing must reuse the same requestId %v, got %v", wantRequestID, op2["requestId"])
	}
}
