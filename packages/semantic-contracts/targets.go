package semanticcontracts

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8). Two independent gaps → remediation:
//
//   - missing_charge → directOp(DebitAccount) over the charged account. No
//     Target: DebitAccount grants operator/scope=any (loftspace-ledger
//     permissions.go), the same objects-base / cafe-domain precedent (no
//     authContext.target dependency) — every payload field the script
//     requires goes directly in Params (Target only ever sets
//     AuthContext.Target for auth-path scoping, it is NEVER merged into the
//     op payload). Params route the charged account (row.accountKey), the
//     computed amount (row.amountCents, type-preserved — resolveParam
//     returns the row value verbatim), the authorizing clause (row.clauseKey,
//     the clauseRef param loftspace-ledger's DebitAccount reads), and (Fire
//     V3) row.period — DebitAccount branches on period="monthly" to re-arm
//     the clause's chargeValidUntil instead of completing it — into the op's
//     payload; Reads routes the account + clause keys — and the clause's own
//     .terms aspect (row.clauseKey.terms), so loftspace-ledger's DebitAccount
//     can derive the authoritative amountCents from the clause instead of
//     trusting this row-templated copy — into ContextHint.Reads so the
//     Processor hydrates them. The `directOp`-must-be-literal guard is
//     satisfied — DebitAccount is a literal operation name, only params/reads
//     are row-templated (the objectLiveness → TombstoneObject / appointment
//     Reminders → RecordAppointmentReminder precedent, granted to operator,
//     which Weaver's service actor holds).
//   - missing_inspection → assignTask(InspectPremises) to the assigned
//     inspector (row.inspectorKey), scoped to the clause (row.clauseKey) —
//     the same shape as lease-signing's missing_signature → assignTask
//     SignLease. Opens a stable-id Task; the inspector completes it by
//     submitting InspectPremises, which the clause DDL's own script handles
//     (mirrors SignLease acting on its own leaseapp).
//
// Every row.<col> template is a clauseSatisfaction BodyColumn — the
// §10.2↔§10.8 column seam, cross-checked by
// TestSemanticContracts_PlaybookColumnsMatchLens.
//
// leaseRentSettlement's own playbook (lenses.go) is the bootstrap ahead of
// this one — two independent gaps, mirroring cafe-domain's tabSettlement
// missing_account → directOp(CreateAccount) shape:
//
//   - missing_account → directOp(LoftspaceCreateAccount) (loftspace-ledger).
//     Grants operator unconfined (loftspace-ledger permissions.go), the same
//     no-authContext.target idiom every directOp in this file uses.
//   - missing_clause → directOp(CreateClause) (this package) — accountKey
//     comes from THIS SAME row (the lens only opens missing_clause once
//     missing_account has already converged, lenses.go), amountCents from
//     requestedRentCents (the lens's own ×100 conversion — never the raw
//     row.requestedRent dollar figure), period + prose are literals (no
//     "row." prefix, so resolveParam passes them through verbatim,
//     strategist.go).
//
// Cross-checked by TestSemanticContracts_LeaseRentSettlementColumnsMatchLens.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: ClauseSatisfactionTarget,
			LensRef:  ClauseSatisfactionTarget,
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_charge": {
					Action:    "directOp",
					Operation: "DebitAccount",
					// DebitAccount is claimed by 4 installed ledger DDLs — pin the
					// loftspace-ledger vertexType DDL this target dispatches to, or the
					// Processor's operationType→class reverse index fails closed
					// (MissingClass).
					Class:  "transaction",
					Params: map[string]string{"accountKey": "row.accountKey", "amountCents": "row.amountCents", "clauseRef": "row.clauseKey", "period": "row.period"},
					Reads:  []string{"row.accountKey", "row.clauseKey", "row.clauseKey.terms"},
				},
				"missing_inspection": {
					Action:    "assignTask",
					Operation: "InspectPremises",
					Assignee:  "row.inspectorKey",
					Target:    "row.clauseKey",
				},
			},
		},
		{
			TargetID: LeaseRentSettlementTarget,
			LensRef:  LeaseRentSettlementTarget,
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_account": {
					Action:    "directOp",
					Operation: "LoftspaceCreateAccount",
					// LoftspaceCreateAccount is claimed by loftspace-ledger's own
					// vertexType DDL (the 4-installed-ledger-DDLs reverse-index trap
					// above applies to every directOp in this file).
					Class:  "account",
					Params: map[string]string{"leaseAppKey": "row.leaseAppKey"},
					Reads:  []string{"row.leaseAppKey"},
				},
				"missing_clause": {
					Action:    "directOp",
					Operation: "CreateClause",
					Class:     "clause",
					Params: map[string]string{
						"leaseAppKey": "row.leaseAppKey",
						"accountKey":  "row.accountKey",
						"amountCents": "row.requestedRentCents",
						"period":      "monthly",
						"prose":       "Monthly rent per the signed lease agreement.",
					},
					Reads: []string{"row.leaseAppKey", "row.accountKey"},
				},
			},
		},
	}
}
