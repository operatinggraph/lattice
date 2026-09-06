package cafeledger

// Rule-engine proof of both business lenses, driven through the `full` engine
// (engine:"full") against an embedded NATS Core/Adjacency KV — the harness
// clinic-ledger / loftspace-ledger / cafe-domain use.
//
// The two lenses make opposite claims about their MATCH shapes, and each claim
// is what these tests hold:
//
//   - ledgerHistory's postedTo/heldFor hops are REQUIRED, so a cafetransaction
//     projects a row only when it is genuinely posted to a live cafeaccount
//     held for a live lease. A dangling transaction must project NOTHING; a
//     lens that relaxed those to OPTIONAL would emit rows with a null
//     accountKey, and a reader summing amountCents per account would drop them.
//   - leaseAccounts anchors on the LEASE, not the account, so a lease that has
//     never been charged still gets a row with a null accountKey — the "has
//     this lease opened a café account yet" question the FE asks before its
//     first charge, which a lens anchored on the account cannot answer.
//
// Unlike loftspace-ledger's, this ledgerHistory has no authorizedBy hop: café
// charges carry no semantic-contracts clause.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type lensFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newLensFixture(t *testing.T) *lensFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &lensFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *lensFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := lenstest.NanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *lensFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *lensFixture) edge(t *testing.T, name, fromName, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[fromName], f.ids[toName]
	fromType, toType := f.types[fromID], f.types[toID]
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType}))
}

// project runs one of the package's lens specs. Neither lens is anchored, so
// the engine enumerates its own roots and no actorKey is supplied.
func (f *lensFixture) project(t *testing.T, specName, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "%s cypher must parse on the full engine", specName)
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkPostedCharge seeds the whole shape a committed Charge produces: a
// cafetransaction posted to a cafeaccount held for a lease.
func (f *lensFixture) mkPostedCharge(t *testing.T, prefix string, amountCents float64, memo string) {
	t.Helper()
	f.vtx(t, prefix+"_lease", "leaseapp")
	f.vtx(t, prefix+"_acct", "cafeaccount")
	f.vtx(t, prefix+"_tx", "cafetransaction")
	f.edge(t, "heldFor", prefix+"_acct", prefix+"_lease")
	f.edge(t, "postedTo", prefix+"_tx", prefix+"_acct")
	f.aspect(t, prefix+"_tx", "entry", "cafetransaction", map[string]any{
		"type":        "debit",
		"amountCents": amountCents,
		"memo":        memo,
		"postedAt":    "2026-07-25T00:00:00Z",
	})
}

func TestCafeLedgerHistory_PostedCharge_ProjectsRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkPostedCharge(t, "posted", 850, "Flat white")

	rows := f.project(t, "cafeLedgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.cafetransaction."+f.ids["posted_tx"], v["key"])
	require.Equal(t, "vtx.cafetransaction."+f.ids["posted_tx"], v["transactionKey"])
	require.Equal(t, "vtx.cafeaccount."+f.ids["posted_acct"], v["accountKey"])
	require.Equal(t, "vtx.leaseapp."+f.ids["posted_lease"], v["leaseAppKey"])
	require.Equal(t, "debit", v["type"])
	require.Equal(t, 850.0, v["amountCents"])
	require.Equal(t, "Flat white", v["memo"])
	require.Equal(t, "2026-07-25T00:00:00Z", v["postedAt"])
}

