package wellnessledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8): NoShowSettlementTarget's and ClassPriceSettlementTarget's two
// independent missing_account/missing_charge (resp. missing_price_charge)
// gaps each, RefundSettlementTarget's single missing_refund gap — mirroring
// clinic-domain/clinic-ledger's identical shape but self-contained inside
// wellness-ledger — it already depends on wellness-domain (for
// bookingRef/priceBookingRef/refundRef validation) and can read
// booking/session/wellnessrefund data directly, so no separate domain-side
// package or cross-package dependency is needed — and ArrearsRemindersTarget's
// three gaps (missing_evaluation plus the two replay-continuation gaps
// missing_replay_a / missing_replay_b), one directOp shared across all three
// (arrearsEvaluationGap below) — the resumable, page-per-dispatch arrears
// mechanism clinic-ledger ships, applied to this ledger.
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
//   - ArrearsRemindersTarget's missing_evaluation / missing_replay_a /
//     missing_replay_b → the same directOp(EvaluateWellnessArrears) over the
//     account (arrearsEvaluationGap below — the same Params/Reads/
//     OptionalReads/Enumerations whichever column dispatches it). The op
//     recomputes the FIFO-oldest open charge over a resumable, page-per-
//     dispatch replay of the account's postedTo history, rewrites .arrears,
//     and — where the recomputed due date has passed and nothing has gone
//     out for it — fires the notification. missing_evaluation opens on
//     never evaluated, marked stale by a posted entry, a timer fired at a
//     due date nothing was reminded for, or a historyTooLong flag recorded
//     under a smaller replay budget than the current one, and dispatches the
//     FIRST page. A history longer than one page leaves a checkpoint on
//     .arrears.replay whose phase flips on every page; the lens projects one
//     continuation gap per phase (missing_replay_a / missing_replay_b) so
//     each page closes the gap that dispatched it and opens the other — the
//     op does not know which gap dispatched it, it just reads the checkpoint
//     and continues or starts. Params{accountKey: row.entityKey} names only
//     the anchor's own key: the member the reminder addresses is NOT routed
//     through Params — an optional-hop column is null on every row where the
//     hop misses, and the strategist refuses to dispatch any row whose
//     Params reference a null column (internal/weaver/strategist.go), which
//     would silently starve exactly the accounts most worth aging. The op
//     resolves the identity itself, live, off the account's own heldFor
//     out-link. Reads[row.entityKey] routes the account ROOT (the liveness
//     guard's hydration); OptionalReads[row.entityKey.arrears] routes the
//     account's own arrears state — absence-tolerant because no account
//     carries the aspect until an evaluation has run on it, and a required
//     read's absence would HydrationMiss the very first evaluation of each
//     one. That declaration is what auto-conditions the op's own .arrears
//     write on the revision it was hydrated at (Contract #3 §3.2); the
//     account DDL's derive_reads returns the same key whatever a dispatcher
//     declares, so this states the read set and that guarantees it.
//     Enumerations declares the bounded postedTo replay the op runs to
//     recompute the head and the heldFor walk that resolves the identity —
//     both nameable up front off the row's own account; the per-transaction
//     .entry reads and the per-credit settlesRefund → reverses hops the
//     replay discovers are not, which is exactly the class-(e) split
//     (read_drift_baseline.txt carries the two link-discovered walks).
//
// The three settlement dispatches of WellnessDebitAccount / WellnessCreditAccount
// each declare OptionalReads[row.accountKey.arrears] beside their Reads: every
// posted entry marks the account's recorded arrears state stale (post_entry,
// scripts.go), and that write is a bare update auto-conditioned only for a key
// the dispatch hydrated. The transaction DDL's own derive_reads guarantees the
// key whatever a dispatcher declares; the declaration here documents it.
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
					Reads:         []string{"row.accountKey", "row.bookingKey"},
					OptionalReads: []string{"row.accountKey.arrears"},
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
					Reads:         []string{"row.accountKey", "row.bookingKey"},
					OptionalReads: []string{"row.accountKey.arrears"},
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
					Reads:         []string{"row.accountKey", "row.refundKey"},
					OptionalReads: []string{"row.accountKey.arrears"},
				},
			},
		},
		{
			TargetID: ArrearsRemindersTarget,
			Description: "A member who owes money on their wellness account past the net term is reminded once, " +
				"about the charge that has actually been sitting unpaid the longest. Paying it off ends the " +
				"episode; a new charge after that starts a fresh one.",
			LensRef: ArrearsRemindersTarget,
			// Three gaps, one op. missing_evaluation opens on an account that
			// needs evaluating (never evaluated, stale, or a recorded lapse at
			// its due date) and dispatches the FIRST page of the op's postedTo
			// replay. A history longer than one page leaves a checkpoint on
			// .arrears.replay whose phase flips on every page, and the lens
			// projects one gap per phase: the page written under phase a closes
			// missing_replay_a and opens missing_replay_b, whose dispatch
			// writes phase a again, and so on until the finalize page writes no
			// checkpoint and every gap is false. Each gap episode is therefore
			// exactly one dispatch — its mark and dispatch count are cleared by
			// the page it dispatched — so the engine's default retry budget
			// stands and a REJECTED page (a wall breach) is reclaimed up to
			// that budget, then parked loud under GapBudgetExhausted. The op
			// itself does not know which gap dispatched it: it reads the
			// checkpoint and continues, or starts. The three entries are
			// identical in every field; they differ only in which column
			// dispatches them.
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_evaluation": arrearsEvaluationGap(),
				"missing_replay_a":   arrearsEvaluationGap(),
				"missing_replay_b":   arrearsEvaluationGap(),
			},
		},
	}
}

// arrearsEvaluationGap is the directOp(EvaluateWellnessArrears) every gap on
// the wellnessArrearsReminders target dispatches — the same params, reads and
// enumerations whichever column opened, because the op reads its own
// checkpoint to decide where in the replay it is.
func arrearsEvaluationGap() pkgmgr.GapActionSpec {
	return pkgmgr.GapActionSpec{
		Action:    "directOp",
		Operation: arrearsOp,
		// EvaluateWellnessArrears is unique to this package's
		// wellnessaccount vertexType DDL, but pinned regardless — the
		// same defensive shape the three targets above use, and the
		// operationType namespace is global (permissions.go).
		Class:         "wellnessaccount",
		Params:        map[string]string{"accountKey": "row.entityKey"},
		Reads:         []string{"row.entityKey"},
		OptionalReads: []string{"row.entityKey.arrears"},
		Enumerations: []pkgmgr.EnumerationSpec{
			{Hub: "row.entityKey", Relation: "postedTo", Direction: "in"},
			{Hub: "row.entityKey", Relation: "heldFor", Direction: "out"},
		},
	}
}
