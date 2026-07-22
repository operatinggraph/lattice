package sync

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	edgestore "github.com/operatinggraph/lattice/internal/edge/store"
	"github.com/operatinggraph/lattice/internal/edge/transport"
	"github.com/operatinggraph/lattice/internal/edge/transport/natstransport"
	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/control/controlwire"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// fakeControlTransport is a Manager Transport whose Request answers control
// RPCs from a programmable func, so ensureFresh/gapped can be unit-tested
// without a live NATS server — the paths where a real service cannot easily
// produce the shape under test (an absent syncgap result, a persistent RPC
// failure). RunDurableConsumer is never reached by ensureFresh, so it panics.
type fakeControlTransport struct {
	requests []string // op suffixes requested, in order
	reply    func(op string, body controlwire.ControlRequest) (controlwire.ControlResponse, error)
}

func (f *fakeControlTransport) RunDurableConsumer(context.Context, transport.ConsumerConfig, transport.Handler) error {
	panic("fakeControlTransport: RunDurableConsumer not used by these tests")
}

func (f *fakeControlTransport) Request(_ context.Context, subject string, data []byte, _ string) ([]byte, error) {
	op := subject[strings.LastIndex(subject, ".")+1:]
	f.requests = append(f.requests, op)
	var body controlwire.ControlRequest
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	resp, err := f.reply(op, body)
	if err != nil {
		return nil, err
	}
	return json.Marshal(resp)
}

// happyControlReply answers register/hydrate with success and syncgap with the
// given gapped value — the baseline a test overrides for the path it probes.
func happyControlReply(gapped bool) func(op string, _ controlwire.ControlRequest) (controlwire.ControlResponse, error) {
	return func(op string, _ controlwire.ControlRequest) (controlwire.ControlResponse, error) {
		switch op {
		case "register":
			return controlwire.ControlResponse{PersonalRegister: &controlwire.PersonalRegisterResult{Registered: true}}, nil
		case "hydrate":
			return controlwire.ControlResponse{PersonalHydrate: &controlwire.PersonalHydrateResult{Hydrated: true, Revision: 1}}, nil
		case "syncgap":
			return controlwire.ControlResponse{PersonalSyncGap: &controlwire.PersonalSyncGapResult{Gapped: gapped}}, nil
		default:
			return controlwire.ControlResponse{Error: "unexpected op " + op}, nil
		}
	}
}

func newFakeManager(t *testing.T, tr *fakeControlTransport, cursor uint64, haveCursor bool) *Manager {
	t.Helper()
	st := openTestStore(t)
	if haveCursor {
		require.NoError(t, st.SetCursor(cursor))
	}
	mgr, err := New(tr, st, Config{IdentityID: "identityA", DeviceID: "deviceX", Logger: testutil.TestLogger()})
	require.NoError(t, err)
	return mgr
}

// TestManager_EnsureFresh_ColdStartMakesNoSyncGapCall proves a never-hydrated
// node (no stored cursor) takes the cold path — register + hydrate — and never
// asks the syncgap RPC.
func TestManager_EnsureFresh_ColdStartMakesNoSyncGapCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tr := &fakeControlTransport{reply: happyControlReply(false)}
	mgr := newFakeManager(t, tr, 0, false)

	require.NoError(t, mgr.ensureFresh(ctx))
	assert.NotContains(t, tr.requests, "syncgap", "cold start must not call syncgap")
	assert.Contains(t, tr.requests, "hydrate", "cold start must hydrate")
}

// TestManager_EnsureFresh_WarmGappedTrueHydrates proves gapped=true over the
// RPC triggers a re-hydrate.
func TestManager_EnsureFresh_WarmGappedTrueHydrates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tr := &fakeControlTransport{reply: happyControlReply(true)}
	mgr := newFakeManager(t, tr, 5, true)

	require.NoError(t, mgr.ensureFresh(ctx))
	assert.Equal(t, "syncgap", tr.requests[0], "warm resume asks syncgap first")
	assert.Contains(t, tr.requests, "hydrate", "gapped=true must re-hydrate")
}

// TestManager_EnsureFresh_AbsentSyncGapResultErrors is the silent-data-loss
// guard (edge-syncgap-control-rpc-design.md §3.3): a decodable response whose
// personalSyncGap is absent must be an ERROR, never defaulted to
// gapped=false — a warm node that should re-hydrate would otherwise resume its
// durable and skip the pruned deltas forever.
func TestManager_EnsureFresh_AbsentSyncGapResultErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tr := &fakeControlTransport{reply: func(op string, _ controlwire.ControlRequest) (controlwire.ControlResponse, error) {
		// A well-formed response with neither Error nor a result struct.
		return controlwire.ControlResponse{}, nil
	}}
	mgr := newFakeManager(t, tr, 5, true)

	err := mgr.ensureFresh(ctx)
	require.Error(t, err, "an absent syncgap result must never be treated as gapped=false")
	assert.NotContains(t, tr.requests, "hydrate", "an unverifiable gap answer must not resume, and must not blindly hydrate either")
}

