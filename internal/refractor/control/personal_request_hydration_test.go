package control_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/personalinterest"
)

func TestControl_PersonalRequestHydration_NoKVConfigured_FailsClosed(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "identityA", DeviceID: "deviceX"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "requesthydration"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.NotEmpty(t, resp.Error)
	assert.Nil(t, resp.PersonalRequestHydration)
}

func TestControl_PersonalRequestHydration_MissingFields_Errors(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kv := makeKV(t, nc, js, "refractor-test-personal-interest-reqhydrate-missing")
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalInterestKV(kv)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{DeviceID: "deviceX"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "requesthydration"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.NotEmpty(t, resp.Error, "identityId is required")
	assert.Nil(t, resp.PersonalRequestHydration)
}

func TestControl_PersonalRequestHydration_UnregisteredDevice_Errors(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kv := makeKV(t, nc, js, "refractor-test-personal-interest-reqhydrate-unregistered")
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalInterestKV(kv)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "identityA", DeviceID: "deviceX"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "requesthydration"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.Contains(t, resp.Error, "not registered", "a phantom entry for a device that never registered must not be created")
	assert.Nil(t, resp.PersonalRequestHydration)
}

// TestControl_PersonalRequestHydration_MarksThenSyncGapSurfacesIt proves the
// full loop this op exists for (loupe-flows-edge-depth-ux.md §3.2): an
// operator's requesthydration call durably marks a registered device, and the
// device's own next syncgap round trip (the warm-resume attach check) sees
// it — even when the cursor itself is not gapped.
func TestControl_PersonalRequestHydration_MarksThenSyncGapSurfacesIt(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kv := makeKV(t, nc, js, "refractor-test-personal-interest-reqhydrate-loop")
	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", nil, nil, "2026-07-30T00:00:00Z"))

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalInterestKV(kv)
	svc.SetSyncFirstSeq(func(context.Context) (uint64, error) { return 1, nil }) // any cursor >= 1 is not gapped
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	reqData, err := json.Marshal(control.ControlRequest{IdentityID: "identityA", DeviceID: "deviceX"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "requesthydration"), reqData, 2*time.Second)
	require.NoError(t, err)
	var reqResp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &reqResp))
	require.Empty(t, reqResp.Error)
	require.NotNil(t, reqResp.PersonalRequestHydration)
	assert.True(t, reqResp.PersonalRequestHydration.Requested)

	gapData, err := json.Marshal(control.ControlRequest{IdentityID: "identityA", DeviceID: "deviceX", Cursor: 100})
	require.NoError(t, err)
	reply, err = nc.Request(control.ControlSubject("personal", "syncgap"), gapData, 2*time.Second)
	require.NoError(t, err)
	var gapResp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &gapResp))
	require.Empty(t, gapResp.Error)
	require.NotNil(t, gapResp.PersonalSyncGap)
	assert.False(t, gapResp.PersonalSyncGap.Gapped, "cursor 100 >= firstSeq 1 is not gapped")
	assert.True(t, gapResp.PersonalSyncGap.HydrationRequested, "the pending operator request must surface on the device's next syncgap check")
}

// TestControl_PersonalRequestHydration_ClearedBySetRevisionCursor proves a
// completed hydrate consumes the request: SetRevisionCursor (called by
// personalHydrate on success) clears HydrationRequestedAt, so a later
// syncgap no longer reports it — otherwise a hydrated device would loop,
// re-hydrating forever on every subsequent warm-resume attach.
func TestControl_PersonalRequestHydration_ClearedBySetRevisionCursor(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kv := makeKV(t, nc, js, "refractor-test-personal-interest-reqhydrate-cleared")
	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", nil, nil, "2026-07-30T00:00:00Z"))
	require.NoError(t, personalinterest.RequestHydration(ctx, kv, "identityA", "deviceX", "2026-07-30T01:00:00Z"))

	before, err := personalinterest.HydrationRequested(ctx, kv, "identityA", "deviceX")
	require.NoError(t, err)
	require.True(t, before, "sanity: the request must be visible before the hydrate")

	require.NoError(t, personalinterest.SetRevisionCursor(ctx, kv, "identityA", "deviceX", 7, "2026-07-30T02:00:00Z"))

	after, err := personalinterest.HydrationRequested(ctx, kv, "identityA", "deviceX")
	require.NoError(t, err)
	assert.False(t, after, "a completed hydrate (SetRevisionCursor) must clear the pending request")
}
