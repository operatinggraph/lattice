package loftspaceledger

// Rule-engine proof of the loftspaceArrearsReminders convergence lens
// (arrearsRemindersSpec), driven through the `full` engine against an
// embedded NATS Core/Adjacency KV — the harness lens_cypher_test.go uses.
//
// The arrears convergence lens reads NO clock: every comparison is between
// two stored facts (the recorded reminder instant, the recorded reminder, and
// the instant a fired @at recorded on this account's own freshnessExpiry
// marker). Every conjunct of freshUntil and of the single gap therefore gets
// a vector here, because a mis-compiled lens FALLS BACK silently on the full
// engine — a shape the engine cannot compile projects nothing rather than
// erroring, and only a pin that runs the real engine can tell the difference.
//
// The one place this lens differs from wellness-ledger's: the timer arms at
// remindAt (the head's recorded due date plus the grace), never at dueAt, and
// the recorded lapse is compared against remindAt. Every vector below seeds
// dueAt and remindAt five days apart so a lens that read the wrong column
// would answer differently.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// projectArrears runs the anchored loftspaceArrearsReminders spec for one
// account. NO clock parameter is supplied: the cypher references none, and
// passing one would let a clock-reading regression pass unnoticed here.
func (f *lensFixture) projectArrears(t *testing.T, acctName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(arrearsRemindersSpec)
	require.NoError(t, err, "loftspaceArrearsReminders cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": "vtx.account." + f.ids[acctName],
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkArrearsAccount seeds an account held for a lease, optionally carrying a
// .arrears aspect. A nil arrears map seeds the account with no aspect at all
// — the never-evaluated shape every account alive at install has.
func (f *lensFixture) mkArrearsAccount(t *testing.T, prefix string, arrears map[string]any) {
	t.Helper()
	f.vtx(t, prefix+"_lease", "leaseapp")
	f.vtx(t, prefix+"_acct", "account")
	f.edge(t, "heldFor", prefix+"_acct", prefix+"_lease")
	if arrears != nil {
		f.aspect(t, prefix+"_acct", "arrears", "loftspaceAccountArrears", arrears)
	}
}

// recordLapse writes the freshnessExpiry marker MarkExpired commits onto the
// ACCOUNT when this target's @at fires: the instant the timer fired for,
// recorded under the target's own key in byTarget, with expiredAt carrying
// the entity-wide maximum. The marker shape is orchestration-base's, not this
// package's.
func (f *lensFixture) recordLapse(t *testing.T, name string, byTarget map[string]string) {
	t.Helper()
	entries := map[string]any{}
	maxAt := ""
	for target, at := range byTarget {
		entries[target] = at
		if at > maxAt {
			maxAt = at
		}
	}
	f.aspect(t, name, "freshnessExpiry", "freshnessExpiry", map[string]any{
		"expiredAt": maxAt,
		"byTarget":  entries,
	})
}

// requireIntColumn asserts a lens-projected column is present and equals want
// whatever numeric type the engine handed back.
func requireIntColumn(t *testing.T, v map[string]any, col string, want int) {
	t.Helper()
	got, ok := v[col]
	require.Truef(t, ok, "row must carry the %s column", col)
	switch n := got.(type) {
	case int:
		require.Equalf(t, want, n, "%s", col)
	case int64:
		require.Equalf(t, want, int(n), "%s", col)
	case float64:
		require.Equalf(t, want, int(n), "%s", col)
	default:
		t.Fatalf("%s is %T, not a numeric cap", col, got)
	}
}

// TestLoftspaceArrears_NeverEvaluated — an account carrying no .arrears at
// all: never evaluated, whether it has charges or not (post_entry mints
// nothing). evaluatedAt is null, so the gap is open from its first
// projection: that is how every such account gets its one evaluation.
// freshUntil is null — there is no recorded reminder instant to arm a timer
// at.
func TestLoftspaceArrears_NeverEvaluated(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "virgin", nil)

	rows := f.projectArrears(t, "virgin_acct")
	require.Len(t, rows, 1, "exactly one row per account")
	v := rows[0].Values
	require.Equal(t, "vtx.account."+f.ids["virgin_acct"], v["entityKey"])
	require.Equal(t, "vtx.account."+f.ids["virgin_acct"], v["actorKey"])
	require.Equal(t, true, v["missing_evaluation"], "an account nothing has ever aged is violating on sight")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "no recorded reminder instant, so no timer arms")
	requireIntColumn(t, v, "maxretries_evaluation", maxArrearsEvaluationRetries)
}

