package lens

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestCoreKVSource_TaxonomyDeadCallback_FiresOnUnexpectedCloseNotOnCleanShutdown
// covers consume's channel-close branch (dynamic-type-taxonomy-design.md
// §6.5's "resolver's consumer dies" row): the dead callback fires when the
// events channel closes while ctx is still live (an unrecoverable
// subscription error — runKVSubscription's doc), and must NOT fire on a
// clean, ctx-cancelled shutdown even though runKVSubscription always closes
// its channel there too. consume is called directly (this file is package
// lens, not lens_test) with a hand-built events channel, so both branches
// are exercised deterministically without depending on JetStream's own
// error timing. A real *substrate.Conn (natsfixture-backed) is used rather
// than nil so the clean-shutdown case's deleteOwnDurable call — which a nil
// conn would panic on — completes normally (deleting a durable that was
// never created, which DeleteDurable treats as success).
func TestCoreKVSource_TaxonomyDeadCallback_FiresOnUnexpectedCloseNotOnCleanShutdown(t *testing.T) {
	s := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, s.ClientURL())
	defer nc.Close()
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	bgCtx := context.Background()
	_, err = js.CreateKeyValue(bgCtx, jetstream.KeyValueConfig{Bucket: "core-kv"})
	require.NoError(t, err)

	// Case 1: the events channel closes while ctx is still live.
	src1 := NewCoreKVSource(conn, "core-kv", "test1", discardTestLogger())
	fired1 := make(chan struct{}, 1)
	src1.SetTaxonomyDeadCallback(func() { fired1 <- struct{}{} })
	events1 := make(chan substrate.KVEvent)
	runCtx1, cancel1 := context.WithCancel(bgCtx)
	defer cancel1()
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		src1.consume(runCtx1, events1, "dead-cb-durable-1")
	}()
	close(events1)
	select {
	case <-fired1:
	case <-time.After(2 * time.Second):
		t.Fatal("taxonomyDeadCB did not fire on an unexpected channel close")
	}
	<-done1

	// Case 2: the channel closes AND ctx is already cancelled — the real
	// shutdown race consume's own comment names ("select does not guarantee
	// which ready case wins"). A plain context.WithCancel can't deterministically
	// force the "!ok" branch here (if its Done() channel is also ready,
	// select may take that case first every time, never touching the
	// ctx.Err() check at all — a naive version of this test using a real
	// cancelled context passed that way for the wrong reason). stuckDoneCtx
	// reports Err() non-nil while its Done() channel never closes, so the
	// events-branch is the ONLY ready case — deterministically exercising
	// the "!ok" branch's ctx.Err() guard itself, which is the thing under
	// test.
	src2 := NewCoreKVSource(conn, "core-kv", "test2", discardTestLogger())
	fired2 := false
	src2.SetTaxonomyDeadCallback(func() { fired2 = true })
	events2 := make(chan substrate.KVEvent)
	close(events2)
	fakeCtx := stuckDoneCtx{Context: bgCtx, err: context.Canceled}
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		src2.consume(fakeCtx, events2, "dead-cb-durable-2")
	}()
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("consume did not return within 2s")
	}
	require.False(t, fired2, "ctx.Err() != nil must suppress the dead callback even though the channel also closed")
}

// stuckDoneCtx wraps a context.Context, reporting a caller-supplied Err()
// while its Done() channel never closes — used to deterministically force
// consume's "!ok" branch to be the only ready select case even though the
// context is (per Err()) already cancelled, isolating the ctx.Err() guard
// from the real, non-deterministic shutdown race it exists to handle.
type stuckDoneCtx struct {
	context.Context
	err error
}

func (c stuckDoneCtx) Done() <-chan struct{} { return nil }
func (c stuckDoneCtx) Err() error            { return c.err }
