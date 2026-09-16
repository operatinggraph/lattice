package semanticcontracts

// Rule-engine proof of leaseRentSettlement's two deposit gaps over one
// fixture set (the LoftSpace "a lease takes a security deposit" design):
//
//   - no .deposit on the lease — neither gap, depositCents null.
//   - .deposit + account, no clause — missing_deposit opens with the ×100
//     amount templated; the return stays shut.
//   - a purpose=deposit clause still active (minted, uncharged) — mint shut;
//     the return never opens on an uncharged deposit, even once ended.
//   - the clause completed (charged), tenancy not ended — the return waits.
//   - completed + endedAt — the return opens with the clause key templated.
//   - the clause returned + endedAt — both shut: the return's own write
//     closes its gap, and a returned clause still counts as minted.
//   - a one-time clause WITHOUT the purpose token on the same lease is never
//     counted by either gap.
//   - missing_deposit and missing_clause are independent in both directions.
//   - an ended tenancy that never had a deposit clause is never minted one.
//   - a purpose=deposit clause of another archetype (monthly, judgment —
//     seeded; mint_clause refuses the shape) holds the mint shut and is
//     never a return candidate.
//   - two charged deposit clauses are returned one per pass.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const depositEndedAt = "2027-07-01T00:00:00Z"

// mkDepositLease seeds an approved, accounted lease inside its original term
// (only missing_clause can be open on it) that records a $2,500 deposit and,
// when endedAt is non-empty, an ended tenancy.
func (f *bcFixture) mkDepositLease(t *testing.T, name, endedAt string) {
	t.Helper()
	tenancy := map[string]any{"leaseStart": origStart, "leaseEnd": origEnd}
	if endedAt != "" {
		tenancy["endedAt"] = endedAt
	}
	f.mkRentLease(t, name, 2500, tenancy)
	f.aspect(t, name, "deposit", "leaseDeposit", map[string]any{"amount": 2500.0, "recordedAt": "2025-09-01T00:00:00Z"})
}

// mkOneTimeClause seeds a oneTime computational clause governing the lease
// with the given purpose token ("" = none) and .status state.
func (f *bcFixture) mkOneTimeClause(t *testing.T, name, leaseName, purpose, state string) {
	t.Helper()
	f.vtx(t, name, "clause")
	terms := map[string]any{"kind": "computational", "conditioned": false, "amountCents": 250000.0, "period": "oneTime"}
	if purpose != "" {
		terms["purpose"] = purpose
	}
	f.aspect(t, name, "terms", "clauseTerms", terms)
	f.aspect(t, name, "status", "clauseStatus", map[string]any{"state": state})
	f.edge(t, "governs", name, leaseName)
}

// mkPurposeClause seeds a computational clause of the given period, or a
// judgment clause when kind is "judgment", carrying the purpose token.
func (f *bcFixture) mkPurposeClause(t *testing.T, name, leaseName, purpose, period, kind, state string) {
	t.Helper()
	f.vtx(t, name, "clause")
	terms := map[string]any{"kind": kind, "conditioned": false, "period": period, "purpose": purpose}
	if kind == "computational" {
		terms["amountCents"] = 250000.0
	}
	f.aspect(t, name, "terms", "clauseTerms", terms)
	f.aspect(t, name, "status", "clauseStatus", map[string]any{"state": state})
	f.edge(t, "governs", name, leaseName)
}

func TestLeaseRentSettlement_NoDeposit_NeitherDepositGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkRentLease(t, "nodeplease", 2500, map[string]any{"leaseStart": origStart, "leaseEnd": origEnd, "endedAt": depositEndedAt})

	v := f.projectLeaseAt(t, "nodeplease")[0].Values
	require.Equal(t, false, v["missing_deposit"], "a lease that records no deposit takes none")
	require.Equal(t, false, v["missing_depositReturn"])
	require.Nil(t, v["depositAmount"])
	require.Nil(t, v["depositCents"], "the CASE guard keeps depositCents null rather than failing the row on a nil operand")
	require.Nil(t, v["depositClauseKey"])
	require.Equal(t, depositEndedAt, v["endedAt"])
}

