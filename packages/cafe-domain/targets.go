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
//     OPTIONAL MATCH converges the gap.
//   - missing_settle → directOp(SettleStaleTab) (this package), auto-closing
//     a tab whose own staleAt deadline passed with no staff Settle. Routes
//     only entityKey + its own .status aspect — SettleStaleTab is a
//     dedicated operationType rather than a directOp against Settle itself
//     because Settle's chargedTo-backfill branch needs a LINK read no
//     GapActionSpec.Reads template can express (ddls.go).
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: TabSettlementTarget,
			LensRef:  TabSettlementTarget,
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
				},
			},
		},
		{
			TargetID: StaleTabSettlementTarget,
			LensRef:  StaleTabSettlementTarget,
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
			},
		},
	}
}
