package cafeledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3) for the three ledger ops a person triggers.
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
// The descriptor's `reason` field is the one payload choice the self voice has
// to warn about: a front-desk submit may set "waiver" to forgive the balance
// instead of recording cash, and the script refuses that on the self-scoped
// leg — a resident pays their tab down, never writes it off.
//
// RefundCafeCharge and PayoutCafeCredit are the other two, both written in the
// STAFF voice (AuthContext "standing") because each is granted to
// operator/frontOfHouse at scope=any and to nobody at scope=self: deciding a
// posted charge was wrong is the café's call, not the call of the person who
// owes it, and handing cash back against a credit is a till act a resident
// cannot perform on themselves. A client that filled an authContext target on
// either would be refused by the script outright.
//
// Dispatch.Reads names only the account, and Dispatch.OptionalReads that
// account's .balance aspect. Together those are the whole declared read set:
// the script's confinement walk (the account's heldFor lease, that lease's
// appliesToUnit unit, its containedIn ancestors, and the actor's worksAt link
// at each level) is a class-(e) enumeration whose keys are data-derived and so
// cannot be pre-declared by the caller — the same reason VoidCharge declares
// nothing for its own require_workplace site walk. RefundCafeCharge declares
// two more READS because its own are knowable client-side: the charge it
// reverses, and that charge's .entry aspect — which carries both halves of the
// refund ceiling (the charge's amount and the refundedCents already given back
// against it) and is the aspect the refund conditions its tally upsert on, so
// declaring it is what supplies the revision the CAS pins.
//
// .balance sits in OptionalReads rather than Reads on all three ops. It is the
// account's maintained running total, the O(1) quantity CreditCafeAccount's
// amount cap and PayoutCafeCredit's credit cap are measured against and the one
// thing on the account a posted entry updates — but an account minted under
// cafe-ledger < 0.4.0 does not carry it, and a required read would
// HydrationMiss-reject every entry against such an account instead of letting a
// payment (or a payout) backfill it.
//
// The declaration DOCUMENTS that read set; it does not guarantee it. What
// guarantees it is the transaction DDL's own derive_reads(op) (Contract #2 §2.5
// class (g)), which returns the same key at the head of step 4 for every
// dispatch of these ops — a descriptor is a hint a client may ignore, and this
// key is what auto-conditions the update the script emits for it (Contract #3
// §3.2), so a submitter that omitted it would otherwise get an unconditioned
// update and lose one of two concurrent entries.
//
// EvaluateCafeArrears and the arrears notification replyOp carry a bare
// OpMetaSpec — no Presentation, no Dispatch — for discoverability alone, parity
// with wellness-reminders' own reminder + replyOp metas. Neither has a form to
// render: Weaver's actuator resolves the first from the §10.8 playbook and the
// bridge resolves the second from the event body, so neither reads a descriptor.
// The S1 gate does not ask them for one either — both are granted to `operator`
// alone.
func OpMetas() []pkgmgr.OpMetaSpec {
	return append([]pkgmgr.OpMetaSpec{
		{
			OperationType: "CreditCafeAccount",
			// refusal-courtesy(facet): InvalidState: none — accountKey is dispatch.targetField-resolved from the entity being viewed, never picked from a Facet-rendered list; the balance aspect's wrong class is a data-integrity fault, not a lens-projected column
			// refusal-courtesy(facet): NoCreditToPayOut, PayoutExceedsCash, PayoutExceedsCredit: unreachable — CreditCafeAccount calls post_entry(entry_type="credit", ...) (scripts.go); is_payout requires entry_type == "debit", so the payout branch never runs
			// refusal-courtesy(facet): RefundExceedsCharge, RefundExceedsPaid: unreachable — CreditCafeAccount calls post_entry(..., allow_reverses_ref=False, ...) (scripts.go); the reversesRef branch only runs when allow_reverses_ref is True
			// refusal-courtesy(facet): NoBalanceToPay, PaymentExceedsBalance, WriteOffExceedsBalance: none — amountCents carries no maximum tied to the account's own live balance (InputSchema below), and no edge-manifest entity lens projects that balance as a column an entityRefCandidates picker could filter on, so Facet's generic form offers no dynamic bound
			// refusal-courtesy(facet): CounterPaymentAlreadyPosted, CounterPaymentMismatch, NoCounterPayment, TabNotSettled: unreachable — this descriptor's InputSchema names no tabRef (the Weaver-only field only cafeTabSettlement's missing_payment dispatch sets), and require_counter_payment (scripts.go) runs only when payload.tabRef is present
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
				`"memo":{"type":"string","title":"Note","description":"Optional note describing the payment."},` +
				`"reason":{"type":"string","enum":["payment","waiver"],"description":"Optional, defaults to \"payment\". A front-desk/operator submit may set \"waiver\" to write off what is owed instead of recording cash collected; server-rejected on a self-scoped submit — you may only pay down your own house tab, never write it off."}},` +
				`"required":["accountKey","amountCents"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "Your own house-tab account — auto-filled by the client (dispatch.targetField), not user-entered.",
				"amountCents": "How much you're paying, entered in dollars — e.g. 4.50. Must be more than zero and cannot exceed what you actually owe (server-verified).",
				"memo":        "Optional free text describing the payment — a reference number, whatever helps you recognise it later.",
				"reason":      "\"payment\" or \"waiver\" (default \"payment\"). Front-desk/operator only — a self-scoped submit is rejected server-side if set to \"waiver\". A write-off is capped at what is owed exactly as a payment is.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "cafetransaction",
				AuthContext: "self",
				TargetField: "accountKey",
				TargetType:  "cafeaccount",
				Reads:       []string{"{payload.accountKey}"},
				// The account's own .balance aspect post_entry maintains — the
				// O(1) source of the amount-owed cap. Absence-tolerant (not
				// Reads) so a payment against an account minted under
				// cafe-ledger < 0.4.0 backfills its .balance instead of
				// HydrationMiss-rejecting.
				//
				// .arrears rides beside it, absence-tolerant for the same two
				// reasons: no account carries the aspect until something opens an
				// arrears episode on it, and the episode write post_entry emits
				// for it is a bare update that is only auto-conditioned on the
				// hydrated revision because the key is declared.
				OptionalReads: []string{"{payload.accountKey}.balance", "{payload.accountKey}.arrears"},
				// Two walks the script runs: the operator-role confinement
				// probe (workplace_exempt's short-circuit over the actor's own
				// holdsRole links, scripts.go actor_holds_operator), and — only
				// against an account carrying no .balance yet, or one whose
				// .balance predates cashCents — the bounded postedTo replay
				// that computes the aspect's two numbers. Every leg a guard
				// binds runs that replay (a payment or write-off for the owed
				// cap, a refund for the cash invariant, a payout for the credit
				// cap), so all three descriptors declare it; only a charge
				// never does.
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
					{Hub: "{payload.accountKey}", Relation: "postedTo", Direction: "in"},
				},
			},
		},
		{
			OperationType: "RefundCafeCharge",
			// refusal-courtesy(facet): InvalidState: none — accountKey is dispatch.targetField-resolved from the entity being viewed, never picked from a Facet-rendered list; the balance aspect's wrong class is a data-integrity fault, not a lens-projected column
			// refusal-courtesy(facet): NoCreditToPayOut, PayoutExceedsCash, PayoutExceedsCredit: unreachable — RefundCafeCharge calls post_entry(entry_type="credit", ...) (scripts.go); is_payout requires entry_type == "debit", so the payout branch never runs
			// refusal-courtesy(facet): RefundExceedsCharge: none — reversesRef carries no x-entityRef annotation (this file's InputSchema), so Facet renders it as a plain text field, not an entityRefCandidates picker that could drop an exhausted charge
			// refusal-courtesy(facet): RefundExceedsPaid: none — cashCents (the account's cash-floor) is never projected by any edge-manifest entity lens, so no Facet column could bound a refund against it
			// refusal-courtesy(facet): NoBalanceToPay, PaymentExceedsBalance, WriteOffExceedsBalance: unreachable — RefundCafeCharge calls post_entry(..., allow_reverses_ref=True, ...) (scripts.go); is_payment requires not allow_reverses_ref, so the whole is_payment block these codes live in never runs
			// refusal-courtesy(facet): CounterPaymentAlreadyPosted, CounterPaymentMismatch, NoCounterPayment, TabNotSettled: unreachable — RefundCafeCharge refuses any tabRef InvalidArgument before post_entry and calls it with allow_tab_ref=False (scripts.go), so require_counter_payment never runs
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
				// The account's own .balance aspect, absence-tolerant for the
				// same reason CreditCafeAccount declares it that way: a refund
				// is not capped by the balance, but it IS bounded by the cash
				// the account has paid in (the aspect's cashCents field — a
				// refund may never take the account further into credit than
				// that), and it keeps both numbers in lockstep like every other
				// posted entry. Where the aspect does not exist, or predates
				// cashCents, the refund computes the numbers from the postedTo
				// replay declared below and writes them. .arrears rides beside
				// it on the same terms: a refund that leaves a balance marks the
				// recorded arrears state stale, and that write is only
				// auto-conditioned because the key is declared.
				OptionalReads: []string{"{payload.accountKey}.balance", "{payload.accountKey}.arrears"},
				// Three live walks the script runs: the operator-role probe
				// (workplace_exempt's short-circuit over the actor's own
				// holdsRole links), the reversed charge's single postedTo hop,
				// proving it belongs to the account being credited, and — only
				// against an account whose .balance is absent or predates
				// cashCents — the bounded postedTo replay that computes the
				// cash floor the refund is bounded by. What has already been
				// refunded is NOT enumerated — it is a tally on the charge's
				// own declared-read .entry aspect, so the ceiling costs one
				// read and pins one revision.
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
					{Hub: "{payload.reversesRef}", Relation: "postedTo", Direction: "out"},
					{Hub: "{payload.accountKey}", Relation: "postedTo", Direction: "in"},
				},
			},
		},
		{
			OperationType: "PayoutCafeCredit",
			// refusal-courtesy(facet): InvalidState: none — accountKey is dispatch.targetField-resolved from the entity being viewed, never picked from a Facet-rendered list; the balance aspect's wrong class is a data-integrity fault, not a lens-projected column
			// refusal-courtesy(facet): NoCreditToPayOut, PayoutExceedsCredit, PayoutExceedsCash: none — accountKey is dispatch.targetField-resolved from context; Facet renders no account picker, and no entity lens projects a per-account credit/cash column entityRefCandidates could filter on
			// refusal-courtesy(facet): RefundExceedsCharge, RefundExceedsPaid: unreachable — PayoutCafeCredit calls post_entry(..., allow_reverses_ref=False, ...) (scripts.go); the reversesRef branch only runs when allow_reverses_ref is True
			// refusal-courtesy(facet): NoBalanceToPay, PaymentExceedsBalance, WriteOffExceedsBalance: unreachable — PayoutCafeCredit calls post_entry(state, op, "debit", ...) (scripts.go); is_payment requires entry_type == "credit", so the whole is_payment block these codes live in never runs for a debit
			// refusal-courtesy(facet): CounterPaymentAlreadyPosted, CounterPaymentMismatch, NoCounterPayment, TabNotSettled: unreachable — PayoutCafeCredit refuses any tabRef InvalidArgument before post_entry and calls it with allow_tab_ref=False (scripts.go), so require_counter_payment never runs
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Pay out credit",
				ShortLabel:  "Pay out",
				Description: "Hand back, in cash, credit a resident's house tab holds — a refunded charge they had already paid — instead of leaving it to prepay their next tabs.",
				Icon:        "receipt",
				Tone:        "neutral",
				SubmitLabel: "Pay out",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"accountKey":{"type":"string","description":"vtx.cafeaccount.<NanoID> of the house-tab account holding the credit — auto-filled from the statement being viewed."},` +
				`"amountCents":{"type":"integer","title":"Payout amount","minimum":1,"description":"How much cash is being handed back, in whole cents. Cannot exceed the credit the account holds."},` +
				`"memo":{"type":"string","title":"Note","description":"Optional note saying how the credit was paid out — the resident sees it on their statement."}},` +
				`"required":["accountKey","amountCents"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "The house-tab account holding the credit — auto-filled by the client (dispatch.targetField), not staff-entered.",
				"amountCents": "How much cash is being handed back, entered in dollars — e.g. 35.75. A partial payout is fine, but it can never exceed the credit the account holds, and an account that owes money or is square has nothing to pay out (server-verified).",
				"memo":        "Optional free text — \"paid from till\", a receipt number. Shown to the resident on their statement, so write it for them.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class: "cafetransaction",
				// A payout carries no target: it is granted to
				// operator/frontOfHouse at scope=any and to nobody at
				// scope=self, so the caller's authority is a standing role
				// grant. A client that filled a self target would be refused
				// by the script rather than routed onto a resident branch.
				AuthContext: "standing",
				TargetField: "accountKey",
				TargetType:  "cafeaccount",
				Reads:       []string{"{payload.accountKey}"},
				// The account's own .balance aspect post_entry maintains — the
				// O(1) source of the credit cap, and of the cashCents floor a
				// payout may never exceed either. Absence-tolerant (not Reads)
				// so a payout against an account minted under cafe-ledger
				// < 0.4.0 backfills its .balance instead of
				// HydrationMiss-rejecting.
				//
				// .arrears rides beside it, absence-tolerant for the same two
				// reasons CreditCafeAccount declares it: a payout never writes
				// the episode (its cap keeps the balance at or below zero, so
				// the debit branch never opens one), but the script reads the
				// key on every entry and the declaration is what hydrates it.
				OptionalReads: []string{"{payload.accountKey}.balance", "{payload.accountKey}.arrears"},
				// Two walks the script runs: the operator-role confinement
				// probe (workplace_exempt's short-circuit over the actor's own
				// holdsRole links, scripts.go actor_holds_operator), and — against
				// an account carrying no .balance yet, or one whose live .balance
				// predates cashCents — the bounded postedTo replay that computes
				// the aspect's numbers. A payout backfills exactly as a payment
				// does, because its cap needs the numbers just the same.
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
					{Hub: "{payload.accountKey}", Relation: "postedTo", Direction: "in"},
				},
			},
		},
		{OperationType: arrearsOp},
	}, notificationOpMetas()...)
}
