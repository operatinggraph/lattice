package wellnessledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3) for the one ledger op a person triggers.
//
// DebitAccount and CreditAccount carry none deliberately: both stay granted
// at scope=any to `operator` alone — every wellness charge/payment today is a
// Weaver-target auto-charge (no-show fee, class price), never a
// front-desk-initiated entry. Neither is something a person decides to do, so
// neither has a form to render, and the S1 gate does not ask them for one (it
// fires on ops granted beyond the trusted-tool roles).
//
// WellnessCreateAccount is the exception: opening a member's ledger account
// is what the browser needs to do before My Classes can show a real balance
// instead of "no charges yet" — a front-desk (or self-service) act, so it
// grants frontOfHouse and needs a descriptor a client can render. The voice
// is STAFF-standing (AuthContext "standing"), mirroring cafe-ledger's
// CreditCafeAccount.
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
	}
}
