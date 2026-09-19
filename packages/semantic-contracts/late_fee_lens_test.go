package semanticcontracts

// Rule-engine proof of leaseRentSettlement's two late-fee gaps over one
// fixture set (the LoftSpace "lateness costs what the lease says" design):
//
//   - no .lateFee on the lease — neither gap, lateFeeCents null.
//   - .lateFee + account, no clause — missing_lateFeeClause opens with the
//     recorded cents templated; the amendment stays shut.
//   - .lateFee, no account — the mint waits on the account, as the deposit's does.
//   - an active purpose=lateFee clause at the recorded amount — both shut
//     (the state the mint leaves, and the state the amendment leaves).
//   - the recorded term changed — missing_lateFeeAmendment opens with the
//     clause key and its own amount templated; the mint stays shut.
//   - the amended clause superseded beside an active replacement at the new
//     amount — both shut: the superseded clause is not counted, the
//     replacement agrees.
//   - only a superseded clause — the mint opens again (no ACTIVE clause).
//   - an ended tenancy with no clause — never minted; an ended tenancy whose
//     clause disagrees is still amended (a fee already minted stays honest).
//   - the gaps are independent of the deposit's and the rent clause's.
//
// And clauseSatisfaction's side of the same design: a perArrearsEpisode
// clause is never billed there, at chargeCount 0 or after a charge.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// mkLateFeeLease seeds an approved, accounted lease inside its original term
// with its rent clause in place (so violating reads the late-fee gaps alone)
// that records a late-fee term of feeCents (0 = none) and, when endedAt is
// non-empty, an ended tenancy.
func (f *bcFixture) mkLateFeeLease(t *testing.T, name, endedAt string, feeCents float64) {
	t.Helper()
	tenancy := map[string]any{"leaseStart": origStart, "leaseEnd": origEnd}
	if endedAt != "" {
		tenancy["endedAt"] = endedAt
	}
	f.mkRentLease(t, name, 2500, tenancy)
	f.mkRentClause(t, name+"_rent", name, origStart, origEnd)
	if feeCents > 0 {
		f.aspect(t, name, "lateFee", "leaseLateFee", map[string]any{"amountCents": feeCents, "recordedAt": "2026-09-18T00:00:00Z"})
	}
}

// mkLateFeeClause seeds a perArrearsEpisode purpose=lateFee clause governing
// the lease at amountCents with the given .status state.
func (f *bcFixture) mkLateFeeClause(t *testing.T, name, leaseName string, amountCents float64, state string) {
	t.Helper()
	f.vtx(t, name, "clause")
	f.aspect(t, name, "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": false, "amountCents": amountCents, "period": "perArrearsEpisode", "purpose": "lateFee"})
	f.aspect(t, name, "status", "clauseStatus", map[string]any{"state": state})
	f.edge(t, "governs", name, leaseName)
}

func TestLeaseRentSettlement_NoLateFee_NeitherLateFeeGap(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkLateFeeLease(t, "nofeelease", "", 0)

	v := f.projectLeaseAt(t, "nofeelease")[0].Values
	require.Equal(t, false, v["missing_lateFeeClause"], "a lease that records no fee term takes none")
	require.Equal(t, false, v["missing_lateFeeAmendment"])
	require.Nil(t, v["lateFeeCents"])
	require.Nil(t, v["lateFeeClauseKey"])
	require.Nil(t, v["lateFeeClauseCents"])
	require.Equal(t, int64(0), v["lateFeeClauseCount"])
}

func TestLeaseRentSettlement_LateFeeRecorded_NoClause_MissingLateFeeClause(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkLateFeeLease(t, "feelease", "", 5000)

	v := f.projectLeaseAt(t, "feelease")[0].Values
	require.Equal(t, true, v["missing_lateFeeClause"])
	require.Equal(t, 5000.0, v["lateFeeCents"], "the dispatch templates the recorded integer cents verbatim — SetLateFee stores cents, so there is no ×100 here")
	require.Equal(t, int64(0), v["lateFeeClauseCount"])
	require.Equal(t, false, v["missing_lateFeeAmendment"], "nothing minted, nothing to amend")
	require.Equal(t, true, v["violating"])
	require.Equal(t, "vtx.account."+f.ids["feelease_acct"], v["accountKey"], "the templated account is this same row's")
	require.Equal(t, false, v["missing_deposit"], "a fee term is not a deposit")
}

