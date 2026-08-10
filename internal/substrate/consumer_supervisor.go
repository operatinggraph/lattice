package substrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// ConsumerSupervisor owns the supervised-pump mechanism for a set of durable
// consumers: a registry of desired ConsumerSpecs, desired-vs-running reconcile
// (Add / Remove / Reset / Stop), and per-consumer lifecycle — a composable
// pause state machine (infra / structural / manual), a NakWithDelay backoff
// floor, and HealthSink persist/restore. Policy (the message handler, error
// classification, recovery probe, and health key) stays with the caller via the
// ConsumerSpec hooks, so Loom and Weaver reuse one supervised pump instead of
// each hand-rolling lifecycle, backoff, and health.
//
// No jetstream (or nats.go) type appears on the exported surface: callers import
// only substrate. The supervisor never sets MaxDeliver on any consumer it
// creates — retry cadence is bounded (NakWithDelay) but retry count is not.
type ConsumerSupervisor struct {
	conn *Conn

	mu      sync.Mutex
	managed map[string]*managedConsumer
	stopped bool
}

// managedConsumer holds the per-consumer runtime: the desired spec and one
// pumpWorker per concurrent pump goroutine binding the durable. A single-worker
// consumer (the default, and every Loom/Weaver/Refractor consumer) has exactly
// one element; a fan-out lane (Workers > 1) has N, all sharing the one durable.
type managedConsumer struct {
	spec    ConsumerSpec
	workers []*pumpWorker
	// resetMu serializes THIS consumer's delete-recreate pair, and nothing else.
	//
	// Recreating a durable is two server calls under one name, and the server
	// has no notion of them belonging together: two callers interleaving them
	// can have one delete land between the other's delete and create, which the
	// server answers by failing the create outright (a 500 while it writes the
	// consumer's metadata into a directory the other request has just removed).
	// Nothing above this package excludes concurrent resets of one consumer, so
	// the pair has to exclude itself.
	//
	// It lives on the managedConsumer rather than on the supervisor so two
	// DIFFERENT durables still recreate concurrently — the hazard is a shared
	// name, not shared server capacity, and the supervisor-wide alternative
	// would serialize a whole corpus behind one slow round trip.
	//
	// What it does NOT cover, deliberately: the pump's reopen, and the drain
	// behind it. Both are unbounded in a way the delete-recreate is not (a
	// handler's latency is not bounded by this package, and a busy consumer
	// never goes idle), so holding this across either would turn a
	// millisecond-wide mutual exclusion into a stall. ResetAwaitReopen's
	// snapshot, request and wait all happen after it is released.
	resetMu sync.Mutex
}

// pumpWorker is one supervised pump goroutine: its context cancel, a done
// channel closed when the goroutine exits, and its own pause state machine.
// Workers of the same consumer share only the durable (the server load-balances
// delivery); they hold no shared mutable state, so no worker can race another.
type pumpWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
	state  *pumpState
}

// workerCount resolves spec.Workers to the number of pump goroutines: at least
// one, even when Workers is left at its zero value.
func workerCount(spec ConsumerSpec) int {
	if spec.Workers > 1 {
		return spec.Workers
	}
	return 1
}

// NewConsumerSupervisor constructs a supervisor over conn. The supervisor uses
// conn's package-internal JetStream handle; nothing jetstream-typed is exposed.
func NewConsumerSupervisor(conn *Conn) *ConsumerSupervisor {
	return &ConsumerSupervisor{
		conn:    conn,
		managed: make(map[string]*managedConsumer),
	}
}

