//go:build leaseshortwindow

// Package leaseconvergence_test — the security-deposit proof (design
// docs/reviews/loftspace-security-deposit-2026-09-15.md §4/§7): a listing
// carrying a depositAmount, applied to and approved, records .deposit on
// the lease at that first approve; leaseRentSettlement mints a purpose=
// deposit one-time clause alongside the rent clause, and the oneTime
// archetype bills it at once through clauseSatisfaction → DebitAccount —
// exactly one debit, authorizedBy the deposit clause, which reads
// completed. Once the tenancy ends (a same-day GiveNotice, the tenancy-
// notice vector's shape), missing_depositReturn dispatches ReturnDeposit —
// exactly one credit, the clause moves to returned, and the gap closes. A
// listing with no deposit at all mints no deposit clause.
package leaseconvergence_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
	loftspacedomain "github.com/operatinggraph/lattice/packages/loftspace-domain"
	loftspaceledger "github.com/operatinggraph/lattice/packages/loftspace-ledger"
	semanticcontracts "github.com/operatinggraph/lattice/packages/semantic-contracts"
)

// seedTenancyApplicationWithDeposit is seedTenancyApplicationFrom
// (tenancy_end_convergence_test.go) parametrized with the listing's own
// depositAmount (SetListing's optional field, packages/loftspace-domain) —
// so a deposit-minting vector actually seeds a unit that carries one.
// depositAmount <= 0 omits the field entirely from the SetListing payload
// (SetListing's own "no deposit" state), the shape the no-deposit sibling
// vector below also proves.
func (h *harness) seedTenancyApplicationWithDeposit(label string, requestedRent, depositAmount float64, moveInDate string) (appKey, appID, applicantKey, unitKey string) {
	h.t.Helper()
	claimSum := sha256.Sum256([]byte("deposit-applicant-claim-" + label + "-" + mustNanoID(h.t)))
	idReply := h.submitOp("CreateUnclaimedIdentity", "identity", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"name":         "Deposit Tenant " + label,
		"email":        "deposit-" + label + "@loftspace.example",
		"claimKeyHash": hex.EncodeToString(claimSum[:]),
	}, nil)
	require.Equalf(h.t, processor.ReplyStatusAccepted, idReply.Status, "CreateUnclaimedIdentity(%s): %+v", label, idReply.Error)
	applicantKey = idReply.PrimaryKey

	unitReply := h.submitOp("CreateLocation", "unit", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"locationType": "unit",
	}, nil)
	require.Equalf(h.t, processor.ReplyStatusAccepted, unitReply.Status, "CreateLocation(%s): %+v", label, unitReply.Error)
	unitKey = unitReply.PrimaryKey

	addrReply := h.submitOp("SetUnitAddress", "loftspaceListing", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"unit": unitKey, "line1": "1 Deposit Way", "city": "Springfield", "region": "OR", "postal": "97477",
	}, &processor.ContextHint{Reads: []string{unitKey}})
	require.Equalf(h.t, processor.ReplyStatusAccepted, addrReply.Status, "SetUnitAddress(%s): %+v", label, addrReply.Error)

	// The listing's OWN rentAmount is requestedRent-200 — different from the
	// .terms requestedRent below — the seedTenancyApplicationFrom precedent
	// that proves the tenancy derivation actually reads .terms first, kept
	// here for the same reason even though this vector does not assert on it.
	listingPayload := map[string]any{
		"unit": unitKey, "rentAmount": requestedRent - 200, "rentCurrency": "USD", "bedrooms": 1,
		"availableFrom": "2020-01-01T00:00:00Z", "leaseTermMonths": 12, "status": "available",
	}
	if depositAmount > 0 {
		listingPayload["depositAmount"] = depositAmount
	}
	listingReply := h.submitOp("SetListing", "loftspaceListing", "default", bootstrap.BootstrapIdentityKey, listingPayload,
		&processor.ContextHint{Reads: []string{unitKey}})
	require.Equalf(h.t, processor.ReplyStatusAccepted, listingReply.Status, "SetListing(%s): %+v", label, listingReply.Error)

	landlordClaim := sha256.Sum256([]byte("deposit-landlord-claim-" + label + "-" + mustNanoID(h.t)))
	landlordReply := h.submitOp("CreateUnclaimedIdentity", "identity", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"name":         "Deposit Landlord " + label,
		"email":        "deposit-landlord-" + label + "@loftspace.example",
		"claimKeyHash": hex.EncodeToString(landlordClaim[:]),
	}, nil)
	require.Equalf(h.t, processor.ReplyStatusAccepted, landlordReply.Status, "CreateUnclaimedIdentity(landlord %s): %+v", label, landlordReply.Error)
	landlordKey := landlordReply.PrimaryKey
	ownerReply := h.submitOp("AssignUnitOwner", "loftspaceOwnership", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"landlord": landlordKey, "unit": unitKey,
	}, &processor.ContextHint{Reads: []string{landlordKey, unitKey},
		Enumerations: testutil.DeclaredEnumerations("AssignUnitOwner", bootstrap.BootstrapIdentityKey, loftspacedomain.OpMetas())})
	require.Equalf(h.t, processor.ReplyStatusAccepted, ownerReply.Status, "AssignUnitOwner(%s): %+v", label, ownerReply.Error)

	appReply := h.submitOp("CreateLeaseApplication", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"applicant": applicantKey, "unit": unitKey,
		"moveInDate": moveInDate, "leaseTermMonths": 12, "requestedRent": requestedRent,
	}, &processor.ContextHint{
		Reads: []string{applicantKey, unitKey},
		OptionalReads: []string{
			"lnk.identity." + applicantKey[len("vtx.identity."):] + ".appliedToUnit.unit." + unitKey[len("vtx.unit."):],
		},
	})
	require.Equalf(h.t, processor.ReplyStatusAccepted, appReply.Status, "CreateLeaseApplication(%s): %+v", label, appReply.Error)
	appKey = appReply.PrimaryKey
	appID = appKey[len("vtx.leaseapp."):]
	return appKey, appID, applicantKey, unitKey
}

