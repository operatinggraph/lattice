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
		{OperationType: arrearsOp},
	}, notificationOpMetas()...)
}