// Add registers spec, creates (idempotently) its durable consumer, and starts
// the supervised pump goroutine. Calling Add with a Name that is already managed
// is a no-op (the existing pump keeps running) — use Reset to recreate a durable
// whose config changed. Returns an error if the spec is invalid or the durable
// cannot be created.
func (s *ConsumerSupervisor) Add(ctx context.Context, spec ConsumerSpec) error {
	if err := validateSpec(spec); err != nil {
		return err
	}

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return fmt.Errorf("substrate: ConsumerSupervisor: Add after Stop")
	}
	if _, exists := s.managed[spec.Name]; exists {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	if _, err := s.createConsumer(ctx, spec); err != nil {
		return err
	}

	// Build the workers (one per Workers, at least one) before taking the lock.
	// Each binds the single durable just created; the server load-balances the
	// pull consumer across them.
	n := workerCount(spec)
	workers := make([]*pumpWorker, 0, n)
	pumpCtxs := make([]context.Context, 0, n)
	for i := 0; i < n; i++ {
		pumpCtx, cancel := context.WithCancel(context.Background())
		workers = append(workers, &pumpWorker{cancel: cancel, done: make(chan struct{}), state: newPumpState()})
		pumpCtxs = append(pumpCtxs, pumpCtx)
	}
	mc := &managedConsumer{spec: spec, workers: workers}

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		cancelAll(workers)
		return fmt.Errorf("substrate: ConsumerSupervisor: Add after Stop")
	}
	if _, exists := s.managed[spec.Name]; exists {
		s.mu.Unlock()
		cancelAll(workers)
		return nil
	}
	s.managed[spec.Name] = mc
	s.mu.Unlock()

	for i, w := range workers {
		w := w
		pumpCtx := pumpCtxs[i]
		go func() {
			defer close(w.done)
			s.runPump(pumpCtx, spec, w.state)
		}()
	}
	return nil
}

// cancelAll cancels every worker's pump context (used to unwind a race-lost Add).
func cancelAll(workers []*pumpWorker) {
	for _, w := range workers {
		w.cancel()
	}
}

// Remove stops the pump for name and deletes its server-side durable. If name is
// not managed, Remove is a no-op. Deleting the durable is the caller's explicit
// intent (operator retiring a consumer), distinct from Stop, which preserves
// durables.
func (s *ConsumerSupervisor) Remove(ctx context.Context, name string) error {
	s.mu.Lock()
	mc, exists := s.managed[name]
	if !exists {
		s.mu.Unlock()
		return nil
	}
	delete(s.managed, name)
	s.mu.Unlock()

	stopWorkers(mc.workers)

	if err := s.conn.js.DeleteConsumer(ctx, mc.spec.Stream, name); err != nil &&
		!errors.Is(err, jetstream.ErrConsumerNotFound) {
		return fmt.Errorf("substrate: ConsumerSupervisor: remove %q: %w", name, err)
	}
	return nil
}

// stopWorkers cancels every worker's pump context, then waits for each goroutine
// to exit — the shared shutdown sequence for Remove and Stop.
func stopWorkers(workers []*pumpWorker) {
	for _, w := range workers {
		w.cancel()
	}
	for _, w := range workers {
		<-w.done
	}
}

// Reset deletes and recreates the durable for name (preserving the spec's
// delivery policy and all other config) and points the pump at the new durable.
// The delete is unconditional and ErrConsumerNotFound-tolerant (TOCTOU
// hardening): it runs whether or not the durable is locally known, so a durable
// that exists in NATS but not in the registry is still recreated cleanly. If name
// is not managed, an optional spec override may be supplied via UpdateSpec before
// Reset; otherwise Reset on an unmanaged name returns an error.
//
// Reset is the migration target for Refractor's rebuild delete-recreate-swap.
//
// Reset does not wait for the pumps: requestReopen is a non-blocking signal, so
// Reset returns while each pump still has its old iterator open. A caller that
// holds a bounded resource across a reset, and wants that resource to cover the
// handover rather than half of it, wants ResetAwaitReopen instead.
func (s *ConsumerSupervisor) Reset(ctx context.Context, name string) error {
	mc, _, err := s.recreateDurable(ctx, name)
	if err != nil {
		return err
	}
	// Signal every pump to re-open its iterator against the recreated durable.
	for _, w := range mc.workers {
		w.state.requestReopen()
	}
	return nil
}

