package loftspaceledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `account`
// (LoftspaceCreateAccount, EvaluateLoftspaceArrears), `transaction`
// (DebitAccount, LoftspaceRecordCharge, CreditAccount, ReturnDeposit), the
// `ledgerAccountGuard` aspect-type declaration (the lease-anchored
// uniqueness guard LoftspaceCreateAccount writes), the
// `loftspaceAccountArrears` aspect-type declaration (the account's
// arrears-episode state), and the notification-outcome DDL pair
// (notifications.go) the bridge replies onto.
func DDLs() []pkgmgr.DDLSpec {
	return append([]pkgmgr.DDLSpec{
		accountDDL(),
		accountGuardAspectTypeDDL(),
		accountArrearsAspectTypeDDL(),
		transactionDDL(),
	}, notificationDDLs()...)
}

func accountDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "account",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"LoftspaceCreateAccount", arrearsOp},
		Description: "Ledger account DDL. Vertex shape: vtx.account.<NanoID>, class=account, root data = {} " +
			"(minimal, D5 — the balance is LENS-derived by summing transactions, never stored). LoftspaceCreateAccount{leaseAppKey} " +
			"mints the account under its OWN independently-generated NanoID (never reused from the lease — Core KV " +
			"NanoIDs are unique identifiers across all of Core KV, not scoped per vertex type; reuse corrupts " +
			"Refractor adjacency, which keys by bare NodeID with no type qualifier). \"One account per lease\" is " +
			"enforced by a deterministic create-only guard aspect on the PRE-EXISTING leaseapp " +
			"(leaseAppKey+\".ledgerAccount\", ledgerAccountGuard DDL) instead: a second LoftspaceCreateAccount for the same " +
			"lease conflicts on that already-existing aspect key. Writes the heldFor link (account→leaseapp, the " +
			"account is the later-arriving vertex so it is the source — Contract #1 §1.1). Requires the " +
			"leaseAppKey be a live leaseapp (no orphan accounts). " +
			"EvaluateLoftspaceArrears{accountKey} is the second operation on this DDL, dispatched by " +
			"Weaver's loftspaceArrearsReminders playbook rather than by a person: it replays the account's own " +
			"postedTo history, one page per dispatch — a history longer than one page records its running aggregate " +
			"and cursor on .arrears.replay and Weaver dispatches the next page through the lens's phase gaps, so " +
			"the head is computed exactly whatever the history's length — ages it with the same plain FIFO the tenant's statement runs " +
			"(every credit offsets the oldest still-open charge first; an unapplied credit carries forward as " +
			"surplus — no entry in this ledger names a charge it reverses, so there is no netting pre-pass), and " +
			"records on the account's .arrears aspect (loftspaceAccountArrears DDL) dueAt — the oldest open " +
			"charge's OWN recorded due date (the .entry.dueAt DebitAccount stamps from the clause's anniversary " +
			"grid), or its postedAt when it recorded none (a landlord one-off is due on receipt); never a term " +
			"added to the posting — and remindAt = dueAt plus the package's grace (ArrearsGraceDays, 5 days). " +
			"Rent is due on its date and \"N days overdue\" counts from dueAt; the reminder waits out the grace. " +
			"Once remindAt has passed the evaluation records remindedFor = dueAt, and where NO reminder has yet " +
			"gone out in this arrears episode (sentAt ABSENT) the same commit also stamps sentAt and fires an " +
			"external.notification to the bridge's \"notification\" adapter keyed on (accountKey, dueAt, headKey) — " +
			"the head's own transaction key, because recorded due dates repeat across charges on one lease. The send " +
			"condition is sentAt's absence, not remindedFor's value: the unit is the EPISODE — from the charge " +
			"that took the account from square to owing until the balance returns to zero — and a partial payment " +
			"moves the head from one overdue charge to the next without starting a new episode, so exactly ONE " +
			"notification goes out per episode however often the evaluation is re-dispatched, redelivered, or " +
			"re-run over a moved head. A history that nets to nothing owed rewrites the aspect to {evaluatedAt} " +
			"alone — this evaluation is the ONLY thing that ends an episode, since the ledger stores no balance " +
			"for a posted entry to see reach zero — and where a payment to zero and a fresh charge both posted " +
			"before it ran, the recorded send predates the charge that opened the new episode (a send happens only " +
			"after the evaluation has seen that episode's opener posted), so the evaluation drops " +
			"remindedFor/sentAt as the finished episode's and the new one is reminded for on its own merits; a " +
			"head that a partial payment moved past the opener is still the same episode and keeps its send " +
			"record. An account whose history runs past the replay budget (page size × page cap) " +
			"is not refused: the evaluation DEGRADES, recording historyTooLong together with the budget it " +
			"exhausted as historyBudget (carrying dueAt/remindAt/remindedFor/sentAt as they stood, clearing stale " +
			"and the in-progress checkpoint) and sending nothing, which holds " +
			"the row quiet and visible rather than re-dispatching a doomed evaluation on every window; the next " +
			"posted entry clears the flag and its budget and buys one more attempt, and a later, larger budget " +
			"reaches the accounts a smaller one parked. The lease and the tenant the notification " +
			"addresses are resolved LIVE off the account's own heldFor out-link and the live lease's own " +
			"applicationFor out-link, never from the payload; an account whose lease was withdrawn or whose " +
			"tenant has gone dead is still evaluated (the arrears fact is about the account), and the " +
			"notification's params carry leaseAppKey / identityKey only where each resolves. Restricted to " +
			"Weaver's dispatch actor: the account it names is forwarded into a message a tenant actually " +
			"receives.",
		Script: accountDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the lease this account is for (LoftspaceCreateAccount; required there, validated alive). The account gets its own independently-minted NanoID; uniqueness (one account per lease) is enforced via the leaseapp's .ledgerAccount guard aspect, not the account's own id."},` +
			`"accountKey":{"type":"string","description":"EvaluateLoftspaceArrears only: vtx.account.<NanoID> of the account whose arrears are being aged (required there, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.account.<NanoID> — the created account on LoftspaceCreateAccount (the caller must read it from the ACCEPTED reply, since the id can no longer be derived from leaseAppKey), or the evaluated account on EvaluateLoftspaceArrears."}}}`,
		FieldDescription: map[string]string{
			"leaseAppKey": "LoftspaceCreateAccount only, and required there. Full vtx.leaseapp.<NanoID> key of the lease the account is opened for. LoftspaceCreateAccount validates it is alive, mints the account under a fresh independent NanoID, writes the leaseapp's .ledgerAccount guard aspect (one account per lease) and the heldFor link (account→leaseapp). EvaluateLoftspaceArrears takes no leaseAppKey field: the lease is resolved live off that same heldFor link, and carried into the notification params only where it resolves live.",
			"accountKey":  "EvaluateLoftspaceArrears only, and required there. Full vtx.account.<NanoID> key of the account to age. Validated alive; its postedTo history is replayed under a bounded budget and the FIFO-oldest open charge's own recorded due date (or its postedAt when it recorded none) is recorded on the account's .arrears aspect, with the reminder instant the grace puts after it.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "EvaluateLoftspaceArrears — age a lease account and remind once the grace has run out",
				Payload: map[string]any{"accountKey": "vtx.account.<NanoID>"},
				ExpectedOutcome: "Validates the account is alive, replays its postedTo history ONE PAGE PER DISPATCH " +
					"(a history longer than one page leaves a checkpoint on .arrears.replay and Weaver dispatches the next " +
					"page) and ages it plain FIFO once the enumeration is exhausted. Writes vtx.account.<NanoID>.arrears = " +
					"{evaluatedAt, dueAt?, remindAt?, remindedFor?, sentAt?} — " +
					"{evaluatedAt} alone when nothing is owed. dueAt is the oldest open charge's own recorded due date (its " +
					"postedAt when it recorded none); remindAt is dueAt + 5 days. When remindAt has passed it stamps " +
					"remindedFor = dueAt, and where no reminder has yet gone out in this episode (sentAt absent) ALSO stamps " +
					"sentAt and emits external.notification keyed <accountKey>:<dueAt>:<headTransactionKey>, with leaseAppKey / identityKey in its " +
					"params only where the account's own heldFor link resolves to a live lease and that lease's applicationFor " +
					"link to a live identity. A re-run recomputes the head, finds sentAt already recorded, and sends nothing. " +
					"A history past the replay budget (page size × page cap) records historyTooLong and historyBudget instead, " +
					"carrying what was already recorded and " +
					"sending nothing. Rejects AuthDenied for any actor but Weaver's dispatch actor and UnknownAccount for an " +
					"absent or tombstoned account.",
			},
			{
				Name:    "LoftspaceCreateAccount — open the ledger account for a signed lease",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>"},
				ExpectedOutcome: "Validates the leaseapp is alive. Atomically commits vtx.account.<freshNanoID> (root data {} — D5) " +
					"+ the leaseapp's .ledgerAccount guard aspect + the heldFor link (account→leaseapp). Emits " +
					"account.created{accountKey, leaseAppKey}. Returns primaryKey (the new account key — the caller's only " +
					"reliable source for it). Rejects with UnknownLeaseApplication if the lease is absent, or " +
					"AccountAlreadyExists if the caller declared the guard aspect in reads and it already exists (a " +
					"repeat/racing caller retrying after learning the account already exists) — a first-time caller who " +
					"declared only leaseAppKey instead sees a raw substrate conflict on the guard aspect's create-only " +
					"write if it loses a genuine race.",
			},
		},
	}
}

