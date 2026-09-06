package cafedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8): cafeTabSettlement's two independent gaps, mirroring
// semantic-contracts/targets.go's missing_charge → directOp(DebitAccount)
// shape, plus a lazy account-open step ahead of it; and the independent
// cafeStaleTabSettlement target's single gap, the pastDueAppointments idiom
// (clinic-reminders/pastdue.go) applied to café's own tab shape:
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
				"has no café account yet, one is opened, then the tab's total is charged to it.",
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