// ResetAwaitReopen is Reset plus a bounded, BEST-EFFORT wait for each pump to
// have re-opened against the recreated durable.
//
// It exists for a caller that holds a bounded resource across a reset — a
// concurrency slot taken so the server is not asked for many simultaneous
// durable transitions at once. Reset alone returns while every pump still holds
// its old iterator, so such a caller releases its resource with the handover
// half-done and the bound covers less than it names.
//
// EXACTLY what the wait guarantees, which is less than "the pump has reopened
// against the durable I just created":
//
//   - It guarantees the acknowledgement was not one the caller failed to wait
//     for. The snapshot is taken before the reopen is requested, so an open that
//     had already completed cannot satisfy it.
//   - It does NOT guarantee the acknowledging open was of the NEW durable. A
//     pump that finished opening the OLD durable and had not yet announced it
//     will announce into this caller's snapshot, releasing it early. Closing
//     that would mean tying the acknowledgement to the durable's identity, which
//     is a larger mechanism than the guarantee is worth: the consequence is the
//     same as an expired budget below — a resource released early, with nothing
//     left inconsistent, and the pump reopening on its own regardless.
//
// So this is a handover BARRIER, best-effort, not a handover proof.
//
// It also does NOT cover the DRAIN. Once the iterator is open, the redelivery
// behind it is ordinary pump traffic. A busy consumer's pump never goes idle in
// steady state, so waiting for the drain would hold the caller's resource for
// that consumer's whole lifetime and stall every other reset behind it. The
// boundary is "the consumer is being read again", not "the backlog is gone".
//
// The wait is BOUNDED and non-fatal. A pump only notices the reopen request when
// its current message returns from the handler (the drain's watcher calls Stop
// on the iterator, which does not interrupt a running handler), and a handler's
// latency is not bounded by this package — see keepAckAlive. So an unbounded
// wait here is a stall waiting to happen. On expiry this logs and returns nil:
// the durable HAS been recreated, and the pump reopens on its own
// nextReopenDelay backoff, so a timeout means "we stopped waiting", not "the
// reset failed". A cancelled ctx is different in kind — the caller is gone, not
// merely impatient — and returns ctx.Err().
//
// A worker holding any pause reason is not waited on. A paused pump cannot
// reopen until an operator Resume, so waiting on one burns the whole budget to
// learn nothing; it is signalled like any other and left to reopen when it is
// resumed.
//
// Overlapping calls on one consumer are safe and independent: the
// acknowledgement is a channel close, so one waiter cannot consume another's.
//
// A non-positive wait requests the reopen and returns without waiting.
func (s *ConsumerSupervisor) ResetAwaitReopen(ctx context.Context, name string, wait time.Duration) error {
	mc, spec, err := s.recreateDurable(ctx, name)
	if err != nil {
		return err
	}

	// Snapshot BEFORE requesting: reopenSnapshot returns the channel the next
	// open will close, so taking it afterwards could hand back one a completed
	// open had already installed, which nothing further would close.
	reopened := make([]<-chan struct{}, 0, len(mc.workers))
	for _, w := range mc.workers {
		if w.state.anyReason() {
			continue
		}
		reopened = append(reopened, w.state.reopenSnapshot())
	}
	for _, w := range mc.workers {
		w.state.requestReopen()
	}
	if wait <= 0 || len(reopened) == 0 {
		return nil
	}

	// One timer for the whole call, not one per worker: the caller gave a single
	// budget, and N workers must not multiply it into N × wait.
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for _, ch := range reopened {
		select {
		case <-ch:
		case <-ctx.Done():
			return fmt.Errorf("substrate: ConsumerSupervisor: reset %q: awaiting reopen: %w", name, ctx.Err())
		case <-timer.C:
			specLogger(spec).Warn("substrate: ConsumerSupervisor: reset recreated the durable but its pump did not reopen within the wait; "+
				"the pump retries on its own backoff",
				"consumer", name, "wait", wait)
			return nil
		}
	}
	return nil
}

