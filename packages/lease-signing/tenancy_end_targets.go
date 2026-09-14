package leasesigning

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// TenancyEndTargets returns the tenancyEnd meta.weaverTarget playbook (design
// loftspace-lease-term-and-tenancy-end-design.md §2.2) — frozen table,
// single-step per gap, deterministic; goal-authoring either leg would be
// ceremony (the leaseExpiry Target A shape). Both gaps are directOps under
// Weaver's service actor (the SetListingStatus cross-package precedent):
//
//   - missing_tenancyEnded → EndTenancy{leaseAppKey: row.entityKey} — this
//     package's own operator-granted op, which records endedAt = leaseEnd on
//     the .tenancy the row read.
//   - missing_relist → SetListingStatus{unit: row.unitKey, status: available}
//     — loftspace-domain's status-only listing transition, the same op and
//     the same Reads shape leaseApplicationComplete's missing_listingLeased
//     dispatches to drive the unit TO leased (targets.go). Its require_manages
//     binds the platform-validated self path only; Weaver's service actor
//     carries no authContext and reaches the write on the standing path, so
//     the flip back to available is admitted the way the flip to leased is.
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
// for SetListingStatus (its NoListing guard reads it).
func tenancyEndTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: TenancyEndTarget,
		Description: "A signed, landlord-approved lease whose term reaches its end with no open renewal is recorded as " +
			"ended, and an ended lease's unit is relisted as available unless another tenant now holds it.",
		LensRef: TenancyEndTarget,
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_tenancyEnded": {
				Action:    "directOp",
				Operation: "EndTenancy",
				Params:    map[string]string{"leaseAppKey": "row.entityKey"},
				Reads:     []string{"row.entityKey", "row.entityKey.tenancy"},
			},
			"missing_relist": {
				Action:    "directOp",
				Operation: "SetListingStatus",
				Params:    map[string]string{"unit": "row.unitKey", "status": "available"},
				Reads:     []string{"row.unitKey", "row.unitKey.listing"},
			},
		},
	}
}
