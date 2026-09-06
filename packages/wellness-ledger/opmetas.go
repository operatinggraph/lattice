package wellnessledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3) for the three ledger ops a person triggers — opening the account,
// then charging or crediting it.
//
// All three are front-desk acts, so all three grant frontOfHouse
// (permissions.go) and need a descriptor a client can render — the S1 gate
// fires on any op granted beyond the trusted-tool roles. The voice is
// STAFF-standing (AuthContext "standing"), mirroring cafe-ledger's
// CreditCafeAccount and clinic-ledger's identical three-op set.
// WellnessCreateAccount also carries a scope=self grant (permissions.go) for
// wellness-app's own hand-coded FE (which submits directly via submitOp, not
// through this descriptor) to open a member's OWN account at self-service
// booking time; no descriptor-driven client exists for wellness yet, so a
// second self-scope OpMeta variant isn't added here until one does.
//
// The WellnessDebitAccount descriptor exposes no bookingRef/priceBookingRef —
// those refs are Weaver's, minted by a settlement target (targets.go), never
// typed by a person — so every submission this descriptor drives is a MANUAL
// charge, the one shape post_entry (scripts.go) requires a non-blank memo for.
// Its schema therefore lists memo as required: a descriptor that let the field
// go empty would render a form whose submission the op refuses.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "WellnessCreateAccount",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Open ledger account",
				Description: "Open the wellness ledger account for a member.",
				Icon:        "wallet",
				Tone:        "primary",
				SubmitLabel: "Open account",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"identityKey":{"type":"string","description":"vtx.identity.<NanoID> of the member the account is for — auto-filled from the member being viewed."}},` +
				`"required":["identityKey"]}`,
			FieldDescriptions: map[string]string{
				"identityKey": "The member whose account is being opened — auto-filled by the client from the member being viewed (dispatch.targetField), not user-entered.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "wellnessaccount",
				AuthContext: "standing",
				TargetField: "identityKey",
				TargetType:  "identity",
				Reads:       []string{"{payload.identityKey}"},
			},
		},
		{
			OperationType: "WellnessDebitAccount",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Record a charge",
				Description: "Charge a member's wellness ledger — a no-show fee or a front-desk-recorded fee. The charge needs a note saying what it is for.",
				Icon:        "receipt",
				Tone:        "primary",
				SubmitLabel: "Record charge",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"accountKey":{"type":"string","description":"vtx.wellnessaccount.<NanoID> of the account being charged — auto-filled from the account being viewed."},` +
				`"amountCents":{"type":"integer","title":"Amount","minimum":1,"description":"Charge amount, in whole cents."},` +
				`"memo":{"type":"string","title":"Note","description":"What the charge is for — required, since this descriptor submits a manual charge."}},` +
				`"required":["accountKey","amountCents","memo"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "The account being charged — auto-filled by the client from the account being viewed (dispatch.targetField), not user-entered.",
				"amountCents": "How much to charge, entered in dollars — e.g. 25.00. Must be more than zero; a charge increases what the member owes.",
				"memo":        "Free text saying what the charge is for — e.g. \"No-show fee — Vinyasa Flow\". Required: this descriptor carries no bookingRef/priceBookingRef, so every submission it drives is a manual charge, and the ledger is append-only (ddls.go, scripts.go post_entry).",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "wellnesstransaction",
				AuthContext: "standing",
				TargetField: "accountKey",
				TargetType:  "wellnessaccount",
				Reads:       []string{"{payload.accountKey}"},
			},
		},
		{
			OperationType: "WellnessCreditAccount",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Record a payment",
				Description: "Credit a payment received against a member's wellness ledger.",
				Icon:        "receipt",
				Tone:        "primary",
				SubmitLabel: "Record payment",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"accountKey":{"type":"string","description":"vtx.wellnessaccount.<NanoID> of the account being paid — auto-filled from the account being viewed."},` +
				`"amountCents":{"type":"integer","title":"Amount","minimum":1,"description":"Amount received, in whole cents."},` +
				`"memo":{"type":"string","title":"Note","description":"Optional note describing the payment."}},` +
				`"required":["accountKey","amountCents"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "The account being credited — auto-filled by the client from the account being viewed (dispatch.targetField), not user-entered.",
				"amountCents": "How much was received, entered in dollars — e.g. 25.00. Must be more than zero; a payment reduces what the member owes.",
				"memo":        "Optional free text describing the payment — e.g. \"Front-desk payment\".",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "wellnesstransaction",
				AuthContext: "standing",
				TargetField: "accountKey",
				TargetType:  "wellnessaccount",
				Reads:       []string{"{payload.accountKey}"},
			},
		},
	}
}
