package clinicledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `clinicaccount`
// (ClinicCreateAccount, EvaluateClinicArrears), `clinictransaction`
// (ClinicDebitAccount, ClinicCreditAccount), the `clinicLedgerAccountGuard`
// aspect-type declaration (the patient-anchored uniqueness guard
// ClinicCreateAccount writes), the `clinicAccountBalance` aspect-type
// declaration (the account-anchored running-balance cache ClinicCreateAccount
// mints and ClinicDebitAccount/ClinicCreditAccount keep updated), the
// `clinicAccountArrears` aspect-type declaration (the account's
// arrears-episode state), and the notification-outcome DDL pair
// (notifications.go) the bridge replies onto. Vertical-prefixed: a DDL
// canonicalName is global across every installed package
// (internal/pkgmgr/installer.go checkCanonicalNameCollision), and
// loftspace-ledger already owns the bare `account` / `transaction` names.
func DDLs() []pkgmgr.DDLSpec {
	return append([]pkgmgr.DDLSpec{
		accountDDL(),
		accountGuardAspectTypeDDL(),
		accountBalanceAspectTypeDDL(),
		accountArrearsAspectTypeDDL(),
		transactionDDL(),
	}, notificationDDLs()...)
}

func accountDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "clinicaccount",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"ClinicCreateAccount", arrearsOp},
		Description: "Ledger account DDL. Vertex shape: vtx.clinicaccount.<NanoID>, class=clinicaccount, root data = {} " +
			"(minimal, D5). ClinicCreateAccount{patientKey} mints the account under its OWN independently-generated NanoID " +
			"(never reused from the patient — Core KV NanoIDs are unique platform-wide identifiers, not scoped per vertex " +
			"type), and mints a .balance aspect ({balanceCents: 0}) alongside it — the running total transactionDDL's " +
			"ClinicDebitAccount/ClinicCreditAccount keep in lockstep with every posted entry (an auto-conditioned update, " +
			"retry-eligible on a concurrent-writer conflict), " +
			"an O(1) cache the ledgerHistory lens's own independent full-history sum remains the display source of truth for. " +
			"\"One account per patient\" is enforced by a deterministic create-only guard aspect on the PATIENT " +
			"(patientKey+\".ledgerAccount\", clinicLedgerAccountGuard DDL) instead: a second ClinicCreateAccount for the same " +
			"patient conflicts on that already-existing aspect key. Writes the heldFor link (account→patient, the account is " +
			"the later-arriving vertex so it is the source — Contract #1 §1.1). Requires the patientKey be a live patient " +
			"(no orphan accounts). " +
			"EvaluateClinicArrears{accountKey} is the second operation on this DDL, dispatched by " +
			"Weaver's clinicArrearsReminders playbook rather than by a person: it replays the account's own postedTo " +
			"history under a bounded budget, ages it with the same FIFO the patient's statement runs (credits offset " +
			"the oldest still-open charge first; a credit that names the charge it reverses retires that charge; an " +
			"unapplied credit carries forward as surplus), and records the resulting due date — the oldest open " +
			"charge's postedAt plus the package's net term — on the account's .arrears aspect (clinicAccountArrears " +
			"DDL). Once that date has passed the evaluation records remindedFor = that date, and where NO reminder has " +
			"yet gone out in this arrears episode (sentAt ABSENT) the same commit also stamps sentAt and fires an " +
			"external.notification to the bridge's \"notification\" adapter keyed on (accountKey, dueAt). The send " +
			"condition is sentAt's absence, not remindedFor's value: the unit is the EPISODE — from the charge that " +
			"took the account from square to owing until the balance returns to zero — and a partial payment moves " +
			"the head from one overdue charge to the next without starting a new episode, so exactly ONE notification " +
			"goes out per episode however often the evaluation is re-dispatched, redelivered, or re-run over a moved " +
			"head. A history longer than one page of the replay is consumed one page per dispatch: each page records its " +
			"running aggregate and cursor on .arrears.replay and Weaver dispatches the next through the lens's phase gaps, " +
			"so the head is computed exactly whatever the history's length. An account whose history outruns the replay " +
			"budget (page size × page cap) is not refused: the evaluation DEGRADES, " +
			"recording historyTooLong with the budget it exhausted (carrying dueAt/remindedFor/sentAt as they stood, clearing stale) and sending " +
			"nothing, which holds the row quiet and visible rather than re-dispatching a doomed evaluation on every " +
			"window; the next entry that rewrites the aspect — a credit, or a charge that opens an episode — clears the flag and buys one more attempt. The patient the notification " +
			"addresses is resolved LIVE off the account's own heldFor out-link, never from the payload; an account " +
			"with no live heldFor patient is still evaluated (the arrears fact is about the account), and the " +
			"notification's params carry a patientKey only where one resolves. Restricted to Weaver's dispatch " +
			"actor: the account it names is forwarded into a message a patient actually receives. No clinic " +
			"operation refuses a debtor — the reminder changes what the desk and the patient see, never what they " +
			"may do.",
		Script: accountDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"patientKey":{"type":"string","description":"ClinicCreateAccount only, and required there. vtx.patient.<NanoID> of the patient this account is for (validated alive). The account gets its own independently-minted NanoID; uniqueness (one account per patient) is enforced via the patient's .ledgerAccount guard aspect, not the account's own id."},` +
			`"accountKey":{"type":"string","description":"EvaluateClinicArrears only: vtx.clinicaccount.<NanoID> of the account whose arrears are being aged (required there, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.clinicaccount.<NanoID> — the created account on ClinicCreateAccount (the caller must read it from the ACCEPTED reply, since the id can no longer be derived from patientKey), or the evaluated account on EvaluateClinicArrears."}}}`,
		FieldDescription: map[string]string{
			"patientKey": "ClinicCreateAccount only, and required there. Full vtx.patient.<NanoID> key of the patient the account is opened for. ClinicCreateAccount validates it is alive, mints the account under a fresh independent NanoID, writes the patient's .ledgerAccount guard aspect (one account per patient) and the heldFor link (account→patient). EvaluateClinicArrears takes no patientKey field: the patient is resolved live off that same heldFor link, and carried into the notification params only where one resolves.",
			"accountKey": "EvaluateClinicArrears only, and required there. Full vtx.clinicaccount.<NanoID> key of the account to age. Validated alive; its postedTo history is replayed under a bounded budget and the FIFO-oldest open charge's due date is recorded on the account's .arrears aspect.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "EvaluateClinicArrears — age a patient's balance and remind once it is overdue",
				Payload: map[string]any{"accountKey": "vtx.clinicaccount.<NanoID>"},
				ExpectedOutcome: "Validates the account is alive, replays its postedTo history under the evaluation budget and ages it " +
					"FIFO. Writes vtx.clinicaccount.<NanoID>.arrears = {evaluatedAt, dueAt?, remindedFor?, sentAt?} — {evaluatedAt} " +
					"alone when nothing is owed. When the oldest open charge's due date has passed it stamps remindedFor = " +
					"that date, and where no reminder has yet gone out in this episode (sentAt absent) ALSO stamps sentAt and " +
					"emits external.notification keyed <accountKey>:<dueAt>, with a patientKey in its params only where " +
					"the account's own heldFor link resolves to a live patient. A re-run recomputes the head, finds sentAt " +
					"already recorded, and sends nothing. A history past the replay budget records historyTooLong instead, " +
					"carrying what was already recorded and sending nothing. Rejects AuthDenied for any actor but Weaver's " +
					"dispatch actor and UnknownAccount for an absent or tombstoned account.",
			},
			{
				Name:    "ClinicCreateAccount — open the ledger account for a registered patient",
				Payload: map[string]any{"patientKey": "vtx.patient.<NanoID>"},
				ExpectedOutcome: "Validates the patient is alive. Atomically commits vtx.clinicaccount.<freshNanoID> (root data {} — D5) " +
					"+ the patient's .ledgerAccount guard aspect + the heldFor link (account→patient). Emits " +
					"account.created{accountKey, patientKey}. Returns primaryKey (the new account key — the caller's only " +
					"reliable source for it). Rejects with UnknownPatient if the patient is absent, or AccountAlreadyExists " +
					"if the caller declared the guard aspect in reads and it already exists (a repeat/racing caller retrying " +
					"after learning the account already exists) — a first-time caller who declared only patientKey instead " +
					"sees a raw substrate conflict on the guard aspect's create-only write if it loses a genuine race.",
			},
		},
	}
}

// accountGuardAspectTypeDDL declares the .ledgerAccount aspect (class
// clinicLedgerAccountGuard) ClinicCreateAccount writes on the PATIENT — the
// deterministic create-only key that enforces "at most one ledger account per
// patient" now that the account itself carries an independent NanoID (not the
// patient's own). Declaration-only: the aspect is written by ClinicCreateAccount,
// never has its own operationType.
func accountGuardAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "clinicLedgerAccountGuard",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"ClinicCreateAccount"},
		Description: "Per-patient ledger-account uniqueness guard aspect. Stored as vtx.patient.<NanoID>.ledgerAccount " +
			"(class clinicLedgerAccountGuard) = {accountKey: <vtx.clinicaccount.<NanoID>>}. Non-sensitive. Created " +
			"exactly once by ClinicCreateAccount, atomically alongside the account vertex it names — a second ClinicCreateAccount for " +
			"the same patient that declares this key in contextHint.reads sees the clean AccountAlreadyExists domain " +
			"rejection; one that does not (the normal first-ever-call shape, since the key doesn't exist yet to declare) " +
			"instead relies on this aspect's own create-only write to fail a genuine concurrent race. Declaration-only: no " +
			"op handler of its own.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"accountKey":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"accountKey": "The vtx.clinicaccount.<NanoID> this patient's (at most one) ledger account was minted as.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "patient ledger-account guard aspect",
				Payload:         map[string]any{"accountKey": "vtx.clinicaccount.<NanoID>"},
				ExpectedOutcome: "Stored as vtx.patient.<NanoID>.ledgerAccount; created once by ClinicCreateAccount alongside the account vertex it names.",
			},
		},
	}
}

// accountBalanceAspectTypeDDL declares the .balance aspect (class
// clinicAccountBalance) on the ACCOUNT — the maintained O(1) running-total
// cache accountDDLScript mints at ClinicCreateAccount ({balanceCents: 0}) and
// transactionDDLScript keeps updated by the signed amount on every
// ClinicDebitAccount/ClinicCreditAccount. Declaration-only, mirroring
// accountGuardAspectTypeDDL: the aspect is written by those three ops' own
// handlers, never has an operationType of its own.
func accountBalanceAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "clinicAccountBalance",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"ClinicCreateAccount", "ClinicDebitAccount", "ClinicCreditAccount"},
		Description: "Per-account running-balance cache aspect. Stored as vtx.clinicaccount.<NanoID>.balance " +
			"(class clinicAccountBalance) = {balanceCents: <integer>}. Non-sensitive. Minted at {balanceCents: 0} by " +
			"ClinicCreateAccount alongside the account vertex it names, then kept in lockstep with every posted entry by " +
			"ClinicDebitAccount (+= amountCents) and ClinicCreditAccount (-= amountCents) via a bare update — auto-conditioned " +
			"on the step-4 hydrated revision rather than an explicit expectedRevision, which is what makes " +
			"it retry-eligible under a concurrent writer instead of hard-conflicting. That conditioning depends on the key " +
			"being declared, and the transaction DDL's own derive_reads declares it on every dispatch rather than trusting the " +
			"submitter to (every dispatcher declares it in optionalReads as well). An account opened before this aspect " +
			"existed carries none: a charge, a staff payment and a waiver against such an account post and leave it alone, and " +
			"only a self-scoped patient payment — the one leg whose cap needs the number — replays the account's own history " +
			"to compute and mint it. Exists purely as this package's own O(1) " +
			"authorization cache (the self-scoped ClinicCreditAccount amount-owed check); the clinicLedgerHistory lens remains " +
			"the independently-derived display source of truth and never reads this aspect. Declaration-only: no op handler " +
			"of its own.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"balanceCents":{"type":"integer"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"balanceCents": "The account's current running balance in integer cents (positive = owed, can go negative on an overpayment/over-waiver). Maintained by ClinicDebitAccount/ClinicCreditAccount, never set directly by a caller.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "account running-balance cache aspect",
				Payload:         map[string]any{"balanceCents": 2500},
				ExpectedOutcome: "Stored as vtx.clinicaccount.<NanoID>.balance; minted at 0 by ClinicCreateAccount, updated by the signed amount on every ClinicDebitAccount/ClinicCreditAccount.",
			},
		},
	}
}

// accountArrearsAspectTypeDDL declares the .arrears aspect (class
// clinicAccountArrears) on the ACCOUNT — the arrears-episode state the
// clinicArrearsReminders convergence lens reads, and the marker that records
// which episode a reminder has already gone out for.
//
// Its LIFETIME, end to end. There is none at ClinicCreateAccount: a brand-new
// account owes nothing, and its missing evaluatedAt is exactly what opens the
// convergence gap once, so the first evaluation writes the aspect. From there
// THREE writers maintain it, each conditioned on one hydrated revision (the key
// is declared optionalReads by both DDLs' derive_reads and by every dispatcher,
// so a bare update is auto-conditioned and retry-eligible rather than
// last-write-wins):
//
//   - ClinicDebitAccount that takes the balance from zero-or-below to owing
//     opens an episode: {dueAt = this charge's postedAt + the net term,
//     evaluatedAt}, dropping any finished episode's remindedFor/sentAt/stale. A
//     debit against an account that ALREADY owes writes nothing — the head is
//     an older charge, and re-stamping dueAt would push a weeks-old debt's due
//     date back to today. Nor does a debit that leaves the account still IN
//     CREDIT (an over-waiver or a reversal of a paid charge took it below zero
//     and this charge only eats into that surplus) — the surplus prepays the
//     charge outright, so there is no open debit to age.
//   - ClinicCreditAccount that takes the balance to zero or below ends the
//     episode: {evaluatedAt} alone, so no timer stays armed.
//   - ClinicCreditAccount that leaves a balance marks the state stale
//     (carrying every other field): a partial payment can move the FIFO head
//     to a later charge with a later due date, which no single entry can
//     compute. A legacy account (no .balance, so the entry op has no before/
//     after balance at all) can ONLY ever mark stale, never mint.
//   - EvaluateClinicArrears recomputes the head from the account's own history
//     and rewrites the aspect outright — which is what the stale mark asks for,
//     so stale is never carried across an evaluation, and neither is
//     historyTooLong. It carries sentAt forward for as long as the episode
//     runs: that field, not remindedFor, is what says a reminder has already
//     gone out for THIS episode, so a head that a partial payment moved to
//     another overdue charge is recorded (remindedFor) without sending again.
//   - An evaluation part-way through a history longer than one page of its
//     postedTo replay writes a CHECKPOINT, replay = {phase, cursor, pages,
//     debits, reversed, creditCents}: the pages consumed, the cursor to resume
//     from, the running aggregate the FIFO head is computed from once the
//     enumeration is exhausted, and a phase that flips on every page (the
//     lens projects one continuation gap per phase, so Weaver chains the
//     pages). Every other field is carried verbatim — evaluatedAt still names
//     the last COMPLETED evaluation — and nothing is sent. The finalize page
//     writes the aspect without it; the entry ops drop it on every branch (a
//     posted entry changes the set under the cursor) and mark stale, so the
//     next evaluation restarts at page 1.
//   - The one evaluation that does NOT recompute is the degraded one: an
//     account whose postedTo history outran the replay budget records
//     historyTooLong together with historyBudget, the entry count it
//     exhausted, carrying dueAt/remindedFor/sentAt untouched and dropping
//     stale and the checkpoint, and sends nothing. The pair suppresses both
//     the convergence gap and the timer while the recorded budget is at least
//     the current one, so the row goes quiet rather than re-dispatching a
//     doomed evaluation on every window; both are dropped by the carry of the
//     next entry that rewrites the aspect — a credit, or an episode-opening
//     charge (a charge against an already-owing balance writes nothing) —
//     which buys exactly one more attempt. A flag recorded under a smaller
//     budget than the current one re-opens the gap for one evaluation under
//     the current budget, so a raised budget reaches the accounts the old one
//     parked, once.
//
// Non-sensitive: dates, booleans, a page count and per-transaction cent
// aggregates on a vtx.clinicaccount (not an identity), no PII. Declaration-only:
// written by the three ops above, never dispatched as an operation in its own
// right.
func accountArrearsAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "clinicAccountArrears",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"ClinicDebitAccount", "ClinicCreditAccount", arrearsOp},
		Description: "Per-account arrears-episode aspect. Stored as vtx.clinicaccount.<NanoID>.arrears " +
			"(class clinicAccountArrears) = {evaluatedAt, dueAt?, remindedFor?, sentAt?, stale?, historyTooLong?, historyBudget?, replay?}. Non-sensitive. " +
			"dueAt is the FIFO-oldest still-open charge's postedAt plus the ledger's net term — a RECORDED time " +
			"fact, written by the op, never a clock a lens reads. remindedFor names the dueAt the evaluation has " +
			"acknowledged as passed — it is what closes the convergence gap; sentAt is when a reminder actually went " +
			"out, and its ABSENCE is the send condition, which is what makes the notification once-per-EPISODE rather " +
			"than once-per-head or once-per-convergence-window. historyTooLong means the account's history outran the " +
			"evaluation's replay budget, so no head could be computed; historyBudget records the budget (in entries) it " +
			"exhausted. The flag suppresses the timer, and the gap while that budget is at least the current one (the " +
			"row stays visible but quiet for an operator); a flag recorded under a smaller budget re-opens the gap for one " +
			"evaluation under the current one. Both are dropped by the next entry that rewrites the aspect — a credit, or a charge that opens an episode — which buys one " +
			"further attempt. replay is the checkpoint of an evaluation part-way through a history longer than one page: " +
			"{phase, cursor, pages, debits, reversed, creditCents} — the pages consumed, the cursor to resume from, the " +
			"running aggregate, and a phase that flips on every page so the lens's two continuation gaps chain the " +
			"dispatches. Present only between the first page and the last; the finalize page writes the aspect without " +
			"it and every entry op drops it (a charge that would otherwise write nothing carries the state and marks it stale when a " +
			"checkpoint is present). stale means what is recorded may no longer " +
			"describe the account (a partial payment moved the head, or the account carries no .balance to reason " +
			"with) and is a request for a fresh EvaluateClinicArrears, which rewrites the aspect and so never carries " +
			"it forward. Written by ClinicDebitAccount (opens an episode on an account that owed nothing), " +
			"ClinicCreditAccount (ends the episode at zero, else marks stale) and EvaluateClinicArrears " +
			"(recomputes the head). Read by the clinicArrearsReminders convergence lens and projected for the front " +
			"desk and the patient's statement by clinicPatientAccounts. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"evaluatedAt":{"type":"string"},"dueAt":{"type":"string"},"remindedFor":{"type":"string"},"sentAt":{"type":"string"},"stale":{"type":"boolean"},"historyTooLong":{"type":"boolean"},"historyBudget":{"type":"integer"},"replay":{"type":"object","properties":{"phase":{"type":"string","enum":["` + ArrearsPhaseA + `","` + ArrearsPhaseB + `"]},"cursor":{"type":"string"},"pages":{"type":"integer"},"debits":{"type":"object"},"reversed":{"type":"object"},"creditCents":{"type":"integer"}}}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"evaluatedAt":    "RFC3339 instant (canonical UTC) the arrears state was last written — by an evaluation or by the entry that changed the episode. Its ABSENCE is what opens the convergence gap for an account nothing has ever evaluated.",
			"dueAt":          "RFC3339 instant (canonical UTC) the FIFO-oldest still-open charge falls overdue: that charge's own postedAt plus the ledger's net term. Absent when the account owes nothing.",
			"remindedFor":    "The dueAt the last evaluation acknowledged as passed. Equal to dueAt closes the convergence gap; different (or absent) leaves it open for a recorded lapse to re-open.",
			"sentAt":         "RFC3339 instant (canonical UTC) a reminder for this arrears episode was sent — the timestamp the front desk and the patient's statement show. Its ABSENCE is what lets the next passed deadline send; it is carried across every write of a live episode and dropped only where the episode itself ends.",
			"stale":          "True when what is recorded may no longer describe the account (a partial payment moved the FIFO head, or the account carries no .balance). Opens the convergence gap; cleared by the evaluation that recomputes the head.",
			"historyTooLong": "True when the account's postedTo history outran the evaluation's bounded replay budget, so no FIFO head could be computed. Suppresses the freshness timer, and — with a historyBudget at least the current budget — the convergence gap: the row stays in the read model for an operator to see, without re-dispatching an evaluation that cannot succeed. Dropped by the next entry that rewrites the aspect — a credit, or a charge that opens an episode; a charge against an already-owing balance writes nothing unless a replay is in progress — which also marks the state stale, buying exactly one more attempt.",
			"historyBudget":  "The replay budget, in postedTo entries, the degraded evaluation exhausted (the package's page size × page cap at the time). A recorded budget smaller than the current one — or none — re-opens the convergence gap for exactly one evaluation under the current budget, so a raised budget reaches the accounts the old one parked. Written only beside historyTooLong and dropped with it.",
			"replay":         "The checkpoint of an evaluation part-way through a history longer than one page of its postedTo replay: {phase: '" + ArrearsPhaseA + "'|'" + ArrearsPhaseB + "', cursor, pages, debits: {txId: {postedAt, amountCents}}, reversed: {txId: cents}, creditCents}. phase flips on every page and is the lens's continuation trigger (one gap per phase); cursor resumes the enumeration; pages counts those consumed; the three aggregates are what the FIFO head is computed from once the enumeration is exhausted. Present only between the first page and the last — the finalize page and the degrade write the aspect without it, and every entry op drops it, because a posted entry changes the set under the cursor. A checkpoint the evaluation cannot resume (a malformed field) is treated as absent: the evaluation restarts at page 1.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "account arrears aspect — overdue, reminded once",
				Payload:         map[string]any{"evaluatedAt": "2026-08-22T09:00:00Z", "dueAt": "2026-08-06T14:20:00Z", "remindedFor": "2026-08-06T14:20:00Z", "sentAt": "2026-08-22T09:00:00Z"},
				ExpectedOutcome: "Stored as vtx.clinicaccount.<NanoID>.arrears; written by EvaluateClinicArrears on the commit that also emitted the notification. remindedFor = dueAt closes the gap, so no second reminder goes out for this episode.",
			},
		},
	}
}

// aspectDeclarationOnlyScript is the declaration-only Starlark for the
// package's aspect-type DDLs — clinicLedgerAccountGuard, clinicAccountBalance,
// clinicAccountArrears and clinicAccountArrearsNotification are written by
// ClinicCreateAccount's, the transaction ops', EvaluateClinicArrears' and the
// notification replyOp's own handlers, never dispatched as operations in their
// own right.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`

func transactionDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "clinictransaction",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"ClinicDebitAccount", "ClinicCreditAccount"},
		Description: "Ledger transaction DDL. Vertex shape: vtx.clinictransaction.<NanoID>, class=clinictransaction, root data = {} " +
			"(minimal, D5 — the entry detail is a .entry aspect). ClinicDebitAccount{accountKey, amountCents, memo?, billedTo?, " +
			"expectedReimbursementCents?, appointmentRef?, visitRef?} records a charge (a copay, an invoice line); ClinicCreditAccount{accountKey, amountCents, memo?, " +
			"reason?, reversesRef?} records a payment received OR a waived charge. Each mints a fresh vtx.clinictransaction.<NanoID> + a .entry aspect " +
			"{type (debit|credit), amountCents, memo?, postedAt, billedTo? (debit only), expectedReimbursementCents? (debit+insurance only), " +
			"reason? (credit only)} " +
			"+ the postedTo link (transaction→account, the transaction is the later-arriving vertex so it is the source — " +
			"Contract #1 §1.1) + a bare (no explicit expectedRevision) update of the account's own .balance aspect (accountDDL) " +
			"by the signed amount — auto-conditioned on the step-4 hydrated revision since this DDL's own derive_reads declares " +
			".balance on every dispatch, which " +
			"is what makes it retry-eligible: a lost race re-hydrates and retries the whole op rather than hard-conflicting. " +
			"Each entry also keeps the account's .arrears episode state (clinicAccountArrears DDL) coarse but current, under " +
			"the same declared-key conditioning: a charge that takes the balance from zero-or-below to owing opens an episode " +
			"(dueAt = its own postedAt plus the net term), a credit that takes it to zero or below ends one ({evaluatedAt} " +
			"alone), a credit that leaves a balance carries the recorded state and marks it stale for EvaluateClinicArrears to " +
			"recompute, and an entry against a legacy account (no .balance) can only ever mark existing state stale. " +
			"An account opened before that aspect existed carries none, and only a self-scoped (patient) ClinicCreditAccount " +
			"backfills it, by replaying that account's own postedTo history once under a bounded budget: a charge, a staff " +
			"payment and a waiver against such an account post without writing .balance, so the account stays legacy until a " +
			"self-pay first touches it and the cache is never seeded from a partial sum. " +
			"The ledgerHistory lens still derives its own full-history sum independently " +
			"(the display source of truth); .balance is this DDL's O(1) authorization cache, letting a self-scoped credit " +
			"verify the amount owed without replaying the account's whole transaction history. A self-scoped credit may never " +
			"exceed that outstanding balance (AuthDenied, the amounts spelled as dollars); a staff credit or waiver is not " +
			"capped by it and may take the balance negative. Requires " +
			"the accountKey be a live account and amountCents be a positive number. A debit carries a bounded payer dimension — " +
			"billedTo (self|insurance, default self when omitted) and, only when billedTo is insurance, " +
			"expectedReimbursementCents (positive, capped at amountCents) — so a clinic can track what it billed insurance for " +
			"vs. what it actually collected (a ClinicCreditAccount payment) — NOT real X12 837/835 claims/clearinghouse integration, " +
			"which is out of scope for a reference vertical. Both fields reject on a ClinicCreditAccount (a payment has nothing to bill). " +
			"A credit's reason (payment|waiver, default payment when omitted) distinguishes cash actually collected from debt the " +
			"clinic forgave (e.g. a no-show fee waived as a courtesy) — both reduce the derived balance identically, but the " +
			"ledgerHistory lens projects reason so a reader never mistakes a waiver for money received. reason:\"waiver\" is " +
			"rejected on a self-scoped (patient) credit — post_entry's own authContextTarget branch — since a patient may pay " +
			"down their own balance but never forgive it. " +
			"ClinicDebitAccount also accepts an optional appointmentRef (vtx.appointment.<NanoID>, validated alive when supplied — " +
			"UnknownAppointment otherwise): the charge IS the fee that appointment's current status carries. When present, the " +
			"op reads the appointment's .status (a derive_reads-declared optionalRead) and refuses NoFeeToSettle unless it carries " +
			"noShowFeeCents > 0 — the settles audit link (transaction→appointment) it then writes is what the clinicNoShowSettlement " +
			"lens (targets.go) walks to converge the no-show-fee gap, and a settles link on a fee-less appointment would read as a " +
			"correction owed a reversal. A charge that is merely FOR a visit (a copay, a procedure) names it through the separate " +
			"optional visitRef (vtx.appointment.<NanoID>, validated alive — UnknownAppointment otherwise — and this account's " +
			"patient's own appointment via its forPatient link — WrongPatient otherwise; rejected on a ClinicCreditAccount, as " +
			"appointmentRef is) which writes a forVisit link (transaction→appointment) that only the clinicLedgerHistory lens " +
			"projects; appointmentRef and visitRef are mutually exclusive (InvalidArgument). A plain human-submitted " +
			"ClinicDebitAccount (neither field) is unaffected — appointmentRef mirrors cafe-ledger's tabRef shape. " +
			"ClinicCreditAccount likewise accepts an optional reversesRef (vtx.clinictransaction.<NanoID> of the debit it " +
			"reverses, validated alive when supplied — UnknownTransaction otherwise — and posted to this same account via its " +
			"postedTo link — WrongAccount otherwise; rejected on a ClinicDebitAccount, and AuthDenied on a self-scoped (patient) " +
			"credit, since the reverses link is what disarms the reversal a later correction owes): when " +
			"present, writes a reverses audit link (credit transaction→the reversed debit transaction) that " +
			"clinicNoShowSettlement's missing_reversal gap walks — the reversal Weaver dispatches once a " +
			"CorrectAppointmentStatus correction moves a charged no-show appointment off `noShow` (clinic-domain never touches " +
			"the ledger directly; the lens converges the gap the correction leaves behind).",
		Script: transactionDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"accountKey":{"type":"string","description":"vtx.clinicaccount.<NanoID> the transaction posts to (ClinicDebitAccount/ClinicCreditAccount; required, validated alive)."},` +
			`"amountCents":{"type":"number","description":"The transaction amount in integer cents; required, must be > 0. A debit is a charge (increases what the patient owes); a credit is a payment (decreases it)."},` +
			`"memo":{"type":"string","description":"Optional free-text description of the charge or payment (e.g. \"Office visit copay\", \"Insurance payment\"). Optional."},` +
			`"billedTo":{"type":"string","enum":["self","insurance"],"description":"ClinicDebitAccount only; who the charge is billed to. Optional, defaults to \"self\" when omitted. Rejected on ClinicCreditAccount."},` +
			`"expectedReimbursementCents":{"type":"number","description":"ClinicDebitAccount only, and only when billedTo is \"insurance\": the amount expected back from the payer, in integer cents. Required when billedTo is \"insurance\" (rejected otherwise), must be > 0 and <= amountCents."},` +
			`"appointmentRef":{"type":"string","description":"ClinicDebitAccount only; optional vtx.appointment.<NanoID> back-reference to the appointment whose fee this charge IS. When supplied, validated alive (UnknownAppointment otherwise) and refused NoFeeToSettle unless the appointment's current status carries noShowFeeCents > 0; a settles audit link (transaction→appointment) is then written — the clinicNoShowSettlement lens reads it to converge the gap. Mutually exclusive with visitRef (InvalidArgument). Mirrors cafe-ledger's tabRef."},` +
			`"visitRef":{"type":"string","description":"ClinicDebitAccount only; optional vtx.appointment.<NanoID> of the visit this charge is FOR (a copay, a procedure). When supplied, validated alive (UnknownAppointment otherwise) and as this account's patient's own appointment (WrongPatient otherwise), and a forVisit link (transaction→appointment) is written that the clinicLedgerHistory lens projects as the line's appointmentKey/visitStartsAt (settlesFee false). Never read by clinicNoShowSettlement. Mutually exclusive with appointmentRef (InvalidArgument); rejected on ClinicCreditAccount."},` +
			`"reason":{"type":"string","enum":["payment","waiver"],"description":"ClinicCreditAccount only; optional, defaults to \"payment\" when omitted. \"waiver\" records the credit as debt the clinic forgave rather than cash collected — both reduce the derived balance the same way, but the ledgerHistory lens projects reason so the two are never confused. Rejected on ClinicDebitAccount, and rejected on a self-scoped (patient) credit — a patient may pay down their own balance but never waive it."},` +
			`"reversesRef":{"type":"string","description":"ClinicCreditAccount only; optional vtx.clinictransaction.<NanoID> back-reference to the debit this credit reverses (e.g. a no-show fee posted before a CorrectAppointmentStatus correction moved the appointment off noShow). When supplied, validated alive (UnknownTransaction otherwise) and posted to this account (WrongAccount otherwise), and a reverses audit link (transaction→transaction) is written — the clinicNoShowSettlement lens reads it to converge the missing_reversal gap. Rejected on ClinicDebitAccount and on a self-scoped (patient) credit (AuthDenied)."}},` +
			`"required":["accountKey","amountCents"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.clinictransaction.<NanoID> of the minted transaction (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"accountKey":                 "Full vtx.clinicaccount.<NanoID> key the transaction posts to. ClinicDebitAccount/ClinicCreditAccount validate it is alive and write the postedTo link (transaction→account) the ledgerHistory lens walks.",
			"amountCents":                "The transaction amount in integer cents; required, must be a positive number. Stored on the .entry aspect and projected verbatim by the ledgerHistory lens.",
			"memo":                       "Optional free-text description of the charge or payment (e.g. \"Office visit copay\", \"Insurance payment — claim #4471\"). Stored on the .entry aspect when supplied; projected by the ledgerHistory lens.",
			"billedTo":                   "ClinicDebitAccount only: \"self\" or \"insurance\" (default \"self\" when omitted). Stored on the .entry aspect; projected by the ledgerHistory lens. Rejected on ClinicCreditAccount — a payment has nothing to bill.",
			"expectedReimbursementCents": "ClinicDebitAccount only, and only when billedTo is \"insurance\": the amount expected back from the payer, in integer cents (required then, must be > 0 and <= amountCents; rejected when billedTo is \"self\" or on a ClinicCreditAccount).",
			"appointmentRef":             "ClinicDebitAccount only: optional full vtx.appointment.<NanoID> key of the appointment whose fee this charge IS. Validated alive when supplied (UnknownAppointment otherwise) and refused NoFeeToSettle unless the appointment's current status carries noShowFeeCents > 0; writes a settles link (transaction→appointment) the clinicNoShowSettlement lens walks to converge the gap. Mutually exclusive with visitRef.",
			"visitRef":                   "ClinicDebitAccount only: optional full vtx.appointment.<NanoID> key of the visit this charge is FOR. Validated alive when supplied (UnknownAppointment otherwise) and as this account's patient's own appointment (WrongPatient otherwise); writes a forVisit link (transaction→appointment) the clinicLedgerHistory lens projects as appointmentKey/visitStartsAt with settlesFee false. Mutually exclusive with appointmentRef; rejected on ClinicCreditAccount.",
			"reason":                     "ClinicCreditAccount only: \"payment\" or \"waiver\" (default \"payment\" when omitted). Stored on the .entry aspect; projected by the ledgerHistory lens. Rejected on ClinicDebitAccount, and rejected on a self-scoped (patient) credit — only front-desk staff / the operator may waive a charge.",
			"reversesRef":                "ClinicCreditAccount only: optional full vtx.clinictransaction.<NanoID> key of the debit this credit reverses. Validated alive when supplied (UnknownTransaction otherwise) and posted to this account (WrongAccount otherwise); writes a reverses link (transaction→transaction) the clinicNoShowSettlement lens walks to converge the missing_reversal gap. Rejected on ClinicDebitAccount and on a self-scoped (patient) credit (AuthDenied).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "ClinicDebitAccount — charge a self-pay copay",
				Payload: map[string]any{"accountKey": "vtx.clinicaccount.<NanoID>", "amountCents": 2500, "memo": "Office visit copay"},
				ExpectedOutcome: "Validates the account is alive and amountCents > 0. Atomically commits vtx.clinictransaction.<NanoID> " +
					"(root data {} — D5) + the .entry aspect {type: debit, amountCents: 2500, memo: \"Office visit copay\", billedTo: \"self\", postedAt} " +
					"(billedTo defaults to self when omitted) + the postedTo link (transaction→account). Emits " +
					"account.debited{accountKey, transactionKey, amountCents}. Returns primaryKey. Rejects UnknownAccount if the account " +
					"is absent, or InvalidArgument if amountCents <= 0.",
			},
			{
				Name: "ClinicDebitAccount — charge billed to insurance",
				Payload: map[string]any{"accountKey": "vtx.clinicaccount.<NanoID>", "amountCents": 15000, "memo": "Specialist visit",
					"billedTo": "insurance", "expectedReimbursementCents": 12000},
				ExpectedOutcome: "Same as the self-pay case, but the .entry aspect adds billedTo: \"insurance\" + expectedReimbursementCents: 12000. " +
					"Rejects InvalidArgument if expectedReimbursementCents is missing, <= 0, or > amountCents.",
			},
			{
				Name:    "ClinicDebitAccount — Weaver-dispatched no-show settlement (appointmentRef)",
				Payload: map[string]any{"accountKey": "vtx.clinicaccount.<NanoID>", "amountCents": 2500, "appointmentRef": "vtx.appointment.<NanoID>"},
				ExpectedOutcome: "Same as the self-pay case, plus validates appointmentRef is alive (UnknownAppointment otherwise) and " +
					"that its current .status carries noShowFeeCents > 0 (NoFeeToSettle otherwise), then writes " +
					"lnk.clinictransaction.<id>.settles.appointment.<id> (transaction→appointment). This is the shape " +
					"clinic-ledger's own clinicNoShowSettlement Weaver target dispatches — a human-submitted ClinicDebitAccount simply " +
					"omits appointmentRef and gets the plain self-pay-copay shape above.",
			},
			{
				Name:    "ClinicDebitAccount — charge a copay for a visit (visitRef)",
				Payload: map[string]any{"accountKey": "vtx.clinicaccount.<NanoID>", "amountCents": 2500, "memo": "Office visit copay", "visitRef": "vtx.appointment.<NanoID>"},
				ExpectedOutcome: "Same as the self-pay case, plus validates visitRef is alive (UnknownAppointment otherwise) and writes " +
					"lnk.clinictransaction.<id>.forVisit.appointment.<id> (transaction→appointment) — no settles link, so " +
					"clinicNoShowSettlement never reads the charge as the visit's fee. The clinicLedgerHistory lens projects the " +
					"visit as the line's appointmentKey/visitStartsAt with settlesFee false. Rejects InvalidArgument if appointmentRef " +
					"is also supplied.",
			},
			{
				Name:    "ClinicCreditAccount — record a payment",
				Payload: map[string]any{"accountKey": "vtx.clinicaccount.<NanoID>", "amountCents": 2500, "memo": "Insurance payment — claim #4471"},
				ExpectedOutcome: "Same shape as ClinicDebitAccount, but writes .entry{type: credit, ...} (no billedTo/expectedReimbursementCents — " +
					"rejected InvalidArgument if either is supplied) and emits account.credited{accountKey, transactionKey, amountCents}. " +
					"A payment reduces what the patient owes (the ledgerHistory-derived balance = sum(debits) − sum(credits)).",
			},
			{
				Name:    "ClinicCreditAccount — waive a no-show fee (front-desk/operator only)",
				Payload: map[string]any{"accountKey": "vtx.clinicaccount.<NanoID>", "amountCents": 2500, "memo": "Waived — patient hardship", "reason": "waiver"},
				ExpectedOutcome: "Same shape as a plain payment, but .entry carries reason: \"waiver\" instead of the default \"payment\" — the " +
					"balance drops identically, but the ledgerHistory lens projects reason so a reader never mistakes forgiven debt for cash " +
					"collected. Rejects AuthDenied if the caller is a self-scoped patient — only the operator/" +
					"front-of-house scope=any grant may waive.",
			},
			{
				Name:    "ClinicCreditAccount — Weaver-dispatched fee reversal (reversesRef)",
				Payload: map[string]any{"accountKey": "vtx.clinicaccount.<NanoID>", "amountCents": 2500, "memo": "Fee reversal (corrected)", "reason": "waiver", "reversesRef": "vtx.clinictransaction.<NanoID>"},
				ExpectedOutcome: "Same as the waiver case, plus validates reversesRef is alive (UnknownTransaction otherwise) and writes " +
					"lnk.clinictransaction.<id>.reverses.clinictransaction.<id> (credit→the reversed debit). This is the shape " +
					"clinicNoShowSettlement's missing_reversal gap dispatches once CorrectAppointmentStatus moves a charged " +
					"appointment (a no-show, or a patient's late cancel) to a status carrying no fee — a human-submitted " +
					"ClinicCreditAccount simply omits reversesRef and gets the plain waiver shape above.",
			},
		},
	}
}
