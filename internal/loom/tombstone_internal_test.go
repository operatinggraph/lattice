package loom

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// markerOn returns the headers of whatever message currently stands on a
// loom-state key's subject. It reads the stream directly because the point of
// these tests is the SHAPE of the message a removal leaves, which every KV
// read path deliberately hides (a marker of either operation reads as absent).
func markerOn(ctx context.Context, t *testing.T, s *stateStore, key string) (nats.Header, uint64) {
	t.Helper()
	stream, err := s.conn.JetStream().Stream(ctx, "KV_"+s.bucket)
	require.NoError(t, err)
	msg, err := stream.GetLastMsgForSubject(ctx, "$KV."+s.bucket+"."+key)
	require.NoError(t, err, "expected a message on %s", key)
	return msg.Header, msg.Sequence
}

// requireExpiringPurgeMarker asserts that key carries the removal shape this
// package commits to: a purge marker (not a permanent DEL) carrying a TTL, so
// the subject leaves the stream rather than being occupied forever on a
// history-1 bucket.
func requireExpiringPurgeMarker(ctx context.Context, t *testing.T, s *stateStore, key string) {
	t.Helper()
	hdr, _ := markerOn(ctx, t, s, key)
	require.Equal(t, "PURGE", hdr.Get("KV-Operation"),
		"%s must carry a purge marker; a DEL marker is a permanent subject", key)
	require.Equal(t, "sub", hdr.Get("Nats-Rollup"), "%s: a purge marker rolls up its subject", key)
	require.Equal(t, tombstoneTTL.String(), hdr.Get("Nats-TTL"),
		"%s: the marker must carry tombstoneTTL — a TTL-less purge marker never expires", key)

	_, err := s.conn.KVGet(ctx, s.bucket, key)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound, "%s must read absent", key)

	tombstones, err := s.conn.KVListTombstones(ctx, s.bucket, key)
	require.NoError(t, err)
	require.Empty(t, tombstones, "%s must not be listed as a DELETE tombstone", key)
}

// TestTransition_TerminalBatchLeavesExpiringMarkers pins the removal shape of
// the terminal transition batch: the pattern pin, the superseded step token
// and the deadline mark are all removed with an expiring purge marker rather
// than the permanent DEL that a history-1 bucket keeps forever. The instance
// cursor is untouched by any of it.
func TestTransition_TerminalBatchLeavesExpiringMarkers(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStoreForTransition(ctx, t)
	pat := Pattern{PatternID: "p1", SubjectType: "widget", MetaKey: "vtx.meta.p1", Steps: []Step{
		{Kind: StepKindSystemOp, Operation: "StepA"},
	}}
	inst := &Instance{
		InstanceID: "inst-terminal", PatternRef: "vtx.meta.p1", SubjectKey: "vtx.widget.w1",
		Cursor: 0, Status: StatusRunning,
	}
	require.NoError(t, s.createInstance(ctx, inst, &pat))

	// Step 0: a live token plus an armed deadline, so the terminal batch has
	// all three removals to make.
	inst.PendingToken = "tok-0"
	require.NoError(t, s.transition(ctx, inst, "tok-0", "", tokenCreateOnly, nil, time.Minute))
	_, err := s.conn.KVGet(ctx, s.bucket, deadlineKey(inst.InstanceID))
	require.NoError(t, err, "precondition: the deadline must be armed")

	// Terminal: status flips, the pin and deadline go, and the old token is
	// superseded by a new one.
	inst.Status = StatusComplete
	inst.PendingToken = "tok-1"
	require.NoError(t, s.transition(ctx, inst, "tok-1", "tok-0", tokenCreateOnly, nil, 0))

	requireExpiringPurgeMarker(ctx, t, s, patternPinKey(inst.InstanceID))
	requireExpiringPurgeMarker(ctx, t, s, deadlineKey(inst.InstanceID))
	requireExpiringPurgeMarker(ctx, t, s, tokenKey("tok-0"))

	// The cursor is the one permanent subject and is never a removal's target.
	got, err := s.getInstance(ctx, inst.InstanceID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, StatusComplete, got.Status)
	tombstones, err := s.conn.KVListTombstones(ctx, s.bucket, "instance.*")
	require.NoError(t, err)
	require.Empty(t, tombstones, "the cursor family must carry no tombstone")
}