// TestLoftspaceArrears_Pending — a recorded reminder instant still ahead of
// any fired timer: NOT violating, and freshUntil = remindAt (NOT dueAt) arms
// the @at. This is the LoftSpace-specific pin: the timer waits out the grace.
func TestLoftspaceArrears_Pending(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "pending", map[string]any{
		"dueAt":       "2026-09-08T00:00:00Z",
		"remindAt":    "2026-09-13T00:00:00Z",
		"evaluatedAt": "2026-09-01T10:00:00Z",
	})

	rows := f.projectArrears(t, "pending_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "no timer has fired on this account — not due for a reminder")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-09-13T00:00:00Z", v["freshUntil"], "freshUntil = remindAt arms the @at timer, never dueAt")
	_, isString := v["freshUntil"].(string)
	require.True(t, isString, "freshUntil must be a scalar string so scheduleFreshness can parse it as RFC3339")
	require.Equal(t, "2026-09-08T00:00:00Z", v["dueAt"])
	require.Equal(t, "2026-09-13T00:00:00Z", v["remindAt"])
	require.Nil(t, v["reminderSentAt"], "nothing has gone out yet")
}

// TestLoftspaceArrears_DueAtOnlyArmsNoTimer pins that dueAt alone is not an
// armed timer: an aspect carrying dueAt with no remindAt (a shape the op
// never writes, since both land together) projects freshUntil null rather
// than arming at the due date. A lens that fell back to dueAt would nag on
// the due date itself, the morning a rent charge posts.
func TestLoftspaceArrears_DueAtOnlyArmsNoTimer(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "dueonly", map[string]any{
		"dueAt":       "2026-09-08T00:00:00Z",
		"evaluatedAt": "2026-09-01T10:00:00Z",
	})

	rows := f.projectArrears(t, "dueonly_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Nil(t, v["freshUntil"], "the timer arms at remindAt; a due date alone arms nothing")
	require.Equal(t, false, v["missing_evaluation"])
}

// TestLoftspaceArrears_LapseAtDueAtDoesNotOpen is the other half of the grace:
// a timer lapse recorded AT dueAt — before remindAt — opens nothing. The
// recorded lapse is compared against remindAt, so a lapse inside the grace is
// not this gap's evidence, and the timer stays armed at remindAt.
func TestLoftspaceArrears_LapseAtDueAtDoesNotOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "graced", map[string]any{
		"dueAt":       "2026-09-08T00:00:00Z",
		"remindAt":    "2026-09-13T00:00:00Z",
		"evaluatedAt": "2026-09-01T10:00:00Z",
	})
	f.recordLapse(t, "graced_acct", map[string]string{ArrearsRemindersTarget: "2026-09-08T00:00:00Z"})

	rows := f.projectArrears(t, "graced_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "a lapse inside the grace is not this gap's evidence")
	require.Equal(t, "2026-09-13T00:00:00Z", v["freshUntil"], "the timer stays armed at remindAt")
}

// TestLoftspaceArrears_Due — the @at has FIRED and its lapse is recorded at
// remindAt, and nothing has been reminded for that due date: the gap OPENS.
// freshUntil goes null once the lapse is recorded — a one-shot wake-up, not
// a re-arm; the violating row itself drives the dispatch from here.
func TestLoftspaceArrears_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "due", map[string]any{
		"dueAt":       "2026-09-08T00:00:00Z",
		"remindAt":    "2026-09-13T00:00:00Z",
		"evaluatedAt": "2026-09-01T10:00:00Z",
	})
	f.recordLapse(t, "due_acct", map[string]string{ArrearsRemindersTarget: "2026-09-13T00:00:00Z"})

	rows := f.projectArrears(t, "due_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["missing_evaluation"], "the recorded lapse reaches remindAt and nothing was reminded for it")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "the timer fired; it is not re-armed")
}

