package clinicledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract
// #10 §10.8): clinicNoShowSettlement's three independent gaps, mirroring
// cafe-domain/targets.go's cafeTabSettlement shape (missing_account →
// directOp(CreateAccount) then missing_charge → directOp(DebitAccount)) but
// self-contained inside clinic-ledger — it already depends on clinic-domain
// (for patientKey validation) and can read appointment data directly, so no
// separate domain-side package or cross-package dependency is needed
// (clinic-domain-owned clinic-noshow-fee-design.md §"Package boundary").
//
//   - missing_account → directOp(ClinicCreateAccount), opening the patient's
//     clinic-ledger account lazily on first no-show rather than requiring a
//     registered patient's account to pre-exist (previously the only route:
//     clinic's standing front-desk/billing ClinicCreateAccount flow, which
//     silently starved unopened patients' no-show fees of ever converging).
//   - missing_charge → directOp(ClinicDebitAccount) over the now-real account,
//     same as before.
//   - missing_reversal → directOp(ClinicCreditAccount), reversing a charge a
//     CorrectAppointmentStatus correction moved off `noShow` — the gap
//     clinicNoShowSettlement's own doc comment (lenses.go) named as
//     currently-undone (filed as a Clinic backlog row, verticals.md).
//     clinic-domain's CorrectAppointmentStatus itself never touches the
//     ledger; this target is what converges the reversal it leaves open.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: NoShowSettlementTarget,
			Description: "Every no-show appointment carrying a fee is charged once to the patient's clinic account. " +
				"If the patient has no account yet, one is opened first and the fee is then posted against " +
				"the visit.",
			LensRef: NoShowSettlementTarget,
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_account": {
					Action:    "directOp",
					Operation: "ClinicCreateAccount",
					// ClinicCreateAccount is claimed by clinic-ledger's clinicaccount
					// DDL only, but pin Class explicitly to match the missing_charge
					// gap's convention below (MissingClass otherwise if ever shared).
					Class:  "clinicaccount",
					Params: map[string]string{"patientKey": "row.patientKey"},
					Reads:  []string{"row.patientKey"},
				},
				"missing_charge": {
					Action:    "directOp",
					Operation: "ClinicDebitAccount",
					// ClinicDebitAccount's DDL is claimed by this package alone, but pin
					// the vertexType DDL this target dispatches to anyway (MissingClass
					// otherwise if ever shared).
					Class:  "clinictransaction",
					Params: map[string]string{"accountKey": "row.accountKey", "amountCents": "row.feeCents", "appointmentRef": "row.appointmentKey", "memo": "row.memo"},
					// Reads the two bare vertex keys the DDL's vertex_alive() checks
					// hydrate (accountKey, appointmentKey). memo is free text ('No-show
					// fee', never a vtx.* key) and belongs in Params only. Declaring it
					// here made every dispatch fail at step4 hydrate (`KV get
					// core-kv/No-show fee: nats: invalid key`), so the charge never
					// executed and the gap never closed — confirmed live in
					// processor.log against every one of this target's dispatches.
					Reads: []string{"row.accountKey", "row.appointmentKey"},
					// OptionalReads: the account's own .balance aspect post_entry
					// maintains — resolveReadKey's row.<col>.<aspect> derived-aspect
					// form (strategist.go). It states this dispatch's read set
					// truthfully; what GUARANTEES the key is hydrated (and so that the
					// charge's own .balance update is auto-conditioned on the revision
					// it was read at, Contract #3 §3.2) is clinic-ledger's own
					// derive_reads, which returns it whatever a dispatcher declares.
					// Absence-tolerant (not Reads) because an account opened before the
					// .balance DDL revision carries none and a charge against one posts
					// without writing it — only a self-scoped patient payment ever
					// backfills, so this unattended dispatch never replays a history.
					OptionalReads: []string{"row.accountKey.balance"},
				},
				"missing_reversal": {
					Action:    "directOp",
					Operation: "ClinicCreditAccount",
					// ClinicCreditAccount's DDL is claimed by this package alone, but pin
					// the vertexType DDL this target dispatches to anyway (MissingClass
					// otherwise if ever shared) — same rationale as missing_charge above.
					Class: "clinictransaction",
					Params: map[string]string{
						"accountKey":  "row.accountKey",
						"amountCents": "row.chargedAmountCents",
						"reason":      "waiver",
						"reversesRef": "row.chargeTxKey",
						"memo":        "No-show fee reversal (corrected)",
					},
					// Reads the two bare vertex keys ClinicCreditAccount's
					// vertex_alive() checks hydrate (accountKey, chargeTxKey) — reason
					// and memo are literal free text, never vtx.* keys, and belong in
					// Params only. Declaring either here would fail step4 hydrate the
					// same way missing_charge's memo field already did (comment above).
					Reads: []string{"row.accountKey", "row.chargeTxKey"},
					// OptionalReads: same derived-aspect / absence-tolerant shape as
					// missing_charge above, and the same reason for no postedTo walk —
					// a reversal is a staff-voice credit, so it is neither capped by the
					// balance nor a leg that backfills one.
					OptionalReads: []string{"row.accountKey.balance"},
				},
			},
		},
	}
}
