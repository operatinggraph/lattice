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
	"github.com/asolgan/lattice/internal/loom"
	"github.com/asolgan/lattice/internal/loom/control"
)

// fakeEngine satisfies the unexported engineControl interface structurally — it
// implements the five methods with the exact signatures *loom.Engine has, so
// control.NewService accepts it. No real *loom.Engine is needed here
// (internal/loom's own tests cover the real engine wiring).
type fakeEngine struct {
	mu        sync.Mutex
	instances []loom.InstanceSummary
	consumers []loom.ConsumerStatus
	detail    map[string]loom.InstanceDetail
	calls     []string // op:name, in call order
	errOn     map[string]error
	pauseNote string         // note PauseConsumer returns on success
	panicOn   map[string]any // op:name → value to panic with inside the engine call
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		detail:  make(map[string]loom.InstanceDetail),
		errOn:   make(map[string]error),
		panicOn: make(map[string]any),
	}
}

func (f *fakeEngine) ListInstances(_ context.Context) ([]loom.InstanceSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "list:")
	if err := f.errOn["list:"]; err != nil {
		return nil, err
	}
	out := make([]loom.InstanceSummary, len(f.instances))
	copy(out, f.instances)
	return out, nil
}

func (f *fakeEngine) ListConsumers(_ context.Context) ([]loom.ConsumerStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "consumers:")
	if err := f.errOn["consumers:"]; err != nil {
		return nil, err
	}
	out := make([]loom.ConsumerStatus, len(f.consumers))
	copy(out, f.consumers)
	return out, nil
}

func (f *fakeEngine) InspectInstance(_ context.Context, instanceID string) (loom.InstanceDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "inspect:"+instanceID)
	if err := f.errOn["inspect:"+instanceID]; err != nil {
		return loom.InstanceDetail{}, err
	}
	return f.detail[instanceID], nil
}

func (f *fakeEngine) PauseConsumer(_ context.Context, name string) (string, error) {
	if err := f.record("pause", name); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pauseNote, nil
}

func (f *fakeEngine) ResumeConsumer(_ context.Context, name string) error {
	return f.record("resume", name)
}

func (f *fakeEngine) record(op, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, op+":"+name)
	if v, ok := f.panicOn[op+":"+name]; ok {
		panic(v)
	}
	if err, ok := f.errOn[op+":"+name]; ok {
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

// spyCapability records every Authorize call and can be configured to deny.
type spyCapability struct {
	mu      sync.Mutex
	calls   []string // op:targetID
	denyErr error
}

func (c *spyCapability) Authorize(_ context.Context, _, op, targetID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, op+":"+targetID)
	return c.denyErr
}

func (c *spyCapability) callLog() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.calls))
	copy(out, c.calls)
	return out
}

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

// startService starts a loom-control responder on nc backed by eng + cap.
func startService(t *testing.T, nc *nats.Conn, eng *fakeEngine, cap control.CapabilityChecker) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc := control.NewService(eng, cap, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))
}

// --- Subject helpers --------------------------------------------------------

func TestControl_Subjects_Exact(t *testing.T) {
	assert.Equal(t, "lattice.ctrl.loom.list", control.ListSubject())
	assert.Equal(t, "lattice.ctrl.loom.consumers", control.ConsumersSubject())
	assert.Equal(t, "lattice.ctrl.loom.abc.inspect", control.NameSubject("abc", "inspect"))
	assert.Equal(t, "lattice.ctrl.loom.loom-widget.pause", control.NameSubject("loom-widget", "pause"))
}

// --- list -------------------------------------------------------------------

func TestControl_List_HappyPath(t *testing.T) {
	nc := startTestServer(t)
	eng := newFakeEngine()
	eng.instances = []loom.InstanceSummary{
		{InstanceID: "i1", PatternRef: "vtx.meta.p1", SubjectKey: "vtx.widget.w1", Cursor: 0, Status: "running"},
		{InstanceID: "i2", PatternRef: "vtx.meta.p2", SubjectKey: "vtx.widget.w2", Cursor: 3, Status: "complete"},
	}
	cap := &spyCapability{}
	startService(t, nc, eng, cap)

	resp := sendRequest(t, nc, control.ListSubject())
	require.Empty(t, resp.Error)
	require.Len(t, resp.Instances, 2)
	assert.Equal(t, "i1", resp.Instances[0].InstanceID)
	assert.Equal(t, "complete", resp.Instances[1].Status)
	// Capability checked per request.
	assert.Equal(t, []string{"list:"}, cap.callLog())
}