func TestLeaseRentSettlement_DepositRecorded_NoClause_MissingDeposit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkDepositLease(t, "deplease", "")

	v := f.projectLeaseAt(t, "deplease")[0].Values
	require.Equal(t, true, v["missing_deposit"])
	require.Equal(t, 2500.0, v["depositAmount"])
	require.Equal(t, 250000.0, v["depositCents"], "the dispatch templates the lens's own ×100 conversion, never a dollar column")
	require.Equal(t, int64(0), v["depositClauseCount"])
	require.Equal(t, false, v["missing_depositReturn"], "nothing minted, nothing to return")
	require.Equal(t, true, v["violating"])
	require.Equal(t, "vtx.account."+f.ids["deplease_acct"], v["accountKey"], "the templated account is this same row's")
}

func TestLeaseRentSettlement_DepositRecorded_NoAccount_WaitsForTheAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkApprovedLeaseWithTerms(t, "depnoacct", 2500)
	f.aspect(t, "depnoacct", "tenancy", "tenancy", map[string]any{"leaseStart": origStart, "leaseEnd": origEnd})
	f.aspect(t, "depnoacct", "deposit", "leaseDeposit", map[string]any{"amount": 2500.0, "recordedAt": "2025-09-01T00:00:00Z"})

	v := f.projectLeaseAt(t, "depnoacct")[0].Values
	require.Equal(t, true, v["missing_account"])
	require.Equal(t, false, v["missing_deposit"], "the deposit clause charges the account, so the mint waits on it exactly as missing_clause does")
}

func TestLeaseRentSettlement_DepositClauseActive_MintShutReturnShut(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkDepositLease(t, "depactive", depositEndedAt)
	f.mkOneTimeClause(t, "depactive_dep", "depactive", "deposit", "active")

	v := f.projectLeaseAt(t, "depactive")[0].Values
	require.Equal(t, false, v["missing_deposit"], "the clause exists — one mint per lease")
	require.Equal(t, int64(1), v["depositClauseCount"])
	require.Nil(t, v["depositClauseKey"], "an uncharged (active) deposit is never a return candidate")
	require.Equal(t, false, v["missing_depositReturn"], "the tenancy has ended, but the deposit was never charged — nothing to return")
	require.Equal(t, true, v["missing_clause"], "no rent clause on this fixture — the one gap open, and the deposit clause never suppresses it")
}

func TestLeaseRentSettlement_DepositClauseCompleted_NotEnded_ReturnWaits(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkDepositLease(t, "depcharged", "")
	f.mkOneTimeClause(t, "depcharged_dep", "depcharged", "deposit", "completed")

	v := f.projectLeaseAt(t, "depcharged")[0].Values
	require.Equal(t, false, v["missing_deposit"])
	require.Equal(t, "vtx.clause."+f.ids["depcharged_dep"], v["depositClauseKey"], "charged: the clause is the return candidate")
	require.Nil(t, v["endedAt"])
	require.Equal(t, false, v["missing_depositReturn"], "the return rides the recorded end, which is not yet recorded")
}

func TestLeaseRentSettlement_DepositClauseCompleted_Ended_MissingDepositReturn(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkDepositLease(t, "depended", depositEndedAt)
	f.mkOneTimeClause(t, "depended_dep", "depended", "deposit", "completed")

	v := f.projectLeaseAt(t, "depended")[0].Values
	require.Equal(t, true, v["missing_depositReturn"])
	require.Equal(t, "vtx.clause."+f.ids["depended_dep"], v["depositClauseKey"], "the dispatch's clauseKey")
	require.Equal(t, "vtx.account."+f.ids["depended_acct"], v["accountKey"], "the dispatch's accountKey")
	require.Equal(t, depositEndedAt, v["endedAt"])
	require.Equal(t, false, v["missing_deposit"])
	require.Equal(t, true, v["violating"])
}

