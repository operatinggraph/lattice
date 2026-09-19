package loftspaceledger

// Rule-engine proof of both business lenses, driven through the `full` engine
// (engine:"full") against an embedded NATS Core/Adjacency KV — the harness
// clinic-ledger / cafe-domain / lease-signing use.
//
// The two lenses make opposite claims about their MATCH shapes, and each claim
// is what these tests hold:
//
//   - ledgerHistory's postedTo/heldFor hops are REQUIRED, so a transaction
//     projects a row only when it is genuinely posted to a live account held
//     for a live lease. A dangling transaction must project NOTHING; a lens
//     that silently relaxed those to OPTIONAL would emit rows with a null
//     accountKey and a reader summing amountCents per account would drop them.
//   - ledgerHistory's authorizedBy hop is OPTIONAL, so a plain human-submitted
//     charge with no clause still projects, with a null clauseKey.
//   - leaseAccounts anchors on the LEASE, not the account, so a lease that has
//     never been charged still gets a row with a null accountKey — that is the
//     "has this lease opened an account yet" question the FE asks before its
//     first charge, and it cannot be answered by a lens that anchors on the
//     account.

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

// mkPostedCharge seeds the whole shape a committed DebitAccount produces: a
// transaction posted to an account held for a lease.
func (f *lensFixture) mkPostedCharge(t *testing.T, prefix string, amountCents float64, memo string) {
	t.Helper()
	f.vtx(t, prefix+"_lease", "leaseapp")
	f.vtx(t, prefix+"_acct", "account")
	f.vtx(t, prefix+"_tx", "transaction")
	f.edge(t, "heldFor", prefix+"_acct", prefix+"_lease")
	f.edge(t, "postedTo", prefix+"_tx", prefix+"_acct")
	f.aspect(t, prefix+"_tx", "entry", "transaction", map[string]any{
		"type":        "debit",
		"amountCents": amountCents,
		"memo":        memo,
		"postedAt":    "2026-07-25T00:00:00Z",
	})
}

func TestLedgerHistory_PostedCharge_ProjectsRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkPostedCharge(t, "posted", 125000, "July rent")

	rows := f.project(t, "ledgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.transaction."+f.ids["posted_tx"], v["key"])
	require.Equal(t, "vtx.transaction."+f.ids["posted_tx"], v["transactionKey"])
	require.Equal(t, "vtx.account."+f.ids["posted_acct"], v["accountKey"])
	require.Equal(t, "vtx.leaseapp."+f.ids["posted_lease"], v["leaseAppKey"])
	require.Equal(t, "debit", v["type"])
	require.Equal(t, 125000.0, v["amountCents"])
	require.Equal(t, "July rent", v["memo"])
	require.Equal(t, "2026-07-25T00:00:00Z", v["postedAt"])
	require.Nil(t, v["clauseKey"], "a plain human-submitted charge carries no clause")
	require.Nil(t, v["clauseProse"])
	require.Nil(t, v["clausePurpose"])
}

func TestLedgerHistory_UnpostedTransaction_ProjectsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	// A transaction with its entry aspect but no postedTo edge — the shape a
	// half-written commit would leave behind.
	f.vtx(t, "orphan_tx", "transaction")
	f.aspect(t, "orphan_tx", "entry", "transaction", map[string]any{
		"type": "debit", "amountCents": 500.0, "postedAt": "2026-07-25T00:00:00Z",
	})

	rows := f.project(t, "ledgerHistory", ledgerHistorySpec)
	require.Empty(t, rows, "postedTo is a REQUIRED match — an unposted transaction must not project a row with a null accountKey")
}

func TestLedgerHistory_AccountNotHeldForLease_ProjectsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "loose_acct", "account")
	f.vtx(t, "loose_tx", "transaction")
	f.edge(t, "postedTo", "loose_tx", "loose_acct")
	f.aspect(t, "loose_tx", "entry", "transaction", map[string]any{
		"type": "credit", "amountCents": 900.0, "postedAt": "2026-07-25T00:00:00Z",
	})

	rows := f.project(t, "ledgerHistory", ledgerHistorySpec)
	require.Empty(t, rows, "heldFor is a REQUIRED match — an account with no lease must not project")
}

