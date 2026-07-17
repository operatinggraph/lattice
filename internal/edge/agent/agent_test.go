package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/edge/overlay"
	"github.com/asolgan/lattice/internal/edge/store"
	"github.com/asolgan/lattice/internal/processor"
)

// fakeSubmitter is a Submitter test double: decide answers every Submit
// call, in FIFO order of arrival, recording the envelopes it saw. A nil
// decide simulates a transport-level failure — the offline case Drain must
// leave the intent queued for.
type fakeSubmitter struct {
	decide func(*processor.OperationEnvelope) (*processor.OperationReply, error)
	seen   []string
}

func (f *fakeSubmitter) Submit(_ context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
	f.seen = append(f.seen, env.RequestID)
	if f.decide == nil {
		return nil, fmt.Errorf("fakeSubmitter: no responder configured (simulated offline)")
	}
	return f.decide(env)
}

func openTestStack(t *testing.T) (store.Store, *overlay.Overlay) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "edge.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st, overlay.New(st)
}

type fakeRehydrator struct{ calls int }

func (f *fakeRehydrator) Rehydrate(context.Context) error {
	f.calls++
	return nil
}

const testKey = "vtx.lease.Lk2Pn6mQrtwzKbcXvP3T"

func testEnv(requestID string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: "UpdateLease",
		Actor:         "vtx.identity.Ak2Pn6mQrtwzKbcXvP3T",
		SubmittedAt:   "2026-07-10T00:00:00Z",
		Payload:       json.RawMessage(`{}`),
	}
}

func TestDrain_AcceptedDequeuesWithoutTouchingOverlay(t *testing.T) {
	ctx := context.Background()
	st, ov := openTestStack(t)

	sub := &fakeSubmitter{decide: func(env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted, Decision: "committed"}, nil
	}}

	require.NoError(t, ov.Apply(testKey, "req1", []byte(`{"rent":150}`), false))
	a := New(sub, st, ov, nil, Config{})
	require.NoError(t, a.Enqueue(testEnv("req1"), []string{testKey}))

	require.NoError(t, a.Drain(ctx))

	intents, err := st.ListIntents()
	require.NoError(t, err)
	require.Empty(t, intents, "an accepted intent must be dequeued")

	v, ok, err := ov.Read(testKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, v.Pending, "accept alone must not clear the overlay — only a fresher confirmed value does (R3)")
}

func TestDrain_DuplicateDequeuesWithoutTouchingOverlay(t *testing.T) {
	ctx := context.Background()
	st, ov := openTestStack(t)

	sub := &fakeSubmitter{decide: func(env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusDuplicate}, nil
	}}

	require.NoError(t, ov.Apply(testKey, "req1", []byte(`{"rent":150}`), false))
	a := New(sub, st, ov, nil, Config{})
	require.NoError(t, a.Enqueue(testEnv("req1"), []string{testKey}))

	require.NoError(t, a.Drain(ctx))

	intents, err := st.ListIntents()
	require.NoError(t, err)
	require.Empty(t, intents)
}

func TestDrain_RevisionConflictRehydratesAndDiscardsOverlay(t *testing.T) {
	ctx := context.Background()
	st, ov := openTestStack(t)

	sub := &fakeSubmitter{decide: func(env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		return &processor.OperationReply{
			RequestID: env.RequestID,
			Status:    processor.ReplyStatusRejected,
			Error:     &processor.ReplyError{Code: processor.ErrCodeRevisionConflict, Message: "stale"},
		}, nil
	}}

	require.NoError(t, ov.Apply(testKey, "req1", []byte(`{"rent":150}`), false))
	rh := &fakeRehydrator{}
	var conflicts []ConflictInfo
	a := New(sub, st, ov, rh, Config{Conflict: func(c ConflictInfo) { conflicts = append(conflicts, c) }})
	require.NoError(t, a.Enqueue(testEnv("req1"), []string{testKey}))

	require.NoError(t, a.Drain(ctx))

	intents, err := st.ListIntents()
	require.NoError(t, err)
	require.Empty(t, intents)
	require.Equal(t, 1, rh.calls, "a RevisionConflict must trigger a re-hydrate")

	_, ok, err := ov.Read(testKey)
	require.NoError(t, err)
	require.False(t, ok, "the stale overlay must be discarded, with no confirmed value to fall back to")

	require.Len(t, conflicts, 1)
	require.Equal(t, "req1", conflicts[0].RequestID)
	require.Equal(t, []string{testKey}, conflicts[0].Keys)
}

