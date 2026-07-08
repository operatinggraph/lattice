package bespokecontracts

import "github.com/asolgan/lattice/internal/pkgmgr"

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
//     payload; Reads routes the account + clause keys into ContextHint.Reads
//     so the Processor hydrates them. The `directOp`-must-be-literal guard is
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
// TestBespokeContracts_PlaybookColumnsMatchLens.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: ClauseSatisfactionTarget,
			LensRef:  ClauseSatisfactionTarget,
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_charge": {
					Action:    "directOp",
					Operation: "DebitAccount",
					Params:    map[string]string{"accountKey": "row.accountKey", "amountCents": "row.amountCents", "clauseRef": "row.clauseKey", "period": "row.period"},
					Reads:     []string{"row.accountKey", "row.clauseKey"},
				},
				"missing_inspection": {
					Action:    "assignTask",
					Operation: "InspectPremises",
					Assignee:  "row.inspectorKey",
					Target:    "row.clauseKey",
				},
			},
		},
	}
}
