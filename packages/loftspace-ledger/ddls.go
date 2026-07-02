package loftspaceledger

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `account`
// (CreateAccount), `transaction` (DebitAccount, CreditAccount), and the
// `ledgerAccountGuard` aspect-type declaration (the lease-anchored
// uniqueness guard CreateAccount writes).
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		accountDDL(),
		accountGuardAspectTypeDDL(),
		transactionDDL(),
	}
}

func accountDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "account",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateAccount"},
		Description: "Ledger account DDL. Vertex shape: vtx.account.<NanoID>, class=account, root data = {} " +
			"(minimal, D5 — the balance is LENS-derived by summing transactions, never stored). CreateAccount{leaseAppKey} " +
			"mints the account under its OWN independently-generated NanoID (never reused from the lease — Core KV " +
			"NanoIDs are unique identifiers across all of Core KV, not scoped per vertex type; reuse corrupts " +
			"Refractor adjacency, which keys by bare NodeID with no type qualifier). \"One account per lease\" is " +
			"enforced by a deterministic create-only guard aspect on the PRE-EXISTING leaseapp " +
			"(leaseAppKey+\".ledgerAccount\", ledgerAccountGuard DDL) instead: a second CreateAccount for the same " +
			"lease conflicts on that already-existing aspect key. Writes the heldFor link (account→leaseapp, the " +
			"account is the later-arriving vertex so it is the source — Contract #1 §1.1). Requires the " +
			"leaseAppKey be a live leaseapp (no orphan accounts).",
		Script: accountDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the lease this account is for (CreateAccount; required, validated alive). The account gets its own independently-minted NanoID; uniqueness (one account per lease) is enforced via the leaseapp's .ledgerAccount guard aspect, not the account's own id."}},` +
			`"required":["leaseAppKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.account.<NanoID> of the created account (the operation's principal key) — the caller must read this from the ACCEPTED reply, since the id can no longer be derived from leaseAppKey."}}}`,
		FieldDescription: map[string]string{
			"leaseAppKey": "Full vtx.leaseapp.<NanoID> key of the lease the account is opened for. CreateAccount validates it is alive, mints the account under a fresh independent NanoID, writes the leaseapp's .ledgerAccount guard aspect (one account per lease) and the heldFor link (account→leaseapp).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateAccount — open the ledger account for a signed lease",
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
// ledgerAccountGuard) CreateAccount writes on the PRE-EXISTING leaseapp — the
// deterministic create-only key that enforces "at most one ledger account per
// lease" now that the account itself carries an independent NanoID (not the
// lease's own). Declaration-only: the aspect is written by CreateAccount,
// never has its own operationType.
func accountGuardAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "ledgerAccountGuard",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateAccount"},
		Description: "Per-lease ledger-account uniqueness guard aspect. Stored as vtx.leaseapp.<NanoID>.ledgerAccount " +
			"(class ledgerAccountGuard) = {accountKey: <vtx.account.<NanoID>>}. Non-sensitive. Created exactly once by " +
			"CreateAccount, atomically alongside the account vertex it names — a second CreateAccount for the same " +
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
				ExpectedOutcome: "Stored as vtx.leaseapp.<NanoID>.ledgerAccount; created once by CreateAccount alongside the account vertex it names.",
			},
		},
	}
}

func transactionDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "transaction",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"DebitAccount", "CreditAccount"},
		Description: "Ledger transaction DDL. Vertex shape: vtx.transaction.<NanoID>, class=transaction, root data = {} " +
			"(minimal, D5 — the entry detail is a .entry aspect). DebitAccount{accountKey, amountCents, memo?} records a " +
			"charge (rent, a late fee, a deposit); CreditAccount{accountKey, amountCents, memo?} records a payment received. " +
			"Each mints a fresh vtx.transaction.<NanoID> + a .entry aspect {type (debit|credit), amountCents, memo?, postedAt} " +
			"+ the postedTo link (transaction→account, the transaction is the later-arriving vertex so it is the source — " +
			"Contract #1 §1.1). The ledger is APPEND-ONLY — no balance is stored or mutated on the account; the ledgerHistory " +
			"lens derives a balance by summing entries, so concurrent debits/credits never race a read-modify-write. Requires " +
			"the accountKey be a live account and amountCents be a positive number.",
		Script: transactionDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"accountKey":{"type":"string","description":"vtx.account.<NanoID> the transaction posts to (DebitAccount/CreditAccount; required, validated alive)."},` +
			`"amountCents":{"type":"number","description":"The transaction amount in integer cents; required, must be > 0. A debit is a charge (increases what the tenant owes); a credit is a payment (decreases it)."},` +
			`"memo":{"type":"string","description":"Optional free-text description of the charge or payment (e.g. \"June rent\", \"Late fee\"). Optional."}},` +
			`"required":["accountKey","amountCents"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.transaction.<NanoID> of the minted transaction (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"accountKey":  "Full vtx.account.<NanoID> key the transaction posts to. DebitAccount/CreditAccount validate it is alive and write the postedTo link (transaction→account) the ledgerHistory lens walks.",
			"amountCents": "The transaction amount in integer cents; required, must be a positive number. Stored on the .entry aspect and projected verbatim by the ledgerHistory lens.",
			"memo":        "Optional free-text description of the charge or payment (e.g. \"June rent\", \"Late fee — 5 days\"). Stored on the .entry aspect when supplied; projected by the ledgerHistory lens.",
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
		},
	}
}
