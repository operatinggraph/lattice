package control_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/weaver/control"
)

// recordingCapability records the actor argument of the last Authorize call
// and always allows — it proves the Service extracts and forwards the
// Lattice-Actor header, independent of any grant decision (Fire 1b).
type recordingCapability struct {
	mu       sync.Mutex
	lastArgs [4]string // actor, op, targetID, "" (padding for future args)
}

func (r *recordingCapability) Authorize(_ context.Context, actor, op, targetID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastArgs = [4]string{actor, op, targetID, ""}
	return nil
}

func (r *recordingCapability) actor() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastArgs[0]
}

// TestControl_List_ActorHeaderExtracted verifies handleList forwards the
// Lattice-Actor request header to CapabilityChecker.Authorize instead of the
// pre-Fire-1a hardcoded "".
func TestControl_List_ActorHeaderExtracted(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	rec := &recordingCapability{}
	svc := control.NewService(newFakeEngine(), rec, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	reply, err := nc.RequestMsg(controlauth.NewActorRequestMsg(control.ListSubject(), "vtx.identity.OPERATOR"), 2*time.Second)
	require.NoError(t, err)
	_ = reply

	assert.Equal(t, "vtx.identity.OPERATOR", rec.actor())
}

// TestControl_Disable_ActorHeaderExtracted verifies dispatchEndpoint forwards
// the header on a mutating op too.
func TestControl_Disable_ActorHeaderExtracted(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	rec := &recordingCapability{}
	svc := control.NewService(newFakeEngine(), rec, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	_, err := nc.RequestMsg(controlauth.NewActorRequestMsg(control.TargetSubject("t1", "disable"), "vtx.identity.OPERATOR"), 2*time.Second)
	require.NoError(t, err)

	assert.Equal(t, "vtx.identity.OPERATOR", rec.actor())
}

// TestControl_List_NoHeaderExtractsEmptyActor verifies an anonymous request
// (no header, mirroring every pre-Fire-1a client) still extracts "" — zero
// behavior change while the checker stays a StubCapabilityChecker.
func TestControl_List_NoHeaderExtractsEmptyActor(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	rec := &recordingCapability{}
	svc := control.NewService(newFakeEngine(), rec, nil)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	_ = sendRequest(t, nc, control.ListSubject())

	assert.Equal(t, "", rec.actor())
}
