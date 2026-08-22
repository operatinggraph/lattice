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

// TestControl_PersonalHydrate_SyncEndSeq_ReadAfterHydratorRuns proves the
// §3.1 ordering guarantee directly: the end-seq seam is read a second time
// after the hydrator fan-out loop has returned, so a hydrator that itself
// moves the SYNC stream forward is reflected in SyncEndSeq but not in the
// SyncStartSeq already captured before the loop. This must fail if the
// second read is moved above the loop.
func TestControl_PersonalHydrate_SyncEndSeq_ReadAfterHydratorRuns(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	counter := uint64(5)
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetSyncLastSeq(func(context.Context, string) (uint64, error) {
		return atomic.LoadUint64(&counter), nil
	})
	// The hydrator moves the seam forward by 1000 while it runs. If the end
	// read happened before (or during) the fan-out, SyncEndSeq would equal
	// SyncStartSeq instead of reflecting the bump.
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
		"the start seam must still be read before the fan-out")
	assert.Equal(t, uint64(1005), resp.PersonalHydrate.SyncEndSeq,
		"the end seam must be read after the fan-out, observing the hydrator's own bump")
}

// TestControl_PersonalHydrate_SyncEndSeq_NilSeam_DegradesToZero proves the
// fail-soft posture: an unconfigured seam must not fail the hydrate — only
// the optimisation input degrades to 0, and the client falls back to its
// degraded gate.
func TestControl_PersonalHydrate_SyncEndSeq_NilSeam_DegradesToZero(t *testing.T) {
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
	assert.Equal(t, uint64(0), resp.PersonalHydrate.SyncEndSeq)
}

// TestControl_PersonalHydrate_SyncEndSeq_SeamError_DegradesToZero proves the
// same fail-soft posture when the seam is configured but errors: the
// hydrate must still succeed, with SyncEndSeq degraded to 0.
func TestControl_PersonalHydrate_SyncEndSeq_SeamError_DegradesToZero(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetSyncLastSeq(func(context.Context, string) (uint64, error) { return 0, errors.New("stream boom") })
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
	assert.Equal(t, uint64(0), resp.PersonalHydrate.SyncEndSeq)
}

// TestControl_PersonalHydrate_SyncEndSeq_NotFoundSubject_DegradesToZero
// proves the cold-identity case: an identity with no frames yet on its own
// personal SYNC subject is not a seam error, so hydration succeeds and
// SyncEndSeq is 0.
func TestControl_PersonalHydrate_SyncEndSeq_NotFoundSubject_DegradesToZero(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetSyncLastSeq(func(context.Context, string) (uint64, error) { return 0, nil })
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
	assert.True(t, resp.PersonalHydrate.Hydrated, "a cold identity with no sync frames yet must still hydrate")
	assert.Equal(t, uint64(0), resp.PersonalHydrate.SyncEndSeq)
}

// TestControl_PersonalHydrate_SyncEndSeq_Success proves the happy path end
// to end: the value the seam returns lands on the result.
func TestControl_PersonalHydrate_SyncEndSeq_Success(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetSyncLastSeq(func(context.Context, string) (uint64, error) { return 4242, nil })
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
	assert.Equal(t, uint64(4242), resp.PersonalHydrate.SyncEndSeq)
}

// TestControl_PersonalHydrate_StartAndEndSeq_BothPresent_Unswapped proves
// the two fields are not transposed: the seam returns a different value on
// its first call (the start read) than on its second (the end read), and
// each value must land on its own field.
func TestControl_PersonalHydrate_StartAndEndSeq_BothPresent_Unswapped(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var calls uint64
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetSyncLastSeq(func(context.Context, string) (uint64, error) {
		if atomic.AddUint64(&calls, 1) == 1 {
			return 111, nil
		}
		return 999, nil
	})
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
	assert.Equal(t, uint64(111), resp.PersonalHydrate.SyncStartSeq, "the first seam read must land on SyncStartSeq")
	assert.Equal(t, uint64(999), resp.PersonalHydrate.SyncEndSeq, "the second seam read must land on SyncEndSeq")
}
