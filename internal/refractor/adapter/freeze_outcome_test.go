package adapter_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGuardedWrite_PostTruncateStrayWriteDoesNotFreezeReplay is the outcome
// test for the whole fire: it pins the END STATE the supersession refusal
// exists to prevent, and proves that end state is now visible rather than
// reported as a heal.
//
// The sequence it reproduces (sweep-rule-snapshot-granularity-design.md §3.2):
// a MATCH hot-reload swaps the rule synchronously and truncates the lens's rows
// on a NEW goroutine. A sweep pass still mid-loop then writes an old-rule row
// into the now-empty target — where the absent-key branch takes it
// unconditionally — stamped at the consumer HEAD. The rebuild's replay carries
// each event's own stream sequence, all at or below that head, so the guard
// drops every one. The actor is frozen holding an old-rule row, and the
// rebuild — the mechanism whose entire purpose was to re-derive the corpus
// under the new rule — has been locked out of that key by a write it was
// racing. If the MATCH edit was a narrowing or a revocation, that frozen row is
// the pre-edit permission set.
//
// §7.5 negative-control discipline: the positive vector is asserted FIRST. A
// test of "the replay does not land" passes trivially against an inert guard,
// so the first subtest proves the guard is genuinely enforcing `>=` before the
// freeze subtest claims anything about being blocked by it.
func TestGuardedWrite_PostTruncateStrayWriteDoesNotFreezeReplay(t *testing.T) {
	ctx := context.Background()
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"key"})
	a.SetGuarded(true)

	keys := map[string]any{"key": "cap.roles.identity.frozen"}
	oldRuleRow := map[string]any{"key": "cap.roles.identity.frozen", "roles": []any{"admin"}}
	newRuleRow := map[string]any{"key": "cap.roles.identity.frozen", "roles": []any{}}

	// The consumer head at the moment the stray sweep write is issued. Every
	// sequence the rebuild replays is at or below it, by construction: the head
	// is the highest sequence this pipeline has applied.
	const head = uint64(100)

	t.Run("positive vector: the guard is genuinely enforcing", func(t *testing.T) {
		// Without this, the freeze assertion below could pass against a guard
		// that never rejects anything.
		outcome, err := a.UpsertWithOutcome(ctx, keys, oldRuleRow, head)
		require.NoError(t, err)
		require.False(t, outcome.DeclinedByWatermark, "the first write into an absent key must land")

		stored, present, err := a.GetRow(ctx, keys)
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, oldRuleRow["roles"], stored["roles"])
	})

	t.Run("the replay is locked out, at every sequence below the head", func(t *testing.T) {
		// The rebuild re-derives this actor under the NEW rule and replays its
		// events. Each carries its own stream sequence — all <= head.
		for _, replaySeq := range []uint64{50, 60, 70, head} {
			outcome, err := a.UpsertWithOutcome(ctx, keys, newRuleRow, replaySeq)
			require.NoError(t, err, "the guard returns success — that is what made this invisible")
			require.True(t, outcome.DeclinedByWatermark,
				"seq %d is at or below the stray write's head token, so it cannot land", replaySeq)
		}

		stored, present, err := a.GetRow(ctx, keys)
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, oldRuleRow["roles"], stored["roles"],
			"the row is FROZEN at the old rule's content — this is the defect, stated as an outcome")
	})

	t.Run("only a genuinely newer event lifts the freeze", func(t *testing.T) {
		// The freeze is not permanent in principle: it lifts when a real CDC
		// event above the stray watermark reprojects the actor. On a narrowed
		// auth lens such events are sparse by construction, which is why the
		// window is unbounded in practice rather than merely long.
		outcome, err := a.UpsertWithOutcome(ctx, keys, newRuleRow, head+1)
		require.NoError(t, err)
		require.False(t, outcome.DeclinedByWatermark)

		stored, _, err := a.GetRow(ctx, keys)
		require.NoError(t, err)
		require.Equal(t, newRuleRow["roles"], stored["roles"])
	})

	t.Run("a retraction is declined the same way — the over-grant direction", func(t *testing.T) {
		// The half the previous attempt at this fix missed. A revocation
		// dropped at a tied watermark leaves the grant live while Deleted and
		// Wrote both report it retracted.
		outcome, err := a.DeleteWithOutcome(ctx, keys, head+1)
		require.NoError(t, err)
		require.True(t, outcome.DeclinedByWatermark, "a tie declines the retraction")
		require.False(t, outcome.Wrote, "a declined revocation retracted nothing")

		_, present, err := a.GetRow(ctx, keys)
		require.NoError(t, err)
		require.True(t, present, "the grant is still live — the revocation did not land")
	})
}
