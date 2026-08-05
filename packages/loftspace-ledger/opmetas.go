package loftspaceledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3) for the ledger ops a person triggers.
//
// DebitAccount carries none deliberately: it stays granted at scope=any to
// `operator` alone — a billing act submitted by the trusted-tool app, not
// something the descriptor vocabulary needs to render standalone. The S1 gate
// does not ask it for one (it fires on ops granted beyond the trusted-tool
// roles).
//
// LoftspaceCreateAccount and CreditAccount are the exceptions:
//
//   - LoftspaceCreateAccount — opening a lease's ledger account is what the
//     browser needs to do before the billing view can show anything but "no
//     account yet" — a front-desk act, so it grants frontOfHouse and needs a
//     descriptor a client can render. The voice is STAFF-standing (AuthContext
//     "standing"), mirroring cafe-ledger's CreditCafeAccount and
//     wellness-ledger's WellnessCreateAccount; unlike those two the grant is
//     workplace-confined in scripts.go, not unconfined.
//   - CreditAccount — its consumer scope=self grant (permissions.go) is a
//     resident-facing act, so it needs a descriptor too. Voice is
//     resident-self (AuthContext "self"), mirroring cafe-domain's Settle: the
//     ownership proof happens server-side (scripts.go's post_entry, off the
//     account's own heldFor topology), not via a templated OptionalReads
//     anchor here — the account carries no denormalized lease anchor for a
//     descriptor client to template ahead of submit.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "LoftspaceCreateAccount",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Open ledger account",
				Description: "Open the billing ledger account for a signed lease.",
				Icon:        "wallet",
				Tone:        "primary",
				SubmitLabel: "Open account",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the lease the account is for — auto-filled from the lease being viewed."}},` +
				`"required":["leaseAppKey"]}`,
			FieldDescriptions: map[string]string{
				"leaseAppKey": "The lease whose account is being opened — auto-filled by the client from the lease being viewed (dispatch.targetField), not user-entered.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "account",
				AuthContext: "standing",
				TargetField: "leaseAppKey",
				TargetType:  "leaseapp",
				Reads:       []string{"{payload.leaseAppKey}"},
			},
		},
		{
			OperationType: "CreditAccount",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Pay rent",
				Description: "Pay down what you owe on your lease's ledger account.",
				Icon:        "wallet",
				Tone:        "primary",
				SubmitLabel: "Pay",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"accountKey":{"type":"string","description":"vtx.account.<NanoID> the payment posts to — auto-filled from your lease's own ledger account."},` +
				`"amountCents":{"type":"integer","description":"Payment amount in integer cents; required, must be a positive number."},` +
				`"memo":{"type":"string","description":"Optional note (e.g. a check number)."}},` +
				`"required":["accountKey","amountCents"]}`,
			FieldDescriptions: map[string]string{
				"accountKey":  "Your lease's ledger account — auto-filled by the client (dispatch.targetField), not user-entered.",
				"amountCents": "The payment amount in integer cents; required, must be a positive number.",
				"memo":        "Optional note attached to the payment (e.g. a check number).",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "transaction",
				AuthContext: "self",
				TargetField: "accountKey",
				TargetType:  "account",
				Reads:       []string{"{payload.accountKey}"},
			},
		},
	}
}