func TestLedgerHistory_AuthorizedByClause_ProjectsProse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkPostedCharge(t, "clausal", 125000, "August rent")
	f.vtx(t, "clausal_clause", "clause")
	f.aspect(t, "clausal_clause", "prose", "clause", map[string]any{
		"text": "Rent is due on the first of each month.",
	})
	f.aspect(t, "clausal_clause", "terms", "clauseTerms", map[string]any{
		"kind": "computational", "conditioned": false, "amountCents": 125000.0, "period": "monthly",
	})
	f.edge(t, "authorizedBy", "clausal_tx", "clausal_clause")

	rows := f.project(t, "ledgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.clause."+f.ids["clausal_clause"], v["clauseKey"], "the authorizedBy hop answers 'why was I charged this?'")
	require.Equal(t, "Rent is due on the first of each month.", v["clauseProse"])
	require.Nil(t, v["clausePurpose"], "a clause minted without a purpose token projects null, never a default")
}

// TestLedgerHistory_DepositEntries_ProjectClausePurpose — the deposit's
// charge and its return credit are both authorizedBy the same purpose=deposit
// clause, and each row carries that token: the statement holds the deposit
// apart from rent by this column, never by the memo.
func TestLedgerHistory_DepositEntries_ProjectClausePurpose(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkPostedCharge(t, "dep", 250000, "")
	f.vtx(t, "dep_clause", "clause")
	f.aspect(t, "dep_clause", "prose", "clause", map[string]any{"text": "Security deposit, held for the tenancy and returned when it ends."})
	f.aspect(t, "dep_clause", "terms", "clauseTerms", map[string]any{
		"kind": "computational", "conditioned": false, "amountCents": 250000.0, "period": "oneTime", "purpose": "deposit",
	})
	f.edge(t, "authorizedBy", "dep_tx", "dep_clause")
	// The return credit, on the same account, authorizedBy the same clause.
	f.vtx(t, "depret_tx", "transaction")
	f.edge(t, "postedTo", "depret_tx", "dep_acct")
	f.aspect(t, "depret_tx", "entry", "transaction", map[string]any{
		"type": "credit", "amountCents": 250000.0, "postedAt": "2027-07-03T09:00:00Z", "memo": "Security deposit returned",
	})
	f.edge(t, "authorizedBy", "depret_tx", "dep_clause")

	rows := f.project(t, "ledgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 2)
	for _, r := range rows {
		require.Equal(t, "deposit", r.Values["clausePurpose"], "row %v", r.Values["transactionKey"])
		require.Equal(t, "vtx.clause."+f.ids["dep_clause"], r.Values["clauseKey"])
	}
}

func TestLeaseAccounts_LeaseWithNoAccount_ProjectsNullAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "fresh_lease", "leaseapp")

	rows := f.project(t, "leaseAccounts", leaseAccountsSpec)
	require.Len(t, rows, 1, "the lens anchors on the LEASE, so a never-charged lease still gets a row")
	v := rows[0].Values
	require.Equal(t, "vtx.leaseapp."+f.ids["fresh_lease"], v["leaseAppKey"])
	require.Nil(t, v["accountKey"], "no account opened yet — this is the row the FE reads before a first charge")
}

func TestLeaseAccounts_LeaseWithAccount_ProjectsAccountKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "held_lease", "leaseapp")
	f.vtx(t, "held_acct", "account")
	f.edge(t, "heldFor", "held_acct", "held_lease")

	rows := f.project(t, "leaseAccounts", leaseAccountsSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.leaseapp."+f.ids["held_lease"], v["leaseAppKey"])
	require.Equal(t, "vtx.account."+f.ids["held_acct"], v["accountKey"], "the heldFor hop is walked INBOUND from the lease")
}

