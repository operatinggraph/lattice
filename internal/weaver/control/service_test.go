package control_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/weaver"
	"github.com/operatinggraph/lattice/internal/weaver/control"
)

// fakeEngine satisfies the unexported engineControl interface structurally —
// it implements ListTargets/Disable/Enable/Revoke/ResetConfidence/
// ResetRetryBudget/ReplayTarget with the exact same signatures as
// *weaver.Engine, so
// control.NewService accepts it. No real
// *weaver.Engine is needed for this package's tests (internal/weaver's own
// tests cover the real engine wiring, per Task 3).
type fakeEngine struct {
	mu      sync.Mutex
	targets []weaver.TargetSummary
	calls   []string // op:targetID, in call order
	errOn   map[string]error
	// resetDeleted is the window count ResetConfidence reports on success.
	resetDeleted int
	// resetPrevious is the dispatch-count ResetRetryBudget reports on success,
	// and resetBudgetArgs records the per-gap arguments it was called with —
	// the two the subject cannot carry.
	resetPrevious   int
	resetBudgetArgs []string
	// replayQueued is the row count ReplayTarget reports on success.
	replayQueued int
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

func (f *fakeEngine) ResetConfidence(_ context.Context, targetID string) (int, error) {
	if err := f.record("resetConfidence", targetID); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resetDeleted, nil
}

func (f *fakeEngine) ResetRetryBudget(_ context.Context, targetID, entityID, gapColumn string) (int, error) {
	f.mu.Lock()
	f.resetBudgetArgs = append(f.resetBudgetArgs, targetID+"/"+entityID+"/"+gapColumn)
	f.mu.Unlock()
	if err := f.record("resetBudget", targetID); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resetPrevious, nil
}

func (f *fakeEngine) ReplayTarget(_ context.Context, targetID string) (int, error) {
	if err := f.record("replayTarget", targetID); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.replayQueued, nil
}

func (f *fakeEngine) budgetArgs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.resetBudgetArgs))
	copy(out, f.resetBudgetArgs)
	return out
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
	srv := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, srv.ClientURL())
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
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
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
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
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
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
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
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.TargetSubject("t1", "revoke"))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.Revoke)
	assert.True(t, resp.Revoke.Revoked)
	assert.Equal(t, []string{"revoke:t1"}, eng.callLog())
}

// TestControl_ResetConfidence verifies the "resetConfidence" op invokes
// Engine.ResetConfidence for the target ID extracted from the subject and
// returns the engine's deleted-window count verbatim — the operator's only
// feedback that the drain reached anything.
func TestControl_ResetConfidence(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	eng.resetDeleted = 3
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.TargetSubject("t1", "resetConfidence"))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.ResetConfidence)
	assert.Equal(t, 3, resp.ResetConfidence.WindowsDeleted)
	assert.Equal(t, []string{"resetConfidence:t1"}, eng.callLog())
	// A reset is confidence-only: it must never be dispatched as a disable,
	// enable, or revoke on the way through.
	assert.Nil(t, resp.Disable)
	assert.Nil(t, resp.Enable)
	assert.Nil(t, resp.Revoke)
}

// TestControl_ResetConfidence_EngineError verifies an unregistered target's
// engine error surfaces in Error rather than reporting a successful zero-window
// drain.
func TestControl_ResetConfidence_EngineError(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	eng.errOn["resetConfidence:ghost"] = errors.New("weaver: target \"ghost\" not registered")
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.TargetSubject("ghost", "resetConfidence"))

	require.Nil(t, resp.ResetConfidence)
	assert.Contains(t, resp.Error, "not registered")
}

