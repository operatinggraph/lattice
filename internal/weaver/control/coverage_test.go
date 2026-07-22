package control_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/weaver"
	"github.com/asolgan/lattice/internal/weaver/control"
)

// denyCapability is a CapabilityChecker that denies every call with a fixed
// error — the inverse of the production StubCapabilityChecker — so the
// control plane's authorize-before-dispatch boundary can be exercised.
type denyCapability struct{ err error }

func (d denyCapability) Authorize(_ context.Context, _, _, _ string) error { return d.err }

// listErrEngine satisfies engineControl with a ListTargets that always errors,
// covering handleList's engine-error branch (fakeEngine's ListTargets never
// fails). The mutating ops are unused here and return nil.
type listErrEngine struct{ err error }

func (e listErrEngine) ListTargets(context.Context) ([]weaver.TargetSummary, error) {
	return nil, e.err
}
func (e listErrEngine) Disable(context.Context, string) error { return nil }
func (e listErrEngine) Enable(context.Context, string) error  { return nil }
func (e listErrEngine) Revoke(context.Context, string) error  { return nil }
func (e listErrEngine) ResetConfidence(context.Context, string) (int, error) {
	return 0, nil
}

// TestControl_List_CapabilityDenied verifies a denied "list" op surfaces the
// authorizer's error and never reaches the engine.
func TestControl_List_CapabilityDenied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine(weaver.TargetSummary{TargetID: "t1"})
	svc := control.NewService(eng, denyCapability{err: errors.New("capability denied")}, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.ListSubject())

	assert.Contains(t, resp.Error, "capability denied")
	assert.Nil(t, resp.Targets)
}

// TestControl_Disable_CapabilityDenied verifies a denied mutating op surfaces
// the authorizer's error and — critically — never invokes the engine, so an
// unauthorized actor cannot effect a state change.
func TestControl_Disable_CapabilityDenied(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	svc := control.NewService(eng, denyCapability{err: errors.New("capability denied")}, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.TargetSubject("t1", "disable"))

	assert.Contains(t, resp.Error, "capability denied")
	assert.Nil(t, resp.Disable)
	assert.Empty(t, eng.callLog(), "engine must not be invoked when authorization is denied")
}

// TestControl_List_EngineError verifies an error from Engine.ListTargets
// surfaces in the response's Error field.
func TestControl_List_EngineError(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService(listErrEngine{err: errors.New("kv unavailable")}, control.NewStubCapabilityChecker(nil), nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.ListSubject())

	assert.Contains(t, resp.Error, "kv unavailable")
	assert.Nil(t, resp.Targets)
}

// TestTargetIDFromSubject table-tests the defensive subject parser directly:
// the wildcard endpoint can only route a conforming 5-token subject, so the
// deviation branches are otherwise unreachable. Guards the Contract #1 control
// subject shape lattice.ctrl.weaver.<targetId>.<op>.
func TestTargetIDFromSubject(t *testing.T) {
	cases := []struct {
		name       string
		subject    string
		wantTarget string
		wantOK     bool
	}{
		{"valid", "lattice.ctrl.weaver.t1.disable", "t1", true},
		{"too few tokens", "lattice.ctrl.weaver.t1", "", false},
		{"too many tokens", "lattice.ctrl.weaver.t1.disable.extra", "", false},
		{"wrong root", "other.ctrl.weaver.t1.disable", "", false},
		{"wrong segment 2", "lattice.data.weaver.t1.disable", "", false},
		{"wrong component", "lattice.ctrl.refractor.t1.disable", "", false},
		{"empty target", "lattice.ctrl.weaver..disable", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := control.TargetIDFromSubject(tc.subject)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantTarget, got)
		})
	}
}

// TestControl_NilCapability_FailsClosed proves the nil-checker default fails
// CLOSED: NewService(engine, nil, ...) denies every op and never reaches the
// engine, so a wiring regression that drops the real checker cannot fail open.
func TestControl_NilCapability_FailsClosed(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine(weaver.TargetSummary{TargetID: "t1"})
	svc := control.NewService(eng, nil, nil) // nil checker → fail-closed denyAllChecker
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	// list is denied.
	listResp := sendRequest(t, nc, control.ListSubject())
	assert.NotEmpty(t, listResp.Error, "nil-checker default must deny the list op")
	assert.Nil(t, listResp.Targets)

	// a mutating op is denied AND never reaches the engine.
	disableResp := sendRequest(t, nc, control.TargetSubject("t1", "disable"))
	assert.NotEmpty(t, disableResp.Error, "nil-checker default must deny the disable op")
	assert.Nil(t, disableResp.Disable)
	assert.Empty(t, eng.callLog(), "engine must not be invoked when the fail-closed default denies")
}

// TestControl_ExplicitStub_Allows proves the opt-in escape hatch still works:
// an explicit StubCapabilityChecker allows every op (the dev/test allow-all
// path), distinct from the nil default which now denies.
func TestControl_ExplicitStub_Allows(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	eng := newFakeEngine()
	svc := control.NewService(eng, control.NewStubCapabilityChecker(nil), nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	resp := sendRequest(t, nc, control.TargetSubject("t1", "disable"))
	require.Empty(t, resp.Error, "explicit StubCapabilityChecker must allow")
	require.NotNil(t, resp.Disable)
	assert.True(t, resp.Disable.Disabled)
	assert.Equal(t, []string{"disable:t1"}, eng.callLog())
}

// TestNewStubCapabilityChecker_NilLogger verifies the nil-logger fallback and
// that the stub authorizes (allow-all is its documented behaviour).
func TestNewStubCapabilityChecker_NilLogger(t *testing.T) {
	c := control.NewStubCapabilityChecker(nil)
	require.NotNil(t, c)
	require.NotNil(t, c.Logger)
	assert.NoError(t, c.Authorize(context.Background(), "actor", "list", "t1"))

	// An explicit logger is retained.
	l := slog.Default()
	assert.Same(t, l, control.NewStubCapabilityChecker(l).Logger)
}
