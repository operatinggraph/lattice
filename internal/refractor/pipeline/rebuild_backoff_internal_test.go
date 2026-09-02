package pipeline

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestNextRebuildPollDelay_GrowsWhileFlatAndResetsOnDecrease pins
// watchRebuildCompletion's poll-delay schedule in isolation from the
// supervisor/NATS machinery it actually runs against: the first observation
// holds at the floor, a steady or growing outstanding count doubles the delay
// up to the cap, doubling clamps rather than overshoots the cap, and a strict
// decrease resets to the floor regardless of how far the delay had grown —
// so a rebuild that starts draining again is checked promptly right when a
// completion might be near.
func TestNextRebuildPollDelay_GrowsWhileFlatAndResetsOnDecrease(t *testing.T) {
	const floor = 500 * time.Millisecond
	const capDelay = 5 * time.Second

	d := nextRebuildPollDelay(floor, floor, capDelay, true, false)
	require.Equal(t, floor, d, "the first observation has nothing to compare against and holds at the floor")

	d = nextRebuildPollDelay(d, floor, capDelay, false, false)
	require.Equal(t, 2*floor, d, "outstanding held steady: the delay doubles")
	d = nextRebuildPollDelay(d, floor, capDelay, false, false)
	require.Equal(t, 4*floor, d, "still flat: doubles again")

	// A strict decrease resets to the floor from wherever the delay had grown.
	d = nextRebuildPollDelay(d, floor, capDelay, false, true)
	require.Equal(t, floor, d)

	// Growth against racing writes behaves exactly like "held steady" —
	// neither brings the rebuild closer to outstanding == 0.
	d = nextRebuildPollDelay(d, floor, capDelay, false, false)
	require.Equal(t, 2*floor, d, "a growing count also doubles — only a decrease resets")

	// Doubling clamps at the cap rather than ever overshooting it.
	require.Equal(t, capDelay, nextRebuildPollDelay(capDelay, floor, capDelay, false, false),
		"already at the cap: stays at the cap")
	require.Equal(t, capDelay, nextRebuildPollDelay(capDelay-1, floor, capDelay, false, false),
		"a value just under the cap clamps to the cap when doubled, not past it")

	// The cap is escaped only by a decrease, same as any other value.
	require.Equal(t, floor, nextRebuildPollDelay(capDelay, floor, capDelay, false, true))
}
