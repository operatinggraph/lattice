package clinicledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8): a single missing_charge → directOp(DebitAccount) gap, mirroring
// cafe-domain/targets.go's shape but self-contained inside clinic-ledger — it
// already depends on clinic-domain (for patientKey validation) and can read
// appointment data directly, so no separate domain-side package or
// cross-package dependency is needed (clinic-domain-owned
// clinic-noshow-fee-design.md §"Package boundary").
//
// No missing_account gap: clinic's existing billing (copay/insurance
// charges) already assumes a registered patient's clinicaccount exists via
// the standing ClinicCreateAccount flow; a no-show'd patient with no account yet
// simply doesn't converge until one is opened, same as today's billing.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: NoShowSettlementTarget,
			LensRef:  NoShowSettlementTarget,
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_charge": {
					Action:    "directOp",
					Operation: "DebitAccount",
					// DebitAccount is claimed by 4 installed ledger DDLs — pin the
					// vertexType DDL this target dispatches to (MissingClass otherwise).
					Class:  "clinictransaction",
					Params: map[string]string{"accountKey": "row.accountKey", "amountCents": "row.feeCents", "appointmentRef": "row.appointmentKey", "memo": "row.memo"},
					// Reads only the two bare vertex keys the DDL's vertex_alive() checks
					// hydrate (accountKey, appointmentKey) — memo is free text ('No-show
					// fee', never a vtx.* key) and belongs in Params only. Declaring it
					// here made every dispatch fail at step4 hydrate (`KV get
					// core-kv/No-show fee: nats: invalid key`), so DebitAccount never
					// executed and the gap never closed — confirmed live in processor.log
					// against every one of this target's dispatches.
					Reads: []string{"row.accountKey", "row.appointmentKey"},
				},
			},
		},
	}
}
