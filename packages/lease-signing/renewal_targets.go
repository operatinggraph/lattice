package leasesigning

import (
	"encoding/json"

	"github.com/asolgan/lattice/internal/pkgmgr"
)

// RenewalTargets returns the package's two renewal meta.weaverTarget playbooks
// (design loftspace-lease-renewal-goal-authored-target-design.md §4.2/§4.3):
//
//   - leaseExpiry (Target A, frozen table — single-step, deterministic;
//     goal-authoring it would be ceremony, §4.2): missing_renewalCycle →
//     directOp OpenRenewal{leaseApp: row.entityKey}, Weaver's service actor
//     (the SetListingStatus cross-package directOp precedent).
//   - renewalComplete (Target B, mode: planned — the FIRST goal-authored
//     target, §4.3): one goal `allOf` over per-tenant-variable facts (fresh
//     bgcheck; guarantor re-verify only if one exists via `anyOf`; terms set;
//     signed), with a 4-action catalog the planner sequences. signRenewal's
//     `pre` is the goal's FULL remainder — the terminal-leg rule (§4.3/§5):
//     because SignRenewal's commit flips the completion scalar, an under-
//     specified `pre` plus the canonical tie-break ("signRenewal" <
//     "verifyGuarantor" lexicographically) would order signing BEFORE
//     verification and close the gap with the guarantor atom permanently
//     unmet — this is the B1 regression the design's adversarial pass caught.
func RenewalTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		leaseExpiryTarget(),
		renewalCompleteTarget(),
	}
}

// leaseExpiryTarget is Target A. Reads = [row.entityKey]: the leaseapp key is
// already in the violation row (the anchor itself), routed into OpenRenewal's
// ContextHint.Reads so its DDL can hydrate + validate it (and its .tenancy
// aspect, read on-demand inside the op — not a declared Reads entry, mirroring
// how SetApplicantProfile reads the unit's .listing on demand).
func leaseExpiryTarget() pkgmgr.WeaverTargetSpec {
	return pkgmgr.WeaverTargetSpec{
		TargetID: "leaseExpiry",
		LensRef:  "leaseExpiry",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_renewalCycle": {
				Action:    "directOp",
				Operation: "OpenRenewal",
				Params:    map[string]string{"leaseApp": "row.entityKey"},
				Reads:     []string{"row.entityKey"},
			},
		},
	}
}

// renewalCompleteTarget is Target B — mode: planned. The Goal addresses
// bgcheckValidUntil (a WALK-COMPUTED root fact — the outcome lives on a
// service instance, not the renewal, so it stays root-mapped per §4.3's
// row-fact naming rule) and three ASPECT-REAL renewal facts bridged via
// GoalColumns (guarantorVerifiedAt / termsSetAt / signedAt — Inc2's bridge
// machinery, both classes exercised in one gap, the rider's core authoring
// rule, §5). maxDepth is R1's len(actions)+2 constant, applied by the engine
// from the catalog length — not an authored field here.
func renewalCompleteTarget() pkgmgr.WeaverTargetSpec {
	goal := json.RawMessage(`{"allOf":[
		{"present":"subject.data.bgcheckValidUntil"},
		{"anyOf":[
			{"equals":{"path":"subject.data.hasGuarantor","value":false}},
			{"present":"subject.guarantorVerification.data.verifiedAt"}
		]},
		{"present":"subject.terms.data.setAt"},
		{"present":"subject.renewalSignature.data.signedAt"}
	]}`)
	goalColumns := map[string]string{
		"guarantorVerifiedAt": "subject.guarantorVerification.data.verifiedAt",
		"termsSetAt":          "subject.terms.data.setAt",
		"signedAt":            "subject.renewalSignature.data.signedAt",
	}
	actions := []pkgmgr.ActionCatalogEntrySpec{
		{
			Ref:     "refreshBgcheck",
			Action:  "triggerLoom",
			Pattern: "backgroundCheck",
			Subject: "row.tenant",
			Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.bgcheckValidUntil"}`)},
			Cost:    2,
		},
		{
			Ref:       "verifyGuarantor",
			Action:    "assignTask",
			Operation: "VerifyGuarantor",
			Assignee:  "row.landlord",
			Target:    "row.entityKey",
			Pre:       json.RawMessage(`{"equals":{"path":"subject.data.hasGuarantor","value":true}}`),
			Effects:   []json.RawMessage{json.RawMessage(`{"present":"subject.guarantorVerification.data.verifiedAt"}`)},
			Cost:      3,
		},
		{
			Ref:       "setTerms",
			Action:    "assignTask",
			Operation: "SetRenewalTerms",
			Assignee:  "row.landlord",
			Target:    "row.entityKey",
			Effects:   []json.RawMessage{json.RawMessage(`{"present":"subject.terms.data.setAt"}`)},
			Cost:      1,
		},
		{
			// The terminal-leg rule (§4.3/§5): pre is the GOAL'S FULL REMAINDER
			// (everything but signedAt itself), mirrored in SignRenewal's own
			// write guard (renewal_scripts.go: NotReadyToSign / GuarantorNotVerified).
			// Without this, the canonical tie-break
			// ("signRenewal" < "verifyGuarantor" lexicographically) would fire
			// signRenewal as soon as it becomes CHEAPEST-cost-reachable, before
			// the guarantor leg — the B1 regression.
			Ref:       "signRenewal",
			Action:    "assignTask",
			Operation: "SignRenewal",
			Assignee:  "row.tenant",
			Target:    "row.entityKey",
			Pre: json.RawMessage(`{"allOf":[
				{"present":"subject.data.bgcheckValidUntil"},
				{"anyOf":[
					{"equals":{"path":"subject.data.hasGuarantor","value":false}},
					{"present":"subject.guarantorVerification.data.verifiedAt"}
				]},
				{"present":"subject.terms.data.setAt"}
			]}`),
			Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.renewalSignature.data.signedAt"}`)},
			Cost:    1,
		},
	}
	return pkgmgr.WeaverTargetSpec{
		TargetID: "renewalComplete",
		LensRef:  "renewalComplete",
		Mode:     "planned",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_renewalComplete": {
				Goal:        goal,
				GoalColumns: goalColumns,
				Actions:     actions,
			},
		},
	}
}
