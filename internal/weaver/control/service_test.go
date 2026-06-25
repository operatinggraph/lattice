package control_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/weaver"
	"github.com/asolgan/lattice/internal/weaver/control"
)

// fakeEngine satisfies the unexported engineControl interface structurally —
// it implements ListTargets/Disable/Enable/Revoke with the exact same
// signatures as *weaver.Engine, so control.NewService accepts it. No real
// *weaver.Engine is needed for this package's tests (internal/weaver's own
// tests cover the real engine wiring, per Task 3).
type fakeEngine struct {
	mu      sync.Mutex
	targets []weaver.TargetSummary
	calls   []string // op:targetID, in call order
	errOn   map[string]error
}

func newFakeEngine(targets ...weaver.TargetSummary) *fakeEngine {
	return &fakeEngine{targets: targets, errOn: make(map[string]error)}
}

func (f *fakeEngine) ListTargets(_ context.Context) ([]weaver.TargetSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]weaver.TargetSummary, len(f.targets))
	copy(out, f.targets)
	return out, nil
}

func (f *fakeEngine) Disable(_ context.Context, targetID string) error {
	return f.record("disable", targetID)
}

func (f *fakeEngine) Enable(_ context.Context, targetID string) error {
	return f.record("enable", targetID)
}

func (f *fakeEngine) Revoke(_ context.Context, targetID string) error {
	return f.record("revoke", targetID)
}

func (f *fakeEngine) record(op, targetID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, op+":"+targetID)
	if err, ok := f.errOn[op+":"+targetID]; ok {
		return err
	}
	return nil
}

func (f *fakeEngine) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

// startTestServer starts an in-memory JetStream-enabled NATS server and
// returns a connected *nats.Conn.
func startTestServer(t *testing.T) *nats.Conn {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	srv := natstest.RunServer(opts)
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func sendRequest(t *testing.T, nc *nats.Conn, subject string) control.ControlResponse {
	t.Helper()
	reply, err := nc.Request(subject, nil, 2*time.Second)
	require.NoError(t, err, "NATS request to control endpoint %s must succeed", subject)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	return resp
}

// TestControl_List verifies the "list" op returns the engine's
// ListTargets snapshot on the exact subject lattice.ctrl.weaver.list (AC #5).
func TestControl_List(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	want := []weaver.TargetSummary{
		{TargetID: "t1", LensRef: "lens-1", Gaps: []string{"missing_a"}, State: "active"},
		{TargetID: "t2", LensRef: "lens-2", Gaps: []string{"missing_b"}, State: "disabled"},
	}
	eng := newFakeEngine(want...)
	svc := control.NewService(eng, nil, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.ListSubject())

	require.Empty(t, resp.Error)
	require.Len(t, resp.Targets, 2)
	assert.Equal(t, want, resp.Targets)
}

// TestControl_ListSubject_Exact verifies that control.ListSubject() matches
// the documented subject lattice.ctrl.weaver.list (AC #5).
func TestControl_ListSubject_Exact(t *testing.T) {
	assert.Equal(t, "lattice.ctrl.weaver.list", control.ListSubject())
}

// TestControl_TargetSubject_Exact verifies that control.TargetSubject builds
// the documented 5-token subject lattice.ctrl.weaver.<targetId>.<op> (AC #5).
func TestControl_TargetSubject_Exact(t *testing.T) {
	assert.Equal(t, "lattice.ctrl.weaver.t1.disable", control.TargetSubject("t1", "disable"))
}

// TestControl_Disable verifies the "disable" op invokes Engine.Disable for
// the target ID extracted from the subject and returns Disabled=true (AC #5).
func TestControl_Disable(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	svc := control.NewService(eng, nil, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.TargetSubject("t1", "disable"))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.Disable)
	assert.True(t, resp.Disable.Disabled)
	assert.Equal(t, []string{"disable:t1"}, eng.callLog())
}

// TestControl_Enable verifies the "enable" op invokes Engine.Enable for the
// target ID extracted from the subject and returns Enabled=true (AC #5, #7).
func TestControl_Enable(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	svc := control.NewService(eng, nil, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.TargetSubject("t1", "enable"))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.Enable)
	assert.True(t, resp.Enable.Enabled)
	assert.Equal(t, []string{"enable:t1"}, eng.callLog())
}

// TestControl_Revoke verifies the "revoke" op invokes Engine.Revoke for the
// target ID extracted from the subject and returns Revoked=true (AC #5, #4).
func TestControl_Revoke(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	svc := control.NewService(eng, nil, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.TargetSubject("t1", "revoke"))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.Revoke)
	assert.True(t, resp.Revoke.Revoked)
	assert.Equal(t, []string{"revoke:t1"}, eng.callLog())
}

// TestControl_Disable_EngineError verifies that an error returned by
// Engine.Disable (e.g. "target not registered") surfaces in the response's
// Error field.
func TestControl_Disable_EngineError(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	eng.errOn["disable:ghost"] = errors.New(`weaver: target "ghost" not registered`)
	svc := control.NewService(eng, nil, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.TargetSubject("ghost", "disable"))

	assert.NotEmpty(t, resp.Error)
	assert.Contains(t, resp.Error, "ghost")
	assert.Nil(t, resp.Disable)
}

// TestControl_UnknownOp verifies that a request to an unregistered op
// subject receives no response — there is no endpoint registered for it, so
// the request times out (mirrors internal/refractor/control's documented
// NATS Services behaviour for unknown ops).
func TestControl_UnknownOp(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	svc := control.NewService(eng, nil, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	subj := control.TargetSubject("t1", "bogus")
	_, err := nc.Request(subj, nil, 250*time.Millisecond)
	require.Error(t, err, "request to unregistered op subject must fail (no responders / timeout)")
}

// TestControl_StartNATSListener_AlreadyStarted verifies that calling
// StartNATSListener twice returns an error.
func TestControl_StartNATSListener_AlreadyStarted(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	svc := control.NewService(eng, nil, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	err := svc.StartNATSListener(ctx, nc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}
