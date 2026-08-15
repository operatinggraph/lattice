package main

import (
	"context"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/rebuildgate"
)

// taxonomyRebuildConcurrency bounds how many Pipeline.Rebuild CALLS this
// scheduler may have in flight at any moment, across every lens — the whole
// fan-out of one taxonomy event or one MATCH edit, not a per-entry limit.
//
// A bound is required because each Rebuild call deletes and recreates the
// lens's durable JetStream consumer and waits for that consumer's pump to reopen
// against the replacement (substrate.ConsumerSupervisor.ResetAwaitReopen). One
// taxonomy event re-derives every live lens carrying `*`, and a live corpus
// runs on the order of a hundred one-per-lens consumers on KV_core-kv — so an
// unbounded sweep asks the NATS server for that many simultaneous durable
// delete-recreates, which is enough to take the server down. Two, rather than
// one, keeps a single slow rebuild from stalling the whole corpus behind it
// while holding the peak at roughly what a single rebuild already costs.
//
// The bound is GLOBAL across the paths that start a rebuild, not one bound per
// path: a rebuild costs the NATS server the same whichever path asked for it, so
// per-path bounds would leave the sum unbounded. cmd/refractor builds ONE
// rebuildgate.Gate at this limit and installs it on both this scheduler and
// control.Service (SetRebuildGate), so a taxonomy sweep and an operator sweep of
// the same corpus share these slots rather than holding a set each. The gate
// also serializes per lens, so the two paths cannot land on the same lens at
// once.
//
// Two things this deliberately does NOT bound, so the claim is not read wider
// than it is enforced. Both are choices, with the same reasoning: a slot held
// across a DRAIN stalls the paths that can actually burst.
//
//   - The REPLAY. The slot covers the whole handover — delete, recreate, and the
//     pump re-opening its iterator on the replacement — but the redelivery that
//     pump then drains runs outside it. A busy lens's pump never goes idle in
//     steady state, so a slot held to the end of the drain would be a slot held
//     for the lens's lifetime. So this bounds concurrent consumer HANDOVERS, not
//     concurrent replay traffic.
//
//   - The SYNCHRONOUS rebuild arm — control.Service.RebuildRule, which the
//     class-key destruction handler calls to attest an erasure. It takes no slot
//     because it needs none and could not afford one.
//
//     It needs none because it cannot burst, and the reason is a COUNTING
//     argument about this process, not a lock: main.go constructs exactly one
//     classkeyshredded.Manager, its handler runs inline on one durable consumer
//     that processes one message at a time, and the handler walks its targets
//     sequentially. So at most one RebuildRule exists process-wide at any
//     instant. (Pipeline.rebuildSerial is NOT what supplies this: by its own doc
//     it serializes RebuildAndWait callers against each other and nothing more,
//     and the gated Pipeline.Rebuild never takes it.)
//
//     It could not afford one because it waits for the rescan to DRAIN, on a
//     budget of classkeyshredded.DefaultRebuildWait — tens of minutes. A slot
//     held across that would freeze every taxonomy and operator rebuild in the
//     corpus for the window, to bound a path that produces at most one rebuild
//     at a time.
//
//     The cost of leaving it out, stated rather than hidden: process-wide
//     rebuild concurrency is this bound PLUS AT MOST ONE, and because the gate's
//     per-lens exclusion cannot see this arm, a synchronous rebuild of lens X may
//     overlap a gated rebuild of lens X. The two then contend on that lens's
//     handover barrier (ConsumerSupervisor.ResetAwaitReopen), which is built for
//     concurrent waiters: its acknowledgement releases every waiter at once, so
//     the contention is bounded and non-fatal.
const taxonomyRebuildConcurrency = 2

// taxonomyRebuildSettle is how long the scheduler waits for the queue to go
// quiet before it starts running jobs.
//
// A single logical taxonomy change often arrives as a BURST of events — a
// re-parent is a link delete followed by a link create, and the intermediate
// state between them has the subtype detached from its parent. A rebuild that
// ran on that intermediate state would truncate and re-derive against a
// taxonomy nobody ever intended, then immediately do it again for the real one.
// Waiting for a quiet period collapses a sweep's worth of moved entries into
// one pass, which is what the design asks the coalescing to do.
//
// It is per-SWEEP, not per-job: the deadline is pushed out by each new enqueue,
// so N entries queued together still cost one window in total, not N.
const taxonomyRebuildSettle = 150 * time.Millisecond

// taxonomyRebuildJob is one entry's owed rebuild, queued for a worker.
//
// key is the lens ID, and it is the gate's serialization key: it is what makes
// this job mutually exclusive with the operator control plane's rebuild of the
// SAME lens, which runs on the same gate from a different goroutine entirely.
//
// run drives the rebuild loop for entry and is responsible for releasing that
// entry's single-flight latch (pipelineEntry.taxRebuildRunning) on its way out.
// abandon releases the same latch WITHOUT rebuilding — what a shutdown drain
// calls instead of run, so a queued entry does not stay latched as "a rebuild
// is being driven" when no goroutine is driving one.
type taxonomyRebuildJob struct {
	entry   *pipelineEntry
	key     string
	run     func()
	abandon func()
}