// ledgerHistoryRow is the loftspace-ledger `ledgerHistory` lens row, decoded
// straight off its NATS-KV read-model bucket — the same shape
// cmd/loftspace-app's ledgerEntryProjection reads, narrowed to the fields
// this vector asserts on.
type ledgerHistoryRow struct {
	TransactionKey string  `json:"transactionKey"`
	Type           string  `json:"type"`
	AmountCents    float64 `json:"amountCents"`
	ClauseKey      string  `json:"clauseKey"`
	ClausePurpose  string  `json:"clausePurpose"`
}

// ledgerEntriesForClause scans the ledgerHistory bucket for every
// transaction authorizedBy clauseKey, narrowed to entryType ("debit" or
// "credit") — the exactly-once-posted witness for a deposit charge/return,
// mirroring the harness's own weaver-targets row-scan idiom one bucket over.
func (h *harness) ledgerEntriesForClause(clauseKey, entryType string) []ledgerHistoryRow {
	h.t.Helper()
	keys, err := h.conn.KVListKeys(h.ctx, loftspaceledger.LedgerHistoryBucket)
	if err != nil {
		return nil
	}
	var out []ledgerHistoryRow
	for _, k := range keys {
		entry, err := h.conn.KVGet(h.ctx, loftspaceledger.LedgerHistoryBucket, k)
		if err != nil || len(entry.Value) == 0 {
			continue
		}
		var row ledgerHistoryRow
		if json.Unmarshal(entry.Value, &row) != nil {
			continue
		}
		if row.ClauseKey == clauseKey && row.Type == entryType {
			out = append(out, row)
		}
	}
	return out
}

