package clinicledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3) for the one ledger op a person triggers.
//
// DebitAccount and CreditAccount carry none deliberately: both stay granted
// at scope=any to `operator` alone — a billing-staff act submitted by the
// trusted-tool app, not something the descriptor vocabulary needs to render
// standalone. The S1 gate does not ask them for one (it fires on ops granted
// beyond the trusted-tool roles).
//
// ClinicCreateAccount is the exception: opening a patient's ledger account is
// what the browser needs to do before the billing view can show anything but
// "no account yet" — a front-desk act, so it grants frontOfHouse and needs a
// descriptor a client can render. The voice is STAFF-standing (AuthContext
// "standing"), mirroring cafe-ledger's CreditCafeAccount and
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
	}
}
