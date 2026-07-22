package control_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/personalinterest"
)

// fakeHydrator is a test double for control.Hydrator: it records the
// identityID it was called with and returns a fixed (revision, err) pair.
type fakeHydrator struct {
	revision   uint64
	err        error
	calledWith []string
}

func (f *fakeHydrator) Hydrate(_ context.Context, identityID string) (uint64, error) {
	f.calledWith = append(f.calledWith, identityID)
	return f.revision, f.err
}

func TestControl_PersonalHydrate_NoHydratorConfigured_FailsClosed(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "identityA"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "hydrate"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.NotEmpty(t, resp.Error)
	assert.Nil(t, resp.PersonalHydrate)
}

func TestControl_PersonalHydrate_MissingIdentityID_Errors(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalHydrator(&fakeHydrator{revision: 100})
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{DeviceID: "deviceX"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "hydrate"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.NotEmpty(t, resp.Error, "identityId is required")
	assert.Nil(t, resp.PersonalHydrate)
}

func TestControl_PersonalHydrate_Success_ReturnsRevision(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	h := &fakeHydrator{revision: 10500}
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalHydrator(h)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "identityA"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "hydrate"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.PersonalHydrate)
	assert.True(t, resp.PersonalHydrate.Hydrated)
	assert.Equal(t, uint64(10500), resp.PersonalHydrate.Revision)
	assert.Equal(t, []string{"identityA"}, h.calledWith)
}

func TestControl_PersonalHydrate_HydratorError_Surfaces(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalHydrator(&fakeHydrator{err: errors.New("boom")})
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "identityA"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "hydrate"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.NotEmpty(t, resp.Error)
	assert.Nil(t, resp.PersonalHydrate)
}

// TestControl_PersonalHydrate_WithDeviceID_RecordsRevisionCursor proves the
// (deviceId, kv-configured) path best-effort records the resulting revision
// into the device's Interest Set doc (§3.5's reserved revisionCursor field)
// without disturbing its existing filter.
func TestControl_PersonalHydrate_WithDeviceID_RecordsRevisionCursor(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kv := makeKV(t, nc, js, "refractor-test-personal-interest-hydrate")
	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", []string{"lease"}, nil, "2026-07-06T00:00:00Z"))

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalHydrator(&fakeHydrator{revision: 20000})
	svc.SetPersonalInterestKV(kv)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "identityA", DeviceID: "deviceX"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "hydrate"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.PersonalHydrate)
	assert.Equal(t, uint64(20000), resp.PersonalHydrate.Revision)

	key, err := personalinterest.Key("identityA", "deviceX")
	require.NoError(t, err)
	entry, err := kv.Get(ctx, key)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	assert.Equal(t, float64(20000), doc["revisionCursor"])

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "payment", "payment.1")
	require.NoError(t, err)
	assert.False(t, relevant, "hydrate must not disturb the device's existing type filter")
}

func TestControl_PersonalHydrate_NoDeviceID_SkipsRevisionCursor(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kv := makeKV(t, nc, js, "refractor-test-personal-interest-hydrate-nodevice")
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalHydrator(&fakeHydrator{revision: 999})
	svc.SetPersonalInterestKV(kv)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "identityA"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "hydrate"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.PersonalHydrate)

	keys, err := kv.ListKeysPrefix(ctx, "identityA.")
	require.NoError(t, err)
	assert.Empty(t, keys, "no deviceId in the request must leave the Interest Set bucket untouched")
}
