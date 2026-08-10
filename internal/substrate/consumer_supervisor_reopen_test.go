package substrate

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// capturingHandler records every slog record a supervisor emits, so a test can
// assert that a non-fatal outcome was actually REPORTED and not merely
// swallowed — the whole difference between "we stopped waiting" and "nothing
// happened" is that one of them says so.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

// sawWarnContaining reports whether any captured record is at Warn or above and
// mentions substr.
func (h *capturingHandler) sawWarnContaining(substr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level >= slog.LevelWarn && strings.Contains(r.Message, substr) {
			return true
		}
	}
	return false
}

// parkedPump is a supervised consumer whose pump is stopped inside its handler:
// it has taken one message and will not return from it until release is closed.
//
// That is the state every ResetAwaitReopen test needs, because it is the state
// the wait exists for. A pump only learns of a reopen request when its current
// message comes back from the handler (the drain's watcher calls Stop on the
// iterator, which does not interrupt a running handler), so a parked pump is a
// pump that provably CANNOT reopen until the test says so — which turns "did the
// call wait for the reopen" into an ordering question rather than a timing one.
//
// It also gives every test a SPENT acknowledgement for free: the pump announced
// its open at Add, so that channel is already closed and a caller that reached
// for it instead of the current one would return immediately.
type parkedPump struct {
	sup     *ConsumerSupervisor
	conn    *Conn
	name    string
	stream  string
	logs    *capturingHandler
	entered chan struct{}
	release chan struct{}
	// releaseOnce is shared with the cleanup, so a test that frees the handler
	// itself and the cleanup that frees it unconditionally cannot both close.
	releaseOnce *sync.Once
}

func newParkedPump(t *testing.T, name string) *parkedPump {
	t.Helper()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	logs := &capturingHandler{}
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	releaseOnce := &sync.Once{}
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }

	sup := NewConsumerSupervisor(c)
	// LIFO: the release runs BEFORE Stop. Stop waits for each pump goroutine to
	// exit, and a pump parked in its handler never will — so a cleanup order
	// that stopped first would hang the test rather than fail it.
	t.Cleanup(sup.Stop)
	t.Cleanup(closeRelease)

	spec := ConsumerSpec{
		Name:   name,
		Stream: "KV_" + bucket,
		Logger: slog.New(logs),
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}

	p := &parkedPump{
		sup: sup, conn: c, name: name, stream: "KV_" + bucket,
		logs: logs, entered: entered, release: release, releaseOnce: releaseOnce,
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.park", []byte(`{"v":1}`))
	p.awaitHandlerEntry(t, "the pump never took the message that parks it")
	return p
}

func (p *parkedPump) awaitHandlerEntry(t *testing.T, msg string) {
	t.Helper()
	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal(msg)
	}
}

func (p *parkedPump) releaseHandler() { p.releaseOnce.Do(func() { close(p.release) }) }

// createdAt reads the durable's server-side creation time, the observable that
// says the delete-recreate half of a reset has landed.
func (p *parkedPump) createdAt(t *testing.T) time.Time {
	t.Helper()
	return consumerInfoByName(context.Background(), t, p.conn, p.stream, p.name).Created
}

