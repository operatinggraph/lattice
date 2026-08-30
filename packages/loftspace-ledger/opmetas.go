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
//   - DebitAccount — granted operator-only (permissions.go, no self-scope
//     row: "a resident may pay down a balance, never charge one"), so its
//     voice is STAFF-standing. loftspace-app's landlord billing view wires a
//     real "record a charge" form to it (web/app.js renderLedgerRecordForm),
//     which is the app-seam rule (vertical-package-standard.md §15) — a
//     shipped screen is proof a person triggers the op, whatever its grant
//     roles. The descriptor's InputSchema is narrower than the DDL's own
//     merged schema (clinic CreateAppointment idiom): it omits the optional
//     clauseRef/period pair (transactionDDL's "DebitAccount only" fields,
//     scripts.go post_entry) — a hand-submitted DebitAccount that supplies
//     clauseRef validates it via the PRE-HYDRATED state dict alone
//     (vertex_alive(state, clause_key), then state[clause_key + ".terms"]),
//     never a live kv.Read fallback, so honoring it would need declaring
//     {payload.clauseRef}.terms — a suffix hung off an OPTIONAL field, which
//     checkReadTemplates refuses on sight (Standard §readTemplateDebt: an
//     omitted clauseRef substitutes empty and leaves the literal ".terms"
//     behind, a malformed key NATS rejects rather than reporting absent).
//     Describing the clause-authorized shape would be a promise this
//     descriptor cannot honour; "a plain human-submitted DebitAccount omits
//     it entirely" (scripts.go) is the shape described below.
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
		},
		{
			OperationType: "DebitAccount",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Record charge",
				Description: "Charge a lease's ledger account (rent, a late fee, a deposit).",
				Icon:        "wallet",
				Tone:        "primary",
				SubmitLabel: "Record charge",
			},
			// Excludes clauseRef/period — see the package doc comment above
			// (checkReadTemplates forbids the suffix read a clause-authorized
			// charge would need to declare).
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
				AuthContext: "standing",
				TargetField: "accountKey",
				TargetType:  "account",
				Reads:       []string{"{payload.accountKey}"},
			},
		},
	}
}