// TestManager_EnsureFresh_SyncGapRetryIsBounded proves a persistent
// control-plane failure fails closed after a BOUNDED number of syncgap
// attempts — never an unbounded boot hang (edge-syncgap-control-rpc-design.md
// §7). The RPC errors every time; ensureFresh must error after exactly
// syncGapMaxAttempts tries.
func TestManager_EnsureFresh_SyncGapRetryIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tr := &fakeControlTransport{reply: func(op string, _ controlwire.ControlRequest) (controlwire.ControlResponse, error) {
		return controlwire.ControlResponse{}, errors.New("control plane down")
	}}
	mgr := newFakeManager(t, tr, 5, true)

	err := mgr.ensureFresh(ctx)
	require.Error(t, err, "a persistent syncgap failure must fail closed, never resume unverified")
	assert.Len(t, tr.requests, syncGapMaxAttempts, "the retry must be bounded to syncGapMaxAttempts")
}

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

// transientErrStore is an edgestore.Store whose ApplyUpsert returns a plain
// (non-sentinel) error for any key — the transient-failure case handle() must
// Nak rather than Term. Only ApplyUpsert is exercised by that path; the
// embedded nil Store makes any other call panic loudly if the path changes.
type transientErrStore struct{ edgestore.Store }

func (transientErrStore) ApplyUpsert(string, uint64, json.RawMessage) (bool, error) {
	return false, errors.New("edge/store: simulated transient backing-engine failure")
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
	// The syncgap seam reads the live SYNC stream's earliest retained sequence
	// on the control host's own connection, mirroring cmd/refractor's
	// IsPersonalLens wiring — so gapped() gets a real gap answer over the RPC.
	svc.SetSyncFirstSeq(func(ctx context.Context) (uint64, error) {
		s, err := conn.JetStream().Stream(ctx, defaultStream)
		if err != nil {
			return 0, err
		}
		return s.CachedInfo().State.FirstSeq, nil
	})
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
// the stream's retention window answers gapped=false over the personal.syncgap
// RPC and does NOT trigger hydration: the fakeHydrator records no call. (Cursor
// == FirstSeq is the fresh boundary — gapped is strictly cursor < FirstSeq.)
func TestManager_EnsureFresh_WarmCursorSkipsHydrate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn := newSyncTestConn(t, ctx)
	interestKV := openInterestKV(t, ctx, conn)

	s, err := conn.JetStream().Stream(ctx, defaultStream)
	require.NoError(t, err)
	firstSeq := s.CachedInfo().State.FirstSeq

	h := &fakeHydrator{conn: conn}
	startControlService(t, ctx, conn, h, interestKV)

	st := openTestStore(t)
	require.NoError(t, st.SetCursor(firstSeq))

	mgr, err := New(natstransport.New(conn), st, Config{IdentityID: "identityA", DeviceID: "deviceX", Logger: testutil.TestLogger()})
	require.NoError(t, err)

	assert.NoError(t, mgr.ensureFresh(ctx))
	assert.Empty(t, h.calledWith, "a warm cursor (gapped=false) must not trigger hydrate")
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

// TestManager_Handle_UnstorableKeyTerminates proves a delta carrying a key the
// store can never accept (neither a Contract #1 key nor a manifest-prefixed
// projection row — e.g. a lens `ns` typo) is Term'd, not Nak'd: the store
// rejects it identically on every redelivery, so a Nak would hot-loop it
// forever (edge-lattice-full-design.md §8.1 RR-2(i)). A transient store error
// still Naks — proven separately by the fake below.
func TestManager_Handle_UnstorableKeyTerminates(t *testing.T) {
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

	// A syntactically well-formed envelope whose key is neither Contract #1 nor
	// manifest-prefixed: the store's ErrUnstorableKey path.
	up := mgr.handle(ctx, transport.Delta{Sequence: 1, Body: body(deltaEnvelope{Op: "upsert", Key: "not-a-real-key", Revision: 1, Data: json.RawMessage(`{"x":1}`)})})
	assert.Equal(t, transport.Term, up, "an unstorable upsert key is dropped, not redelivered")

	del := mgr.handle(ctx, transport.Delta{Sequence: 2, Body: body(deltaEnvelope{Op: "delete", Key: "manifes.typo.key", Revision: 1})})
	assert.Equal(t, transport.Term, del, "an unstorable delete key is dropped, not redelivered")
}

// TestManager_Handle_TransientApplyErrorNaks proves the RR-2(i) classification
// is narrow: only store.ErrUnstorableKey terminates; any OTHER apply error is
// treated as transient and Nak'd for redelivery (e.g. a backing-engine I/O
// blip). A fake store returns a non-sentinel error for a valid key.
func TestManager_Handle_TransientApplyErrorNaks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn := newSyncTestConn(t, ctx)
	mgr, err := New(natstransport.New(conn), &transientErrStore{}, Config{IdentityID: "identityA", DeviceID: "deviceX", Logger: testutil.TestLogger()})
	require.NoError(t, err)

	leaseID, err := substrate.NewNanoID()
	require.NoError(t, err)
	key := substrate.VertexKey("lease", leaseID)
	b, err := json.Marshal(deltaEnvelope{Op: "upsert", Key: key, Revision: 1, Data: json.RawMessage(`{"x":1}`)})
	require.NoError(t, err)

	decision := mgr.handle(ctx, transport.Delta{Sequence: 1, Body: b})
	assert.Equal(t, transport.Nak, decision, "a transient (non-sentinel) apply error must Nak for redelivery, not Term")
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