func TestDrain_OtherRejectionDiscardsWithoutRehydrate(t *testing.T) {
	ctx := context.Background()
	st, ov := openTestStack(t)

	sub := &fakeSubmitter{decide: func(env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		return &processor.OperationReply{
			RequestID: env.RequestID,
			Status:    processor.ReplyStatusRejected,
			Error:     &processor.ReplyError{Code: processor.ErrCodeDDLViolation, Message: "bad shape"},
		}, nil
	}}

	require.NoError(t, ov.Apply(testKey, "req1", []byte(`{"rent":150}`), false))
	rh := &fakeRehydrator{}
	a := New(sub, st, ov, rh, Config{})
	require.NoError(t, a.Enqueue(testEnv("req1"), []string{testKey}))

	require.NoError(t, a.Drain(ctx))

	require.Zero(t, rh.calls, "a non-conflict rejection must not trigger a re-hydrate")
	_, ok, err := ov.Read(testKey)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestDrain_TransportFailureLeavesIntentQueued(t *testing.T) {
	ctx := context.Background()
	st, ov := openTestStack(t)

	sub := &fakeSubmitter{} // no decide configured — every Submit fails, as if offline.
	a := New(sub, st, ov, nil, Config{})
	require.NoError(t, a.Enqueue(testEnv("req1"), nil))

	err := a.Drain(ctx)
	require.Error(t, err)

	intents, err2 := st.ListIntents()
	require.NoError(t, err2)
	require.Len(t, intents, 1, "a transport failure must leave the intent queued for a later Drain")
}

func TestDrain_MalformedIntentIsDroppedNotWedged(t *testing.T) {
	ctx := context.Background()
	st, ov := openTestStack(t)
	// store.EnqueueIntent validates its argument is syntactically valid JSON
	// (json.RawMessage), so genuinely malformed bytes can never reach the
	// queue — the reachable "malformed" case is syntactically valid JSON
	// that doesn't carry an envelope (e.g. written by a future buggy path).
	_, err := st.EnqueueIntent([]byte("{}"))
	require.NoError(t, err)

	a := New(&fakeSubmitter{}, st, ov, nil, Config{})
	require.NoError(t, a.Drain(ctx))

	intents, err := st.ListIntents()
	require.NoError(t, err)
	require.Empty(t, intents)
}

func TestDrain_MultipleIntentsSubmitInFIFOOrder(t *testing.T) {
	ctx := context.Background()
	st, ov := openTestStack(t)

	sub := &fakeSubmitter{decide: func(env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted}, nil
	}}

	a := New(sub, st, ov, nil, Config{})
	require.NoError(t, a.Enqueue(testEnv("req1"), nil))
	require.NoError(t, a.Enqueue(testEnv("req2"), nil))
	require.NoError(t, a.Enqueue(testEnv("req3"), nil))

	require.NoError(t, a.Drain(ctx))

	intents, err := st.ListIntents()
	require.NoError(t, err)
	require.Empty(t, intents)
	require.Equal(t, []string{"req1", "req2", "req3"}, sub.seen)
}

func TestGC_PrunesSupersededOverlays(t *testing.T) {
	st, ov := openTestStack(t)
	_, err := st.ApplyUpsert(testKey, 3, []byte(`{"rent":100}`))
	require.NoError(t, err)
	require.NoError(t, ov.Apply(testKey, "req1", []byte(`{"rent":150}`), false))
	_, err = st.ApplyUpsert(testKey, 4, []byte(`{"rent":175}`))
	require.NoError(t, err)

	a := New(nil, st, ov, nil, Config{})
	stillPending, err := a.GC()
	require.NoError(t, err)
	require.Zero(t, stillPending)

	keys, err := ov.PendingKeys()
	require.NoError(t, err)
	require.Empty(t, keys)
}

func TestGC_KeepsUnsupersededOverlay(t *testing.T) {
	st, ov := openTestStack(t)
	require.NoError(t, ov.Apply(testKey, "req1", []byte(`{"rent":150}`), false))

	a := New(nil, st, ov, nil, Config{})
	stillPending, err := a.GC()
	require.NoError(t, err)
	require.Equal(t, 1, stillPending)
}
