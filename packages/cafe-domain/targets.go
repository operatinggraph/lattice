package cafedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8). Two independent gaps, mirroring semantic-contracts/targets.go's
// missing_charge → directOp(DebitAccount) shape, plus a lazy account-open
// step ahead of it:
//
//   - missing_account → directOp(CreateAccount) (cafe-ledger), opening the
//     resident's café-ledger account on first settled tab. No Target: this
//     op grants operator/scope=any (cafe-ledger permissions.go), the same
//     objects-base precedent (no authContext.target dependency) — every
//     payload field the DDL requires goes directly in Params, never relies
//     on Target injection (Target only ever sets AuthContext.Target for
//     auth-path scoping, it is NEVER merged into the op payload).
//   - missing_charge → directOp(DebitAccount) (cafe-ledger) over the now-real
//     account, posting the tab's total with the tabRef back-link so the
//     lens's settles OPTIONAL MATCH converges the gap.
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
					Params: map[string]string{"accountKey": "row.accountKey", "amountCents": "row.totalCents", "tabRef": "row.tabKey"},
					Reads:  []string{"row.accountKey", "row.tabKey"},
				},
			},
		},
	}
}
