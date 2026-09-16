package cafedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8): cafeTabSettlement's three gaps, mirroring
// semantic-contracts/targets.go's missing_charge → directOp(DebitAccount)
// shape, with a lazy account-open step ahead of it and a counter-payment
// posting after it; and the independent cafeStaleTabSettlement target's
// single gap, the pastDueAppointments idiom (clinic-reminders/pastdue.go)
// applied to café's own tab shape:
//
//   - missing_account → directOp(CreateAccount) (cafe-ledger), opening the
//     resident's café-ledger account on first settled tab. No Target: this
//     op grants operator/scope=any (cafe-ledger permissions.go), the same
//     objects-base precedent (no authContext.target dependency) — every
//     payload field the DDL requires goes directly in Params, never relies
//     on Target injection (Target only ever sets AuthContext.Target for
//     auth-path scoping, it is NEVER merged into the op payload).
//   - missing_charge → directOp(DebitAccount) (cafe-ledger) over the now-real
//     account, posting the tab's total (+ its itemsMemo as the ledger entry's
//     memo, lenses.go) with the tabRef back-link so the lens's settles
//     OPTIONAL MATCH converges the gap. It declares the account's .balance
//     aspect in optionalReads — cafe-ledger keeps that running total in
//     lockstep with every entry posted to an account that carries one, and this
//     dispatch's own update of it is conditioned on the revision it was read at.
//   - missing_payment → directOp(CreditCafeAccount) (cafe-ledger) over the
//     same account, posting the cash the desk took at settle
//     (row.paidAtSettleCents, a staff Settle{paidCents} — ddls.go) as a
//     payment with the tabRef back-link — the credit writes the same settles
//     link the charge does, and the lens counts settling credits to converge
//     the gap. cafe-ledger bounds a tab-tied credit by the tab's own recorded
//     paidAtSettleCents (read off row.tabKey.status, declared here), not by
//     the live balance, and dedups it off the tab's settles entries. The lens
//     opens this gap only once a settling DEBIT exists (lenses.go), so the
//     credit always lands inside the
//     balance that charge opened and cafe-ledger's payment cap never refuses
//     it for cash already handed over. memo and reason are plain string
//     literals (an unprefixed Params value — internal/weaver/strategist.go);
//     amountCents arrives as the row column's own number.
//   - missing_settle → directOp(SettleStaleTab) (this package), auto-closing
//     a tab whose own staleAt deadline passed with no staff Settle. Routes
//     only entityKey + its own .status aspect — SettleStaleTab is a
//     dedicated operationType rather than a directOp against Settle itself
//     because Settle's chargedTo-backfill branch needs a LINK read no
//     GapActionSpec.Reads template can express (ddls.go).
//   - missing_staleat → directOp(BackfillTabStaleAt) (this package), backfilling
//     the SAME staleAt OpenTab would have written for a tab opened before that
//     field shipped — invisible to missing_settle above until this runs
//     (lenses.go). Same Reads shape as missing_settle: entityKey + its own
//     .status aspect.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: TabSettlementTarget,
			Description: "A settled tab that owes money is posted to the resident's house account. If their lease " +
				"has no café account yet, one is opened, then the tab's total is charged to it, and any cash " +
				"the desk took at the counter when it was settled is credited against that charge.",
			LensRef: TabSettlementTarget,
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_account": {
					Action:    "directOp",
					Operation: "CreateAccount",
					// CreateAccount is claimed by 3 installed ledger DDLs
					// (cafeaccount/account/clinicaccount) — pin the vertexType DDL
					// this target actually dispatches to, or the Processor's
					// operationType→class reverse index fails closed (MissingClass).
					Class:  "cafeaccount",
					Params: map[string]string{"leaseAppKey": "row.leaseAppKey"},
					Reads:  []string{"row.leaseAppKey"},
				},
				"missing_charge": {
					Action:    "directOp",
					Operation: "DebitAccount",
					// DebitAccount is claimed by 4 installed ledger DDLs — pin the
					// vertexType DDL this target dispatches to (see missing_account).
					Class:  "cafetransaction",
					Params: map[string]string{"accountKey": "row.accountKey", "amountCents": "row.totalCents", "memo": "row.itemsMemo", "tabRef": "row.tabKey"},
					Reads:  []string{"row.accountKey", "row.tabKey"},
					// OptionalReads: the account's own .balance aspect cafe-ledger's
					// post_entry maintains — resolveReadKey's row.<col>.<aspect>
					// derived-aspect form (strategist.go). It states this dispatch's
					// read set truthfully; what GUARANTEES the key is hydrated (and so
					// that the charge's own .balance update is auto-conditioned on the
					// revision it was read at, Contract #3 §3.2) is cafe-ledger's own
					// derive_reads, which returns it whatever a dispatcher declares.
					// Absence-tolerant (not Reads) because an account minted under
					// cafe-ledger < 0.4.0 carries no .balance and a charge against one
					// posts without writing it — only a payment ever backfills.
					//
					// row.accountKey.arrears is the account's arrears-episode state,
					// declared on the same terms: a charge posted to an account that
					// owed nothing OPENS an arrears episode by writing that aspect, and
					// that write is only auto-conditioned on the revision it was
					// hydrated at because the key is declared. Absence-tolerant because
					// no account carries the aspect until an episode opens on it.
					OptionalReads: []string{"row.accountKey.balance", "row.accountKey.arrears"},
				},
				"missing_payment": {
					Action:    "directOp",
					Operation: "CreditCafeAccount",
					// CreditCafeAccount is unique to cafe-ledger's cafetransaction
					// DDL today, but pinned regardless (see missing_account).
					Class: "cafetransaction",
					// row.paidAtSettleCents is non-null on every violating row: the
					// gap's own conjunct is paidAtSettleCents > 0 (lenses.go), so
					// the dispatch never meets the strategist's null-column refusal.
					Params: map[string]string{"accountKey": "row.accountKey", "amountCents": "row.paidAtSettleCents", "memo": "Paid at the counter", "reason": "payment", "tabRef": "row.tabKey"},
					// row.tabKey.status (the row.<col>.<aspect> derived form) is the
					// tab's own recorded counter payment — what cafe-ledger's
					// require_counter_payment measures amountCents against instead
					// of the live balance. Required, not optional: a settled tab
					// always carries it, and its absence is a correctness error.
					// cafe-ledger's own derive_reads returns the same key whatever a
					// dispatcher declares.
					Reads: []string{"row.accountKey", "row.tabKey", "row.tabKey.status"},
					// The two walks require_counter_payment runs, both nameable up
					// front off the row's own keys: the account's heldFor lease (the
					// tab must be held by it) and the tab's inbound settles entries
					// (a counter payment already posted refuses this one). The
					// per-entry .entry reads the second walk discovers are the
					// class-(e) follow-ups no dispatcher can name.
					Enumerations: []pkgmgr.EnumerationSpec{
						{Hub: "row.accountKey", Relation: "heldFor", Direction: "out"},
						{Hub: "row.tabKey", Relation: "settles", Direction: "in"},
					},
					// OptionalReads on the same terms as missing_charge: .balance is
					// the running total cafe-ledger's post_entry maintains and reads
					// for the payment cap — declared here to state the read set
					// truthfully, guaranteed hydrated by cafe-ledger's own
					// derive_reads, absence-tolerant because a legacy account carries
					// none until its first payment backfills it (this dispatch may be
					// that payment). .arrears is the arrears-episode state a payment
					// to zero closes or a partial payment marks stale; absence-
					// tolerant because no account carries it until an episode opens.
					OptionalReads: []string{"row.accountKey.balance", "row.accountKey.arrears"},
				},
			},
		},
		{
			TargetID: StaleTabSettlementTarget,
			Description: "No tab stays open past its settle-by deadline. A tab whose deadline passes without staff " +
				"settling it is closed automatically, and a tab with no deadline recorded gets one filled " +
				"in.",
			LensRef: StaleTabSettlementTarget,
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_settle": {
					Action:    "directOp",
					Operation: "SettleStaleTab",
					// SettleStaleTab is unique to this package's tab vertexType DDL
					// today, but pinned regardless — the same defensive shape every
					// other directOp in this file uses (see missing_account).
					Class:  "tab",
					Params: map[string]string{"tabKey": "row.entityKey"},
					Reads:  []string{"row.entityKey", "row.entityKey.status"},
				},
				"missing_staleat": {
					Action:    "directOp",
					Operation: "BackfillTabStaleAt",
					Class:     "tab",
					Params:    map[string]string{"tabKey": "row.entityKey"},
					Reads:     []string{"row.entityKey", "row.entityKey.status"},
				},
			},
		},
	}
}