// TestCafeLedgerHistory_RefundProjectsReversesKey pins the reverses hop: a
// refund is an ordinary credit entry, so nothing in its own aspect
// distinguishes it from a payment the resident handed over — reversesKey, the
// projection of the link RefundCafeCharge writes, is the ONLY thing that does.
// The charge it reverses is seeded with a settles link too, so the same run
// pins tabKey on the debit's row (what the front desk reads to know a debit is
// a café charge with something to give back) and, on the refund's own row,
// that both columns are independent: a refund settles no tab, and a charge
// reverses nothing.
func TestCafeLedgerHistory_RefundProjectsReversesKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkPostedCharge(t, "refunded", 900, "Settled tab")
	f.vtx(t, "refunded_tab", "tab")
	f.edge(t, "settles", "refunded_tx", "refunded_tab")

	// The charge's entry as a committed refund leaves it: the refundedCents
	// tally added, every other field carried across untouched. Seeding the
	// tally here is what makes the amountCents assertion below meaningful —
	// the ledger's balance arithmetic reads the charge's own amount, so a
	// tally that overwrote or netted it off would quietly halve what the
	// resident is shown to owe. The aspect class is "transactionEntry", what
	// post_entry and the refund's tally upsert both write (scripts.go) — the
	// owning vertex's own type is not it.
	f.aspect(t, "refunded_tx", "entry", "transactionEntry", map[string]any{
		"type":          "debit",
		"amountCents":   900.0,
		"refundedCents": 400.0,
		"memo":          "Settled tab",
		"postedAt":      "2026-07-25T00:00:00Z",
	})

	f.vtx(t, "refund_tx", "cafetransaction")
	f.edge(t, "postedTo", "refund_tx", "refunded_acct")
	f.edge(t, "reverses", "refund_tx", "refunded_tx")
	f.aspect(t, "refund_tx", "entry", "transactionEntry", map[string]any{
		"type":        "credit",
		"amountCents": 400.0,
		"memo":        "Wrong item charged",
		"postedAt":    "2026-07-26T00:00:00Z",
	})

	rows := f.project(t, "cafeLedgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 2)
	byKey := map[string]map[string]any{}
	for _, r := range rows {
		byKey[r.Values["transactionKey"].(string)] = r.Values
	}

	charge := byKey["vtx.cafetransaction."+f.ids["refunded_tx"]]
	require.NotNil(t, charge)
	require.Equal(t, "vtx.tab."+f.ids["refunded_tab"], charge["tabKey"], "the settles hop is what marks a debit as a refundable café charge")
	require.Nil(t, charge["reversesKey"], "a charge reverses nothing")
	require.Equal(t, 900.0, charge["amountCents"],
		"the refundedCents tally is a note on the charge, not a rewrite of it — the projected charge keeps its full amount")
	require.Equal(t, "Settled tab", charge["memo"], "the tally upsert carries every other entry field across")
	require.NotContains(t, charge, "refundedCents", "the tally is the refund ceiling, not a column the statement reads")

	refund := byKey["vtx.cafetransaction."+f.ids["refund_tx"]]
	require.NotNil(t, refund)
	require.Equal(t, "credit", refund["type"], "a refund posts an ordinary credit — every balance consumer sums it unchanged")
	require.Equal(t, "vtx.cafetransaction."+f.ids["refunded_tx"], refund["reversesKey"],
		"reversesKey is the only thing distinguishing a refund from a payment the resident made")
	require.Nil(t, refund["tabKey"], "a refund settles no tab")
}

// TestCafeLedgerHistory_PlainCharge_NullsBothOptionalHops proves the two new
// hops are genuinely OPTIONAL. A plain hand-posted debit — no tab behind it, no
// refund against it — must still project its row; had either hop been written
// REQUIRED, the whole history would vanish for every café that never refunds.
func TestCafeLedgerHistory_PlainCharge_NullsBothOptionalHops(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkPostedCharge(t, "plain", 650, "Flat white")

	rows := f.project(t, "cafeLedgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 1, "an unreversed, un-settled charge still projects")
	require.Nil(t, rows[0].Values["reversesKey"])
	require.Nil(t, rows[0].Values["tabKey"])
}

func TestCafeLedgerHistory_UnpostedTransaction_ProjectsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	// A cafetransaction with its entry aspect but no postedTo edge — the shape
	// a half-written commit would leave behind.
	f.vtx(t, "orphan_tx", "cafetransaction")
	f.aspect(t, "orphan_tx", "entry", "cafetransaction", map[string]any{
		"type": "debit", "amountCents": 500.0, "postedAt": "2026-07-25T00:00:00Z",
	})

	rows := f.project(t, "cafeLedgerHistory", ledgerHistorySpec)
	require.Empty(t, rows, "postedTo is a REQUIRED match — an unposted transaction must not project a row with a null accountKey")
}

