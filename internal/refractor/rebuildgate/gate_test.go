package rebuildgate_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/rebuildgate"
)

// pollUntil waits for check to hold, without a fixed sleep standing in for
// synchronization.
func pollUntil(t *testing.T, timeout time.Duration, check func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(msg)
}

// TestGate_HoldsTheGlobalBoundUnderFanOut pins the property the gate exists
// for: however many keys fan out at once, only `limit` fn bodies run together.
//
// Proven from both sides — the peak never exceeds the bound, AND the bound is
// actually reached (the test waits for saturation before releasing anything),
// so a gate that ran everything one at a time would not satisfy it.
func TestGate_HoldsTheGlobalBoundUnderFanOut(t *testing.T) {
	const limit = 3
	const callers = 24

	g := rebuildgate.New(limit)
	release := make(chan struct{})

	var inFlight, maxInFlight, completed atomic.Int64
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		key := fmt.Sprintf("lens-%02d", i)
		go func() {
			defer wg.Done()
			_ = g.Do(context.Background(), key, func() error {
				cur := inFlight.Add(1)
				for {
					peak := maxInFlight.Load()
					if cur <= peak || maxInFlight.CompareAndSwap(peak, cur) {
						break
					}
				}
				<-release
				inFlight.Add(-1)
				completed.Add(1)
				return nil
			})
		}()
	}

	pollUntil(t, 5*time.Second, func() bool {
		return maxInFlight.Load() >= limit
	}, "the gate never saturated — the bound was never actually exercised")
	close(release)
	wg.Wait()

	assert.EqualValues(t, limit, maxInFlight.Load(),
		"at most %d fn bodies may run at once across all keys", limit)
	assert.EqualValues(t, callers, completed.Load(), "every caller must eventually run its own fn")
}

// TestGate_SameKeyNeverOverlaps pins the per-key exclusion, and that the second
// caller runs its OWN fn rather than joining the first.
//
// Joining would be silently wrong for both callers this gate has: a taxonomy
// rebuild joining an operator rebuild that started before the taxonomy changed
// would miss the change, and a truncate=true call joining a truncate=false one
// would report success for work never done.
func TestGate_SameKeyNeverOverlaps(t *testing.T) {
	g := rebuildgate.New(4) // ample slots: any serialization here is the KEY's doing.

	firstRunning := make(chan struct{})
	release := make(chan struct{})
	var inFlight, maxInFlight, calls atomic.Int64

	body := func() error {
		cur := inFlight.Add(1)
		for {
			peak := maxInFlight.Load()
			if cur <= peak || maxInFlight.CompareAndSwap(peak, cur) {
				break
			}
		}
		if calls.Add(1) == 1 {
			close(firstRunning)
			<-release
		}
		inFlight.Add(-1)
		return nil
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		require.NoError(t, g.Do(context.Background(), "same", body))
	}()
	<-firstRunning

	wg.Add(1)
	go func() {
		defer wg.Done()
		require.NoError(t, g.Do(context.Background(), "same", body))
	}()

	// The second caller must still be waiting: nothing but the first call
	// finishing can let it in. Asserted over a window rather than at an instant —
	// a bare read here would be satisfied by a goroutine the runtime has simply
	// not scheduled yet, which is not the same claim at all.
	require.Never(t, func() bool { return calls.Load() > 1 }, 200*time.Millisecond, 5*time.Millisecond,
		"a second Do on a live key must wait, not run alongside")
	close(release)
	wg.Wait()

	assert.EqualValues(t, 1, maxInFlight.Load(), "two Do calls on one key may never overlap")
	assert.EqualValues(t, 2, calls.Load(),
		"the waiting caller must run its OWN fn — the gate serializes, it never coalesces")
}

// TestGate_DifferentKeysRunConcurrently is the other half of the same claim: at
// a limit above one the gate must not be accidentally serial. Without this, a
// gate that simply took one global lock would pass every exclusion assertion
// above while destroying the throughput the bound was chosen to preserve.
func TestGate_DifferentKeysRunConcurrently(t *testing.T) {
	g := rebuildgate.New(2)

	both := make(chan struct{}, 2)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for _, key := range []string{"lens-a", "lens-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, g.Do(context.Background(), key, func() error {
				both <- struct{}{}
				<-release
				return nil
			}))
		}()
	}

	pollUntil(t, 5*time.Second, func() bool { return len(both) == 2 },
		"two different keys must be able to run at once at limit 2")
	close(release)
	wg.Wait()
}