// TestLoftspaceArrears_Sent — the same account after EvaluateLoftspaceArrears
// sent the reminder: remindedFor = dueAt closes the gap, and freshUntil stays
// null. This is the "no re-dispatch after a send" assertion — a gap that
// re-opened every convergence window would mint a fresh notification each
// time.
func TestLoftspaceArrears_Sent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "sent", map[string]any{
		"dueAt":       "2026-09-08T00:00:00Z",
		"remindAt":    "2026-09-13T00:00:00Z",
		"remindedFor": "2026-09-08T00:00:00Z",
		"sentAt":      "2026-09-13T00:05:00Z",
		"evaluatedAt": "2026-09-13T00:05:00Z",
	})
	f.recordLapse(t, "sent_acct", map[string]string{ArrearsRemindersTarget: "2026-09-13T00:00:00Z"})

	rows := f.projectArrears(t, "sent_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "one reminder per episode: the recorded send closes the gap for good")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "remindedFor = dueAt, so no timer re-arms either")
	require.Equal(t, "2026-09-13T00:05:00Z", v["reminderSentAt"], "the landlord and the tenant both read this")
}

// TestLoftspaceArrears_SentWithoutLapseArmsNoTimer is the shape an account
// takes when its first-ever evaluation finds a head already past its grace,
// so remindedFor and sentAt are stamped without any timer having fired — no
// lapse is recorded. freshUntil must still be null: a timer armed at a
// reminder instant already reminded for would fire into a gap remindedFor
// already closes, a wasted @at per standing account. Without a lapse the
// timer's own conjunct cannot null freshUntil, so this vector is what pins
// the remindedFor conjunct on the freshUntil side.
func TestLoftspaceArrears_SentWithoutLapseArmsNoTimer(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "sentnolapse", map[string]any{
		"dueAt":       "2026-06-01T09:00:00Z",
		"remindAt":    "2026-06-06T09:00:00Z",
		"remindedFor": "2026-06-01T09:00:00Z",
		"sentAt":      "2026-09-12T09:00:00Z",
		"evaluatedAt": "2026-09-12T09:00:00Z",
	})

	rows := f.projectArrears(t, "sentnolapse_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "remindedFor = dueAt closes the gap with or without a lapse")
	require.Nil(t, v["freshUntil"], "an episode already reminded for arms no timer, lapse or no lapse")
}

// TestLoftspaceArrears_Stale — a posted entry marked the recorded state stale
// (this ledger's post_entry cannot see a balance, so EVERY entry does). The
// gap opens with no timer involved at all, and freshUntil is suppressed even
// though the recorded remindAt is still in the future and unreminded: arming
// a timer at an instant already known to be suspect would fire a reminder for
// the wrong charge.
func TestLoftspaceArrears_Stale(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "stale", map[string]any{
		"dueAt":       "2026-09-08T00:00:00Z",
		"remindAt":    "2026-09-13T00:00:00Z",
		"evaluatedAt": "2026-09-01T10:00:00Z",
		"stale":       true,
	})

	rows := f.projectArrears(t, "stale_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["missing_evaluation"], "stale opens the gap directly — no fired timer needed")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "a reminder instant known to be stale must not arm a timer")
	require.Equal(t, true, v["stale"])
}

// TestLoftspaceArrears_Cleared — the evaluation found nothing owed and rewrote
// .arrears to {evaluatedAt} alone. No remindAt: nothing violating, no timer,
// and nothing left of the finished episode to make the NEXT charge look
// reminded.
func TestLoftspaceArrears_Cleared(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "cleared", map[string]any{"evaluatedAt": "2026-09-20T09:00:00Z"})
	// The marker from the episode that has just ended stays on the account
	// forever (orchestration-base merges, never clears). It must not make a
	// cleared account violating.
	f.recordLapse(t, "cleared_acct", map[string]string{ArrearsRemindersTarget: "2026-09-13T00:00:00Z"})

	rows := f.projectArrears(t, "cleared_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "nothing is owed — the episode is over")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
	require.Nil(t, v["dueAt"])
	require.Nil(t, v["remindAt"])
}