func TestCafeLedgerHistory_AccountNotHeldForLease_ProjectsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "loose_acct", "cafeaccount")
	f.vtx(t, "loose_tx", "cafetransaction")
	f.edge(t, "postedTo", "loose_tx", "loose_acct")
	f.aspect(t, "loose_tx", "entry", "cafetransaction", map[string]any{
		"type": "credit", "amountCents": 900.0, "postedAt": "2026-07-25T00:00:00Z",
	})

	rows := f.project(t, "cafeLedgerHistory", ledgerHistorySpec)
	require.Empty(t, rows, "heldFor is a REQUIRED match — an account with no lease must not project")
}

func TestCafeLeaseAccounts_LeaseWithNoAccount_ProjectsNullAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "fresh_lease", "leaseapp")

	rows := f.project(t, "cafeLeaseAccounts", leaseAccountsSpec)
	require.Len(t, rows, 1, "the lens anchors on the LEASE, so a never-charged lease still gets a row")
	v := rows[0].Values
	require.Equal(t, "vtx.leaseapp."+f.ids["fresh_lease"], v["leaseAppKey"])
	require.Nil(t, v["accountKey"], "no café account opened yet — this is the row the FE reads before a first charge")
}

func TestCafeLeaseAccounts_LeaseWithAccount_ProjectsAccountKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "held_lease", "leaseapp")
	f.vtx(t, "held_acct", "cafeaccount")
	f.edge(t, "heldFor", "held_acct", "held_lease")

	rows := f.project(t, "cafeLeaseAccounts", leaseAccountsSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.leaseapp."+f.ids["held_lease"], v["leaseAppKey"])
	require.Equal(t, "vtx.cafeaccount."+f.ids["held_acct"], v["accountKey"], "the heldFor hop is walked INBOUND from the lease")
}

// --- cafeArrearsReminders ---
//
// The arrears convergence lens reads NO clock: every comparison is between two
// stored facts (the recorded due date, the recorded reminder, and the instant a
// fired @at recorded on this account's own freshnessExpiry marker). Every
// conjunct of freshUntil and of the single gap therefore gets a vector here,
// because a mis-compiled lens FALLS BACK silently on the full engine — a shape
// the engine cannot compile projects nothing rather than erroring, and only a
// pin that runs the real engine can tell the difference.

// projectArrears runs the anchored cafeArrearsReminders spec for one account.
// NO clock parameter is supplied: the cypher references none, and passing one
// would let a clock-reading regression pass unnoticed here.
func (f *lensFixture) projectArrears(t *testing.T, acctName string) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(cafeArrearsRemindersSpec)
	require.NoError(t, err, "cafeArrearsReminders cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": "vtx.cafeaccount." + f.ids[acctName],
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkArrearsAccount seeds a cafeaccount held for a lease, optionally carrying a
// .arrears aspect. A nil arrears map seeds the account with no aspect at all —
// the never-evaluated shape every account alive at install has.
func (f *lensFixture) mkArrearsAccount(t *testing.T, prefix string, arrears map[string]any) {
	t.Helper()
	f.vtx(t, prefix+"_lease", "leaseapp")
	f.vtx(t, prefix+"_acct", "cafeaccount")
	f.edge(t, "heldFor", prefix+"_acct", prefix+"_lease")
	if arrears != nil {
		f.aspect(t, prefix+"_acct", "arrears", "cafeAccountArrears", arrears)
	}
}

// recordLapse writes the freshnessExpiry marker MarkExpired commits onto the
// ACCOUNT when this target's @at fires: the instant the timer fired for,
// recorded under the target's own key in byTarget, with expiredAt carrying the
// entity-wide maximum. Copied from wellness-reminders' own fixture — the marker
// shape is orchestration-base's, not this package's.
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

// TestCafeArrears_NeverEvaluated — an account carrying no .arrears at all
// (every account alive before this shipped). evaluatedAt is null, so the gap is
// open from its first projection: that is how each standing account gets its
// one evaluation. freshUntil is null — there is no recorded due date to arm a
// timer at.
func TestCafeArrears_NeverEvaluated(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "virgin", nil)

	rows := f.projectArrears(t, "virgin_acct")
	require.Len(t, rows, 1, "exactly one row per account even with the lease linked")
	v := rows[0].Values
	require.Equal(t, "vtx.cafeaccount."+f.ids["virgin_acct"], v["entityKey"])
	require.Equal(t, "vtx.cafeaccount."+f.ids["virgin_acct"], v["actorKey"])
	require.Equal(t, "vtx.leaseapp."+f.ids["virgin_lease"], v["leaseAppKey"], "the playbook routes this into the op's leaseAppKey param")
	require.Equal(t, true, v["missing_evaluation"], "an account nothing has ever aged is violating on sight")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "no recorded due date, so no timer arms")
}