// TestLedgerHistory_LateFee_ProjectsBilledForKey — the late fee's billedFor
// hop projects the head charge it was billed for; every other row (the
// charge itself, a payment) projects null.
func TestLedgerHistory_LateFee_ProjectsBilledForKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "bf_lease", "leaseapp")
	f.vtx(t, "bf_acct", "account")
	f.edge(t, "heldFor", "bf_acct", "bf_lease")
	f.vtx(t, "bf_rent", "transaction")
	f.aspect(t, "bf_rent", "entry", "transactionEntry", map[string]any{"type": "debit", "amountCents": 240000.0, "postedAt": "2026-09-01T09:00:00Z", "dueAt": "2026-09-01T09:00:00Z"})
	f.edge(t, "postedTo", "bf_rent", "bf_acct")
	f.vtx(t, "bf_fee", "transaction")
	f.aspect(t, "bf_fee", "entry", "transactionEntry", map[string]any{"type": "debit", "amountCents": 5000.0, "postedAt": "2026-09-12T09:00:00Z", "dueAt": "2026-09-12T09:00:00Z", "memo": "Late fee — rent due 2026-09-01"})
	f.edge(t, "postedTo", "bf_fee", "bf_acct")
	f.edge(t, "billedFor", "bf_fee", "bf_rent")

	rows := f.project(t, "ledgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 2)
	byKey := map[string]map[string]any{}
	for _, r := range rows {
		byKey[r.Values["transactionKey"].(string)] = r.Values
	}
	require.Equal(t, "vtx.transaction."+f.ids["bf_rent"], byKey["vtx.transaction."+f.ids["bf_fee"]]["billedForKey"], "the fee names the charge it was billed for")
	require.Nil(t, byKey["vtx.transaction."+f.ids["bf_rent"]]["billedForKey"], "the charge names nothing")
	require.Nil(t, byKey["vtx.transaction."+f.ids["bf_fee"]]["reversesKey"])
}

// TestLeaseAccounts_ProjectsTheArrearsColumns — the four arrears columns read
// straight off the account's own .arrears aspect: dueAt, remindedFor, sentAt
// and lateFeeAt (the instant the episode's late fee was billed), each null
// where the aspect carries none, so the landlord ledger, the tenant
// statement and the portfolio row can say when the rent fell due, when the
// reminder went out and when the fee was billed.
func TestLeaseAccounts_ProjectsTheArrearsColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "arr_lease", "leaseapp")
	f.vtx(t, "arr_acct", "account")
	f.edge(t, "heldFor", "arr_acct", "arr_lease")
	f.aspect(t, "arr_acct", "arrears", "loftspaceAccountArrears", map[string]any{
		"evaluatedAt": "2026-09-12T09:00:00Z", "dueAt": "2026-09-01T09:00:00Z", "remindAt": "2026-09-06T09:00:00Z",
		"remindedFor": "2026-09-01T09:00:00Z", "sentAt": "2026-09-12T09:00:00Z", "lateFeeAt": "2026-09-12T09:00:00Z",
	})

	rows := f.project(t, "leaseAccounts", leaseAccountsSpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "2026-09-01T09:00:00Z", v["arrearsDueAt"])
	require.Equal(t, "2026-09-01T09:00:00Z", v["arrearsRemindedFor"])
	require.Equal(t, "2026-09-12T09:00:00Z", v["arrearsReminderSentAt"])
	require.Equal(t, "2026-09-12T09:00:00Z", v["arrearsLateFeeAt"], "arrearsLateFeeAt is a.arrears.data.lateFeeAt verbatim")

	// An episode reminded for before the lease had a fee term: sentAt without
	// lateFeeAt, the column null rather than borrowed from sentAt.
	f.vtx(t, "nofee_lease", "leaseapp")
	f.vtx(t, "nofee_acct", "account")
	f.edge(t, "heldFor", "nofee_acct", "nofee_lease")
	f.aspect(t, "nofee_acct", "arrears", "loftspaceAccountArrears", map[string]any{
		"evaluatedAt": "2026-09-12T09:00:00Z", "dueAt": "2026-09-01T09:00:00Z", "remindAt": "2026-09-06T09:00:00Z",
		"remindedFor": "2026-09-01T09:00:00Z", "sentAt": "2026-09-12T09:00:00Z",
	})
	for _, r := range f.project(t, "leaseAccounts", leaseAccountsSpec) {
		if r.Values["leaseAppKey"] == "vtx.leaseapp."+f.ids["nofee_lease"] {
			require.Equal(t, "2026-09-12T09:00:00Z", r.Values["arrearsReminderSentAt"])
			require.Nil(t, r.Values["arrearsLateFeeAt"])
		}
	}
}