// TestDisarmDeadline_LeavesExpiringMarker pins the single-publish removal path
// (disarmDeadline, deleteToken) in the same shape, and that the re-entry no-op
// the deadline watcher depends on still holds when the standing marker is a
// purge rather than a DEL.
func TestDisarmDeadline_LeavesExpiringMarker(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStore(ctx, t)
	const instanceID = "inst-disarm"

	require.NoError(t, s.rearmDeadline(ctx, instanceID, time.Minute))
	require.NoError(t, s.disarmDeadline(ctx, instanceID))
	requireExpiringPurgeMarker(ctx, t, s, deadlineKey(instanceID))

	// The watcher's re-fire: a second disarm against the standing purge marker
	// must write nothing new, or the disarm loop never breaks.
	_, before := markerOn(ctx, t, s, deadlineKey(instanceID))
	require.NoError(t, s.disarmDeadline(ctx, instanceID))
	_, after := markerOn(ctx, t, s, deadlineKey(instanceID))
	require.Equal(t, before, after, "a re-fired disarm must not publish a fresh marker")
	requireExpiringPurgeMarker(ctx, t, s, deadlineKey(instanceID))
}

// TestDeleteToken_LeavesExpiringMarker pins the reverse-pointer removal, and
// that the redelivery path's second clear — which runs against the marker the
// first one left — is still a tolerated no-op.
func TestDeleteToken_LeavesExpiringMarker(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStore(ctx, t)
	const token = "tok-stale"

	ptrBody, err := json.Marshal(tokenPointer{InstanceID: "inst-1"})
	require.NoError(t, err)
	_, err = s.conn.KVPut(ctx, s.bucket, tokenKey(token), ptrBody)
	require.NoError(t, err)

	require.NoError(t, s.deleteToken(ctx, token))
	requireExpiringPurgeMarker(ctx, t, s, tokenKey(token))

	require.NoError(t, s.deleteToken(ctx, token), "the redelivered clear must stay a no-op")
	requireExpiringPurgeMarker(ctx, t, s, tokenKey(token))
}

// TestRelay_RemovesOutboxRecordWithExpiringMarker pins the third single-publish
// site: the relay's removal on publish-ack. The outbox family is the largest of
// the four by subject count, so a permanent marker here is the biggest single
// contributor to the bucket's residue.
func TestRelay_RemovesOutboxRecordWithExpiringMarker(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStore(ctx, t)
	_, err := s.conn.JetStream().CreateStream(ctx, jetstream.StreamConfig{
		Name:     "core-operations",
		Subjects: []string{"ops.>"},
	})
	require.NoError(t, err)

	rec := outboxRecord{RequestID: "req-1", Operation: "DoThing", Lane: "system", Actor: "vtx.identity.x"}
	body, err := json.Marshal(rec)
	require.NoError(t, err)
	_, err = s.conn.KVPut(ctx, s.bucket, outboxKey(rec.RequestID), body)
	require.NoError(t, err)

	r := newRelay(s.conn, s.bucket, testRelayLogger())
	decision, err := r.handle(ctx, substrate.Message{
		Subject: "$KV." + s.bucket + "." + outboxKey(rec.RequestID),
		Body:    body,
	})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, decision)

	requireExpiringPurgeMarker(ctx, t, s, outboxKey(rec.RequestID))

	// The marker the relay leaves is itself an empty body, which the relay's
	// own predicate acks without re-publishing.
	decision, err = r.handle(ctx, substrate.Message{
		Subject: "$KV." + s.bucket + "." + outboxKey(rec.RequestID),
		Body:    nil,
	})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, decision)
}
