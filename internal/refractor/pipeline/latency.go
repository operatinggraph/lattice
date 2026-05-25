// Per-Lens projection latency ring buffer. Each pipeline that enables
// latency tracking owns one buffer; the heartbeater
// (health.LatticeHeartbeater) reads from it at tick and publishes
// mean/p95/p99 under `health.refractor.<instance>.lens.<canonicalName>`.
package pipeline

import (
	"sort"
	"sync"
	"time"
)

// DefaultLatencyBufferSize is the default ring buffer capacity per
// Decision #5. 128 samples per Lens, summarised per heartbeat.
const DefaultLatencyBufferSize = 128

// LatencyRingBuffer captures recent per-event projection latencies for
// one Lens pipeline. Inserts are O(1) under a mutex; summarisation is
// O(n log n) at heartbeat tick (n ≤ size). The buffer is rolling — new
// samples overwrite the oldest. Reads do NOT clear the buffer; the
// heartbeater always sees the most recent N samples.
type LatencyRingBuffer struct {
	mu       sync.Mutex
	samples  []time.Duration
	next     int
	capacity int
}

// NewLatencyRingBuffer returns an empty buffer with the given capacity.
// A capacity ≤ 0 falls back to DefaultLatencyBufferSize.
func NewLatencyRingBuffer(capacity int) *LatencyRingBuffer {
	if capacity <= 0 {
		capacity = DefaultLatencyBufferSize
	}
	return &LatencyRingBuffer{
		samples:  make([]time.Duration, 0, capacity),
		capacity: capacity,
	}
}

// Record appends one latency sample. Safe to call from multiple
// goroutines (the pipeline currently records on a single goroutine but
// the lock keeps this contract explicit).
func (b *LatencyRingBuffer) Record(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.samples) < b.capacity {
		b.samples = append(b.samples, d)
		return
	}
	b.samples[b.next] = d
	b.next = (b.next + 1) % b.capacity
}

// LatencyStats is the summarised view of one ring buffer at one
// heartbeat tick. Count == 0 means no samples were recorded since the
// pipeline started; the heartbeater treats that case as "skip the
// `lens.<canonicalName>.*` keys" rather than emitting zero values.
type LatencyStats struct {
	Count int
	Mean  time.Duration
	P95   time.Duration
	P99   time.Duration
}

// Snapshot returns the current mean/p95/p99 over the buffered samples.
// The buffer is left intact (rolling window semantics per Decision #5).
func (b *LatencyRingBuffer) Snapshot() LatencyStats {
	b.mu.Lock()
	if len(b.samples) == 0 {
		b.mu.Unlock()
		return LatencyStats{}
	}
	// Copy so we can sort without exposing internal state.
	cp := make([]time.Duration, len(b.samples))
	copy(cp, b.samples)
	b.mu.Unlock()

	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	var total time.Duration
	for _, s := range cp {
		total += s
	}
	mean := total / time.Duration(len(cp))
	return LatencyStats{
		Count: len(cp),
		Mean:  mean,
		P95:   percentile(cp, 0.95),
		P99:   percentile(cp, 0.99),
	}
}

// percentile returns the value at the given quantile from a sorted
// slice using the nearest-rank method. For n == 1 returns sorted[0].
func percentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	// Nearest-rank: rank = ceil(q * n), 1-indexed; clamp to [1, n].
	rank := int(float64(len(sorted))*q + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