func TestControl_List_EngineError(t *testing.T) {
	nc := startTestServer(t)
	eng := newFakeEngine()
	eng.errOn["list:"] = errors.New("loom: list instances: kv down")
	startService(t, nc, eng, nil)

	resp := sendRequest(t, nc, control.ListSubject())
	assert.NotEmpty(t, resp.Error)
	assert.Contains(t, resp.Error, "kv down")
	assert.Nil(t, resp.Instances)
}

// --- consumers --------------------------------------------------------------

func TestControl_Consumers_HappyPath(t *testing.T) {
	nc := startTestServer(t)
	eng := newFakeEngine()
	eng.consumers = []loom.ConsumerStatus{
		{Name: "loom-trigger", State: "running"},
		{Name: "loom-outbox-relay", State: "running"},
		{Name: "loom-widget", State: "pausedManual"},
	}
	cap := &spyCapability{}
	startService(t, nc, eng, cap)

	resp := sendRequest(t, nc, control.ConsumersSubject())
	require.Empty(t, resp.Error)
	require.Len(t, resp.Consumers, 3)
	assert.Equal(t, "loom-widget", resp.Consumers[2].Name)
	assert.Equal(t, "pausedManual", resp.Consumers[2].State)
	assert.Equal(t, []string{"consumers:"}, cap.callLog())
}

func TestControl_Consumers_EngineError(t *testing.T) {
	nc := startTestServer(t)
	eng := newFakeEngine()
	eng.errOn["consumers:"] = errors.New("loom: consumers: boom")
	startService(t, nc, eng, nil)

	resp := sendRequest(t, nc, control.ConsumersSubject())
	assert.NotEmpty(t, resp.Error)
	assert.Contains(t, resp.Error, "boom")
	assert.Nil(t, resp.Consumers)
}

// --- inspect ----------------------------------------------------------------

func TestControl_Inspect_Running(t *testing.T) {
	nc := startTestServer(t)
	eng := newFakeEngine()
	eng.detail["i1"] = loom.InstanceDetail{
		Instance:    loom.InstanceSummary{InstanceID: "i1", Status: "running", Cursor: 1},
		CurrentStep: &loom.Step{Kind: "userTask", Operation: "ApproveThing"},
		Terminal:    false,
	}
	cap := &spyCapability{}
	startService(t, nc, eng, cap)

	resp := sendRequest(t, nc, control.NameSubject("i1", "inspect"))
	require.Empty(t, resp.Error)
	require.NotNil(t, resp.Instance)
	assert.False(t, resp.Instance.Terminal)
	require.NotNil(t, resp.Instance.CurrentStep)
	assert.Equal(t, "userTask", resp.Instance.CurrentStep.Kind)
	assert.Equal(t, []string{"inspect:i1"}, cap.callLog())
	assert.Equal(t, []string{"inspect:i1"}, eng.callLog())
}

func TestControl_Inspect_Terminal(t *testing.T) {
	nc := startTestServer(t)
	eng := newFakeEngine()
	eng.detail["done1"] = loom.InstanceDetail{
		Instance:    loom.InstanceSummary{InstanceID: "done1", Status: "complete", Cursor: 2},
		CurrentStep: nil,
		Terminal:    true,
	}
	startService(t, nc, eng, nil)

	resp := sendRequest(t, nc, control.NameSubject("done1", "inspect"))
	require.Empty(t, resp.Error)
	require.NotNil(t, resp.Instance)
	assert.True(t, resp.Instance.Terminal)
	assert.Nil(t, resp.Instance.CurrentStep)
}

