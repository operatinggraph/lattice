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
// CreditCafeAccount is the exception: recording a payment is a front-desk act with
// a human on the other side of the counter, so it grants frontOfHouse and needs
// a descriptor a client can render. The voice is STAFF-standing (AuthContext
// "standing"), matching cafe-domain's VoidCharge rather than its self-scoped
// Charge — there is no consumer grant here at all, because a resident crediting
// their own account is the one thing a ledger must never let them do.
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
				Title:       "Record a payment",
				Description: "Credit a house-tab payment received at the counter.",
				Icon:        "receipt",
				Tone:        "primary",
				SubmitLabel: "Record payment",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"accountKey":{"type":"string","description":"vtx.cafeaccount.<NanoID> of the house account being paid — auto-filled from the account being viewed."},` +
				`"amountCents":{"type":"integer","minimum":1,"description":"Amount received, in whole cents."},` +
				`"memo":{"type":"string","description":"Optional note describing the payment."}},` +
				`"required":["accountKey","amountCents"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "The house account being credited — auto-filled by the client from the account being viewed (dispatch.targetField), not user-entered. A staffer may only credit accounts whose lease sits at a location they worksAt.",
				"amountCents": "How much was received, in whole cents. Must be greater than zero; a payment reduces what the resident owes.",
				"memo":        "Optional free text describing the payment — how it was tendered, a reference number, whatever the front desk needs to recognise it later.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "cafetransaction",
				AuthContext: "standing",
				TargetField: "accountKey",
				TargetType:  "cafeaccount",
				Reads:       []string{"{payload.accountKey}"},
			},
		},
	}
}
