package control_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/loom/control"
)

// recordingCapability records the actor argument of the last Authorize call
// and always allows — it proves the Service extracts and forwards the
// Lattice-Actor header, independent of any grant decision (Fire 1b).
type recordingCapability struct {
	mu   sync.Mutex
	last string
}

func (r *recordingCapability) Authorize(_ context.Context, actor, _, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = actor
	return nil
}

func (r *recordingCapability) actor() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// TestControl_List_ActorHeaderExtracted verifies handleExact (the exact-op
// path) forwards the Lattice-Actor request header instead of the
// pre-Fire-1a hardcoded "".
func TestControl_List_ActorHeaderExtracted(t *testing.T) {
	nc := startTestServer(t)
	rec := &recordingCapability{}
	startService(t, nc, newFakeEngine(), rec)

	_, err := nc.RequestMsg(controlauth.NewActorRequestMsg(control.ListSubject(), "vtx.identity.OPERATOR"), 2*time.Second)
	require.NoError(t, err)

	assert.Equal(t, "vtx.identity.OPERATOR", rec.actor())
}

// TestControl_Pause_ActorHeaderExtracted verifies dispatchEndpoint (the
// per-name path) forwards the header too.
func TestControl_Pause_ActorHeaderExtracted(t *testing.T) {
	nc := startTestServer(t)
	rec := &recordingCapability{}
	startService(t, nc, newFakeEngine(), rec)

	_, err := nc.RequestMsg(controlauth.NewActorRequestMsg(control.NameSubject("inst-1", "pause"), "vtx.identity.OPERATOR"), 2*time.Second)
	require.NoError(t, err)

	assert.Equal(t, "vtx.identity.OPERATOR", rec.actor())
}

// TestControl_List_NoHeaderExtractsEmptyActor verifies an anonymous request
// (no header, mirroring every pre-Fire-1a client) still extracts "" — zero
// behavior change while the checker stays a StubCapabilityChecker.
func TestControl_List_NoHeaderExtractsEmptyActor(t *testing.T) {
	nc := startTestServer(t)
	rec := &recordingCapability{}
	startService(t, nc, newFakeEngine(), rec)

	_ = sendRequest(t, nc, control.ListSubject())

	assert.Equal(t, "", rec.actor())
}
