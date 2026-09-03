package leasesigning

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's meta.weaverTarget playbook (Contract #10
// §10.8). TargetID == the bound lens's OutputKeyPattern prefix — the §10.2↔§10.8
// binding. LensRef resolves to that lens's in-batch NanoID at install.
//
// Two targets are declared here (the renewal chain adds two more): the
// leaseapp-anchored leaseApplicationComplete, and the identity-anchored
// applicantOnboarding. The split is a granularity one — a convergence target's
// anchor must be the granularity of the work its gaps dispatch, and recording
// PII is a property of the PERSON while every other gap here is a property of
// the APPLICATION.
//
// leaseApplicationComplete — each gap → remediation:
//   - missing_onboarding → NOTHING dispatches here. It is surface-declared
//     (violating, projected, an operator-visible Health issue), because the work
//     it names is per-IDENTITY while this target's anchor is per-APPLICATION: an
//     applicant holding N applications projects N rows, and a triggerLoom from
//     here mints one Loom instance per row (the artifact id is derived from the
//     row's own entityId), so one person is asked for their SSN N times. The
//     real dispatch lives on the applicantOnboarding target below, whose anchor
//     IS the applicant. The column stays projected and stays in `violating`: the
//     application genuinely is blocked on onboarding, the applicant FE's stepper
//     reads it, and it closes on the same `.ssn` write it always did.
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
//   - missing_manager (violating, but NO playbook entry, same missing_decision
//     shape) opens once a landlord APPROVES an application whose unit carries no
//     live `manages` link (loftspace-domain's landlordUnitsRead/
//     landlordLeaseApplicationsRead both walk unit <- manages <- identity, so an
//     unmanaged unit's resident silently drops out of every landlord/staff view —
//     DecideLeaseApplication's operator/frontOfHouse standing grant never checks
//     for a manager, so this is reachable outside seed data). No auto-dispatch:
//     WHO becomes the interim manager is a business call AssignUnitOwner alone
//     can't make (it CONFERS management, not infers it) — this flag makes the gap
//     visible in the weaver-targets bucket instead of silently vanishing, the fix
//     is a human running AssignUnitOwner. Closes the moment any `manages` link
//     lands (the unit is an appliesToUnit neighbor, so the link reprojects this
//     anchor).
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
// applicantOnboarding — one gap, one remediation:
//   - missing_onboarding → triggerLoom(onboarding) over the applicant identity
//     (row.applicant, which on this target is the anchor itself). One row per
//     applicant means one gap per applicant means one RecordIdentityPII task,
//     by construction — no dedup mechanism involved. Its lens carries the
//     identity-scoped inflight_onboarding companion, so a task already sitting
//     open suppresses re-dispatch across mark-lease reclaims.
//
// External remediation is triggerLoom of an externalTask pattern (the retired
// nudge action is never used). Every gap key is a column the bound lens
// projects, and the converse holds too: every missing_* column a bound lens
// projects is a gap key — surface-declared (missing_decision, missing_manager,
// and missing_onboarding on leaseApplicationComplete) where this target
// deliberately dispatches nothing for it, so Weaver can tell a deliberate
// orphan from an authoring omission. And every row.<col> template
// (row.applicant, row.entityKey, row.unitKey, the row.doc* pointers) is a lens
// BodyColumn — the §10.2↔§10.8 column seam (cross-checked by
// TestLeaseSigning_PlaybookColumnsMatchLens and
// TestLeaseSigning_MissingColumnsAreDeclaredGaps). Literals
// (status=leased, linkName=signedLease) are passed verbatim (no row. prefix);
// row.docSize resolves type-preserving (a number reaches AttachObject's
// integer-validated size, Contract #10 §10.8 templating).
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	targets := []pkgmgr.WeaverTargetSpec{{
		TargetID: "leaseApplicationComplete",
		Description: "A lease application reaches a signed, executed lease with its document attached, and an " +
			"approved application's unit is marked leased. Outstanding steps are requested from the " +
			"applicant or the vendor.",
		LensRef: "leaseApplicationComplete",
		// Admission (Contract #10 §10.8 "Admission control", Fire 8) paces this
		// target's two vendor-backed gaps independently: a spike of applicants
		// hitting missing_bgcheck/missing_payment together must not burst either
		// vendor beyond a sane call rate. Conservative placeholder budgets — a
		// real vendor integration tunes these to its actual rate-limit contract.
		Admission: &pkgmgr.AdmissionSpec{
			AdapterRates: map[string]float64{"backgroundCheck": 2, "stripe": 5},
		},
		// A gap that exhausts its retry budget (maxretries_bgcheck/payment = 3,
		// packages/lease-signing/retry_budget.go) escalates to the Augur
		// AI-reasoning tier (Contract #10 §10.8 "Augur escalation") instead of
		// parking behind an unread Health-KV GapBudgetExhausted warning — the
		// contract's own default posture for budget exhaustion on any gap in
		// this target, declared vs. default-capped alike.
		Augur: &pkgmgr.AugurSpec{Escalate: []string{"exhausted"}},
		Gaps: map[string]pkgmgr.GapActionSpec{
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
			// missing_decision and missing_manager keep the row violating for work
			// Weaver deliberately never dispatches: a landlord decision Weaver has
			// no assignee for, and an unmanaged approved unit that closes only when
			// a `manages` link lands (lens_cypher_test.go's
			// TestLeaseApplicationComplete_ManagerGap_OpensWhenApprovedAndUnmanaged
			// pins that closure — an operator action, never a Weaver dispatch).
			// `surface` is the declaration that says so: it raises a per-
			// (target,entity,column) Health issue and Acks, instead of riding the
			// long redelivery floor forever the way an undeclared column would.
			"missing_decision": {Action: "surface", IssueCode: "LeaseDecisionAwaiting", IssueSeverity: "warning"},
			"missing_manager":  {Action: "surface", IssueCode: "LeaseUnitUnmanaged", IssueSeverity: "warning"},
			// missing_onboarding is the third surface-declared column, and the
			// only one whose work IS dispatched — from the applicantOnboarding
			// target below, whose anchor matches the work's granularity. Declaring
			// it here keeps the projected column out of dispatchGap's config-error
			// arm (which would hold every application of an un-onboarded applicant
			// on the long redelivery floor) and gives an operator a named issue
			// for an application blocked on a person who has not recorded PII.
			"missing_onboarding": {Action: "surface", IssueCode: "LeaseOnboardingAwaiting", IssueSeverity: "warning"},
		},
	}, {
		TargetID: "applicantOnboarding",
		Description: "An applicant holding at least one live lease application has recorded their PII. One row per " +
			"applicant, so the request is made once however many applications they hold.",
		LensRef: "applicantOnboarding",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_onboarding": {Action: "triggerLoom", Pattern: "onboarding", Subject: "row.applicant"},
		},
	}, {
		// staleUserTasks — orchestration-base's orphanedTaskGrants cancels a task
		// whose granted OPERATION died; this cancels a task whose own GAP already
		// closed through another route while the grant is still perfectly live
		// (lenses.go's staleUserTasksSpec doc comment). Reuses the exact same
		// directOp CancelTask{taskKey} orphanedTaskGrants already dispatches — no
		// new op, no new permission, and Class pins the "task" DDL the same way
		// (a directOp fails closed on the first other package that also claims
		// the operationType, so it stays pinned regardless of ambiguity today).
		TargetID: "staleUserTasks",
		Description: "An open user task — RecordIdentityPII, SignLease, or SetRenewalTerms — whose own gap already " +
			"closed through another route is cancelled instead of sitting in the assignee's inbox forever.",
		LensRef: "staleUserTasks",
		Gaps: map[string]pkgmgr.GapActionSpec{
			"missing_cancellation": {
				Action:    "directOp",
				Operation: "CancelTask",
				Class:     "task",
				Params:    map[string]string{"taskKey": "row.taskKey"},
				Reads:     []string{"row.taskKey"},
			},
		},
	}, {
		// backgroundCheckFreshness declares NO gaps, and that is its whole shape:
		// it dispatches nothing. Weaver's row handler runs the freshness
		// bookkeeping leg — arm / re-arm / clear the @at from the row's freshUntil
		// — on every delivery, before it reads any gap column, so a target with an
		// empty playbook still gets its timer. What the timer buys is the
		// MarkExpired that records the lapse on the background-check instance,
		// which is this target's anchor; the readers of that fact (this package's
		// readiness fragment and renewalComplete's bgcheck aggregate) are anchored
		// elsewhere and could never have hosted it.
		TargetID: BackgroundCheckFreshnessTarget,
		Description: "A completed background check's freshness window is recorded on the check itself when it " +
			"lapses, so every view asking whether a check is still current reads a recorded fact rather than a clock. " +
			"OPERATOR NOTE: this target dispatches nothing and declares no gap, so it has nothing to raise. If it is " +
			"disabled, or its MarkExpired is rejected, expired checks keep reading as CURRENT everywhere — the " +
			"application's bgcheck gap stays shut, the renewal's background-check atom stays satisfied, and no " +
			"violation anywhere reports it. The same holds for any check whose window lapsed while no timer " +
			"watched it, until its overdue timer fires and the marker lands. Freshness fails OPEN here; the " +
			"recorded lapse is the only evidence.",
		LensRef: BackgroundCheckFreshnessTarget,
	}}
	return append(targets, RenewalTargets()...)
}