// taxonomyRebuildScheduler serializes the taxonomy rebuild fan-out behind
// taxonomyRebuildConcurrency workers, so one taxonomy event cannot burst N
// concurrent durable delete-recreates at the NATS server.
//
// Enqueue never blocks. Every job is submitted from CoreKVSource's single
// dispatch goroutine — the one every lens's CDC events, spec reloads and
// tombstones share — so a scheduler that applied backpressure there would stall
// the entire source rather than just the rebuild sweep. The queue is therefore
// an unbounded slice guarded by mu, with cond waking a worker, never a
// fixed-capacity channel.
//
// State table, for the zero value this is used as (reloader embeds it by
// value; there is no constructor):
//
//   - created: lazily, on the first enqueue — cond, the queued set and the
//     worker goroutines are all built under mu the first time a job arrives, so
//     a reloader that never rebuilds (every unit test that constructs one)
//     starts no goroutines at all.
//   - queued: an entry is added to `queued` at enqueue and removed the moment a
//     worker DEQUEUES it. From then until the job finishes, coalescing is held
//     by the entry's own taxRebuildRunning latch, which the enqueuer checks
//     before it ever reaches this type — so the two latches overlap and there is
//     no window in which a fresh publication is dropped by one while the other
//     has already let go. A duplicate enqueue is therefore unreachable; one that
//     arrived anyway is ABANDONED rather than dropped, so the latch it was
//     holding is handed back instead of wedging the lens forever.
//   - drained/reset at shutdown: stop() flips `stopped` and broadcasts. Workers
//     finish the job in hand, then abandon every remaining queued job — clearing
//     the single-flight latch and LEAVING taxRebuildPending set, which is the
//     truthful record (the rebuild is still owed; the next taxonomy event drives
//     it) — and exit when the queue is empty. Enqueue after stop() abandons
//     immediately rather than queueing work nothing will run.
//   - settling: a worker waits for the queue to go quiet (taxonomyRebuildSettle)
//     before it takes anything off it, so a burst of events describing one
//     logical change is rebuilt once, from the settled state, rather than once
//     per intermediate state.
//   - an entry deleted from the registry while queued: the job still runs, and
//     that is safe. pipelineDeleter.Delete calls Pipeline.RemoveConsumer, which
//     drops the durable from the supervisor's managed set, and Pipeline.rebuild
//     establishes that the consumer is still managed BEFORE it truncates — so
//     the rebuild fails without clearing a target nothing would re-derive, and
//     never panics.
type taxonomyRebuildScheduler struct {
	mu     sync.Mutex
	cond   *sync.Cond
	queue  []taxonomyRebuildJob
	queued map[*pipelineEntry]struct{}
	// quietAt is when the current burst is considered settled — pushed out by
	// every enqueue, waited on by every worker before it runs a job.
	quietAt time.Time
	// settle overrides taxonomyRebuildSettle. Zero means the constant; tests
	// that are not about the settling behaviour set it to a negligible value so
	// they do not pay the window.
	settle  time.Duration
	started bool
	stopped bool
	// gate is what actually holds the fan-out down, and it is SHARED with the
	// operator control plane (control.Service.SetRebuildGate) so the two paths
	// bound one total rather than one each — cmd/refractor installs the same
	// instance on both. A worker occupies one of its slots for the whole of
	// job.run, and holds the job's key for that long too, so no other path can
	// rebuild that lens concurrently.
	//
	// Filled in lazily at the first enqueue when nothing installed one, at
	// taxonomyRebuildConcurrency. A nil gate is never "unbounded": the many
	// tests that construct a bare &reloader{} would otherwise be the one shape
	// that reaches the NATS server with no ceiling, and an un-wired path must
	// come out tighter than intended, never looser.
	gate *rebuildgate.Gate
	// ctx is the process context the workers exit on, captured on the call that
	// starts them (enqueue is the only caller that sees one) and used as the
	// gate wait's context so a shutdown does not leave a worker queued behind a
	// slot forever. Never nil once started — context.Background() stands in for
	// a test reloader that wires none, and has a nil Done channel, which is
	// exactly the "wait forever" this field otherwise supplies.
	ctx context.Context
}

// setGate installs the gate this scheduler's workers run their jobs on — the
// instance cmd/refractor also gives control.Service, so the reload path and the
// operator control plane share one bound.
//
// A nil gate is ignored, leaving the lazily-built default in place. Every
// installation path here is fail-tight in the same direction: nothing a caller
// passes can leave this scheduler running its fan-out unbounded.
func (s *taxonomyRebuildScheduler) setGate(g *rebuildgate.Gate) {
	if g == nil {
		return
	}
	s.mu.Lock()
	s.gate = g
	s.mu.Unlock()
}

