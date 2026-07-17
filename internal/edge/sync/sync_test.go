package sync

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	edgestore "github.com/asolgan/lattice/internal/edge/store"
	"github.com/asolgan/lattice/internal/edge/transport"
	"github.com/asolgan/lattice/internal/edge/transport/natstransport"
	"github.com/asolgan/lattice/internal/refractor/control"
	"github.com/asolgan/lattice/internal/refractor/subjects"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

// newSyncTestConn spins up an embedded JetStream-enabled NATS server and
// ensures the SYNC stream exists (unbounded retention — callers that need to
// force retention eviction recreate the stream with an explicit MaxMsgs via
// conn.JetStream() directly).
func newSyncTestConn(t *testing.T, ctx context.Context) *substrate.Conn {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url})
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	require.NoError(t, conn.EnsureStream(ctx, substrate.StreamSpec{
		Name:     defaultStream,
		Subjects: []string{defaultSubjectPrefix + ".>"},
	}))
	return conn
}

func openTestStore(t *testing.T) edgestore.Store {
	t.Helper()
	st, err := edgestore.Open(filepath.Join(t.TempDir(), "edge.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func openInterestKV(t *testing.T, ctx context.Context, conn *substrate.Conn) *substrate.KV {
	t.Helper()
	_, err := conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "personal-lens-interest"})
	require.NoError(t, err)
	kv, err := conn.OpenKV(ctx, "personal-lens-interest")
	require.NoError(t, err)
	return kv
}

// fakeHydrator is a control.Hydrator test double that records the identityID
// it was called with and, when publish is set, fans out a delta itself —
// mirroring the real Personal-Lens Hydrator's bulk-projection side effect.
type fakeHydrator struct {
	conn       *substrate.Conn
	revision   uint64
	calledWith []string
	publish    func(ctx context.Context, conn *substrate.Conn, identityID string)
}

func (f *fakeHydrator) Hydrate(ctx context.Context, identityID string) (uint64, error) {
	f.calledWith = append(f.calledWith, identityID)
	if f.publish != nil {
		f.publish(ctx, f.conn, identityID)
	}
	return f.revision, nil
}

func startControlService(t *testing.T, ctx context.Context, conn *substrate.Conn, h control.Hydrator, interestKV *substrate.KV) {
	t.Helper()
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalHydrator(h)
	svc.SetPersonalInterestKV(interestKV)
	require.NoError(t, svc.StartNATSListener(ctx, conn.NATS()))
}

func publishDelta(t *testing.T, ctx context.Context, conn *substrate.Conn, identityID string, env deltaEnvelope) {
	t.Helper()
	body, err := json.Marshal(env)
	require.NoError(t, err)
	require.NoError(t, conn.Publish(ctx, subjects.PersonalSync(defaultSubjectPrefix, identityID), body, nil))
}

func TestNew_RequiresIdentityAndDevice(t *testing.T) {
	_, err := New(nil, nil, Config{})
	assert.Error(t, err)
	_, err = New(nil, nil, Config{IdentityID: "a"})
	assert.Error(t, err)
	_, err = New(nil, nil, Config{DeviceID: "b"})
	assert.Error(t, err)
}

// TestManager_ColdStart_HydratesRegistersAndAppliesBulkDelta proves the
// cold-start path end-to-end: a never-hydrated node calls personal.register
// + personal.hydrate, the hydrator's bulk-published delta lands on the SYNC
// stream, and the durable consumer applies it to the Local VAL Store and
// advances the cursor.
func TestManager_ColdStart_HydratesRegistersAndAppliesBulkDelta(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := newSyncTestConn(t, ctx)
	interestKV := openInterestKV(t, ctx, conn)

	recipient := "identityA"
	leaseID, err := substrate.NewNanoID()
	require.NoError(t, err)
	key := substrate.VertexKey("lease", leaseID)
	h := &fakeHydrator{conn: conn, revision: 42}
	h.publish = func(ctx context.Context, conn *substrate.Conn, identityID string) {
		publishDelta(t, ctx, conn, identityID, deltaEnvelope{
			Op: "upsert", Key: key, Revision: 1, ProjectionSeq: 1,
			Data: json.RawMessage(`{"monthlyRent":2200}`),
		})
	}
	startControlService(t, ctx, conn, h, interestKV)

	st := openTestStore(t)
	mgr, err := New(natstransport.New(conn), st, Config{IdentityID: recipient, DeviceID: "deviceX", Logger: testutil.TestLogger()})
	require.NoError(t, err)

	runCtx, runCancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- mgr.Run(runCtx) }()

	require.Eventually(t, func() bool {
		entry, ok, err := st.Get(key)
		return err == nil && ok && !entry.Deleted && string(entry.Data) == `{"monthlyRent":2200}`
	}, 10*time.Second, 100*time.Millisecond, "bulk delta must be applied to the local store")

	runCancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Manager.Run did not return after context cancellation")
	}

	assert.Equal(t, []string{recipient}, h.calledWith)
	cursor, ok, err := st.Cursor()
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Greater(t, cursor, uint64(0))
}

