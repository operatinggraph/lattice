package substrate

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// gatedProgressMsg is a jetstream.Msg whose InProgress blocks until the test
// releases it, and which records whether any InProgress ran after the caller
// declared the heartbeat stopped.
type gatedProgressMsg struct {
	entered chan struct{} // one token per InProgress that has begun
	release chan struct{} // closed to let every in-flight InProgress return

	mu           sync.Mutex
	stopReturned bool
	sends        int
	sendsAfter   int
}

func newGatedProgressMsg() *gatedProgressMsg {
	return &gatedProgressMsg{entered: make(chan struct{}, 16), release: make(chan struct{})}
}

func (m *gatedProgressMsg) InProgress() error {
	select {
	case m.entered <- struct{}{}:
	default:
	}
	<-m.release
	m.mu.Lock()
	m.sends++
	if m.stopReturned {
		m.sendsAfter++
	}
	m.mu.Unlock()
	return nil
}

func (m *gatedProgressMsg) markStopReturned() {
	m.mu.Lock()
	m.stopReturned = true
	m.mu.Unlock()
}

func (m *gatedProgressMsg) counts() (total, after int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sends, m.sendsAfter
}

func (m *gatedProgressMsg) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{NumDelivered: 1}, nil
}
func (m *gatedProgressMsg) Data() []byte                     { return nil }
func (m *gatedProgressMsg) Headers() nats.Header             { return nil }
func (m *gatedProgressMsg) Subject() string                  { return "$KV.weaver-targets.t.e" }
func (m *gatedProgressMsg) Reply() string                    { return "" }
func (m *gatedProgressMsg) Ack() error                       { return nil }
func (m *gatedProgressMsg) DoubleAck(context.Context) error  { return nil }
func (m *gatedProgressMsg) Nak() error                       { return nil }
func (m *gatedProgressMsg) NakWithDelay(time.Duration) error { return nil }
func (m *gatedProgressMsg) Term() error                      { return nil }
func (m *gatedProgressMsg) TermWithReason(string) error      { return nil }

// TestKeepAckAlive_StopOrdersAgainstAnInFlightHeartbeat pins the ordering the
// redelivery floors depend on.
//
// A delayed Nak is implemented server-side by BACKDATING the message's pending
// timestamp to now-AckWait+delay, and a +WPI re-stamps that entry to now — so a
// heartbeat that lands after the decision silently replaces whatever floor the
// handler asked for with a plain AckWait. The harm inverts as the floors grow:
// against the 5 s transient floor a straggler merely delayed the retry, but
// against the 5 m config-error floor it pulls redelivery FORWARD by an order of
// magnitude, turning a deliberate re-poll cadence back into an AckWait loop.
//
// Two things must therefore hold, and neither did while stop only closed a
// channel: stop must not return while a +WPI is in flight, and no +WPI may be
// sent after stop has returned.
func TestKeepAckAlive_StopOrdersAgainstAnInFlightHeartbeat(t *testing.T) {
	t.Parallel()

	msg := newGatedProgressMsg()
	// AckWait 4ms ⇒ a 2ms heartbeat interval: the first tick lands immediately
	// on the scale of this test.
	stop := keepAckAlive(msg, ConsumerSpec{Name: "kaa-test", AckWait: 4 * time.Millisecond})

	// Wait for a heartbeat to be genuinely mid-send.
	select {
	case <-msg.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat was sent; the fixture never reached the ordering under test")
	}

	stopped := make(chan struct{})
	go func() {
		stop()
		msg.markStopReturned()
		close(stopped)
	}()

	// The send is still blocked, so stop must still be blocked with it. A stop
	// that returns here is the bug: the caller would apply its decision while a
	// +WPI was still in flight behind it.
	select {
	case <-stopped:
		t.Fatal("stop returned while a +WPI was still in flight — the decision would race the heartbeat " +
			"and a delayed Nak's backdated timestamp would be re-stamped to now")
	case <-time.After(50 * time.Millisecond):
	}

	close(msg.release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop never returned after the in-flight +WPI completed")
	}

	// Past stop, no further heartbeat may be sent however many ticks elapse.
	total, after := msg.counts()
	if total == 0 {
		t.Fatal("the fixture recorded no heartbeat at all")
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, afterNow := msg.counts(); afterNow > 0 {
			t.Fatalf("%d +WPI landed after stop returned — a tick already past its select must be "+
				"refused, not merely signalled", afterNow)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, afterFinal := msg.counts(); afterFinal != after {
		t.Fatalf("a +WPI landed after stop returned (%d)", afterFinal)
	}
}

// TestKeepAckAlive_NoHeartbeatEscapesBehindStop is the repetition vector over
// the same ordering: 300 rounds of releasing an in-flight send at the moment
// stop is called, asserting that none of them lets a +WPI out behind stop.
//
// It covers the ordering as a whole, not one line of it. The narrower window the
// send side's re-read of the stop signal closes — a tick that left its select
// while stop was still pending, then waited on the lock through stop's critical
// section — is a few instructions wide and cannot be forced from outside: this
// fixture always makes stop the first waiter on the lock, so the loop always
// observes the stop signal at its next select. The re-read stays because the
// window is real; this test is not what proves it.
func TestKeepAckAlive_NoHeartbeatEscapesBehindStop(t *testing.T) {
	t.Parallel()
	for round := 0; round < 300; round++ {
		msg := newGatedProgressMsg()
		// An interval far shorter than the blocked send, so a tick is always
		// already pending when the send completes and the loop re-selects.
		stop := keepAckAlive(msg, ConsumerSpec{Name: "kaa-race", AckWait: 2 * time.Millisecond})

		select {
		case <-msg.entered:
		case <-time.After(5 * time.Second):
			stop()
			t.Fatalf("round %d: no heartbeat was sent", round)
		}

		stopped := make(chan struct{})
		go func() {
			stop()
			msg.markStopReturned()
			close(stopped)
		}()
		close(msg.release)
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: stop never returned", round)
		}
		// Give any escaped tick its chance to land before the round is judged.
		time.Sleep(2 * time.Millisecond)
		if _, after := msg.counts(); after > 0 {
			t.Fatalf("round %d: %d +WPI landed after stop returned — a tick waiting on the send lock "+
				"must re-read the stop signal under it, not send regardless", round, after)
		}
	}
}

// TestKeepAckAlive_StopIsIdempotent pins the contract processMsg relies on: the
// returned stop is called on every path out of the handler, sometimes twice.
func TestKeepAckAlive_StopIsIdempotent(t *testing.T) {
	t.Parallel()
	msg := newGatedProgressMsg()
	close(msg.release) // never block
	stop := keepAckAlive(msg, ConsumerSpec{Name: "kaa-idempotent", AckWait: 4 * time.Millisecond})
	stop()
	stop()
	stop()
}
