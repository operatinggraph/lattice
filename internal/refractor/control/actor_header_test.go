package control_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/refractor/control"
)

// recordingCapability records the actor argument of the last Authorize call
// and always allows — it proves dispatchEndpoint now calls the capability
// checker at all (pre-Fire-1a, refractor's control.Service had no capability
// field and never called one) and that it forwards the Lattice-Actor header.
type recordingCapability struct {
	mu    sync.Mutex
	last  string
	calls int
}

func (r *recordingCapability) Authorize(_ context.Context, actor, _, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = actor
	r.calls++
	return nil
}

func (r *recordingCapability) actor() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func (r *recordingCapability) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// TestControl_Health_ActorHeaderExtracted verifies dispatchEndpoint now calls
// the capability checker (previously never wired at all) and forwards the
// Lattice-Actor request header.
func TestControl_Health_ActorHeaderExtracted(t *testing.T) {
	nc, _ := startControlTestServerConn(t)

	svc := control.NewService()
	rec := &recordingCapability{}
	svc.SetCapabilityChecker(rec)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	subj := control.ControlSubject("rule-actor-test", "health")
	_, err := nc.RequestMsg(controlauth.NewActorRequestMsg(subj, "vtx.identity.OPERATOR"), 2*time.Second)
	require.NoError(t, err)

	assert.Equal(t, 1, rec.callCount(), "dispatchEndpoint must call the capability checker exactly once")
	assert.Equal(t, "vtx.identity.OPERATOR", rec.actor())
}

// TestControl_Health_NoHeaderExtractsEmptyActor verifies an anonymous request
// (no header) still extracts "" and — under the default
// StubCapabilityChecker — still succeeds (zero behavior change).
func TestControl_Health_NoHeaderExtractsEmptyActor(t *testing.T) {
	nc, _ := startControlTestServerConn(t)

	svc := control.NewService()
	rec := &recordingCapability{}
	svc.SetCapabilityChecker(rec)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	subj := control.ControlSubject("rule-actor-test", "health")
	_, err := nc.Request(subj, nil, 2*time.Second)
	require.NoError(t, err)

	assert.Equal(t, "", rec.actor())
}

// TestControl_SetCapabilityChecker_NilResetsToFailClosed verifies
// SetCapabilityChecker(nil) falls back to the fail-closed denyAllChecker
// (deny every op) rather than leaving a nil checker that would panic
// dispatchEndpoint's unconditional call, and rather than resetting to an
// allow-all: a cleared checker must fail closed, never open.
func TestControl_SetCapabilityChecker_NilResetsToFailClosed(t *testing.T) {
	nc, _ := startControlTestServerConn(t)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil)) // start allow-all
	svc.SetCapabilityChecker(nil)                                   // reset must go fail-closed, not back to allow

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	subj := control.ControlSubject("rule-actor-test", "health")
	reply, err := nc.Request(subj, nil, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	assert.NotEmpty(t, resp.Error, "SetCapabilityChecker(nil) must reset to a fail-closed deny, not allow-all")
}

// TestControl_ConstructionDefault_FailsClosed verifies the construction-time
// default fails CLOSED: a Service that never has SetCapabilityChecker(real)
// called denies every op, so the pre-set window / a dropped wiring call cannot
// fail open. Production sets the real checker before serving (cmd/refractor).
func TestControl_ConstructionDefault_FailsClosed(t *testing.T) {
	nc, _ := startControlTestServerConn(t)

	svc := control.NewService() // no SetCapabilityChecker → fail-closed default

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	subj := control.ControlSubject("rule-actor-test", "health")
	reply, err := nc.Request(subj, nil, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	assert.NotEmpty(t, resp.Error, "the construction-time default must deny (fail closed) until the real checker is set")
}
