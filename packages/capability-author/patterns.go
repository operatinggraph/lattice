package capabilityauthor

import "github.com/asolgan/lattice/internal/pkgmgr"

// LoomPatterns returns the package's meta.loomPattern declarations (Contract
// #10 §10.5) — the escalation dispatch (design
// ai-authored-capabilities-design.md §3.4).
//
// capabilityAuthor is a single externalTask step whose SubjectType is
// capabilityproposal itself — genuinely first-of-kind: every other shipped
// pattern (packages/lease-signing) subjects a long-lived actor (identity);
// here the subject IS the proposal vertex RequestCapabilityAuthoring already
// minted write-ahead of dispatch. There is no neighbor to project through
// (unlike lease-signing's row.applicant): the escalation context
// (requesterId/intent/contextRef) lives on the subject's OWN `.request`
// aspect, so the step's Params use the subject-templated `subject.<aspect>.
// data.<field>` form (Contract #10 §10.5) rather than a literal — Loom's
// inferExternalTaskReads declares the `.request` aspect read from these
// tokens, and the instanceOp (CreateAuthoringClaim) resolves them Processor-
// side via orchestration-base's resolve_subject_params before emitting the
// external.capabilityAuthor event.
func LoomPatterns() []pkgmgr.LoomPatternSpec {
	return []pkgmgr.LoomPatternSpec{
		{
			PatternID:         "capabilityAuthor",
			SubjectType:       "capabilityproposal",
			CompletionDomains: []string{"orchestration"},
			Steps: []pkgmgr.StepSpec{{
				Kind:       "externalTask",
				Adapter:    "capabilityAuthor",
				InstanceOp: "CreateAuthoringClaim",
				ReplyOp:    "RecordCapabilityProposal",
				Params: map[string]any{
					"requesterId": "subject.request.data.requesterId",
					"intent":      "subject.request.data.intent",
					"contextRef":  "subject.request.data.contextRef",
				},
			}},
		},
	}
}