// TestLedgerHistory_RecurringCharge_ProjectsItsPeriodAndDueDate — a charge
// whose .entry records the period it bills and its due date (DebitAccount's
// termed/monthly stamp) projects all three; a plain charge with none
// projects null for each, never an error.
func TestLedgerHistory_RecurringCharge_ProjectsItsPeriodAndDueDate(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkPostedCharge(t, "rent", 212500, "")
	f.aspect(t, "rent_tx", "entry", "transaction", map[string]any{
		"type": "debit", "amountCents": 212500.0, "postedAt": "2026-09-13T23:49:30Z",
		"periodStart": "2026-09-06T00:00:00Z", "periodEnd": "2026-10-06T00:00:00Z", "dueAt": "2026-09-06T00:00:00Z",
	})
	f.mkPostedCharge(t, "plain", 4500, "Lockout fee")

	rows := f.project(t, "ledgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 2)
	byKey := map[string]map[string]any{}
	for _, r := range rows {
		byKey[r.Values["transactionKey"].(string)] = r.Values
	}
	rent := byKey["vtx.transaction."+f.ids["rent_tx"]]
	require.Equal(t, "2026-09-06T00:00:00Z", rent["periodStart"])
	require.Equal(t, "2026-10-06T00:00:00Z", rent["periodEnd"])
	require.Equal(t, "2026-09-06T00:00:00Z", rent["dueAt"])
	plain := byKey["vtx.transaction."+f.ids["plain_tx"]]
	require.Nil(t, plain["periodStart"], "a one-time charge records no period")
	require.Nil(t, plain["periodEnd"])
	require.Nil(t, plain["dueAt"])
}

// TestLoftspaceLedgerHistory_Reverses_ProjectsReversesKey — a credit that
// reverses a charge carries the charge's key as reversesKey (the outbound
// reverses hop), a plain charge carries null, and the reversed charge itself
// reverses nothing — the hop is outbound only. The column is what the
// statement and the landlord's ledger net the credit against and label the
// row by.
func TestLoftspaceLedgerHistory_Reverses_ProjectsReversesKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.mkPostedCharge(t, "rev", 205000, "September rent")
	f.vtx(t, "rev_credit", "transaction")
	f.edge(t, "postedTo", "rev_credit", "rev_acct")
	f.edge(t, "reverses", "rev_credit", "rev_tx")
	f.aspect(t, "rev_credit", "entry", "transaction", map[string]any{
		"type": "credit", "amountCents": 205000.0, "memo": "Reversal: rent billed after the lease end", "postedAt": "2026-09-13T00:00:00Z",
	})

	rows := f.project(t, "ledgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 2)
	byKey := map[string]map[string]any{}
	for _, r := range rows {
		byKey[r.Values["key"].(string)] = r.Values
	}
	credit := byKey["vtx.transaction."+f.ids["rev_credit"]]
	require.NotNil(t, credit)
	require.Equal(t, "vtx.transaction."+f.ids["rev_tx"], credit["reversesKey"],
		"the reverses link names the charge this credit corrects")
	require.Nil(t, credit["clauseKey"], "a reversal authorizes off no clause")
	debit := byKey["vtx.transaction."+f.ids["rev_tx"]]
	require.NotNil(t, debit)
	require.Nil(t, debit["reversesKey"], "the reversed charge itself reverses nothing — the hop is outbound only")
}