// accountGuardAspectTypeDDL declares the .ledgerAccount aspect (class
// ledgerAccountGuard) LoftspaceCreateAccount writes on the PRE-EXISTING leaseapp — the
// deterministic create-only key that enforces "at most one ledger account per
// lease" now that the account itself carries an independent NanoID (not the
// lease's own). Declaration-only: the aspect is written by LoftspaceCreateAccount,
// never has its own operationType.
func accountGuardAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "ledgerAccountGuard",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"LoftspaceCreateAccount"},
		Description: "Per-lease ledger-account uniqueness guard aspect. Stored as vtx.leaseapp.<NanoID>.ledgerAccount " +
			"(class ledgerAccountGuard) = {accountKey: <vtx.account.<NanoID>>}. Non-sensitive. Created exactly once by " +
			"LoftspaceCreateAccount, atomically alongside the account vertex it names — a second LoftspaceCreateAccount for the same " +
			"lease that declares this key in contextHint.reads sees the clean AccountAlreadyExists domain rejection; " +
			"one that does not (the normal first-ever-call shape, since the key doesn't exist yet to declare) instead " +
			"relies on this aspect's own create-only write to fail a genuine concurrent race. Declaration-only: no op " +
			"handler of its own.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"accountKey":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"accountKey": "The vtx.account.<NanoID> this lease's (at most one) ledger account was minted as.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "lease ledger-account guard aspect",
				Payload:         map[string]any{"accountKey": "vtx.account.<NanoID>"},
				ExpectedOutcome: "Stored as vtx.leaseapp.<NanoID>.ledgerAccount; created once by LoftspaceCreateAccount alongside the account vertex it names.",
			},
		},
	}
}