func TestLeaseRentSettlement_LateFeeRecorded_NoAccount_WaitsForTheAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkApprovedLeaseWithTerms(t, "feenoacct", 2500)
	f.aspect(t, "feenoacct", "tenancy", "tenancy", map[string]any{"leaseStart": origStart, "leaseEnd": origEnd})
	f.aspect(t, "feenoacct", "lateFee", "leaseLateFee", map[string]any{"amountCents": 5000.0, "recordedAt": "2026-09-18T00:00:00Z"})

	v := f.projectLeaseAt(t, "feenoacct")[0].Values
	require.Equal(t, true, v["missing_account"])
	require.Equal(t, false, v["missing_lateFeeClause"], "the fee clause charges the account, so the mint waits on it exactly as missing_deposit does")
	require.Equal(t, false, v["missing_lateFeeAmendment"])
}

func TestLeaseRentSettlement_LateFeeClauseAgrees_BothShut(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkLateFeeLease(t, "feeagree", "", 5000)
	f.mkLateFeeClause(t, "feeagree_fee", "feeagree", 5000, "active")

	v := f.projectLeaseAt(t, "feeagree")[0].Values
	require.Equal(t, false, v["missing_lateFeeClause"], "the state the mint leaves — one clause per lease")
	require.Equal(t, false, v["missing_lateFeeAmendment"], "the amounts agree")
	require.Equal(t, int64(1), v["lateFeeClauseCount"])
	require.Equal(t, "vtx.clause."+f.ids["feeagree_fee"], v["lateFeeClauseKey"])
	require.Equal(t, 5000.0, v["lateFeeClauseCents"])
	require.Equal(t, false, v["violating"])
}

func TestLeaseRentSettlement_LateFeeTermChanged_MissingLateFeeAmendment(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkLateFeeLease(t, "feechanged", "", 7500)
	f.mkLateFeeClause(t, "feechanged_fee", "feechanged", 5000, "active")

	v := f.projectLeaseAt(t, "feechanged")[0].Values
	require.Equal(t, true, v["missing_lateFeeAmendment"])
	require.Equal(t, "vtx.clause."+f.ids["feechanged_fee"], v["lateFeeClauseKey"], "the dispatch names the clause to supersede")
	require.Equal(t, 5000.0, v["lateFeeClauseCents"], "the clause's own recorded amount, the fact the disagreement is judged on")
	require.Equal(t, 7500.0, v["lateFeeCents"], "the new amount the replacement is minted at")
	require.Equal(t, false, v["missing_lateFeeClause"], "an active clause exists — amended, never doubled")
	require.Equal(t, true, v["violating"])
}

func TestLeaseRentSettlement_LateFeeSuperseded_ReplacementCounts(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkLateFeeLease(t, "feesup", "", 7500)
	// The state SupersedeClause leaves: the amended clause's root tombstoned
	// and its status superseded, the replacement active at the new amount.
	f.mkLateFeeClause(t, "feesup_old", "feesup", 5000, "superseded")
	f.mkLateFeeClause(t, "feesup_new", "feesup", 7500, "active")

	v := f.projectLeaseAt(t, "feesup")[0].Values
	require.Equal(t, false, v["missing_lateFeeAmendment"], "the state the supersede leaves — the replacement agrees")
	require.Equal(t, false, v["missing_lateFeeClause"])
	require.Equal(t, int64(1), v["lateFeeClauseCount"], "the superseded clause is not counted")
	require.Equal(t, "vtx.clause."+f.ids["feesup_new"], v["lateFeeClauseKey"])
	require.Equal(t, 7500.0, v["lateFeeClauseCents"])
	require.Equal(t, false, v["violating"])
}

func TestLeaseRentSettlement_OnlySupersededLateFeeClause_MintsAgain(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkLateFeeLease(t, "feesuponly", "", 5000)
	f.mkLateFeeClause(t, "feesuponly_old", "feesuponly", 5000, "superseded")

	v := f.projectLeaseAt(t, "feesuponly")[0].Values
	require.Equal(t, true, v["missing_lateFeeClause"], "no ACTIVE clause governs the lease — the count is over the active state")
	require.Equal(t, false, v["missing_lateFeeAmendment"], "nothing active to amend")
	require.Equal(t, int64(0), v["lateFeeClauseCount"])
	require.Nil(t, v["lateFeeClauseKey"])
}

func TestLeaseRentSettlement_EndedTenancy_LateFee_NeverMintedStillAmended(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkLateFeeLease(t, "feeended", depositEndedAt, 5000)

	v := f.projectLeaseAt(t, "feeended")[0].Values
	require.Equal(t, false, v["missing_lateFeeClause"], "no NEW term on an ended lease: SetLateFee refuses TenancyEnded, and a clause first minted here would be one the lease never agreed while live (an ended tenancy's existing fee clause still bills its arrears, exactly as the reminder still goes out)")
	require.Equal(t, false, v["missing_lateFeeAmendment"])

	f.mkLateFeeLease(t, "feeendedamend", depositEndedAt, 7500)
	f.mkLateFeeClause(t, "feeendedamend_fee", "feeendedamend", 5000, "active")
	v = f.projectLeaseAt(t, "feeendedamend")[0].Values
	require.Equal(t, true, v["missing_lateFeeAmendment"], "an ended tenancy's recorded term still keeps its clause honest")
	require.Equal(t, false, v["missing_lateFeeClause"])
}