// settleWindow is the quiet period this scheduler waits out. Caller holds mu.
func (s *taxonomyRebuildScheduler) settleWindow() time.Duration {
	if s.settle > 0 {
		return s.settle
	}
	return taxonomyRebuildSettle
}

// enqueue submits job and returns immediately. ctx is the process context the
// workers exit on; it is read only on the call that starts them.
func (s *taxonomyRebuildScheduler) enqueue(ctx context.Context, job taxonomyRebuildJob) {
	s.mu.Lock()
	if s.cond == nil {
		s.cond = sync.NewCond(&s.mu)
	}
	if s.queued == nil {
		s.queued = make(map[*pipelineEntry]struct{})
	}
	if s.gate == nil {
		s.gate = rebuildgate.New(taxonomyRebuildConcurrency)
	}
	if !s.started {
		s.started = true
		s.startWorkersLocked(ctx)
	}
	if s.stopped {
		s.mu.Unlock()
		job.abandon()
		return
	}
	if _, dup := s.queued[job.entry]; dup {
		s.mu.Unlock()
		// Unreachable while the entry's own taxRebuildRunning latch is taken
		// before every enqueue: it is held from before this call until after the
		// job is dequeued, so a second job for one entry cannot be queued.
		// Abandoned rather than dropped anyway — dropping it silently would
		// strand that latch true forever, and a wedged lens is a far worse
		// failure than the redundant release this costs if the invariant holds.
		job.abandon()
		return
	}
	s.queued[job.entry] = struct{}{}
	s.queue = append(s.queue, job)
	// Every enqueue pushes the settle deadline out, so a burst settles once.
	s.quietAt = time.Now().Add(s.settleWindow())
	s.mu.Unlock()
	s.cond.Broadcast()
}

// startWorkersLocked starts the fixed worker pool and the shutdown watcher.
// Called once, under mu, from the first enqueue.
//
// A nil ctx (a test reloader that wires none) and context.Background() both
// have a nil Done channel, and a watcher on one would block forever for no
// purpose, so neither gets one — the workers then live for the process. The nil
// is normalized away here rather than at every use, because a worker passes this
// context to the gate on every job and a nil one there would panic.
func (s *taxonomyRebuildScheduler) startWorkersLocked(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.ctx = ctx
	for i := 0; i < taxonomyRebuildConcurrency; i++ {
		go s.worker()
	}
	if ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			s.stop()
		}()
	}
}

// stop puts the scheduler into its draining state: no further job is run, every
// queued job is abandoned, and the workers exit once the queue is empty.
func (s *taxonomyRebuildScheduler) stop() {
	s.mu.Lock()
	s.stopped = true
	cond := s.cond
	s.mu.Unlock()
	if cond != nil {
		cond.Broadcast()
	}
}

func (s *taxonomyRebuildScheduler) worker() {
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.stopped {
			s.cond.Wait()
		}
		if len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		// Wait the burst out before taking anything off the queue. Sleeping
		// OUTSIDE the lock keeps enqueue non-blocking (it is called from the
		// source's single dispatch goroutine), and re-reading quietAt afterwards
		// is what makes a later enqueue extend the window rather than race it. A
		// draining scheduler skips the wait: its jobs are about to be abandoned.
		if wait := time.Until(s.quietAt); wait > 0 && !s.stopped {
			s.mu.Unlock()
			timer := time.NewTimer(wait)
			<-timer.C
			timer.Stop()
			continue
		}
		job := s.queue[0]
		s.queue = s.queue[1:]
		delete(s.queued, job.entry)
		stopped := s.stopped
		gate, ctx := s.gate, s.ctx
		s.mu.Unlock()

		if stopped {
			job.abandon()
			continue
		}
		// The job runs INSIDE the gate, so this worker occupies one of the
		// shared slots — and the lens's key — for the whole of run, including
		// its re-derive loop. Waiting for the slot happens here, outside mu, for
		// the same reason the settle wait does: enqueue is called from
		// CoreKVSource's single dispatch goroutine and must never block behind
		// another path's rebuild.
		//
		// Every way Do can return an error, enumerated so the arm below is not
		// read as covering more than it does:
		//
		//   - ctx cancelled while queued for the lens's KEY — another path is
		//     rebuilding this same lens (the operator control op).
		//   - ctx cancelled while queued for a SLOT.
		//   - whatever fn returns.
		//
		// The third is unreachable here, but as a property of THIS CALLER rather
		// than of Do: the closure discards job.run's outcome and returns nil
		// unconditionally, because a failed rebuild is already recorded on the
		// lens's health entry by driveTaxonomyRebuild. So an error here always
		// means ctx died — either wait, it does not matter which — and ctx is the
		// process context, so that is the shutdown drain. It takes the drain's
		// answer: hand the latch back, leave taxRebuildPending set as the truthful
		// record that the rebuild is still owed, and let the next taxonomy event
		// drive it.
		if err := gate.Do(ctx, job.key, func() error { job.run(); return nil }); err != nil {
			job.abandon()
		}
	}
}
