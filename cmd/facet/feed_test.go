package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/edge/overlay"
	"github.com/operatinggraph/lattice/internal/edge/store"
	edgesync "github.com/operatinggraph/lattice/internal/edge/sync"
	"github.com/operatinggraph/lattice/internal/edge/transport"
)

// TestFeed_SyncDegradedTransitions pins the connectivity frame's second axis:
// transitions publish exactly once, every frame carries both sticky bits, and
// connectivityState exposes the pair a fresh SSE connection replays.
func TestFeed_SyncDegradedTransitions(t *testing.T) {
	fd := newFeed(nil)
	ch := fd.subscribe()
	defer fd.unsubscribe(ch)

	// publish is synchronous with the setter (same goroutine), so after a
	// setter returns the channel deterministically holds its frame or none.
	requireNoFrame := func() {
		t.Helper()
		select {
		case fr := <-ch:
			t.Fatalf("unexpected frame published: %+v", fr)
		default:
		}
	}
	nextFrame := func() frame {
		t.Helper()
		select {
		case fr := <-ch:
			return fr
		default:
			t.Fatal("expected a frame, none published")
			return frame{}
		}
	}

	connected, degraded := fd.connectivityState()
	require.True(t, connected, "newFeed starts connected (post-dial optimism)")
	require.False(t, degraded)

	fd.setSyncDegraded(true)
	fr := nextFrame()
	require.Equal(t, "connectivity", fr.Kind)
	require.True(t, fr.Connected, "a sync wedge is not a socket outage")
	require.True(t, fr.SyncDegraded)

	fd.setSyncDegraded(true)
	requireNoFrame() // every failed Run re-marks; only transitions broadcast

	fd.setConnected(false)
	fr = nextFrame()
	require.False(t, fr.Connected)
	require.True(t, fr.SyncDegraded, "a socket drop must not erase the degraded axis")

	fd.setSyncDegraded(false)
	fr = nextFrame()
	require.False(t, fr.Connected)
	require.False(t, fr.SyncDegraded)

	connected, degraded = fd.connectivityState()
	require.False(t, connected)
	require.False(t, degraded)
}

// TestFeed_SnapshotManifestFrames_ExcludesRetractedRows proves a fresh SSE
// connection's snapshot burst (server.go's handleFeed) never replays a
// tombstoned manifest row — whether tombstoned by an explicit delete or by a
// Personal Lens keyset frame retracting its last attribution (personal-lens-
// retraction-design.md §3.3) — the same posture internal/edge/browser.Host's
// Snapshot already held. A live row still replays.
func TestFeed_SnapshotManifestFrames_ExcludesRetractedRows(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/edge.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	ov := overlay.New(st)

	liveKey := "manifest.task.livelenslivelenslive"
	deletedKey := "manifest.task.deletedkeydeletedkey"
	retractedKey := "manifest.task.retractedretractedret"

	_, err = st.ApplyUpsert(liveKey, "", 1, []byte(`{"a":1}`))
	require.NoError(t, err)
	_, err = st.ApplyUpsert(deletedKey, "", 1, []byte(`{"a":1}`))
	require.NoError(t, err)
	_, err = st.ApplyDelete(deletedKey, 2)
	require.NoError(t, err)
	_, err = st.ApplyUpsert(retractedKey, "lensQueued", 1, []byte(`{"a":1}`))
	require.NoError(t, err)
	_, _, err = st.ApplyKeySet("lensQueued", 5, nil)
	require.NoError(t, err)

	fd := newFeed(nil)
	frames, err := fd.snapshotManifestFrames(st, ov)
	require.NoError(t, err)

	keys := make([]string, 0, len(frames))
	for _, fr := range frames {
		keys = append(keys, fr.Key)
	}
	require.Equal(t, []string{liveKey}, keys, "only the live row belongs in a fresh snapshot")
}

// deadControlTransport fails every control RPC — a Manager built over it
// wedges in ensureFresh exactly like a controlauth denial on
// personal.syncgap does on a live stack.
type deadControlTransport struct{}

func (deadControlTransport) RunDurableConsumer(context.Context, transport.ConsumerConfig, transport.Handler) error {
	panic("deadControlTransport: ensureFresh never passes, the consumer is unreachable")
}

func (deadControlTransport) Request(context.Context, string, []byte, string) ([]byte, error) {
	return nil, errors.New("control plane down")
}

// degradedRecorder observes runSyncLoop's marking without a full feed. The
// send is non-blocking so a slow test runner's extra retry cycles can never
// wedge the loop goroutine on an unread channel.
type degradedRecorder struct{ ch chan bool }

func (r degradedRecorder) setSyncDegraded(degraded bool) {
	select {
	case r.ch <- degraded:
	default:
	}
}

// TestRunSyncLoop_MarksDegradedOnRunError pins the wedge signal: a sync
// manager that cannot get past its freshness gate marks the feed degraded on
// the failed attempt instead of only logging a WARN.
func TestRunSyncLoop_MarksDegradedOnRunError(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/edge.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr, err := edgesync.New(deadControlTransport{}, st, edgesync.Config{
		IdentityID: "identityA",
		DeviceID:   "deviceX",
		Logger:     quiet,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	rec := degradedRecorder{ch: make(chan bool, 1)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSyncLoop(ctx, mgr, rec, "identityA", quiet)
	}()

	require.True(t, <-rec.ch, "first failed Run must mark sync degraded")
	cancel()
	<-done
}