// TestManager_EnsureFresh_WarmCursorSkipsHydrate proves a cursor still within
// the stream's retention window does NOT trigger hydration — no control
// service is started at all, so a wrongly-triggered hydrate call would fail
// with "no responders" and this test would catch it.
func TestManager_EnsureFresh_WarmCursorSkipsHydrate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn := newSyncTestConn(t, ctx)

	s, err := conn.JetStream().Stream(ctx, defaultStream)
	require.NoError(t, err)
	firstSeq := s.CachedInfo().State.FirstSeq

	st := openTestStore(t)
	require.NoError(t, st.SetCursor(firstSeq))

	mgr, err := New(natstransport.New(conn), st, Config{IdentityID: "identityA", DeviceID: "deviceX", Logger: testutil.TestLogger()})
	require.NoError(t, err)

	assert.NoError(t, mgr.ensureFresh(ctx))
}

// TestManager_EnsureFresh_GapTriggersHydrate proves a cursor that has fallen
// behind the stream's current FirstSeq (retention pruned messages the node
// never saw) triggers re-hydration instead of a silent skip.
func TestManager_EnsureFresh_GapTriggersHydrate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url := testutil.StartEmbeddedNATS(t)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url})
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	// MaxMsgs:1 forces eviction as new messages arrive, so a handful of
	// publishes deterministically advances FirstSeq past an old cursor —
	// no need to wait out a MaxAge timer.
	_, err = conn.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     defaultStream,
		Subjects: []string{defaultSubjectPrefix + ".>"},
		MaxMsgs:  1,
	})
	require.NoError(t, err)

	recipient := "identityA"
	leaseID, err := substrate.NewNanoID()
	require.NoError(t, err)
	key := substrate.VertexKey("lease", leaseID)
	for i := 0; i < 3; i++ {
		publishDelta(t, ctx, conn, recipient, deltaEnvelope{Op: "upsert", Key: key, Revision: uint64(i + 1)})
	}

	interestKV := openInterestKV(t, ctx, conn)
	h := &fakeHydrator{conn: conn, revision: 99}
	startControlService(t, ctx, conn, h, interestKV)

	st := openTestStore(t)
	require.NoError(t, st.SetCursor(1)) // behind FirstSeq once eviction has run

	mgr, err := New(natstransport.New(conn), st, Config{IdentityID: recipient, DeviceID: "deviceX", Logger: testutil.TestLogger()})
	require.NoError(t, err)

	require.NoError(t, mgr.ensureFresh(ctx))
	assert.Equal(t, []string{recipient}, h.calledWith)
}

// TestManager_Handle covers the message-apply switch directly: upsert,
// delete, hydrationComplete, an unknown op (cursor still advances — forward
// compatibility), and a malformed envelope (terminated, not redelivered).
func TestManager_Handle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn := newSyncTestConn(t, ctx)
	st := openTestStore(t)
	mgr, err := New(natstransport.New(conn), st, Config{IdentityID: "identityA", DeviceID: "deviceX", Logger: testutil.TestLogger()})
	require.NoError(t, err)

	body := func(env deltaEnvelope) []byte {
		b, err := json.Marshal(env)
		require.NoError(t, err)
		return b
	}
	leaseID, err := substrate.NewNanoID()
	require.NoError(t, err)
	key := substrate.VertexKey("lease", leaseID)

	decision := mgr.handle(ctx, transport.Delta{
		Sequence: 1,
		Body:     body(deltaEnvelope{Op: "upsert", Key: key, Revision: 1, Data: json.RawMessage(`{"x":1}`)}),
	})
	require.Equal(t, transport.Ack, decision)
	entry, ok, err := st.Get(key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, entry.Deleted)

	decision = mgr.handle(ctx, transport.Delta{
		Sequence: 2,
		Body:     body(deltaEnvelope{Op: "delete", Key: key, Revision: 2}),
	})
	require.Equal(t, transport.Ack, decision)
	entry, ok, err = st.Get(key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, entry.Deleted)

	decision = mgr.handle(ctx, transport.Delta{Sequence: 3, Body: body(deltaEnvelope{Op: "hydrationComplete", Revision: 100})})
	assert.Equal(t, transport.Ack, decision)
	cursor, ok, err := st.Cursor()
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, uint64(3), cursor)

	decision = mgr.handle(ctx, transport.Delta{Sequence: 4, Body: body(deltaEnvelope{Op: "somethingFutureVersion"})})
	assert.Equal(t, transport.Ack, decision, "unknown op should not block the pipeline")
	cursor, _, err = st.Cursor()
	require.NoError(t, err)
	assert.Equal(t, uint64(4), cursor, "cursor advances even for an unrecognized op")

	decision = mgr.handle(ctx, transport.Delta{Sequence: 5, Body: []byte("not json")})
	assert.Equal(t, transport.Term, decision, "a malformed envelope is dropped, not redelivered")
}

