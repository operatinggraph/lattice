package substrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// provisionAccessorStream creates a plain JetStream stream over acc.> for the
// pending/outstanding accessor and Add-unwind tests, and returns its name.
func provisionAccessorStream(ctx context.Context, t *testing.T, c *Conn) string {
	t.Helper()
	const stream = "acc-work"
	if _, err := c.js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     stream,
		Subjects: []string{"acc.>"},
	}); err != nil {
		t.Fatalf("create stream %q: %v", stream, err)
	}
	return stream
}

// TestSupervisor_PendingForConsumer proves the accessor reports the durable's
// real un-delivered backlog: a pump held paused from activation never opens its
// iterator, so every published message stays pending; after Resume the pump
// drains and the count converges to zero. Also pins the unknown-name contract:
// a name outside the registry yields (0, error) naming the "pending" read.
func TestSupervisor_PendingForConsumer(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	stream := provisionAccessorStream(ctx, t, c)

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	processed := make(chan struct{}, 8)
	spec := ConsumerSpec{
		Name:          "acc-pending",
		Stream:        stream,
		FilterSubject: "acc.pending",
		InitialPause:  PauseManual, // hold the pump so nothing is ever fetched
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			processed <- struct{}{}
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}

	const published = 3
	for i := 0; i < published; i++ {
		if _, err := c.js.Publish(ctx, "acc.pending", []byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	// The paused pump never opened an iterator, so all messages count as pending.
	waitFor(t, 5*time.Second, func() bool {
		n, err := sup.PendingForConsumer(ctx, "acc-pending")
		return err == nil && n == published
	}, "PendingForConsumer did not report the published backlog")

	// With nothing delivered, outstanding equals pending (NumAckPending is 0).
	out, err := sup.OutstandingForConsumer(ctx, "acc-pending")
	if err != nil {
		t.Fatalf("OutstandingForConsumer: %v", err)
	}
	if out != published {
		t.Fatalf("OutstandingForConsumer = %d, want %d (pending only, nothing in flight)", out, published)
	}

	// Unknown name: (0, error), and the error names the "pending" read.
	n, err := sup.PendingForConsumer(ctx, "acc-unknown")
	if err == nil || n != 0 {
		t.Fatalf("PendingForConsumer(unknown) = (%d, %v), want (0, not-managed error)", n, err)
	}
	if !strings.Contains(err.Error(), "not managed") || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("PendingForConsumer(unknown) error = %q, want it to say not-managed and name the pending read", err)
	}

	// Resume: the pump drains and the backlog converges to zero.
	if !sup.Resume(ctx, "acc-pending") {
		t.Fatal("Resume of a managed consumer should return true")
	}
	for i := 0; i < published; i++ {
		select {
		case <-processed:
		case <-time.After(5 * time.Second):
			t.Fatalf("pump processed only %d/%d messages after Resume", i, published)
		}
	}
	waitFor(t, 5*time.Second, func() bool {
		n, err := sup.PendingForConsumer(ctx, "acc-pending")
		return err == nil && n == 0
	}, "PendingForConsumer did not converge to 0 after the drain")
}

// TestSupervisor_OutstandingForConsumer_CountsUnackedInFlight proves the
// drain-detection contract: a delivered-but-unacked message leaves NumPending,
// so PendingForConsumer alone reads a mid-processing consumer as drained;
// OutstandingForConsumer adds NumAckPending and keeps counting every message
// until it is acked. The handler holds deliveries in flight (un-acked) behind a
// gate while MaxAckPending keeps part of the backlog undelivered, so both terms
// of the sum are non-zero at once; releasing the gate acks everything and the
// count drains to zero.
func TestSupervisor_OutstandingForConsumer_CountsUnackedInFlight(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	stream := provisionAccessorStream(ctx, t, c)

	const (
		published     = 4
		maxAckPending = 2
	)
	gate := make(chan struct{})
	acked := make(chan struct{}, published)
	spec := ConsumerSpec{
		Name:          "acc-outstanding",
		Stream:        stream,
		FilterSubject: "acc.outstanding",
		MaxAckPending: maxAckPending,
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			<-gate // hold every delivery in flight, un-acked, until released
			acked <- struct{}{}
			return Ack, nil
		},
	}
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for i := 0; i < published; i++ {
		if _, err := c.js.Publish(ctx, "acc.outstanding", []byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	// Converge to the held state: maxAckPending messages delivered (in flight,
	// un-acked) and the rest still pending. The state then holds until the gate
	// opens — no ack can land while the handler is blocked.
	waitFor(t, 10*time.Second, func() bool {
		pending, err := sup.PendingForConsumer(ctx, spec.Name)
		if err != nil {
			return false
		}
		outstanding, err := sup.OutstandingForConsumer(ctx, spec.Name)
		if err != nil {
			return false
		}
		return pending == published-maxAckPending && outstanding == published
	}, "did not converge to the held state (pending=2, outstanding=4) with deliveries un-acked")

	// The key drain-detection vector, read at the stable held state: pending
	// under-counts by exactly the un-acked in-flight messages; outstanding does
	// not. Cross-check both accessors against the raw consumer info.
	pending, err := sup.PendingForConsumer(ctx, spec.Name)
	if err != nil {
		t.Fatalf("PendingForConsumer: %v", err)
	}
	outstanding, err := sup.OutstandingForConsumer(ctx, spec.Name)
	if err != nil {
		t.Fatalf("OutstandingForConsumer: %v", err)
	}
	info := consumerInfoByName(ctx, t, c, stream, spec.Name)
	if pending != info.NumPending {
		t.Fatalf("PendingForConsumer = %d, want NumPending = %d", pending, info.NumPending)
	}
	if want := info.NumPending + uint64(info.NumAckPending); outstanding != want {
		t.Fatalf("OutstandingForConsumer = %d, want NumPending+NumAckPending = %d", outstanding, want)
	}
	if outstanding != published {
		t.Fatalf("outstanding = %d, want %d (every published message is unfinished)", outstanding, published)
	}
	if pending >= outstanding {
		t.Fatalf("pending (%d) must under-count the held in-flight messages relative to outstanding (%d)", pending, outstanding)
	}

	// Unknown name: (0, error), and the error names the "outstanding" read.
	n, err := sup.OutstandingForConsumer(ctx, "acc-unknown")
	if err == nil || n != 0 {
		t.Fatalf("OutstandingForConsumer(unknown) = (%d, %v), want (0, not-managed error)", n, err)
	}
	if !strings.Contains(err.Error(), "not managed") || !strings.Contains(err.Error(), "outstanding") {
		t.Fatalf("OutstandingForConsumer(unknown) error = %q, want it to say not-managed and name the outstanding read", err)
	}

	// Release the gate: every message acks and the consumer truly drains.
	close(gate)
	for i := 0; i < published; i++ {
		select {
		case <-acked:
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d/%d messages acked after the gate opened", i, published)
		}
	}
	waitFor(t, 5*time.Second, func() bool {
		outstanding, err := sup.OutstandingForConsumer(ctx, spec.Name)
		return err == nil && outstanding == 0
	}, "OutstandingForConsumer did not drain to 0 after all messages acked")
}

// TestSupervisor_AckFloorForConsumer proves the accessor reports the
// durable's persisted ack floor — zero before anything is acked, and the
// stream sequence of the last acked message once the consumer has processed
// some. This is the primitive a caller uses to seed in-process
// forward-progress state at startup instead of starting cold at zero.
func TestSupervisor_AckFloorForConsumer(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	stream := provisionAccessorStream(ctx, t, c)

	acked := make(chan uint64, 8)
	spec := ConsumerSpec{
		Name:          "acc-ackfloor",
		Stream:        stream,
		FilterSubject: "acc.ackfloor",
		Handler: func(_ context.Context, m Message) (Decision, error) {
			acked <- m.Sequence
			return Ack, nil
		},
	}
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Before anything is published/acked, the floor is zero.
	floor, err := sup.AckFloorForConsumer(ctx, "acc-ackfloor")
	if err != nil {
		t.Fatalf("AckFloorForConsumer (fresh durable): %v", err)
	}
	if floor != 0 {
		t.Fatalf("AckFloorForConsumer (fresh durable) = %d, want 0", floor)
	}

	const published = 3
	var lastSeq uint64
	for i := 0; i < published; i++ {
		if _, err := c.js.Publish(ctx, "acc.ackfloor", []byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	for i := 0; i < published; i++ {
		select {
		case lastSeq = <-acked:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d/%d messages acked", i, published)
		}
	}

	// Once every published message is acked, the floor converges to the last
	// acked stream sequence.
	waitFor(t, 5*time.Second, func() bool {
		floor, err := sup.AckFloorForConsumer(ctx, "acc-ackfloor")
		return err == nil && floor == lastSeq
	}, "AckFloorForConsumer did not converge to the last acked stream sequence")

	// Unknown name: (0, error), and the error names the "ack floor" read.
	n, err := sup.AckFloorForConsumer(ctx, "acc-unknown")
	if err == nil || n != 0 {
		t.Fatalf("AckFloorForConsumer(unknown) = (%d, %v), want (0, not-managed error)", n, err)
	}
	if !strings.Contains(err.Error(), "not managed") || !strings.Contains(err.Error(), "ack floor") {
		t.Fatalf("AckFloorForConsumer(unknown) error = %q, want it to say not-managed and name the ack floor read", err)
	}
}

// TestSupervisor_Accessors_DeletedDurable_FailLoud proves both accessors ERROR
// when a managed consumer's durable is gone from the server, surfacing the
// underlying not-found — they must never report 0 for a name they cannot read,
// because a drain-watch caller would take that 0 as "drained". The pump is held
// paused from activation so nothing recreates or touches the durable.
func TestSupervisor_Accessors_DeletedDurable_FailLoud(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	stream := provisionAccessorStream(ctx, t, c)

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)
	spec := ConsumerSpec{
		Name:          "acc-deleted",
		Stream:        stream,
		FilterSubject: "acc.deleted",
		InitialPause:  PauseManual,
		Handler:       func(_ context.Context, _ Message) (Decision, error) { return Ack, nil },
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := c.js.DeleteConsumer(ctx, stream, spec.Name); err != nil {
		t.Fatalf("delete durable out-of-band: %v", err)
	}

	n, err := sup.PendingForConsumer(ctx, spec.Name)
	if err == nil || n != 0 {
		t.Fatalf("PendingForConsumer(deleted durable) = (%d, %v), want (0, error)", n, err)
	}
	if !errors.Is(err, jetstream.ErrConsumerNotFound) {
		t.Fatalf("PendingForConsumer error = %v, want it to wrap ErrConsumerNotFound", err)
	}
	n, err = sup.OutstandingForConsumer(ctx, spec.Name)
	if err == nil || n != 0 {
		t.Fatalf("OutstandingForConsumer(deleted durable) = (%d, %v), want (0, error)", n, err)
	}
	if !errors.Is(err, jetstream.ErrConsumerNotFound) {
		t.Fatalf("OutstandingForConsumer error = %v, want it to wrap ErrConsumerNotFound", err)
	}
}

// TestSupervisor_AddAfterStop_Rejected pins the terminal-registry contract: a
// stopped supervisor rejects Add with an explicit error and registers nothing.
func TestSupervisor_AddAfterStop_Rejected(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	stream := provisionAccessorStream(ctx, t, c)

	sup := NewConsumerSupervisor(c)
	sup.Stop()

	err := sup.Add(ctx, ConsumerSpec{
		Name:          "acc-after-stop",
		Stream:        stream,
		FilterSubject: "acc.afterstop",
		Handler:       func(_ context.Context, _ Message) (Decision, error) { return Ack, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "Add after Stop") {
		t.Fatalf("Add on a stopped supervisor = %v, want an Add-after-Stop error", err)
	}
	if sup.IsManaged("acc-after-stop") {
		t.Fatal("a rejected Add must not register the consumer")
	}
}

// TestSupervisor_ConcurrentDuplicateAdds_LoserUnwinds drives the duplicate-name
// unwind inside Add: racing Adds of the same spec pass the cheap registration
// pre-check together, each builds its workers, and exactly one wins the registry
// insert — every loser cancels its just-built worker contexts (cancelAll) and
// returns nil without starting a pump. The pre-insert window spans the
// durable-create round-trip, so concurrent Adds land in it reliably; iterations
// widen the exposure. The winner's pump must be untouched by the losers'
// unwinds: it still delivers.
func TestSupervisor_ConcurrentDuplicateAdds_LoserUnwinds(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	stream := provisionAccessorStream(ctx, t, c)

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	const (
		iterations = 8
		racers     = 4
	)
	for i := 0; i < iterations; i++ {
		name := fmt.Sprintf("acc-dup-%d", i)
		subject := fmt.Sprintf("acc.dup.%d", i)
		delivered := make(chan struct{}, racers)
		spec := ConsumerSpec{
			Name:          name,
			Stream:        stream,
			FilterSubject: subject,
			Handler: func(_ context.Context, _ Message) (Decision, error) {
				delivered <- struct{}{}
				return Ack, nil
			},
		}

		start := make(chan struct{})
		errs := make([]error, racers)
		var wg sync.WaitGroup
		for r := 0; r < racers; r++ {
			wg.Add(1)
			go func(r int) {
				defer wg.Done()
				<-start
				errs[r] = sup.Add(ctx, spec)
			}(r)
		}
		close(start)
		wg.Wait()

		for r, err := range errs {
			if err != nil {
				t.Fatalf("iteration %d: racer %d Add = %v, want nil (a losing duplicate Add unwinds silently)", i, r, err)
			}
		}
		if !sup.IsManaged(name) {
			t.Fatalf("iteration %d: %q not managed after racing Adds", i, name)
		}
		if got := len(sup.ManagedNames()); got != i+1 {
			t.Fatalf("iteration %d: %d managed consumers, want %d (exactly one registration per name)", i, got, i+1)
		}

		// The surviving pump still delivers — the losers' context cancels did not
		// touch the winner's workers.
		if _, err := c.js.Publish(ctx, subject, []byte(`{"probe":true}`)); err != nil {
			t.Fatalf("iteration %d: publish: %v", i, err)
		}
		select {
		case <-delivered:
		case <-time.After(10 * time.Second):
			t.Fatalf("iteration %d: winning pump never delivered — an unwind broke the surviving worker", i)
		}
	}
}

// TestSupervisor_StopRacingAdd_NeverLeaksAPump races Stop against a single Add
// on fresh supervisors, timed to land inside Add's unwind window: the durable's
// appearance on the server is the observable that Add has passed its pre-check
// and is between creating the durable and registering, so Stop fired at that
// moment contends with the registration itself. Whichever interleaving lands —
// Add unwound at the post-create stopped check (cancelAll), or Add registered
// first and then stopped — the invariant is the same: afterwards the registry
// is empty and no pump survives. The lock race stays a genuine race, so
// iterations explore both outcomes.
func TestSupervisor_StopRacingAdd_NeverLeaksAPump(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	stream := provisionAccessorStream(ctx, t, c)

	const iterations = 12
	for i := 0; i < iterations; i++ {
		name := fmt.Sprintf("acc-stopadd-%d", i)
		sup := NewConsumerSupervisor(c)
		spec := ConsumerSpec{
			Name:          name,
			Stream:        stream,
			FilterSubject: fmt.Sprintf("acc.stopadd.%d", i),
			Handler:       func(_ context.Context, _ Message) (Decision, error) { return Ack, nil },
		}

		addErr := make(chan error, 1)
		go func() {
			addErr <- sup.Add(ctx, spec)
		}()

		// Busy-poll (each probe is its own server round-trip, so it self-paces)
		// until the durable exists — Add is now inside the window before its
		// registration re-check — then fire Stop into that window.
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := c.js.Consumer(ctx, stream, name); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("iteration %d: durable %q never appeared", i, name)
			}
		}
		sup.Stop()
		err := <-addErr

		if err != nil && !strings.Contains(err.Error(), "Add after Stop") {
			t.Fatalf("iteration %d: Add = %v, want nil or an Add-after-Stop error", i, err)
		}
		if sup.IsManaged(name) {
			t.Fatalf("iteration %d: %q still managed after Stop — a racing Add leaked past the stopped registry", i, name)
		}
		// A second Stop must be a clean no-op whether or not the Add won.
		sup.Stop()
	}
}