func TestLeaseRentSettlement_DepositClauseReturned_Ended_BothShut(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkDepositLease(t, "depreturned", depositEndedAt)
	f.mkOneTimeClause(t, "depreturned_dep", "depreturned", "deposit", "returned")

	v := f.projectLeaseAt(t, "depreturned")[0].Values
	require.Equal(t, false, v["missing_depositReturn"], "returned is ReturnDeposit's own write — the state the gap closes on")
	require.Nil(t, v["depositClauseKey"])
	require.Equal(t, false, v["missing_deposit"], "a returned clause is still the minted deposit clause; the mint never re-opens")
	require.Equal(t, int64(1), v["depositClauseCount"])
}

func TestLeaseRentSettlement_OneTimeClauseWithoutPurpose_NeverCounted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkDepositLease(t, "depfee", "")
	f.mkOneTimeClause(t, "depfee_lockout", "depfee", "", "completed")
	f.mkOneTimeClause(t, "depfee_pet", "depfee", "petFee", "completed")

	v := f.projectLeaseAt(t, "depfee")[0].Values
	require.Equal(t, true, v["missing_deposit"], "a one-time fee with no purpose token, or another purpose, is not the deposit — period alone is no mark")
	require.Equal(t, int64(0), v["depositClauseCount"])
	require.Nil(t, v["depositClauseKey"], "a charged one-time fee is never a return candidate")

	f.mkDepositLease(t, "depfeeended", depositEndedAt)
	f.mkOneTimeClause(t, "depfeeended_lockout", "depfeeended", "", "completed")
	w := f.projectLeaseAt(t, "depfeeended")[0].Values
	require.Nil(t, w["depositClauseKey"], "a charged one-time fee on an ended tenancy is never returned as a deposit")
	require.Equal(t, false, w["missing_depositReturn"])
}

// TestLeaseRentSettlement_DepositAndRentGapsIndependent — a lease with its
// rent clause and no deposit clause opens only missing_deposit; a lease with
// its deposit clause and no rent clause opens only missing_clause. Neither
// clause is counted by the other's gap.
func TestLeaseRentSettlement_DepositAndRentGapsIndependent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkDepositLease(t, "rentonly", "")
	f.mkRentClause(t, "rentonly_rent", "rentonly", origStart, origEnd)

	v := f.projectLeaseAt(t, "rentonly")[0].Values
	require.Equal(t, false, v["missing_clause"], "the rent clause covers the current term")
	require.Equal(t, true, v["missing_deposit"], "the rent clause is not the deposit clause")

	f.mkDepositLease(t, "deponly", "")
	f.mkOneTimeClause(t, "deponly_dep", "deponly", "deposit", "completed")

	w := f.projectLeaseAt(t, "deponly")[0].Values
	require.Equal(t, false, w["missing_deposit"])
	require.Equal(t, true, w["missing_clause"], "a deposit clause is oneTime — it never suppresses the rent clause")
	require.Equal(t, true, w["violating"])
}

// TestLeaseRentSettlement_EndedTenancy_NoClause_NeverMinted — a lease that
// records a deposit but whose tenancy ended before any deposit clause was
// minted is not minted one now: it would be billed and refunded in one
// breath. Neither gap opens.
func TestLeaseRentSettlement_EndedTenancy_NoClause_NeverMinted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkDepositLease(t, "dependednoclause", depositEndedAt)

	v := f.projectLeaseAt(t, "dependednoclause")[0].Values
	require.Equal(t, false, v["missing_deposit"], "the tenancy has ended — a deposit minted now would be charged and refunded for nothing")
	require.Equal(t, false, v["missing_depositReturn"])
	require.Equal(t, 2500.0, v["depositAmount"])
	require.Equal(t, depositEndedAt, v["endedAt"])
}

