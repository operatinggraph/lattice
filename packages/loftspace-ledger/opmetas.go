package loftspaceledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3) for the ledger ops a person triggers.
//
//   - LoftspaceCreateAccount — opening a lease's ledger account is what the
//     browser needs to do before the billing view can show anything but "no
//     account yet" — a front-desk act, so it grants frontOfHouse and needs a
//     descriptor a client can render. The voice is STAFF-standing (AuthContext
//     "standing"), mirroring cafe-ledger's CreditCafeAccount and
//     wellness-ledger's WellnessCreateAccount; unlike those two the grant is
//     workplace-confined in scripts.go, not unconfined.
//   - CreditAccount — its consumer scope=self grant (permissions.go) is a
//     resident-facing act, so it needs a descriptor too. Voice is
//     resident-self (AuthContext "self"), mirroring cafe-domain's Settle: the
//     ownership proof happens server-side (scripts.go's post_entry, off the
//     account's own heldFor topology), not via a templated OptionalReads
//     anchor here — the account carries no denormalized lease anchor for a
//     descriptor client to template ahead of submit.
//   - LoftspaceRecordCharge — a person's manual charge on a lease account:
//     the landlord's consumer scope=self grant (permissions.go) makes it a
//     person-facing act, so it needs a descriptor. Voice is self (AuthContext
//     "self"), like CreditAccount: the ownership proof happens server-side
//     (scripts.go's post_entry, off the account's own
//     heldFor→appliesToUnit→manages topology), not via a templated
//     OptionalReads anchor here. loftspace-app's landlord billing view wires
//     a real "record a charge" form to it (web/app.js
//     renderLedgerRecordForm). Its payload is {accountKey, amountCents,
//     memo?} only — never clauseRef/period, which belong to DebitAccount.
//   - DebitAccount — the orchestrated, clause-authorized charge (Weaver's
//     clauseSatisfaction playbook, packages/semantic-contracts, and the CLI as
//     operator). Operator-only, no shipped screen dispatches it, so it
//     carries no descriptor: one would have to describe the clauseRef shape,
//     and a hand-submitted DebitAccount that supplies clauseRef validates it
//     via the PRE-HYDRATED state dict alone (vertex_alive(state, clause_key),
//     then state[clause_key + ".terms"]), never a live kv.Read fallback, so
//     honoring it would need declaring {payload.clauseRef}.terms — a suffix
//     hung off an OPTIONAL field, which checkReadTemplates refuses on sight
//     (Standard §readTemplateDebt: an omitted clauseRef substitutes empty and
//     leaves the literal ".terms" behind, a malformed key NATS rejects rather
//     than reporting absent).
//
// The CreditAccount and LoftspaceRecordCharge dispatches ALSO declare the
// account's own .arrears aspect in OptionalReads: post_entry (scripts.go)
// marks it stale on every posted entry, and that write is a bare update
// auto-conditioned on the step-4 hydrated revision only for a key the dispatch
// hydrated (Contract #3 §3.2). Absence-tolerant, because no account carries
// the aspect until an evaluation has run on it. The declaration DOCUMENTS that
// read set; the transaction DDL's own derive_reads GUARANTEES it for a
// submitter that omitted it.
//
// ReturnDeposit carries an InputSchema + FieldDescriptions and no Dispatch:
// it is Weaver's leaseRentSettlement dispatch (packages/semantic-contracts,
// missing_depositReturn) and the operator's CLI escape hatch, with no
// shipped screen — no Dispatch means no descriptor-driven client (Facet
// included) ever offers it, so it carries no refusal-courtesy declarations
// (a `(facet)` line on an op with no Dispatch is a stale declaration, not a
// courtesy). The schema is what names its three fields as required, the
// declaration lint-opmeta-required-fields checks the script's own
// required_string calls against, and what a CLI operator reads to spell
// the payload. Its reads need no static declaration here: the transaction
// DDL's own derive_reads supplies the whole set from the payload keys.
//
// RecordDepositDeduction and PayOutBalance both grant a consumer scope=self
// (the landlord — permissions.go), so each needs a full descriptor exactly
// like LoftspaceRecordCharge/CreditAccount: Presentation + a Dispatch
// (Class "transaction", AuthContext "self", TargetField/TargetType
// "accountKey"/"account", the same idiom). Facet's generic renderer becomes a
// SITE for both (any op with a non-nil Dispatch is), so each carries
// `(facet)` refusal-courtesy declarations for its governed codes: the
// generic renderer has no entity-lens column for a clause's own custody,
// status or remaining balance, or an account's live-computed balance, so
// every declaration is `none`. Their reads still come from the transaction
// DDL's own derive_reads (the Dispatch's own Reads/OptionalReads document
// the same set; they do not need to supply it — Contract #2 §2.5's
// derivation guarantees it whatever a caller declares).
//
// EvaluateLoftspaceArrears and the arrears notification replyOp carry a bare
// OpMetaSpec — no Presentation, no Dispatch — for discoverability alone, parity
// with wellness-ledger's own evaluate + replyOp metas. Neither has a form to
// render: Weaver's actuator resolves the first from the §10.8 playbook and the
// bridge resolves the second from the event body, so neither reads a
// descriptor. The S1 gate does not ask them for one either — both are granted
// to `operator` alone.
func OpMetas() []pkgmgr.OpMetaSpec {
	return append([]pkgmgr.OpMetaSpec{
		{
			OperationType: "LoftspaceCreateAccount",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Open ledger account",
				Description: "Open the billing ledger account for a signed lease.",
				Icon:        "wallet",
				Tone:        "primary",
				SubmitLabel: "Open account",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the lease the account is for — auto-filled from the lease being viewed."}},` +
				`"required":["leaseAppKey"]}`,
			FieldDescriptions: map[string]string{
				"leaseAppKey": "The lease whose account is being opened — auto-filled by the client from the lease being viewed (dispatch.targetField), not user-entered.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "account",
				AuthContext: "standing",
				TargetField: "leaseAppKey",
				TargetType:  "leaseapp",
				Reads:       []string{"{payload.leaseAppKey}"},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
			// refusal-courtesy(facet): AccountAlreadyExists: none — no VisibleWhen or entity lens column reports whether a lease already has a ledger account; Facet offers Open ledger account on every leaseapp row.
		},
		{
			OperationType: "CreditAccount",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Pay rent",
				Description: "Pay down what you owe on your lease's ledger account.",
				Icon:        "wallet",
				Tone:        "primary",
				SubmitLabel: "Pay",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"accountKey":{"type":"string","description":"vtx.account.<NanoID> the payment posts to — auto-filled from your lease's own ledger account."},` +
				`"amountCents":{"type":"integer","description":"Payment amount in integer cents; required, must be a positive number."},` +
				`"memo":{"type":"string","description":"Optional note (e.g. a check number)."}},` +
				`"required":["accountKey","amountCents"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "Your lease's ledger account — auto-filled by the client (dispatch.targetField), not user-entered.",
				"amountCents": "The payment amount in integer cents; required, must be a positive number.",
				"memo":        "Optional note attached to the payment (e.g. a check number).",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "transaction",
				AuthContext: "self",
				TargetField: "accountKey",
				TargetType:  "account",
				Reads:       []string{"{payload.accountKey}"},
				// The account's own arrears episode state post_entry marks stale
				// (absence-tolerant: absent until the first evaluation).
				OptionalReads: []string{"{payload.accountKey}.arrears"},
			},
			// refusal-courtesy(facet): AmountMismatch, TermExhausted: unreachable — CreditAccount's post_entry call hardcodes allow_clause_ref=False (scripts.go), so the clauseRef branch that raises these never runs for any CreditAccount dispatch.
			// refusal-courtesy(facet): InvalidState: none — accountKey is dispatch.targetField-resolved from the entity being viewed, never picked from a Facet-rendered list; the arrears aspect's wrong class is a data-integrity fault (post_entry, scripts.go), not a lens-projected column.
			// refusal-courtesy(facet): NoBalanceToPay, PaymentExceedsBalance: none — AuthContext "self" means every Facet submit of this op is self-scoped, so the self-credit balance-verification block (post_entry's authContextTarget branch, scripts.go) always runs, but amountCents carries no maximum tied to the account's own live balance (InputSchema above), and no edge-manifest entity lens projects that balance as a column Facet could bound against
		},
		{
			OperationType: "LoftspaceRecordCharge",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Record charge",
				Description: "Charge a lease's ledger account (rent, a late fee, a repair).",
				Icon:        "wallet",
				Tone:        "primary",
				SubmitLabel: "Record charge",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"accountKey":{"type":"string","description":"vtx.account.<NanoID> the charge posts to — auto-filled from the lease's own ledger account."},` +
				`"amountCents":{"type":"integer","description":"Charge amount in integer cents; required, must be a positive number."},` +
				`"memo":{"type":"string","description":"Optional note (e.g. \"June rent\", \"Late fee\")."}},` +
				`"required":["accountKey","amountCents"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "The lease's ledger account — auto-filled by the client from the lease being viewed (dispatch.targetField), not user-entered.",
				"amountCents": "The charge amount in integer cents; required, must be a positive number.",
				"memo":        "Optional note attached to the charge (e.g. \"June rent\", \"Late fee\").",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "transaction",
				AuthContext: "self",
				TargetField: "accountKey",
				TargetType:  "account",
				Reads:       []string{"{payload.accountKey}"},
				// The account's own arrears episode state post_entry marks stale
				// (absence-tolerant: absent until the first evaluation).
				OptionalReads: []string{"{payload.accountKey}.arrears"},
			},
			// refusal-courtesy(facet): AmountMismatch, TermExhausted: unreachable — LoftspaceRecordCharge's post_entry call hardcodes allow_clause_ref=False (scripts.go), so the clauseRef branch that raises these never runs for any LoftspaceRecordCharge dispatch.
			// refusal-courtesy(facet): InvalidState: none — accountKey is dispatch.targetField-resolved from the entity being viewed, never picked from a Facet-rendered list; the arrears aspect's wrong class is a data-integrity fault (post_entry, scripts.go), not a lens-projected column.
			// refusal-courtesy(facet): NoBalanceToPay, PaymentExceedsBalance: unreachable — a debit never enters the balance block: the resident branch refuses it AuthDenied first, the landlord branch has no cap.
		},
		{
			OperationType: "ReturnDeposit",
			InputSchema: `{"type":"object","properties":` +
				`{"leaseAppKey":{"type":"string","x-entityRef":"leaseapp","description":"vtx.leaseapp.<NanoID> of the lease whose .tenancy.endedAt is recorded — the end the return rides."},` +
				`"clauseKey":{"type":"string","x-entityRef":"clause","description":"vtx.clause.<NanoID> of the completed purpose=deposit clause being returned; its own .terms.amountCents is the amount credited."},` +
				`"accountKey":{"type":"string","x-entityRef":"account","description":"vtx.account.<NanoID> of the lease's ledger account the credit posts to; the clause's chargesTo link must name it."}},` +
				`"required":["leaseAppKey","clauseKey","accountKey"]}`,
			FieldDescriptions: map[string]string{
				"leaseAppKey": "The lease the deposit clause governs (ClauseLeaseMismatch otherwise; UnknownLeaseApplication when not live). Its .tenancy must record endedAt — the recorded end of the tenancy, never the notice or the scheduled term end (TenancyNotEnded while absent).",
				"clauseKey":   "The deposit clause: .terms.purpose must be deposit on a oneTime computational clause (NotADeposit otherwise) and .status must be completed, the state DebitAccount's charge leaves (DepositNotCharged while still active). A clause already returned is a no-op once the lease and account it names check out.",
				"accountKey":  "The lease's ledger account the credit posts to; the clause's chargesTo link must name it (ClauseAccountMismatch otherwise).",
			},
		},
		{
			OperationType: "RecordDepositDeduction",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Deduct from deposit",
				Description: "Deduct damage or a fee from a tenant's held security deposit.",
				Icon:        "wallet",
				Tone:        "primary",
				SubmitLabel: "Deduct",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"accountKey":{"type":"string","x-entityRef":"account","description":"vtx.account.<NanoID> the deduction posts to — auto-filled from the lease's own ledger account."},` +
				`"clauseKey":{"type":"string","x-entityRef":"clause","description":"vtx.clause.<NanoID> of the charged purpose=deposit clause the deduction is taken from."},` +
				`"amountCents":{"type":"integer","description":"Deduction amount in integer cents; required, must be a positive number, and the running total may not exceed the clause's own amountCents."},` +
				`"reason":{"type":"string","description":"Required, 1-200 characters — why the deduction was taken; recorded as the transaction's own memo."}},` +
				`"required":["accountKey","clauseKey","amountCents","reason"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "The lease's ledger account the deduction posts to — auto-filled by the client (dispatch.targetField), not user-entered.",
				"clauseKey":   "The charged security deposit clause: .terms.purpose must be deposit on a oneTime computational clause (NotADeposit otherwise) and .status.state must be completed (DepositNotHeld otherwise).",
				"amountCents": "The deduction amount in integer cents, required and positive. Refused DeductionExceedsDeposit once the clause's own .deductions.totalCents + amountCents would exceed its .terms.amountCents.",
				"reason":      "Required, 1-200 characters. Recorded as the transaction's own memo — the line the statement shows for this deduction.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "transaction",
				AuthContext: "self",
				TargetField: "accountKey",
				TargetType:  "account",
				Reads:       []string{"{payload.accountKey}", "{payload.clauseKey}"},
			},
			// refusal-courtesy(facet): ClauseAccountMismatch: none — no entity lens projects which account a clause's chargesTo link names; the generic renderer offers this op on every account row regardless of which clause is picked.
			// refusal-courtesy(facet): DepositNotHeld: none — no entity lens column reports a deposit clause's own .status.state for the generic form to pre-filter on.
			// refusal-courtesy(facet): DeductionExceedsDeposit: none — the clause's own remaining balance is not a column any entity lens projects, so the generic form's InputSchema minimum/maximum cannot bound amountCents against it.
			// refusal-courtesy(facet): InvalidState: none — CreateClause writes the deposit clause's .status unconditionally, so its absence is a data-integrity fault, never a lens-projected state.
		},
		{
			OperationType: "PayOutBalance",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Pay out balance",
				Description: "Pay an ended tenancy's whole credit balance out to the tenant.",
				Icon:        "wallet",
				Tone:        "primary",
				SubmitLabel: "Pay out",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"accountKey":{"type":"string","x-entityRef":"account","description":"vtx.account.<NanoID> the payout debits — auto-filled from the lease's own ledger account; the amount is computed from its own postedTo history, never trusted from the payload."},` +
				`"leaseAppKey":{"type":"string","x-entityRef":"leaseapp","description":"vtx.leaseapp.<NanoID> of the lease the account is held for (AccountLeaseMismatch otherwise); its .tenancy must record endedAt (TenancyNotEnded otherwise)."}},` +
				`"required":["accountKey","leaseAppKey"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "The lease's ledger account to pay out — auto-filled by the client (dispatch.targetField), not user-entered. The amount is computed from its own postedTo history and refuses NoCreditBalance when nothing is owed back.",
				"leaseAppKey": "The lease the account is held for (AccountLeaseMismatch otherwise); its .tenancy.endedAt must be recorded (TenancyNotEnded otherwise).",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "transaction",
				AuthContext: "self",
				TargetField: "accountKey",
				TargetType:  "account",
				Reads:       []string{"{payload.accountKey}", "{payload.leaseAppKey}"},
				// The account's own arrears episode state post_entry-style
				// marks stale (absence-tolerant: absent until the first
				// evaluation) — PayOutBalance is an ordinary debit.
				OptionalReads: []string{"{payload.accountKey}.arrears"},
			},
			// refusal-courtesy(facet): AccountLeaseMismatch: none — no entity lens projects the lease an account is held for, so the generic form cannot pre-filter the leaseAppKey picker against it.
			// refusal-courtesy(facet): TenancyNotEnded: none — no entity lens column projects .tenancy.endedAt for the generic form to gate on.
			// refusal-courtesy(facet): NoCreditBalance: none — the account's live balance is computed from its own history at dispatch time, not a lens-projected column the generic form could read ahead of submit.
			// refusal-courtesy(facet): HistoryTooLong: none — an account history long enough to exhaust the replay budget is an operator-visible edge case; no lens column reports it.
			// refusal-courtesy(facet): InvalidState: none — the account's .arrears aspect carrying a class other than loftspaceAccountArrears is a data-integrity fault (post_entry's arrears_stale_mark), never a lens-projected column.
		},
		{OperationType: arrearsOp},
	}, notificationOpMetas()...)
}
