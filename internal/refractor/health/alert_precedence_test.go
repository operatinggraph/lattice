package health

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAlertRank_TotalOrder pins the per-lens `alert` precedence as a TABLE.
// The field is single-valued and several of its conditions can hold at once —
// a lens can be lagging AND holding a row the guard refuses to let the sweep
// repair — so the order is real policy. Encoding it in the call sequence of the
// branches that set the field (what this replaced) makes it invisible: a
// reviewer has to read every branch in order to learn it, and a branch inserted
// in the wrong place changes the policy silently.
func TestAlertRank_TotalOrder(t *testing.T) {
	// Worst first. Each neighbour pair is asserted, so the whole chain is
	// pinned without asserting every combination.
	order := []string{
		"paused",
		"unreadable",
		"repair-failing",
		"repair-blocked",
		"sweep-stalled",
		"unverified",
		"lagging",
		"ok",
	}
	for i := 0; i+1 < len(order); i++ {
		worse, better := order[i], order[i+1]
		require.Greater(t, alertRank[worse], alertRank[better],
			"%q must outrank %q", worse, better)
	}
}

// TestRaiseAlert_KeepsTheWorst proves a caller never has to know what else may
// already be set — the property that lets each condition raise independently.
func TestRaiseAlert_KeepsTheWorst(t *testing.T) {
	t.Run("a worse candidate wins", func(t *testing.T) {
		require.Equal(t, "repair-blocked", raiseAlert("lagging", "repair-blocked"))
	})
	t.Run("a better candidate does not displace", func(t *testing.T) {
		require.Equal(t, "paused", raiseAlert("paused", "repair-failing"))
		require.Equal(t, "repair-failing", raiseAlert("repair-failing", "repair-blocked"))
	})
	t.Run("an empty candidate is inert", func(t *testing.T) {
		require.Equal(t, "lagging", raiseAlert("lagging", ""))
	})
	t.Run("an empty current is the same as ok", func(t *testing.T) {
		require.Equal(t, "unverified", raiseAlert("", "unverified"))
	})
	t.Run("ok yields to everything above it", func(t *testing.T) {
		for _, a := range []string{"lagging", "unverified", "sweep-stalled", "repair-blocked", "repair-failing", "unreadable", "paused"} {
			require.Equal(t, a, raiseAlert("ok", a))
		}
	})
	t.Run("an unknown value never displaces a known one", func(t *testing.T) {
		// A value absent from the table ranks 0, so a typo degrades to "does
		// not raise" rather than silently outranking a real alert.
		require.Equal(t, "repair-blocked", raiseAlert("repair-blocked", "repare-blocked"))
	})
}
