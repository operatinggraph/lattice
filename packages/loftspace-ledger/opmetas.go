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
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
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
			},
			// refusal-courtesy(facet): AmountMismatch, InvalidState, TermExhausted: unreachable — CreditAccount's post_entry call hardcodes allow_clause_ref=False (scripts.go), so the clauseRef branch that raises these never runs for any CreditAccount dispatch.
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
			},
			// refusal-courtesy(facet): AmountMismatch, InvalidState, TermExhausted: unreachable — LoftspaceRecordCharge's post_entry call hardcodes allow_clause_ref=False (scripts.go), so the clauseRef branch that raises these never runs for any LoftspaceRecordCharge dispatch.
			// refusal-courtesy(facet): NoBalanceToPay, PaymentExceedsBalance: unreachable — a debit never enters the balance block: the resident branch refuses it AuthDenied first, the landlord branch has no cap.
		},
	}
}