// recreateDurable is the delete-recreate half both reset entry points share. It
// returns the managed consumer so the caller can drive its workers, and the spec
// the recreate actually used; it does not touch the workers itself, because the
// two entry points differ precisely in what they do with them afterwards.
//
// The spec is COPIED out under s.mu and everything downstream reads the copy.
// mc.spec is mutable shared state — UpdateSpec rewrites it in place under the
// same lock, and a reset is exactly what a caller runs right after an UpdateSpec
// — so reading the field directly across the JetStream calls is both a data race
// and a torn read: the delete could address one stream and the create another.
// The copy is shallow, which is all that is needed; UpdateSpec swaps whole
// fields rather than mutating through them.
//
// The pair runs under the consumer's own resetMu (see its declaration for what
// that does and does not cover). Lock order: s.mu is taken only for the registry
// lookup and the copy, and released before resetMu is acquired, so no goroutine
// ever waits for a per-consumer reset while holding the registry lock.
func (s *ConsumerSupervisor) recreateDurable(ctx context.Context, name string) (*managedConsumer, ConsumerSpec, error) {
	s.mu.Lock()
	mc, exists := s.managed[name]
	var spec ConsumerSpec
	if exists {
		spec = mc.spec
	}
	s.mu.Unlock()
	if !exists {
		return nil, ConsumerSpec{}, fmt.Errorf("substrate: ConsumerSupervisor: reset %q: not managed", name)
	}

	mc.resetMu.Lock()
	defer mc.resetMu.Unlock()

	// Unconditional delete (TOCTOU-safe): tolerate ErrConsumerNotFound.
	if err := s.conn.js.DeleteConsumer(ctx, spec.Stream, name); err != nil &&
		!errors.Is(err, jetstream.ErrConsumerNotFound) {
		return nil, ConsumerSpec{}, fmt.Errorf("substrate: ConsumerSupervisor: reset %q: delete: %w", name, err)
	}

	if _, err := s.createConsumer(ctx, spec); err != nil {
		return nil, ConsumerSpec{}, fmt.Errorf("substrate: ConsumerSupervisor: reset %q: create: %w", name, err)
	}
	return mc, spec, nil
}

// UpdateSpec replaces the desired spec for an already-managed consumer without
// recreating the durable. Used to change a spec's FilterSubject (etc.) before a
// Reset recreates the durable with the new config. Returns an error if name is
// not managed. Hooks and config captured by the running pump are refreshed; the
// pump picks up the new handler/classify/probe atomically.
func (s *ConsumerSupervisor) UpdateSpec(name string, mutate func(*ConsumerSpec)) error {
	s.mu.Lock()
	mc, exists := s.managed[name]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("substrate: ConsumerSupervisor: update %q: not managed", name)
	}
	mutate(&mc.spec)
	updated := mc.spec
	workers := mc.workers
	s.mu.Unlock()
	for _, w := range workers {
		w.state.updateSpec(updated)
	}
	return nil
}

// Stop stops every pump but does NOT delete any durable: a durable's persisted
// position is the point of its durability (substrate doctrine). Callers that
// want delete-on-shutdown invoke Remove per consumer from their own adapter
// layer. After Stop the supervisor rejects further Add calls.
func (s *ConsumerSupervisor) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	managed := make([]*managedConsumer, 0, len(s.managed))
	for _, mc := range s.managed {
		managed = append(managed, mc)
	}
	s.managed = make(map[string]*managedConsumer)
	s.mu.Unlock()

	// Cancel every worker across every consumer first, then wait — so all pumps
	// tear down concurrently rather than serially per consumer.
	for _, mc := range managed {
		for _, w := range mc.workers {
			w.cancel()
		}
	}
	for _, mc := range managed {
		for _, w := range mc.workers {
			<-w.done
		}
	}
}

// Pause manually pauses the pump for name (operator control surface; FR30 / 9.4
// disable). Idempotent. A manual pause is cleared only by Resume, never by a
// passing probe. Returns true iff name was managed and the pause was applied;
// false (no-op) if name is not managed. The bool lets a control surface fuse the
// managed-check and the act into one lock acquisition, with no check-then-act
// gap a concurrent Remove could slip through.
func (s *ConsumerSupervisor) Pause(ctx context.Context, name string) bool {
	s.mu.Lock()
	mc, exists := s.managed[name]
	s.mu.Unlock()
	if !exists {
		return false
	}
	// An operator pause is lane-wide: fan out to every worker, then persist once
	// (the workers share one durable / one health key).
	for _, w := range mc.workers {
		w.state.addReason(PauseManual)
	}
	s.persistPaused(ctx, mc.spec, PauseManual, "")
	return true
}