// TestLeaseConvergence_DepositChargedAndReturned:
//
//  1. A listing carrying depositAmount=1500, applied to and approved
//     through the full applicant journey (approveAndDrain — PII, sign,
//     bgcheck/payment through the live bridge, landlord approve). The
//     first approve records .deposit = {amount: 1500, recordedAt}.
//  2. leaseRentSettlement's missing_deposit gap mints a purpose=deposit
//     one-time clause (amountCents = 150000) alongside the rent clause;
//     the oneTime archetype bills it at once through clauseSatisfaction →
//     DebitAccount — exactly one debit, authorizedBy the deposit clause,
//     which reads .status.state = completed.
//  3. A same-day GiveNotice (the tenancy-notice vector's same-day shape —
//     admitted, and its re-armed @at is overdue the instant it exists)
//     ends the tenancy; EndTenancy records .tenancy.endedAt.
//  4. missing_depositReturn dispatches ReturnDeposit — exactly one credit,
//     authorizedBy the SAME deposit clause, which moves to
//     .status.state = returned; missing_depositReturn itself reads false
//     once closed, and no second return credit ever posts.
func TestLeaseConvergence_DepositChargedAndReturned(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	h := newHarness(t,
		withExtraPackages(loftspaceledger.Package, semanticcontracts.Package),
		// ledgerHistory (loftspace-ledger, a flat lens) must be explicitly
		// activated here — the harness only auto-activates the lenses its
		// own `want` map names, and every other package lens (including
		// this one) is never wired unless listed, so its NATS-KV bucket
		// would otherwise stay entirely unpopulated for this run.
		withExtraLenses(leasesigning.TenancyEndTarget, semanticcontracts.LeaseRentSettlementTarget, semanticcontracts.ClauseSatisfactionTarget, "ledgerHistory"))

	const requestedRent = 2300.0
	const depositAmount = 1500.0
	const depositCents = 150000.0
	moveInDate := time.Now().UTC().AddDate(0, 0, -10).Truncate(time.Second).Format(time.RFC3339)
	appKey, appID, applicantKey, unitKey := h.seedTenancyApplicationWithDeposit("D", requestedRent, depositAmount, moveInDate)

	// --- leg 1: approval records .deposit from the listing ---
	h.approveAndDrain(appKey, applicantKey, unitKey)
	deposit := h.aspectData(appKey, "deposit")
	require.NotNil(t, deposit, ".deposit must be recorded after the first approve")
	require.Equal(t, depositAmount, deposit["amount"], "the recorded deposit must equal the listing's own depositAmount")
	require.NotEmpty(t, deposit["recordedAt"], "recordedAt must be stamped")

	// --- leg 2: leaseRentSettlement mints AND clauseSatisfaction charges the
	// deposit clause. depositClauseKey (the lens's own max(CASE … state =
	// 'completed')) is non-null only once the charge has actually posted, so
	// polling it proves both the mint and the charge in one wait. ---
	var depositClauseKey string
	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow(semanticcontracts.LeaseRentSettlementTarget, appID)
		if row == nil {
			return false
		}
		if k, _ := row["depositClauseKey"].(string); k != "" {
			depositClauseKey = k
		}
		return depositClauseKey != ""
	}, 45*time.Second, 200*time.Millisecond, "leaseRentSettlement must mint and clauseSatisfaction must charge the deposit clause")

	terms := h.aspectData(depositClauseKey, "terms")
	require.NotNil(t, terms, ".terms must exist on the minted deposit clause")
	require.Equal(t, "deposit", terms["purpose"], "the minted clause must carry purpose=deposit")
	require.Equal(t, "oneTime", terms["period"])
	require.Equal(t, depositCents, terms["amountCents"])

	status := h.aspectData(depositClauseKey, "status")
	require.NotNil(t, status)
	require.Equal(t, "completed", status["state"], "the charged deposit clause must read completed")

	// The ledgerHistory lens is a SEPARATE CDC pipeline from the
	// weaver-targets row polled above, so it can lag a beat behind the
	// clause's own .status write — poll it too rather than reading once.
	var debits []ledgerHistoryRow
	require.Eventuallyf(t, func() bool {
		debits = h.ledgerEntriesForClause(depositClauseKey, "debit")
		return len(debits) == 1
	}, 30*time.Second, 200*time.Millisecond, "exactly one deposit debit must post; last seen %+v", debits)
	require.Equal(t, depositCents, debits[0].AmountCents)
	require.Equal(t, "deposit", debits[0].ClausePurpose)

	// --- leg 3: a same-day notice ends the tenancy quickly (the tenancy-
	// notice vector's shape: midnight UTC has already passed, so the
	// re-armed @at is overdue the instant GiveNotice records it) ---
	today := time.Now().UTC().Format("2006-01-02")
	noticeReply := h.submitOp("GiveNotice", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKey, "moveOutDate": today,
	}, &processor.ContextHint{
		Reads:         []string{appKey, appKey + ".tenancy", appKey + ".signature"},
		OptionalReads: []string{appKey + ".notice"},
	})
	require.Equalf(t, processor.ReplyStatusAccepted, noticeReply.Status, "GiveNotice: %+v", noticeReply.Error)

	require.Eventuallyf(t, func() bool {
		tn := h.aspectData(appKey, "tenancy")
		return tn != nil && tn["endedAt"] != nil
	}, 45*time.Second, 200*time.Millisecond, "EndTenancy must record .tenancy.endedAt once the re-armed @at fires")

	// --- leg 4: missing_depositReturn dispatches ReturnDeposit — one
	// credit, the clause moves to returned, the gap closes ---
	require.Eventuallyf(t, func() bool {
		st := h.aspectData(depositClauseKey, "status")
		return st != nil && st["state"] == "returned"
	}, 45*time.Second, 200*time.Millisecond, "ReturnDeposit must mark the deposit clause returned; status=%v", h.aspectData(depositClauseKey, "status"))

	var credits []ledgerHistoryRow
	require.Eventuallyf(t, func() bool {
		credits = h.ledgerEntriesForClause(depositClauseKey, "credit")
		return len(credits) == 1
	}, 30*time.Second, 200*time.Millisecond, "exactly one deposit credit must post; last seen %+v", credits)
	require.Equal(t, depositCents, credits[0].AmountCents)
	require.Equal(t, "deposit", credits[0].ClausePurpose)

	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow(semanticcontracts.LeaseRentSettlementTarget, appID)
		return row != nil && !rowBool(row, "missing_depositReturn")
	}, 30*time.Second, 200*time.Millisecond, "missing_depositReturn must read false once the deposit is returned")

	// --- steady state: no re-dispatch, no second credit ---
	cut := time.Now().Add(5 * time.Second)
	for time.Now().Before(cut) {
		st := h.aspectData(depositClauseKey, "status")
		require.NotNil(t, st)
		require.Equal(t, "returned", st["state"], "the returned clause must hold")
		row := h.weaverTargetRow(semanticcontracts.LeaseRentSettlementTarget, appID)
		require.NotNil(t, row)
		require.Falsef(t, rowBool(row, "missing_depositReturn"), "missing_depositReturn must stay false; row=%v", row)
		time.Sleep(150 * time.Millisecond)
	}
	require.Lenf(t, h.ledgerEntriesForClause(depositClauseKey, "credit"), 1, "no second return credit must ever post")
}

