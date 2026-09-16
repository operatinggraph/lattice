package clinicledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbooks (Contract
// #10 §10.8).
//
// clinicNoShowSettlement carries three independent gaps, mirroring
// cafe-domain/targets.go's cafeTabSettlement shape (missing_account →
// directOp(CreateAccount) then missing_charge → directOp(DebitAccount)) but
// self-contained inside clinic-ledger — it already depends on clinic-domain
// (for patientKey validation) and can read appointment data directly, so no
// separate domain-side package or cross-package dependency is needed
// (clinic-domain-owned clinic-noshow-fee-design.md §"Package boundary").
//
//   - missing_account → directOp(ClinicCreateAccount), opening the patient's
//     clinic-ledger account lazily on the first fee rather than requiring a
//     registered patient's account to pre-exist (the standing front-desk /
//     billing ClinicCreateAccount flow alone would silently starve unopened
//     patients' fees of ever converging).
//   - missing_charge → directOp(ClinicDebitAccount) over the now-real
//     account, with the lens's memo naming what was billed: a no-show fee or
//     a late-cancellation fee (a patient's own cancel inside the 24-hour
//     window carries the same fee on its status).
//   - missing_reversal → directOp(ClinicCreditAccount), reversing a charge
//     whose appointment's CURRENT status no longer carries a fee — a
//     CorrectAppointmentStatus correction to completed / cancelled, the
//     waiver. clinic-domain's CorrectAppointmentStatus itself never touches
//     the ledger; this target is what converges the reversal it leaves open.
//
// clinicArrearsReminders carries three gaps → ONE remediation:
//
//   - missing_evaluation → directOp(EvaluateClinicArrears) over the account.
//     The op recomputes the FIFO-oldest open charge, rewrites .arrears, and —
//     where the recomputed due date has passed and nothing has gone out for
//     it — fires the notification. Whichever of the ways the gap opened
//     (never evaluated, marked stale by a partial payment, a timer fired at a
//     due date nothing was reminded for, or a historyTooLong flag recorded
//     under a smaller replay budget than the current one), the remediation is
//     the same recomputation.
//   - missing_replay_a / missing_replay_b → the same directOp. A history
//     longer than one page of the op's postedTo replay is consumed one page
//     per dispatch, and the checkpoint each page leaves on .arrears.replay
//     alternates its phase; the lens projects one gap per phase so each page
//     closes the gap that dispatched it and opens the other (the Gaps
//     comment below). The op reads the checkpoint, not the column.
//
// directOp, not a Loom pattern: a reminder is a single op, no multi-step
// externalTask flow — the same shape clinic-reminders' visit reminder uses.
// The bridge's notification send hangs off the op's own transactional outbox,
// not off a pattern.
//
// Params{accountKey: row.entityKey} routes the candidate account into the
// op's payload. The patient it is held for is NOT routed through Params: the
// row's own patientKey column is OPTIONAL (an account with no live heldFor
// patient still projects a row, with a null patientKey — lenses.go), and the
// strategist refuses to dispatch any row whose Params reference a null
// column (internal/weaver/strategist.go), which would silently starve the gap
// forever for exactly the accounts most worth aging. The op instead resolves
// the patient itself, live, off the account's own heldFor out-link — the
// same state the row's column merely projects — so evaluation never depends
// on which shape the row happens to be in. patientKey stays a projected
// column here purely for operator observability (the weaver-targets read
// model).
//
// Reads[row.entityKey] routes the account ROOT (the liveness guard's
// hydration). OptionalReads[row.entityKey.arrears] routes the account's own
// arrears state — absence-tolerant because no account carries the aspect
// until something opens an episode on it, and a required read's absence
// would HydrationMiss the very first evaluation of each one. That declaration
// is what auto-conditions the op's own .arrears write on the revision it was
// hydrated at (Contract #3 §3.2); the account DDL's derive_reads returns the
// same key whatever a dispatcher declares, so this states the read set and
// that guarantees it.
//
// Enumerations declares the bounded postedTo replay the op runs to recompute
// the head — the walk itself is nameable up front (the hub is the row's own
// account), the per-transaction .entry reads it discovers are not, which is
// exactly the class-(e) split ClinicCreditAccount's own backfill replay
// declares itself under (opmetas.go). It also declares the heldFor walk
// patient_for_account runs to resolve the notification's patient live —
// likewise nameable up front off the same hub, and read-drift-checked exactly
// like postedTo.
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: NoShowSettlementTarget,
			Description: "Every appointment whose status carries a fee — a no-show, or a patient's own cancellation " +
				"inside the 24-hour late-cancel window — is charged once to the patient's clinic account. " +
				"If the patient has no account yet, one is opened first and the fee is then posted against " +
				"the visit. A charge whose appointment is later corrected to a fee-less status is reversed once.",
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
					// fee' / 'Late-cancellation fee', never a vtx.* key) and belongs in Params only. Declaring it
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
					//
					// row.appointmentKey.status: the fee the op verifies before writing
					// settles (NoFeeToSettle) — the appointment's current status, read
					// through the same derived-aspect form. derive_reads guarantees it
					// whatever a dispatcher declares; this states it.
					//
					// row.accountKey.arrears: a charge posted to an account that owed
					// nothing OPENS an arrears episode by writing that aspect, and a
					// charge against one already owing reads it to decide not to; the
					// write is OCC on the revision this declaration hydrates.
					// Absence-tolerant: no account carries .arrears until an episode
					// opens on it.
					OptionalReads: []string{"row.accountKey.balance", "row.appointmentKey.status", "row.accountKey.arrears"},
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
						"memo":        "Fee reversal (corrected)",
					},
					// Reads the two bare vertex keys ClinicCreditAccount's
					// vertex_alive() checks hydrate (accountKey, chargeTxKey) — reason
					// and memo are literal free text, never vtx.* keys, and belong in
					// Params only. Declaring either here would fail step4 hydrate the
					// same way missing_charge's memo field already did (comment above).
					Reads: []string{"row.accountKey", "row.chargeTxKey"},
					// The postedTo link post_entry reads to prove chargeTxKey is a
					// charge on accountKey (WrongAccount) is composed from BOTH
					// columns, which row.<col>.<aspect> templating cannot express
					// (resolveReadKey, strategist.go) — the op's own derive_reads
					// declares it for this dispatch as it does for every other.
					// OptionalReads: same derived-aspect / absence-tolerant shape as
					// missing_charge above, and the same reason for no postedTo walk —
					// a reversal is a staff-voice credit, so it is neither capped by the
					// balance nor a leg that backfills one. row.accountKey.arrears for
					// the same reason missing_charge declares it: a reversal that clears
					// the balance ENDS the episode by rewriting that aspect.
					OptionalReads: []string{"row.accountKey.balance", "row.accountKey.arrears"},
				},
			},
		},
		{
			TargetID: ArrearsRemindersTarget,
			Description: "A patient who owes money on their clinic account past the net term is reminded once, " +
				"about the charge that has actually been sitting unpaid the longest. Paying it off ends the " +
				"episode; a new charge against a settled account starts a fresh one. Nothing is refused: " +
				"the desk sees the debt at check-in, and care goes ahead.",
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

// arrearsEvaluationGap is the directOp(EvaluateClinicArrears) every gap on the
// clinicArrearsReminders target dispatches — the same params, reads and
// enumerations whichever column opened, because the op reads its own
// checkpoint to decide where in the replay it is.
func arrearsEvaluationGap() pkgmgr.GapActionSpec {
	return pkgmgr.GapActionSpec{
		Action:    "directOp",
		Operation: arrearsOp,
		// EvaluateClinicArrears is unique to this package's clinicaccount
		// vertexType DDL today, but pinned regardless — the same defensive
		// shape the settlement gaps use, and the ledger operationType
		// namespace is global (permissions.go).
		Class:         "clinicaccount",
		Params:        map[string]string{"accountKey": "row.entityKey"},
		Reads:         []string{"row.entityKey"},
		OptionalReads: []string{"row.entityKey.arrears"},
		Enumerations: []pkgmgr.EnumerationSpec{
			{Hub: "row.entityKey", Relation: "postedTo", Direction: "in"},
			{Hub: "row.entityKey", Relation: "heldFor", Direction: "out"},
		},
	}
}
