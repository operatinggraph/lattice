package processor

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ExecutorImpl is the step-5 implementation (Starlark Execute). It runs the
// operation's class script (hydrated at step 4) against the ScriptContext and
// returns the proposed ScriptResult. DDL validation of the result is step 6.
//
// It also owns the step-5 wall observation: every execution that reached
// Runner.Run — success or abort — is one sample of {wall duration, live reads,
// listings} in a 128-deep ring, and every ScriptTimeout additionally bumps a
// cumulative counter. The heartbeat reads both through Step5Stats.
type ExecutorImpl struct {
	Runner *StarlarkRunner
	Logger *slog.Logger

	ring          *step5Ring
	ringOnce      sync.Once
	timeoutsTotal atomic.Uint64
}

// Step5Stats is the snapshot view of the step-5 ring at a heartbeat tick.
// Count==0 means no execution has run yet: the latency figures are zero and the
// two means are meaningless, which is why the emitter renders them as null
// rather than 0 (Contract #5 §5.4).
//
// TimeoutsTotal is NOT a ring statistic — it is cumulative since the executor
// was constructed and never resets, so a reader derives a rate by diffing two
// ticks. The ring behind the other figures is overwrite-only with no time
// window: Count is occupancy and pins at the capacity, so a burst's p99
// persists until 128 newer executions displace it.
type Step5Stats struct {
	Count         int
	Mean          time.Duration
	P95           time.Duration
	P99           time.Duration
	MeanLiveReads float64
	MeanListings  float64
	TimeoutsTotal uint64
}

// step5Sample is one execution's cost: how long Runner.Run took and how many
// Core KV round trips the script issued from inside it.
type step5Sample struct {
	wall      time.Duration
	liveReads int
	listings  int
}

// step5Ring is a fixed-capacity overwrite-only ring of step5Samples, with the
// same semantics as latencyRing (capacity 128, no time window, occupancy never
// decreases) widened to three columns so the read and listing means come from
// the same window as the latency percentiles.
type step5Ring struct {
	mu       sync.Mutex
	samples  []step5Sample
	next     int
	capacity int
}

func newStep5Ring(capacity int) *step5Ring {
	if capacity <= 0 {
		capacity = 128
	}
	return &step5Ring{
		samples:  make([]step5Sample, 0, capacity),
		capacity: capacity,
	}
}

func (r *step5Ring) record(s step5Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.samples) < r.capacity {
		r.samples = append(r.samples, s)
		return
	}
	r.samples[r.next] = s
	r.next = (r.next + 1) % r.capacity
}

func (r *step5Ring) snapshot() Step5Stats {
	r.mu.Lock()
	if len(r.samples) == 0 {
		r.mu.Unlock()
		return Step5Stats{}
	}
	cp := make([]step5Sample, len(r.samples))
	copy(cp, r.samples)
	r.mu.Unlock()

	n := len(cp)
	var totalWall time.Duration
	var totalReads, totalLists int
	walls := make([]time.Duration, n)
	for i, s := range cp {
		walls[i] = s.wall
		totalWall += s.wall
		totalReads += s.liveReads
		totalLists += s.listings
	}
	sort.Slice(walls, func(i, j int) bool { return walls[i] < walls[j] })
	return Step5Stats{
		Count:         n,
		Mean:          totalWall / time.Duration(n),
		P95:           ringPercentile(walls, 0.95),
		P99:           ringPercentile(walls, 0.99),
		MeanLiveReads: float64(totalReads) / float64(n),
		MeanListings:  float64(totalLists) / float64(n),
	}
}

// NewExecutor constructs an Executor with the given runner. Pass nil to
// use a default-budget runner.
func NewExecutor(runner *StarlarkRunner, logger *slog.Logger) *ExecutorImpl {
	if runner == nil {
		runner = NewStarlarkRunner(0, 0)
	}
	if logger == nil {
		logger = slog.Default()
	}
	e := &ExecutorImpl{Runner: runner, Logger: logger}
	e.stats()
	return e
}

// stats returns the executor's ring, creating it on first use so an
// ExecutorImpl assembled as a struct literal observes its executions too.
func (e *ExecutorImpl) stats() *step5Ring {
	e.ringOnce.Do(func() { e.ring = newStep5Ring(128) })
	return e.ring
}

// Step5Stats returns the current window: latency over the last 128 executions
// of this process, the mean per-execution live-read and listing counts over the
// same window, and the cumulative timeout count since construction. Live —
// never reset, and gone when the process is.
func (e *ExecutorImpl) Step5Stats() Step5Stats {
	s := e.stats().snapshot()
	s.TimeoutsTotal = e.timeoutsTotal.Load()
	return s
}

// Execute implements Executor.
func (e *ExecutorImpl) Execute(ctx context.Context, env *OperationEnvelope, state HydratedState) (ScriptResult, error) {
	rid := env.RequestID
	sc := state.Context
	if sc.Operation == nil {
		// Defensive: someone constructed HydratedState without going
		// through HydratorImpl.
		sc.Operation = env
	}
	if sc.ScriptSource == "" {
		return ScriptResult{}, &ScriptError{
			Code:               "ScriptError",
			Message:            "no script source in hydrated state — step 4 may have been skipped",
			OperationRequestID: rid,
		}
	}

	start := time.Now()
	result, err := e.Runner.Run(ctx, sc)
	wall := time.Since(start)

	// The counters come straight off the recorder rather than through record(),
	// which sorts every set to build a value nothing here reads.
	var liveReads, listings int
	if sc.ReadRecorder != nil {
		liveReads = sc.ReadRecorder.liveReadCalls
		listings = sc.ReadRecorder.listCalls
	}
	e.stats().record(step5Sample{wall: wall, liveReads: liveReads, listings: listings})

	if err != nil {
		// Same discrimination classifyStepError applies to this error a few
		// frames up: *ScriptError carries the script's own code, *HydrationError
		// the miss classification.
		code := "InternalError"
		var sErr *ScriptError
		var hErr *HydrationError
		switch {
		case errors.As(err, &sErr):
			code = sErr.Code
			if code == "ScriptTimeout" {
				e.timeoutsTotal.Add(1)
			}
		case errors.As(err, &hErr):
			code = hErr.Code
		}
		e.Logger.Info("step 5: aborted",
			"requestId", rid,
			"class", sc.ScriptClass,
			"code", code,
			"wallMs", wall.Milliseconds(),
			"liveReads", liveReads,
			"listings", listings,
		)
		// Already typed — *ScriptError, or *HydrationError when the script
		// touched a declared read step 4 recorded absent. classifyStepError
		// discriminates both, so pass it through unwrapped.
		return ScriptResult{}, err
	}

	e.Logger.Info("step 5: executed",
		"requestId", rid,
		"class", sc.ScriptClass,
		"mutations", len(result.Mutations),
		"events", len(result.Events),
		"wallMs", wall.Milliseconds(),
		"liveReads", liveReads,
		"listings", listings,
	)

	return result, nil
}