// TestGate_CancelledWhileWaitingForTheKeyDoesNotRun pins that a caller giving
// up returns ctx.Err() and never runs fn — a rebuild abandoned at the gate must
// not be reported, or run later, as one that happened.
func TestGate_CancelledWhileWaitingForTheKeyDoesNotRun(t *testing.T) {
	g := rebuildgate.New(4)

	holding := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		require.NoError(t, g.Do(context.Background(), "held", func() error {
			close(holding)
			<-release
			return nil
		}))
	}()
	<-holding

	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Bool
	errCh := make(chan error, 1)
	go func() {
		errCh <- g.Do(ctx, "held", func() error {
			ran.Store(true)
			return nil
		})
	}()
	cancel()

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, context.Canceled, "a caller cancelled at the key must report its context error")
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled caller never returned")
	}
	assert.False(t, ran.Load(), "fn must not run for a caller that gave up waiting")

	close(release)
	wg.Wait()
}

// TestGate_CancelledWhileWaitingForASlotDoesNotRun is the same claim on the
// other wait. The key is free here; the global bound is what the caller is
// queued behind.
func TestGate_CancelledWhileWaitingForASlotDoesNotRun(t *testing.T) {
	g := rebuildgate.New(1)

	holding := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		require.NoError(t, g.Do(context.Background(), "occupies-the-slot", func() error {
			close(holding)
			<-release
			return nil
		}))
	}()
	<-holding

	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Bool
	errCh := make(chan error, 1)
	go func() {
		errCh <- g.Do(ctx, "a-free-key", func() error {
			ran.Store(true)
			return nil
		})
	}()
	cancel()

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, context.Canceled, "a caller cancelled at the slot must report its context error")
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled caller never returned")
	}
	assert.False(t, ran.Load(), "fn must not run for a caller that gave up waiting")

	close(release)
	wg.Wait()

	// The abandoned caller must not have stranded the key it briefly held.
	require.NoError(t, g.Do(context.Background(), "a-free-key", func() error { return nil }))
}

// TestGate_PanickingFnLeavesTheGateUsable pins that the key lock and the slot
// are handed back on the panic path. A rebuild that panics must cost its own
// caller, not wedge every other lens's rebuild behind a slot nobody holds.
func TestGate_PanickingFnLeavesTheGateUsable(t *testing.T) {
	g := rebuildgate.New(1)

	func() {
		defer func() {
			assert.NotNil(t, recover(), "the panic must propagate to the caller, not be swallowed")
		}()
		_ = g.Do(context.Background(), "panics", func() error {
			panic("rebuild blew up")
		})
	}()

	done := make(chan error, 1)
	go func() {
		done <- g.Do(context.Background(), "panics", func() error { return nil })
	}()
	select {
	case err := <-done:
		require.NoError(t, err, "the same key must be acquirable again after a panic")
	case <-time.After(5 * time.Second):
		t.Fatal("a panicking fn stranded its key lock")
	}

	done2 := make(chan error, 1)
	go func() {
		done2 <- g.Do(context.Background(), "another-key", func() error { return nil })
	}()
	select {
	case err := <-done2:
		require.NoError(t, err, "the global slot must be returned after a panic")
	case <-time.After(5 * time.Second):
		t.Fatal("a panicking fn stranded its slot")
	}
}

// TestGate_ReturnsFnErrorUnchanged pins that the gate is transparent to the
// caller's own failure: a rebuild error must reach the logger that classifies
// it, not be replaced by a gate-shaped one.
func TestGate_ReturnsFnErrorUnchanged(t *testing.T) {
	g := rebuildgate.New(2)
	sentinel := errors.New("target unreachable")
	assert.ErrorIs(t, g.Do(context.Background(), "k", func() error { return sentinel }), sentinel)
}

// TestGate_NonPositiveLimitIsBoundedNotUnbounded pins the fail-tight default. A
// forgotten or mis-computed limit must come out at 1 — the failure mode this
// type exists to prevent is the UNBOUNDED one, so no argument may reach it.
func TestGate_NonPositiveLimitIsBoundedNotUnbounded(t *testing.T) {
	for _, limit := range []int{0, -1} {
		g := rebuildgate.New(limit)

		var inFlight, maxInFlight atomic.Int64
		release := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			key := fmt.Sprintf("k%d", i)
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = g.Do(context.Background(), key, func() error {
					cur := inFlight.Add(1)
					for {
						peak := maxInFlight.Load()
						if cur <= peak || maxInFlight.CompareAndSwap(peak, cur) {
							break
						}
					}
					<-release
					inFlight.Add(-1)
					return nil
				})
			}()
		}
		pollUntil(t, 5*time.Second, func() bool { return maxInFlight.Load() >= 1 },
			"nothing ever ran")
		close(release)
		wg.Wait()
		assert.EqualValues(t, 1, maxInFlight.Load(),
			"New(%d) must bound at 1, never run unbounded", limit)
	}
}
