//go:build leaseshortwindow

// Package leaseconvergence_test — the tenancy-notice proof (design
// docs/reviews/loftspace-tenancy-notice-2026-09-15.md §6): a tenant gives
// notice on a live, signed lease, and the term ends EARLY through the REAL
// Weaver temporal lane — the tenancyEnd target, armed on leaseEnd since boot,
// re-arms on the recorded move-out, the now-overdue @at fires, MarkExpired
// records the lapse, EndTenancy records endedAt = moveOutAt exactly once
// (even while a renewal cycle is open — the notice overrides the hold), the
// unit relists, the rent clause's term is shortened to the move-out and
// completed, and SignRenewal refuses NoticeGiven. Same build tag as its
// sibling tenancy_end_convergence_test.go.
package leaseconvergence_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
	loftspaceledger "github.com/operatinggraph/lattice/packages/loftspace-ledger"
	semanticcontracts "github.com/operatinggraph/lattice/packages/semantic-contracts"
)

// findClauseKeyFor scans Core KV for the clause whose governs link points at
// leaseAppKey, returning its vtx.clause.<id> key ("" if none) — the
// findRenewalKey idiom one relation over.
func (h *harness) findClauseKeyFor(appID string, deadline time.Duration) string {
	h.t.Helper()
	cut := time.Now().Add(deadline)
	for time.Now().Before(cut) {
		keys, err := h.conn.KVListKeys(h.ctx, bootstrap.CoreKVBucket)
		if err == nil {
			for _, k := range keys {
				t1, id1, name, t2, id2, ok := substrate.ParseLinkKey(k)
				if !ok || t1 != "clause" || name != "governs" || t2 != "leaseapp" || id2 != appID {
					continue
				}
				return "vtx.clause." + id1
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return ""
}

// TestLeaseConvergence_NoticeEndsTheTenancyEarly:
//
//  1. A signed, landlord-approved application whose term is LIVE (moveInDate
//     ten days back, 12 months) leases its unit; leaseRentSettlement mints its
//     rent account and a clause termed [termStart, leaseEnd). The tenancyEnd
//     target is live from boot, so its row arms freshUntil = leaseEnd — a
//     pending @at a year out. The renewal cycle is opened by hand and the
//     landlord sets terms, so the cycle is genuinely open.
//  2. GiveNotice (as the operator) records today's UTC date as the move-out —
//     the same-day notice the tenant who already left records. The .notice
//     lands with givenBy = operator.
//  3. SignRenewal on the open cycle is refused NoticeGiven — every earlier
//     guard (terms, profile, guarantor) is satisfied, so the notice is what
//     answers — and the cycle stays open and unsigned; renewalComplete's row
//     reads the cycle as not open (the planner assigns nothing more).
//  4. The tenancyEnd row re-arms on the move-out, an EARLIER instant than the
//     pending leaseEnd @at; midnight UTC has already passed, so the re-armed
//     @at is overdue and fires at once, MarkExpired records the lapse, and
//     EndTenancy records endedAt = moveOutAt — NOT leaseEnd — exactly once,
//     with the renewal still open (the notice overrides the open-cycle
//     hold). The unit relists.
//  5. The rent clause converges through leaseRentSettlement's
//     missing_termShortened → ShortenClauseTerm: .terms.validUntil becomes
//     the move-out and .status.state reads completed (clauseSatisfaction's
//     first charge already recorded a due past the move-out, or DebitAccount
//     caps the final period at the shortened end and completes it).
//  6. Steady state: the tenancyEnd row reads termEnd / moveOutAt as the
//     move-out, nothing open, freshUntil null — held across a settle window.
//
// A move-out in the FUTURE cannot lapse within test time (its @at is a real
// calendar day away), so the end is driven by a same-day notice; the
// earlier-instant re-arm is still exercised, because the target was armed on
// leaseEnd before the notice landed.
func TestLeaseConvergence_NoticeEndsTheTenancyEarly(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	h := newHarness(t,
		withExtraPackages(loftspaceledger.Package, semanticcontracts.Package),
		withExtraLenses(leasesigning.TenancyEndTarget, "renewalComplete", semanticcontracts.LeaseRentSettlementTarget, semanticcontracts.ClauseSatisfactionTarget))

	const requestedRent = 2300.0
	moveInDate := time.Now().UTC().AddDate(0, 0, -10).Truncate(time.Second).Format(time.RFC3339)
	appKey, appID, applicantKey, unitKey := h.seedTenancyApplicationFrom("N", requestedRent, moveInDate)

	// --- leg 1: a live lease, its rent clause, and an open renewal cycle ---
	h.approveAndDrain(appKey, applicantKey, unitKey)
	require.Equal(t, "leased", h.unitListingStatus(unitKey), "the approved application's unit must be marked leased")
	tenancy := h.aspectData(appKey, "tenancy")
	require.NotNil(t, tenancy, ".tenancy must exist after the first approve")
	leaseEnd, _ := tenancy["leaseEnd"].(string)
	require.Greaterf(t, leaseEnd, time.Now().UTC().Format(time.RFC3339), "the term must still be live; leaseEnd=%s", leaseEnd)

	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow(leasesigning.TenancyEndTarget, appID)
		return row != nil && row["freshUntil"] == leaseEnd
	}, 30*time.Second, 200*time.Millisecond, "the tenancyEnd row must arm on leaseEnd before any notice exists")

	clauseKey := h.findClauseKeyFor(appID, 45*time.Second)
	require.NotEmpty(t, clauseKey, "leaseRentSettlement must mint the lease's rent clause")
	require.Eventuallyf(t, func() bool {
		terms := h.aspectData(clauseKey, "terms")
		return terms != nil && terms["validUntil"] == leaseEnd
	}, 30*time.Second, 200*time.Millisecond, "the rent clause must be termed to leaseEnd before the notice")

	profile := h.submitOp("SetApplicantProfile", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKey, "unit": unitKey, "annualIncome": 72000, "employmentStatus": "employed",
	}, &processor.ContextHint{Reads: []string{appKey}})
	require.Equalf(t, processor.ReplyStatusAccepted, profile.Status, "SetApplicantProfile: %+v", profile.Error)

	openReply := h.submitOp("OpenRenewal", "renewal", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseApp": appKey,
	}, &processor.ContextHint{Reads: []string{appKey, appKey + ".tenancy"}})
	require.Equalf(t, processor.ReplyStatusAccepted, openReply.Status, "OpenRenewal: %+v", openReply.Error)
	renewalKey := h.findRenewalKey(appID, 30*time.Second)
	require.NotEmpty(t, renewalKey, "OpenRenewal must mint the cycle's renewal vertex")
	renewalID := renewalKey[len("vtx.renewal."):]
	terms := h.submitOp("SetRenewalTerms", "renewal", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"renewalKey": renewalKey, "rentAmount": 2400, "termMonths": 12,
	}, &processor.ContextHint{Reads: []string{renewalKey}, OptionalReads: []string{renewalKey + ".renewalSignature"}})
	require.Equalf(t, processor.ReplyStatusAccepted, terms.Status, "SetRenewalTerms: %+v", terms.Error)
	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow("renewalComplete", renewalID)
		return row != nil && rowBool(row, "open") && row["termsSetAt"] != nil
	}, 30*time.Second, 200*time.Millisecond, "the control: renewalComplete reads the cycle open, terms set, before the notice")

	// --- leg 2: notice for today (same-day is admitted; midnight UTC has
	// already passed, so the timer it re-arms is overdue the moment it exists) ---
	endTenancyCount := h.startOpCounter("EndTenancy", func(p map[string]any) bool {
		return p["leaseAppKey"] == appKey
	})
	// Every revision of the renewalComplete row from here on, so the row the
	// NOTICE projects is seen even though the end's row overwrites it a few
	// hops later (a poll could miss the window; a watch cannot).
	renewalRows, err := h.convKV.WatchUpdates(h.ctx)
	require.NoError(t, err)
	renewalRowKey := "renewalComplete." + renewalID
	marks := h.startMarkExpiredCounter(leasesigning.TenancyEndTarget)
	today := time.Now().UTC().Format("2006-01-02")
	noticeReply := h.submitOp("GiveNotice", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKey, "moveOutDate": today,
	}, &processor.ContextHint{
		Reads:         []string{appKey, appKey + ".tenancy", appKey + ".signature"},
		OptionalReads: []string{appKey + ".notice"},
	})
	require.Equalf(t, processor.ReplyStatusAccepted, noticeReply.Status, "GiveNotice: %+v", noticeReply.Error)
	notice := h.aspectData(appKey, "notice")
	require.NotNil(t, notice, ".notice must exist after GiveNotice")
	moveOutAt, _ := notice["moveOutAt"].(string)
	require.Equal(t, today+"T00:00:00Z", moveOutAt, "a bare date records midnight UTC")
	require.Equal(t, "operator", notice["givenBy"])
	require.Lessf(t, moveOutAt, leaseEnd, "the move-out must precede leaseEnd; moveOutAt=%s leaseEnd=%s", moveOutAt, leaseEnd)

	// --- leg 3: the open cycle cannot be signed, and the planner stops ---
	signReads := &processor.ContextHint{
		Reads: []string{
			renewalKey,
			"lnk.renewal." + renewalID + ".renews.leaseapp." + appID,
			"lnk.leaseapp." + appID + ".applicationFor.identity." + applicantKey[len("vtx.identity."):],
			appKey + ".tenancy",
		},
		OptionalReads: []string{renewalKey + ".terms", appKey + ".applicationSignals", renewalKey + ".guarantorVerification", appKey + ".notice"},
	}
	signReply := h.submitOp("SignRenewal", "renewal", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"renewalKey": renewalKey, "leaseApp": appKey, "applicant": applicantKey,
	}, signReads)
	require.Equalf(t, processor.ReplyStatusRejected, signReply.Status, "SignRenewal on a lease under notice must be refused; reply=%+v", signReply)
	require.NotNil(t, signReply.Error)
	require.Containsf(t, signReply.Error.Message, "NoticeGiven", "the refusal must be the notice talking: %s", signReply.Error.Message)
	require.Containsf(t, signReply.Error.Message, today+" (UTC)", "the refusal names the move-out's UTC date: %s", signReply.Error.Message)
	renewalRoot, ok := h.vertexRootData(renewalKey)
	require.True(t, ok)
	require.Equal(t, "open", renewalRoot["status"], "the refused signing leaves the cycle's own status open")
	require.Nil(t, h.aspectData(renewalKey, "renewalSignature"), "a refused SignRenewal writes no .renewalSignature")
	// The row that closes the cycle must be the NOTICE's, not the end's: a
	// projected row is one consistent snapshot, so a row reading open=false
	// with tenancyEndedAt still null can only have closed on the notice
	// conjunct. Read off the watch: the first closed row must carry no end.
	closedOnNotice := false
	watchDeadline := time.After(30 * time.Second)
	for !closedOnNotice {
		select {
		case ev, ok := <-renewalRows:
			require.True(t, ok, "the renewalComplete watch closed before the cycle closed")
			if ev.Key != renewalRowKey || ev.IsDeleted {
				continue
			}
			var row map[string]any
			require.NoError(t, json.Unmarshal(ev.Value, &row))
			if rowBool(row, "open") {
				continue
			}
			require.Nilf(t, row["tenancyEndedAt"], "renewalComplete closed only once the term ended — the notice itself must close it; row=%v", row)
			require.Equalf(t, moveOutAt, row["noticeMoveOutAt"], "the closed row names the notice that closed it; row=%v", row)
			require.Falsef(t, rowBool(row, "violating"), "a closed cycle dispatches nothing; row=%v", row)
			closedOnNotice = true
		case <-watchDeadline:
			t.Fatalf("renewalComplete never read the cycle under notice as not open; row=%v", h.weaverTargetRow("renewalComplete", renewalID))
		}
	}

	// --- leg 4: the re-armed @at fires; EndTenancy ends the term on the
	// move-out despite the open cycle; the unit relists ---
	require.Eventuallyf(t, func() bool {
		tn := h.aspectData(appKey, "tenancy")
		return tn != nil && tn["endedAt"] != nil
	}, 45*time.Second, 200*time.Millisecond, "EndTenancy must record .tenancy.endedAt once the re-armed @at fires on the move-out")

	ended := h.aspectData(appKey, "tenancy")
	require.Equal(t, moveOutAt, ended["endedAt"], "endedAt must equal the recorded move-out, not leaseEnd and not the fire instant")
	require.Equal(t, leaseEnd, ended["leaseEnd"], "leaseEnd stays the term's own end")

	require.Eventuallyf(t, func() bool {
		return h.unitListingStatus(unitKey) == "available"
	}, 45*time.Second, 200*time.Millisecond, "the ended tenancy's unit must relist as available")
	require.GreaterOrEqualf(t, marks.seen(), 1, "at least one MarkExpired must have recorded the overdue lapse for tenancyEnd")

	// --- leg 5: the rent clause is shortened to the move-out and completed ---
	require.Eventuallyf(t, func() bool {
		ct := h.aspectData(clauseKey, "terms")
		return ct != nil && ct["validUntil"] == moveOutAt
	}, 45*time.Second, 200*time.Millisecond, "ShortenClauseTerm must cap the rent clause's validUntil at the move-out; terms=%v", h.aspectData(clauseKey, "terms"))
	require.Eventuallyf(t, func() bool {
		st := h.aspectData(clauseKey, "status")
		return st != nil && st["state"] == "completed"
	}, 45*time.Second, 200*time.Millisecond, "the shortened clause must read completed; status=%v", h.aspectData(clauseKey, "status"))

	// --- leg 6: steady state ---
	cut := time.Now().Add(5 * time.Second)
	for time.Now().Before(cut) {
		tRow := h.weaverTargetRow(leasesigning.TenancyEndTarget, appID)
		require.NotNil(t, tRow, "the tenancyEnd row must remain present")
		require.Equalf(t, moveOutAt, tRow["termEnd"], "termEnd is the move-out; tenancyEnd row=%v", tRow)
		require.Equalf(t, moveOutAt, tRow["moveOutAt"], "moveOutAt projects the recorded notice; tenancyEnd row=%v", tRow)
		require.Falsef(t, rowBool(tRow, "missing_tenancyEnded"), "missing_tenancyEnded must stay false; tenancyEnd row=%v", tRow)
		require.Falsef(t, rowBool(tRow, "missing_relist"), "missing_relist must stay false; tenancyEnd row=%v", tRow)
		require.Falsef(t, rowBool(tRow, "violating"), "violating must stay false; tenancyEnd row=%v", tRow)
		require.Nilf(t, tRow["freshUntil"], "freshUntil must be null once ended; tenancyEnd row=%v", tRow)
		require.Equal(t, "available", h.unitListingStatus(unitKey), "the unit must stay available at steady state")
		require.Equal(t, moveOutAt, h.aspectData(clauseKey, "terms")["validUntil"], "the shortened term must hold")
		time.Sleep(150 * time.Millisecond)
	}
	require.Equalf(t, 1, endTenancyCount.seen(),
		"EndTenancy must be accepted exactly once (idempotent no-op thereafter; no re-dispatch storm)")

	// The ended term stays refused to the still-open cycle — now by the
	// recorded end, which SignRenewal reads ahead of the notice.
	signAgain := h.submitOp("SignRenewal", "renewal", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"renewalKey": renewalKey, "leaseApp": appKey, "applicant": applicantKey,
	}, signReads)
	require.Equal(t, processor.ReplyStatusRejected, signAgain.Status)
	require.True(t, strings.Contains(signAgain.Error.Message, "TenancyEnded"), signAgain.Error.Message)
}
