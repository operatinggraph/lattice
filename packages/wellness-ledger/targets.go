package wellnessledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8): two independent missing_* → directOp(DebitAccount) gaps over
// the same booking, mirroring clinic-domain/clinic-ledger's identical shape
// but self-contained inside wellness-ledger — it already depends on
// wellness-domain (for bookingRef/priceBookingRef validation) and can read
// booking/session data directly, so no separate domain-side package or
// cross-package dependency is needed.
//
//   - NoShowSettlementTarget's missing_charge — a noShow booking's fee.
//   - ClassPriceSettlementTarget's missing_price_charge — a priced session's
//     booking price, unconditional on attendance. Independent of the first:
//     each dispatches DebitAccount with a DIFFERENT ref param (bookingRef vs.
//     priceBookingRef), writing a DIFFERENT settles/settlesClassPrice link, so
//     the two never converge (or double-charge) each other's gap.
//
// No missing_account gap on either target: wellness's existing billing (any
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
					Operation: "DebitAccount",
					// DebitAccount is claimed by multiple installed ledger DDLs
					// (clinic-ledger, cafe-ledger, loftspace-ledger, this one) —
					// pin the vertexType DDL this target dispatches to
					// (MissingClass otherwise).
					Class:  "wellnesstransaction",
					Params: map[string]string{"accountKey": "row.accountKey", "amountCents": "row.feeCents", "bookingRef": "row.bookingKey"},
					Reads:  []string{"row.accountKey", "row.bookingKey"},
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
					Operation: "DebitAccount",
					// Pin the vertexType DDL this target dispatches to
					// (MissingClass otherwise) — same rationale as
					// NoShowSettlementTarget's Class field above.
					Class:  "wellnesstransaction",
					Params: map[string]string{"accountKey": "row.accountKey", "amountCents": "row.priceCents", "priceBookingRef": "row.bookingKey", "memo": "row.sessionName"},
					Reads:  []string{"row.accountKey", "row.bookingKey", "row.sessionName"},
				},
			},
		},
	}
}
