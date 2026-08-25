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
// it implements ListTargets/Disable/Enable/Revoke/ResetConfidence/ResetRetryBudget with the exact same
// signatures as *weaver.Engine, so control.NewService accepts it. No real
// *weaver.Engine is needed for this package's tests (internal/weaver's own
// tests cover the real engine wiring, per Task 3).
type fakeEngine struct {
	mu      sync.Mutex
	targets []weaver.TargetSummary
	calls   []string // op:targetID, in call order
	errOn   map[string]error
	// resetDeleted is the window count ResetConfidence reports on success.
	resetDeleted int
	// resetCleared/resetFound are what ResetRetryBudget reports on success.
	resetCleared int
	resetFound   bool
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

func (f *fakeEngine) ResetRetryBudget(_ context.Context, targetID, entityID, gapColumn string) (int, bool, error) {
	if err := f.record("resetBudget", targetID+":"+entityID+":"+gapColumn); err != nil {
		return 0, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resetCleared, f.resetFound, nil
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

// nanoID is a 20-character Contract #1 NanoID for the resetBudget subject's
// entityId token — the shape the responder whitelists before the engine.
const nanoID = "abcdefghijkmnpqrstuv"

// TestControl_ResetBudget verifies the "resetBudget" op parses all three scope
// tokens off its 7-token subject, invokes Engine.ResetRetryBudget with them, and
// returns the engine's answer verbatim. The entity and gap ride in the SUBJECT,
// not in a request body: NATS permissions can scope a subject and can never
// scope a body, so a scope carried in a body would be invisible to the
// transport's own authorization layer.
func TestControl_ResetBudget(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	eng.resetCleared, eng.resetFound = 3, true
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.ResetBudgetSubject("t1", nanoID, "missing_x"))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.ResetBudget)
	assert.Equal(t, 3, resp.ResetBudget.ClearedCount)
	assert.True(t, resp.ResetBudget.Found)
	assert.Equal(t, []string{"resetBudget:t1:" + nanoID + ":missing_x"}, eng.callLog())
	// A budget reset is exactly that: it must never be dispatched as another op
	// on the way through.
	assert.Nil(t, resp.Disable)
	assert.Nil(t, resp.Enable)
	assert.Nil(t, resp.Revoke)
	assert.Nil(t, resp.ResetConfidence)
}

// TestControl_ResetBudget_NotFoundIsASuccess pins the absence answer reaching
// the operator intact: a gap that was never parked reports found=false with no
// error, which is what lets the CLI say "nothing to reset" instead of claiming a
// park was released.
func TestControl_ResetBudget_NotFoundIsASuccess(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	eng.resetCleared, eng.resetFound = 0, false
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.ResetBudgetSubject("t1", nanoID, "missing_x"))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.ResetBudget)
	assert.False(t, resp.ResetBudget.Found)
	assert.Equal(t, 0, resp.ResetBudget.ClearedCount)
}

// TestControl_ResetBudget_EngineError verifies an engine refusal (an
// unregistered target, a revision conflict against a racing dispatch) surfaces
// in Error rather than as a successful zero reset.
func TestControl_ResetBudget_EngineError(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	eng.errOn["resetBudget:ghost:"+nanoID+":missing_x"] = errors.New(`weaver: target "ghost" not registered`)
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.ResetBudgetSubject("ghost", nanoID, "missing_x"))

	require.Nil(t, resp.ResetBudget)
	assert.Contains(t, resp.Error, "not registered")
}

// TestControl_ResetBudget_CapabilityDenied verifies the verb is gated by its own
// per-op capability check. It deletes nothing, but the reconciler DISPATCHES the
// gap it releases — so an ungranted actor reaching it would make the platform
// act on the world.
func TestControl_ResetBudget_CapabilityDenied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	svc := control.NewService(eng, denyCapability{err: errors.New("capability denied")}, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.ResetBudgetSubject("t1", nanoID, "missing_x"))

	assert.Contains(t, resp.Error, "capability denied")
	assert.Empty(t, eng.callLog(), "a denied resetBudget must never reach the engine")
}

