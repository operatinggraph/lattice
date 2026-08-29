package loom

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// sentinelInstanceID is a valid 20-character NanoID (Contract #1 alphabet) for
// an instance that was never created — the shape the control plane must be able
// to report as "no such instance" rather than as a read failure.
const sentinelInstanceID = "kkkkmmmmnnnnppppqqqq"

// The not-found sentinel is matchable, so the control plane can tell "there is
// no such instance" apart from a KV read failure without matching on message
// text. The negative control is the load-bearing half: an engine pointed at a
// bucket that does not exist must NOT report the sentinel, or a genuine
// infrastructure failure would be rendered to the operator as a clean
// "not found".
func TestInspectInstance_NotFoundIsMatchableSentinel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)

	_, err := e.InspectInstance(ctx, sentinelInstanceID)
	require.ErrorIs(t, err, ErrInstanceNotFound)
	require.Contains(t, err.Error(), sentinelInstanceID, "the message must still name the instance")

	require.ErrorIs(t, e.RedriveInstance(ctx, sentinelInstanceID), ErrInstanceNotFound)

	other := NewEngine(conn, Config{
		LoomStateBucket: "loom-state-never-provisioned",
		EventsStream:    "core-events",
		ActorKey:        "vtx.identity.LoomCtrlActor123",
		Logger:          controlTestLogger(),
	})
	_, err = other.InspectInstance(ctx, sentinelInstanceID)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrInstanceNotFound)
}
