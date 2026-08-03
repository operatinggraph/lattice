package substrate

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// provisionWorkStream creates a plain (non-KV) JetStream stream over work.> for
// the fan-out tests and returns its name.
func provisionWorkStream(ctx context.Context, t *testing.T, c *Conn) string {
	t.Helper()
	const stream = "fanout-work"
	if _, err := c.js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     stream,
		Subjects: []string{"work.>"},
	}); err != nil {
		t.Fatalf("create stream %q: %v", stream, err)
	}
	return stream
}

// TestSupervisor_Workers_FanOutConcurrency proves a Workers > 1 spec runs
// multiple pump goroutines that process a lane's backlog concurrently, and that
// the shared durable load-balances each message to exactly one worker (no
// duplicate delivery, no lost message). The handler blocks the first arrival
// until a SECOND worker arrives concurrently, so the test deterministically
// proves at least two workers process simultaneously — impossible with a single
// pump. Bounded prefetch (pumpPullMaxMessages) keeps any one worker from
// hoarding the whole burst, which is exactly what makes the fan-out real.
func TestSupervisor_Workers_FanOutConcurrency(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	stream := provisionWorkStream(ctx, t, c)

	const (
		workers   = 4
		published = 64 // >> pumpPullMaxMessages so no single worker can hoard
	)

	var (
		concurrent    int32
		maxConcurrent int32
		processed     int32
		gateOnce      sync.Once
		gate          = make(chan struct{}) // closed once >=2 workers are concurrent
		seenMu        sync.Mutex
		seen          = map[string]int{}
	)

	handler := func(_ context.Context, m Message) (Decision, error) {
		cur := atomic.AddInt32(&concurrent, 1)
		for {
			old := atomic.LoadInt32(&maxConcurrent)
			if cur <= old || atomic.CompareAndSwapInt32(&maxConcurrent, old, cur) {
				break
			}
		}
		// Once two workers are in the handler at the same time, release everyone.
		if cur >= 2 {
			gateOnce.Do(func() { close(gate) })
		}
		select {
		case <-gate:
		case <-time.After(15 * time.Second):
			// Fail-safe so a regression (single pump) fails fast rather than hangs.
		}
		seenMu.Lock()
		seen[m.Subject]++
		seenMu.Unlock()
		atomic.AddInt32(&processed, 1)
		atomic.AddInt32(&concurrent, -1)
		return Ack, nil
	}

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)
	if err := sup.Add(ctx, ConsumerSpec{
		Name:          "fanout-consumer",
		Stream:        stream,
		FilterSubject: "work.job",
		DeliverPolicy: DeliverAll,
		Workers:       workers,
		Handler:       handler,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	for i := 0; i < published; i++ {
		if _, err := c.js.Publish(ctx, "work.job", []byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	// The gate closes only when two workers are concurrently in the handler.
	select {
	case <-gate:
	case <-time.After(10 * time.Second):
		t.Fatalf("never observed 2 concurrent workers (maxConcurrent=%d) — fan-out not parallel",
			atomic.LoadInt32(&maxConcurrent))
	}

	// All messages drain, each exactly once.
	deadline := time.Now().Add(10 * time.Second)
	for atomic.LoadInt32(&processed) < published {
		if time.Now().After(deadline) {
			t.Fatalf("only %d/%d processed before deadline", atomic.LoadInt32(&processed), published)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&maxConcurrent); got < 2 {
		t.Fatalf("maxConcurrent = %d, want >= 2 (concurrent fan-out)", got)
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	total := 0
	for _, n := range seen {
		total += n
	}
	if total != published {
		t.Fatalf("total deliveries = %d, want %d (each message delivered exactly once)", total, published)
	}
}

// TestSupervisor_PrefetchBoundAlwaysApplied proves the prefetch bound is not
// conditional on the worker count. Leaving the single-worker path on nats.go's
// DefaultMaxMessages (500) is what let a lens consumer buffer hundreds of
// messages whose AckWait clock was already running, and the worker count has
// nothing to do with it: every worker's drain loop is serial, so every worker
// needs the same bound.
func TestSupervisor_PrefetchBoundAlwaysApplied(t *testing.T) {
	if pumpPullMaxMessages != 1 {
		t.Fatalf("pumpPullMaxMessages = %d, want 1 — a serial worker must not hold work it has not started", pumpPullMaxMessages)
	}
	// Heartbeat + prefetch bound, for every spec: messagesOpts takes no spec at
	// all now, which is the point — there is no path that opts out.
	if got := len(messagesOpts()); got != 2 {
		t.Fatalf("messagesOpts len = %d, want 2 (heartbeat + prefetch bound)", got)
	}
}
