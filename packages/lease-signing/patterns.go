package leasesigning

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// LoomPatterns returns the package's meta.loomPattern declarations (Contract
// #10 §10.5). Three run over the applicant `identity` subject (the triggerLoom
// subject the §10.8 playbook passes as row.applicant); leaseDocument runs over
// the `leaseapp` subject (row.entityKey). All declare completionDomains
// ["orchestration"]:
//
//   - backgroundCheck / collectPayment — each a single externalTask step. The
//     step submits CreateLeaseServiceInstance (mints vtx.service.<handle>,
//     family bgcheck/payment, emits external.<adapter>) and parks for the
//     bridge's RecordLeaseServiceOutcome, which emits
//     orchestration.externalTaskCompleted — so completionDomains is
//     ["orchestration"], NOT the replyOp's own domain. The deadline disarms on
//     the instanceOp commit; the bridge wait is unbounded.
//   - onboarding — a single userTask step binding RecordIdentityPII (the
//     applicant records their PII), which writes the identity's .ssn/.dob
//     aspects and flips the lens's missing_onboarding gap false. A userTask
//     completes via orchestration.taskCompleted, so completionDomains is
//     ["orchestration"] too (Contract #10 §10.5 — a userTask completes on the
//     orchestration domain regardless of subject type).
//   - leaseDocument — a single externalTask step over the signed application
//     itself (subjectType leaseapp — the document is about the application).
//     The step submits CreateLeaseDocInstance (mints the docGen claim,
//     assembles the document fields Processor-side, emits external.docGen) and
//     parks for the bridge's RecordLeaseDocOutcome. The vendor adapter renders
//     the executed-lease artifact and writes its bytes to the object store;
//     the anchor (AttachObject) is a separate Weaver directOp off the
//     missing_leaseDocAttach gap, not a pattern step. tenantName is
//     subject-templated at the TOP LEVEL of Params (subject.tenantName.data.
//     value) — the leaseapp's own SENSITIVE .tenantName aspect, resolved to a
//     $sensitiveRef marker the bridge's docGen adapter opens at the egress
//     boundary (retention-class-egress-envelope-design.md §3.5(a)); absent
//     when the application carries no snapshot, tolerated by the descriptor
//     floor rather than failing the dispatch.
//
// The Adapter names (backgroundCheck, stripe, docGen) match the bridge's
// registered adapters (cmd/bridge/main.go).
func LoomPatterns() []pkgmgr.LoomPatternSpec {
	return []pkgmgr.LoomPatternSpec{
		{
			PatternID:         "backgroundCheck",
			SubjectType:       "identity",
			CompletionDomains: []string{"orchestration"},
			Steps: []pkgmgr.StepSpec{{
				Kind:       "externalTask",
				Adapter:    "backgroundCheck",
				InstanceOp: "CreateLeaseServiceInstance",
				ReplyOp:    "RecordLeaseServiceOutcome",
				// name/dob are subject-templated (Contract #10 §10.5): both
				// identity-domain aspects are sensitive, so Loom's inference
				// declares them under egressReads (not reads) and the vendor
				// receives real plaintext only at the bridge's egress-unwrap
				// boundary — the live subject-PII adapter payload consumer named
				// in sensitive-param-egress-design.md §7.
				Params: map[string]any{"family": "backgroundCheck", "name": "subject.name.data.value", "dob": "subject.dob.data.value"},
			}},
		},
		{
			PatternID:         "collectPayment",
			SubjectType:       "identity",
			CompletionDomains: []string{"orchestration"},
			Steps: []pkgmgr.StepSpec{{
				Kind:       "externalTask",
				Adapter:    "stripe",
				InstanceOp: "CreateLeaseServiceInstance",
				ReplyOp:    "RecordLeaseServiceOutcome",
				Params:     map[string]any{"family": "payment"},
			}},
		},
		{
			PatternID:         "onboarding",
			SubjectType:       "identity",
			CompletionDomains: []string{"orchestration"},
			Steps: []pkgmgr.StepSpec{{
				Kind:      "userTask",
				Operation: "RecordIdentityPII",
			}},
		},
		{
			PatternID:         "leaseDocument",
			SubjectType:       "leaseapp",
			CompletionDomains: []string{"orchestration"},
			Steps: []pkgmgr.StepSpec{{
				Kind:       "externalTask",
				Adapter:    "docGen",
				InstanceOp: "CreateLeaseDocInstance",
				ReplyOp:    "RecordLeaseDocOutcome",
				// tenantName is subject-templated at the TOP LEVEL of Params,
				// beside family -- never into doc -- because the unwrap
				// substitutes markers at the top level of params only and the
				// docGen adapter reads tenantName from the unwrapped map
				// (retention-class-egress-envelope-design.md §3.5(a)). The
				// leaseapp's own .tenantName aspect is SENSITIVE, so Loom's
				// inferExternalTaskReads declares it under egressReads, not
				// reads, and CreateLeaseDocInstance drops the template when
				// the aspect is absent (the descriptor floor tolerates that
				// absence at hydrate) rather than failing the dispatch.
				Params: map[string]any{"family": "docGen", "tenantName": "subject.tenantName.data.value"},
			}},
		},
	}
}
