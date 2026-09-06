package cafeledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `cafeaccount`
// (CreateAccount, EvaluateCafeArrears), `cafetransaction` (DebitAccount,
// CreditCafeAccount, RefundCafeCharge), the `cafeLedgerAccountGuard`
// aspect-type declaration (the lease-anchored uniqueness guard CreateAccount
// writes), the `cafeAccountBalance` aspect-type declaration (the
// account-anchored running-balance cache CreateAccount mints and every posted
// entry keeps updated), the `cafeAccountArrears` aspect-type declaration (the
// account's arrears-episode state), and the notification-outcome DDL pair
// (notifications.go) the bridge replies onto. Vertical-prefixed like
// clinic-ledger: a DDL canonicalName is global across every installed package
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
		CanonicalName:     "cafeaccount",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateAccount", arrearsOp},
		Description: "House-tab ledger account DDL. Vertex shape: vtx.cafeaccount.<NanoID>, class=cafeaccount, root " +
			"data = {} (minimal, D5 — the DISPLAYED balance is LENS-derived by summing transactions). " +
			"CreateAccount also mints a .balance aspect ({balanceCents: 0}, cafeAccountBalance DDL) alongside the " +
			"account — the running total transactionDDL's DebitAccount/CreditCafeAccount/RefundCafeCharge keep in " +
			"lockstep with every posted entry (an auto-conditioned update, retry-eligible on a concurrent-writer " +
			"conflict), an O(1) authorization cache the cafeLedgerHistory lens's own independent full-history sum " +
			"remains the display source of truth for. " +
			"CreateAccount{leaseAppKey} mints the account under its OWN independently-generated NanoID (never reused " +
			"from the lease — Core KV NanoIDs are unique identifiers across all of Core KV, not scoped per vertex " +
			"type; reuse corrupts Refractor adjacency, which keys by bare NodeID with no type qualifier). \"One café " +
			"account per lease\" is enforced by a deterministic create-only guard aspect on the PRE-EXISTING leaseapp " +
			"(leaseAppKey+\".cafeLedgerAccount\" — vertical-prefixed LOCAL NAME, not just class, because this same " +
			"leaseapp already carries loftspace-ledger's own \".ledgerAccount\" guard aspect; reusing that local name " +
			"would collide key-for-key with it) instead: a second CreateAccount for the same lease conflicts on that " +
			"already-existing aspect key. Writes the heldFor link (cafeaccount→leaseapp, the cafeaccount is the " +
			"later-arriving vertex so it is the source — Contract #1 §1.1). Requires the leaseAppKey be a live " +
			"leaseapp (no orphan accounts). " +
			"EvaluateCafeArrears{accountKey} is the second operation on this DDL, dispatched by " +
			"Weaver's cafeArrearsReminders playbook rather than by a person: it replays the account's own postedTo " +
			"history under a bounded budget, ages it with the same FIFO the resident's statement runs (credits offset " +
			"the oldest still-open charge first; an unapplied credit carries forward as surplus), and records the " +
			"resulting due date — the oldest open charge's postedAt plus the package's net term — on the account's " +
			".arrears aspect (cafeAccountArrears DDL). Once that date has passed the evaluation records remindedFor = " +
			"that date, and where NO reminder has yet gone out in this arrears episode (sentAt ABSENT) the same commit " +
			"also stamps sentAt and fires an external.notification to the bridge's \"notification\" adapter keyed on " +
			"(accountKey, dueAt). The send condition is sentAt's absence, not remindedFor's value: the unit is the " +
			"EPISODE — from the charge that took the account from square to owing until the balance returns to zero — " +
			"and a partial payment moves the head from one overdue charge to the next without starting a new episode, " +
			"so exactly ONE notification goes out per episode however often the evaluation is re-dispatched, " +
			"redelivered, or re-run over a moved head. An account whose history outruns the replay budget is not " +
			"refused: the evaluation DEGRADES, recording historyTooLong (carrying dueAt/remindedFor/sentAt as they " +
			"stood, clearing stale) and sending nothing, which holds the row quiet and visible rather than " +
			"re-dispatching a doomed evaluation on every window; the next posted entry clears the flag and buys one " +
			"more attempt. The lease the notification addresses is resolved LIVE off the account's own heldFor " +
			"out-link, never from the payload; an account with no live heldFor lease is still evaluated (the " +
			"arrears fact is about the account), and the notification's params carry a leaseAppKey only where " +
			"one resolves. Restricted to Weaver's dispatch actor: the account it names is forwarded into a " +
			"message a resident actually receives.",
		Script: accountDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"leaseAppKey":{"type":"string","description":"CreateAccount only, and required there. vtx.leaseapp.<NanoID> of the resident lease this café account is for (validated alive). The account gets its own independently-minted NanoID; uniqueness (one café account per lease) is enforced via the leaseapp's .cafeLedgerAccount guard aspect, not the account's own id."},` +
			`"accountKey":{"type":"string","description":"EvaluateCafeArrears only: vtx.cafeaccount.<NanoID> of the account whose arrears are being aged (required there, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.cafeaccount.<NanoID> — the created account on CreateAccount (the caller must read it from the ACCEPTED reply, since the id can no longer be derived from leaseAppKey), or the evaluated account on EvaluateCafeArrears."}}}`,
		FieldDescription: map[string]string{
			"leaseAppKey": "CreateAccount only, and required there. Full vtx.leaseapp.<NanoID> key of the resident lease the café account is opened for — validated alive, then the account is minted under a fresh independent NanoID alongside the leaseapp's .cafeLedgerAccount guard aspect (one café account per lease) and the heldFor link (cafeaccount→leaseapp). EvaluateCafeArrears takes no leaseAppKey field: the lease is resolved live off that same heldFor link, and carried into the notification params only where one resolves.",
			"accountKey":  "EvaluateCafeArrears only, and required there. Full vtx.cafeaccount.<NanoID> key of the account to age. Validated alive; its postedTo history is replayed under a bounded budget and the FIFO-oldest open charge's due date is recorded on the account's .arrears aspect.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "EvaluateCafeArrears — age a house tab and remind once it is overdue",
				Payload: map[string]any{"accountKey": "vtx.cafeaccount.<NanoID>"},
				ExpectedOutcome: "Validates the account is alive, replays its postedTo history under the evaluation budget and ages it " +
					"FIFO. Writes vtx.cafeaccount.<NanoID>.arrears = {evaluatedAt, dueAt?, remindedFor?, sentAt?} — {evaluatedAt} " +
					"alone when nothing is owed. When the oldest open charge's due date has passed it stamps remindedFor = " +
					"that date, and where no reminder has yet gone out in this episode (sentAt absent) ALSO stamps sentAt and " +
					"emits external.notification keyed <accountKey>:<dueAt>, with a leaseAppKey in its params only where " +
					"the account's own heldFor link resolves to a live lease. A re-run recomputes the head, finds sentAt " +
					"already recorded, and sends nothing. A history past the replay budget records historyTooLong instead, " +
					"carrying what was already recorded and sending nothing. Rejects AuthDenied for any actor but Weaver's " +
					"dispatch actor and UnknownAccount for an absent or tombstoned account.",
			},
			{
				Name:    "CreateAccount — open the house-tab account for a resident lease",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>"},
				ExpectedOutcome: "Validates the leaseapp is alive. Atomically commits vtx.cafeaccount.<freshNanoID> (root data {} — D5) " +
					"+ the leaseapp's .cafeLedgerAccount guard aspect + the account's .balance aspect ({balanceCents: 0}) " +
					"+ the heldFor link (cafeaccount→leaseapp). Emits " +
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

// accountGuardAspectTypeDDL declares the .cafeLedgerAccount aspect (class
// cafeLedgerAccountGuard) CreateAccount writes on the PRE-EXISTING leaseapp —
// the deterministic create-only key that enforces "at most one café account
// per lease" now that the account itself carries an independent NanoID (not
// the lease's own). The local name is vertical-prefixed (cafeLedgerAccount,
// not ledgerAccount): this leaseapp already carries loftspace-ledger's own
// .ledgerAccount aspect, and a bare, unprefixed local name would collide
// key-for-key with it. Declaration-only: the aspect is written by
// CreateAccount, never has its own operationType.
func accountGuardAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "cafeLedgerAccountGuard",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateAccount"},
		Description: "Per-lease café-ledger-account uniqueness guard aspect. Stored as " +
			"vtx.leaseapp.<NanoID>.cafeLedgerAccount (class cafeLedgerAccountGuard) = {accountKey: " +
			"<vtx.cafeaccount.<NanoID>>}. Non-sensitive. Created exactly once by CreateAccount, atomically alongside " +
			"the account vertex it names — a second CreateAccount for the same lease that declares this key in " +
			"contextHint.reads sees the clean AccountAlreadyExists domain rejection; one that does not (the normal " +
			"first-ever-call shape, since the key doesn't exist yet to declare) instead relies on this aspect's own " +
			"create-only write to fail a genuine concurrent race. The local name is vertical-prefixed " +
			"(cafeLedgerAccount) because this same leaseapp already carries loftspace-ledger's own .ledgerAccount " +
			"guard aspect — a bare local name would collide with it. Declaration-only: no op handler of its own.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"accountKey":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"accountKey": "The vtx.cafeaccount.<NanoID> this lease's (at most one) café account was minted as.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "lease café-ledger-account guard aspect",
				Payload:         map[string]any{"accountKey": "vtx.cafeaccount.<NanoID>"},
				ExpectedOutcome: "Stored as vtx.leaseapp.<NanoID>.cafeLedgerAccount; created once by CreateAccount alongside the account vertex it names.",
			},
		},
	}
}

// accountBalanceAspectTypeDDL declares the .balance aspect (class
// cafeAccountBalance) on the ACCOUNT — the maintained O(1) running-total cache
// accountDDLScript mints at CreateAccount ({balanceCents: 0}) and
// transactionDDLScript moves by the signed amount on every
// DebitAccount/CreditCafeAccount/RefundCafeCharge posted to an account that
// carries it. Declaration-only, mirroring
// accountGuardAspectTypeDDL: the aspect is written by those four ops' own
// handlers, never has an operationType of its own.
//
// Its LIFETIME, end to end: created at CreateAccount at zero; on an account
// minted under cafe-ledger < 0.4.0, which carries none, computed once from a
// bounded replay of the account's postedTo history by the first PAYMENT posted
// to it — a charge or a refund against such an account posts without writing
// any .balance, leaving it legacy until that payment, whose replay then sums
// the whole history including those entries. Updated by the signed amount on
// every entry posted to an account that carries it, and never settable by a
// caller (no op takes balanceCents as a payload field). Absent on a legacy
// account is why every dispatcher declares it optionalReads and not reads, and
// why the transaction DDL's own derive_reads returns it under optionalReads
// too. A tombstoned .balance is revived by the same update the next entry
// emits, never re-created (a create against a tombstone is refused, Contract #3
// §3.3).
func accountBalanceAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "cafeAccountBalance",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateAccount", "DebitAccount", "CreditCafeAccount", "RefundCafeCharge"},
		Description: "Per-account running-balance cache aspect. Stored as vtx.cafeaccount.<NanoID>.balance " +
			"(class cafeAccountBalance) = {balanceCents: <integer>}. Non-sensitive. Minted at {balanceCents: 0} by " +
			"CreateAccount alongside the account vertex it names, then kept in lockstep with every posted entry by " +
			"DebitAccount (+= amountCents), CreditCafeAccount (-= amountCents) and RefundCafeCharge (-= amountCents) " +
			"via a bare update — auto-conditioned on the step-4 hydrated revision (a declared read) rather than an " +
			"explicit expectedRevision, which is what makes it retry-eligible under a concurrent writer instead of " +
			"hard-conflicting. An account minted under cafe-ledger < 0.4.0 carries no .balance; the key is declared " +
			"optionalReads (not reads) by every dispatcher and by the transaction DDL's own derive_reads, and the " +
			"first PAYMENT posted to such an account computes the aspect from a bounded replay of its postedTo " +
			"history — a charge or a refund against one posts without writing any .balance at all. Exists purely as " +
			"this package's own O(1) authorization " +
			"cache (the CreditCafeAccount amount-owed cap, on the resident and staff legs alike); the " +
			"cafeLedgerHistory lens remains the independently-derived display source of truth and never reads this " +
			"aspect. Declaration-only: no op handler of its own.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"balanceCents":{"type":"integer"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"balanceCents": "The account's current running balance in integer cents (positive = owed, can go negative when a refund gives back a charge the resident had already paid). Maintained by DebitAccount/CreditCafeAccount/RefundCafeCharge, never set directly by a caller.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "account running-balance cache aspect",
				Payload:         map[string]any{"balanceCents": 1425},
				ExpectedOutcome: "Stored as vtx.cafeaccount.<NanoID>.balance; minted at 0 by CreateAccount, updated by the signed amount on every DebitAccount/CreditCafeAccount/RefundCafeCharge.",
			},
		},
	}
}

// accountArrearsAspectTypeDDL declares the .arrears aspect (class
// cafeAccountArrears) on the ACCOUNT — the arrears-episode state the
// cafeArrearsReminders convergence lens reads, and the marker that records
// which episode a reminder has already gone out for.
//
// Its LIFETIME, end to end. There is none at CreateAccount: a brand-new account
// owes nothing, and its missing evaluatedAt is exactly what opens the
// convergence gap once, so the first evaluation writes the aspect. From there
// FIVE writers maintain it, each conditioned on one hydrated revision (the key
// is declared optionalReads by both DDLs' derive_reads and by every dispatcher,
// so a bare update is auto-conditioned and retry-eligible rather than
// last-write-wins):
//
//   - DebitAccount that takes the balance from zero-or-below to owing opens an
//     episode: {dueAt = this charge's postedAt + the net term, evaluatedAt},
//     dropping any finished episode's remindedFor/sentAt/stale. A debit against
//     an account that ALREADY owes writes nothing — the head is an older charge,
//     and re-stamping dueAt would push a weeks-old debt's due date back to
//     today. Nor does a debit that leaves the account still IN CREDIT (a refund
//     took it below zero and this charge only eats into that surplus) — the
//     surplus prepays the charge outright, so there is no open debit to age.
//   - CreditCafeAccount / RefundCafeCharge that take the balance to zero or
//     below end the episode: {evaluatedAt} alone, so no timer stays armed.
//   - CreditCafeAccount / RefundCafeCharge that leave a balance mark the state
//     stale (carrying every other field): a partial payment can move the FIFO
//     head to a later charge with a later due date, which no single entry can
//     compute. A legacy account (no .balance, so the entry op has no before/
//     after balance at all) can ONLY ever mark stale, never mint.
//   - EvaluateCafeArrears recomputes the head from the account's own history and
//     rewrites the aspect outright — which is what the stale mark asks for, so
//     stale is never carried across an evaluation, and neither is
//     historyTooLong. It carries sentAt forward for as long as the episode runs:
//     that field, not remindedFor, is what says a reminder has already gone out
//     for THIS episode, so a head that a partial payment moved to another
//     overdue charge is recorded (remindedFor) without sending again.
//   - The one evaluation that does NOT recompute is the degraded one: an account
//     whose postedTo history outran the replay budget records historyTooLong,
//     carrying dueAt/remindedFor/sentAt untouched and dropping stale, and sends
//     nothing. The flag suppresses both the convergence gap and the timer, so
//     the row goes quiet rather than re-dispatching a doomed evaluation on every
//     window; it is dropped by the next posted entry's carry, which buys exactly
//     one more attempt.
//
// Non-sensitive: dates and two booleans on a vtx.cafeaccount (not an identity), no
// money and no PII. Declaration-only: written by the four ops above, never
// dispatched as an operation in its own right.
func accountArrearsAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "cafeAccountArrears",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"DebitAccount", "CreditCafeAccount", "RefundCafeCharge", arrearsOp},
		Description: "Per-account arrears-episode aspect. Stored as vtx.cafeaccount.<NanoID>.arrears " +
			"(class cafeAccountArrears) = {evaluatedAt, dueAt?, remindedFor?, sentAt?, stale?, historyTooLong?}. Non-sensitive. " +
			"dueAt is the FIFO-oldest still-open charge's postedAt plus the ledger's net term — a RECORDED time " +
			"fact, written by the op, never a clock a lens reads. remindedFor names the dueAt the evaluation has " +
			"acknowledged as passed — it is what closes the convergence gap; sentAt is when a reminder actually went " +
			"out, and its ABSENCE is the send condition, which is what makes the notification once-per-EPISODE rather " +
			"than once-per-head or once-per-convergence-window. historyTooLong means the account's history outran the " +
			"evaluation's replay budget, so no head could be computed: it suppresses both the gap and the timer (the " +
			"row stays visible but quiet for an operator) and is dropped by the next posted entry, which buys one " +
			"further attempt. stale means what is recorded may no longer " +
			"describe the account (a partial payment moved the head, or the account carries no .balance to reason " +
			"with) and is a request for a fresh EvaluateCafeArrears, which rewrites the aspect and so never carries " +
			"it forward. Written by DebitAccount (opens an episode on an account that owed nothing), " +
			"CreditCafeAccount / RefundCafeCharge (end the episode at zero, else mark stale) and EvaluateCafeArrears " +
			"(recomputes the head). Read by the cafeArrearsReminders convergence lens and projected for the front " +
			"desk by cafeLeaseAccounts. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"evaluatedAt":{"type":"string"},"dueAt":{"type":"string"},"remindedFor":{"type":"string"},"sentAt":{"type":"string"},"stale":{"type":"boolean"},"historyTooLong":{"type":"boolean"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"evaluatedAt":    "RFC3339 instant (canonical UTC) the arrears state was last written — by an evaluation or by the entry that changed the episode. Its ABSENCE is what opens the convergence gap for an account nothing has ever evaluated.",
			"dueAt":          "RFC3339 instant (canonical UTC) the FIFO-oldest still-open charge falls overdue: that charge's own postedAt plus the ledger's net term. Absent when the account owes nothing.",
			"remindedFor":    "The dueAt the last evaluation acknowledged as passed. Equal to dueAt closes the convergence gap; different (or absent) leaves it open for a recorded lapse to re-open.",
			"sentAt":         "RFC3339 instant (canonical UTC) a reminder for this arrears episode was sent — the timestamp the front-desk grid and the resident's statement show. Its ABSENCE is what lets the next passed deadline send; it is carried across every write of a live episode and dropped only where the episode itself ends.",
			"stale":          "True when what is recorded may no longer describe the account (a partial payment moved the FIFO head, or the account carries no .balance). Opens the convergence gap; cleared by the evaluation that recomputes the head.",
			"historyTooLong": "True when the account's postedTo history outran the evaluation's bounded replay budget, so no FIFO head could be computed. Suppresses BOTH the convergence gap and the freshness timer — the row stays in the read model for an operator to see, without re-dispatching an evaluation that cannot succeed. Dropped by the next posted entry (which also marks the state stale), buying exactly one more attempt.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "account arrears aspect — overdue, reminded once",
				Payload:         map[string]any{"evaluatedAt": "2026-08-22T09:00:00Z", "dueAt": "2026-08-06T14:20:00Z", "remindedFor": "2026-08-06T14:20:00Z", "sentAt": "2026-08-22T09:00:00Z"},
				ExpectedOutcome: "Stored as vtx.cafeaccount.<NanoID>.arrears; written by EvaluateCafeArrears on the commit that also emitted the notification. remindedFor = dueAt closes the gap, so no second reminder goes out for this episode.",
			},
		},
	}
}

// aspectDeclarationOnlyScript is the declaration-only Starlark for the
// package's aspect-type DDLs — cafeLedgerAccountGuard, cafeAccountBalance and
// cafeAccountArrears are written by CreateAccount's, the transaction ops' and
// EvaluateCafeArrears' own handlers, never dispatched as operations in their
// own right.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`

func transactionDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "cafetransaction",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"DebitAccount", "CreditCafeAccount", "RefundCafeCharge"},
		Description: "House-tab ledger transaction DDL. Vertex shape: vtx.cafetransaction.<NanoID>, " +
			"class=cafetransaction, root data = {} (minimal, D5 — the entry detail is a .entry aspect). " +
			"DebitAccount{accountKey, amountCents, memo?, tabRef?} records a café charge (a settled tab); " +
			"CreditCafeAccount{accountKey, amountCents, memo?} records a payment received; " +
			"RefundCafeCharge{accountKey, reversesRef, amountCents, memo?} gives back part or all of a charge " +
			"already posted. Each mints a fresh " +
			"vtx.cafetransaction.<NanoID> + a .entry aspect " +
			"{type (debit|credit), amountCents, memo?, postedAt, refundedCents?} + the " +
			"postedTo link (cafetransaction→cafeaccount, the cafetransaction is the later-arriving vertex so it is " +
			"the source — Contract #1 §1.1) + a bare (no explicit expectedRevision) update of the account's own " +
			".balance aspect (accountDDL) by the signed amount, where the account carries one — auto-conditioned on " +
			"the step-4 hydrated revision since .balance is a declared read (this DDL's own derive_reads declares it " +
			"on every dispatch, so a submitter cannot omit it), which is what makes it retry-eligible: a lost race " +
			"re-hydrates and retries the whole op rather than hard-conflicting. A posted entry's own money fields are never " +
			"rewritten, and the cafeLedgerHistory lens still derives its own full-history sum independently (the " +
			"display source of truth); .balance is this DDL's O(1) authorization cache, letting a payment verify the " +
			"amount owed without replaying the account's whole transaction history. " +
			"CreditCafeAccount's amountCents may never exceed the account's outstanding balance, and is refused " +
			"outright on an account that owes nothing (AuthDenied) — on the resident's own scope=self submit and on " +
			"a staff scope=any submit alike, since nothing on this platform verifies that a payment actually " +
			"happened whoever keyed it. Requires the accountKey be a live account and amountCents be a positive " +
			"number. DebitAccount-only optional tabRef (the cafe-domain Settle consumer, mirroring loftspace-ledger's " +
			"clauseRef): when present and the referenced tab is alive, writes the settles audit link " +
			"(cafetransaction→tab) the cafeTabSettlement Weaver target reads to detect the charge is posted; a plain " +
			"human-submitted DebitAccount omitting tabRef is byte-for-byte unaffected. " +
			"RefundCafeCharge-only REQUIRED reversesRef: the vtx.cafetransaction.<NanoID> of the posted charge " +
			"being given back. It posts an ordinary credit entry — so every balance consumer sums it unchanged — " +
			"plus a reverses link (cafetransaction→cafetransaction, the refund is the later-arriving vertex so it " +
			"is the source), which is what distinguishes a correction from a payment the resident handed over. The " +
			"reference must name a live cafetransaction whose .entry is a DEBIT posted to the SAME accountKey, and " +
			"amountCents may not exceed that charge's own amount minus what has already been given back against it " +
			"(RefundExceedsCharge), so partial refunds accumulate to at most the charge. That charge-scoped ceiling " +
			"is a refund's ONLY cap — a refund is not bounded by the outstanding balance the way a payment is, so " +
			"giving back a charge the resident already paid legitimately takes .balance negative. The refunded " +
			"amount is maintained as a " +
			"refundedCents field on the reversed charge's own .entry aspect, upserted in the refund's batch under " +
			"a compare-and-set pinned to the revision that aspect was read at. That single tally is the ceiling: " +
			"two refunds racing one charge can never jointly overrun it — the loser either loses that " +
			"compare-and-set outright or, on an account whose own .balance update is auto-conditioned too, " +
			"re-hydrates and re-runs and is then refused on the fresh tally. " +
			"A refund is a front-desk act and is never self-scoped: a submit carrying an authContext target is " +
			"refused.",
		Script: transactionDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"accountKey":{"type":"string","description":"vtx.cafeaccount.<NanoID> the transaction posts to (DebitAccount/CreditCafeAccount/RefundCafeCharge; required, validated alive)."},` +
			`"amountCents":{"type":"integer","description":"The transaction amount in whole cents; required, must be > 0. A debit is a charge (increases what the resident owes on the house tab); a credit is a payment (decreases it) and may never exceed what is owed; a refund is a credit bounded instead by the charge it reverses."},` +
			`"memo":{"type":"string","description":"Optional free-text description of the charge, payment or refund (e.g. \"Settled tab — table 4\", \"House tab payment\", \"Wrong item charged\"). Optional."},` +
			`"tabRef":{"type":"string","description":"DebitAccount only: vtx.tab.<NanoID> of the cafe-domain tab this charge settles (optional, validated alive when supplied). Writes the settles audit link."},` +
			`"reversesRef":{"type":"string","description":"RefundCafeCharge only: vtx.cafetransaction.<NanoID> of the posted charge being given back (required, validated alive, must be a debit on the same account). Writes the reverses link."}},` +
			`"required":["accountKey","amountCents"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.cafetransaction.<NanoID> of the minted transaction (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"accountKey":  "Full vtx.cafeaccount.<NanoID> key the transaction posts to. DebitAccount/CreditCafeAccount/RefundCafeCharge validate it is alive and write the postedTo link (transaction→account) the cafeLedgerHistory lens walks.",
			"amountCents": "The transaction amount in integer cents; required, must be a positive number. Stored on the .entry aspect and projected verbatim by the cafeLedgerHistory lens — a refund never alters the charge's own amountCents. On CreditCafeAccount it is additionally capped by the account's own outstanding balance (server-verified against the maintained .balance aspect, on the resident and staff legs alike), and refused outright on an account that owes nothing. On RefundCafeCharge it is capped instead by what the reversed charge still has un-refunded (its amountCents minus its refundedCents tally).",
			"memo":        "Optional free-text description of the charge, payment or refund (e.g. \"Settled tab — table 4\", \"House tab payment\", \"Wrong item charged\"). Stored on the .entry aspect when supplied; projected by the cafeLedgerHistory lens.",
			"tabRef":      "DebitAccount only. Full vtx.tab.<NanoID> key of the cafe-domain tab this charge settles. Validated alive when supplied; writes the settles audit link (transaction→tab) the cafeTabSettlement Weaver target's missing_charge gap reads. Omitted on a plain human-submitted DebitAccount, and refused outright (InvalidArgument) on RefundCafeCharge, whose credit settles no tab.",
			"reversesRef": "RefundCafeCharge only, and required there. Full vtx.cafetransaction.<NanoID> key of the posted charge being given back — validated alive, required to be a DEBIT posted to the same accountKey, and the ceiling on amountCents (its own amountCents minus its refundedCents). Writes the reverses link (refund→charge) the cafeLedgerHistory lens projects as reversesKey, and adds this refund to the charge's refundedCents tally under a compare-and-set on that .entry aspect's hydrated revision.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "DebitAccount — settle a café tab onto the house account",
				Payload: map[string]any{"accountKey": "vtx.cafeaccount.<NanoID>", "amountCents": 1850, "memo": "Settled tab — table 4"},
				ExpectedOutcome: "Validates the account is alive and amountCents > 0. Atomically commits vtx.cafetransaction.<NanoID> " +
					"(root data {} — D5) + the .entry aspect {type: debit, amountCents: 1850, memo: \"Settled tab — table 4\", postedAt} " +
					"+ the postedTo link (transaction→account). Emits account.debited{accountKey, transactionKey, amountCents}. " +
					"Returns primaryKey. Rejects UnknownAccount if the account is absent, or InvalidArgument if amountCents <= 0.",
			},
			{
				Name:    "DebitAccount — Weaver-dispatched tab settlement (tabRef)",
				Payload: map[string]any{"accountKey": "vtx.cafeaccount.<NanoID>", "amountCents": 1850, "tabRef": "vtx.tab.<NanoID>"},
				ExpectedOutcome: "Same as the plain DebitAccount, plus (tabRef alive) the settles link " +
					"(transaction→tab) — the cafeTabSettlement Weaver target's missing_charge gap templates this from " +
					"row.tabKey. Rejects UnknownTab if the referenced tab is absent or tombstoned.",
			},
			{
				Name:    "CreditCafeAccount — record a house-tab payment",
				Payload: map[string]any{"accountKey": "vtx.cafeaccount.<NanoID>", "amountCents": 1850, "memo": "House tab payment"},
				ExpectedOutcome: "Same shape as DebitAccount, but writes .entry{type: credit, ...} and emits " +
					"account.credited{accountKey, transactionKey, amountCents}. A payment reduces what the resident owes " +
					"(the cafeLedgerHistory-derived balance = sum(debits) − sum(credits)). Rejects AuthDenied when the " +
					"account owes nothing, or when amountCents runs past its outstanding balance — whoever submitted it, " +
					"since no payment rail witnesses either a resident's or a staffer's keyed amount.",
			},
			{
				Name:    "RefundCafeCharge — give back a charge that should not have been posted",
				Payload: map[string]any{"accountKey": "vtx.cafeaccount.<NanoID>", "reversesRef": "vtx.cafetransaction.<NanoID>", "amountCents": 450, "memo": "Wrong item charged"},
				ExpectedOutcome: "Validates the account is alive, that reversesRef names a live cafetransaction whose .entry " +
					"is a debit postedTo that same account, and that 450 fits inside what that charge still has " +
					"un-refunded. Commits the ordinary credit shape (transaction + .entry{type: credit, …} + postedTo) " +
					"plus the reverses link (refund→charge) and the charge's own refundedCents tally, conditioned on " +
					"the revision its .entry was read at, and emits account.credited. Rejects UnknownTransaction if " +
					"the reference is absent, InvalidArgument if it is a credit or posted to another account, " +
					"RefundExceedsCharge if the amount runs past the charge, and AuthDenied on a self-scoped submit.",
			},
		},
	}
}
