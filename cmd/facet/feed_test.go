package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestFeed_HydrationIsStickyAndReplayed pins the signal the renderer's boot
// gate waits on (facet-app-ux.md §10). edge/sync fires OnHydrationComplete on
// the cold path only, so a browser that connects after hydration — a reload
// onto a warm engine, or a second tab — would hear nothing at all and could
// not tell a finished world from one still catching up.
func TestFeed_HydrationIsStickyAndReplayed(t *testing.T) {
	fd := newFeed(nil)

	hydrated, rev := fd.hydrationState()
	require.False(t, hydrated, "a fresh engine has not hydrated")
	require.Zero(t, rev)

	ch := fd.subscribe()
	defer fd.unsubscribe(ch)
	fd.publishReady(42)

	select {
	case fr := <-ch:
		require.Equal(t, "ready", fr.Kind)
		require.Equal(t, uint64(42), fr.Revision)
	default:
		t.Fatal("publishReady published no live frame")
	}

	hydrated, rev = fd.hydrationState()
	require.True(t, hydrated, "hydration is sticky for the engine's lifetime")
	require.Equal(t, uint64(42), rev)
}

// TestFeed_MarkResumed pins the warm-resume half: an engine rebuilt over a
// store that already carries a cursor is past hydration by edge/sync's own
// cold-vs-warm test, and will never signal it again.
func TestFeed_MarkResumed(t *testing.T) {
	fd := newFeed(nil)
	ch := fd.subscribe()
	defer fd.unsubscribe(ch)

	fd.markResumed(7)

	hydrated, rev := fd.hydrationState()
	require.True(t, hydrated)
	require.Equal(t, uint64(7), rev)

	select {
	case fr := <-ch:
		t.Fatalf("markResumed must not broadcast — no tab is waiting on a past event: %+v", fr)
	default:
	}

	// A retention-gapped store re-hydrates; the later signal just re-marks.
	fd.publishReady(9)
	hydrated, rev = fd.hydrationState()
	require.True(t, hydrated)
	require.Equal(t, uint64(9), rev)
}

// TestWriteSSE_ReplaysReadyAfterTheSnapshot proves the replay a reconnecting
// browser gets, and its ORDER: `ready` releases the renderer's boot gate, so a
// ready ahead of the snapshot burst would paint an empty Home for the frame it
// takes the rows to land. An engine that has not hydrated replays no ready at
// all — that silence is what holds the gate on a fresh sign-in.
func TestWriteSSE_ReplaysReadyAfterTheSnapshot(t *testing.T) {
	// A cancelled request context makes this deterministic without a sleep:
	// writeSSE writes its whole replay before it ever reaches the stream loop,
	// then returns on the first select.
	serve := func(fd *feed, snapshot func() []frame) string {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest(http.MethodGet, "/api/feed", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		writeSSE(rec, req, slog.New(slog.NewTextHandler(io.Discard, nil)), fd, snapshot)
		return rec.Body.String()
	}
	oneRow := func() []frame {
		return []frame{{Kind: "manifest", Key: "vtx.unit.aaaaaaaaaaaaaaaaaaaa.manifest"}}
	}

	hydrating := newFeed(nil)
	body := serve(hydrating, oneRow)
	require.NotContains(t, body, "event: ready",
		"a still-hydrating engine promises nothing — the gate stays up")

	warm := newFeed(nil)
	warm.publishReady(11)
	body = serve(warm, oneRow)
	require.Contains(t, body, "event: ready")
	require.Less(t, strings.Index(body, "event: manifest"), strings.Index(body, "event: ready"),
		"ready must follow the snapshot burst it releases the gate for")
}