// TestCafeArrears_Pending — a recorded due date still ahead of any fired timer:
// NOT violating, and freshUntil = dueAt arms the @at.
func TestCafeArrears_Pending(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "pending", map[string]any{
		"dueAt":       "2026-09-11T10:00:00Z",
		"evaluatedAt": "2026-08-27T10:00:00Z",
	})

	rows := f.projectArrears(t, "pending_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "no timer has fired on this account — not due")
	require.Equal(t, false, v["violating"])
	require.Equal(t, "2026-09-11T10:00:00Z", v["freshUntil"], "freshUntil = dueAt arms the @at timer")
	_, isString := v["freshUntil"].(string)
	require.True(t, isString, "freshUntil must be a scalar string so scheduleFreshness can parse it as RFC3339")
	require.Equal(t, "2026-09-11T10:00:00Z", v["dueAt"])
	require.Nil(t, v["reminderSentAt"], "nothing has gone out yet")
}

// TestCafeArrears_Due — the @at has FIRED and its lapse is recorded at dueAt,
// and nothing has been reminded for that date: the gap OPENS. freshUntil goes
// null once the lapse is recorded — a one-shot wake-up, not a re-arm; the
// violating row itself drives the dispatch from here.
func TestCafeArrears_Due(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "due", map[string]any{
		"dueAt":       "2026-08-06T14:20:00Z",
		"evaluatedAt": "2026-07-22T14:20:00Z",
	})
	f.recordLapse(t, "due_acct", map[string]string{CafeArrearsRemindersTarget: "2026-08-06T14:20:00Z"})

	rows := f.projectArrears(t, "due_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["missing_evaluation"], "the recorded lapse reaches dueAt and nothing was reminded for it")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "the timer fired; it is not re-armed")
}

// TestCafeArrears_Sent — the same account after EvaluateCafeArrears sent the
// reminder: remindedFor = dueAt closes the gap, and freshUntil stays null. This
// is the "no re-dispatch after a send" assertion — a gap that re-opened every
// convergence window would mint a fresh notification each time.
func TestCafeArrears_Sent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "sent", map[string]any{
		"dueAt":       "2026-08-06T14:20:00Z",
		"remindedFor": "2026-08-06T14:20:00Z",
		"sentAt":      "2026-08-06T14:25:00Z",
		"evaluatedAt": "2026-08-06T14:25:00Z",
	})
	f.recordLapse(t, "sent_acct", map[string]string{CafeArrearsRemindersTarget: "2026-08-06T14:20:00Z"})

	rows := f.projectArrears(t, "sent_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "one reminder per episode: the recorded send closes the gap for good")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "remindedFor = dueAt, so no timer re-arms either")
	require.Equal(t, "2026-08-06T14:25:00Z", v["reminderSentAt"], "the front desk and the resident both read this")
}

// TestCafeArrears_Stale — a partial payment moved the FIFO head somewhere
// post_entry cannot compute, so it marked the recorded state stale. The gap
// opens with no timer involved at all, and freshUntil is suppressed even though
// the recorded dueAt is still in the future and unreminded: arming a timer at a
// date already known to be wrong would fire a reminder for the wrong charge.
func TestCafeArrears_Stale(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "stale", map[string]any{
		"dueAt":       "2026-09-11T10:00:00Z",
		"evaluatedAt": "2026-08-27T10:00:00Z",
		"stale":       true,
	})

	rows := f.projectArrears(t, "stale_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, true, v["missing_evaluation"], "stale opens the gap directly — no fired timer needed")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "a due date known to be stale must not arm a timer")
	require.Equal(t, true, v["stale"])
}