// awaitRecreated polls until the durable's creation time has moved past before.
// Tolerates the transient not-found of the delete window rather than treating it
// as a failure.
func (p *parkedPump) awaitRecreated(t *testing.T, before time.Time) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cons, err := p.conn.js.Consumer(context.Background(), p.stream, p.name)
		if err == nil {
			if info, ierr := cons.Info(context.Background()); ierr == nil && info.Created.After(before) {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("durable %q was never recreated", p.name)
}

// TestSupervisor_ConcurrentResetsOfOneDurableAllSucceed pins that the
// delete-recreate pair excludes itself.
//
// Recreating a durable is two server calls under one name. Interleaved, one
// caller's delete can land between the other's delete and create, and the server
// answers that by failing the create — it is writing the consumer's metadata into
// a directory the other request has just removed. Nothing above this package
// excludes concurrent resets of one consumer: Weaver and Loom reconcile with
// plain Reset, and a caller with two independent reset paths can have both in
// flight for one name. A failed reset is not a cosmetic loss either — it reaches
// the caller as a failed rebuild, and a caller that answers a failed rebuild by
// pausing the consumer would pause a healthy one because two resets overlapped.
//
// Both entry points are driven, since both go through the same pair, and the
// fan-out is released by a barrier rather than by timing so every reset really is
// contending.
func TestSupervisor_ConcurrentResetsOfOneDurableAllSucceed(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	const name = "sup-concurrent-reset"
	if err := sup.Add(ctx, ConsumerSpec{
		Name:    name,
		Stream:  "KV_" + bucket,
		Logger:  slog.New(&capturingHandler{}),
		Handler: func(context.Context, Message) (Decision, error) { return Ack, nil },
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	const resetters = 8
	const rounds = 5
	for round := 0; round < rounds; round++ {
		var ready sync.WaitGroup
		ready.Add(resetters)
		release := make(chan struct{})
		errs := make(chan error, resetters)

		for i := 0; i < resetters; i++ {
			go func(i int) {
				ready.Done()
				<-release
				if i%2 == 0 {
					errs <- sup.Reset(context.Background(), name)
				} else {
					errs <- sup.ResetAwaitReopen(context.Background(), name, 5*time.Second)
				}
			}(i)
		}

		// Every goroutine is parked on release before any of them starts, so the
		// contention is structural rather than a hoped-for coincidence.
		ready.Wait()
		close(release)

		for i := 0; i < resetters; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("round %d: concurrent resets of one durable must all succeed, got: %v", round, err)
			}
		}
	}
}

// TestSupervisor_UpdateSpecRacingResetDoesNotTearTheSpec drives the one pair
// nothing else exercises: UpdateSpec rewriting a consumer's spec while resets of
// that consumer read it.
//
// It is the pair the production sequence creates — UpdateSpec then Reset is how
// a caller changes a filter — and mc.spec is ordinary mutable shared state, so a
// reset that read the field directly across its JetStream calls would both race
// the writer and risk a torn pair: the delete addressing one spec's stream and
// the create another's. Under -race the reader/writer overlap is the assertion;
// the error checks are what would catch a torn pair.
//
// Both filters are valid on the same stream, so any interleaving must leave a
// consumer carrying one of them, and no reset may fail.
func TestSupervisor_UpdateSpecRacingResetDoesNotTearTheSpec(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	const name = "sup-updatespec-race"
	filterA := "$KV." + bucket + ".vtx.agreement.>"
	filterB := "$KV." + bucket + ".vtx.identity.>"
	if err := sup.Add(ctx, ConsumerSpec{
		Name:          name,
		Stream:        "KV_" + bucket,
		FilterSubject: filterA,
		Logger:        slog.New(&capturingHandler{}),
		Handler:       func(context.Context, Message) (Decision, error) { return Ack, nil },
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	const resetters = 6
	const updaters = 4
	const rounds = 5
	for round := 0; round < rounds; round++ {
		var ready sync.WaitGroup
		ready.Add(resetters + updaters)
		release := make(chan struct{})
		errs := make(chan error, resetters+updaters)

		for i := 0; i < resetters; i++ {
			go func(i int) {
				ready.Done()
				<-release
				if i%2 == 0 {
					errs <- sup.Reset(context.Background(), name)
				} else {
					errs <- sup.ResetAwaitReopen(context.Background(), name, 5*time.Second)
				}
			}(i)
		}
		for i := 0; i < updaters; i++ {
			go func(i int) {
				ready.Done()
				<-release
				filter := filterA
				if i%2 == 0 {
					filter = filterB
				}
				errs <- sup.UpdateSpec(name, func(s *ConsumerSpec) { s.FilterSubject = filter })
			}(i)
		}

		// Everyone is parked before anyone starts, so the readers and the writer
		// genuinely overlap rather than happening to.
		ready.Wait()
		close(release)

		for i := 0; i < resetters+updaters; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("round %d: a reset racing UpdateSpec must still succeed, got: %v", round, err)
			}
		}
	}

	// Whatever order they landed in, the durable exists and carries one of the
	// two filters — never a mixture, never nothing.
	info := consumerInfoByName(context.Background(), t, c, "KV_"+bucket, name)
	if info.Config.FilterSubject != filterA && info.Config.FilterSubject != filterB {
		t.Fatalf("consumer filter = %q, want one of %q / %q", info.Config.FilterSubject, filterA, filterB)
	}
}

// TestSupervisor_Reset_SignalsEveryPumpToReopen pins the half of Reset that
// TestSupervisor_Reset_RecreatesWithNewFilter does not see: the recreated
// durable satisfies that test whether or not any pump is ever told about it, and
// Weaver and Loom reconcile a diverged consumer with UpdateSpec + Reset and
// nothing else.
//
// It asserts the SIGNAL, not the reopen, and the difference is deliberate.
// Deleting the durable is self-announcing — a pump blocked on the old iterator
// finds out from the server and re-opens on its own, promptly, so "the pump
// re-opened" is satisfied with or without this signal and cannot pin it. What
// the signal supplies is that the reopen is the supervisor's doing and takes
// effect at once (the drain's watcher stops the iterator the moment it arrives),
// rather than being contingent on how a client happens to react to a consumer
// vanishing underneath it.
//
// The consumer is paused first, which is the one state where the trigger is
// observable: a paused pump sits in waitWhilePaused with no drain, so nothing is
// running to consume the signal before the assertion reads it.
func TestSupervisor_Reset_SignalsEveryPumpToReopen(t *testing.T) {
	t.Parallel()
	p := newParkedPump(t, "sup-reset-signals")

	if !p.sup.Pause(context.Background(), p.name) {
		t.Fatal("Pause must apply to a managed consumer")
	}
	// Let the parked handler finish so the drain unwinds into the paused state.
	p.releaseHandler()

	p.sup.mu.Lock()
	worker := p.sup.managed[p.name].workers[0]
	p.sup.mu.Unlock()

	// The pump has reached waitWhilePaused once handleDrainOutcome has cleared
	// both triggers and no drain is left to consume a new one.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if worker.state.anyReason() && len(worker.state.reopenTrigger) == 0 && len(worker.state.pauseTrigger) == 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(worker.state.reopenTrigger) != 0 {
		t.Fatal("the pump never settled into its paused state; the assertion below would be reading a stale trigger")
	}

	if err := p.sup.Reset(context.Background(), p.name); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if len(worker.state.reopenTrigger) != 1 {
		t.Fatal("Reset recreated the durable but did not ask its pump to re-open — the reopen would then " +
			"depend entirely on the client noticing the consumer had vanished")
	}
}