// accountArrearsAspectTypeDDL declares the .arrears aspect (class
// loftspaceAccountArrears) on the ACCOUNT — the arrears-episode state the
// loftspaceArrearsReminders convergence lens reads, and the marker that records
// which episode a reminder has already gone out for.
//
// Its LIFETIME, end to end. There is none at LoftspaceCreateAccount: a
// brand-new account owes nothing, and its missing evaluatedAt is exactly what
// opens the convergence gap once, so the first evaluation writes the aspect.
// From there TWO kinds of writer maintain it, each conditioned on one
// hydrated revision (the key is declared optionalReads by both DDLs'
// derive_reads and by every dispatcher, so a bare update is auto-conditioned
// and retry-eligible rather than last-write-wins):
//
//   - EvaluateLoftspaceArrears recomputes the head from the account's own
//     history and rewrites the aspect outright — {dueAt, remindAt,
//     evaluatedAt} plus the episode's send record while a charge is open,
//     {evaluatedAt} alone once the history nets to nothing owed. An episode
//     ends ONLY in this op (this ledger stores no balance for a posted entry
//     to see reach zero), in one of two ways: the history nets to nothing
//     owed, or the charge that opened the episode it finds posted after the
//     recorded sentAt — a payment to zero and a fresh charge both posted
//     before the evaluation ran, so the send belongs to the finished episode
//     and is dropped with its remindedFor (a head that a partial payment
//     moved past the opener is still the same episode, and keeps it). stale
//     is never carried across an evaluation, and
//     neither is historyTooLong. It carries sentAt forward for as long as the
//     episode runs: that field, not remindedFor, is what says a reminder has
//     already gone out for THIS episode, so a head that a partial payment
//     moved to another overdue charge is recorded (remindedFor) without
//     sending again.
//   - An evaluation part-way through a history longer than one page of its
//     postedTo replay writes a CHECKPOINT, replay = {phase, cursor, pages,
//     entries}: the pages consumed, the cursor to resume from, and every
//     debit and credit read so far (each keyed by its bare transaction ID,
//     never its full vtx key, and carrying its own recorded postedAt — no
//     netting, no total to collapse into, since this ledger has no reverses
//     relation) — the exact rows the FIFO head is computed from once the
//     enumeration is exhausted, and a phase that flips
//     on every page (the lens projects one continuation gap per phase, so
//     Weaver chains the pages). Every other field is carried verbatim — evaluatedAt still names
//     the last COMPLETED evaluation — and nothing is sent. The finalize page
//     writes the aspect without it; every posted entry drops it (a charge
//     that would otherwise write nothing carries the state and marks it stale
//     when a checkpoint is present) and marks stale, so the next evaluation
//     restarts at page 1.
//   - The one evaluation that does NOT recompute is the degraded one: an
//     account whose postedTo history outran the replay budget records
//     historyTooLong together with historyBudget, the entry count it
//     exhausted, carrying dueAt/remindAt/remindedFor/sentAt untouched and
//     dropping stale and the checkpoint, and sends nothing. The pair
//     suppresses both the convergence gap and the timer while the recorded
//     budget is at least the current one, so the row goes quiet rather than
//     re-dispatching a doomed evaluation on every window; both are dropped by
//     the next posted entry's carry, which buys exactly one more attempt. A
//     flag recorded under a smaller budget than the current one re-opens the
//     gap for one evaluation under the current budget, so a raised budget
//     reaches the accounts the old one parked, once.
//   - EVERY DebitAccount / LoftspaceRecordCharge / CreditAccount /
//     ReturnDeposit against an account that carries the aspect marks it
//     stale (carrying every other
//     field, the send record included, and dropping any in-progress replay
//     checkpoint — a posted entry changes the set the checkpoint's cursor
//     pages over): with no balance to reason from, an
//     entry can tell neither an episode opening from one continuing nor a
//     clearing payment from a partial one, so it asks for the recomputation
//     and never guesses. Against an account with no aspect it writes nothing
//     — such an account is already opening the never-evaluated gap.
//
// Non-sensitive: dates, two booleans, a page count and per-transaction cent
// aggregates on a vtx.account (not an identity), no money and no PII.
// Declaration-only: written by the five ops above, never dispatched as an
// operation in its own right. Never tombstoned (an account is never
// tombstoned).
func accountArrearsAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "loftspaceAccountArrears",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"DebitAccount", "LoftspaceRecordCharge", "CreditAccount", "ReturnDeposit", arrearsOp},
		Description: "Per-account arrears-episode aspect. Stored as vtx.account.<NanoID>.arrears " +
			"(class loftspaceAccountArrears) = {evaluatedAt, dueAt?, remindAt?, remindedFor?, sentAt?, stale?, historyTooLong?, historyBudget?, replay?}. " +
			"Non-sensitive. dueAt is the FIFO-oldest still-open charge's OWN recorded due date (the .entry.dueAt a " +
			"clause-authorized rent charge carries from its anniversary grid), or its postedAt when it recorded none " +
			"(a landlord one-off is due on receipt) — a RECORDED time fact read as the fact it records, never a term " +
			"added to the posting and never a clock a lens reads; it is what \"N days overdue\" counts from. remindAt " +
			"is dueAt plus the package's grace (ArrearsGraceDays, 5 days) — where the reminder timer arms and what the " +
			"recorded lapse is compared against. remindedFor names the dueAt the evaluation has acknowledged as past " +
			"its grace — it is what closes the convergence gap; sentAt is the instant the reminder's outbox event was " +
			"committed (the SEND INTENT — the adapter's delivery outcome is .arrearsNotification), and its ABSENCE is " +
			"the send condition, which is what makes the notification once-per-EPISODE rather than once-per-head or " +
			"once-per-convergence-window. historyTooLong means the account's history outran the evaluation's replay " +
			"budget, so no head could be computed under it; historyBudget records the budget (in entries) it " +
			"exhausted. The flag suppresses the timer, and the gap while that budget is at least the current one (the " +
			"row stays visible but quiet for an operator); a flag recorded under a smaller budget re-opens the gap for " +
			"one evaluation under the current one. Both are dropped by the next posted entry, which buys one further " +
			"attempt. replay is the checkpoint of an evaluation part-way through a history longer than one page: " +
			"{phase, cursor, pages, entries} — the pages consumed, the cursor to resume from, every debit and credit " +
			"read so far keyed by its bare transaction ID and its own recorded postedAt, and a phase that flips on every page so the lens's " +
			"two continuation gaps chain the " +
			"dispatches. Present only between the first page and the last; the finalize page writes the aspect " +
			"without it and every posted entry drops it (a charge that would otherwise write nothing carries the " +
			"state and marks it stale when a checkpoint is present). " +
			"stale means what is recorded may no longer describe the account — EVERY posted entry sets it, because " +
			"this ledger stores no balance for an entry to reason from — and is a request for a fresh " +
			"EvaluateLoftspaceArrears, which rewrites the aspect and so never carries it forward. Written by " +
			"DebitAccount / LoftspaceRecordCharge / CreditAccount / ReturnDeposit (mark stale; drop any checkpoint; mint nothing where absent) and " +
			"EvaluateLoftspaceArrears (recomputes the head; ends the episode at {evaluatedAt} alone when nothing is " +
			"owed, and drops a send record that predates the charge that opened the episode it finds — the boundary between an episode paid " +
			"off and the next one opened before any evaluation ran). Read by the loftspaceArrearsReminders " +
			"convergence lens and projected for the landlord ledger, the tenant statement and the portfolio list by " +
			"leaseAccounts. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"evaluatedAt":{"type":"string"},"dueAt":{"type":"string"},"remindAt":{"type":"string"},"remindedFor":{"type":"string"},"sentAt":{"type":"string"},"stale":{"type":"boolean"},"historyTooLong":{"type":"boolean"},"historyBudget":{"type":"integer"},"replay":{"type":"object","properties":{"phase":{"type":"string","enum":["` + ArrearsPhaseA + `","` + ArrearsPhaseB + `"]},"cursor":{"type":"string"},"pages":{"type":"integer"},"entries":{"type":"object"}}}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"evaluatedAt":    "RFC3339 instant (canonical UTC) the arrears state was last written by an evaluation. Its ABSENCE is what opens the convergence gap for an account nothing has ever evaluated.",
			"dueAt":          "RFC3339 instant (canonical UTC) the FIFO-oldest still-open charge fell due: that charge's own recorded .entry.dueAt, or its postedAt when it recorded none. What the statement counts \"N days overdue\" from. Absent when the account owes nothing.",
			"remindAt":       "RFC3339 instant (canonical UTC) the reminder is armed for: dueAt plus the package's grace (5 days). The convergence lens arms its timer here and compares the recorded lapse against it. Absent when the account owes nothing.",
			"remindedFor":    "The dueAt the last evaluation acknowledged as past its grace. Equal to dueAt closes the convergence gap; different (or absent) leaves it open for a recorded lapse to re-open.",
			"sentAt":         "RFC3339 instant (canonical UTC) the reminder's outbox event was committed for this arrears episode — the send intent the landlord ledger and the tenant's statement show. Its ABSENCE is what lets the next passed reminder instant send; it is carried across every write of a live episode and dropped only by the evaluation that finds the episode over: no open charge, or an episode whose opening charge posted after this instant (the balance returned to zero and a new charge opened a fresh episode before an evaluation ran); a head that a partial payment moved past the opener stays in the same episode and keeps it.",
			"stale":          "True when what is recorded may no longer describe the account — every posted entry sets it, since the ledger stores no balance to reason from. Opens the convergence gap; cleared by the evaluation that recomputes the head.",
			"historyTooLong": "True when the account's postedTo history outran the evaluation's bounded replay budget, so no FIFO head could be computed. Suppresses the freshness timer, and — with a historyBudget at least the current budget — the convergence gap: the row stays in the read model for an operator to see, without re-dispatching an evaluation that cannot succeed. Dropped by the next posted entry (which also marks the state stale), buying exactly one more attempt.",
			"historyBudget":  "The replay budget, in postedTo entries, the degraded evaluation exhausted (the package's page size × page cap at the time). A recorded budget smaller than the current one — or none — re-opens the convergence gap for exactly one evaluation under the current budget, so a raised budget reaches the accounts the old one parked. Written only beside historyTooLong and dropped with it.",
			"replay":         "The checkpoint of an evaluation part-way through a history longer than one page of its postedTo replay: {phase: '" + ArrearsPhaseA + "'|'" + ArrearsPhaseB + "', cursor, pages, entries: {txId: {postedAt, type, amountCents, dueAt}}}. phase flips on every page and is the lens's continuation trigger (one gap per phase); cursor resumes the enumeration; pages counts those consumed; entries carries every debit and credit read so far, keyed by its bare transaction ID (never its full vtx key — the checkpoint carries an identity to re-derive from, not a relationship to stand in for one) and its own recorded postedAt — the exact rows the FIFO head is computed from once the enumeration is exhausted, no netting, no total (this ledger has no reverses relation, and the episode-start computation needs each entry's own real timing, not a collapsed sum); dueAt is carried because arrears_head reads it off every debit row to name the head's own recorded due date. Present only between the first page and the last — the finalize page and the degrade write the aspect without it, and every posted entry drops it, because a posted entry changes the set under the cursor. A checkpoint the evaluation cannot resume (a malformed field) is treated as absent: the evaluation restarts at page 1.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "account arrears aspect — overdue past the grace, reminded once",
				Payload:         map[string]any{"evaluatedAt": "2026-09-15T09:00:00Z", "dueAt": "2026-09-08T00:00:00Z", "remindAt": "2026-09-13T00:00:00Z", "remindedFor": "2026-09-08T00:00:00Z", "sentAt": "2026-09-15T09:00:00Z"},
				ExpectedOutcome: "Stored as vtx.account.<NanoID>.arrears; written by EvaluateLoftspaceArrears on the commit that also emitted the notification. remindedFor = dueAt closes the gap, so no second reminder goes out for this episode.",
			},
		},
	}
}

func transactionDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "transaction",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"DebitAccount", "CreditAccount", "LoftspaceRecordCharge", "ReturnDeposit"},
		Description: "Ledger transaction DDL. Vertex shape: vtx.transaction.<NanoID>, class=transaction, root data = {} " +
			"(minimal, D5 — the entry detail is a .entry aspect). DebitAccount{accountKey, amountCents, memo?, clauseRef?, " +
			"period?} records a charge (rent, a late fee, a deposit) — the orchestrated shape, operator-only, that " +
			"Weaver's clauseSatisfaction playbook dispatches with a clauseRef; LoftspaceRecordCharge{accountKey, " +
			"amountCents, memo?} records the same debit entry as a person's manual charge (no clauseRef/period — a " +
			"vertical-unique name because operationType is a global namespace and cafe-ledger admits its own " +
			"DebitAccount); CreditAccount{accountKey, amountCents, memo?} " +
			"records a payment received; ReturnDeposit{leaseAppKey, clauseKey, accountKey} (below) credits a " +
			"charged security deposit back once the tenancy has ended. Each mints a fresh vtx.transaction.<NanoID> + a .entry aspect {type " +
			"(debit|credit), amountCents, memo?, postedAt, periodStart?, periodEnd?, dueAt?} + the postedTo link (transaction→account, the transaction " +
			"is the later-arriving vertex so it is the source — Contract #1 §1.1). The ledger is APPEND-ONLY — no " +
			"balance is stored or mutated on the account; the ledgerHistory lens derives a balance by summing " +
			"entries, so concurrent debits/credits never race a read-modify-write. Requires the accountKey be a " +
			"live account and amountCents be a positive number. LoftspaceRecordCharge and CreditAccount carry a " +
			"consumer scope=self grant beside the operator's scope=any; a self-scoped submit (authContext.target " +
			"present) is bound to the account's OWN heldFor→leaseapp topology, never the payload, along one of two " +
			"paths tried in order: the RESIDENT (the lease's applicationFor link resolves to the caller) may " +
			"CreditAccount only, capped at the account's recomputed outstanding balance (NoBalanceToPay / " +
			"PaymentExceedsBalance; a resident charge is refused AuthDenied); otherwise the LANDLORD (the caller " +
			"manages the unit the lease appliesToUnit) may LoftspaceRecordCharge and CreditAccount, uncapped — the " +
			"landlord is the creditor. A caller holding neither link is refused AuthDenied without the unit being " +
			"named. DebitAccount has no self grant, so a self-scoped DebitAccount never reaches the script. DebitAccount's optional clauseRef (the " +
			"semantic-contracts Executable Paper consumer, Contract #10 §10.8's canonical directOp target) " +
			"additionally validates the clause is live, DERIVES the authoritative amountCents from the clause's " +
			"own .terms aspect (rejecting AmountMismatch if the payload's amountCents disagrees — money is " +
			"append-only and never self-heals, so a stale or copied payload value is never trusted over the " +
			"clause's own record) and writes the authorizedBy link (transaction→clause, the audit chain of " +
			"custody). What it does to the clause's .status next depends on the accompanying " +
			"`period` param (Fire V3, semantic-contracts' clauseSatisfaction playbook always supplies it alongside " +
			"clauseRef): period=\"monthly\" keeps state active (a recurring clause never completes); any other " +
			"value (or clauseRef with no period, the Fire V1/V2 shape) marks .status completed as before. " +
			"chargeValidUntil — the clause's next due date — is stamped UNCONDITIONALLY either way — this op has " +
			"no read of the clause's own .terms.data.period to cross-check `period` against, so it is a " +
			"defense-in-depth measure (not just the monthly branch's convergence signal): the clauseSatisfaction " +
			"lens's monthly gate reads only chargeValidUntil, never `state`, so a genuinely-monthly clause still " +
			"re-arms correctly even if a caller passed the wrong/no period; the reverse mismatch is harmless (a " +
			"oneTime clause's gate never reads chargeValidUntil). WHICH instant is stamped depends on the clause's " +
			"term: an untermed clause gets postedAt + 30 days (the legacy cadence); a clause whose .terms carry " +
			"validFrom/validUntil bills the calendar-month period whose start is its recorded due date " +
			"(.status.chargeValidUntil, declared as an OptionalRead by the playbook — absent or before validFrom " +
			"means period 0) and records the NEXT anniversary, computed from validFrom each time so Jan 31 -> " +
			"Feb 28 -> Mar 31 never drifts; when that next due reaches validUntil the clause is marked completed " +
			"(its final period is billed), and a due at or past validUntil is refused (TermExhausted) with no " +
			"transaction minted. A recurring charge records the period it bills on its own .entry — periodStart / " +
			"periodEnd (the termed clause's anniversary period, its end capped at validUntil; the untermed clause's " +
			"postedAt + 30 days) and dueAt (the period's start: validFrom is the first period's due date and every " +
			"later period falls due on its anniversary) — so a statement names the month covered and the due date " +
			"from the row itself. A one-time charge stamps none of the three. " +
			"ReturnDeposit{leaseAppKey, clauseKey, accountKey} (the LoftSpace \"a lease takes a security deposit\" " +
			"design) is Weaver's dispatch for leaseRentSettlement's missing_depositReturn gap (packages/semantic-contracts): " +
			"operator-only, no self grant, no screen. It reads everything from the graph's own record, never the " +
			"payload — the clause's .terms must carry purpose=deposit on a oneTime computational clause (NotADeposit " +
			"otherwise: a monthly clause completes on its final period after N charges and a judgment clause charges " +
			"nothing, so neither is a deposit) and its amountCents is " +
			"the amount credited; the clause's .status must be completed, the state DebitAccount's one-time charge " +
			"leaves (DepositNotCharged while still active — an uncharged deposit is never refunded); the lease's " +
			".tenancy must record endedAt (TenancyNotEnded otherwise — the recorded end, never the notice or the " +
			"term; UnknownLeaseApplication for a lease that is not live); and the clause's own deterministic " +
			"chargesTo / governs links must name the payload account and lease (ClauseAccountMismatch / " +
			"ClauseLeaseMismatch). Only then is a clause already returned an idempotent no-op (empty mutations, no " +
			"event) — a mis-addressed submit is refused, never read as done. Otherwise it mints vtx.transaction.<NanoID> + .entry {type: credit, " +
			"amountCents, postedAt, memo: \"Security deposit returned\"} + the postedTo link + the authorizedBy link " +
			"(transaction→clause, the same chain of custody the charge recorded), moves the clause's .status to " +
			"{state: returned, returnedAt: postedAt, ...every field kept} pinned to the revision it hydrated at, and " +
			"marks .arrears stale like every other entry. Emits loftspace.depositReturned{accountKey, transactionKey, " +
			"clauseKey, leaseAppKey, amountCents}. The credit is an ordinary credit: it nets against whatever the " +
			"tenant still owes and the remainder reads as a credit balance — the refund owed. The DDL's own " +
			"derive_reads hydrates every key it reads (the account and its .arrears, the clause and its .terms and " +
			".status, the lease and its .tenancy, and the two custody links) whatever the submitter declared. " +
			"Every entry, from any of the four ops, ALSO marks the account's .arrears episode state " +
			"(loftspaceAccountArrears DDL) stale where it exists — carrying every other field, the episode's send " +
			"record included — and mints nothing where it does not: with no stored balance an entry cannot tell an " +
			"episode opening from one continuing, so it asks EvaluateLoftspaceArrears to recompute rather than " +
			"guess. A .arrears document of any other class is refused (InvalidState) rather than carried. The write " +
			"is a bare update auto-conditioned on the revision the key hydrated at, and the DDL's own derive_reads " +
			"hydrates it whatever the submitter declared.",
		Script: transactionDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"accountKey":{"type":"string","description":"vtx.account.<NanoID> the transaction posts to (every op; required, validated alive). ReturnDeposit additionally requires the clause's chargesTo link to name it."},` +
			`"clauseKey":{"type":"string","description":"ReturnDeposit only: vtx.clause.<NanoID> of the completed purpose=deposit clause being returned (required for ReturnDeposit; validated alive, NotADeposit / DepositNotCharged otherwise). Its own .terms.amountCents is the amount credited."},` +
			`"leaseAppKey":{"type":"string","description":"ReturnDeposit only: vtx.leaseapp.<NanoID> of the lease whose .tenancy.endedAt is the recorded end the return rides (required for ReturnDeposit; TenancyNotEnded while absent). The clause's governs link must name it."},` +
			`"amountCents":{"type":"number","description":"The transaction amount in integer cents; required by DebitAccount / LoftspaceRecordCharge / CreditAccount, must be > 0. A debit is a charge (increases what the tenant owes); a credit is a payment (decreases it). ReturnDeposit takes none — it credits the clause's own .terms.amountCents."},` +
			`"memo":{"type":"string","description":"Optional free-text description of the charge or payment (e.g. \"June rent\", \"Late fee\"). Optional."},` +
			`"clauseRef":{"type":"string","description":"DebitAccount only: vtx.clause.<NanoID> of the semantic-contract clause authorizing this charge (optional, validated alive when supplied). The clause's OWN .terms.amountCents is authoritative — a payload amountCents that disagrees is rejected (AmountMismatch). Writes the authorizedBy audit link and updates the clause's .status."},` +
			`"period":{"type":"string","description":"DebitAccount only, alongside clauseRef (Fire V3): \"monthly\" keeps the clause active instead of completing it; any other value (or omitted) marks the clause completed, the Fire V1/V2 behavior. chargeValidUntil is stamped unconditionally either way (defense-in-depth — see the DDL description)."}},` +
			`"required":["accountKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.transaction.<NanoID> of the minted transaction (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"accountKey":  "Full vtx.account.<NanoID> key the transaction posts to. Every op validates it is alive and writes the postedTo link (transaction→account) the ledgerHistory lens walks; ReturnDeposit also proves the clause's chargesTo link names it (ClauseAccountMismatch otherwise).",
			"clauseKey":   "ReturnDeposit only. Full vtx.clause.<NanoID> key of the deposit clause being returned: its .terms must carry purpose=deposit and its .status must be completed (charged). The credit's amountCents is the clause's own; the transaction is linked authorizedBy it and its .status moves to returned.",
			"leaseAppKey": "ReturnDeposit only. Full vtx.leaseapp.<NanoID> key of the lease the clause governs (ClauseLeaseMismatch otherwise); its .tenancy.endedAt must be recorded (TenancyNotEnded otherwise).",
			"amountCents": "The transaction amount in integer cents; required by DebitAccount, LoftspaceRecordCharge and CreditAccount (a positive number), never by ReturnDeposit, which credits the clause's own .terms.amountCents. Stored on the .entry aspect and projected verbatim by the ledgerHistory lens. DebitAccount with a clauseRef must match the clause's own .terms.amountCents exactly (AmountMismatch otherwise) — the clause is the authoritative amount, not the payload.",
			"memo":        "Optional free-text description of the charge or payment (e.g. \"June rent\", \"Late fee — 5 days\"). Stored on the .entry aspect when supplied; projected by the ledgerHistory lens.",
			"clauseRef":   "DebitAccount only. Full vtx.clause.<NanoID> key of the semantic-contract clause authorizing this charge. When supplied, validates the clause is alive, derives the authoritative amountCents from the clause's own .terms (rejecting AmountMismatch on disagreement with the payload), writes the authorizedBy link (transaction→clause), and updates the clause's .status per the period param.",
			"period":      "DebitAccount only, alongside clauseRef (Fire V3). \"monthly\" keeps the clause active (recurring) until its term is fully billed; anything else marks .status completed (one-time, Fire V1/V2 default). chargeValidUntil is stamped either way, unconditionally — on the anniversary grid from .terms.validFrom for a termed clause, postedAt + 30 days otherwise.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "DebitAccount — charge rent",
				Payload: map[string]any{"accountKey": "vtx.account.<NanoID>", "amountCents": 150000, "memo": "June rent"},
				ExpectedOutcome: "Validates the account is alive and amountCents > 0. Atomically commits vtx.transaction.<NanoID> " +
					"(root data {} — D5) + the .entry aspect {type: debit, amountCents: 150000, memo: \"June rent\", postedAt} " +
					"+ the postedTo link (transaction→account). Emits account.debited{accountKey, transactionKey, amountCents}. " +
					"Returns primaryKey. Rejects UnknownAccount if the account is absent, or InvalidArgument if amountCents <= 0.",
			},
			{
				Name:    "CreditAccount — record a rent payment",
				Payload: map[string]any{"accountKey": "vtx.account.<NanoID>", "amountCents": 150000, "memo": "Rent payment — check #1042"},
				ExpectedOutcome: "Same shape as DebitAccount, but writes .entry{type: credit, ...} and emits " +
					"account.credited{accountKey, transactionKey, amountCents}. A payment reduces what the tenant owes " +
					"(the ledgerHistory-derived balance = sum(debits) − sum(credits)).",
			},
			{
				Name:    "ReturnDeposit — credit the security deposit back once the tenancy has ended",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>", "clauseKey": "vtx.clause.<NanoID>", "accountKey": "vtx.account.<NanoID>"},
				ExpectedOutcome: "The clause's .terms carry purpose=deposit and amountCents 250000, its .status is completed " +
					"(DebitAccount charged it), the lease's .tenancy records endedAt, and the clause's chargesTo / governs " +
					"links name the account and lease: commits vtx.transaction.<NanoID> + .entry{type: credit, amountCents: " +
					"250000, memo: \"Security deposit returned\", postedAt} + postedTo + authorizedBy (transaction→clause), " +
					"moves .status to {state: returned, returnedAt, completedAt kept}, marks .arrears stale where present, " +
					"and emits loftspace.depositReturned. leaseRentSettlement's missing_depositReturn then closes (a " +
					"returned clause is not a candidate); a re-dispatch against the returned clause is an empty no-op. " +
					"Dispatched by Weaver's leaseRentSettlement playbook, never by a screen.",
			},
			{
				Name:    "DebitAccount — clause-authorized one-time charge (semantic-contracts Weaver dispatch)",
				Payload: map[string]any{"accountKey": "vtx.account.<NanoID>", "amountCents": 4500, "clauseRef": "vtx.clause.<NanoID>"},
				ExpectedOutcome: "Same as a plain DebitAccount, plus: validates the clause is alive, writes the authorizedBy " +
					"link (transaction→clause) and marks the clause's .status {state: completed, completedAt}. Dispatched by " +
					"Weaver's clauseSatisfaction playbook (missing_charge gap), never submitted directly by a human caller.",
			},
			{
				Name:    "DebitAccount — clause-authorized recurring charge (Fire V3, monthly)",
				Payload: map[string]any{"accountKey": "vtx.account.<NanoID>", "amountCents": 1500, "clauseRef": "vtx.clause.<NanoID>", "period": "monthly"},
				ExpectedOutcome: "Same as the one-time clause-authorized charge, but with period=\"monthly\": .status stays " +
					"{state: active}, gaining chargeValidUntil ~30 days out instead of completing. The clauseSatisfaction " +
					"lens goes non-violating (freshUntil=chargeValidUntil arms Weaver's temporal lane) until chargeValidUntil " +
					"lapses, at which point missing_charge re-opens and the next period's DebitAccount fires.",
			},
			{
				Name:    "DebitAccount — clause-authorized rent charge on a termed clause",
				Payload: map[string]any{"accountKey": "vtx.account.<NanoID>", "amountCents": 240000, "clauseRef": "vtx.clause.<NanoID>", "period": "monthly"},
				ExpectedOutcome: "The clause's .terms carry validFrom 2026-01-31T00:00:00Z / validUntil 2027-01-31T00:00:00Z and " +
					"its recorded due (.status.chargeValidUntil, read as an OptionalRead) is 2026-02-28T00:00:00Z: this charge " +
					"bills the period starting Feb 28 and re-arms chargeValidUntil to 2026-03-31T00:00:00Z — the anniversary " +
					"computed from validFrom, not Feb 28 + 1 month. The charge whose next due reaches validUntil marks the " +
					"clause {state: completed}; a due already at validUntil is refused TermExhausted.",
			},
		},
	}
}