// TestManager_Handle_OnChangeFiresOnlyOnApplied proves the change-notification
// hook (edge-showcase-app-design.md §7 Fire 0, G3) fires exactly once per
// delta that actually lands in the store, with the right deleted flag, and
// stays silent for a stale/duplicate redelivery dropped under last-writer-
// wins-by-revision — a UI host must not be told "changed" for a no-op apply.
func TestManager_Handle_OnChangeFiresOnlyOnApplied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn := newSyncTestConn(t, ctx)
	st := openTestStore(t)

	type change struct {
		key     string
		deleted bool
	}
	var changes []change
	mgr, err := New(natstransport.New(conn), st, Config{
		IdentityID: "identityA", DeviceID: "deviceX", Logger: testutil.TestLogger(),
		OnChange: func(key string, deleted bool) { changes = append(changes, change{key, deleted}) },
	})
	require.NoError(t, err)

	body := func(env deltaEnvelope) []byte {
		b, err := json.Marshal(env)
		require.NoError(t, err)
		return b
	}
	leaseID, err := substrate.NewNanoID()
	require.NoError(t, err)
	key := substrate.VertexKey("lease", leaseID)

	// Fresh upsert: fires with deleted=false.
	mgr.handle(ctx, transport.Delta{Sequence: 1, Body: body(deltaEnvelope{Op: "upsert", Key: key, Revision: 2})})
	// Stale redelivery (revision behind current): dropped, must not fire.
	mgr.handle(ctx, transport.Delta{Sequence: 2, Body: body(deltaEnvelope{Op: "upsert", Key: key, Revision: 1})})
	// Delete: fires with deleted=true.
	mgr.handle(ctx, transport.Delta{Sequence: 3, Body: body(deltaEnvelope{Op: "delete", Key: key, Revision: 3})})
	// Stale delete redelivery: dropped, must not fire.
	mgr.handle(ctx, transport.Delta{Sequence: 4, Body: body(deltaEnvelope{Op: "delete", Key: key, Revision: 1})})

	require.Equal(t, []change{{key, false}, {key, true}}, changes)
}

// TestManager_UpdateInterest_RegistersWithoutHydrating proves the Interest
// re-registration passthrough (edge-showcase-app-design.md §7 Fire 0, G4)
// calls only personal.register — no personal.hydrate — and updates cfg so a
// later reconnect/hydrate re-registers with the new interest.
func TestManager_UpdateInterest_RegistersWithoutHydrating(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn := newSyncTestConn(t, ctx)
	interestKV := openInterestKV(t, ctx, conn)

	recipient := "identityA"
	h := &fakeHydrator{conn: conn}
	startControlService(t, ctx, conn, h, interestKV)

	st := openTestStore(t)
	mgr, err := New(natstransport.New(conn), st, Config{IdentityID: recipient, DeviceID: "deviceX", Logger: testutil.TestLogger()})
	require.NoError(t, err)

	require.NoError(t, mgr.UpdateInterest(ctx, []string{"lease"}, []string{"unit-1"}))

	assert.Empty(t, h.calledWith, "UpdateInterest must not call personal.hydrate")
	assert.Equal(t, []string{"lease"}, mgr.cfg.Types)
	assert.Equal(t, []string{"unit-1"}, mgr.cfg.Anchors)
}