// TestSupervisor_ResetAwaitReopen_ReturnsOnlyAfterThePumpReopens is the barrier
// the call exists to provide: the delete-recreate is half a reset, so a caller
// that holds a bounded resource only for that half covers less of the handover
// than the bound names.
//
// The pump is parked in its handler, so it cannot reopen; the call must not
// return. Releasing the handler is the ONLY thing that lets the pump reopen, so
// a return after that is a return caused by the reopen, not merely one that
// happened later.
func TestSupervisor_ResetAwaitReopen_ReturnsOnlyAfterThePumpReopens(t *testing.T) {
	t.Parallel()
	p := newParkedPump(t, "sup-reopen-order")

	done := make(chan error, 1)
	go func() { done <- p.sup.ResetAwaitReopen(context.Background(), p.name, 30*time.Second) }()

	select {
	case err := <-done:
		t.Fatalf("ResetAwaitReopen returned (%v) while the pump was still parked in its handler — "+
			"it cannot have waited for a reopen that had not happened", err)
	case <-time.After(500 * time.Millisecond):
	}

	p.releaseHandler()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ResetAwaitReopen after a successful reopen: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("ResetAwaitReopen never returned after the pump was free to reopen")
	}

	// Corroboration that the signal it woke on was a real reopen: the pump is
	// reading the recreated durable, so the replayed message reaches the handler
	// again.
	p.awaitHandlerEntry(t, "the pump signalled a reopen but is not reading the recreated durable")
}

