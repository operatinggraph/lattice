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
//
// The WellnessDebitAccount and WellnessCreditAccount dispatches ALSO declare
// the account's own .arrears aspect in OptionalReads: post_entry (scripts.go)
// marks it stale on every posted entry, and that write is a bare update
// auto-conditioned on the step-4 hydrated revision only for a key the dispatch
// hydrated (Contract #3 §3.2). Absence-tolerant, because no account carries
// the aspect until an evaluation has run on it. The declaration DOCUMENTS that
// read set; the transaction DDL's own derive_reads GUARANTEES it for a
// submitter that omitted it.
//
// EvaluateWellnessArrears and the arrears notification replyOp carry a bare
// OpMetaSpec — no Presentation, no Dispatch — for discoverability alone, parity
// with wellness-reminders' own reminder + replyOp metas. Neither has a form to
// render: Weaver's actuator resolves the first from the §10.8 playbook and the
// bridge resolves the second from the event body, so neither reads a
// descriptor. The S1 gate does not ask them for one either — both are granted
// to `operator` alone.
func OpMetas() []pkgmgr.OpMetaSpec {
	return append([]pkgmgr.OpMetaSpec{
		{
			OperationType: "WellnessCreateAccount",
			// refusal-courtesy(facet): AccountAlreadyExists: none — no identity/member entity lens projects whether a wellnessaccount already exists for this identity; a re-submission returns AccountAlreadyExists cleanly (idempotent, the same steady state wellness-app's own ensureLedgerAccount swallows).
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
				// The account's own arrears episode state post_entry marks stale
				// (absence-tolerant: absent until the first evaluation).
				OptionalReads: []string{"{payload.accountKey}.arrears"},
			},
			// refusal-courtesy(facet): NoBalanceToPay, PaymentExceedsBalance: unreachable — AuthContext "standing" means Facet never attaches a target to this dispatch, so op.authContextTarget is always "" server-side; the self-credit balance-verification block these codes live in (post_entry's authContextTarget branch, scripts.go) only runs when a target is present
			// refusal-courtesy(facet): InvalidState: none — accountKey is dispatch.targetField-resolved from the entity being viewed, never picked from a Facet-rendered list; the arrears aspect's wrong class is a data-integrity fault (post_entry, scripts.go), not a lens-projected column
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
				// The account's own arrears episode state post_entry marks stale
				// (absence-tolerant: absent until the first evaluation).
				OptionalReads: []string{"{payload.accountKey}.arrears"},
			},
			// refusal-courtesy(facet): NoBalanceToPay, PaymentExceedsBalance: unreachable — AuthContext "standing" means Facet never attaches a target to this dispatch, so op.authContextTarget is always "" server-side; the self-credit balance-verification block these codes live in (post_entry's authContextTarget branch, scripts.go) only runs when a target is present
			// refusal-courtesy(facet): InvalidState: none — accountKey is dispatch.targetField-resolved from the entity being viewed, never picked from a Facet-rendered list; the arrears aspect's wrong class is a data-integrity fault (post_entry, scripts.go), not a lens-projected column
		},
		{OperationType: arrearsOp},
	}, notificationOpMetas()...)
}