// TestControl_Inspect_TypedErrors proves the engine's typed errors (missing pin,
// cursor out of range, not found) propagate as a structured error reply — never a
// panic / request timeout.
func TestControl_Inspect_TypedErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		errText string
	}{
		{"missingpin", "pattern pin missing for live instance (pin is written atomically with the instance)"},
		{"oob", `loom: instance "oob" cursor 5 out of range (pattern has 1 steps)`},
		{"ghost", `loom: instance "ghost" not found`},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			nc := startTestServer(t)
			eng := newFakeEngine()
			eng.errOn["inspect:"+tc.name] = errors.New(tc.errText)
			startService(t, nc, eng, nil)

			resp := sendRequest(t, nc, control.NameSubject(tc.name, "inspect"))
			assert.NotEmpty(t, resp.Error, "typed error must surface as a structured reply")
			assert.Contains(t, resp.Error, tc.errText)
			assert.Nil(t, resp.Instance)
		})
	}
}

// --- pause / resume ---------------------------------------------------------

func TestControl_Pause_HappyPath(t *testing.T) {
	nc := startTestServer(t)
	eng := newFakeEngine()
	// The engine composes the advisory note; the handler must plumb whatever it
	// returns into PauseResult.Note verbatim (C7 + the per-domain stall note).
	eng.pauseNote = "manual pause persists across restart until resume; in-flight instances awaiting this domain will stall until resume"
	cap := &spyCapability{}
	startService(t, nc, eng, cap)

	resp := sendRequest(t, nc, control.NameSubject("loom-widget", "pause"))
	require.Empty(t, resp.Error)
	require.NotNil(t, resp.Pause)
	assert.True(t, resp.Pause.Paused)
	// The handler plumbs the engine's composed note through unchanged.
	assert.Equal(t, eng.pauseNote, resp.Pause.Note)
	assert.Contains(t, resp.Pause.Note, "persists across restart")
	assert.Equal(t, []string{"pause:loom-widget"}, eng.callLog())
	assert.Equal(t, []string{"pause:loom-widget"}, cap.callLog())
}

func TestControl_Resume_HappyPath(t *testing.T) {
	nc := startTestServer(t)
	eng := newFakeEngine()
	startService(t, nc, eng, nil)

	resp := sendRequest(t, nc, control.NameSubject("loom-widget", "resume"))
	require.Empty(t, resp.Error)
	require.NotNil(t, resp.Resume)
	assert.True(t, resp.Resume.Resumed)
	assert.Equal(t, []string{"resume:loom-widget"}, eng.callLog())
}

// TestControl_Pause_UnknownName proves an engine not-managed error surfaces as a
// structured error reply.
func TestControl_Pause_UnknownName(t *testing.T) {
	nc := startTestServer(t)
	eng := newFakeEngine()
	eng.errOn["pause:loom-nope"] = errors.New(`loom: consumer not managed: "loom-nope"`)
	startService(t, nc, eng, nil)

	resp := sendRequest(t, nc, control.NameSubject("loom-nope", "pause"))
	assert.NotEmpty(t, resp.Error)
	assert.Contains(t, resp.Error, "not managed")
	assert.Nil(t, resp.Pause)
}

// TestControl_Pause_RelayDeadlineRejected proves the engine's
// dispatch/crash-safety-critical rejection surfaces as a structured error reply
// for both the relay and the deadline consumer.
func TestControl_Pause_RelayDeadlineRejected(t *testing.T) {
	for _, name := range []string{"loom-outbox-relay", "loom-deadline"} {
		name := name
		t.Run(name, func(t *testing.T) {
			nc := startTestServer(t)
			eng := newFakeEngine()
			eng.errOn["pause:"+name] = errors.New("loom: consumer is dispatch/crash-safety critical and cannot be paused: " + name)
			startService(t, nc, eng, nil)

			resp := sendRequest(t, nc, control.NameSubject(name, "pause"))
			assert.NotEmpty(t, resp.Error)
			assert.Contains(t, resp.Error, "crash-safety critical")
			assert.Nil(t, resp.Pause)
		})
	}
}