// TestLeaseRentSettlement_TwoActiveLateFeeClauses_AmendmentHeld — an
// operator hand-mint beside the lens's own clause: two ACTIVE fee clauses
// govern the lease and neither agrees with the term. The amendment gap holds
// shut (lateFeeClauseCount = 1 is its conjunct) — a supersede of max()'s
// pick would leave the other clause and re-open the gap forever — and the
// mint holds shut too; lateFeeClauseKey names the greatest key, the one the
// arrears evaluation bills.
func TestLeaseRentSettlement_TwoActiveLateFeeClauses_AmendmentHeld(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkLateFeeLease(t, "feetwo", "", 9000)
	f.mkLateFeeClause(t, "feetwo_a", "feetwo", 5000, "active")
	f.mkLateFeeClause(t, "feetwo_b", "feetwo", 7500, "active")

	v := f.projectLeaseAt(t, "feetwo")[0].Values
	require.Equal(t, int64(2), v["lateFeeClauseCount"])
	require.Equal(t, false, v["missing_lateFeeAmendment"], "two active fee clauses: the amendment is held, never a supersede that leaves the other")
	require.Equal(t, false, v["missing_lateFeeClause"])
	greater := "vtx.clause." + f.ids["feetwo_a"]
	if other := "vtx.clause." + f.ids["feetwo_b"]; other > greater {
		greater = other
	}
	require.Equal(t, greater, v["lateFeeClauseKey"], "max() over the fan — the same clause the arrears evaluation bills")
}

func TestLeaseRentSettlement_LateFeeAndDepositGapsIndependent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.mkLateFeeLease(t, "feeindep", "", 5000)
	f.aspect(t, "feeindep", "deposit", "leaseDeposit", map[string]any{"amount": 2500.0, "recordedAt": "2025-09-01T00:00:00Z"})
	f.mkOneTimeClause(t, "feeindep_dep", "feeindep", "deposit", "active")

	v := f.projectLeaseAt(t, "feeindep")[0].Values
	require.Equal(t, true, v["missing_lateFeeClause"], "the fee clause is missing whatever the deposit and rent clauses read")
	require.Equal(t, false, v["missing_deposit"], "the deposit clause never counts as a fee clause")
	require.Equal(t, false, v["missing_clause"])
	require.Equal(t, int64(1), v["depositClauseCount"])
	require.Equal(t, int64(0), v["lateFeeClauseCount"])
}

// TestClauseSatisfaction_PerArrearsEpisode_NeverBilledHere pins the one-time
// arm to period = 'oneTime': a perArrearsEpisode clause with no charge yet
// reads missing_charge FALSE and violating FALSE — an arm written as
// period <> 'monthly' bills it at mint, before any rent was ever late — and
// stays FALSE after the arrears evaluation has posted a fee authorizedBy it
// (a transaction authorizedBy it), the row never arming a timer either way.
func TestClauseSatisfaction_PerArrearsEpisode_NeverBilledHere(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newBcFixture(t)
	f.vtx(t, "feeclause", "clause")
	f.aspect(t, "feeclause", "terms", "clauseTerms", map[string]any{"kind": "computational", "conditioned": false, "amountCents": 5000.0, "period": "perArrearsEpisode", "purpose": "lateFee"})
	f.aspect(t, "feeclause", "status", "clauseStatus", map[string]any{"state": "active"})
	f.vtx(t, "feeclause_acct", "account")
	f.edge(t, "chargesTo", "feeclause", "feeclause_acct")

	rows := f.projectAt(t, "feeclause")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["missing_charge"], "a perArrearsEpisode clause is never clauseSatisfaction's to bill — the one-time arm is period = 'oneTime', not period <> 'monthly'")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
	require.Equal(t, "perArrearsEpisode", v["period"])

	// After the arrears evaluation posts a fee against it: still nothing here.
	f.vtx(t, "feeclause_tx", "transaction")
	f.edge(t, "authorizedBy", "feeclause_tx", "feeclause")
	v = f.projectAt(t, "feeclause")[0].Values
	require.Equal(t, false, v["missing_charge"])
	require.Equal(t, false, v["violating"])
}