// TestCafeArrears_Cleared — the account was paid off, so the evaluation rewrote
// .arrears to {evaluatedAt} alone. No dueAt: nothing violating, no timer, and
// nothing left of the finished episode to make the NEXT charge look reminded.
func TestCafeArrears_Cleared(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "cleared", map[string]any{"evaluatedAt": "2026-08-20T09:00:00Z"})
	// The marker from the episode that has just ended stays on the account
	// forever (orchestration-base merges, never clears). It must not make a
	// cleared account violating.
	f.recordLapse(t, "cleared_acct", map[string]string{CafeArrearsRemindersTarget: "2026-08-06T14:20:00Z"})

	rows := f.projectArrears(t, "cleared_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "nothing is owed — the episode is over")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
	require.Nil(t, v["dueAt"])
}

// TestCafeArrears_NewEpisodeAfterOldLapse is the standing-marker vector: the
// freshnessExpiry entry from a PREVIOUS episode is permanent, and a new episode
// whose dueAt is later than that recorded instant must re-arm rather than open
// on the old lapse. It always is later — a new episode's charge posts after the
// last one ended and both add the same net term — so this pins the ordering the
// design leans on.
func TestCafeArrears_NewEpisodeAfterOldLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "reopened", map[string]any{
		"dueAt":       "2026-09-20T08:00:00Z",
		"evaluatedAt": "2026-09-05T08:00:00Z",
	})
	f.recordLapse(t, "reopened_acct", map[string]string{CafeArrearsRemindersTarget: "2026-08-06T14:20:00Z"})

	rows := f.projectArrears(t, "reopened_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "the old lapse predates this episode's due date")
	require.Equal(t, "2026-09-20T08:00:00Z", v["freshUntil"], "a fresh @at arms for the new episode")
}

// TestCafeArrears_SiblingTargetLapseDoesNotOpen pins the byTarget indirection:
// the marker is shared by every target that arms a timer on this anchor, and
// only THIS target's own entry may open this gap. Reading expiredAt (the
// entity-wide maximum) instead would have any sibling's fired timer send an
// arrears reminder.
func TestCafeArrears_SiblingTargetLapseDoesNotOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "sibling", map[string]any{
		"dueAt":       "2026-09-11T10:00:00Z",
		"evaluatedAt": "2026-08-27T10:00:00Z",
	})
	f.recordLapse(t, "sibling_acct", map[string]string{"someOtherTarget": "2026-09-30T00:00:00Z"})

	rows := f.projectArrears(t, "sibling_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_evaluation"], "another target's fired timer is not this gap's evidence")
	require.Equal(t, "2026-09-11T10:00:00Z", v["freshUntil"], "and it must not suppress this target's own @at either")
}

// TestCafeArrears_HistoryTooLongGoesQuiet pins the degrade posture, and it is
// the ONE vector where a recorded lapse must NOT open the gap. An account whose
// transaction history outran the evaluation's replay budget carries
// historyTooLong: the op cannot compute a head for it, so leaving the gap open
// would have Weaver re-dispatch an evaluation that can only fail again, every
// window, forever, with nothing sent and nothing said. Both suppressions are
// asserted together — a lens that killed only the gap would still arm an @at at
// a dueAt no evaluation could confirm, and one that killed only the timer would
// still re-dispatch. The row itself stays projected, which is the operator's
// signal in the weaver-targets bucket.
func TestCafeArrears_HistoryTooLongGoesQuiet(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "toolong", map[string]any{
		"dueAt":          "2026-08-06T14:20:00Z",
		"evaluatedAt":    "2026-08-22T09:00:00Z",
		"historyTooLong": true,
	})
	// The lapse IS recorded and nothing was reminded for it: without the
	// historyTooLong conjunct this is TestCafeArrears_Due exactly, so the vector
	// reds the moment either suppression is dropped.
	f.recordLapse(t, "toolong_acct", map[string]string{CafeArrearsRemindersTarget: "2026-08-06T14:20:00Z"})

	rows := f.projectArrears(t, "toolong_acct")
	require.Len(t, rows, 1, "the row must stay projected — quiet is not invisible")
	v := rows[0].Values
	require.Equal(t, true, v["historyTooLong"], "the operator reads this column off the weaver-targets row")
	require.Equal(t, false, v["missing_evaluation"], "an evaluation that cannot succeed must not be re-dispatched every window")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "and no timer arms at a due date no evaluation could confirm")
}