// TestSupervisor_ResetAwaitReopen_ASpentAcknowledgementDoesNotSatisfyTheWait
// pins the one exclusion the barrier does claim: an acknowledgement the caller
// did not wait for cannot release it.
//
// Every successful open closes the acknowledgement channel and installs a fresh
// one, so a pump that has simply been running has left a CLOSED channel behind
// it. A waiter that reached for that one — by snapshotting after requesting the
// reopen, or by holding a reference from earlier — would return at once and
// report a handover that had not begun. Here the pump is parked and cannot
// reopen, so returning early is only possible by picking up a spent
// acknowledgement; the call must instead spend its whole budget.
func TestSupervisor_ResetAwaitReopen_ASpentAcknowledgementDoesNotSatisfyTheWait(t *testing.T) {
	t.Parallel()
	p := newParkedPump(t, "sup-reopen-spent")

	// Spend one more, immediately before the call, so the most recent
	// acknowledgement in existence is closed and the call has to distinguish it
	// from the one it is owed.
	p.sup.mu.Lock()
	workers := p.sup.managed[p.name].workers
	p.sup.mu.Unlock()
	for _, w := range workers {
		w.state.signalReopened()
	}

	const budget = 400 * time.Millisecond
	start := time.Now()
	err := p.sup.ResetAwaitReopen(context.Background(), p.name, budget)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a reopen that did not arrive in time is not a failure: %v", err)
	}
	if elapsed < budget {
		t.Fatalf("ResetAwaitReopen returned after %v, inside its %v budget — it took a spent acknowledgement "+
			"instead of waiting for the one it is owed", elapsed, budget)
	}
}

// TestSupervisor_ResetAwaitReopen_OverlappingWaitersAreBothReleased pins that
// the acknowledgement cannot be STOLEN.
//
// Nothing excludes two resets of one consumer: this package takes no lock across
// them, and a caller with two independent reset paths can have both in flight
// for one name. If the acknowledgement were a value one
// waiter could receive, both would arm, the pump would reopen once, one would
// take it and the other would sit out its entire budget and then report a pump
// that had demonstrably reopened — a false alarm produced by the barrier itself.
//
// So both waiters must be released by the single reopen, and neither may reach
// its budget. The budget here is far longer than the test's patience, so a
// stolen wakeup fails rather than passes slowly.
func TestSupervisor_ResetAwaitReopen_OverlappingWaitersAreBothReleased(t *testing.T) {
	t.Parallel()
	p := newParkedPump(t, "sup-reopen-overlap")

	const budget = 60 * time.Second
	type result struct {
		err     error
		elapsed time.Duration
	}
	results := make(chan result, 2)
	start := func() {
		go func() {
			began := time.Now()
			err := p.sup.ResetAwaitReopen(context.Background(), p.name, budget)
			results <- result{err: err, elapsed: time.Since(began)}
		}()
	}

	// Started one at a time, each admitted only once its own delete-recreate has
	// landed. What overlaps here is the WAIT, which is what this test is about;
	// two delete-recreates issued at the same instant race each other at the
	// server, which is a separate matter and not the one being pinned.
	before := p.createdAt(t)
	start()
	p.awaitRecreated(t, before)
	mid := p.createdAt(t)
	start()
	p.awaitRecreated(t, mid)

	// Both must now be waiting on the reopen: neither may return before the pump
	// is free to reopen.
	select {
	case r := <-results:
		t.Fatalf("a waiter returned (%v) after %v while the pump was still parked", r.err, r.elapsed)
	case <-time.After(500 * time.Millisecond):
	}

	p.releaseHandler()

	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("overlapping waiter %d: %v", i, r.err)
			}
			if r.elapsed >= budget {
				t.Fatalf("overlapping waiter %d spent its whole %v budget — the other waiter took its wakeup",
					i, budget)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("overlapping waiter %d was never released; one reopen must release every waiter", i)
		}
	}
}

