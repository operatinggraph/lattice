package consumer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestBootstrapper_AppliedSeq_ZeroBeforeAnythingIsApplied pins the refusing
// reading. A reader compares its own ordering token against this cursor and
// must not act on an unknown one, so a bootstrapper that has applied nothing
// and polled nothing answers 0 rather than something that looks like progress.
func TestBootstrapper_AppliedSeq_ZeroBeforeAnythingIsApplied(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx := context.Background()
	js, nc := startJS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-seq-cold"})
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "adj-seq-cold")
	require.NoError(t, err)

	b := consumer.NewBootstrapper(conn, "core-seq-cold", adjKV)
	require.Equal(t, uint64(0), b.AppliedSeq(),
		"a bootstrapper that has neither applied a message nor completed a poll has measured nothing")
}

// TestBootstrapper_AppliedSeq_TracksTheStreamHead is the cursor's two sources,
// one per path Ready itself has:
//
//   - the CAUGHT-UP POLL, which is the only source on a stream the handler
//     never runs against. An empty stream leaves the cursor at the head it
//     read, which is the honest answer for an index that reflects everything
//     the stream holds; the same path is what gives a restart against an
//     already-drained durable a non-zero cursor.
//   - the HANDLER, which stamps each retired message's sequence, so a
//     bootstrapper that drains a backlog ends at the last message's sequence.
//
// Both are asserted against the stream's own last sequence, so the test says
// "caught up TO WHAT" rather than merely "moved".
func TestBootstrapper_AppliedSeq_TracksTheStreamHead(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	js, nc := startJS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	t.Run("an empty stream's cursor comes from the caught-up poll", func(t *testing.T) {
		_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-seq-empty"})
		require.NoError(t, err)
		_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-seq-empty"})
		require.NoError(t, err)
		adjKV, err := conn.OpenKV(ctx, "adj-seq-empty")
		require.NoError(t, err)

		b := consumer.NewBootstrapper(conn, "core-seq-empty", adjKV)
		go func() { _ = b.Run(ctx) }()
		select {
		case <-b.Ready():
		case <-ctx.Done():
			t.Fatal("timed out waiting for Ready on an empty stream")
		}

		last, err := conn.StreamLastSequence(ctx, "KV_core-seq-empty")
		require.NoError(t, err)
		require.Equal(t, last, b.AppliedSeq(),
			"the poll reports the head the drained consumer has reached — 0 on a stream that holds nothing")
	})

	t.Run("a drained backlog's cursor comes from the handler", func(t *testing.T) {
		coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-seq-backlog"})
		require.NoError(t, err)
		_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-seq-backlog"})
		require.NoError(t, err)
		adjKV, err := conn.OpenKV(ctx, "adj-seq-backlog")
		require.NoError(t, err)

		const backlog = 25
		for i := range backlog {
			evt := adjacency.CoreKVEvent{
				CoreKvKey:   fmt.Sprintf("core.s%d", i),
				EdgeID:      fmt.Sprintf("s%d", i),
				Name:        "HAS_PARTY",
				Direction:   "outbound",
				NodeID:      "nodeSeq",
				OtherNodeID: fmt.Sprintf("other%d", i),
			}
			data, merr := json.Marshal(evt)
			require.NoError(t, merr)
			_, perr := coreKV.Put(ctx, "edge."+evt.EdgeID, data)
			require.NoError(t, perr)
		}

		last, err := conn.StreamLastSequence(ctx, "KV_core-seq-backlog")
		require.NoError(t, err)
		require.Equal(t, uint64(backlog), last)

		b := consumer.NewBootstrapper(conn, "core-seq-backlog", adjKV)
		go func() { _ = b.Run(ctx) }()
		select {
		case <-b.Ready():
		case <-ctx.Done():
			t.Fatal("timed out waiting for Ready after a backlog")
		}

		// Ready is raised from inside the handler, after its disposition but
		// before the pump has necessarily returned, so poll for the cursor
		// rather than reading it once — the condition, never a fixed sleep.
		require.Eventually(t, func() bool { return b.AppliedSeq() == last }, 10*time.Second, 10*time.Millisecond,
			"a drained backlog leaves the cursor at the last applied sequence, got %d want %d", b.AppliedSeq(), last)
	})

	t.Run("a restart against an already-drained durable takes its cursor from the poll", func(t *testing.T) {
		// The path that makes the poll load-bearing rather than decorative: on
		// an empty stream the head is 0 and a poll that did nothing would be
		// indistinguishable from one that worked. Here the stream holds
		// messages the DURABLE has already retired, so a second process on the
		// same durable is handed nothing — its handler cannot run, and the
		// poll is the only thing that can give it a cursor. Without one it
		// would refuse every retraction until the next live event.
		coreKV, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-seq-restart"})
		require.NoError(t, err)
		for _, bucket := range []string{"adj-seq-restart-1", "adj-seq-restart-2"} {
			_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket})
			require.NoError(t, err)
		}
		adjOne, err := conn.OpenKV(ctx, "adj-seq-restart-1")
		require.NoError(t, err)
		adjTwo, err := conn.OpenKV(ctx, "adj-seq-restart-2")
		require.NoError(t, err)

		for i := range 5 {
			evt := adjacency.CoreKVEvent{
				CoreKvKey:   fmt.Sprintf("core.r%d", i),
				EdgeID:      fmt.Sprintf("r%d", i),
				Name:        "HAS_PARTY",
				Direction:   "outbound",
				NodeID:      "nodeRestart",
				OtherNodeID: fmt.Sprintf("other%d", i),
			}
			data, merr := json.Marshal(evt)
			require.NoError(t, merr)
			_, perr := coreKV.Put(ctx, "edge."+evt.EdgeID, data)
			require.NoError(t, perr)
		}
		last, err := conn.StreamLastSequence(ctx, "KV_core-seq-restart")
		require.NoError(t, err)

		firstCtx, stopFirst := context.WithCancel(ctx)
		first := consumer.NewBootstrapper(conn, "core-seq-restart", adjOne)
		go func() { _ = first.Run(firstCtx) }()
		select {
		case <-first.Ready():
		case <-ctx.Done():
			t.Fatal("timed out waiting for the first bootstrapper to drain")
		}
		require.Eventually(t, func() bool { return first.AppliedSeq() == last }, 10*time.Second, 10*time.Millisecond,
			"the first process applies the backlog")
		stopFirst()

		second := consumer.NewBootstrapper(conn, "core-seq-restart", adjTwo)
		go func() { _ = second.Run(ctx) }()
		select {
		case <-second.Ready():
		case <-ctx.Done():
			t.Fatal("timed out waiting for the restarted bootstrapper")
		}
		require.Eventually(t, func() bool { return second.AppliedSeq() == last }, 10*time.Second, 10*time.Millisecond,
			"a restarted process is handed no message, so only the caught-up poll can give it a cursor; got %d want %d",
			second.AppliedSeq(), last)
	})
}