// TestLoftspaceArrears_NewEpisodeAfterOldLapse is the standing-marker vector:
// the freshnessExpiry entry from a PREVIOUS episode is permanent, and a new
// episode whose remindAt is later than that recorded instant must re-arm
// rather than open on the old lapse.
func TestLoftspaceArrears_NewEpisodeAfterOldLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "reopened", map[string]any{
		"dueAt":       "2026-10-08T00:00:00Z",
		"remindAt":    "2026-10-13T00:00:00Z",
		"evaluatedAt": "2026-10-05T08:00:00Z",
	})
	f.recordLapse(t, "reopened_acct", map[string]string{ArrearsRemindersTarget: "2026-09-13T00:00:00Z"})

	rows := f.projectArrears(t, "reopened_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "the old lapse predates this episode's reminder instant")
	require.Equal(t, "2026-10-13T00:00:00Z", v["freshUntil"], "a fresh @at arms for the new episode")
}

// TestLoftspaceArrears_SiblingTargetLapseDoesNotOpen pins the byTarget
// indirection: the marker is shared by every target that arms a timer on this
// anchor, and only THIS target's own entry may open this gap. Reading
// expiredAt (the entity-wide maximum) instead would have any sibling's fired
// timer send an arrears reminder.
func TestLoftspaceArrears_SiblingTargetLapseDoesNotOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "sibling", map[string]any{
		"dueAt":       "2026-09-08T00:00:00Z",
		"remindAt":    "2026-09-13T00:00:00Z",
		"evaluatedAt": "2026-09-01T10:00:00Z",
	})
	f.recordLapse(t, "sibling_acct", map[string]string{"someOtherTarget": "2026-09-30T00:00:00Z"})

	rows := f.projectArrears(t, "sibling_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "another target's fired timer is not this gap's evidence")
	require.Equal(t, "2026-09-13T00:00:00Z", v["freshUntil"], "and it must not suppress this target's own @at either")
}

// TestLoftspaceArrears_HistoryTooLongGoesQuiet pins the degrade posture, and
// it is the ONE vector where a recorded lapse must NOT open the gap. An
// account whose transaction history outran the evaluation's replay budget
// carries historyTooLong: the op cannot compute a head for it, so leaving
// the gap open would have Weaver re-dispatch an evaluation that can only
// fail again, every window, forever, with nothing sent and nothing said.
// Both suppressions are asserted together — a lens that killed only the gap
// would still arm an @at at a remindAt no evaluation could confirm, and one
// that killed only the timer would still re-dispatch. The row itself stays
// projected, which is the operator's signal in the weaver-targets bucket.
func TestLoftspaceArrears_HistoryTooLongGoesQuiet(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "toolong", map[string]any{
		"dueAt":          "2026-09-08T00:00:00Z",
		"remindAt":       "2026-09-13T00:00:00Z",
		"evaluatedAt":    "2026-09-15T09:00:00Z",
		"historyTooLong": true,
		"historyBudget":  ArrearsPageLimit * ArrearsMaxPages,
	})
	// The lapse IS recorded and nothing was reminded for it: without the
	// historyTooLong conjunct this is TestLoftspaceArrears_Due exactly, so the
	// vector reds the moment either suppression is dropped.
	f.recordLapse(t, "toolong_acct", map[string]string{ArrearsRemindersTarget: "2026-09-13T00:00:00Z"})

	rows := f.projectArrears(t, "toolong_acct")
	require.Len(t, rows, 1, "the row must stay projected — quiet is not invisible")
	v := rows[0].Values
	require.Equal(t, true, v["historyTooLong"], "the operator reads this column off the weaver-targets row")
	requireIntColumn(t, v, "historyBudget", ArrearsPageLimit*ArrearsMaxPages)
	require.Equal(t, false, v["missing_evaluation"], "an evaluation that cannot succeed must not be re-dispatched every window")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "and no timer arms at a reminder instant no evaluation could confirm")
}

