package leasesigning

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract #10
// §10.8). TargetID == the lens OutputKeyPattern prefix (leaseApplicationComplete)
// — the §10.2↔§10.8 binding. LensRef resolves to the leaseApplicationComplete
// lens's in-batch NanoID at install.
//
// Each gap → remediation:
//   - missing_onboarding → triggerLoom(onboarding) over the applicant identity.
//   - missing_bgcheck    → triggerLoom(backgroundCheck): an externalTask pattern.
//     Adapter is set to the pattern's own vendor (backgroundCheck) purely as an
//     admission-bucketing label (resolved.Adapter, evaluator.go admitGap) — the
//     Loom pattern itself, not this label, drives the actual dispatch.
//   - missing_payment    → triggerLoom(collectPayment): an externalTask pattern.
//     Adapter is set to stripe for the same admission-bucketing reason.
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
//   - missing_leaseDoc → triggerLoom(leaseDocument) over the application itself
//     (row.entityKey — the pattern's subjectType is leaseapp). Opens on signing
//     (signature present, no completed docGen outcome, none in flight, none
//     failed); the pattern's externalTask has the vendor render + store the
//     executed-lease bytes and RecordLeaseDocOutcome close the gap. A FAILED
//     render is terminal (declined_docGen folds the gap false — no auto-retry;
//     re-generation is a fresh manual StartLoomPattern).
//   - missing_leaseDocAttach → directOp AttachObject anchoring the produced
//     bytes to the application under the signedLease slot. The attach payload is
//     drawn from the row's doc-pointer columns (projected off the completed
//     docGen .outcome, so they are non-null exactly when this gap is open); the
//     op is objects-base's generic attach, granted to operator (Weaver's service
//     actor) — the replyOp cannot mint the object vertex itself (step-6 class→DDL
//     resolution routes object-class mutations to objects-base's DDL). Closes
//     when the signedLease link lands and the lens reprojects; a detached
//     executed lease re-opens it (self-healing re-attach).
//
// External remediation is triggerLoom of an externalTask pattern (the retired
// nudge action is never used). Every gap key is a column the lens projects, and
// every row.<col> template (row.applicant, row.entityKey, row.unitKey, the
// row.doc* pointers) is a lens BodyColumn — the §10.2↔§10.8 column seam
// (cross-checked by TestLeaseSigning_PlaybookColumnsMatchLens). Literals
// (status=leased, linkName=signedLease) are passed verbatim (no row. prefix);
// row.docSize resolves type-preserving (a number reaches AttachObject's
// integer-validated size, Contract #10 §10.8 templating).
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	targets := []pkgmgr.WeaverTargetSpec{{
		TargetID: "leaseApplicationComplete",
		LensRef:  "leaseApplicationComplete",
		// Admission (Contract #10 §10.8 "Admission control", Fire 8) paces this
		// target's two vendor-backed gaps independently: a spike of applicants
		// hitting missing_bgcheck/missing_payment together must not burst either
		// vendor beyond a sane call rate. Conservative placeholder budgets — a
		// real vendor integration tunes these to its actual rate-limit contract.
		Admission: &pkgmgr.AdmissionSpec{
			AdapterRates: map[string]float64{"backgroundCheck": 2, "stripe": 5},
		},
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_onboarding":    {Action: "triggerLoom", Pattern: "onboarding", Subject: "row.applicant"},
			"missing_bgcheck":       {Action: "triggerLoom", Pattern: "backgroundCheck", Subject: "row.applicant", Adapter: "backgroundCheck"},
			"missing_payment":       {Action: "triggerLoom", Pattern: "collectPayment", Subject: "row.applicant", Adapter: "stripe"},
			"missing_signature":     {Action: "assignTask", Operation: "SignLease", Assignee: "row.applicant", Target: "row.entityKey"},
			"missing_listingLeased": {Action: "directOp", Operation: "SetListingStatus", Params: map[string]string{"unit": "row.unitKey", "status": "leased"}, Reads: []string{"row.unitKey", "row.unitKey.listing"}},
			"missing_leaseDoc":      {Action: "triggerLoom", Pattern: "leaseDocument", Subject: "row.entityKey"},
			"missing_leaseDocAttach": {Action: "directOp", Operation: "AttachObject", Params: map[string]string{
				"digest": "row.docDigest", "size": "row.docSize", "contentType": "row.docContentType",
				"storeName": "row.docStoreName", "filename": "row.docFilename",
				"targetKey": "row.entityKey", "linkName": "signedLease",
			}, Reads: []string{"row.entityKey"}},
		},
	}}
	return append(targets, RenewalTargets()...)
}
