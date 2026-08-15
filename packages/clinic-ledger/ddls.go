package clinicledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `clinicaccount`
// (ClinicCreateAccount), `clinictransaction` (ClinicDebitAccount, ClinicCreditAccount), and the
// `clinicLedgerAccountGuard` aspect-type declaration (the patient-anchored
// uniqueness guard ClinicCreateAccount writes). Vertical-prefixed: a DDL
// canonicalName is global across every installed package
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
		CanonicalName:     "clinicaccount",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"ClinicCreateAccount"},
		Description: "Ledger account DDL. Vertex shape: vtx.clinicaccount.<NanoID>, class=clinicaccount, root data = {} " +
			"(minimal, D5 — the balance is LENS-derived by summing transactions, never stored). ClinicCreateAccount{patientKey} " +
			"mints the account under its OWN independently-generated NanoID (never reused from the patient — Core KV NanoIDs " +
			"are unique platform-wide identifiers, not scoped per vertex type). \"One account per patient\" is enforced by a " +
			"deterministic create-only guard aspect on the PATIENT (patientKey+\".ledgerAccount\", " +
			"clinicLedgerAccountGuard DDL) instead: a second ClinicCreateAccount for the same patient conflicts on that " +
			"already-existing aspect key. Writes the heldFor link (account→patient, the account is the later-arriving " +
			"vertex so it is the source — Contract #1 §1.1). Requires the patientKey be a live patient (no orphan accounts).",
		Script: accountDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"patientKey":{"type":"string","description":"vtx.patient.<NanoID> of the patient this account is for (ClinicCreateAccount; required, validated alive). The account gets its own independently-minted NanoID; uniqueness (one account per patient) is enforced via the patient's .ledgerAccount guard aspect, not the account's own id."}},` +
			`"required":["patientKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.clinicaccount.<NanoID> of the created account (the operation's principal key) — the caller must read this from the ACCEPTED reply, since the id can no longer be derived from patientKey."}}}`,
		FieldDescription: map[string]string{
			"patientKey": "Full vtx.patient.<NanoID> key of the patient the account is opened for. ClinicCreateAccount validates it is alive, mints the account under a fresh independent NanoID, writes the patient's .ledgerAccount guard aspect (one account per patient) and the heldFor link (account→patient).",
		},
		Examples: []pkgmgr.ExampleSpec{
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

// aspectDeclarationOnlyScript is the declaration-only Starlark for
// clinicLedgerAccountGuard — written by ClinicCreateAccount's own op handler, never
// dispatched as an operation in its own right.
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
			"expectedReimbursementCents?} records a charge (a copay, an invoice line); ClinicCreditAccount{accountKey, amountCents, memo?} " +
			"records a payment received. Each mints a fresh vtx.clinictransaction.<NanoID> + a .entry aspect " +
			"{type (debit|credit), amountCents, memo?, postedAt, billedTo? (debit only), expectedReimbursementCents? (debit+insurance only)} " +
			"+ the postedTo link (transaction→account, the transaction is the later-arriving vertex so it is the source — " +
			"Contract #1 §1.1). The ledger is APPEND-ONLY — no balance is stored or mutated on the account; the ledgerHistory " +
			"lens derives a balance by summing entries, so concurrent debits/credits never race a read-modify-write. Requires " +
			"the accountKey be a live account and amountCents be a positive number. A debit carries a bounded payer dimension — " +
			"billedTo (self|insurance, default self when omitted) and, only when billedTo is insurance, " +
			"expectedReimbursementCents (positive, capped at amountCents) — so a clinic can track what it billed insurance for " +
			"vs. what it actually collected (a ClinicCreditAccount payment) — NOT real X12 837/835 claims/clearinghouse integration, " +
			"which is out of scope for a reference vertical. Both fields reject on a ClinicCreditAccount (a payment has nothing to bill). " +
			"ClinicDebitAccount also accepts an optional appointmentRef (vtx.appointment.<NanoID>, validated alive when supplied — " +
			"UnknownAppointment otherwise): when present, writes a settles audit link (transaction→appointment) that the " +
			"clinicNoShowSettlement lens (targets.go) walks to converge the no-show-fee gap once posted. A plain " +
			"human-submitted ClinicDebitAccount (no appointmentRef) is unaffected — the field mirrors cafe-ledger's tabRef shape.",
		Script: transactionDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"accountKey":{"type":"string","description":"vtx.clinicaccount.<NanoID> the transaction posts to (ClinicDebitAccount/ClinicCreditAccount; required, validated alive)."},` +
			`"amountCents":{"type":"number","description":"The transaction amount in integer cents; required, must be > 0. A debit is a charge (increases what the patient owes); a credit is a payment (decreases it)."},` +
			`"memo":{"type":"string","description":"Optional free-text description of the charge or payment (e.g. \"Office visit copay\", \"Insurance payment\"). Optional."},` +
			`"billedTo":{"type":"string","enum":["self","insurance"],"description":"ClinicDebitAccount only; who the charge is billed to. Optional, defaults to \"self\" when omitted. Rejected on ClinicCreditAccount."},` +
			`"expectedReimbursementCents":{"type":"number","description":"ClinicDebitAccount only, and only when billedTo is \"insurance\": the amount expected back from the payer, in integer cents. Required when billedTo is \"insurance\" (rejected otherwise), must be > 0 and <= amountCents."},` +
			`"appointmentRef":{"type":"string","description":"ClinicDebitAccount only; optional vtx.appointment.<NanoID> back-reference to the no-show appointment this charge settles. When supplied, validated alive (UnknownAppointment otherwise) and a settles audit link (transaction→appointment) is written — the clinicNoShowSettlement lens reads it to converge the gap. Mirrors cafe-ledger's tabRef."}},` +
			`"required":["accountKey","amountCents"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.clinictransaction.<NanoID> of the minted transaction (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"accountKey":                 "Full vtx.clinicaccount.<NanoID> key the transaction posts to. ClinicDebitAccount/ClinicCreditAccount validate it is alive and write the postedTo link (transaction→account) the ledgerHistory lens walks.",
			"amountCents":                "The transaction amount in integer cents; required, must be a positive number. Stored on the .entry aspect and projected verbatim by the ledgerHistory lens.",
			"memo":                       "Optional free-text description of the charge or payment (e.g. \"Office visit copay\", \"Insurance payment — claim #4471\"). Stored on the .entry aspect when supplied; projected by the ledgerHistory lens.",
			"billedTo":                   "ClinicDebitAccount only: \"self\" or \"insurance\" (default \"self\" when omitted). Stored on the .entry aspect; projected by the ledgerHistory lens. Rejected on ClinicCreditAccount — a payment has nothing to bill.",
			"expectedReimbursementCents": "ClinicDebitAccount only, and only when billedTo is \"insurance\": the amount expected back from the payer, in integer cents (required then, must be > 0 and <= amountCents; rejected when billedTo is \"self\" or on a ClinicCreditAccount).",
			"appointmentRef":             "ClinicDebitAccount only: optional full vtx.appointment.<NanoID> key of the no-show appointment this charge settles. Validated alive when supplied (UnknownAppointment otherwise); writes a settles link (transaction→appointment) the clinicNoShowSettlement lens walks to converge the gap.",
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
				ExpectedOutcome: "Same as the self-pay case, plus validates appointmentRef is alive (UnknownAppointment otherwise) " +
					"and writes lnk.clinictransaction.<id>.settles.appointment.<id> (transaction→appointment). This is the shape " +
					"clinic-ledger's own clinicNoShowSettlement Weaver target dispatches — a human-submitted ClinicDebitAccount simply " +
					"omits appointmentRef and gets the plain self-pay-copay shape above.",
			},
			{
				Name:    "ClinicCreditAccount — record a payment",
				Payload: map[string]any{"accountKey": "vtx.clinicaccount.<NanoID>", "amountCents": 2500, "memo": "Insurance payment — claim #4471"},
				ExpectedOutcome: "Same shape as ClinicDebitAccount, but writes .entry{type: credit, ...} (no billedTo/expectedReimbursementCents — " +
					"rejected InvalidArgument if either is supplied) and emits account.credited{accountKey, transactionKey, amountCents}. " +
					"A payment reduces what the patient owes (the ledgerHistory-derived balance = sum(debits) − sum(credits)).",
			},
		},
	}
}
