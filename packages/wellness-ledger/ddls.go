package wellnessledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `wellnessaccount`
// (CreateAccount), `wellnesstransaction` (DebitAccount, CreditAccount), and
// the `wellnessLedgerAccountGuard` aspect-type declaration (the
// identity-anchored uniqueness guard CreateAccount writes). Vertical-prefixed:
// a DDL canonicalName is global across every installed package
// (internal/pkgmgr/installer.go checkCanonicalNameCollision), and
// loftspace-ledger already owns the bare `account` / `transaction` names.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		accountDDL(),
		accountGuardAspectTypeDDL(),
		transactionDDL(),
	}
}

func accountDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "wellnessaccount",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateAccount"},
		Description: "Ledger account DDL. Vertex shape: vtx.wellnessaccount.<NanoID>, class=wellnessaccount, root data = {} " +
			"(minimal, D5 — the balance is LENS-derived by summing transactions, never stored). CreateAccount{identityKey} " +
			"mints the account under its OWN independently-generated NanoID (never reused from the identity — Core KV " +
			"NanoIDs are unique platform-wide identifiers, not scoped per vertex type). \"One account per member\" is " +
			"enforced by a deterministic create-only guard aspect on the IDENTITY (identityKey+\".wellnessLedgerAccount\", " +
			"wellnessLedgerAccountGuard DDL) instead: a second CreateAccount for the same identity conflicts on that " +
			"already-existing aspect key. Writes the heldFor link (account→identity, the account is the later-arriving " +
			"vertex so it is the source — Contract #1 §1.1). Requires the identityKey be a live identity (no orphan accounts).",
		Script: accountDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"identityKey":{"type":"string","description":"vtx.identity.<NanoID> of the member this account is for (CreateAccount; required, validated alive). The account gets its own independently-minted NanoID; uniqueness (one account per member) is enforced via the identity's .wellnessLedgerAccount guard aspect, not the account's own id."}},` +
			`"required":["identityKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.wellnessaccount.<NanoID> of the created account (the operation's principal key) — the caller must read this from the ACCEPTED reply, since the id can no longer be derived from identityKey."}}}`,
		FieldDescription: map[string]string{
			"identityKey": "Full vtx.identity.<NanoID> key of the member the account is opened for. CreateAccount validates it is alive, mints the account under a fresh independent NanoID, writes the identity's .wellnessLedgerAccount guard aspect (one account per member) and the heldFor link (account→identity).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateAccount — open the ledger account for a member",
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
// wellnessLedgerAccountGuard) CreateAccount writes on the IDENTITY — the
// deterministic create-only key that enforces "at most one ledger account per
// member" now that the account itself carries an independent NanoID (not the
// identity's own). Declaration-only: the aspect is written by CreateAccount,
// never has its own operationType.
func accountGuardAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "wellnessLedgerAccountGuard",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateAccount"},
		Description: "Per-member ledger-account uniqueness guard aspect. Stored as vtx.identity.<NanoID>.wellnessLedgerAccount " +
			"(class wellnessLedgerAccountGuard) = {accountKey: <vtx.wellnessaccount.<NanoID>>}. Non-sensitive. Created " +
			"exactly once by CreateAccount, atomically alongside the account vertex it names — a second CreateAccount for " +
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
				ExpectedOutcome: "Stored as vtx.identity.<NanoID>.wellnessLedgerAccount; created once by CreateAccount alongside the account vertex it names.",
			},
		},
	}
}

// aspectDeclarationOnlyScript is the declaration-only Starlark for
// wellnessLedgerAccountGuard — written by CreateAccount's own op handler,
// never dispatched as an operation in its own right.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`

func transactionDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "wellnesstransaction",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"DebitAccount", "CreditAccount"},
		Description: "Ledger transaction DDL. Vertex shape: vtx.wellnesstransaction.<NanoID>, class=wellnesstransaction, root data = {} " +
			"(minimal, D5 — the entry detail is a .entry aspect). DebitAccount{accountKey, amountCents, memo?, bookingRef?, " +
			"priceBookingRef?} records a charge (a no-show fee or a class-price charge); CreditAccount{accountKey, " +
			"amountCents, memo?} records a payment received. " +
			"Each mints a fresh vtx.wellnesstransaction.<NanoID> + a .entry aspect {type (debit|credit), amountCents, memo?, " +
			"postedAt} + the postedTo link (transaction→account, the transaction is the later-arriving vertex so it is the " +
			"source — Contract #1 §1.1). The ledger is APPEND-ONLY — no balance is stored or mutated on the account; the " +
			"wellnessLedgerHistory lens derives a balance by summing entries, so concurrent debits/credits never race a " +
			"read-modify-write. Requires the accountKey be a live account and amountCents be a positive number. " +
			"DebitAccount also accepts an optional bookingRef (vtx.booking.<NanoID>, validated alive when supplied — " +
			"UnknownBooking otherwise): when present, writes a settles audit link (transaction→booking) that the " +
			"wellnessNoShowSettlement lens (targets.go) walks to converge the no-show-fee gap once posted. A plain " +
			"human-submitted DebitAccount (no bookingRef) is unaffected — the field mirrors clinic-ledger's appointmentRef " +
			"shape (itself mirroring cafe-ledger's tabRef). DebitAccount separately and independently accepts an optional " +
			"priceBookingRef (vtx.booking.<NanoID>, validated alive when supplied — UnknownBooking otherwise): when " +
			"present, writes a DISTINCT settlesClassPrice audit link (transaction→booking) that the " +
			"wellnessClassPriceSettlement lens (lenses.go/targets.go) walks to converge the class-price gap once posted — " +
			"a separate relation from settles/bookingRef so the two settlement gaps (no-show fee vs. class price) never " +
			"collide in a count(). A DebitAccount may carry bookingRef, priceBookingRef, both, or neither — the two are " +
			"independent, no mutual exclusion.",
		Script: transactionDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"accountKey":{"type":"string","description":"vtx.wellnessaccount.<NanoID> the transaction posts to (DebitAccount/CreditAccount; required, validated alive)."},` +
			`"amountCents":{"type":"number","description":"The transaction amount in integer cents; required, must be > 0. A debit is a charge (increases what the member owes); a credit is a payment (decreases it)."},` +
			`"memo":{"type":"string","description":"Optional free-text description of the charge or payment (e.g. \"No-show fee — Vinyasa Flow\", \"Front-desk payment\"). Optional."},` +
			`"bookingRef":{"type":"string","description":"DebitAccount only; optional vtx.booking.<NanoID> back-reference to the no-show booking this charge settles. When supplied, validated alive (UnknownBooking otherwise) and a settles audit link (transaction→booking) is written — the wellnessNoShowSettlement lens reads it to converge the gap. Mirrors clinic-ledger's appointmentRef."},` +
			`"priceBookingRef":{"type":"string","description":"DebitAccount only; optional vtx.booking.<NanoID> back-reference to the booking this charge settles the CLASS PRICE for. Independent of bookingRef (a DebitAccount may carry either, both, or neither). When supplied, validated alive (UnknownBooking otherwise) and a settlesClassPrice audit link (transaction→booking) is written — the wellnessClassPriceSettlement lens reads it to converge the gap."}},` +
			`"required":["accountKey","amountCents"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.wellnesstransaction.<NanoID> of the minted transaction (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"accountKey":      "Full vtx.wellnessaccount.<NanoID> key the transaction posts to. DebitAccount/CreditAccount validate it is alive and write the postedTo link (transaction→account) the wellnessLedgerHistory lens walks.",
			"amountCents":     "The transaction amount in integer cents; required, must be a positive number. Stored on the .entry aspect and projected verbatim by the wellnessLedgerHistory lens.",
			"memo":            "Optional free-text description of the charge or payment. Stored on the .entry aspect when supplied; projected by the wellnessLedgerHistory lens.",
			"bookingRef":      "DebitAccount only: optional full vtx.booking.<NanoID> key of the no-show booking this charge settles. Validated alive when supplied (UnknownBooking otherwise); writes a settles link (transaction→booking) the wellnessNoShowSettlement lens walks to converge the gap.",
			"priceBookingRef": "DebitAccount only: optional full vtx.booking.<NanoID> key of the booking this charge settles the class price for. Independent of bookingRef. Validated alive when supplied (UnknownBooking otherwise); writes a settlesClassPrice link (transaction→booking) the wellnessClassPriceSettlement lens walks to converge the gap.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "DebitAccount — charge a front-desk fee",
				Payload: map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>", "amountCents": 2500, "memo": "No-show fee"},
				ExpectedOutcome: "Validates the account is alive and amountCents > 0. Atomically commits vtx.wellnesstransaction.<NanoID> " +
					"(root data {} — D5) + the .entry aspect {type: debit, amountCents: 2500, memo: \"No-show fee\", postedAt} " +
					"+ the postedTo link (transaction→account). Emits account.debited{accountKey, transactionKey, amountCents}. " +
					"Returns primaryKey. Rejects UnknownAccount if the account is absent, or InvalidArgument if amountCents <= 0.",
			},
			{
				Name: "DebitAccount — Weaver-dispatched no-show settlement (bookingRef)",
				Payload: map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>", "amountCents": 2500, "bookingRef": "vtx.booking.<NanoID>"},
				ExpectedOutcome: "Same as the plain charge, plus validates bookingRef is alive (UnknownBooking otherwise) " +
					"and writes lnk.wellnesstransaction.<id>.settles.booking.<id> (transaction→booking). This is the shape " +
					"wellness-ledger's own wellnessNoShowSettlement Weaver target dispatches — a human-submitted DebitAccount " +
					"simply omits bookingRef and gets the plain charge shape above.",
			},
			{
				Name: "DebitAccount — Weaver-dispatched class-price settlement (priceBookingRef)",
				Payload: map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>", "amountCents": 1500, "priceBookingRef": "vtx.booking.<NanoID>"},
				ExpectedOutcome: "Same as the plain charge, plus validates priceBookingRef is alive (UnknownBooking otherwise) " +
					"and writes lnk.wellnesstransaction.<id>.settlesClassPrice.booking.<id> (transaction→booking) — a distinct " +
					"relation from settles/bookingRef, so the no-show and class-price gaps never collide in a count(). This is " +
					"the shape wellness-ledger's own wellnessClassPriceSettlement Weaver target dispatches; bookingRef and " +
					"priceBookingRef may both be supplied on the same call (independent, no mutual exclusion).",
			},
			{
				Name:    "CreditAccount — record a payment",
				Payload: map[string]any{"accountKey": "vtx.wellnessaccount.<NanoID>", "amountCents": 2500, "memo": "Front-desk payment"},
				ExpectedOutcome: "Same shape as DebitAccount minus bookingRef, but writes .entry{type: credit, ...} and emits " +
					"account.credited{accountKey, transactionKey, amountCents}. A payment reduces what the member owes " +
					"(the wellnessLedgerHistory-derived balance = sum(debits) − sum(credits)).",
			},
		},
	}
}