// TestCafeArrears_HistoryTooLongClearedReopens is the positive vector the one
// above is measured against: the SAME state with the flag absent IS violating.
// Without it a lens that had simply stopped computing missing_evaluation would
// pass the suppression pin.
func TestCafeArrears_HistoryTooLongClearedReopens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkArrearsAccount(t, "cleardone", map[string]any{
		"dueAt":       "2026-08-06T14:20:00Z",
		"evaluatedAt": "2026-08-22T09:00:00Z",
	})
	f.recordLapse(t, "cleardone_acct", map[string]string{CafeArrearsRemindersTarget: "2026-08-06T14:20:00Z"})

	rows := f.projectArrears(t, "cleardone_acct")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Nil(t, v["historyTooLong"])
	require.Equal(t, true, v["missing_evaluation"], "the identical row without the flag opens the gap")
}

// TestCafeArrears_NoLeaseStillProjects pins the heldFor walk as OPTIONAL. An
// account with no lease is not a shape CreateAccount produces, but a lease
// tombstoned out from under a live account is — and the row for such an account
// must still project, because it is the row that carries the gap: a required
// MATCH would silently stop aging that resident's tab and remind nobody.
// leaseAppKey comes back null, which the playbook routes as an absent optional
// param — the op's own leaseAppKey is optional for exactly this reason.
func TestCafeArrears_NoLeaseStillProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "nolease_acct", "cafeaccount")
	f.aspect(t, "nolease_acct", "arrears", "cafeAccountArrears", map[string]any{
		"dueAt":       "2026-09-11T10:00:00Z",
		"evaluatedAt": "2026-08-27T10:00:00Z",
	})

	rows := f.projectArrears(t, "nolease_acct")
	require.Len(t, rows, 1, "an account with no heldFor lease still projects its arrears row")
	v := rows[0].Values
	require.Equal(t, "vtx.cafeaccount."+f.ids["nolease_acct"], v["entityKey"])
	require.Nil(t, v["leaseAppKey"], "no lease to walk to")
	require.Equal(t, false, v["missing_evaluation"])
	require.Equal(t, "2026-09-11T10:00:00Z", v["freshUntil"], "and the timer arms exactly as it would with a lease")
}

// TestCafeLeaseAccounts_ProjectsArrearsColumns pins the three informational
// columns the front-desk grid and the resident statement read for "when did the
// reminder go out" — they come off the ACCOUNT's aspect through the lens's
// inbound heldFor hop, so a lease with no account still projects nulls.
func TestCafeLeaseAccounts_ProjectsArrearsColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "arr_lease", "leaseapp")
	f.vtx(t, "arr_acct", "cafeaccount")
	f.edge(t, "heldFor", "arr_acct", "arr_lease")
	f.aspect(t, "arr_acct", "arrears", "cafeAccountArrears", map[string]any{
		"dueAt":       "2026-08-06T14:20:00Z",
		"remindedFor": "2026-08-06T14:20:00Z",
		"sentAt":      "2026-08-06T14:25:00Z",
		"evaluatedAt": "2026-08-06T14:25:00Z",
	})

	rows := f.project(t, "cafeLeaseAccounts", leaseAccountsSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.cafeaccount."+f.ids["arr_acct"], v["accountKey"])
	require.Equal(t, "2026-08-06T14:20:00Z", v["arrearsDueAt"])
	require.Equal(t, "2026-08-06T14:20:00Z", v["arrearsRemindedFor"])
	require.Equal(t, "2026-08-06T14:25:00Z", v["arrearsReminderSentAt"])
}
