package wellnessledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `wellnessaccount`
// (WellnessCreateAccount), `wellnesstransaction` (WellnessDebitAccount, WellnessCreditAccount), and
// the `wellnessLedgerAccountGuard` aspect-type declaration (the
// identity-anchored uniqueness guard WellnessCreateAccount writes). Vertical-prefixed:
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
		PermittedCommands: []string{"WellnessCreateAccount"},
		Description: "Ledger account DDL. Vertex shape: vtx.wellnessaccount.<NanoID>, class=wellnessaccount, root data = {} " +
			"(minimal, D5 — the balance is LENS-derived by summing transactions, never stored). WellnessCreateAccount{identityKey} " +
			"mints the account under its OWN independently-generated NanoID (never reused from the identity — Core KV " +
			"NanoIDs are unique platform-wide identifiers, not scoped per vertex type). \"One account per member\" is " +
			"enforced by a deterministic create-only guard aspect on the IDENTITY (identityKey+\".wellnessLedgerAccount\", " +
			"wellnessLedgerAccountGuard DDL) instead: a second WellnessCreateAccount for the same identity conflicts on that " +
			"already-existing aspect key. Writes the heldFor link (account→identity, the account is the later-arriving " +
			"vertex so it is the source — Contract #1 §1.1). Requires the identityKey be a live identity (no orphan accounts).",
		Script: accountDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"identityKey":{"type":"string","description":"vtx.identity.<NanoID> of the member this account is for (WellnessCreateAccount; required, validated alive). The account gets its own independently-minted NanoID; uniqueness (one account per member) is enforced via the identity's .wellnessLedgerAccount guard aspect, not the account's own id."}},` +
			`"required":["identityKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.wellnessaccount.<NanoID> of the created account (the operation's principal key) — the caller must read this from the ACCEPTED reply, since the id can no longer be derived from identityKey."}}}`,
		FieldDescription: map[string]string{
			"identityKey": "Full vtx.identity.<NanoID> key of the member the account is opened for. WellnessCreateAccount validates it is alive, mints the account under a fresh independent NanoID, writes the identity's .wellnessLedgerAccount guard aspect (one account per member) and the heldFor link (account→identity).",
		},
		Examples: []pkgmgr.ExampleSpec{
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

// aspectDeclarationOnlyScript is the declaration-only Starlark for
// wellnessLedgerAccountGuard — written by WellnessCreateAccount's own op handler,
// never dispatched as an operation in its own right.
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
			"authContextTarget branch — since a member may pay down their own balance but never forgive or refund it.",
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
