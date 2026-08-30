package loftspacedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares the op-meta vertices for this package's user-facing ops.
//
// SetListingStatus carries a single grant — consumer at scope=self — so its
// dispatch is unambiguously the self path: a landlord flips the status of a
// unit they manage, bound in-script by their own manages link.
//
// SetListing, SetUnitAddress, and AssignUnitOwner are granted to `operator`
// alone (permissions.go's mk() helper — scope=any, no consumer/landlord row),
// so each is AuthContext "standing": the shipped loftspace-app posts a real
// landlord-facing listing form against them (submitPostListing, app.js), which
// is exactly the app-seam rule (vertical-package-standard.md §15) — a shipped
// screen is proof a person triggers the op, whatever its grant roles.
// RemoveUnitOwner stays bare: no `cmd/*-app` source references it, so it
// carries no app-seam obligation, and the trusted admin tool (an operator
// calling it directly) hardcodes its own dispatch.
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
				`"status":{"type":"string","title":"New status","enum":["available","pending","leased","withdrawn"],"description":"The new listing status."}},` +
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
		{
			OperationType: "SetListing",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Publish listing economics",
				Description: "Set the rent, term, and availability for a unit's listing.",
				Icon:        "building",
				Tone:        "primary",
				SubmitLabel: "Save listing",
				Group:       "My units",
			},
			// A full replace (ddls.go SetListing writes the whole .listing
			// aspect), so the schema names every field, not just the ones
			// changing.
			InputSchema: `{"type":"object","properties":` +
				`{"unit":{"type":"string","description":"vtx.unit.<NanoID> of the unit — auto-filled from the unit being viewed."},` +
				`"rentAmount":{"type":"number","title":"Monthly rent","description":"Monthly rent. Must be > 0."},` +
				`"rentCurrency":{"type":"string","title":"Currency","description":"ISO currency code for rentAmount, e.g. USD."},` +
				`"bedrooms":{"type":"integer","title":"Bedrooms","description":"Bedroom count. Must be >= 0."},` +
				`"bathrooms":{"type":"number","title":"Bathrooms","description":"Optional bathroom count, may be fractional e.g. 1.5. Must be >= 0."},` +
				`"sqft":{"type":"integer","title":"Square feet","description":"Optional floor area in square feet. Must be > 0."},` +
				`"availableFrom":{"type":"string","format":"date-time","title":"Available from","description":"Earliest move-in date."},` +
				`"leaseTermMonths":{"type":"integer","title":"Lease term (months)","description":"Lease term in months. Must be > 0."},` +
				`"status":{"type":"string","title":"Status","enum":["available","pending","leased","withdrawn"],"description":"Listing availability. 'withdrawn' is off-market and hidden from applicant browse; relist by setting available again."}},` +
				`"required":["unit","rentAmount","rentCurrency","bedrooms","availableFrom","leaseTermMonths","status"]}`,
			FieldDescriptions: map[string]string{
				"unit":            "The unit being listed — auto-filled by the client from the unit being viewed (dispatch.targetField), not user-entered.",
				"rentAmount":      "Monthly rent. REPLACES the stored value, so a re-submit must resupply it.",
				"rentCurrency":    "ISO currency code (e.g. USD) for rentAmount.",
				"bedrooms":        "Bedroom count.",
				"bathrooms":       "Optional bathroom count (may be fractional, e.g. 1.5). Omitted clears any existing value (full replace).",
				"sqft":            "Optional floor area in square feet. Omitted clears any existing value (full replace).",
				"availableFrom":   "Earliest move-in date.",
				"leaseTermMonths": "Lease term in whole months.",
				"status":          "Listing availability. 'withdrawn' takes the unit off-market (hidden from applicant browse; relist via status=available). Setting only THIS field going forward — without resupplying the economics — is SetListingStatus, a separate op.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       loftspaceListingDDL,
				AuthContext: "standing",
				TargetField: "unit",
				TargetType:  "unit",
				Reads:       []string{"{payload.unit}"},
			},
		},
		{
			OperationType: "SetUnitAddress",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Set unit address",
				Description: "Record a unit's street address.",
				Icon:        "building",
				Tone:        "primary",
				SubmitLabel: "Save address",
				Group:       "My units",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"unit":{"type":"string","description":"vtx.unit.<NanoID> of the unit — auto-filled from the unit being viewed."},` +
				`"line1":{"type":"string","title":"Address line 1","description":"Street address line 1."},` +
				`"line2":{"type":"string","title":"Address line 2","description":"Optional street address line 2."},` +
				`"city":{"type":"string","title":"City","description":"City."},` +
				`"region":{"type":"string","title":"State / region","description":"State / province / region."},` +
				`"postal":{"type":"string","title":"Postal code","description":"Postal / ZIP code."}},` +
				`"required":["unit","line1","city","region","postal"]}`,
			FieldDescriptions: map[string]string{
				"unit":   "The unit whose address is being set — auto-filled by the client from the unit being viewed (dispatch.targetField), not user-entered.",
				"line1":  "Street address line 1. REPLACES the stored value, so a re-submit must resupply it.",
				"line2":  "Optional street address line 2. Omitted clears any existing value (full replace).",
				"city":   "City.",
				"region": "State / province / region.",
				"postal": "Postal / ZIP code.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       loftspaceListingDDL,
				AuthContext: "standing",
				TargetField: "unit",
				TargetType:  "unit",
				Reads:       []string{"{payload.unit}"},
			},
		},
		{
			OperationType: "AssignUnitOwner",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Assign unit manager",
				Description: "Record that an identity manages (owns / property-manages) this unit.",
				Icon:        "building",
				Tone:        "primary",
				SubmitLabel: "Assign",
				Group:       "My units",
			},
			// landlord stays a plain field, never a ContextParam auto-fill from
			// {actor}: the op is granted operator-only (no self path), and
			// enforce_manages (ownership.go) places no actor==landlord
			// constraint on who may be conferred — an operator (or an app
			// dispatching on an operator's behalf) may assign ANY identity as
			// manager, most often the signed-in landlord's own identity when
			// self-onboarding a listing (cmd/loftspace-app/web/app.js
			// submitPostListing), but not exclusively.
			InputSchema: `{"type":"object","properties":` +
				`{"landlord":{"type":"string","title":"Manager","description":"vtx.identity.<NanoID> of the identity to record as the unit's manager."},` +
				`"unit":{"type":"string","description":"vtx.unit.<NanoID> of the unit — auto-filled from the unit being viewed."}},` +
				`"required":["landlord","unit"]}`,
			FieldDescriptions: map[string]string{
				"landlord": "The identity to record as this unit's manager — often the signed-in landlord's own identity when self-onboarding a listing, but any identity may be named.",
				"unit":     "The unit being assigned a manager — auto-filled by the client from the unit being viewed (dispatch.targetField), not user-entered.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       loftspaceOwnershipDDL,
				AuthContext: "standing",
				TargetField: "unit",
				TargetType:  "unit",
				Reads:       []string{"{payload.landlord}", "{payload.unit}"},
				// Two DIFFERENT keys, both class-(d) per ownership.go's own
				// read-posture annotations, both absence-tolerant: the actor's
				// own manages probe (enforce_manages — absence is the normal
				// case for an operator, whose actor_holds_operator exemption
				// never reaches this read at all, and is the denial signal for
				// any other caller "conferring on someone else"), and the
				// (landlord, unit) pair's own link — the create/revive/no-op
				// idempotency check, absent on every first assignment.
				OptionalReads: []string{
					"lnk.identity.{actor:id}.manages.unit.{payload.unit:id}",
					"lnk.identity.{payload.landlord:id}.manages.unit.{payload.unit:id}",
				},
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
	}
}
