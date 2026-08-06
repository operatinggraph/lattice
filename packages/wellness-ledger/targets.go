package wellnessledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8): three independent missing_* → directOp gaps, mirroring
// clinic-domain/clinic-ledger's identical shape but self-contained inside
// wellness-ledger — it already depends on wellness-domain (for
// bookingRef/priceBookingRef/refundRef validation) and can read
// booking/session/wellnessrefund data directly, so no separate domain-side
// package or cross-package dependency is needed.
//
//   - NoShowSettlementTarget's missing_charge — a noShow booking's fee.
//   - ClassPriceSettlementTarget's missing_price_charge — a priced session's
//     booking price, unconditional on attendance. Independent of the first:
//     each dispatches WellnessDebitAccount with a DIFFERENT ref param (bookingRef vs.
//     priceBookingRef), writing a DIFFERENT settles/settlesClassPrice link, so
//     the two never converge (or double-charge) each other's gap.
//   - RefundSettlementTarget's missing_refund — reverses a class-price charge
//     already posted before its booking was cancelled. Dispatches
//     WellnessCreditAccount (not WellnessDebitAccount) with refundRef,
//     anchored on wellness-domain's wellnessrefund marker vertex rather than
//     the booking, which CancelBooking has already tombstoned by the time
//     the marker exists.
//
// No missing_account gap on the first two targets: wellness's existing billing (any
// front-desk-driven charge) already assumes a member's wellnessaccount exists
// via the standing CreateAccount flow; a booker with no account yet simply
// doesn't converge until one is opened, same as today's billing (mirrors
// clinic's identical rationale, targets.go's doc comment).
//
// A booking re-marked away from noShow (SetBookingAttendance is re-markable,
// unlike clinic's terminal appointment status) drops noShowFeeCents from its
// carried-forward .status fields automatically (wellness-domain's
// SetBookingAttendance only carries rate/seat/booker/session forward, never
// noShowFeeCents) — so the convergence gap simply stops matching (feeCents
// null) rather than needing an explicit "un-charge" path. A charge already
// posted before the re-mark stands: the settles link makes it permanent
// audit history, exactly as a posted invoice line is never silently reversed
// by a later status correction.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: NoShowSettlementTarget,
			LensRef:  NoShowSettlementTarget,
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_charge": {
					Action:    "directOp",
					Operation: "WellnessDebitAccount",
					// WellnessDebitAccount is claimed by multiple installed ledger DDLs
					// (clinic-ledger, cafe-ledger, loftspace-ledger, this one) —
					// pin the vertexType DDL this target dispatches to
					// (MissingClass otherwise).
					Class:  "wellnesstransaction",
					Params: map[string]string{"accountKey": "row.accountKey", "amountCents": "row.feeCents", "bookingRef": "row.bookingKey", "memo": "row.memo"},
					// memo excluded from Reads deliberately — it's free text
					// ('No-show fee', never a vtx.* key), and declaring it here
					// fails step4 hydrate the same way clinic-ledger's identical
					// gap already hit (see clinic-ledger/targets.go's doc comment).
					Reads: []string{"row.accountKey", "row.bookingKey"},
				},
			},
		},
		{
			TargetID: ClassPriceSettlementTarget,
			LensRef:  ClassPriceSettlementTarget,
			// No missing_account gap here either — same rationale as
			// NoShowSettlementTarget above: a member's billing already assumes
			// a wellnessaccount exists via the standing CreateAccount flow, so
			// a booker with no account yet simply doesn't converge until one is
			// opened.
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_price_charge": {
					Action:    "directOp",
					Operation: "WellnessDebitAccount",
					// Pin the vertexType DDL this target dispatches to
					// (MissingClass otherwise) — same rationale as
					// NoShowSettlementTarget's Class field above.
					Class:  "wellnesstransaction",
					Params: map[string]string{"accountKey": "row.accountKey", "amountCents": "row.priceCents", "priceBookingRef": "row.bookingKey", "memo": "row.sessionName"},
					Reads:  []string{"row.accountKey", "row.bookingKey", "row.sessionName"},
				},
			},
		},
		{
			TargetID: RefundSettlementTarget,
			LensRef:  RefundSettlementTarget,
			// No missing_account gap: a wellnessrefund only ever exists
			// because CancelBooking already resolved a live accountKey
			// off the original charge's postedTo link (wellness-domain/
			// ddls.go) before minting it — unlike the two targets above,
			// there is no "account might not exist yet" case here.
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_refund": {
					Action:    "directOp",
					Operation: "WellnessCreditAccount",
					// Pin the vertexType DDL this target dispatches to
					// (MissingClass otherwise) — same rationale as the
					// two targets above.
					Class:  "wellnesstransaction",
					Params: map[string]string{"accountKey": "row.accountKey", "amountCents": "row.amountCents", "refundRef": "row.refundKey", "memo": "row.memo"},
					// memo excluded from Reads deliberately — same rationale as
					// NoShowSettlementTarget's gap above.
					Reads: []string{"row.accountKey", "row.refundKey"},
				},
			},
		},
	}
}
