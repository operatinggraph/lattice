package clinicledger

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `clinicaccount`
// (CreateAccount) and `clinictransaction` (DebitAccount, CreditAccount).
// Vertical-prefixed: a DDL canonicalName is global across every installed
// package (internal/pkgmgr/installer.go checkCanonicalNameCollision), and
// loftspace-ledger already owns the bare `account` / `transaction` names.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		accountDDL(),
		transactionDDL(),
	}
}

func accountDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "clinicaccount",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateAccount"},
		Description: "Ledger account DDL. Vertex shape: vtx.clinicaccount.<NanoID>, class=clinicaccount, root data = {} " +
			"(minimal, D5 — the balance is LENS-derived by summing transactions, never stored). CreateAccount{patientKey} " +
			"mints exactly one account per patient: the account's NanoID is the SAME bare id as the patient's own " +
			"(a deterministic key, not minted), so a second CreateAccount for the same patient conflicts on the " +
			"already-existing key (AccountAlreadyExists) rather than needing a separate guard link. Writes the heldFor " +
			"link (account→patient, the account is the later-arriving vertex so it is the source — Contract #1 §1.1). " +
			"Requires the patientKey be a live patient (no orphan accounts).",
		Script: accountDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"patientKey":{"type":"string","description":"vtx.patient.<NanoID> of the patient this account is for (CreateAccount; required, validated alive). The account's own id is derived from this key's NanoID — one account per patient."}},` +
			`"required":["patientKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.clinicaccount.<NanoID> of the created account (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"patientKey": "Full vtx.patient.<NanoID> key of the patient the account is opened for. CreateAccount validates it is alive, derives the account's id from the same NanoID (one account per patient — a second call for the same patient conflicts on the deterministic key), and writes the heldFor link (account→patient).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateAccount — open the ledger account for a registered patient",
				Payload: map[string]any{"patientKey": "vtx.patient.<NanoID>"},
				ExpectedOutcome: "Validates the patient is alive. Atomically commits vtx.clinicaccount.<sameNanoID> (root data {} — D5) " +
					"+ the heldFor link (account→patient). Emits account.created{accountKey, patientKey}. Returns primaryKey " +
					"(the account key). Rejects with UnknownPatient if the patient is absent, or AccountAlreadyExists " +
					"if an account already exists for this patient.",
			},
		},
	}
}

func transactionDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "clinictransaction",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"DebitAccount", "CreditAccount"},
		Description: "Ledger transaction DDL. Vertex shape: vtx.clinictransaction.<NanoID>, class=clinictransaction, root data = {} " +
			"(minimal, D5 — the entry detail is a .entry aspect). DebitAccount{accountKey, amountCents, memo?} records a " +
			"charge (a copay, an invoice line); CreditAccount{accountKey, amountCents, memo?} records a payment received. " +
			"Each mints a fresh vtx.clinictransaction.<NanoID> + a .entry aspect {type (debit|credit), amountCents, memo?, postedAt} " +
			"+ the postedTo link (transaction→account, the transaction is the later-arriving vertex so it is the source — " +
			"Contract #1 §1.1). The ledger is APPEND-ONLY — no balance is stored or mutated on the account; the ledgerHistory " +
			"lens derives a balance by summing entries, so concurrent debits/credits never race a read-modify-write. Requires " +
			"the accountKey be a live account and amountCents be a positive number.",
		Script: transactionDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"accountKey":{"type":"string","description":"vtx.clinicaccount.<NanoID> the transaction posts to (DebitAccount/CreditAccount; required, validated alive)."},` +
			`"amountCents":{"type":"number","description":"The transaction amount in integer cents; required, must be > 0. A debit is a charge (increases what the patient owes); a credit is a payment (decreases it)."},` +
			`"memo":{"type":"string","description":"Optional free-text description of the charge or payment (e.g. \"Office visit copay\", \"Insurance payment\"). Optional."}},` +
			`"required":["accountKey","amountCents"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.clinictransaction.<NanoID> of the minted transaction (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"accountKey":  "Full vtx.clinicaccount.<NanoID> key the transaction posts to. DebitAccount/CreditAccount validate it is alive and write the postedTo link (transaction→account) the ledgerHistory lens walks.",
			"amountCents": "The transaction amount in integer cents; required, must be a positive number. Stored on the .entry aspect and projected verbatim by the ledgerHistory lens.",
			"memo":        "Optional free-text description of the charge or payment (e.g. \"Office visit copay\", \"Insurance payment — claim #4471\"). Stored on the .entry aspect when supplied; projected by the ledgerHistory lens.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "DebitAccount — charge a copay",
				Payload: map[string]any{"accountKey": "vtx.clinicaccount.<NanoID>", "amountCents": 2500, "memo": "Office visit copay"},
				ExpectedOutcome: "Validates the account is alive and amountCents > 0. Atomically commits vtx.clinictransaction.<NanoID> " +
					"(root data {} — D5) + the .entry aspect {type: debit, amountCents: 2500, memo: \"Office visit copay\", postedAt} " +
					"+ the postedTo link (transaction→account). Emits account.debited{accountKey, transactionKey, amountCents}. " +
					"Returns primaryKey. Rejects UnknownAccount if the account is absent, or InvalidArgument if amountCents <= 0.",
			},
			{
				Name:    "CreditAccount — record a payment",
				Payload: map[string]any{"accountKey": "vtx.clinicaccount.<NanoID>", "amountCents": 2500, "memo": "Insurance payment — claim #4471"},
				ExpectedOutcome: "Same shape as DebitAccount, but writes .entry{type: credit, ...} and emits " +
					"account.credited{accountKey, transactionKey, amountCents}. A payment reduces what the patient owes " +
					"(the ledgerHistory-derived balance = sum(debits) − sum(credits)).",
			},
		},
	}
}