// TestControl_ResetConfidence_CapabilityDenied verifies the new verb is gated
// by the same per-op capability check as every other mutating op — it deletes
// engine state, so an ungranted actor must never reach the engine.
func TestControl_ResetConfidence_CapabilityDenied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	svc := control.NewService(eng, denyCapability{err: errors.New("capability denied")}, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.TargetSubject("t1", "resetConfidence"))

	assert.Contains(t, resp.Error, "capability denied")
	assert.Empty(t, eng.callLog(), "a denied resetConfidence must never reach the engine")
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
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
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
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
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
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	err := svc.StartNATSListener(ctx, nc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

// TestControl_ResetBudget_CarriesThePerGapArgumentsInTheBody pins the one way
// this op differs from every other per-target op: the control endpoints are
// registered on a single-token wildcard (lattice.ctrl.weaver.*.<op>), so the
// subject can carry the targetId and nothing else — but the retry budget is
// per-(target, entity, gap). The entityId and gapColumn ride the request body,
// and must arrive at the engine intact, or the verb would reset a different
// gap than the operator named.
func TestControl_ResetBudget_CarriesThePerGapArgumentsInTheBody(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine(weaver.TargetSummary{TargetID: "t1"})
	eng.resetPrevious = 3
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
	if err := svc.StartNATSListener(ctx, nc); err != nil {
		t.Fatalf("start listener: %v", err)
	}

	const entityID = "entityAAAAAAAAAAAAAA"
	body, err := json.Marshal(control.ResetBudgetRequest{EntityID: entityID, GapColumn: "missing_x"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	msg, err := nc.Request(control.TargetSubject("t1", "resetBudget"), body, 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	var resp control.ControlResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected error response: %s", resp.Error)
	}
	if resp.ResetBudget == nil || resp.ResetBudget.PreviousCount != 3 {
		t.Fatalf("resetBudget result = %+v, want previousCount 3", resp.ResetBudget)
	}
	if got, want := eng.budgetArgs(), []string{"t1/" + entityID + "/missing_x"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("engine received %v, want %v", got, want)
	}
}

// TestControl_ResetBudget_RejectsAnUnreadableBody verifies a malformed request
// fails with a structured error rather than resetting some zero-valued gap: an
// empty entityId/gapColumn would otherwise reach the engine as a real request.
func TestControl_ResetBudget_RejectsAnUnreadableBody(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine(weaver.TargetSummary{TargetID: "t1"})
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
	if err := svc.StartNATSListener(ctx, nc); err != nil {
		t.Fatalf("start listener: %v", err)
	}

	msg, err := nc.Request(control.TargetSubject("t1", "resetBudget"), []byte("{not json"), 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	var resp control.ControlResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == "" {
		t.Fatal("a malformed resetBudget body must produce an error response")
	}
	if len(eng.budgetArgs()) != 0 {
		t.Fatalf("the engine must not be called for a malformed body, got %v", eng.budgetArgs())
	}
}

// TestControl_ReplayTarget verifies the "replayTarget" op invokes
// Engine.ReplayTarget for the target ID extracted from the subject and returns
// the engine's queued-row count verbatim — the operator's only feedback for how
// big a re-delivery burst the verb just ordered.
func TestControl_ReplayTarget(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	eng.replayQueued = 26
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.TargetSubject("t1", "replayTarget"))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.ReplayTarget)
	assert.Equal(t, 26, resp.ReplayTarget.RowsQueued)
	assert.Equal(t, []string{"replayTarget:t1"}, eng.callLog())
	// A replay re-delivers rows and nothing else: it must never be dispatched as
	// a disable, enable, or revoke on the way through.
	assert.Nil(t, resp.Disable)
	assert.Nil(t, resp.Enable)
	assert.Nil(t, resp.Revoke)
}

// TestControl_ReplayTarget_EngineError verifies a refusal — an unregistered or
// disabled target, or a consumer this instance does not manage — surfaces in
// Error rather than reporting a successful zero-row replay. The verb's whole
// value is that its refusals reach the operator: a refusal reported as success
// leaves the diagnostic that prompted the replay standing over a fact nothing
// re-derived.
func TestControl_ReplayTarget_EngineError(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	eng.errOn["replayTarget:ghost"] = errors.New("weaver: target \"ghost\" not registered")
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.TargetSubject("ghost", "replayTarget"))

	require.Nil(t, resp.ReplayTarget)
	assert.Contains(t, resp.Error, "not registered")
}

// TestControl_ReplayTarget_CapabilityDenied verifies the verb is gated by the
// same per-op capability check as every other mutating op — it makes Weaver
// re-dispatch a whole target's violating backlog, so an ungranted actor must
// never reach the engine.
func TestControl_ReplayTarget_CapabilityDenied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	svc := control.NewService(eng, denyCapability{err: errors.New("capability denied")}, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.TargetSubject("t1", "replayTarget"))

	assert.Contains(t, resp.Error, "capability denied")
	assert.Empty(t, eng.callLog(), "a denied replayTarget must never reach the engine")
}
