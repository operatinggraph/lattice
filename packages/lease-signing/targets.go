package leasesigning

import "github.com/asolgan/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract #10
// §10.8). TargetID == the lens OutputKeyPattern prefix (leaseApplicationComplete)
// — the §10.2↔§10.8 binding. LensRef resolves to the leaseApplicationComplete
// lens's in-batch NanoID at install.
//
// Each gap → remediation:
//   - missing_onboarding → triggerLoom(onboarding) over the applicant identity.
//   - missing_bgcheck    → triggerLoom(backgroundCheck): an externalTask pattern.
//   - missing_payment    → triggerLoom(collectPayment): an externalTask pattern.
//   - missing_signature  → assignTask SignLease to the applicant, scoped to the
//     application (the only gap closed by a user op rather than a flow).
//   - missing_listingLeased → directOp SetListingStatus(status=leased) over the
//     leased unit (row.unitKey). A cross-package directOp into loftspace-domain
//     (the op is granted to operator, which Weaver's service actor holds) — the
//     objectLiveness→TombstoneObject / appointmentReminders→RecordAppointmentReminder
//     precedent. Opens once a landlord APPROVES a qualified application
//     (DecideLeaseApplication decision=approved) and its unit is not yet leased;
//     closes when SetListingStatus flips the unit's .listing.status to leased and the
//     lens reprojects (the unit is an appliesToUnit neighbor, so its aspect change
//     reprojects this anchor). A qualified-but-undecided application sits in the
//     lens's missing_decision state (violating, but NO playbook entry — nothing
//     dispatches); the landlord decision is the human gate the flip waits behind.
//
// External remediation is triggerLoom of an externalTask pattern (the retired
// nudge action is never used). Every gap key is a column the lens projects, and
// every row.<col> template (row.applicant, row.entityKey, row.unitKey) is a lens
// BodyColumn — the §10.2↔§10.8 column seam (cross-checked by
// TestLeaseSigning_PlaybookColumnsMatchLens). The literal status=leased is passed
// verbatim (no row. prefix).
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	targets := []pkgmgr.WeaverTargetSpec{{
		TargetID: "leaseApplicationComplete",
		LensRef:  "leaseApplicationComplete",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_onboarding":    {Action: "triggerLoom", Pattern: "onboarding", Subject: "row.applicant"},
			"missing_bgcheck":       {Action: "triggerLoom", Pattern: "backgroundCheck", Subject: "row.applicant"},
			"missing_payment":       {Action: "triggerLoom", Pattern: "collectPayment", Subject: "row.applicant"},
			"missing_signature":     {Action: "assignTask", Operation: "SignLease", Assignee: "row.applicant", Target: "row.entityKey"},
			"missing_listingLeased": {Action: "directOp", Operation: "SetListingStatus", Params: map[string]string{"unit": "row.unitKey", "status": "leased"}, Reads: []string{"row.unitKey"}},
		},
	}}
	return append(targets, RenewalTargets()...)
}
