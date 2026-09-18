package clinicledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3) for the three ledger ops a person triggers — opening the account,
// then charging or crediting it.
//
// ClinicCreateAccount and ClinicDebitAccount are front-desk-only acts (grant
// frontOfHouse alone, permissions.go), so both stay in the STAFF-standing
// voice (AuthContext "standing"), mirroring cafe-ledger's CreditCafeAccount
// and wellness-ledger's WellnessCreateAccount.
//
// ClinicCreditAccount is now DUAL-grant (operator/frontOfHouse at scope=any,
// PLUS a patient at scope=self — permissions.go), so it carries ONE
// descriptor written in the SELF voice, per clinic-domain's own dual-grant
// idiom (its opmetas.go — CreateAppointment/RescheduleAppointment/
// SetAppointmentStatus each declare a single AuthContext "self" meta despite
// also granting staff): a staff FE hardcodes its own dispatch (clinic-app's
// front-desk billing form), while a descriptor-driven client cannot infer
// the self path, so the self path is what the descriptor must name.
//
// .balance sits in OptionalReads rather than Reads on both transaction ops. It
// is the account's maintained running total, the O(1) quantity a self-scoped
// ClinicCreditAccount's amount cap is measured against and the one thing on the
// account a posted entry updates — but an account minted under clinic-ledger
// < 0.3.0 does not carry it, and a required read would HydrationMiss-reject
// every entry against such an account instead of letting a self-pay backfill it.
//
// The declaration DOCUMENTS that read set; derive_reads GUARANTEES it. The
// transaction DDL's own derive_reads(op) (scripts.go, Contract #2 §2.5 class
// (g)) returns the same key at the head of step 4 for every dispatch of these
// ops — a descriptor is a hint a client may ignore, and this key is what
// auto-conditions the update the script emits for it (Contract #3 §3.2), so a
// submitter that omitted it would otherwise get an unconditioned update and lose
// one of two concurrent entries. .arrears rides beside it on both entry ops
// for the same reason: post_entry's episode write is a bare update on that
// key, and the aspect is absent on every account until an episode opens.
//
// EvaluateClinicArrears (Weaver-dispatched, operator-only) and the bridge's
// RecordClinicArrearsReminderNotification replyOp each carry a bare
// OpMetaSpec: no person triggers either, so neither has a form to describe.
func OpMetas() []pkgmgr.OpMetaSpec {
	return append([]pkgmgr.OpMetaSpec{
		{
			OperationType: "ClinicCreateAccount",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Open ledger account",
				Description: "Open the billing ledger account for a registered patient.",
				Icon:        "wallet",
				Tone:        "primary",
				SubmitLabel: "Open account",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"patientKey":{"type":"string","description":"vtx.patient.<NanoID> of the patient the account is for — auto-filled from the patient being viewed."}},` +
				`"required":["patientKey"]}`,
			FieldDescriptions: map[string]string{
				"patientKey": "The patient whose account is being opened — auto-filled by the client from the patient being viewed (dispatch.targetField), not user-entered.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "clinicaccount",
				AuthContext: "standing",
				TargetField: "patientKey",
				TargetType:  "patient",
				Reads:       []string{"{payload.patientKey}"},
				// refusal-courtesy(facet): AccountAlreadyExists: none — TargetType "patient" names an entityType no edge-manifest lens projects; Facet has no picker row to hide, and the race this code guards against has no ex-ante UI signal either way.
			},
		},
		{
			OperationType: "ClinicDebitAccount",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Record a charge",
				Description: "Charge a patient's billing ledger — a copay or invoice line.",
				Icon:        "receipt",
				Tone:        "primary",
				SubmitLabel: "Record charge",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"accountKey":{"type":"string","description":"vtx.clinicaccount.<NanoID> of the account being charged — auto-filled from the account being viewed."},` +
				`"amountCents":{"type":"integer","title":"Amount","minimum":1,"description":"Charge amount, in whole cents."},` +
				`"memo":{"type":"string","title":"Note","description":"Optional note describing the charge."},` +
				`"visitRef":{"type":"string","description":"Optional vtx.appointment.<NanoID> of the visit this charge is for — auto-filled from the visit picked on the charge form. Validated alive server-side; never the visit's fee (that is the Weaver-dispatched appointmentRef)."}},` +
				`"required":["accountKey","amountCents"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "The account being charged — auto-filled by the client from the account being viewed (dispatch.targetField), not user-entered.",
				"amountCents": "How much to charge, entered in dollars — e.g. 25.00. Must be more than zero; a charge increases what the patient owes.",
				"memo":        "Optional free text describing the charge — e.g. \"Office visit copay\".",
				"visitRef":    "Optional — the visit this charge is for, auto-filled from the visit picked on the charge form (not user-entered). The ledger history then names the visit on the line.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "clinictransaction",
				AuthContext: "standing",
				TargetField: "accountKey",
				TargetType:  "clinicaccount",
				// {payload.visitRef}: the visit the charge names, when it names
				// one — the form module drops the template of an absent optional
				// field, so a plain charge declares only the account.
				Reads: []string{"{payload.accountKey}", "{payload.visitRef}"},
				// The account's heldFor walk post_entry runs to prove a visitRef
				// names this patient's own appointment (WrongPatient). Only a
				// charge carrying visitRef walks it, but an enumeration
				// declaration is unconditional (Contract #2 §2.5 class (e)), so
				// every charge declares it; it is bounded at exactly one link.
				Enumerations: []pkgmgr.EnumerationSpec{{Hub: "{payload.accountKey}", Relation: "heldFor", Direction: "out"}},
				// The account's own .balance aspect post_entry maintains.
				// Absence-tolerant (not Reads) so a charge against an account
				// minted under clinic-ledger < 0.3.0 posts instead of
				// HydrationMiss-rejecting. Such a charge leaves that account
				// legacy rather than seeding .balance from itself alone, so this
				// op never runs the postedTo replay and declares no enumeration
				// for it — only a self-scoped ClinicCreditAccount does.
				//
				// .arrears rides beside it, absence-tolerant for the same two
				// reasons: no account carries the aspect until something opens an
				// arrears episode on it, and the episode write post_entry emits
				// for it is a bare update that is only auto-conditioned on the
				// hydrated revision because the key is declared.
				OptionalReads: []string{"{payload.accountKey}.balance", "{payload.accountKey}.arrears"},
				// refusal-courtesy(facet): InvalidState, NoFeeToSettle, WrongAccount, WrongPatient: none — TargetType "clinicaccount" names an entityType no edge-manifest lens projects; Facet has no picker row for the account, and visitRef is a plain typed field, not an x-entityRef picker Facet could drop.
				// refusal-courtesy(facet): NoBalanceToPay, PaymentExceedsBalance: unreachable — ClinicDebitAccount dispatches post_entry with entry_type="debit" (scripts.go); is_self_pay requires entry_type=="credit" on the authContextTarget branch (a debit with a target fails AuthDenied before is_self_pay is ever set), so the block these codes live in never runs for a debit
				// refusal-courtesy(facet): SelfClearing: unreachable — require_not_own_account (post_entry, scripts.go) runs only on a credit (entry_type == "credit"); ClinicDebitAccount dispatches post_entry with entry_type="debit"
			},
		},
		{
			OperationType: "ClinicCreditAccount",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Pay balance",
				Description: "Pay down what you owe on your billing ledger account.",
				Icon:        "receipt",
				Tone:        "primary",
				SubmitLabel: "Pay",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"accountKey":{"type":"string","description":"vtx.clinicaccount.<NanoID> of your own account — auto-filled from the account being viewed."},` +
				`"amountCents":{"type":"integer","title":"Amount","minimum":1,"description":"Payment amount, in whole cents."},` +
				`"memo":{"type":"string","title":"Note","description":"Optional note describing the payment."},` +
				`"reason":{"type":"string","enum":["payment","waiver"],"description":"Optional, defaults to \"payment\". A front-desk/operator submit may set \"waiver\" to forgive a charge instead of recording cash collected; server-rejected on a self-scoped submit — you may only pay down your own balance, never waive it."},` +
				`"reversesRef":{"type":"string","description":"Optional vtx.clinictransaction.<NanoID> of the open charge this waiver forgives — pre-filled from the statement line. Validated alive server-side; the history then names the charge on the credit line."}},` +
				`"required":["accountKey","amountCents"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "Your own billing account — auto-filled by the client (dispatch.targetField), not user-entered.",
				"amountCents": "How much you're paying, entered in dollars — e.g. 25.00. Must be more than zero and cannot exceed what you actually owe (server-verified).",
				"memo":        "Optional free text describing the payment — e.g. a check number.",
				"reason":      "\"payment\" or \"waiver\" (default \"payment\"). Front-desk/operator only — a self-scoped submit is rejected server-side if set to \"waiver\".",
				"reversesRef": "Optional — the open charge this waiver forgives, pre-filled from the statement line (not user-entered). A self-scoped payment never sends it.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "clinictransaction",
				AuthContext: "self",
				TargetField: "accountKey",
				TargetType:  "clinicaccount",
				// {payload.reversesRef}: the charge a waiver names, when it names
				// one — the form module drops the template of an absent optional
				// field, so a plain payment declares only the account.
				Reads: []string{"{payload.accountKey}", "{payload.reversesRef}"},
				// .arrears beside .balance: the payment's own episode write (an
				// end at zero, or a stale mark on a partial payment) is a bare
				// update conditioned on this declared key's hydrated revision.
				OptionalReads: []string{"{payload.accountKey}.balance", "{payload.accountKey}.arrears"},
				// A legacy account (no .balance aspect yet) makes a SELF-SCOPED
				// payment walk its postedTo history once to backfill the number
				// its own cap needs — bounded, and declared here per Contract #2
				// §2.5 even though most dispatches never exercise it. The self
				// leg is the only one that replays, which is why this op declares
				// the walk and ClinicDebitAccount does not.
				Enumerations: []pkgmgr.EnumerationSpec{{Hub: "{payload.accountKey}", Relation: "postedTo", Direction: "in"}},
				// refusal-courtesy(facet): InvalidState, NoFeeToSettle, WrongAccount, WrongPatient: none — TargetType "clinicaccount" names an entityType no edge-manifest lens projects; Facet has no picker row for the account, and reversesRef is a plain typed field, not an x-entityRef picker Facet could drop.
				// refusal-courtesy(facet): SelfClearing: unreachable — AuthContext "self" means every Facet submit of this op takes post_entry's authContextTarget branch (scripts.go), which refuses a waiver or a reversesRef AuthDenied; the self-clearing check lives on the staff leg (no target) this form never submits on
				// refusal-courtesy(facet): NoBalanceToPay, PaymentExceedsBalance: none — AuthContext "self" means every Facet submit of this op is self-scoped, so is_self_pay is always true server-side, but amountCents carries no maximum tied to the account's own live balance (InputSchema above), and no edge-manifest entity lens projects that balance as a column Facet could bound against
			},
		},
		{OperationType: arrearsOp},
	}, notificationOpMetas()...)
}
