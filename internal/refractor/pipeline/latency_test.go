// Unit tests for the per-Lens projection-latency ring buffer — the NFR-P3
// "primary instrument" surfaced on the Refractor heartbeat as lens.<name> p95/
// p99/mean/count. Pure, deterministic math; no NATS, no goroutines.
package pipeline

import (
	"testing"
	"time"
)

func TestNewLatencyRingBuffer_CapacityFallback(t *testing.T) {
	for _, in := range []int{0, -1, -100} {
		b := NewLatencyRingBuffer(in)
		if b.capacity != DefaultLatencyBufferSize {
			t.Fatalf("capacity(%d) = %d, want fallback %d", in, b.capacity, DefaultLatencyBufferSize)
		}
	}
	if b := NewLatencyRingBuffer(8); b.capacity != 8 {
		t.Fatalf("capacity = %d, want 8", b.capacity)
	}
}

func TestLatencyRingBuffer_EmptySnapshot(t *testing.T) {
	got := NewLatencyRingBuffer(4).Snapshot()
	if got != (LatencyStats{}) {
		t.Fatalf("empty buffer Snapshot = %+v, want zero value", got)
	}
	if got.Count != 0 {
		t.Fatalf("empty Count = %d, want 0", got.Count)
	}
}

func TestLatencyRingBuffer_SingleSample(t *testing.T) {
	b := NewLatencyRingBuffer(4)
	b.Record(7 * time.Millisecond)
	got := b.Snapshot()
	want := LatencyStats{Count: 1, Mean: 7 * time.Millisecond, P95: 7 * time.Millisecond, P99: 7 * time.Millisecond}
	if got != want {
		t.Fatalf("single-sample Snapshot = %+v, want %+v", got, want)
	}
}

func TestLatencyRingBuffer_MeanAndPercentiles(t *testing.T) {
	// A fixed 1..100 ms series (100 samples, capacity 128 so no wrap).
	b := NewLatencyRingBuffer(128)
	for i := 1; i <= 100; i++ {
		b.Record(time.Duration(i) * time.Millisecond)
	}
	got := b.Snapshot()
	if got.Count != 100 {
		t.Fatalf("Count = %d, want 100", got.Count)
	}
	// Mean of 1..100 = 50.5ms; integer-division truncates to 50ms with ns units.
	wantMean := (5050 * time.Millisecond) / 100
	if got.Mean != wantMean {
		t.Fatalf("Mean = %v, want %v", got.Mean, wantMean)
	}
	// Nearest-rank: rank = ceil(q*n). p95 over 1..100 → rank 95 → 95ms; p99 → 99ms.
	if got.P95 != 95*time.Millisecond {
		t.Fatalf("P95 = %v, want 95ms", got.P95)
	}
	if got.P99 != 99*time.Millisecond {
		t.Fatalf("P99 = %v, want 99ms", got.P99)
	}
}

func TestLatencyRingBuffer_WrapAroundKeepsNewest(t *testing.T) {
	// Capacity 4, record 6 samples: the two oldest (1,2) are overwritten, leaving
	// {3,4,5,6}. Snapshot must reflect only the most-recent window.
	b := NewLatencyRingBuffer(4)
	for i := 1; i <= 6; i++ {
		b.Record(time.Duration(i) * time.Millisecond)
	}
	got := b.Snapshot()
	if got.Count != 4 {
		t.Fatalf("Count after wrap = %d, want 4 (capacity)", got.Count)
	}
	// Mean of {3,4,5,6} = 4.5ms → 4ms after truncation.
	if want := (18 * time.Millisecond) / 4; got.Mean != want {
		t.Fatalf("Mean after wrap = %v, want %v (newest window only)", got.Mean, want)
	}
	// Max sample is 6ms; p99 (nearest-rank, rank=4) is the largest retained sample.
	if got.P99 != 6*time.Millisecond {
		t.Fatalf("P99 after wrap = %v, want 6ms", got.P99)
	}
	// The overwritten oldest value (1ms) must not survive as the min/p-low.
	if got.P95 < 3*time.Millisecond {
		t.Fatalf("P95 = %v includes an overwritten sample (<3ms)", got.P95)
	}
}

func TestLatencyRingBuffer_SnapshotDoesNotDrain(t *testing.T) {
	// Rolling-window semantics (Decision #5): reads leave samples intact.
	b := NewLatencyRingBuffer(8)
	b.Record(5 * time.Millisecond)
	b.Record(15 * time.Millisecond)
	first := b.Snapshot()
	second := b.Snapshot()
	if first != second {
		t.Fatalf("Snapshot drained the buffer: %+v then %+v", first, second)
	}
	if second.Count != 2 {
		t.Fatalf("Count = %d after two reads, want 2 (buffer intact)", second.Count)
	}
}

func TestPercentile_EdgeCases(t *testing.T) {
	if got := percentile(nil, 0.95); got != 0 {
		t.Fatalf("percentile(empty) = %v, want 0", got)
	}
	if got := percentile([]time.Duration{42 * time.Millisecond}, 0.99); got != 42*time.Millisecond {
		t.Fatalf("percentile(single) = %v, want 42ms", got)
	}
	sorted := []time.Duration{
		10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond,
		40 * time.Millisecond, 50 * time.Millisecond,
	}
	// q=0 clamps rank to 1 → the smallest; q=1 → the largest.
	if got := percentile(sorted, 0); got != 10*time.Millisecond {
		t.Fatalf("percentile(q=0) = %v, want 10ms (clamped to rank 1)", got)
	}
	if got := percentile(sorted, 1.0); got != 50*time.Millisecond {
		t.Fatalf("percentile(q=1) = %v, want 50ms", got)
	}
	// q=0.5 over 5 samples, nearest-rank ceil(2.5)=3 → 30ms (the median).
	if got := percentile(sorted, 0.5); got != 30*time.Millisecond {
		t.Fatalf("percentile(q=0.5) = %v, want 30ms", got)
	}
}
