package cafeledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3) for the one ledger op a person triggers.
//
// CreateAccount and DebitAccount carry none deliberately: both are granted at
// scope=any to `operator` alone — CreateAccount when a resident's house tab is
// first opened, DebitAccount by the cafeTabSettlement Weaver target posting a
// settled tab. Neither is something a person decides to do, so neither has a
// form to render, and the S1 gate does not ask them for one (it fires on ops
// granted beyond the trusted-tool roles).
//
// CreditCafeAccount is the exception: recording a payment is a person-triggered
// act, so it needs a descriptor a client can render. It is now DUAL-grant
// (operator/frontOfHouse at scope=any, PLUS a resident at scope=self —
// permissions.go), so it carries ONE descriptor written in the SELF voice, per
// clinic-ledger's own dual-grant idiom (ClinicCreditAccount's opmetas.go):
// cafe-app's front-desk billing form hardcodes its own dispatch, while a
// descriptor-driven client cannot infer the self path, so the self path is
// what the descriptor must name.
//
// Dispatch.Class is "cafetransaction", the transaction DDL's own CanonicalName
// (transactionDDL) — the Contract #2 §2.1 envelope `class` DDL-hint, never the
// vertical name.
//
// Dispatch.Reads names only the account. That is the whole declared read set:
// the script's confinement walk (the account's heldFor lease, that lease's
// appliesToUnit unit, its containedIn ancestors, and the actor's worksAt link
// at each level) is a class-(e) enumeration whose keys are data-derived and so
// cannot be pre-declared by the caller — the same reason VoidCharge declares
// nothing for its own require_workplace site walk.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "CreditCafeAccount",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Pay house tab",
				Description: "Pay down what you owe on your café house tab.",
				Icon:        "receipt",
				Tone:        "primary",
				SubmitLabel: "Pay",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"accountKey":{"type":"string","description":"vtx.cafeaccount.<NanoID> of your own account — auto-filled from the account being viewed."},` +
				`"amountCents":{"type":"integer","title":"Amount","minimum":1,"description":"Payment amount, in whole cents."},` +
				`"memo":{"type":"string","title":"Note","description":"Optional note describing the payment."}},` +
				`"required":["accountKey","amountCents"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "Your own house-tab account — auto-filled by the client (dispatch.targetField), not user-entered.",
				"amountCents": "How much you're paying, entered in dollars — e.g. 4.50. Must be more than zero and cannot exceed what you actually owe (server-verified).",
				"memo":        "Optional free text describing the payment — a reference number, whatever helps you recognise it later.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "cafetransaction",
				AuthContext: "self",
				TargetField: "accountKey",
				TargetType:  "cafeaccount",
				Reads:       []string{"{payload.accountKey}"},
				// The operator-role confinement probe: workplace_exempt's
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (scripts.go actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
	}
}