// Resume clears manual + structural pauses for name and force-exits an in-flight
// infra probe loop, so processing resumes without waiting for the next probe
// (FR31). No-op if name is not managed.
//
// Resume only clears pause reasons that were active at the moment it was
// called. A pause reason added AFTER a Resume — e.g. a structural escalation
// discovered by the probe loop, or a fresh infra failure on the next pump
// iteration — is NOT retroactively cleared by that earlier Resume; the new
// failure re-enters its own pause state and requires its own Resume.
//
// Returns true iff name was managed and the resume was applied; false (no-op)
// if name is not managed — the bool lets a control surface fuse the
// managed-check and the act into one lock acquisition.
func (s *ConsumerSupervisor) Resume(ctx context.Context, name string) bool {
	s.mu.Lock()
	mc, exists := s.managed[name]
	s.mu.Unlock()
	if !exists {
		return false
	}
	// Lane-wide resume: clear manual + structural on every worker, then persist
	// once.
	for _, w := range mc.workers {
		w.state.operatorResume()
	}
	s.persistActive(context.WithoutCancel(ctx), mc.spec)
	return true
}

// IsManaged reports whether name is currently in the supervisor's managed set.
// Read under the same lock that guards Add/Remove, so it is a consistent,
// race-free view at the call instant. It is the authoritative allow-list for an
// operator control surface: Pause/Resume are silent no-ops on an unmanaged name,
// so a caller validates IsManaged first to turn an unknown name into an explicit
// error rather than a silently-dropped command.
func (s *ConsumerSupervisor) IsManaged(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.managed[name]
	return ok
}

// ManagedNames returns the names of every currently-managed consumer, read under
// the registry lock. The returned slice is a fresh copy the caller owns; order is
// unspecified (it is the Go map-iteration order). It is the authoritative name
// set for an operator control surface — the lazily-populated health/state caches
// elsewhere are not a reliable allow-list.
func (s *ConsumerSupervisor) ManagedNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.managed))
	for name := range s.managed {
		names = append(names, name)
	}
	return names
}

// PendingForConsumer returns the number of pending (un-delivered) messages for
// the named consumer's durable, queried by durable name. Returns an error if the
// consumer info cannot be read. Exposed as a substrate-typed accessor so callers
// (e.g. Refractor's rebuild lag-watch) need no jetstream.Consumer handle.
func (s *ConsumerSupervisor) PendingForConsumer(ctx context.Context, name string) (uint64, error) {
	info, err := s.consumerInfo(ctx, name, "pending")
	if err != nil {
		return 0, err
	}
	return info.NumPending, nil
}

// OutstandingForConsumer returns the number of messages the named consumer has
// not finished with: the un-delivered backlog (NumPending) plus the messages
// delivered and still awaiting acknowledgement (NumAckPending). A message the
// pump has fetched but not yet acked leaves NumPending, so NumPending alone
// reads zero while work is still in flight — callers asking "has this consumer
// drained?" (e.g. Refractor's rebuild-completion watch) must use this, not
// PendingForConsumer, which answers the narrower "how deep is the backlog?"
// that a lag/backlog metric wants.
func (s *ConsumerSupervisor) OutstandingForConsumer(ctx context.Context, name string) (uint64, error) {
	info, err := s.consumerInfo(ctx, name, "outstanding")
	if err != nil {
		return 0, err
	}
	ackPending := info.NumAckPending
	if ackPending < 0 {
		ackPending = 0
	}
	return info.NumPending + uint64(ackPending), nil
}

