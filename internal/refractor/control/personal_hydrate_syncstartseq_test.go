package control_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/control"
)

// bumpingHydrator is a test double for control.Hydrator whose Hydrate call
// mutates a shared counter before returning — used to prove the SYNC start
// seq is read before any hydrator runs, not during or after the fan-out.
type bumpingHydrator struct {
	revision uint64
	counter  *uint64
	bumpBy   uint64
}

func (h *bumpingHydrator) Hydrate(context.Context, string) (uint64, error) {
	atomic.AddUint64(h.counter, h.bumpBy)
	return h.revision, nil
}

// TestControl_PersonalHydrate_SyncStartSeq_ReadBeforeHydratorRuns proves the
// §3.2 ordering guarantee directly: the seam is snapshotted once, before the
// hydrator fan-out loop, so a hydrator that itself moves the SYNC stream
// forward cannot influence the value the op already captured.
func TestControl_PersonalHydrate_SyncStartSeq_ReadBeforeHydratorRuns(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	counter := uint64(5)
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetSyncLastSeq(func(context.Context) (uint64, error) { return atomic.LoadUint64(&counter), nil })
	// If the seam were read after (or during) the fan-out, this hydrator
	// would have already moved the counter to 1005 by the time it was read.
	svc.RegisterPersonalHydrator("rule-1", &bumpingHydrator{revision: 100, counter: &counter, bumpBy: 1000})
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
	assert.Equal(t, uint64(5), resp.PersonalHydrate.SyncStartSeq,
		"the seam must be read before the hydrator fan-out, not after — a later read would see 1005")
	assert.Equal(t, uint64(1005), atomic.LoadUint64(&counter), "the hydrator must still have run")
}

// TestControl_PersonalHydrate_SyncStartSeq_NilSeam_DegradesToZero proves the
// fail-soft posture: an unconfigured seam must not fail the hydrate — only
// the optimisation input degrades to 0 (today's DeliverAll fallback).
func TestControl_PersonalHydrate_SyncStartSeq_NilSeam_DegradesToZero(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	// SetSyncLastSeq deliberately not called.
	svc.RegisterPersonalHydrator("rule-1", &fakeHydrator{revision: 100})
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "identityA"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "hydrate"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.PersonalHydrate)
	assert.True(t, resp.PersonalHydrate.Hydrated, "hydration must still succeed with no seam configured")
	assert.Equal(t, uint64(0), resp.PersonalHydrate.SyncStartSeq)
}

// TestControl_PersonalHydrate_SyncStartSeq_SeamError_DegradesToZero proves
// the same fail-soft posture when the seam is configured but errors: the
// hydrate must still succeed, with SyncStartSeq degraded to 0.
func TestControl_PersonalHydrate_SyncStartSeq_SeamError_DegradesToZero(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetSyncLastSeq(func(context.Context) (uint64, error) { return 0, errors.New("stream boom") })
	svc.RegisterPersonalHydrator("rule-1", &fakeHydrator{revision: 100})
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "identityA"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "hydrate"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.PersonalHydrate)
	assert.True(t, resp.PersonalHydrate.Hydrated, "hydration must still succeed when the seam errors")
	assert.Equal(t, uint64(0), resp.PersonalHydrate.SyncStartSeq)
}

// TestControl_PersonalHydrate_SyncStartSeq_Success proves the happy path
// end to end and that Lenses is still populated as before — no regression
// from the new field.
func TestControl_PersonalHydrate_SyncStartSeq_Success(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetSyncLastSeq(func(context.Context) (uint64, error) { return 4242, nil })
	svc.RegisterPersonalHydrator("rule-1", &fakeHydrator{revision: 100})
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
	assert.Equal(t, uint64(4242), resp.PersonalHydrate.SyncStartSeq)
	assert.Equal(t, []string{"rule-1"}, resp.PersonalHydrate.Lenses, "Lenses must still be populated")
}
