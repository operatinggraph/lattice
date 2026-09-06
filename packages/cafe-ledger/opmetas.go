package cafeledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3) for the two ledger ops a person triggers.
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
// RefundCafeCharge is the other, and it is written in the STAFF voice
// (AuthContext "standing") because it is granted to operator/frontOfHouse at
// scope=any and to nobody at scope=self: deciding a posted charge was wrong is
// the café's call, not the call of the person who owes it. A client that filled
// an authContext target here would be refused by the script outright.
//
// Dispatch.Reads names only the account. That is the whole declared read set:
// the script's confinement walk (the account's heldFor lease, that lease's
// appliesToUnit unit, its containedIn ancestors, and the actor's worksAt link
// at each level) is a class-(e) enumeration whose keys are data-derived and so
// cannot be pre-declared by the caller — the same reason VoidCharge declares
// nothing for its own require_workplace site walk. RefundCafeCharge declares
// two more because its own reads ARE knowable client-side: the charge it
// reverses, and that charge's .entry aspect — which carries both halves of the
// refund ceiling (the charge's amount and the refundedCents already given back
// against it) and is the aspect the refund conditions its tally upsert on, so
// declaring it is what supplies the revision the CAS pins.
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
		{
			OperationType: "RefundCafeCharge",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Refund a posted charge",
				ShortLabel:  "Refund",
				Description: "Give back a charge already posted to a resident's house tab — the refund stays attached to the charge it corrects.",
				Icon:        "receipt",
				Tone:        "neutral",
				SubmitLabel: "Refund",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"accountKey":{"type":"string","description":"vtx.cafeaccount.<NanoID> of the house-tab account the charge sits on — auto-filled from the statement being viewed."},` +
				`"reversesRef":{"type":"string","description":"vtx.cafetransaction.<NanoID> of the posted charge being refunded — pre-filled from the statement line the refund was started from."},` +
				`"amountCents":{"type":"integer","title":"Refund amount","minimum":1,"description":"How much to give back, in whole cents. Cannot exceed what this charge still has un-refunded."},` +
				`"memo":{"type":"string","title":"Reason","description":"Optional note saying why the charge is being refunded — the resident sees it on their statement."}},` +
				`"required":["accountKey","reversesRef","amountCents"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "The house-tab account the charge was posted to — auto-filled by the client (dispatch.targetField), not staff-entered.",
				"reversesRef": "The charge being refunded, pre-filled from the statement line the refund was started from — normally left alone. It has to be a charge, not a payment or an earlier refund, and it has to sit on this same account; anything else is refused.",
				"amountCents": "How much to give back, entered in dollars — e.g. 4.50. A partial refund is fine, and several partial refunds may be given against one charge, but together they can never exceed the charge itself.",
				"memo":        "Optional free text saying why — \"wrong item\", \"spilled\", a ticket number. Shown to the resident on their statement, so write it for them.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class: "cafetransaction",
				// A refund carries no target: it is granted to
				// operator/frontOfHouse at scope=any and to nobody at
				// scope=self, so the caller's authority is a standing role
				// grant. A client that filled a self target would be refused
				// by the script rather than routed onto a resident branch.
				AuthContext: "standing",
				TargetField: "accountKey",
				TargetType:  "cafeaccount",
				Reads: []string{
					"{payload.accountKey}",
					"{payload.reversesRef}",
					"{payload.reversesRef}.entry",
				},
				// Two live walks the script runs: the operator-role probe
				// (workplace_exempt's short-circuit over the actor's own
				// holdsRole links) and the reversed charge's single postedTo
				// hop, proving it belongs to the account being credited. What
				// has already been refunded is NOT enumerated — it is a tally
				// on the charge's own declared-read .entry aspect, so the
				// ceiling costs one read and pins one revision.
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
					{Hub: "{payload.reversesRef}", Relation: "postedTo", Direction: "out"},
				},
			},
		},
	}
}
