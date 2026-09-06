package wellnessledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8): NoShowSettlementTarget's and ClassPriceSettlementTarget's two
// independent missing_account/missing_charge (resp. missing_price_charge)
// gaps each, plus RefundSettlementTarget's single missing_refund gap —
// mirroring clinic-domain/clinic-ledger's identical shape but self-contained
// inside wellness-ledger — it already depends on wellness-domain (for
// bookingRef/priceBookingRef/refundRef validation) and can read
// booking/session/wellnessrefund data directly, so no separate domain-side
// package or cross-package dependency is needed.
//
//   - NoShowSettlementTarget's missing_account → directOp(WellnessCreateAccount),
//     opening the booker's account lazily on first no-show rather than
//     requiring a booking identity's account to pre-exist (previously the
//     only route: wellness's standing front-desk CreateAccount flow, which
//     silently starved unopened bookers' no-show fees of ever converging).
//     missing_charge → directOp(WellnessDebitAccount) over the now-real
//     account, same as before.
//   - ClassPriceSettlementTarget's missing_account → the identical lazy-open
//     relay, independently, for a priced session's booking price
//     (unconditional on attendance). missing_price_charge → directOp
//     (WellnessDebitAccount) once the account exists. Both targets dispatch
//     WellnessDebitAccount with a DIFFERENT ref param (bookingRef vs.
//     priceBookingRef), writing a DIFFERENT settles/settlesClassPrice link, so
//     the two never converge (or double-charge) each other's gap.
//   - RefundSettlementTarget's missing_refund — reverses a class-price charge
//     and/or a no-show fee already posted before its booking was cancelled
//     or released (wellness-domain's CancelBooking and ReleaseOrphanedBooking
//     each mint a wellnessrefund per charge shape they find still posted —
//     a booking can owe both at once). Dispatches WellnessCreditAccount (not
//     WellnessDebitAccount) with refundRef, anchored on wellness-domain's
//     wellnessrefund marker vertex rather than the booking, which is already
//     tombstoned by the time the marker exists. memo templates off row.memo
//     (the marker's OWN detail.memo — "Class price refund" or "No-show fee
//     refund") rather than a literal, since one target now serves both
//     shapes. No missing_account gap here: a wellnessrefund only ever exists
//     because its minting op already resolved a live accountKey off the
//     original charge's postedTo link before minting it — unlike the two
//     targets above, there is no "account might not exist yet" case here.
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
			Description: "Every no-show booking carrying a fee is charged once to the member's wellness account. " +
				"If the member has no account yet, one is opened first and the fee is then posted against " +
				"the booking.",
			LensRef: NoShowSettlementTarget,
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_account": {
					Action:    "directOp",
					Operation: "WellnessCreateAccount",
					// WellnessCreateAccount is claimed by wellness-ledger's
					// wellnessaccount vertexType DDL alone (the sibling
					// wellnessLedgerAccountGuard aspectType DDL also lists it in
					// PermittedCommands, but only as a step-6 write gate —
					// aspectType DDLs are excluded from the operationType->class
					// reverse index, internal/processor/ddl_cache.go's
					// commandIndexEligible, so they never make an op ambiguous).
					// Pin Class explicitly anyway, mirroring clinic-ledger's
					// identical ClinicCreateAccount pin (MissingClass otherwise
					// if ever a second vertexType DDL claims it).
					Class:  "wellnessaccount",
					Params: map[string]string{"identityKey": "row.identityKey"},
					Reads:  []string{"row.identityKey"},
				},
				"missing_charge": {
					Action:    "directOp",
					Operation: "WellnessDebitAccount",
					// WellnessDebitAccount is claimed by wellness-ledger's
					// wellnesstransaction DDL alone — no other installed package
					// declares this operationType (each vertical prefixes its
					// own, e.g. clinic-ledger's ClinicDebitAccount). Pin Class
					// explicitly anyway, mirroring clinic-ledger's identical
					// ClinicDebitAccount pin (MissingClass otherwise if ever
					// shared).
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
			Description: "Every booking on a paid class is charged its class price once to the member's account, " +
				"whether or not the member ends up attending.",
			LensRef: ClassPriceSettlementTarget,
			// missing_price_charge carries its own retry cap (maxretries_price_charge
			// = 3, retry_budget.go), so a stuck class-price charge that exhausts it
			// escalates to the Augur AI-reasoning tier (mirroring lease-signing's
			// renewalComplete Augur block) instead of only raising a standing
			// Health-KV GapBudgetExhausted issue with no remediation path.
			Augur: &pkgmgr.AugurSpec{Escalate: []string{"exhausted"}},
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_account": {
					Action:    "directOp",
					Operation: "WellnessCreateAccount",
					// Pin the vertexType DDL this target dispatches to
					// (MissingClass otherwise) — same rationale as
					// NoShowSettlementTarget's gap above.
					Class:  "wellnessaccount",
					Params: map[string]string{"identityKey": "row.identityKey"},
					Reads:  []string{"row.identityKey"},
				},
				"missing_price_charge": {
					Action:    "directOp",
					Operation: "WellnessDebitAccount",
					// Pin the vertexType DDL this target dispatches to
					// (MissingClass otherwise) — same rationale as
					// NoShowSettlementTarget's Class field above.
					Class:  "wellnesstransaction",
					Params: map[string]string{"accountKey": "row.accountKey", "amountCents": "row.priceCents", "priceBookingRef": "row.bookingKey", "memo": "row.sessionName"},
					// sessionName excluded from Reads deliberately — it's free
					// text (a class name like 'Vinyasa Flow', never a vtx.* key)
					// used only as the memo Param value; declaring it here fails
					// step4 hydrate the same way clinic-ledger's identical memo
					// field already hit (see clinic-ledger/targets.go's doc
					// comment, and this file's own missing_charge gap above).
					Reads: []string{"row.accountKey", "row.bookingKey"},
				},
			},
		},
		{
			TargetID: RefundSettlementTarget,
			Description: "A class price or no-show fee paid for a booking that was later cancelled or released " +
				"(its class called off) is credited back to the member's account exactly once.",
			LensRef: RefundSettlementTarget,
			// No missing_account gap: a wellnessrefund only ever exists
			// because its minting op already resolved a live accountKey
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
					Class: "wellnesstransaction",
					// reason is a plain string, not a "row."-prefixed template —
					// strategist.go's resolveParam only templates a "row."
					// prefix or decodes a "json:" typed literal, so a bare
					// literal like "refund" is the correct spelling here.
					Params: map[string]string{"accountKey": "row.accountKey", "amountCents": "row.amountCents", "refundRef": "row.refundKey", "memo": "row.memo", "reason": "refund"},
					// memo excluded from Reads deliberately — same rationale as
					// NoShowSettlementTarget's gap above.
					Reads: []string{"row.accountKey", "row.refundKey"},
				},
			},
		},
	}
}
