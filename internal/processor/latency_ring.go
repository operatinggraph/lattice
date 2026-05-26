// Per-call latency ring buffer for Processor step 3. Ported from
// `internal/refractor/pipeline/latency.go` — deliberately copied rather than
// shared because at one consumer the shared package would be premature
// factoring.
package processor

import (
	"sort"
	"sync"
	"time"
)

// LatencyStats is the snapshot view of a ring at heartbeat tick.
// Count==0 means no samples — heartbeat callers may use that to skip
// emission for staleness (Decision #4) or to emit a zero-count signal
// for liveness (Decision #5 — step3-latency always emits).
type LatencyStats struct {
	Count int
	Mean  time.Duration
	P95   time.Duration
	P99   time.Duration
}

type latencyRing struct {
	mu       sync.Mutex
	samples  []time.Duration
	next     int
	capacity int
}

func newLatencyRing(capacity int) *latencyRing {
	if capacity <= 0 {
		capacity = 128
	}
	return &latencyRing{
		samples:  make([]time.Duration, 0, capacity),
		capacity: capacity,
	}
}

func (r *latencyRing) record(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.samples) < r.capacity {
		r.samples = append(r.samples, d)
		return
	}
	r.samples[r.next] = d
	r.next = (r.next + 1) % r.capacity
}

func (r *latencyRing) snapshot() LatencyStats {
	r.mu.Lock()
	if len(r.samples) == 0 {
		r.mu.Unlock()
		return LatencyStats{}
	}
	cp := make([]time.Duration, len(r.samples))
	copy(cp, r.samples)
	r.mu.Unlock()

	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	var total time.Duration
	for _, s := range cp {
		total += s
	}
	mean := total / time.Duration(len(cp))
	return LatencyStats{
		Count: len(cp),
		Mean:  mean,
		P95:   ringPercentile(cp, 0.95),
		P99:   ringPercentile(cp, 0.99),
	}
}

func ringPercentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := int(float64(len(sorted))*q + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
