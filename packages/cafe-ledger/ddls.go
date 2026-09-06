package cafeledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `cafeaccount`
// (CreateAccount), `cafetransaction` (DebitAccount, CreditCafeAccount,
// RefundCafeCharge), the `cafeLedgerAccountGuard` aspect-type declaration (the
// lease-anchored uniqueness guard CreateAccount writes), and the
// `cafeAccountBalance` aspect-type declaration (the account-anchored
// running-balance cache CreateAccount mints and every posted entry keeps
// updated). Vertical-prefixed like clinic-ledger: a DDL canonicalName is
// global across every installed package (internal/pkgmgr/installer.go
// checkCanonicalNameCollision), and loftspace-ledger already owns the bare
// `account` / `transaction` names.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		accountDDL(),
		accountGuardAspectTypeDDL(),
		accountBalanceAspectTypeDDL(),
		transactionDDL(),
	}
}

func accountDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "cafeaccount",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateAccount"},
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
			"leaseapp (no orphan accounts).",
		Script: accountDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the resident lease this café account is for (CreateAccount; required, validated alive). The account gets its own independently-minted NanoID; uniqueness (one café account per lease) is enforced via the leaseapp's .cafeLedgerAccount guard aspect, not the account's own id."}},` +
			`"required":["leaseAppKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.cafeaccount.<NanoID> of the created account (the operation's principal key) — the caller must read this from the ACCEPTED reply, since the id can no longer be derived from leaseAppKey."}}}`,
		FieldDescription: map[string]string{
			"leaseAppKey": "Full vtx.leaseapp.<NanoID> key of the resident lease the café account is opened for. CreateAccount validates it is alive, mints the account under a fresh independent NanoID, writes the leaseapp's .cafeLedgerAccount guard aspect (one café account per lease) and the heldFor link (cafeaccount→leaseapp).",
		},
		Examples: []pkgmgr.ExampleSpec{
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

// aspectDeclarationOnlyScript is the declaration-only Starlark for the
// package's aspect-type DDLs — cafeLedgerAccountGuard and cafeAccountBalance
// are written by CreateAccount's and the transaction ops' own handlers, never
// dispatched as operations in their own right.
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