// TestLoftspaceArrears_HistoryTooLongUnderSmallerBudgetRearms is the way a
// raised budget reaches the accounts a smaller one parked. A flag recorded
// under a budget below the current one — or with no budget recorded at all —
// does not prove the current evaluation cannot succeed, so it no longer
// suppresses the GAP (the timer stays suppressed by any flag, as by stale):
// missing_evaluation's budget arm opens the gap for exactly one evaluation,
// which either finalizes or re-records the flag at the current budget (and
// TestLoftspaceArrears_HistoryTooLongGoesQuiet takes over). Two shapes: the
// recorded-but-smaller budget, and the absent one — `historyBudget >= N` is
// false on a null, so NOT of it is true, and absence reads as smaller.
func TestLoftspaceArrears_HistoryTooLongUnderSmallerBudgetRearms(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	// Flagged under a smaller recorded budget; no lapse, nothing stale.
	f.mkArrearsAccount(t, "smallbudget", map[string]any{
		"dueAt":          "2026-09-11T10:00:00Z",
		"evaluatedAt":    "2026-08-22T09:00:00Z",
		"historyTooLong": true,
		"historyBudget":  30,
	})
	// Flagged with no budget recorded, no due date, evaluated.
	f.mkArrearsAccount(t, "nobudget", map[string]any{
		"evaluatedAt":    "2026-08-22T09:00:00Z",
		"historyTooLong": true,
	})

	rows := f.projectArrears(t, "smallbudget_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["missing_evaluation"], "a flag recorded under a smaller budget opens the gap for one evaluation under the current one")
	require.Equal(t, true, v["violating"])
	requireIntColumn(t, v, "historyBudget", 30)
	require.Nil(t, v["freshUntil"], "the flag still suppresses the timer whatever budget it was recorded under — a remindAt a degraded evaluation carried is not an instant to arm on; the gap, not the timer, is the re-arm")

	rows = f.projectArrears(t, "nobudget_acct")
	require.Len(t, rows, 1)
	v = rows[0].Values
	require.Equal(t, true, v["historyTooLong"])
	require.Nil(t, v["historyBudget"])
	require.Equal(t, true, v["missing_evaluation"], "no recorded budget reads as a smaller one — the account is re-armed once")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "no due date recorded, so nothing to arm")
}

// TestLoftspaceArrears_MidReplayPhaseGaps pins the continuation shape. An
// evaluation part-way through a history longer than one page leaves a
// checkpoint whose phase flips on every page; while it stands the row is
// neither pending nor due — missing_evaluation is false and freshUntil null,
// whatever the lapse or the stale mark say, because the head is unknown until
// the enumeration is exhausted — and exactly ONE phase gap is open, the one
// naming the recorded phase. That gap's directOp writes the other phase, so
// the gap that dispatched a page is closed by that page's own write and the
// next page is dispatched by its sibling. Both phases are pinned, the fixture
// otherwise identical to TestLoftspaceArrears_Due plus a stale mark, so a
// checkpoint that failed to gate either arm reds here.
func TestLoftspaceArrears_MidReplayPhaseGaps(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	for _, phase := range []string{ArrearsPhaseA, ArrearsPhaseB} {
		name := "replay" + phase
		f.mkArrearsAccount(t, name, map[string]any{
			"dueAt":       "2026-09-08T00:00:00Z",
			"remindAt":    "2026-09-13T00:00:00Z",
			"evaluatedAt": "2026-08-22T09:00:00Z",
			"stale":       true,
			"replay": map[string]any{
				"phase":  phase,
				"cursor": "lnk.transaction.LFREPLAYTXAHJKMNPQRS.postedTo.account." + f.ids[name+"_acct"],
				"pages":  2,
				"entries": map[string]any{
					"LFREPLAYTXAHJKMNPQRS": map[string]any{
						"postedAt": "2026-08-22T09:00:00Z", "type": "debit", "amountCents": 150000,
					},
				},
			},
		})
		f.recordLapse(t, name+"_acct", map[string]string{ArrearsRemindersTarget: "2026-09-13T00:00:00Z"})

		rows := f.projectArrears(t, name+"_acct")
		require.Len(t, rows, 1)
		v := rows[0].Values
		require.Equal(t, false, v["missing_evaluation"], "phase %s: a replay in progress is not an evaluation to dispatch — the lapse and the stale mark wait for the finalize page", phase)
		require.Equal(t, phase == ArrearsPhaseA, v["missing_replay_a"], "phase %s", phase)
		require.Equal(t, phase == ArrearsPhaseB, v["missing_replay_b"], "phase %s", phase)
		require.Equal(t, true, v["violating"], "phase %s: the open phase gap is what drives the next page", phase)
		require.Nil(t, v["freshUntil"], "phase %s: no timer arms at a reminder instant the replay has not confirmed", phase)
		require.Equal(t, true, v["replaying"], "phase %s", phase)
		requireIntColumn(t, v, "replayPages", 2)
	}
}

