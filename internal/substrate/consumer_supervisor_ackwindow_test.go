package substrate

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// provisionAckWindowStream creates a plain JetStream stream over ackw.> for the
// ack-window tests and returns its name.
func provisionAckWindowStream(ctx context.Context, t *testing.T, c *Conn) string {
	t.Helper()
	const stream = "ackw-work"
	if _, err := c.js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     stream,
		Subjects: []string{"ackw.>"},
	}); err != nil {
		t.Fatalf("create stream %q: %v", stream, err)
	}
	return stream
}

// TestSupervisor_AckWindowBoundToThePump proves the two halves of the wedge fix
// together, because neither alone closes it.
//
// A pump's drain loop is serial, so the ONLY message whose AckWait window it can
// honour is the one it is currently handling. The test publishes a burst far
// larger than any plausible in-flight count, blocks the handler on the first
// message for several AckWait periods, and asserts:
//
//   - **Prefetch bound.** NumAckPending never exceeds one while the handler is
//     blocked. On nats.go's DefaultMaxMessages the client would have pulled the
//     whole burst into its buffer, marking every one of them delivered with the
//     ack clock running on work the pump had not started — the condition that
//     froze a live lens's ack floor.
//   - **In-progress heartbeat.** The blocked message is not redelivered
//     underneath its own handler, even after the window has elapsed several
//     times over. NumRedelivered stays zero and the delivery count stays one.
//
// Then it releases the handler and requires the whole burst to drain, so the
// bound is proven not to cost liveness.
func TestSupervisor_AckWindowBoundToThePump(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	stream := provisionAckWindowStream(ctx, t, c)

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	// Short enough to elapse several times inside the test, long enough that the
	// server's own timer granularity is not the thing under measurement.
	const ackWait = 500 * time.Millisecond
	const published = 40

	entered := make(chan struct{}, published)
	release := make(chan struct{})

	// The drain is tracked by DISTINCT stream sequence, not by handler
	// invocation count. AckWait is 500ms — deliberately short, so the observation
	// phase below can watch several full windows — and the drain that follows is
	// serial, so on a runner slow enough to put 500ms between a delivery and its
	// ack JetStream legitimately redelivers. Counting invocations would score
	// that redelivery as progress and let the drain wait finish with messages
	// still outstanding, which is the assertion right after it. A set makes the
	// wait mean what the next assertion assumes: every published message handled.
	var handledMu sync.Mutex
	handled := make(map[uint64]struct{}, published)
	progress := make(chan struct{}, 1)
	distinctHandled := func() int {
		handledMu.Lock()
		defer handledMu.Unlock()
		return len(handled)
	}

	// Registered after sup.Stop so it runs BEFORE it (cleanups are LIFO): an
	// early t.Fatalf must not leave a pump goroutine parked in the handler with
	// Stop waiting on it.
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseAll)

	spec := ConsumerSpec{
		Name:          "ackw-pump",
		Stream:        stream,
		FilterSubject: "ackw.msg",
		AckWait:       ackWait,
		Handler: func(_ context.Context, m Message) (Decision, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			handledMu.Lock()
			handled[m.Sequence] = struct{}{}
			handledMu.Unlock()
			// Non-blocking: the pump must never park here, or Stop would wait on
			// a handler that no reader is left to unblock.
			select {
			case progress <- struct{}{}:
			default:
			}
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}

	for i := 0; i < published; i++ {
		if _, err := c.js.Publish(ctx, "ackw.msg", []byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	// The handler is now blocked inside the first message.
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("handler never entered")
	}

	info := func() *jetstream.ConsumerInfo {
		t.Helper()
		got, err := sup.consumerInfo(ctx, spec.Name, "ack window")
		if err != nil {
			t.Fatalf("consumer info: %v", err)
		}
		return got
	}

	// Watch across several full AckWait periods. A prefetch above one shows up
	// immediately as NumAckPending > 1; a missing heartbeat shows up as the
	// blocked message being redelivered once the window lapses.
	const observedWindows = 5
	sampler := time.NewTicker(ackWait / 5)
	defer sampler.Stop()
	deadline := time.After(observedWindows * ackWait)
observe:
	for {
		select {
		case <-deadline:
			break observe
		case <-sampler.C:
			got := info()
			if got.NumAckPending > 1 {
				t.Fatalf("NumAckPending = %d while the pump is inside one handler; a serial pump must hold at most 1 (prefetch bound not applied)", got.NumAckPending)
			}
			if got.NumRedelivered != 0 {
				t.Fatalf("NumRedelivered = %d: the in-flight message was redelivered under its own handler (in-progress heartbeat not holding the window open)", got.NumRedelivered)
			}
		}
	}

	// The blocked message must still be a first delivery.
	if got := info(); got.Delivered.Consumer != 1 {
		t.Fatalf("consumer sequence = %d after %v blocked; want 1 (every increment past the first is a redelivery of the same message)", got.Delivered.Consumer, observedWindows*ackWait)
	}

	releaseAll()

	drainDeadline := time.After(30 * time.Second)
	for distinctHandled() < published {
		select {
		case <-progress:
		case <-drainDeadline:
			t.Fatalf("only %d of %d distinct messages drained after release", distinctHandled(), published)
		}
	}

	waitFor(t, 15*time.Second, func() bool {
		n, err := sup.OutstandingForConsumer(ctx, spec.Name)
		return err == nil && n == 0
	}, "consumer did not drain to zero outstanding after the handler was released")
}
