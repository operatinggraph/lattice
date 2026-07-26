package loftspacedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares the op-meta vertices for this package's user-facing ops.
//
// Only SetListingStatus is user-facing here. The other four ops — SetListing,
// SetUnitAddress, AssignUnitOwner, RemoveUnitOwner — are granted to `operator`
// alone, and the trusted admin tool hardcodes its own dispatch, so none is
// user-facing under S1 and none owes a descriptor. That exemption comes from
// the role, not from a `[no-op-meta:]` Note: the marker exists for an op that
// IS granted outside the trusted-tool set but still has no human dispatcher.
//
// SetListingStatus carries a single grant — consumer at scope=self — so its
// dispatch is unambiguously the self path: a landlord flips the status of a
// unit they manage, bound in-script by their own manages link.
//
// Dispatch.Class is the owning DDL's CanonicalName, never the vertical name
// (service-domain's RequestService idiom).
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "SetListingStatus",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Change listing status",
				ShortLabel:  "Set status",
				Description: "Take a unit you manage on or off the market, or mark it leased.",
				Icon:        "building",
				Tone:        "primary",
				SubmitLabel: "Update status",
				Group:       "My units",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"unit":{"type":"string","description":"vtx.unit.<NanoID> of the unit whose listing status is changing."},` +
				`"status":{"type":"string","enum":["available","pending","leased","withdrawn"],"description":"The new listing status."}},` +
				`"required":["unit","status"]}`,
			FieldDescriptions: map[string]string{
				"unit":   "The unit — filled from the unit in view, not typed. You must hold a manages link to it.",
				"status": "available = on the market; pending = under application; leased = tenancy in place; withdrawn = off-market and hidden from applicant browse, relist by setting available again. Only this field changes — the listing's economics are preserved.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       loftspaceListingDDL,
				AuthContext: "self",
				TargetField: "unit",
				TargetType:  "unit",
				// The unit and its existing .listing are both required: the op
				// rewrites only the status field and preserves the economics,
				// so it must read what is already there (ddls.go, class-(a)).
				Reads: []string{"{payload.unit}", "{payload.unit}.listing"},
				// The management link is the landlord ownership probe's own
				// declared read (ddls.go, class-(d)) — absence is a denial the
				// script raises, not a missing-key failure at dispatch. Note
				// this differs from lease-signing's landlord ops, where the
				// unit is only knowable after the application's link resolves
				// and the same probe is therefore an undeclarable class-(e).
				OptionalReads: []string{
					"lnk.identity.{actor:id}.manages.unit.{payload.unit:id}",
				},
			},
		},
	}
}
