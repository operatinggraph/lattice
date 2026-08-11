package pipeline

import "sync"

// DefaultPeakRowsBufferSize is the observation window's capacity: the number
// of most-recent evaluations whose peak binding rows the buffer keeps. Sized
// like the latency window (DefaultLatencyBufferSize) because it answers the
// same shape of question over the same event stream.
const DefaultPeakRowsBufferSize = 128

// PeakRowsRingBuffer holds the peak binding rows of the most recent
// evaluations of one lens, and reports the largest of them. It is the gauge
// that sizes what a lens materializes against the engine's binding-set cap:
// an operator reads it when an evaluation is refused, and it is the
// measurement that would justify reviving lazy binding expansion (a single
// lens peaking within an order of magnitude of the cap).
//
// The window is ROLLING, mirroring LatencyRingBuffer: a new sample overwrites
// the oldest, and reads do NOT clear it. An all-time monotonic maximum is
// deliberately NOT what this reports — one pathological anchor would pin it
// forever, and a gauge that can only ever rise says nothing about now.
//
// Lifetime, at every boundary the buffer can cross:
//
//	process restart   Created empty, per process. The stored health value is
//	                  never blanked by a restart: Snapshot reports Count == 0
//	                  until the first evaluation, and the publisher skips the
//	                  write on an empty window rather than writing a zero over
//	                  a real prior observation.
//	lens rebuild      CARRIED, not reset. The buffer belongs to the Pipeline,
//	                  which survives a rebuild, and a rescan's evaluations are
//	                  evaluations of the same cypher over the widest anchor
//	                  set the lens ever walks — precisely the samples worth
//	                  keeping. The window ages them out on its own once
//	                  steady-state traffic resumes.
//	pause -> resume   CARRIED. A paused lens records nothing, so the window
//	                  freezes with its last N samples and the reported peak
//	                  stops moving; it resumes ageing at the first evaluation
//	                  after resume. A frozen gauge on a paused lens is correct
//	                  — the lens is not materializing anything to measure.
//	replay            Recorded like any other evaluation. A re-delivered event
//	                  really does materialize its binding set again, so it
//	                  earns a sample; there is no dedup by anchor.
//	retry             Every attempt records its own sample, including each
//	                  footprint-drift re-execution and each retry-queue
//	                  redelivery — the same posture the latency buffer takes,
//	                  and for the same reason: the work was really done.
//	hot reload        CARRIED. A reloaded cypher's samples enter the same
//	                  window, which can briefly mix two cyphers' peaks and
//	                  ages the older ones out within one window's worth of
//	                  evaluations.
//
// Ordering within an evaluation: the sample is recorded once the engine
// returns, before the pipeline acts on any error, so a REFUSED evaluation
// contributes the peak that refused it.
type PeakRowsRingBuffer struct {
	mu       sync.Mutex
	samples  []int
	next     int
	capacity int
}

// NewPeakRowsRingBuffer returns an empty buffer with the given capacity.
// A capacity <= 0 falls back to DefaultPeakRowsBufferSize.
func NewPeakRowsRingBuffer(capacity int) *PeakRowsRingBuffer {
	if capacity <= 0 {
		capacity = DefaultPeakRowsBufferSize
	}
	return &PeakRowsRingBuffer{
		samples:  make([]int, 0, capacity),
		capacity: capacity,
	}
}

// Record appends one evaluation's peak binding rows. A negative sample is
// discarded rather than clamped — the engine never produces one, and silently
// folding it to zero would hide a wiring fault behind a plausible number.
// Safe to call from multiple goroutines.
func (b *PeakRowsRingBuffer) Record(rows int) {
	if rows < 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.samples) < b.capacity {
		b.samples = append(b.samples, rows)
		return
	}
	b.samples[b.next] = rows
	b.next = (b.next + 1) % b.capacity
}

// PeakRowsStats is the summarised view of one window at one publish cycle.
// Count == 0 means the window holds no samples at all — no evaluation has run
// since this process started the lens — and a publisher must treat that as
// "nothing to say", not as a peak of zero. A recorded 0 is a real observation
// (an evaluation whose first pattern matched nothing) and lands with Count 1.
type PeakRowsStats struct {
	Count int
	Peak  int
}

// Snapshot returns the largest peak over the buffered samples. The buffer is
// left intact — rolling-window semantics, so successive reads see the same
// samples until new evaluations displace them.
func (b *PeakRowsRingBuffer) Snapshot() PeakRowsStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.samples) == 0 {
		return PeakRowsStats{}
	}
	peak := b.samples[0]
	for _, s := range b.samples[1:] {
		if s > peak {
			peak = s
		}
	}
	return PeakRowsStats{Count: len(b.samples), Peak: peak}
}