// TestControl_ResetBudget_MalformedScopeNeverReachesTheEngine is the responder's
// half of the fail-closed whitelist. Both extra tokens are publisher-supplied
// and concatenate into a weaver-state key, so a shape outside the two the key is
// ever built from is refused at the boundary — the engine is not consulted and
// no KV round-trip happens at all.
func TestControl_ResetBudget_MalformedScopeNeverReachesTheEngine(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	eng.resetCleared, eng.resetFound = 9, true
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	for _, tc := range []struct{ name, entityID, gapColumn string }{
		{"entityId is not a NanoID", "short", "missing_x"},
		{"entityId is a reserved marker", "__control", "missing_x"},
		{"gapColumn without the missing_ prefix", nanoID, "notagap"},
		{"gapColumn is a reserved marker", nanoID, "__count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := sendRequest(t, nc, control.ResetBudgetSubject("t1", tc.entityID, tc.gapColumn))
			assert.Nil(t, resp.ResetBudget)
			assert.Contains(t, resp.Error, "invalid resetBudget scope")
		})
	}
	assert.Empty(t, eng.callLog(), "a malformed scope must never reach the engine")
}

// TestControl_ResetBudget_SubjectShapeIsItsOwnGrammar pins the two halves of the
// subject-grammar decision. resetBudget's 7-token subject must NOT be accepted
// by targetIDFromSubject — that parser's exact-5-token rule serves
// disable/enable/revoke/resetConfidence, and loosening it to admit a longer
// subject would silently widen every one of them. And a 5-token resetBudget
// subject must reach no endpoint, so the scope cannot be omitted.
func TestControl_ResetBudget_SubjectShapeIsItsOwnGrammar(t *testing.T) {
	_, ok := control.TargetIDFromSubject(control.ResetBudgetSubject("t1", nanoID, "missing_x"))
	assert.False(t, ok, "the 7-token resetBudget subject must not parse as a per-target subject")

	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := newFakeEngine()
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	_, err := nc.Request(control.TargetSubject("t1", "resetBudget"), nil, 250*time.Millisecond)
	require.Error(t, err, "a scopeless resetBudget subject must match no endpoint")
	assert.Empty(t, eng.callLog())
}

// TestResetBudgetScopeFromSubject table-tests the defensive 7-token parser
// directly: the wildcard endpoint can only route a conforming subject with
// non-empty tokens, so the deviation branches are otherwise unreachable.
func TestResetBudgetScopeFromSubject(t *testing.T) {
	cases := []struct {
		name                            string
		subject                         string
		wantTarget, wantEntity, wantGap string
		wantOK                          bool
	}{
		{"valid", "lattice.ctrl.weaver.t1.resetBudget." + nanoID + ".missing_x", "t1", nanoID, "missing_x", true},
		{"too few tokens", "lattice.ctrl.weaver.t1.resetBudget." + nanoID, "", "", "", false},
		{"too many tokens", "lattice.ctrl.weaver.t1.resetBudget." + nanoID + ".missing_x.extra", "", "", "", false},
		{"wrong root", "other.ctrl.weaver.t1.resetBudget." + nanoID + ".missing_x", "", "", "", false},
		{"wrong segment 2", "lattice.data.weaver.t1.resetBudget." + nanoID + ".missing_x", "", "", "", false},
		{"wrong component", "lattice.ctrl.refractor.t1.resetBudget." + nanoID + ".missing_x", "", "", "", false},
		{"wrong op", "lattice.ctrl.weaver.t1.resetConfidence." + nanoID + ".missing_x", "", "", "", false},
		{"empty target", "lattice.ctrl.weaver..resetBudget." + nanoID + ".missing_x", "", "", "", false},
		{"empty entity", "lattice.ctrl.weaver.t1.resetBudget..missing_x", "", "", "", false},
		{"empty gap", "lattice.ctrl.weaver.t1.resetBudget." + nanoID + ".", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, entity, gap, ok := control.ResetBudgetScopeFromSubject(tc.subject)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantTarget, target)
			assert.Equal(t, tc.wantEntity, entity)
			assert.Equal(t, tc.wantGap, gap)
		})
	}
}