// AckFloorForConsumer returns the named durable's persisted ack floor — the
// JetStream stream sequence up to which every message is acked. It survives a
// process restart (the durable, not the process, owns it), so a caller can
// seed in-process forward-progress state from it at startup instead of
// starting cold at zero. Returns an error if the consumer info cannot be read.
func (s *ConsumerSupervisor) AckFloorForConsumer(ctx context.Context, name string) (uint64, error) {
	info, err := s.consumerInfo(ctx, name, "ack floor")
	if err != nil {
		return 0, err
	}
	return info.AckFloor.Stream, nil
}

// AckStats is the pair a caller needs to tell "caught up" from "wedged", read
// together so both describe the same instant.
//
// NumPending alone cannot make that distinction: a consumer that has been
// handed everything and cannot finish it reports NumPending 0, exactly like one
// that is genuinely drained. AckPending is the work it still owes, and AckFloor
// is whether that work is being retired.
type AckStats struct {
	// AckPending is the count of messages delivered but not yet acked.
	AckPending uint64
	// AckFloor is the stream sequence up to which every message is acked.
	AckFloor uint64
}

// AckStatsForConsumer returns the named durable's un-acked count and ack floor
// from a single ConsumerInfo read, so a caller polling both does not pay two
// round trips or risk reading them from two different instants.
func (s *ConsumerSupervisor) AckStatsForConsumer(ctx context.Context, name string) (AckStats, error) {
	info, err := s.consumerInfo(ctx, name, "ack stats")
	if err != nil {
		return AckStats{}, err
	}
	ackPending := info.NumAckPending
	if ackPending < 0 {
		ackPending = 0
	}
	return AckStats{AckPending: uint64(ackPending), AckFloor: info.AckFloor.Stream}, nil
}

