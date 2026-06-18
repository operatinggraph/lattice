package leasesigning

import "github.com/asolgan/lattice/internal/pkgmgr"

// LoomPatterns returns the package's meta.loomPattern declarations (Contract
// #10 §10.5). All three run over the applicant `identity` subject (the
// triggerLoom subject the §10.8 playbook passes as row.applicant) and declare
// completionDomains ["orchestration"]:
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
//
// The Adapter names (backgroundCheck, stripe) match the bridge's registered
// adapters (cmd/bridge/main.go). 14.4 does not run the bridge (the e2e is
// 14.5), but the names are correct now.
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
				Params:     map[string]any{"family": "backgroundCheck"},
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
	}
}
