package loftspaceledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `account`
// (LoftspaceCreateAccount, EvaluateLoftspaceArrears), `transaction`
// (DebitAccount, LoftspaceRecordCharge, CreditAccount, ReturnDeposit,
// RecordDepositDeduction, PayOutBalance), the
// `ledgerAccountGuard` aspect-type declaration (the lease-anchored
// uniqueness guard LoftspaceCreateAccount writes), the
// `loftspaceAccountArrears` aspect-type declaration (the account's
// arrears-episode state), the `depositDeductions` aspect-type declaration
// (this package's OWN running-deduction-total aspect on a semantic-contracts
// clause — the lease-signing `leaseDeposit`-on-`leaseapp` idiom applied the
// other direction: a package owning an aspect on ANOTHER package's vertex
// type), and the notification-outcome DDL pair (notifications.go) the
// bridge replies onto.
func DDLs() []pkgmgr.DDLSpec {
	return append([]pkgmgr.DDLSpec{
		accountDDL(),
		accountGuardAspectTypeDDL(),
		accountArrearsAspectTypeDDL(),
		transactionDDL(),
		depositDeductionsAspectTypeDDL(),
	}, notificationDDLs()...)
}

func accountDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: "account",
		Class:         "meta.ddl.vertexType",
		// CreditAccount, PayOutBalance and LinkReversal ALSO touch this list:
		// each writes a bare, content-unchanged update of the account ROOT as
		// a CAS idempotency anchor for a race no other shared key catches —
		// the resident's capped self-credit (CreditAccount), the whole of
		// PayOutBalance, and two LinkReversals naming different charges for
		// one credit. See make_vtx_update's call sites in
		// transactionDDLScript for why.
		PermittedCommands: []string{"LoftspaceCreateAccount", "CreditAccount", "PayOutBalance", "LinkReversal", arrearsOp},
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
			"the head is computed exactly whatever the history's length — ages it with the same FIFO the tenant's statement runs " +
			"(a credit that names the charge it reverses — the reverses link CreditAccount's reversesRef or LinkReversal " +
			"writes — retires THAT charge, capped at its face, at the credit's own position in the walk; every other " +
			"credit offsets the oldest still-open charge first; an unapplied credit carries forward as surplus), and " +
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
			"receives. On the same send commit — and no other — it bills the lease's LATE FEE: it walks the " +
			"account's inbound chargesTo links for the live clause whose .terms.purpose is lateFee and .status.state " +
			"is active (semantic-contracts mints it from the lease's .lateFee term, period perArrearsEpisode) and, " +
			"with one found, posts a debit of that clause's own amountCents in the same batch — vtx.transaction.<NanoID> " +
			"+ .entry {type: debit, amountCents, postedAt: evaluatedAt, dueAt: evaluatedAt, memo: \"Late fee — rent due " +
			"<dueAt's UTC day>\"} + postedTo + authorizedBy the clause, the shape DebitAccount posts, + a billedFor link " +
			"to the head charge (fee → charge; the ledgerHistory and one-bill rows project it as billedForKey, so a " +
			"reversal of that charge is tied to the fee it leaves owed) — and records lateFeeAt = evaluatedAt on " +
			".arrears, carried and dropped exactly as sentAt is. The notification's balanceCents is the balance the " +
			"commit LEAVES (the fee included) and lateFeeCents the fee itself. Once per episode, because the send " +
			"is: a re-evaluation finds sentAt recorded and bills nothing; a fee term set after the reminder went " +
			"out — or an amendment committing concurrently with the send — bills from the next episode; no fee " +
			"clause, no fee; with several live fee clauses the greatest clause key bills (the settlement lens's own " +
			"max). The fee debit is due on receipt and sits behind the rent in the FIFO, so paying the rent alone " +
			"leaves the fee as the head of the SAME episode — no second reminder, no second fee. A fee bills on an " +
			"ended tenancy's arrears exactly as the reminder goes out on them: the account is evaluated whatever " +
			"the lease's state, and the clause stays active; only NEW fee terms stop at the tenancy's end " +
			"(SetLateFee refuses TenancyEnded, the settlement lens mints none).",
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
					"link to a live identity — and, where a live purpose=lateFee clause charges the account, posts that " +
					"clause's amount as a debit authorizedBy it in the same batch and stamps lateFeeAt. A re-run recomputes " +
					"the head, finds sentAt already recorded, and sends (and bills) nothing. " +
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
//     never its full vtx key, and carrying its own recorded postedAt, plus —
//     on a credit that reverses a charge — the bare id of that charge; no
//     total to collapse into) — the exact rows the FIFO head is computed from once the
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
//     ReturnDeposit / PayOutBalance against an account that carries the
//     aspect marks it stale (carrying every other
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
		PermittedCommands: []string{"DebitAccount", "LoftspaceRecordCharge", "CreditAccount", "ReturnDeposit", "PayOutBalance", "LinkReversal", arrearsOp},
		Description: "Per-account arrears-episode aspect. Stored as vtx.account.<NanoID>.arrears " +
			"(class loftspaceAccountArrears) = {evaluatedAt, dueAt?, remindAt?, remindedFor?, sentAt?, lateFeeAt?, stale?, historyTooLong?, historyBudget?, replay?}. " +
			"Non-sensitive. dueAt is the FIFO-oldest still-open charge's OWN recorded due date (the .entry.dueAt a " +
			"clause-authorized rent charge carries from its anniversary grid), or its postedAt when it recorded none " +
			"(a landlord one-off is due on receipt) — a RECORDED time fact read as the fact it records, never a term " +
			"added to the posting and never a clock a lens reads; it is what \"N days overdue\" counts from. remindAt " +
			"is dueAt plus the package's grace (ArrearsGraceDays, 5 days) — where the reminder timer arms and what the " +
			"recorded lapse is compared against. remindedFor names the dueAt the evaluation has acknowledged as past " +
			"its grace — it is what closes the convergence gap; sentAt is the instant the reminder's outbox event was " +
			"committed (the SEND INTENT — the adapter's delivery outcome is .arrearsNotification), and its ABSENCE is " +
			"the send condition, which is what makes the notification once-per-EPISODE rather than once-per-head or " +
			"once-per-convergence-window. lateFeeAt is the instant the episode's late fee was billed — stamped on the " +
			"send commit alone, when a live purpose=lateFee clause charges the account, and carried and dropped " +
			"exactly as sentAt is (the fee is once per episode because the send is). historyTooLong means the account's history outran the evaluation's replay " +
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
			"this ledger stores no balance for an entry to reason from, and so does LinkReversal, which changes what " +
			"an already-read credit retires — and is a request for a fresh " +
			"EvaluateLoftspaceArrears, which rewrites the aspect and so never carries it forward. Written by " +
			"DebitAccount / LoftspaceRecordCharge / CreditAccount / ReturnDeposit / PayOutBalance / LinkReversal (mark stale; drop any checkpoint; mint nothing where absent) and " +
			"EvaluateLoftspaceArrears (recomputes the head; ends the episode at {evaluatedAt} alone when nothing is " +
			"owed, and drops a send record that predates the charge that opened the episode it finds — the boundary between an episode paid " +
			"off and the next one opened before any evaluation ran). Read by the loftspaceArrearsReminders " +
			"convergence lens and projected for the landlord ledger, the tenant statement and the portfolio list by " +
			"leaseAccounts. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"evaluatedAt":{"type":"string"},"dueAt":{"type":"string"},"remindAt":{"type":"string"},"remindedFor":{"type":"string"},"sentAt":{"type":"string"},"lateFeeAt":{"type":"string"},"stale":{"type":"boolean"},"historyTooLong":{"type":"boolean"},"historyBudget":{"type":"integer"},"replay":{"type":"object","properties":{"phase":{"type":"string","enum":["` + ArrearsPhaseA + `","` + ArrearsPhaseB + `"]},"cursor":{"type":"string"},"pages":{"type":"integer"},"entries":{"type":"object"}}}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"evaluatedAt":    "RFC3339 instant (canonical UTC) the arrears state was last written by an evaluation. Its ABSENCE is what opens the convergence gap for an account nothing has ever evaluated.",
			"dueAt":          "RFC3339 instant (canonical UTC) the FIFO-oldest still-open charge fell due: that charge's own recorded .entry.dueAt, or its postedAt when it recorded none. What the statement counts \"N days overdue\" from. Absent when the account owes nothing.",
			"remindAt":       "RFC3339 instant (canonical UTC) the reminder is armed for: dueAt plus the package's grace (5 days). The convergence lens arms its timer here and compares the recorded lapse against it. Absent when the account owes nothing.",
			"remindedFor":    "The dueAt the last evaluation acknowledged as past its grace. Equal to dueAt closes the convergence gap; different (or absent) leaves it open for a recorded lapse to re-open.",
			"sentAt":         "RFC3339 instant (canonical UTC) the reminder's outbox event was committed for this arrears episode — the send intent the landlord ledger and the tenant's statement show. Its ABSENCE is what lets the next passed reminder instant send; it is carried across every write of a live episode and dropped only by the evaluation that finds the episode over: no open charge, or an episode whose opening charge posted after this instant (the balance returned to zero and a new charge opened a fresh episode before an evaluation ran); a head that a partial payment moved past the opener stays in the same episode and keeps it.",
			"lateFeeAt":      "RFC3339 instant (canonical UTC) this arrears episode's late fee was billed — the send commit that also stamped sentAt, when a live purpose=lateFee clause charged the account (on an ended tenancy's arrears as on a live one's). The fee debit itself is the transaction posted at that instant, authorizedBy the clause and billedFor the head charge. Carried across every write of a live episode and dropped exactly where sentAt is (the evaluation that finds the episode over, or a send record that predates the episode's opener); absent beside a present sentAt on an episode reminded for before the lease had a fee term (which bills from the next episode) or whose fee clause sat past the chargesTo page bound.",
			"stale":          "True when what is recorded may no longer describe the account — every posted entry sets it, since the ledger stores no balance to reason from, and LinkReversal sets it because the credit it ties to a charge now retires that charge instead of the oldest open one. Opens the convergence gap; cleared by the evaluation that recomputes the head.",
			"historyTooLong": "True when the account's postedTo history outran the evaluation's bounded replay budget, so no FIFO head could be computed. Suppresses the freshness timer, and — with a historyBudget at least the current budget — the convergence gap: the row stays in the read model for an operator to see, without re-dispatching an evaluation that cannot succeed. Dropped by the next posted entry (which also marks the state stale), buying exactly one more attempt.",
			"historyBudget":  "The replay budget, in postedTo entries, the degraded evaluation exhausted (the package's page size × page cap at the time). A recorded budget smaller than the current one — or none — re-opens the convergence gap for exactly one evaluation under the current budget, so a raised budget reaches the accounts the old one parked. Written only beside historyTooLong and dropped with it.",
			"replay":         "The checkpoint of an evaluation part-way through a history longer than one page of its postedTo replay: {phase: '" + ArrearsPhaseA + "'|'" + ArrearsPhaseB + "', cursor, pages, entries: {txId: {postedAt, type, amountCents, dueAt, reversesId?}}}. phase flips on every page and is the lens's continuation trigger (one gap per phase); cursor resumes the enumeration; pages counts those consumed; entries carries every debit and credit read so far, keyed by its bare transaction ID (never its full vtx key — the checkpoint carries an identity to re-derive from, not a relationship to stand in for one) and its own recorded postedAt — the exact rows the FIFO head is computed from once the enumeration is exhausted, no total (the episode-start computation needs each entry's own real timing, not a collapsed sum); dueAt is carried because arrears_head reads it off every debit row to name the head's own recorded due date; reversesId is the bare id of the charge a credit's reverses link names, carried so the head's netting pre-pass retires that charge rather than the oldest open one. Present only between the first page and the last — the finalize page and the degrade write the aspect without it, and every posted entry drops it, because a posted entry changes the set under the cursor. A checkpoint the evaluation cannot resume (a malformed field) is treated as absent: the evaluation restarts at page 1.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "account arrears aspect — overdue past the grace, reminded once",
				Payload:         map[string]any{"evaluatedAt": "2026-09-15T09:00:00Z", "dueAt": "2026-09-08T00:00:00Z", "remindAt": "2026-09-13T00:00:00Z", "remindedFor": "2026-09-08T00:00:00Z", "sentAt": "2026-09-15T09:00:00Z", "lateFeeAt": "2026-09-15T09:00:00Z"},
				ExpectedOutcome: "Stored as vtx.account.<NanoID>.arrears; written by EvaluateLoftspaceArrears on the commit that also emitted the notification and posted the lease's late fee (the account carried a live purpose=lateFee clause). remindedFor = dueAt closes the gap, so no second reminder — and no second fee — goes out for this episode.",
			},
		},
	}
}

func transactionDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: "transaction",
		Class:         "meta.ddl.vertexType",
		// EvaluateLoftspaceArrears (the account DDL's op) mints a transaction
		// too: the late-fee debit it posts on the commit that sends the
		// reminder, the clause-authorized shape DebitAccount posts, so this
		// gate admits it.
		PermittedCommands: []string{"DebitAccount", "CreditAccount", "LoftspaceRecordCharge", "ReturnDeposit", "RecordDepositDeduction", "PayOutBalance", "LinkReversal", arrearsOp},
		Description: "Ledger transaction DDL. Vertex shape: vtx.transaction.<NanoID>, class=transaction, root data = {} " +
			"(minimal, D5 — the entry detail is a .entry aspect). DebitAccount{accountKey, amountCents, memo?, clauseRef?, " +
			"period?} records a charge (rent, a late fee, a deposit) — the orchestrated shape, operator-only, that " +
			"Weaver's clauseSatisfaction playbook dispatches with a clauseRef; LoftspaceRecordCharge{accountKey, " +
			"amountCents, memo?} records the same debit entry as a person's manual charge (no clauseRef/period — a " +
			"vertical-unique name because operationType is a global namespace and cafe-ledger admits its own " +
			"DebitAccount); CreditAccount{accountKey, amountCents, memo?, reversesRef?} " +
			"records a payment received — or, with reversesRef naming a live debit posted to the same account, the " +
			"landlord's reversal of that charge: amountCents may not exceed the charge's face (ReversalExceedsCharge), " +
			"the target must be a debit (NotADebit) on this account (WrongAccount; UnknownTransaction when not live) and never " +
			"the security deposit's charge (DepositNotReversible, read off its authorizedBy clause's purpose — the deposit is " +
			"deducted from or returned, never reversed), " +
			"a resident's self-scoped credit may not carry it (AuthDenied) and a debit op never may (InvalidArgument); " +
			"the batch then writes the reverses link (credit→debit, the credit is the later-arriving vertex so it is " +
			"the source — Contract #1 §1.1), which the arrears evaluation and the ledgerHistory lens read to retire " +
			"THAT charge rather than the oldest open one. LinkReversal{accountKey, creditKey, reversesRef} " +
			"(operator-only, no screen) writes the same link create-only onto a credit posted naming nothing — " +
			"both live and posted to the payload account, the credit a credit (NotACredit), the target a debit, the " +
			"credit within the charge's face, the credit not posted strictly before the charge (ReversalPrecedesCharge; a " +
			"same-second pair is admitted) and the credit reversing nothing yet (AlreadyLinked) — serialized on the account " +
			"root's bare update so two links racing one credit cannot both land — and marks " +
			".arrears stale like a posted entry, since what the credit retires has changed. " +
			"ReturnDeposit{leaseAppKey, clauseKey, accountKey} (below) credits a " +
			"charged security deposit back once the tenancy has ended. Each mints a fresh vtx.transaction.<NanoID> + a .entry aspect {type " +
			"(debit|credit|deduction), kind?, amountCents, memo?, postedAt, periodStart?, periodEnd?, dueAt?} + the postedTo link (transaction→account, the transaction " +
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
			"the clause's own full amount; the clause's .status must be completed, the state DebitAccount's one-time charge " +
			"leaves (DepositNotCharged while still active — an uncharged deposit is never refunded); the lease's " +
			".tenancy must record endedAt (TenancyNotEnded otherwise — the recorded end, never the notice or the " +
			"term; UnknownLeaseApplication for a lease that is not live); and the clause's own deterministic " +
			"chargesTo / governs links must name the payload account and lease (ClauseAccountMismatch / " +
			"ClauseLeaseMismatch). Only then is a clause already returned an idempotent no-op (empty mutations, no " +
			"event) — a mis-addressed submit is refused, never read as done. Otherwise it credits the NET of the " +
			"clause's full amount less whatever RecordDepositDeduction has already taken off it, as recorded on " +
			"this package's own .deductions aspect on the clause (absent = never deducted): a positive net mints vtx.transaction.<NanoID> + .entry {type: credit, " +
			"amountCents: net, postedAt, memo: \"Security deposit returned\"} + the postedTo link + the authorizedBy link " +
			"(transaction→clause, the same chain of custody the charge recorded) and marks .arrears stale like every " +
			"other entry; a net of ZERO (the deposit fully deducted) mints NO transaction and no links at all — a " +
			"zero-amount entry is not a transaction — the event still fires with amountCents 0 and no transactionKey, " +
			"and the response carries no primaryKey. Either way the clause's .status moves to " +
			"{state: returned, returnedAt: postedAt, ...every field kept} pinned to the revision it hydrated at, and writes .deductions (the hydrated data unchanged when present, {totalCents: 0, count: 0} when absent) as the serialization anchor against RecordDepositDeduction. " +
			"Emits loftspace.depositReturned{accountKey, clauseKey, leaseAppKey, amountCents, transactionKey?}. " +
			"RecordDepositDeduction{accountKey, clauseKey, amountCents, reason} (operator, or the landlord's " +
			"consumer scope=self — the same self_scope_standing proof post_entry runs, a resident refused AuthDenied) " +
			"takes a deduction off a charged, still-held deposit clause before it is returned: it proves the same " +
			"custody (chargesTo / purpose=deposit / oneTime / computational — ClauseAccountMismatch / NotADeposit) " +
			"and .status completed (DepositNotHeld otherwise — active means not yet charged, returned means already " +
			"gone), refuses DeductionExceedsDeposit once the running total on its own .deductions aspect (depositDeductions DDL) plus this amount would " +
			"exceed the clause's own amountCents, and mints vtx.transaction.<NanoID> + .entry {type: deduction, " +
			"amountCents, postedAt, memo: reason} + the postedTo link + the authorizedBy link (the same chain of " +
			"custody), plus a CREATE (first deduction) or BARE UPDATE (later ones; never expectedRevision-pinned, so " +
			"two concurrent deductions both land under the §3.2 re-hydrate retry) of the clause's OWN .deductions " +
			"aspect (depositDeductions DDL, this package — never semantic-contracts' clauseStatus). A deduction is neither a debit " +
			"nor a credit — every balance reader that tests the type explicitly (the resident self-credit walk, the " +
			"arrears FIFO) ignores it, and it marks NO .arrears stale (it moves no FIFO). Emits " +
			"loftspace.depositDeducted{accountKey, transactionKey, clauseKey, amountCents}. " +
			"PayOutBalance{accountKey, leaseAppKey} (operator, or the landlord's consumer scope=self, same proof) " +
			"pays an ended tenancy's whole credit balance out to the tenant: the amount is computed OP-SIDE from the " +
			"account's own postedTo history (the same recomputation the resident self-credit cap uses, extracted as " +
			"account_balance_cents), never trusted from the payload — HistoryTooLong on an exhausted replay budget, " +
			"never a partial sum. It proves the account's own heldFor link names the payload lease " +
			"(AccountLeaseMismatch otherwise), the lease's .tenancy records endedAt (TenancyNotEnded otherwise), and " +
			"refuses NoCreditBalance once the recomputed balance is not negative. It mints vtx.transaction.<NanoID> + " +
			".entry {type: debit, kind: payout, amountCents: the credit balance's magnitude, postedAt, memo: " +
			"\"Balance paid out to the tenant\"} + the postedTo link (no authorizedBy — no clause authorizes it) and " +
			"marks .arrears stale like every other debit. Emits loftspace.balancePaidOut{accountKey, transactionKey, " +
			"leaseAppKey, amountCents}. kind is the recorded PROVENANCE of a debit no clause authorizes — the " +
			"ledgerHistory and one-bill rentEntries lenses project it so a statement labels the row by this recorded " +
			"fact, never by its (editable) memo. The DDL's own " +
			"derive_reads hydrates every key each custody-checking op reads (the account and its " +
			".arrears, the clause and its .terms, .status and .deductions, the lease and its .tenancy, and the deterministic " +
			"custody links each op's own payload keys can address) whatever the submitter declared. " +
			"Every entry, from any of the six ops but RecordDepositDeduction, ALSO marks the account's .arrears episode state " +
			"(loftspaceAccountArrears DDL) stale where it exists — carrying every other field, the episode's send " +
			"record included — and mints nothing where it does not: with no stored balance an entry cannot tell an " +
			"episode opening from one continuing, so it asks EvaluateLoftspaceArrears to recompute rather than " +
			"guess. RecordDepositDeduction marks nothing — a deduction moves no FIFO. A .arrears document of any other class is refused (InvalidState) rather than carried. The write " +
			"is a bare update auto-conditioned on the revision the key hydrated at, and the DDL's own derive_reads " +
			"hydrates it whatever the submitter declared.",
		Script: transactionDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"accountKey":{"type":"string","description":"vtx.account.<NanoID> the transaction posts to (every op; required, validated alive). ReturnDeposit / RecordDepositDeduction additionally require the clause's chargesTo link to name it; PayOutBalance additionally requires the account's own heldFor link to name the payload leaseAppKey."},` +
			`"clauseKey":{"type":"string","description":"ReturnDeposit / RecordDepositDeduction only: vtx.clause.<NanoID> of the purpose=deposit clause (required there; validated alive, NotADeposit otherwise). ReturnDeposit additionally requires .status completed (DepositNotCharged otherwise); RecordDepositDeduction additionally requires it (DepositNotHeld otherwise) and refuses DeductionExceedsDeposit once the running deduction total would exceed its .terms.amountCents."},` +
			`"leaseAppKey":{"type":"string","description":"ReturnDeposit / PayOutBalance only: vtx.leaseapp.<NanoID> of the lease whose .tenancy.endedAt is the recorded end the return/payout rides (required there; TenancyNotEnded while absent). ReturnDeposit's clause must govern it; PayOutBalance's account must be heldFor it (AccountLeaseMismatch otherwise)."},` +
			`"amountCents":{"type":"number","description":"The transaction amount in integer cents; required by DebitAccount / LoftspaceRecordCharge / CreditAccount / RecordDepositDeduction, must be > 0. A debit is a charge (increases what the tenant owes); a credit is a payment (decreases it); a deduction moves custody without moving what is owed. ReturnDeposit and PayOutBalance take none — both compute their own amount from the graph's own record."},` +
			`"memo":{"type":"string","description":"Optional free-text description of the charge or payment (e.g. \"June rent\", \"Late fee\"). Optional."},` +
			`"reversesRef":{"type":"string","description":"CreditAccount only: vtx.transaction.<NanoID> of the live debit this credit reverses, posted to the same account (WrongAccount otherwise; UnknownTransaction when not live; NotADebit on a credit; DepositNotReversible on the security deposit's charge). amountCents may not exceed its face (ReversalExceedsCharge). Refused on a resident's self-scoped credit (AuthDenied) and on any debit op (InvalidArgument). Writes the reverses link."},` +
			`"creditKey":{"type":"string","description":"LinkReversal only: vtx.transaction.<NanoID> of the live credit, posted to accountKey, that the link ties to reversesRef (NotACredit otherwise; AlreadyLinked once it reverses anything)."},` +
			`"reason":{"type":"string","description":"RecordDepositDeduction only, required, 1-200 characters: why the deduction was taken (e.g. \"Carpet cleaning\"). Recorded as the transaction's own memo — the line the statement shows for this deduction."},` +
			`"clauseRef":{"type":"string","description":"DebitAccount only: vtx.clause.<NanoID> of the semantic-contract clause authorizing this charge (optional, validated alive when supplied). The clause's OWN .terms.amountCents is authoritative — a payload amountCents that disagrees is rejected (AmountMismatch). Writes the authorizedBy audit link and updates the clause's .status."},` +
			`"period":{"type":"string","description":"DebitAccount only, alongside clauseRef (Fire V3): \"monthly\" keeps the clause active instead of completing it; any other value (or omitted) marks the clause completed, the Fire V1/V2 behavior. chargeValidUntil is stamped unconditionally either way (defense-in-depth — see the DDL description)."}},` +
			`"required":["accountKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.transaction.<NanoID> of the minted transaction (the operation's principal key). Absent on a zero-net ReturnDeposit, which mints no transaction at all."}}}`,
		FieldDescription: map[string]string{
			"accountKey":  "Full vtx.account.<NanoID> key the transaction posts to. Every op validates it is alive and writes the postedTo link (transaction→account) the ledgerHistory lens walks; ReturnDeposit/RecordDepositDeduction also prove the clause's chargesTo link names it (ClauseAccountMismatch otherwise); PayOutBalance proves its own heldFor link names the payload lease (AccountLeaseMismatch otherwise).",
			"clauseKey":   "ReturnDeposit / RecordDepositDeduction only. Full vtx.clause.<NanoID> key of the deposit clause: its .terms must carry purpose=deposit and its .status must be completed (charged, not yet returned). ReturnDeposit credits the net of its amountCents less the clause's own .deductions.totalCents and moves .status to returned; RecordDepositDeduction adds to .deductions.totalCents (DeductionExceedsDeposit past the clause's own amountCents).",
			"leaseAppKey": "ReturnDeposit / PayOutBalance only. Full vtx.leaseapp.<NanoID> key of the lease: ReturnDeposit's clause must govern it (ClauseLeaseMismatch otherwise); PayOutBalance's account must be heldFor it (AccountLeaseMismatch otherwise). Either way its .tenancy.endedAt must be recorded (TenancyNotEnded otherwise).",
			"amountCents": "The transaction amount in integer cents; required by DebitAccount, LoftspaceRecordCharge, CreditAccount and RecordDepositDeduction (a positive number), never by ReturnDeposit or PayOutBalance, which both compute their own amount from the graph's own record. Stored on the .entry aspect and projected verbatim by the ledgerHistory lens. DebitAccount with a clauseRef must match the clause's own .terms.amountCents exactly (AmountMismatch otherwise) — the clause is the authoritative amount, not the payload.",
			"memo":        "Optional free-text description of the charge or payment (e.g. \"June rent\", \"Late fee — 5 days\"). Stored on the .entry aspect when supplied; projected by the ledgerHistory lens.",
			"reversesRef": "CreditAccount (the landlord's or the operator's, never a resident's — AuthDenied) and LinkReversal. Full vtx.transaction.<NanoID> key of the live debit the credit reverses: it must be a debit (NotADebit) posted to the payload account (WrongAccount), not the security deposit's charge (DepositNotReversible — read off the charge's authorizedBy clause; the deposit is deducted from or returned, never reversed), and the credit's amountCents may not exceed its face (ReversalExceedsCharge). LinkReversal further refuses a credit that posted strictly before the charge (ReversalPrecedesCharge). Written as the reverses link (credit→debit) the arrears evaluation, the statement and the ledgerHistory lens read.",
			"creditKey":   "LinkReversal only. Full vtx.transaction.<NanoID> key of the live credit being tied to the charge it reverses: it must be a credit (NotACredit) posted to the payload account (WrongAccount) that reverses nothing yet (AlreadyLinked otherwise).",
			"reason":      "RecordDepositDeduction only, required, 1-200 characters. Stored verbatim as the deduction transaction's own .entry.memo — the statement's own line for the deduction, with no separate tag hiding it.",
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
				Name:    "CreditAccount — reverse a charge",
				Payload: map[string]any{"accountKey": "vtx.account.<NanoID>", "amountCents": 205000, "memo": "Reversal: rent billed after the lease end", "reversesRef": "vtx.transaction.<NanoID>"},
				ExpectedOutcome: "reversesRef is a live debit of 205000 posted to this account, the caller is the operator or the " +
					"managing landlord, and 205000 does not exceed its face: commits the credit exactly as a payment would " +
					"PLUS the reverses link lnk.transaction.<creditId>.reverses.transaction.<debitId>, and marks .arrears " +
					"stale. The arrears evaluation and the statement then retire THAT charge with this credit, leaving " +
					"an older open charge as the head. Rejects ReversalExceedsCharge above the face, WrongAccount for a " +
					"charge on another account, NotADebit for a credit, UnknownTransaction for a dead key, AuthDenied on " +
					"a resident's self-scoped submit.",
			},
			{
				Name:    "LinkReversal — tie a credit posted naming nothing to the charge it corrects",
				Payload: map[string]any{"accountKey": "vtx.account.<NanoID>", "creditKey": "vtx.transaction.<NanoID>", "reversesRef": "vtx.transaction.<NanoID>"},
				ExpectedOutcome: "Both transactions are live and posted to the account, the credit is a credit within the debit's " +
					"face, and it reverses nothing yet: commits the reverses link create-only and marks .arrears stale, " +
					"emitting account.reversalLinked{accountKey, creditKey, reversesKey}. Rejects AlreadyLinked once " +
					"the credit names a charge, NotACredit / NotADebit on the wrong entry types, WrongAccount when " +
					"either is posted elsewhere, ReversalExceedsCharge above the face.",
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
			{
				Name:    "RecordDepositDeduction — take a damage deduction off a held deposit",
				Payload: map[string]any{"accountKey": "vtx.account.<NanoID>", "clauseKey": "vtx.clause.<NanoID>", "amountCents": 25000, "reason": "Carpet cleaning"},
				ExpectedOutcome: "The clause's .terms carry purpose=deposit/oneTime/computational and amountCents 250000, its .status " +
					"is completed with no .deductions aspect recorded yet: commits vtx.transaction.<NanoID> + .entry{type: deduction, " +
					"amountCents: 25000, memo: \"Carpet cleaning\", postedAt} + postedTo + authorizedBy (transaction→clause), " +
					"and creates the clause's .deductions aspect at {totalCents: 25000, count: 1} (no .arrears mark — a deduction moves no " +
					"FIFO). Emits loftspace.depositDeducted. A second deduction of 230000 would be refused " +
					"DeductionExceedsDeposit (25000 + 230000 > 250000).",
			},
			{
				Name:    "PayOutBalance — pay an ended tenancy's credit balance out to the tenant",
				Payload: map[string]any{"accountKey": "vtx.account.<NanoID>", "leaseAppKey": "vtx.leaseapp.<NanoID>"},
				ExpectedOutcome: "The account is heldFor the payload lease, the lease's .tenancy records endedAt, and the account's " +
					"own postedTo history recomputes to a credit balance of -22500 (225.00 owed back): commits " +
					"vtx.transaction.<NanoID> + .entry{type: debit, kind: payout, amountCents: 22500, memo: \"Balance paid out " +
					"to the tenant\", postedAt} + postedTo (no authorizedBy — no clause authorizes it), marks .arrears stale. " +
					"Emits loftspace.balancePaidOut. Rejects NoCreditBalance once the recomputed balance is not negative " +
					"(a re-submit after this one pays the balance to zero).",
			},
		},
	}
}

// depositDeductionsAspectTypeDDL declares the .deductions aspect (class
// depositDeductions) — the running deduction total loftspace-ledger keeps
// on a semantic-contracts CLAUSE, the lease-signing leaseDeposit-on-leaseapp
// idiom (packages/lease-signing/ddls.go, leaseDepositAspectDDL) applied the
// other direction: the writer owns the aspect it writes, never the vertex
// type it sits on. Kept off the clause's own .status (semantic-contracts'
// clauseStatus) on purpose — PermittedCommands is a commit-time gate
// (step6_validate.go), not documentation, so RecordDepositDeduction writing
// clauseStatus would need admitting there, and that operationType also
// carries a consumer scope=self grant (permissions.go): the S9 hazard
// (lint-package-standard) a foreign package's script was never meant to
// authorize.
//
// BOTH RecordDepositDeduction and ReturnDeposit write here, and that is the
// point, not an accident: the two ops share no other written key —
// RecordDepositDeduction reads .status and writes .deductions, ReturnDeposit
// reads .deductions and writes .status, so a boundary-time deduction could
// land on an already-returned clause, or a return could credit a total a
// concurrent deduction was mid-flight on. ReturnDeposit writes .deductions
// too — a BARE update carrying the hydrated data unchanged when present
// (Contract #3 §3.2: a deduction that lands first conflicts this write and
// the platform re-hydrates/re-executes/re-commits against the fresh total),
// or a CREATE of {totalCents: 0, count: 0, lastRecordedAt} when absent
// (Contract #2 §2.5's absentConditionedCreates: retry-eligible the same way
// against a concurrent first deduction's own create) — making this aspect
// the SERIALIZATION ANCHOR the two ops share, not merely a running total.
// RecordDepositDeduction still owns the actual accumulation (CREATE on the
// first deduction, BARE UPDATE on every later one): totalCents accumulates,
// count increments, lastRecordedAt is the most recent deduction's own
// postedAt; it also refuses DeductionExceedsDeposit off the total read here.
// Non-sensitive: two dollar figures, a count and a timestamp, no PII. Never
// tombstoned (a deposit clause is never tombstoned).
func depositDeductionsAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "depositDeductions",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"RecordDepositDeduction", "ReturnDeposit"},
		Description: "Running security-deposit-deduction total AND cross-op serialization anchor (loftspace-ledger). " +
			"Stored as vtx.clause.<NanoID>.deductions (class depositDeductions) = {totalCents, count, lastRecordedAt} " +
			"on a semantic-contracts purpose=deposit clause. RecordDepositDeduction creates it on the FIRST deduction " +
			"taken off a charged, still-held deposit clause, then updates it (a bare, never expectedRevision-pinned " +
			"write, so two concurrent deductions both land exact under the platform's own re-hydrate retry) on every " +
			"later one: totalCents accumulates, count increments, lastRecordedAt is the most recent deduction's own " +
			"postedAt; it also refuses DeductionExceedsDeposit once totalCents plus the new amount would exceed the " +
			"clause's own terms.amountCents. ReturnDeposit reads it to credit the NET — terms.amountCents minus " +
			"totalCents, absent = never deducted — and ALSO writes it (a bare update of the unchanged data when " +
			"present, a create of {totalCents: 0, count: 0} when absent): with .status and .deductions on separate " +
			"keys, this write is what makes a deduction racing a return serialize through a shared conditioned key " +
			"instead of both landing independently. Declaration-only: no op handler of its own.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"totalCents":{"type":"integer"},"count":{"type":"integer"},"lastRecordedAt":{"type":"string"}},"required":["totalCents","count","lastRecordedAt"]}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"totalCents":     "The running total, in integer cents, deducted off the deposit clause across every RecordDepositDeduction so far. ReturnDeposit credits the clause's own terms.amountCents less this figure.",
			"count":          "How many deductions have posted against this clause. Audit/display only — no gate reads it.",
			"lastRecordedAt": "RFC3339 instant (canonical UTC) of the most recent deduction's own postedAt — or, on the {totalCents: 0, count: 0} record ReturnDeposit creates for a never-deducted deposit, the return's own postedAt.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "deposit deductions aspect — one deduction recorded",
				Payload:         map[string]any{"totalCents": 25000, "count": 1, "lastRecordedAt": "2027-06-15T09:00:00Z"},
				ExpectedOutcome: "Created (op:create) by RecordDepositDeduction on the clause's first deduction. A second deduction of 10000 updates it (op:update, bare) to {totalCents: 35000, count: 2, lastRecordedAt: <the second deduction's postedAt>}.",
			},
		},
	}
}