// consumerInfo reads the live ConsumerInfo for a managed durable. op names the
// calling accessor so the error identifies which read failed.
func (s *ConsumerSupervisor) consumerInfo(ctx context.Context, name, op string) (*jetstream.ConsumerInfo, error) {
	s.mu.Lock()
	mc, exists := s.managed[name]
	s.mu.Unlock()
	if !exists {
		return nil, fmt.Errorf("substrate: ConsumerSupervisor: %s %q: not managed", op, name)
	}
	cons, err := s.conn.js.Consumer(ctx, mc.spec.Stream, name)
	if err != nil {
		return nil, fmt.Errorf("substrate: ConsumerSupervisor: %s %q: consumer: %w", op, name, err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("substrate: ConsumerSupervisor: %s %q: info: %w", op, name, err)
	}
	return info, nil
}

// createConsumer creates (idempotently) the durable described by spec. The
// supervisor never sets MaxDeliver — retry count stays unbounded.
func (s *ConsumerSupervisor) createConsumer(ctx context.Context, spec ConsumerSpec) (jetstream.Consumer, error) {
	cfg := jetstream.ConsumerConfig{
		Durable:       spec.Name,
		DeliverPolicy: toJetstreamDeliverPolicy(spec.DeliverPolicy),
		AckPolicy:     jetstream.AckExplicitPolicy,
	}
	// FilterSubjects (the multi-filter set) and FilterSubject (the single filter)
	// are mutually exclusive on a JetStream consumer config — setting both is
	// rejected by the server. The set form takes precedence when supplied.
	if len(spec.FilterSubjects) > 0 {
		cfg.FilterSubjects = spec.FilterSubjects
	} else {
		cfg.FilterSubject = spec.FilterSubject
	}
	if spec.DeliverGroup != "" {
		cfg.DeliverGroup = spec.DeliverGroup
	}
	if spec.AckWait > 0 {
		cfg.AckWait = spec.AckWait
	}
	if spec.MaxAckPending > 0 {
		cfg.MaxAckPending = spec.MaxAckPending
	}
	cons, err := s.conn.js.CreateOrUpdateConsumer(ctx, spec.Stream, cfg)
	if err != nil {
		// The client rejects a create/update response whose echoed config lacks
		// the requested plural FilterSubjects, inferring "server too old" — but
		// that inference can be wrong: the server can have stored the filters
		// while only the response echo (or its parse) glitched, observed under
		// a boot burst against a 2.14 server that handles plural filters
		// correctly when probed directly. The server is the authority on what
		// was stored, so on exactly this error, read the consumer back: if the
		// stored config carries the requested filter set, the create succeeded
		// and the handle is served as such — otherwise the error stands and the
		// caller's broad-filter fallback proceeds.
		if len(cfg.FilterSubjects) > 0 && errors.Is(err, jetstream.ErrConsumerMultipleFilterSubjectsNotSupported) {
			if verified, verr := s.verifyStoredFilterSubjects(ctx, spec.Stream, spec.Name, cfg.FilterSubjects); verr == nil && verified != nil {
				return verified, nil
			}
		}
		return nil, fmt.Errorf("substrate: ConsumerSupervisor: create consumer %q on %q: %w",
			spec.Name, spec.Stream, err)
	}
	return cons, nil
}

// verifyStoredFilterSubjects reads consumer name on stream back from the
// server and returns its handle iff the stored config's FilterSubjects match
// want exactly (order-insensitive). Returns (nil, nil) on a clean mismatch —
// the caller's original error stands.
func (s *ConsumerSupervisor) verifyStoredFilterSubjects(ctx context.Context, stream, name string, want []string) (jetstream.Consumer, error) {
	cons, err := s.conn.js.Consumer(ctx, stream, name)
	if err != nil {
		return nil, err
	}
	info, err := cons.Info(ctx)
	if err != nil {
		return nil, err
	}
	got := info.Config.FilterSubjects
	if len(got) != len(want) {
		return nil, nil
	}
	wantSet := make(map[string]struct{}, len(want))
	for _, w := range want {
		wantSet[w] = struct{}{}
	}
	for _, g := range got {
		if _, ok := wantSet[g]; !ok {
			return nil, nil
		}
	}
	return cons, nil
}

func toJetstreamDeliverPolicy(p DeliverPolicy) jetstream.DeliverPolicy {
	if p == DeliverLastPerSubject {
		return jetstream.DeliverLastPerSubjectPolicy
	}
	return jetstream.DeliverAllPolicy
}

func validateSpec(spec ConsumerSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("substrate: ConsumerSupervisor: spec.Name required")
	}
	if spec.Stream == "" {
		return fmt.Errorf("substrate: ConsumerSupervisor: spec %q: Stream required", spec.Name)
	}
	if spec.Handler == nil {
		return fmt.Errorf("substrate: ConsumerSupervisor: spec %q: Handler required", spec.Name)
	}
	if spec.FilterSubject != "" && len(spec.FilterSubjects) > 0 {
		return fmt.Errorf("substrate: ConsumerSupervisor: spec %q: FilterSubject and FilterSubjects are mutually exclusive", spec.Name)
	}
	return nil
}

func specLogger(spec ConsumerSpec) *slog.Logger {
	if spec.Logger != nil {
		return spec.Logger
	}
	return slog.Default()
}

// persistActive / persistPaused funnel every supervisor state transition through
// the spec's HealthSink. A nil sink skips the I/O; sink errors are logged, never
// fatal (mirrors the pipeline's setHealthActive/setHealthPaused guards).
func (s *ConsumerSupervisor) persistActive(ctx context.Context, spec ConsumerSpec) {
	if spec.Health == nil {
		return
	}
	if err := spec.Health.SetActive(ctx); err != nil {
		specLogger(spec).Error("substrate: ConsumerSupervisor: health set active",
			"consumer", spec.Name, "error", err)
	}
}

func (s *ConsumerSupervisor) persistPaused(ctx context.Context, spec ConsumerSpec, reason PauseReason, lastErr string) {
	if spec.Health == nil {
		return
	}
	if err := spec.Health.SetPaused(ctx, reason, lastErr); err != nil {
		specLogger(spec).Error("substrate: ConsumerSupervisor: health set paused",
			"consumer", spec.Name, "reason", reason, "error", err)
	}
}

// effectiveProbeInterval / effectiveRedeliveryDelay resolve the spec's tunables
// against their package defaults.
func effectiveProbeInterval(spec ConsumerSpec) time.Duration {
	if spec.ProbeInterval > 0 {
		return spec.ProbeInterval
	}
	return DefaultProbeInterval
}
