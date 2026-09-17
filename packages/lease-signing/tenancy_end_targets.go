package leasesigning

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// TenancyEndTargets returns the tenancyEnd meta.weaverTarget playbook (design
// loftspace-lease-term-and-tenancy-end-design.md §2.2 +
// loftspace-residence-spine-2026-09-16.md decision 3) — frozen table,
// single-step per gap, deterministic; goal-authoring either leg would be
// ceremony (the leaseExpiry Target A shape). All four gaps are directOps
// under Weaver's service actor (the SetListingStatus cross-package
// precedent):
//
//   - missing_tenancyEnded → EndTenancy{leaseAppKey: row.entityKey} — this
//     package's own operator-granted op, which records endedAt = termEnd on
//     the .tenancy the row read and frees the applicant's per-(applicant,
//     unit) guard link so they may apply for the unit again.
//   - missing_relist → SetListingStatus{unit: row.unitKey, status: available}
//     — loftspace-domain's status-only listing transition, the same op and
//     the same Reads shape leaseApplicationComplete's missing_listingLeased
//     dispatches to drive the unit TO leased (targets.go). Its require_manages
//     binds the platform-validated self path only; Weaver's service actor
//     carries no authContext and reaches the write on the standing path, so
//     the flip back to available is admitted the way the flip to leased is.
//   - missing_residenceUnwired → UnwireResidesIn{linkKey: row.residenceLinkKey}
//     — service-location's operator-granted op, the mirror release of
//     leaseApplicationComplete's missing_residence wire (targets.go). The
//     applicant's own residesIn link to the unit this application named is
//     tombstoned once the term ends and no other live tenancy of the same
//     applicant on the same unit still needs it (tenancy_end_lenses.go).
//   - missing_availabilityFloored → FloorListingAvailability{unit: row.unitKey,
//     availableFrom: row.marketFrom} — loftspace-domain's availability floor,
//     the same op shape and the same Reads as the relist: it raises the
//     listing's availableFrom to the term's recorded end (endedAt, or a
//     notice's effective end on a live term) when the stored date sits
//     before it, preserving the rest, and never lowers a landlord's later
//     date. Independent of the relist and of the listing's status, so an
//     already-available unit with a stale date is floored too. missing_relist
//     and missing_availabilityFloored can open on the same row and both upsert
//     .listing unconditioned; a lost write re-opens its own gap and
//     re-dispatches on the next pass (self-healing, one budget count).
func TenancyEndTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{tenancyEndTarget()}
}

// tenancyEndTarget's Reads follow the leaseExpiry idiom: the gap's params are
// already in the violation row (the anchor itself, or the unit the row walked
// to), routed into the op's ContextHint.Reads so its DDL can hydrate +
// validate them, and the aspect the op rewrites is a (a) required declared
// read too via the row.<column>.<aspect> template form — .tenancy for
// EndTenancy (a row with this gap open has proven the aspect exists; the op's
// OCC pin rests on the hydrated revision that declaration produces), .listing
// for SetListingStatus and FloorListingAvailability (both rewrite it and
// refuse NoListing off it). EndTenancy's .notice is
// a (d) OptionalRead: a lease with no notice is the common case, and the op
// reads the recorded move-out from hydration to end the term there rather
// than at leaseEnd (termEnd, the same value the lens armed the timer on).
func tenancyEndTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: TenancyEndTarget,
		Description: "A signed, landlord-approved lease whose term reaches its end with no open renewal is recorded as " +
			"ended, an ended lease's unit is relisted as available unless another approved application now holds it, " +
			"and the applicant's residence at that unit is released unless another live tenancy of the same applicant " +
			"on the same unit still needs it. A unit marked leased by hand while every approved application on it has " +
			"ended is flipped back to available on every evaluation; withdrawn is the off-platform hold. A unit's " +
			"listed availability is floored at the term's recorded end — an ended term's endedAt, or a live term's " +
			"notice — so a unit is never marketed from a day its tenancy still covers; a landlord's later date is kept.",
		LensRef: TenancyEndTarget,
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_tenancyEnded": {
				Action:        "directOp",
				Operation:     "EndTenancy",
				Params:        map[string]string{"leaseAppKey": "row.entityKey"},
				Reads:         []string{"row.entityKey", "row.entityKey.tenancy"},
				OptionalReads: []string{"row.entityKey.notice"},
				// The guard release (scripts.go free_applied_to_unit_guard):
				// the unit and the applicant off the application's own links,
				// degree 1 each by construction.
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "row.entityKey", Relation: "appliesToUnit", Direction: "out"},
					{Hub: "row.entityKey", Relation: "applicationFor", Direction: "out"},
				},
			},
			"missing_relist": {
				Action:    "directOp",
				Operation: "SetListingStatus",
				Params:    map[string]string{"unit": "row.unitKey", "status": "available"},
				Reads:     []string{"row.unitKey", "row.unitKey.listing"},
			},
			"missing_residenceUnwired": {
				Action:    "directOp",
				Operation: "UnwireResidesIn",
				Params:    map[string]string{"linkKey": "row.residenceLinkKey"},
				Reads:     []string{"row.residenceLinkKey"},
			},
			"missing_availabilityFloored": {
				Action:    "directOp",
				Operation: "FloorListingAvailability",
				Params:    map[string]string{"unit": "row.unitKey", "availableFrom": "row.marketFrom"},
				Reads:     []string{"row.unitKey", "row.unitKey.listing"},
			},
		},
	}
}
