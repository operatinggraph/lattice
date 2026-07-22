package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/edge/transport"
	"github.com/operatinggraph/lattice/internal/refractor/control/controlwire"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// establishedTransport extends fakeControlTransport with a RunDurableConsumer
// that records the call and returns, so Run's full path — ensureFresh →
// OnRunEstablished → durable consumer — is drivable without a live server.
type establishedTransport struct {
	*fakeControlTransport
	events *[]string
}

func (f *establishedTransport) RunDurableConsumer(context.Context, transport.ConsumerConfig, transport.Handler) error {
	*f.events = append(*f.events, "consumer")
	return nil
}

func newEstablishedManager(t *testing.T, tr Transport, cursor uint64, haveCursor bool, events *[]string) *Manager {
	t.Helper()
	st := openTestStore(t)
	if haveCursor {
		require.NoError(t, st.SetCursor(cursor))
	}
	mgr, err := New(tr, st, Config{
		IdentityID: "identityA",
		DeviceID:   "deviceX",
		Logger:     testutil.TestLogger(),
		OnRunEstablished: func() {
			*events = append(*events, "established")
		},
	})
	require.NoError(t, err)
	return mgr
}

// The warm-resume path (cursor present, no gap) never delivers a
// hydrationComplete delta, so OnRunEstablished is the only signal a host's
// degraded-sync indicator can clear on — it must fire after ensureFresh
// passes and before the durable consumer starts.
func TestManager_Run_OnRunEstablishedFiresOnWarmResume(t *testing.T) {
	var events []string
	tr := &establishedTransport{
		fakeControlTransport: &fakeControlTransport{reply: happyControlReply(false)},
		events:               &events,
	}
	mgr := newEstablishedManager(t, tr, 7, true, &events)

	require.NoError(t, mgr.Run(context.Background()))
	require.Equal(t, []string{"established", "consumer"}, events)
}

func TestManager_Run_OnRunEstablishedFiresOnColdStart(t *testing.T) {
	var events []string
	tr := &establishedTransport{
		fakeControlTransport: &fakeControlTransport{reply: happyControlReply(false)},
		events:               &events,
	}
	mgr := newEstablishedManager(t, tr, 0, false, &events)

	require.NoError(t, mgr.Run(context.Background()))
	require.Equal(t, []string{"established", "consumer"}, events)
}

// A Run that wedges in ensureFresh (the syncgap fail-closed path) must NOT
// report established — the callback firing here would clear a host's
// degraded indicator while sync is still down.
func TestManager_Run_OnRunEstablishedAbsentWhenEnsureFreshFails(t *testing.T) {
	var events []string
	happy := happyControlReply(false)
	tr := &establishedTransport{
		fakeControlTransport: &fakeControlTransport{reply: func(op string, body controlwire.ControlRequest) (controlwire.ControlResponse, error) {
			if op == "syncgap" {
				return controlwire.ControlResponse{}, errors.New("control plane down")
			}
			return happy(op, body)
		}},
		events: &events,
	}
	mgr := newEstablishedManager(t, tr, 7, true, &events)

	err := mgr.Run(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "ensure fresh")
	require.Empty(t, events)
}
