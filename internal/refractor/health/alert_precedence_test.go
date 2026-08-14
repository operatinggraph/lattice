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
	// Worst first, as ONE ordered list. Each neighbour pair is asserted, so the
	// whole chain is pinned without asserting every combination — and the list
	// is then checked for completeness against the live table, so a token added
	// to alertRank without a decision about where it belongs fails here by name
	// instead of landing at whatever rank its author happened to type.
	order := []string{
		// A read model that is CONFIDENTLY wrong outranks one that is merely
		// frozen: a paused lens misleads nobody, a null indistinguishable from a
		// lawful erasure misleads everybody.
		"secure-redaction",
		"paused",
		"unreadable",
		"repair-failing",
		"repair-blocked",
		"sweep-stalled",
		// The audit's own halt, one rank quieter than the sweep's: the sweep
		// both detects and repairs, so its silence stops repairs too, while the
		// audit is read-only and its silence costs verdicts alone.
		"audit-stalled",
		"unverified",
		// A named, bounded wrongness an operator can act on, below the unknown
		// of unknown size that "unverified" is, and above a read model that is
		// merely behind and will catch up on its own.
		"diverged",
		"lagging",
		// The quietest token, and the only one that describes a lens which is
		// fine right now: it reports a window that has already closed, so
		// anything currently wrong displaces it. Still above "ok", because the
		// recovery skipped the events the pause window swallowed.
		"structural-pause-auto-recovered",
		"ok",
	}
	for i := 0; i+1 < len(order); i++ {
		worse, better := order[i], order[i+1]
		require.Greater(t, alertRank[worse], alertRank[better],
			"%q must outrank %q", worse, better)
	}

	listed := make(map[string]bool, len(order))
	for _, token := range order {
		require.False(t, listed[token], "%q is listed twice, so the order it pins is ambiguous", token)
		listed[token] = true
		_, declared := alertRank[token]
		require.True(t, declared, "%q is ordered here but absent from alertRank, so nothing raises it", token)
	}
	for token := range alertRank {
		if token == "" {
			// The empty token is "no alert", a synonym of ok rather than a rank
			// of its own — it is deliberately not in the order.
			continue
		}
		require.True(t, listed[token],
			"alertRank declares %q but this order does not place it. Decide where it belongs and add it: a token "+
				"nobody ordered still competes for the single-valued field, and its rank is then whatever its "+
				"author happened to type.", token)
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