// TestLeaseRentSettlement_OtherArchetypeDeposit_HoldsMintShut_NeverReturned
// — a purpose=deposit clause that is not a oneTime computational clause
// (mint_clause refuses the shape; this is seeded data) counts as the
// installed deposit, so the mint stays shut and nothing is charged twice —
// and is never a return candidate, whatever its state: a monthly clause's
// completed means its final period billed, not a deposit collected.
func TestLeaseRentSettlement_OtherArchetypeDeposit_HoldsMintShut_NeverReturned(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkDepositLease(t, "depmonthly", depositEndedAt)
	f.mkPurposeClause(t, "depmonthly_dep", "depmonthly", "deposit", "monthly", "computational", "completed")

	v := f.projectLeaseAt(t, "depmonthly")[0].Values
	require.Equal(t, false, v["missing_deposit"], "a clause tagged deposit holds the mint shut whatever its shape")
	require.Equal(t, int64(1), v["depositClauseCount"])
	require.Nil(t, v["depositClauseKey"], "a completed monthly clause is not a charged deposit")
	require.Equal(t, false, v["missing_depositReturn"])

	f.mkDepositLease(t, "depjudgment", depositEndedAt)
	f.mkPurposeClause(t, "depjudgment_dep", "depjudgment", "deposit", "oneTime", "judgment", "completed")

	w := f.projectLeaseAt(t, "depjudgment")[0].Values
	require.Equal(t, false, w["missing_deposit"])
	require.Nil(t, w["depositClauseKey"], "a judgment clause charges nothing — nothing to return")
	require.Equal(t, false, w["missing_depositReturn"])
}

// TestLeaseRentSettlement_TwoChargedDeposits_ReturnedOnePerPass — two
// charged purpose=deposit clauses on one ended lease (an amendment that
// re-minted one): max() picks exactly one; once it is marked returned the
// other is the sole candidate on the next pass — the missing_term idiom, so
// neither is starved behind the first pick.
func TestLeaseRentSettlement_TwoChargedDeposits_ReturnedOnePerPass(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkDepositLease(t, "twodeps", depositEndedAt)
	f.mkOneTimeClause(t, "twodeps_a", "twodeps", "deposit", "completed")
	f.mkOneTimeClause(t, "twodeps_b", "twodeps", "deposit", "completed")
	aKey := "vtx.clause." + f.ids["twodeps_a"]
	bKey := "vtx.clause." + f.ids["twodeps_b"]

	first := f.projectLeaseAt(t, "twodeps")[0].Values
	require.Equal(t, true, first["missing_depositReturn"])
	pick, _ := first["depositClauseKey"].(string)
	require.Contains(t, []string{aKey, bKey}, pick, "max() must pick one of the two charged deposits")

	pickedName, otherKey := "twodeps_a", bKey
	if pick == bKey {
		pickedName, otherKey = "twodeps_b", aKey
	}
	f.aspect(t, pickedName, "status", "clauseStatus", map[string]any{"state": "returned", "returnedAt": "2027-07-03T09:00:00Z"})

	second := f.projectLeaseAt(t, "twodeps")[0].Values
	require.Equal(t, true, second["missing_depositReturn"], "the other charged deposit is still there")
	require.Equal(t, otherKey, second["depositClauseKey"], "the passed-over clause is now the sole candidate")

	f.aspect(t, "twodeps_a", "status", "clauseStatus", map[string]any{"state": "returned"})
	f.aspect(t, "twodeps_b", "status", "clauseStatus", map[string]any{"state": "returned"})
	third := f.projectLeaseAt(t, "twodeps")[0].Values
	require.Equal(t, false, third["missing_depositReturn"], "both returned — the gap closes")
	require.Equal(t, false, third["missing_deposit"])
}
