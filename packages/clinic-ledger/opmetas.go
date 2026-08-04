package clinicledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3) for the three ledger ops a person triggers — opening the account,
// then charging or crediting it.
//
// All three are front-desk acts, so all three grant frontOfHouse (permissions.go)
// and need a descriptor a client can render — the S1 gate fires on any op
// granted beyond the trusted-tool roles. The voice is STAFF-standing
// (AuthContext "standing"), mirroring cafe-ledger's CreditCafeAccount and
// wellness-ledger's WellnessCreateAccount.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
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
				`"memo":{"type":"string","title":"Note","description":"Optional note describing the charge."}},` +
				`"required":["accountKey","amountCents"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "The account being charged — auto-filled by the client from the account being viewed (dispatch.targetField), not user-entered.",
				"amountCents": "How much to charge, entered in dollars — e.g. 25.00. Must be more than zero; a charge increases what the patient owes.",
				"memo":        "Optional free text describing the charge — e.g. \"Office visit copay\".",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "clinictransaction",
				AuthContext: "standing",
				TargetField: "accountKey",
				TargetType:  "clinicaccount",
				Reads:       []string{"{payload.accountKey}"},
			},
		},
		{
			OperationType: "ClinicCreditAccount",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Record a payment",
				Description: "Credit a payment received against a patient's billing ledger.",
				Icon:        "receipt",
				Tone:        "primary",
				SubmitLabel: "Record payment",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"accountKey":{"type":"string","description":"vtx.clinicaccount.<NanoID> of the account being paid — auto-filled from the account being viewed."},` +
				`"amountCents":{"type":"integer","title":"Amount","minimum":1,"description":"Amount received, in whole cents."},` +
				`"memo":{"type":"string","title":"Note","description":"Optional note describing the payment."}},` +
				`"required":["accountKey","amountCents"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "The account being credited — auto-filled by the client from the account being viewed (dispatch.targetField), not user-entered.",
				"amountCents": "How much was received, entered in dollars — e.g. 25.00. Must be more than zero; a payment reduces what the patient owes.",
				"memo":        "Optional free text describing the payment — e.g. \"Insurance payment — claim #4471\".",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "clinictransaction",
				AuthContext: "standing",
				TargetField: "accountKey",
				TargetType:  "clinicaccount",
				Reads:       []string{"{payload.accountKey}"},
			},
		},
	}
}