// --- crafted subjects / names -----------------------------------------------

// TestControl_CraftedNames feeds crafted name tokens at the per-name endpoints.
// The 5-token parse alone does not reject these — safety rests on the downstream
// not-found (inspect) / not-managed (pause/resume) guards, which here are modelled
// by the fake returning a typed error. The point of the row set is that NONE
// panics or times out: every one gets a structured reply.
func TestControl_CraftedNames(t *testing.T) {
	// Names that survive the single-token wildcard as a literal token. "*" / ">"
	// in a PUBLISHED subject are literal tokens, not wildcards, so they reach the
	// handler and must be handled gracefully (engine returns a typed error).
	for _, name := range []string{"*", ">", "list", "consumers", "naïve-unicodé"} {
		name := name
		t.Run("inspect/"+name, func(t *testing.T) {
			nc := startTestServer(t)
			eng := newFakeEngine()
			eng.errOn["inspect:"+name] = errors.New(`loom: instance "` + name + `" not found`)
			startService(t, nc, eng, nil)

			resp := sendRequest(t, nc, control.NameSubject(name, "inspect"))
			assert.NotEmpty(t, resp.Error)
			assert.Nil(t, resp.Instance)
		})
		t.Run("pause/"+name, func(t *testing.T) {
			nc := startTestServer(t)
			eng := newFakeEngine()
			eng.errOn["pause:"+name] = errors.New(`loom: consumer not managed: "` + name + `"`)
			startService(t, nc, eng, nil)

			resp := sendRequest(t, nc, control.NameSubject(name, "pause"))
			assert.NotEmpty(t, resp.Error)
			assert.Nil(t, resp.Pause)
		})
	}
}

// TestControl_CapabilityDenied proves a capability denial short-circuits the op:
// the engine method is NOT invoked and the response carries the denial error.
func TestControl_CapabilityDenied(t *testing.T) {
	nc := startTestServer(t)
	eng := newFakeEngine()
	cap := &spyCapability{denyErr: errors.New("capability: denied")}
	startService(t, nc, eng, cap)

	resp := sendRequest(t, nc, control.NameSubject("loom-widget", "pause"))
	assert.NotEmpty(t, resp.Error)
	assert.Contains(t, resp.Error, "denied")
	assert.Nil(t, resp.Pause)
	// The engine pause must not have been called (capability ran first).
	assert.Empty(t, eng.callLog())
}

// TestControl_HandlerPanic_RecoversToErrorReply proves the handler-level panic
// guard: the micro framework does not recover panics in handlers (they run in the
// NATS async-subscription goroutine), so a panic in a control handler — here
// injected into the engine's pause call — must be recovered and turned into a
// structured error reply rather than crashing the process or wedging the request.
func TestControl_HandlerPanic_RecoversToErrorReply(t *testing.T) {
	nc := startTestServer(t)
	eng := newFakeEngine()
	eng.panicOn["pause:loom-widget"] = "boom in engine"
	startService(t, nc, eng, nil)

	resp := sendRequest(t, nc, control.NameSubject("loom-widget", "pause"))
	assert.NotEmpty(t, resp.Error, "a recovered panic must surface as an error reply, not a timeout/crash")
	assert.Contains(t, resp.Error, "internal error")
	assert.Nil(t, resp.Pause)

	// The responder is still alive after recovering — a subsequent request on a
	// healthy op still works.
	resp2 := sendRequest(t, nc, control.ListSubject())
	require.Empty(t, resp2.Error)
}

// TestControl_UnknownOp verifies a request to an unregistered op subject gets no
// responder (times out) — mirrors the documented NATS Services behaviour.
func TestControl_UnknownOp(t *testing.T) {
	nc := startTestServer(t)
	eng := newFakeEngine()
	startService(t, nc, eng, nil)

	_, err := nc.Request(control.NameSubject("i1", "bogus"), nil, 250*time.Millisecond)
	require.Error(t, err, "request to unregistered op subject must fail (no responders / timeout)")
}

// TestControl_StartNATSListener_AlreadyStarted verifies a second start errors.
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