// TestLoftspaceArrears_MalformedCheckpointReopensEvaluation pins the failure
// mode of the phase chain: a checkpoint whose phase is neither value opens NO
// phase gap, and without this vector's conjunct it would also close
// missing_evaluation — a row with a recorded replay and no gap at all, parked
// forever with nobody dispatched. The replay conjunct admits such a
// checkpoint, so the evaluation gap re-opens and the op (which treats an
// unresumable checkpoint as absent) restarts at page 1.
func TestLoftspaceArrears_MalformedCheckpointReopensEvaluation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "badphase", map[string]any{
		"evaluatedAt": "2026-08-22T09:00:00Z",
		"stale":       true,
		"replay": map[string]any{
			"phase":   "x",
			"cursor":  "lnk.transaction.LFREPLAYTXAHJKMNPQRS.postedTo.account." + f.ids["badphase_acct"],
			"pages":   1,
			"entries": map[string]any{},
		},
	})

	rows := f.projectArrears(t, "badphase_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_replay_a"], "an unknown phase is neither continuation")
	require.Equal(t, false, v["missing_replay_b"])
	require.Equal(t, true, v["missing_evaluation"], "so the evaluation gap re-opens rather than leaving the row with no gap")
	require.Equal(t, true, v["violating"])
	require.Equal(t, true, v["replaying"], "the operator still sees the recorded checkpoint")
}

// TestLoftspaceArrears_NoCheckpointNoPhaseGap is the vector the two above are
// measured against: the SAME due shape with no checkpoint is violating
// through missing_evaluation alone, both phase gaps false. Without it a lens
// that projected a phase gap on every row would pass the mid-replay pins.
func TestLoftspaceArrears_NoCheckpointNoPhaseGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "nocheckpoint", map[string]any{
		"dueAt":       "2026-09-08T00:00:00Z",
		"remindAt":    "2026-09-13T00:00:00Z",
		"evaluatedAt": "2026-08-22T09:00:00Z",
	})
	f.recordLapse(t, "nocheckpoint_acct", map[string]string{ArrearsRemindersTarget: "2026-09-13T00:00:00Z"})

	rows := f.projectArrears(t, "nocheckpoint_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["missing_evaluation"])
	require.Equal(t, false, v["missing_replay_a"], "no checkpoint, no continuation")
	require.Equal(t, false, v["missing_replay_b"])
	require.Equal(t, true, v["violating"])
	require.Equal(t, false, v["replaying"])
	require.Nil(t, v["replayPages"])
}

// TestLoftspaceArrears_HistoryTooLongArmsNoTimer is the timer half of the
// degrade posture on its own: a historyTooLong account whose recorded
// remindAt is still ahead and on which NO timer has fired. The gap vector
// above records a lapse, and that lapse would null freshUntil by itself — so
// only this shape can tell that the historyTooLong conjunct on freshUntil is
// really there.
func TestLoftspaceArrears_HistoryTooLongArmsNoTimer(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "toolongahead", map[string]any{
		"dueAt":          "2026-09-08T00:00:00Z",
		"remindAt":       "2026-09-13T00:00:00Z",
		"evaluatedAt":    "2026-09-01T10:00:00Z",
		"historyTooLong": true,
		"historyBudget":  ArrearsPageLimit * ArrearsMaxPages,
	})

	rows := f.projectArrears(t, "toolongahead_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"])
	require.Nil(t, v["freshUntil"], "no timer arms at a reminder instant the degraded evaluation could not confirm")
}

// TestLoftspaceArrears_HistoryTooLongClearedReopens is the positive vector the
// one above is measured against: the SAME state with the flag absent IS
// violating. Without it a lens that had simply stopped computing
// missing_evaluation would pass the suppression pin.
func TestLoftspaceArrears_HistoryTooLongClearedReopens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "cleardone", map[string]any{
		"dueAt":       "2026-09-08T00:00:00Z",
		"remindAt":    "2026-09-13T00:00:00Z",
		"evaluatedAt": "2026-09-15T09:00:00Z",
	})
	f.recordLapse(t, "cleardone_acct", map[string]string{ArrearsRemindersTarget: "2026-09-13T00:00:00Z"})

	rows := f.projectArrears(t, "cleardone_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Nil(t, v["historyTooLong"])
	require.Equal(t, true, v["missing_evaluation"], "the identical row without the flag opens the gap")
}

