package wellnessledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `wellnessaccount`
// (WellnessCreateAccount, EvaluateWellnessArrears), `wellnesstransaction`
// (WellnessDebitAccount, WellnessCreditAccount), the
// `wellnessLedgerAccountGuard` aspect-type declaration (the identity-anchored
// uniqueness guard WellnessCreateAccount writes), the `wellnessAccountArrears`
// aspect-type declaration (the account's arrears-episode state), and the
// notification-outcome DDL pair (notifications.go) the bridge replies onto.
// Vertical-prefixed: a DDL canonicalName is global across every installed
// package (internal/pkgmgr/installer.go checkCanonicalNameCollision), and
// loftspace-ledger already owns the bare `account` / `transaction` names.
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
		CanonicalName:     "wellnessaccount",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"WellnessCreateAccount", arrearsOp},
		Description: "Ledger account DDL. Vertex shape: vtx.wellnessaccount.<NanoID>, class=wellnessaccount, root data = {} " +
			"(minimal, D5 — the balance is LENS-derived by summing transactions, never stored). WellnessCreateAccount{identityKey} " +
			"mints the account under its OWN independently-generated NanoID (never reused from the identity — Core KV " +
			"NanoIDs are unique platform-wide identifiers, not scoped per vertex type). \"One account per member\" is " +
			"enforced by a deterministic create-only guard aspect on the IDENTITY (identityKey+\".wellnessLedgerAccount\", " +
			"wellnessLedgerAccountGuard DDL) instead: a second WellnessCreateAccount for the same identity conflicts on that " +
			"already-existing aspect key. Writes the heldFor link (account→identity, the account is the later-arriving " +
			"vertex so it is the source — Contract #1 §1.1). Requires the identityKey be a live identity (no orphan accounts). " +
			"EvaluateWellnessArrears{accountKey} is the second operation on this DDL, dispatched by " +
			"Weaver's wellnessArrearsReminders playbook rather than by a person: it replays the account's own postedTo " +
			"history ONE PAGE PER DISPATCH (ArrearsPageLimit entries, scripts.go), ages it with the same FIFO the " +
			"member's statement runs (a refund credit retires the charge its wellnessrefund marker reverses; every " +
			"other credit offsets the oldest still-open charge first; an unapplied credit carries forward as " +
			"surplus), and records the resulting due date — the oldest open charge's postedAt plus the package's " +
			"net term — on the account's .arrears aspect (wellnessAccountArrears DDL). A history longer than one " +
			"page records its running aggregate and the cursor to resume from as a checkpoint on .arrears.replay, " +
			"and Weaver dispatches the next page through the lens's two phase gaps, so any history up to " +
			"ArrearsPageLimit × ArrearsMaxPages entries is reached exactly across as many dispatches as it has " +
			"pages, chained at the row's own re-evaluation cadence. Once the enumeration is exhausted and the due " +
			"date has passed the evaluation records remindedFor = that date, " +
			"and where NO reminder has yet gone out in this arrears episode (sentAt ABSENT) the same commit also " +
			"stamps sentAt and fires an external.notification to the bridge's \"notification\" adapter keyed on " +
			"(accountKey, dueAt). The send condition is sentAt's absence, not remindedFor's value: the unit is the " +
			"EPISODE — from the charge that took the account from square to owing until the balance returns to zero — " +
			"and a partial payment moves the head from one overdue charge to the next without starting a new episode, " +
			"so exactly ONE notification goes out per episode however often the evaluation is re-dispatched, " +
			"redelivered, or re-run over a moved head. A history that nets to nothing owed rewrites the aspect to " +
			"{evaluatedAt} alone — this evaluation is the ONLY thing that ends an episode, since the ledger stores " +
			"no balance for a posted entry to see reach zero — and where a payment to zero and a fresh charge both " +
			"posted before it ran, the recorded send predates the charge that opened the new episode (a reminder " +
			"only ever goes out for a head a whole term old), so the evaluation drops remindedFor/sentAt as the " +
			"finished episode's and the " +
			"new one is reminded for on its own merits. An account whose history outruns the replay budget (past " +
			"ArrearsMaxPages pages) " +
			"is not refused: the evaluation DEGRADES, recording historyTooLong with the historyBudget it exhausted " +
			"(carrying dueAt/remindedFor/sentAt as they stood, clearing stale and dropping the checkpoint) and " +
			"sending nothing, which holds the row quiet and visible rather than " +
			"re-dispatching a doomed evaluation on every window; the next posted entry clears the flag and buys one " +
			"more attempt, and a later, larger budget reaches the accounts a smaller one parked, once. The member " +
			"the notification addresses is resolved LIVE off the account's own heldFor " +
			"out-link, never from the payload; an account with no live heldFor identity is still evaluated (the " +
			"arrears fact is about the account), and the notification's params carry an identityKey only where " +
			"one resolves. Restricted to Weaver's dispatch actor: the account it names is forwarded into a " +
			"message a member actually receives.",
		Script: accountDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"identityKey":{"type":"string","description":"vtx.identity.<NanoID> of the member this account is for (WellnessCreateAccount; required there, validated alive). The account gets its own independently-minted NanoID; uniqueness (one account per member) is enforced via the identity's .wellnessLedgerAccount guard aspect, not the account's own id."},` +
			`"accountKey":{"type":"string","description":"EvaluateWellnessArrears only: vtx.wellnessaccount.<NanoID> of the account whose arrears are being aged (required there, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.wellnessaccount.<NanoID> — the created account on WellnessCreateAccount (the caller must read it from the ACCEPTED reply, since the id can no longer be derived from identityKey), or the evaluated account on EvaluateWellnessArrears."}}}`,
		FieldDescription: map[string]string{
			"identityKey": "WellnessCreateAccount only, and required there. Full vtx.identity.<NanoID> key of the member the account is opened for. WellnessCreateAccount validates it is alive, mints the account under a fresh independent NanoID, writes the identity's .wellnessLedgerAccount guard aspect (one account per member) and the heldFor link (account→identity). EvaluateWellnessArrears takes no identityKey field: the identity is resolved live off that same heldFor link, and carried into the notification params only where one resolves.",
			"accountKey":  "EvaluateWellnessArrears only, and required there. Full vtx.wellnessaccount.<NanoID> key of the account to age. Validated alive; its postedTo history is replayed under a bounded budget and the FIFO-oldest open charge's due date is recorded on the account's .arrears aspect.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "EvaluateWellnessArrears — age a member's account and remind once it is overdue",
				Payload: map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>"},
				ExpectedOutcome: "Validates the account is alive, replays ONE PAGE of its postedTo history and folds it into a " +
					"running aggregate. A history that fits one page finalizes in the same dispatch and ages it FIFO; a longer " +
					"one records the aggregate and the cursor as a checkpoint on .arrears.replay and Weaver dispatches the next " +
					"page. On finalize, writes vtx.wellnessaccount.<NanoID>.arrears = {evaluatedAt, dueAt?, remindedFor?, sentAt?} " +
					"— {evaluatedAt} alone when nothing is owed. When the oldest open charge's due date has passed it stamps " +
					"remindedFor = that date, and where no reminder has yet gone out in this episode (sentAt absent) ALSO stamps " +
					"sentAt and emits external.notification keyed <accountKey>:<dueAt>, with an identityKey in its params only " +
					"where the account's own heldFor link resolves to a live identity. A re-run recomputes the head, finds sentAt " +
					"already recorded, and sends nothing. A history past ArrearsMaxPages pages records historyTooLong with the " +
					"historyBudget it exhausted instead, dropping any checkpoint and " +
					"carrying what was already recorded and sending nothing. Rejects AuthDenied for any actor but Weaver's " +
					"dispatch actor and UnknownAccount for an absent or tombstoned account.",
			},
			{
				Name:    "WellnessCreateAccount — open the ledger account for a member",
				Payload: map[string]any{"identityKey": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Validates the identity is alive. Atomically commits vtx.wellnessaccount.<freshNanoID> (root data {} — D5) " +
					"+ the identity's .wellnessLedgerAccount guard aspect + the heldFor link (account→identity). Emits " +
					"account.created{accountKey, identityKey}. Returns primaryKey (the new account key — the caller's only " +
					"reliable source for it). Rejects with UnknownIdentity if the identity is absent, or AccountAlreadyExists " +
					"if the caller declared the guard aspect in reads and it already exists (a repeat/racing caller retrying " +
					"after learning the account already exists) — a first-time caller who declared only identityKey instead " +
					"sees a raw substrate conflict on the guard aspect's create-only write if it loses a genuine race.",
			},
		},
	}
}

// accountGuardAspectTypeDDL declares the .wellnessLedgerAccount aspect (class
// wellnessLedgerAccountGuard) WellnessCreateAccount writes on the IDENTITY — the
// deterministic create-only key that enforces "at most one ledger account per
// member" now that the account itself carries an independent NanoID (not the
// identity's own). Declaration-only: the aspect is written by WellnessCreateAccount,
// never has its own operationType.
func accountGuardAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "wellnessLedgerAccountGuard",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"WellnessCreateAccount"},
		Description: "Per-member ledger-account uniqueness guard aspect. Stored as vtx.identity.<NanoID>.wellnessLedgerAccount " +
			"(class wellnessLedgerAccountGuard) = {accountKey: <vtx.wellnessaccount.<NanoID>>}. Non-sensitive. Created " +
			"exactly once by WellnessCreateAccount, atomically alongside the account vertex it names — a second WellnessCreateAccount for " +
			"the same identity that declares this key in contextHint.reads sees the clean AccountAlreadyExists domain " +
			"rejection; one that does not (the normal first-ever-call shape, since the key doesn't exist yet to declare) " +
			"instead relies on this aspect's own create-only write to fail a genuine concurrent race. Prefixed distinctly " +
			"from clinic-ledger's .ledgerAccount / loftspace-ledger's .ledgerAccount: an identity may hold accounts across " +
			"multiple verticals, so each vertical's guard localName must not collide. Declaration-only: no op handler of " +
			"its own.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"accountKey":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"accountKey": "The vtx.wellnessaccount.<NanoID> this member's (at most one) ledger account was minted as.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "member ledger-account guard aspect",
				Payload:         map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>"},
				ExpectedOutcome: "Stored as vtx.identity.<NanoID>.wellnessLedgerAccount; created once by WellnessCreateAccount alongside the account vertex it names.",
			},
		},
	}
}

// accountArrearsAspectTypeDDL declares the .arrears aspect (class
// wellnessAccountArrears) on the ACCOUNT — the arrears-episode state the
// wellnessArrearsReminders convergence lens reads, and the marker that records
// which episode a reminder has already gone out for.
//
// Its LIFETIME, end to end. There is none at WellnessCreateAccount: a brand-new
// account owes nothing, and its missing evaluatedAt is exactly what opens the
// convergence gap once, so the first evaluation writes the aspect. From there
// TWO kinds of writer maintain it, each conditioned on one hydrated revision
// (the key is declared optionalReads by both DDLs' derive_reads and by every
// dispatcher, so a bare update is auto-conditioned and retry-eligible rather
// than last-write-wins):
//
//   - EvaluateWellnessArrears recomputes the head from the account's own
//     history and rewrites the aspect outright — {dueAt, evaluatedAt} plus the
//     episode's send record while a charge is open, {evaluatedAt} alone once
//     the history nets to nothing owed. An episode ends ONLY in this op (this
//     ledger stores no balance for a posted entry to see reach zero), in one
//     of two ways: the history nets to nothing owed, or the charge that
//     opened the episode it finds posted at or after the recorded sentAt — a
//     payment to zero and a fresh charge both posted before the evaluation
//     ran, so the send belongs to the finished episode and is dropped with
//     its remindedFor (a head that a partial payment moved past the opener
//     is still the same episode, and keeps it). stale is never
//     carried across an evaluation, and neither is historyTooLong/
//     historyBudget or the replay checkpoint. It carries
//     sentAt forward for as long as the episode runs: that field, not
//     remindedFor, is what says a reminder has already gone out for THIS
//     episode, so a head that a partial payment moved to another overdue
//     charge is recorded (remindedFor) without sending again.
//   - A history longer than one page of the op's own postedTo enumeration
//     leaves the aspect MID-REPLAY instead: the evaluation that consumed a
//     page but has not exhausted the enumeration writes ONLY the checkpoint
//     (.arrears.replay = {phase, cursor, pages, entries}) — every other
//     recorded field is carried verbatim, because
//     a page has evaluated nothing. The phase flips on every page and is what
//     the wellnessArrearsReminders lens projects one continuation gap per
//     value for, so the page that closes the gap that dispatched it opens
//     the other. The page that exhausts the enumeration computes the head
//     over the whole aggregate and writes the aspect without a checkpoint —
//     the same outcome as the single-page case above.
//   - The one evaluation that does NOT recompute is the degraded one: an
//     account whose postedTo history outran the replay budget (more pages
//     than ArrearsMaxPages) records historyTooLong with the historyBudget it
//     exhausted, carrying dueAt/remindedFor/sentAt untouched and dropping
//     stale and any checkpoint, and sends nothing. The flag suppresses both
//     the convergence gap and the timer while the recorded budget is at
//     least the current one, so the row goes quiet rather than
//     re-dispatching a doomed evaluation on every window; it is dropped by
//     the next posted entry's carry, which buys exactly one more attempt,
//     and a later, larger budget reaches the accounts a smaller one parked.
//   - EVERY WellnessDebitAccount / WellnessCreditAccount against an account
//     that carries the aspect marks it stale (carrying every other field, the
//     send record included, but dropping any replay checkpoint — the entry
//     changes the postedTo set the checkpoint's cursor pages over): with no
//     balance to reason from, an entry can
//     tell neither an episode opening from one continuing nor a clearing
//     payment from a partial one, so it asks for the recomputation and never
//     guesses. Against an account with no aspect it writes nothing — such an
//     account is already opening the never-evaluated gap.
//
// Non-sensitive: dates, an object and booleans on a vtx.wellnessaccount (not
// an identity), no money and no PII. Declaration-only: written by the three
// ops above, never dispatched as an operation in its own right. Never
// tombstoned (an account is never tombstoned).
func accountArrearsAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "wellnessAccountArrears",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"WellnessDebitAccount", "WellnessCreditAccount", arrearsOp},
		Description: "Per-account arrears-episode aspect. Stored as vtx.wellnessaccount.<NanoID>.arrears " +
			"(class wellnessAccountArrears) = {evaluatedAt, dueAt?, remindedFor?, sentAt?, stale?, historyTooLong?, " +
			"historyBudget?, replay?}. Non-sensitive. " +
			"dueAt is the FIFO-oldest still-open charge's postedAt plus the ledger's net term — a RECORDED time " +
			"fact, written by the op, never a clock a lens reads. remindedFor names the dueAt the evaluation has " +
			"acknowledged as passed — it is what closes the convergence gap; sentAt is the instant the reminder's " +
			"outbox event was committed (the SEND INTENT — the adapter's delivery outcome is .arrearsNotification), " +
			"and its ABSENCE is the send condition, which is what makes the notification once-per-EPISODE rather " +
			"than once-per-head or once-per-convergence-window. historyTooLong (with historyBudget, the entry count " +
			"it exhausted) means the account's history outran the " +
			"evaluation's replay budget, so no head could be computed: it suppresses both the gap and the timer (the " +
			"row stays visible but quiet for an operator) while historyBudget is at least the current budget, and is " +
			"dropped by the next posted entry, which buys one " +
			"further attempt. replay is the checkpoint of an evaluation part-way through a history longer than one " +
			"page of its postedTo replay: {phase: \"a\"|\"b\", cursor, pages, entries: {txId: {postedAt, type, " +
			"amountCents, reversesKey}}}. phase flips on every page and is the lens's continuation trigger " +
			"(one gap per phase); cursor resumes the enumeration; pages counts those consumed; entries is every " +
			"posted entry folded so far, keyed by transaction id and keeping each one's OWN postedAt and reversesKey " +
			"rather than netted into per-debit sums and a running credit total, because the FIFO walk's " +
			"episode-start tracking needs every credit's own chronological position, not just the final open set — " +
			"and is what the FIFO head is computed from (via arrears_rows) once the enumeration is exhausted. Present only between the " +
			"first page and the last — the finalize page and the degrade write the aspect without it, and every " +
			"entry op drops it, because a posted entry changes the set under the cursor. A checkpoint the evaluation " +
			"cannot resume (a malformed field) is treated as absent: the evaluation restarts at page 1. stale means " +
			"what is recorded may no longer describe the account — EVERY posted entry " +
			"sets it, because this ledger stores no balance for an entry to reason from — and is a request for a " +
			"fresh EvaluateWellnessArrears, which rewrites the aspect and so never carries it forward. Written by " +
			"WellnessDebitAccount / WellnessCreditAccount (mark stale, drop any checkpoint; mint nothing where absent) and " +
			"EvaluateWellnessArrears (pages the replay, writing only the checkpoint while it is mid-way; recomputes " +
			"the head on the finalize page; ends the episode at {evaluatedAt} alone when nothing is " +
			"owed, and drops a send record that predates the charge that opened the episode it finds — the boundary between an episode paid " +
			"off and the next one opened before any evaluation ran). Read by the wellnessArrearsReminders " +
			"convergence lens and projected for the front desk and the " +
			"member's statement by wellnessMemberAccounts. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"evaluatedAt":{"type":"string"},"dueAt":{"type":"string"},"remindedFor":{"type":"string"},"sentAt":{"type":"string"},"stale":{"type":"boolean"},"historyTooLong":{"type":"boolean"},"historyBudget":{"type":"integer"},"replay":{"type":"object","properties":{"phase":{"type":"string","enum":["` + ArrearsPhaseA + `","` + ArrearsPhaseB + `"]},"cursor":{"type":"string"},"pages":{"type":"integer"},"entries":{"type":"object","additionalProperties":{"type":"object","properties":{"postedAt":{"type":"string"},"type":{"type":"string"},"amountCents":{"type":"integer"},"reversesKey":{"type":"string"}}}}}}}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"evaluatedAt":    "RFC3339 instant (canonical UTC) the arrears state was last written by an evaluation. Its ABSENCE is what opens the convergence gap for an account nothing has ever evaluated.",
			"dueAt":          "RFC3339 instant (canonical UTC) the FIFO-oldest still-open charge falls overdue: that charge's own postedAt plus the ledger's net term. Absent when the account owes nothing.",
			"remindedFor":    "The dueAt the last evaluation acknowledged as passed. Equal to dueAt closes the convergence gap; different (or absent) leaves it open for a recorded lapse to re-open.",
			"sentAt":         "RFC3339 instant (canonical UTC) the reminder's outbox event was committed for this arrears episode — the send intent the front-desk grid and the member's statement show, and the fact a booking hold reads. Its ABSENCE is what lets the next passed deadline send; it is carried across every write of a live episode and dropped only by the evaluation that finds the episode over: no open charge, or an episode whose opening charge posted at or after this instant (the balance returned to zero and a new charge opened a fresh episode before an evaluation ran); a head that a partial payment moved past the opener stays in the same episode and keeps it.",
			"stale":          "True when what is recorded may no longer describe the account — every posted entry sets it, since the ledger stores no balance to reason from. Opens the convergence gap; cleared by the evaluation that recomputes the head.",
			"historyBudget":  "The entry budget (ArrearsPageLimit × ArrearsMaxPages) historyTooLong was recorded under. Lets a later, larger budget tell an account it parks apart from one an earlier, smaller budget already parked, and re-arm the former exactly once.",
			"replay":         "The checkpoint of an evaluation part-way through a history longer than one page of its postedTo replay: {phase: \"a\"|\"b\", cursor, pages, entries: {txId: {postedAt, type, amountCents, reversesKey}}}. phase flips on every page and is the lens's continuation trigger (one gap per phase); cursor resumes the enumeration; pages counts those consumed; entries is every posted entry folded so far, keyed by transaction id with its own postedAt/type/reversesKey preserved, which arrears_rows turns back into arrears_head's usual per-row input once the enumeration is exhausted. Present only between the first page and the last — the finalize page and the degrade write the aspect without it, and every entry op drops it, because a posted entry changes the set under the cursor. A checkpoint the evaluation cannot resume (a malformed field) is treated as absent: the evaluation restarts at page 1.",
			"historyTooLong": "True when the account's postedTo history outran the evaluation's bounded replay budget, so no FIFO head could be computed. Suppresses BOTH the convergence gap and the freshness timer — the row stays in the read model for an operator to see, without re-dispatching an evaluation that cannot succeed. Dropped by the next posted entry (which also marks the state stale), buying exactly one more attempt.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "account arrears aspect — overdue, reminded once",
				Payload:         map[string]any{"evaluatedAt": "2026-08-22T09:00:00Z", "dueAt": "2026-08-06T14:20:00Z", "remindedFor": "2026-08-06T14:20:00Z", "sentAt": "2026-08-22T09:00:00Z"},
				ExpectedOutcome: "Stored as vtx.wellnessaccount.<NanoID>.arrears; written by EvaluateWellnessArrears on the commit that also emitted the notification. remindedFor = dueAt closes the gap, so no second reminder goes out for this episode.",
			},
		},
	}
}

// aspectDeclarationOnlyScript is the declaration-only Starlark for the
// package's aspect-type DDLs — wellnessLedgerAccountGuard, wellnessAccountArrears
// and wellnessAccountArrearsNotification are written by the account, transaction
// and notification ops' own handlers, never dispatched as operations in their
// own right.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`

func transactionDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "wellnesstransaction",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"WellnessDebitAccount", "WellnessCreditAccount"},
		Description: "Ledger transaction DDL. Vertex shape: vtx.wellnesstransaction.<NanoID>, class=wellnesstransaction, root data = {} " +
			"(minimal, D5 — the entry detail is a .entry aspect). WellnessDebitAccount{accountKey, amountCents, memo?, bookingRef?, " +
			"priceBookingRef?} records a charge (a no-show fee or a class-price charge); WellnessCreditAccount{accountKey, " +
			"amountCents, memo?, reason?} records a payment received OR a waived charge. " +
			"Each mints a fresh vtx.wellnesstransaction.<NanoID> + a .entry aspect {type (debit|credit), amountCents, memo?, " +
			"postedAt, reason? (credit only)} + the postedTo link (transaction→account, the transaction is the later-arriving " +
			"vertex so it is the source — Contract #1 §1.1). The ledger is APPEND-ONLY — no balance is stored or mutated on the " +
			"account; the wellnessLedgerHistory lens derives a balance by summing entries, so concurrent debits/credits never " +
			"race a read-modify-write. Requires the accountKey be a live account and amountCents be a positive number. " +
			"A MANUAL charge — a WellnessDebitAccount carrying neither bookingRef nor priceBookingRef — additionally requires a " +
			"non-blank memo (InvalidArgument otherwise): an append-only entry can never be explained after the fact. A " +
			"settlement debit (either booking ref present) stays memo-optional, since its ref already names the booking. " +
			"WellnessDebitAccount also accepts an optional bookingRef (vtx.booking.<NanoID>, validated alive when supplied — " +
			"UnknownBooking otherwise): when present, writes a settles audit link (transaction→booking) that the " +
			"wellnessNoShowSettlement lens (targets.go) walks to converge the no-show-fee gap once posted. A plain " +
			"human-submitted WellnessDebitAccount (no bookingRef) is unaffected — the field mirrors clinic-ledger's appointmentRef " +
			"shape (itself mirroring cafe-ledger's tabRef). WellnessDebitAccount separately and independently accepts an optional " +
			"priceBookingRef (vtx.booking.<NanoID>, validated alive when supplied — UnknownBooking otherwise): when " +
			"present, writes a DISTINCT settlesClassPrice audit link (transaction→booking) that the " +
			"wellnessClassPriceSettlement lens (lenses.go/targets.go) walks to converge the class-price gap once posted — " +
			"a separate relation from settles/bookingRef so the two settlement gaps (no-show fee vs. class price) never " +
			"collide in a count(). A WellnessDebitAccount may carry bookingRef, priceBookingRef, both, or neither — the two are " +
			"independent, no mutual exclusion. WellnessCreditAccount separately accepts an optional refundRef " +
			"(vtx.wellnessrefund.<NanoID>, validated alive when supplied — UnknownRefund otherwise, NOT class=booking: a " +
			"cancelled booking is already tombstoned by the time any refund posts, wellness-domain/ddls.go): when " +
			"present, writes a settlesRefund audit link (transaction→wellnessrefund) that the wellnessRefundSettlement " +
			"lens (lenses.go/targets.go) walks to converge the refund gap once posted. " +
			"A credit's reason (payment|waiver|refund, default payment when omitted) distinguishes cash actually collected from debt the " +
			"studio forgave (e.g. a no-show fee waived as a courtesy) from a credit that returns money already collected (a settled " +
			"class price or no-show fee reversed by wellnessRefundSettlement) — all three reduce the derived balance identically, but the " +
			"wellnessLedgerHistory lens projects reason so a reader never mistakes a refund or a waiver for a fresh payment. " +
			"reason:\"waiver\" and reason:\"refund\" are both rejected on a self-scoped (member) credit — post_entry's own " +
			"authContextTarget branch — since a member may pay down their own balance but never forgive or refund it. " +
			"Every entry, debit or credit, ALSO marks the account's .arrears episode state (wellnessAccountArrears DDL) " +
			"stale where it exists — carrying every other field, the episode's send record included, but DROPPING " +
			"any replay checkpoint (the entry changes the postedTo set the checkpoint's cursor pages over, so the " +
			"next EvaluateWellnessArrears restarts at page 1) — and mints " +
			"nothing where it does not: with no stored balance an entry cannot tell an episode opening from one " +
			"continuing, so it asks EvaluateWellnessArrears to recompute rather than guess. The write is a bare update " +
			"auto-conditioned on the revision the key hydrated at, and the DDL's own derive_reads hydrates it " +
			"whatever the submitter declared.",
		Script: transactionDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"accountKey":{"type":"string","description":"vtx.wellnessaccount.<NanoID> the transaction posts to (WellnessDebitAccount/WellnessCreditAccount; required, validated alive)."},` +
			`"amountCents":{"type":"number","description":"The transaction amount in integer cents; required, must be > 0. A debit is a charge (increases what the member owes); a credit is a payment (decreases it)."},` +
			`"memo":{"type":"string","description":"Free-text description of the charge or payment (e.g. \"No-show fee — Vinyasa Flow\", \"Front-desk payment\"). Required, non-blank, on a MANUAL charge — a WellnessDebitAccount carrying neither bookingRef nor priceBookingRef (InvalidArgument otherwise). Optional on every other entry: a settlement debit names its booking through its ref, and a credit records money moved against a balance already itemised."},` +
			`"bookingRef":{"type":"string","description":"WellnessDebitAccount only; optional vtx.booking.<NanoID> back-reference to the no-show booking this charge settles. When supplied, validated alive (UnknownBooking otherwise) and a settles audit link (transaction→booking) is written — the wellnessNoShowSettlement lens reads it to converge the gap. Mirrors clinic-ledger's appointmentRef."},` +
			`"priceBookingRef":{"type":"string","description":"WellnessDebitAccount only; optional vtx.booking.<NanoID> back-reference to the booking this charge settles the CLASS PRICE for. Independent of bookingRef (a WellnessDebitAccount may carry either, both, or neither). When supplied, validated alive (UnknownBooking otherwise) and a settlesClassPrice audit link (transaction→booking) is written — the wellnessClassPriceSettlement lens reads it to converge the gap."},` +
			`"refundRef":{"type":"string","description":"WellnessCreditAccount only; optional vtx.wellnessrefund.<NanoID> back-reference to the refund marker this payment settles. When supplied, validated alive (UnknownRefund otherwise) and a settlesRefund audit link (transaction→wellnessrefund) is written — the wellnessRefundSettlement lens reads it to converge the gap."},` +
			`"reason":{"type":"string","enum":["payment","waiver","refund"],"description":"WellnessCreditAccount only; optional, defaults to \"payment\" when omitted. \"waiver\" records the credit as debt the studio forgave rather than cash collected; \"refund\" records a credit that returns money already collected (a settled class price or no-show fee reversed by wellnessRefundSettlement) — all three reduce the derived balance the same way, but the wellnessLedgerHistory lens projects reason so none are ever confused. Rejected on WellnessDebitAccount, and rejected on a self-scoped (member) credit — a member may pay down their own balance but never waive or refund it."}},` +
			`"required":["accountKey","amountCents"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.wellnesstransaction.<NanoID> of the minted transaction (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"accountKey":      "Full vtx.wellnessaccount.<NanoID> key the transaction posts to. WellnessDebitAccount/WellnessCreditAccount validate it is alive and write the postedTo link (transaction→account) the wellnessLedgerHistory lens walks.",
			"amountCents":     "The transaction amount in integer cents; required, must be a positive number. Stored on the .entry aspect and projected verbatim by the wellnessLedgerHistory lens.",
			"memo":            "Free-text description of the charge or payment. Stored on the .entry aspect when supplied; projected by the wellnessLedgerHistory lens. Required non-blank on a manual charge (a WellnessDebitAccount carrying neither bookingRef nor priceBookingRef) — the entry is append-only, so an unexplained charge stays unexplained; optional on a settlement debit and on every credit.",
			"bookingRef":      "WellnessDebitAccount only: optional full vtx.booking.<NanoID> key of the no-show booking this charge settles. Validated alive when supplied (UnknownBooking otherwise); writes a settles link (transaction→booking) the wellnessNoShowSettlement lens walks to converge the gap.",
			"priceBookingRef": "WellnessDebitAccount only: optional full vtx.booking.<NanoID> key of the booking this charge settles the class price for. Independent of bookingRef. Validated alive when supplied (UnknownBooking otherwise); writes a settlesClassPrice link (transaction→booking) the wellnessClassPriceSettlement lens walks to converge the gap.",
			"refundRef":       "WellnessCreditAccount only: optional full vtx.wellnessrefund.<NanoID> key of the refund marker this payment settles (minted by wellness-domain's CancelBooking, ddls.go, when a cancelled booking already carried a posted class-price charge). Validated alive when supplied (UnknownRefund otherwise); writes a settlesRefund link (transaction→wellnessrefund) the wellnessRefundSettlement lens walks to converge the gap.",
			"reason":          "WellnessCreditAccount only: \"payment\", \"waiver\", or \"refund\" (default \"payment\" when omitted). \"refund\" records a credit that returns money already collected (a settled class price or no-show fee reversed by wellnessRefundSettlement), distinct from cash collected (payment) and debt forgiven (waiver). Stored on the .entry aspect; projected by the wellnessLedgerHistory lens. Rejected on WellnessDebitAccount, and rejected on a self-scoped (member) credit — only front-desk staff / the operator may waive or refund a charge.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "WellnessDebitAccount — charge a front-desk fee",
				Payload: map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>", "amountCents": 2500, "memo": "No-show fee"},
				ExpectedOutcome: "Validates the account is alive and amountCents > 0. Atomically commits vtx.wellnesstransaction.<NanoID> " +
					"(root data {} — D5) + the .entry aspect {type: debit, amountCents: 2500, memo: \"No-show fee\", postedAt} " +
					"+ the postedTo link (transaction→account). Emits account.debited{accountKey, transactionKey, amountCents}. " +
					"Returns primaryKey. Rejects UnknownAccount if the account is absent, or InvalidArgument if amountCents <= 0.",
			},
			{
				Name:    "WellnessDebitAccount — Weaver-dispatched no-show settlement (bookingRef)",
				Payload: map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>", "amountCents": 2500, "bookingRef": "vtx.booking.<NanoID>"},
				ExpectedOutcome: "Same as the plain charge, plus validates bookingRef is alive (UnknownBooking otherwise) " +
					"and writes lnk.wellnesstransaction.<id>.settles.booking.<id> (transaction→booking). This is the shape " +
					"wellness-ledger's own wellnessNoShowSettlement Weaver target dispatches — a human-submitted WellnessDebitAccount " +
					"simply omits bookingRef and gets the plain charge shape above.",
			},
			{
				Name:    "WellnessDebitAccount — Weaver-dispatched class-price settlement (priceBookingRef)",
				Payload: map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>", "amountCents": 1500, "priceBookingRef": "vtx.booking.<NanoID>"},
				ExpectedOutcome: "Same as the plain charge, plus validates priceBookingRef is alive (UnknownBooking otherwise) " +
					"and writes lnk.wellnesstransaction.<id>.settlesClassPrice.booking.<id> (transaction→booking) — a distinct " +
					"relation from settles/bookingRef, so the no-show and class-price gaps never collide in a count(). This is " +
					"the shape wellness-ledger's own wellnessClassPriceSettlement Weaver target dispatches; bookingRef and " +
					"priceBookingRef may both be supplied on the same call (independent, no mutual exclusion).",
			},
			{
				Name:    "WellnessCreditAccount — record a payment",
				Payload: map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>", "amountCents": 2500, "memo": "Front-desk payment"},
				ExpectedOutcome: "Same shape as WellnessDebitAccount minus bookingRef, but writes .entry{type: credit, ...} and emits " +
					"account.credited{accountKey, transactionKey, amountCents}. A payment reduces what the member owes " +
					"(the wellnessLedgerHistory-derived balance = sum(debits) − sum(credits)).",
			},
			{
				Name:    "WellnessCreditAccount — Weaver-dispatched class-price refund (refundRef)",
				Payload: map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>", "amountCents": 1500, "refundRef": "vtx.wellnessrefund.<NanoID>"},
				ExpectedOutcome: "Same as a plain payment, plus validates refundRef is alive (UnknownRefund otherwise) " +
					"and writes lnk.wellnesstransaction.<id>.settlesRefund.wellnessrefund.<id> (transaction→wellnessrefund). " +
					"This is the shape wellness-ledger's own wellnessRefundSettlement Weaver target dispatches — a " +
					"human-submitted WellnessCreditAccount simply omits refundRef and gets the plain payment shape above.",
			},
			{
				Name:    "WellnessCreditAccount — waive a no-show fee (front-desk/operator only)",
				Payload: map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>", "amountCents": 2500, "memo": "Waived — member hardship", "reason": "waiver"},
				ExpectedOutcome: "Same shape as a plain payment, but .entry carries reason: \"waiver\" instead of the default \"payment\" — the " +
					"balance drops identically, but the wellnessLedgerHistory lens projects reason so a reader never mistakes forgiven debt for " +
					"cash collected. Rejects AuthDenied if the caller is a self-scoped member — only the operator/front-of-house scope=any " +
					"grant may waive.",
			},
		},
	}
}
