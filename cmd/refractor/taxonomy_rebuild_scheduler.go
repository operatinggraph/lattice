package main

import (
	"context"
	"sync"
	"time"
)

// taxonomyRebuildConcurrency bounds how many Pipeline.Rebuild CALLS this
// scheduler may have in flight at any moment, across every lens — the whole
// fan-out of one taxonomy event or one MATCH edit, not a per-entry limit.
//
// A bound is required because each Rebuild call deletes and recreates the
// lens's durable JetStream consumer (substrate.ConsumerSupervisor.Reset). One
// taxonomy event re-derives every live lens carrying `*`, and a live corpus
// runs on the order of a hundred one-per-lens consumers on KV_core-kv — so an
// unbounded sweep asks the NATS server for that many simultaneous durable
// delete-recreates, which is enough to take the server down. Two, rather than
// one, keeps a single slow rebuild from stalling the whole corpus behind it
// while holding the peak at roughly what a single rebuild already costs.
//
// Two things it deliberately does NOT bound, so the claim is not read wider
// than it is enforced:
//
//   - The REPLAY. Reset returns once the durable is recreated and the pumps are
//     asked to reopen; the redelivery those pumps then drain runs outside the
//     slot. So this bounds concurrent consumer-management calls, not concurrent
//     replay traffic.
//   - The operator control plane. control.Service.rebuildRule spawns one
//     goroutine per request with no coordination, so an operator rebuilding a
//     corpus from the control plane reproduces the unbounded burst this
//     scheduler exists to prevent on the reload path. Routing that path through
//     this scheduler needs the control service to reach the reloader, which it
//     has no handle on today — a named residual, not something this constant
//     covers.
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
// run drives the rebuild loop for entry and is responsible for releasing that
// entry's single-flight latch (pipelineEntry.taxRebuildRunning) on its way out.
// abandon releases the same latch WITHOUT rebuilding — what a shutdown drain
// calls instead of run, so a queued entry does not stay latched as "a rebuild
// is being driven" when no goroutine is driving one.
type taxonomyRebuildJob struct {
	entry   *pipelineEntry
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
// purpose, so neither gets one — the workers then live for the process.
func (s *taxonomyRebuildScheduler) startWorkersLocked(ctx context.Context) {
	for i := 0; i < taxonomyRebuildConcurrency; i++ {
		go s.worker()
	}
	if ctx != nil && ctx.Done() != nil {
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
		s.mu.Unlock()

		if stopped {
			job.abandon()
			continue
		}
		job.run()
	}
}