// TestLeaseConvergence_NoDepositOnTheListingMintsNoDepositClause is the
// negative sibling: a listing carrying NO depositAmount at all records no
// .deposit aspect at approval, and leaseRentSettlement's missing_deposit
// gap never opens (depositAmount is null, so its own conjunct
// (depositAmount <> null) is false) — no deposit clause is ever minted,
// while the rent clause converges normally through the same run.
func TestLeaseConvergence_NoDepositOnTheListingMintsNoDepositClause(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	h := newHarness(t,
		withExtraPackages(loftspaceledger.Package, semanticcontracts.Package),
		withExtraLenses(leasesigning.TenancyEndTarget, semanticcontracts.LeaseRentSettlementTarget, semanticcontracts.ClauseSatisfactionTarget))

	const requestedRent = 2100.0
	moveInDate := time.Now().UTC().AddDate(0, 0, -10).Truncate(time.Second).Format(time.RFC3339)
	appKey, appID, applicantKey, unitKey := h.seedTenancyApplicationWithDeposit("N", requestedRent, 0, moveInDate)

	h.approveAndDrain(appKey, applicantKey, unitKey)
	require.Nil(t, h.aspectData(appKey, "deposit"), ".deposit must not be recorded when the listing carries no depositAmount")

	// The rent clause still mints and bills normally (missing_clause closes) —
	// proves this run genuinely converges rather than stalling before the
	// deposit gap is even reachable.
	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow(semanticcontracts.LeaseRentSettlementTarget, appID)
		return row != nil && !rowBool(row, "missing_clause") && !rowBool(row, "missing_account")
	}, 45*time.Second, 200*time.Millisecond, "the rent clause must still mint and bill with no deposit on the listing")

	// missing_deposit never opens: its own conjunct requires depositAmount <>
	// null, which this lease never records.
	cut := time.Now().Add(5 * time.Second)
	for time.Now().Before(cut) {
		row := h.weaverTargetRow(semanticcontracts.LeaseRentSettlementTarget, appID)
		require.NotNil(t, row)
		require.Falsef(t, rowBool(row, "missing_deposit"), "missing_deposit must never open with no depositAmount recorded; row=%v", row)
		time.Sleep(150 * time.Millisecond)
	}

	// No clause governing this lease carries purpose=deposit at all — the
	// direct proof that missing_deposit's steady false above did not just
	// arise from a slow mint.
	for _, clauseKey := range h.clauseKeysGoverning(appID) {
		terms := h.aspectData(clauseKey, "terms")
		if terms == nil {
			continue
		}
		require.NotEqualf(t, "deposit", terms["purpose"], "no clause governing this lease may carry purpose=deposit; clause=%s terms=%v", clauseKey, terms)
	}
}

// clauseKeysGoverning scans Core KV for every clause whose governs link
// points at appID — findClauseKeyFor (tenancy_notice_convergence_test.go)
// generalized to every match instead of the first.
func (h *harness) clauseKeysGoverning(appID string) []string {
	h.t.Helper()
	keys, err := h.conn.KVListKeys(h.ctx, bootstrap.CoreKVBucket)
	if err != nil {
		return nil
	}
	var out []string
	for _, k := range keys {
		t1, id1, name, t2, id2, ok := substrate.ParseLinkKey(k)
		if !ok || t1 != "clause" || name != "governs" || t2 != "leaseapp" || id2 != appID {
			continue
		}
		out = append(out, "vtx.clause."+id1)
	}
	return out
}