// TestLoftspaceArrears_NoLeaseStillProjects pins that the lens walks NOTHING:
// an account with no heldFor lease at all still projects its row and arms
// its timer, because the row is what carries the gap and the lease and
// tenant are the op's to resolve. A required (or even optional) hop here
// would be the dossier's optional-hop Params refusal waiting to happen.
func TestLoftspaceArrears_NoLeaseStillProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "nolease_acct", "account")
	f.aspect(t, "nolease_acct", "arrears", "loftspaceAccountArrears", map[string]any{
		"dueAt":       "2026-09-08T00:00:00Z",
		"remindAt":    "2026-09-13T00:00:00Z",
		"evaluatedAt": "2026-09-01T10:00:00Z",
	})

	rows := f.projectArrears(t, "nolease_acct")
	require.Len(t, rows, 1, "an account with no heldFor lease still projects its arrears row")
	v := rows[0].Values
	require.Equal(t, "vtx.account."+f.ids["nolease_acct"], v["entityKey"])
	require.Equal(t, false, v["missing_evaluation"])
	require.Equal(t, "2026-09-13T00:00:00Z", v["freshUntil"], "and the timer arms exactly as it would with a lease")
}

// TestLeaseAccounts_ProjectsArrearsColumns pins the three informational
// columns the landlord ledger, the tenant statement and the portfolio list
// read for "when did this fall due, when did the reminder go out" — they come
// off the ACCOUNT's aspect through the lens's inbound heldFor hop, so a lease
// with no account still projects nulls.
func TestLeaseAccounts_ProjectsArrearsColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "arr_lease", "leaseapp")
	f.vtx(t, "arr_acct", "account")
	f.edge(t, "heldFor", "arr_acct", "arr_lease")
	// All five instants deliberately differ — the state a re-evaluation leaves
	// after a partial payment moved the head to a charge not yet past its
	// grace: dueAt is the NEW head's due date, remindAt its grace, remindedFor
	// the old head it reminded for, sentAt the recorded send instant,
	// evaluatedAt the evaluation that carried the record. Each column must
	// read its own field, not a neighbour that happens to equal it in the
	// common shape.
	f.aspect(t, "arr_acct", "arrears", "loftspaceAccountArrears", map[string]any{
		"dueAt":       "2026-10-01T00:00:00Z",
		"remindAt":    "2026-10-06T00:00:00Z",
		"remindedFor": "2026-09-01T00:00:00Z",
		"sentAt":      "2026-09-06T09:00:00Z",
		"evaluatedAt": "2026-09-22T09:00:00Z",
	})
	// A lease that holds no account: the row projects with every arrears
	// column null.
	f.vtx(t, "noacct_lease", "leaseapp")

	rows := f.project(t, "leaseAccounts", leaseAccountsSpec)
	require.Len(t, rows, 2)
	byKey := map[string]map[string]any{}
	for _, r := range rows {
		byKey[r.Values["leaseAppKey"].(string)] = r.Values
	}
	v := byKey["vtx.leaseapp."+f.ids["arr_lease"]]
	require.NotNil(t, v)
	require.Equal(t, "vtx.account."+f.ids["arr_acct"], v["accountKey"])
	require.Equal(t, "2026-10-01T00:00:00Z", v["arrearsDueAt"])
	require.Equal(t, "2026-09-01T00:00:00Z", v["arrearsRemindedFor"])
	require.Equal(t, "2026-09-06T09:00:00Z", v["arrearsReminderSentAt"])

	n := byKey["vtx.leaseapp."+f.ids["noacct_lease"]]
	require.NotNil(t, n)
	require.Nil(t, n["accountKey"])
	require.Nil(t, n["arrearsDueAt"])
	require.Nil(t, n["arrearsRemindedFor"])
	require.Nil(t, n["arrearsReminderSentAt"])
}