// TestSupervisor_ResetAwaitReopen_SkipsAPausedPump pins that a paused worker is
// not waited on.
//
// A paused pump cannot reopen until an operator Resume, so waiting on one burns
// the entire budget to learn nothing. That matters most to a caller that pauses
// consumers itself on failure: it would meet its own pause as a timeout on every
// later reset, and never be able to tell the two apart.
//
// The pump here is parked as well as paused, so a call that did NOT skip would
// spend the whole budget. It is given a budget far larger than the test's own
// patience, so passing requires the skip.
func TestSupervisor_ResetAwaitReopen_SkipsAPausedPump(t *testing.T) {
	t.Parallel()
	p := newParkedPump(t, "sup-reopen-paused")
	before := p.createdAt(t)

	if !p.sup.Pause(context.Background(), p.name) {
		t.Fatal("Pause must apply to a managed consumer")
	}

	done := make(chan error, 1)
	go func() { done <- p.sup.ResetAwaitReopen(context.Background(), p.name, 5*time.Minute) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("skipping a paused pump is not a failure: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ResetAwaitReopen waited on a paused pump, which can never reopen until it is resumed")
	}

	// It skipped the WAIT, not the WORK: the durable is recreated either way.
	p.awaitRecreated(t, before)
}

// TestSupervisor_ResetAwaitReopen_TimeoutIsNotAFailure pins the non-fatal
// outcome. The durable HAS been recreated by the time the wait starts, and the
// pump reopens on its own backoff, so an expired budget means "we stopped
// holding the caller's slot" — reported, not returned as an error, because a
// caller that treated it as a failed reset would pause or refuse a lens that is
// fine.
func TestSupervisor_ResetAwaitReopen_TimeoutIsNotAFailure(t *testing.T) {
	t.Parallel()
	p := newParkedPump(t, "sup-reopen-timeout")
	before := p.createdAt(t)

	if err := p.sup.ResetAwaitReopen(context.Background(), p.name, 300*time.Millisecond); err != nil {
		t.Fatalf("an expired reopen budget must not be reported as an error: %v", err)
	}
	if !p.logs.sawWarnContaining("did not reopen within the wait") {
		t.Fatal("an expired reopen budget must be reported on the consumer's logger; silently returning nil " +
			"makes a held-up handover indistinguishable from a clean one")
	}
	p.awaitRecreated(t, before)
}

// TestSupervisor_ResetAwaitReopen_CancelledContextIsAFailure is the other half
// of the previous test: a cancelled context is different in kind from an expired
// budget. The budget expiring means the caller waited as long as it agreed to;
// the context ending means the caller is GONE. One is a normal outcome, the
// other must propagate, and a caller cannot tell them apart if both return nil.
func TestSupervisor_ResetAwaitReopen_CancelledContextIsAFailure(t *testing.T) {
	t.Parallel()
	p := newParkedPump(t, "sup-reopen-cancel")
	before := p.createdAt(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.sup.ResetAwaitReopen(ctx, p.name, 5*time.Minute) }()

	// Cancel only once the delete-recreate has demonstrably landed, so the
	// cancellation lands on the WAIT and not on the reset's own JetStream calls.
	p.awaitRecreated(t, before)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled context must not be reported as a completed reset")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ResetAwaitReopen must surface the context error, got %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ResetAwaitReopen ignored its cancelled context")
	}
}
