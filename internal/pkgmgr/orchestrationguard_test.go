package pkgmgr

import (
	"strings"
	"testing"
)

func TestValidateWeaverTargets_Valid(t *testing.T) {
	def := Definition{
		WeaverTargets: []WeaverTargetSpec{{
			TargetID: "leaseSigning",
			LensRef:  "leaseSigningCandidates",
			Gaps: map[string]GapActionSpec{
				"missing_signature": {Action: "assignTask", Operation: "SignLease", Assignee: "row.tenant", Target: "row.lease"},
			},
		}},
	}
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("expected valid weaver target to pass, got: %v", err)
	}
}

func TestValidateWeaverTargets_MissingTargetID(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{TargetID: ""}}}
	if err := def.validateWeaverTargets(); err == nil {
		t.Fatal("expected error for empty TargetID, got nil")
	}
}

func TestValidateWeaverTargets_BadTargetIDKeyShape(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{TargetID: "lease.signing"}}}
	err := def.validateWeaverTargets()
	if err == nil {
		t.Fatal("expected error for dotted TargetID, got nil")
	}
	if !strings.Contains(err.Error(), "lease.signing") {
		t.Errorf("error should name the offending targetId; got %q", err)
	}
}

func TestValidateWeaverTargets_DuplicateTargetID(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{
		{TargetID: "leaseSigning"},
		{TargetID: "leaseSigning"},
	}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-targetId error, got %v", err)
	}
}

func TestValidateWeaverTargets_NonMissingGapKeyRejected(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"signature": {Action: "directOp", Operation: "SignLease"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "missing_<gap>") {
		t.Fatalf("expected missing_<gap> convention error, got %v", err)
	}
}

func TestValidateWeaverTargets_BareMissingPrefixRejected(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_": {Action: "directOp", Operation: "SignLease"}},
	}}}
	if err := def.validateWeaverTargets(); err == nil {
		t.Fatal("expected error for bare missing_ gap key, got nil")
	}
}

func TestValidateWeaverTargets_ReservedExpectedRevisionParamRejected(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps: map[string]GapActionSpec{
			"missing_signature": {Action: "assignTask", Params: map[string]string{"expectedRevision": "3"}},
		},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "expectedRevision") {
		t.Fatalf("expected reserved-param error, got %v", err)
	}
}

func TestValidateWeaverTargets_UnknownActionRejected(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: "teleport", Operation: "X"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "teleport") {
		t.Fatalf("expected unknown-action error naming the action, got %v", err)
	}
}

func TestValidateWeaverTargets_EmptyActionRejected(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: ""}},
	}}}
	if err := def.validateWeaverTargets(); err == nil {
		t.Fatal("expected error for empty action, got nil")
	}
}

func TestValidateWeaverTargets_TriggerLoomMissingPattern(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: "triggerLoom", Subject: "row.lease"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "Pattern") {
		t.Fatalf("expected triggerLoom missing-Pattern error, got %v", err)
	}
}

func TestValidateWeaverTargets_TriggerLoomMissingSubject(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: "triggerLoom", Pattern: "leaseSigning"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "Subject") {
		t.Fatalf("expected triggerLoom missing-Subject error, got %v", err)
	}
}

func TestValidateWeaverTargets_AssignTaskMissingAssignee(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: "assignTask", Operation: "SignLease", Target: "row.lease"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "Assignee") {
		t.Fatalf("expected assignTask missing-Assignee error, got %v", err)
	}
}

func TestValidateWeaverTargets_AssignTaskMissingTarget(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: "assignTask", Operation: "SignLease", Assignee: "row.tenant"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "Target") {
		t.Fatalf("expected assignTask missing-Target error, got %v", err)
	}
}

func TestValidateWeaverTargets_DirectOpMissingOperation(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: "directOp"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "Operation") {
		t.Fatalf("expected directOp missing-Operation error, got %v", err)
	}
}

func TestValidateWeaverTargets_EachActionWellFormedAccepted(t *testing.T) {
	cases := map[string]GapActionSpec{
		"missing_a": {Action: "triggerLoom", Pattern: "leaseSigning", Subject: "row.lease"},
		"missing_c": {Action: "assignTask", Operation: "SignLease", Assignee: "row.tenant", Target: "row.lease"},
		"missing_d": {Action: "directOp", Operation: "MarkExpired"},
	}
	for col, ga := range cases {
		def := Definition{WeaverTargets: []WeaverTargetSpec{{
			TargetID: "leaseSigning",
			Gaps:     map[string]GapActionSpec{col: ga},
		}}}
		if err := def.validateWeaverTargets(); err != nil {
			t.Fatalf("expected well-formed %s action to pass, got: %v", ga.Action, err)
		}
	}
}

func TestValidateLoomPatterns_Valid(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "leaseSigning",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "systemOp", Operation: "SignLease"}},
	}}}
	if err := def.validateLoomPatterns(); err != nil {
		t.Fatalf("expected valid pattern to pass, got: %v", err)
	}
}

func TestValidateLoomPatterns_MissingPatternID(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{SubjectType: "lease", Steps: []StepSpec{{Kind: "systemOp", Operation: "X"}}}}}
	if err := def.validateLoomPatterns(); err == nil {
		t.Fatal("expected error for empty PatternID, got nil")
	}
}

func TestValidateLoomPatterns_MissingSubjectType(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{PatternID: "p", Steps: []StepSpec{{Kind: "systemOp", Operation: "X"}}}}}
	if err := def.validateLoomPatterns(); err == nil {
		t.Fatal("expected error for empty SubjectType, got nil")
	}
}

func TestValidateLoomPatterns_NoSteps(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{PatternID: "p", SubjectType: "lease"}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "at least one step") {
		t.Fatalf("expected no-steps error, got %v", err)
	}
}

func TestValidateLoomPatterns_BadStepKind(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "magic", Operation: "X"}},
	}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected bad-step-kind error, got %v", err)
	}
}

func TestValidateLoomPatterns_EmptyStepOperation(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "systemOp", Operation: ""}},
	}}}
	if err := def.validateLoomPatterns(); err == nil {
		t.Fatal("expected error for empty step operation, got nil")
	}
}

func TestValidateLoomPatterns_ExternalTaskValid(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "leaseSigning",
		SubjectType: "lease",
		Steps: []StepSpec{{
			Kind:       "externalTask",
			Adapter:    "docusign",
			InstanceOp: "CreateSigningInstance",
			ReplyOp:    "ResolveSigning",
			Params:     map[string]any{"template": "lease"},
		}},
	}}}
	if err := def.validateLoomPatterns(); err != nil {
		t.Fatalf("expected valid externalTask pattern to pass, got: %v", err)
	}
}

func TestValidateLoomPatterns_ExternalTaskNoOperationRequired(t *testing.T) {
	// An externalTask must NOT require `operation` — its op vocabulary is
	// instanceOp/replyOp.
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "externalTask", Adapter: "docusign", InstanceOp: "CreateSigningInstance", ReplyOp: "ResolveSigning"}},
	}}}
	if err := def.validateLoomPatterns(); err != nil {
		t.Fatalf("externalTask must not require operation, got: %v", err)
	}
}

func TestValidateLoomPatterns_ExternalTaskMissingAdapter(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "externalTask", InstanceOp: "CreateSigningInstance", ReplyOp: "ResolveSigning"}},
	}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "adapter") {
		t.Fatalf("expected externalTask missing-adapter error, got %v", err)
	}
}

func TestValidateLoomPatterns_ExternalTaskMissingInstanceOp(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "externalTask", Adapter: "docusign", ReplyOp: "ResolveSigning"}},
	}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "instanceOp") {
		t.Fatalf("expected externalTask missing-instanceOp error, got %v", err)
	}
}

func TestValidateLoomPatterns_ExternalTaskMissingReplyOp(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "externalTask", Adapter: "docusign", InstanceOp: "CreateSigningInstance"}},
	}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "replyOp") {
		t.Fatalf("expected externalTask missing-replyOp error, got %v", err)
	}
}

func TestValidateLoomPatterns_SystemOpStillRequiresOperation(t *testing.T) {
	// The externalTask branch must not relax the systemOp/userTask operation
	// requirement.
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "systemOp", Operation: ""}},
	}}}
	if err := def.validateLoomPatterns(); err == nil {
		t.Fatal("expected systemOp without operation to still be rejected, got nil")
	}
}

func TestValidateLoomPatterns_SystemOpWithStrayInstanceOpRejected(t *testing.T) {
	// A systemOp carrying an externalTask-only field must be rejected fail-closed
	// rather than validating clean with the foreign field silently ignored.
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "systemOp", Operation: "SignLease", InstanceOp: "CreateSigningInstance"}},
	}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "instanceOp") {
		t.Fatalf("expected systemOp stray-instanceOp error, got %v", err)
	}
}

func TestValidateLoomPatterns_UserTaskWithStrayAdapterRejected(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "userTask", Operation: "SignLease", Adapter: "docusign"}},
	}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "adapter") {
		t.Fatalf("expected userTask stray-adapter error, got %v", err)
	}
}

func TestValidateLoomPatterns_ExternalTaskWithStrayOperationRejected(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps: []StepSpec{{
			Kind:       "externalTask",
			Adapter:    "docusign",
			InstanceOp: "CreateSigningInstance",
			ReplyOp:    "ResolveSigning",
			Operation:  "SignLease",
		}},
	}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "operation") {
		t.Fatalf("expected externalTask stray-operation error, got %v", err)
	}
}

func TestValidateLoomPatterns_DuplicatePatternID(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{
		{PatternID: "leaseSigning", SubjectType: "lease", Steps: []StepSpec{{Kind: "systemOp", Operation: "X"}}},
		{PatternID: "leaseSigning", SubjectType: "lease", Steps: []StepSpec{{Kind: "systemOp", Operation: "Y"}}},
	}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-patternId error, got %v", err)
	}
}

func TestValidateOpMetas_Valid(t *testing.T) {
	def := Definition{OpMetas: []OpMetaSpec{{OperationType: "SignLease"}}}
	if err := def.validateOpMetas(); err != nil {
		t.Fatalf("expected valid op-meta to pass, got: %v", err)
	}
}

func TestValidateOpMetas_EmptyOperationType(t *testing.T) {
	def := Definition{OpMetas: []OpMetaSpec{{OperationType: ""}}}
	if err := def.validateOpMetas(); err == nil {
		t.Fatal("expected error for empty OperationType, got nil")
	}
}

func TestValidateOpMetas_BadToken(t *testing.T) {
	def := Definition{OpMetas: []OpMetaSpec{{OperationType: "Sign.Lease"}}}
	if err := def.validateOpMetas(); err == nil {
		t.Fatal("expected error for dotted OperationType, got nil")
	}
}

func TestValidateOpMetas_DuplicateOperationType(t *testing.T) {
	def := Definition{OpMetas: []OpMetaSpec{
		{OperationType: "SignLease"},
		{OperationType: "SignLease"},
	}}
	err := def.validateOpMetas()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-operationType error, got %v", err)
	}
}

func augurTargetDef(a *AugurSpec) Definition {
	return Definition{
		WeaverTargets: []WeaverTargetSpec{{
			TargetID: "leaseSigning",
			LensRef:  "leaseSigningCandidates",
			Gaps: map[string]GapActionSpec{
				"missing_approval": {Action: "assignTask", Operation: "Approve", Assignee: "row.a", Target: "row.t"},
			},
			Augur: a,
		}},
	}
}

func TestValidateWeaverTargets_AugurNilOK(t *testing.T) {
	if err := augurTargetDef(nil).validateWeaverTargets(); err != nil {
		t.Fatalf("a target with no augur block must pass: %v", err)
	}
}

func TestValidateWeaverTargets_AugurValid(t *testing.T) {
	// Option F: no loom pattern — Weaver dispatches the reasoning op directly as a
	// directOp, so the block is just escalate + the optional op/adapter/replyOp
	// overrides (defaulted at dispatch when omitted).
	def := augurTargetDef(&AugurSpec{
		Escalate: []string{"unplannable", "exhausted"},
		Op:       "CreateAugurReasoningClaim",
		Adapter:  "augur",
		ReplyOp:  "RecordProposal",
		Model:    "claude-opus-4-8",
		AutoApply: &AugurAutoApplySpec{
			Actions: []string{"triggerLoom", "directOp"}, MinConfidence: 0.9,
		},
	})
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("a fully-populated valid augur block must pass: %v", err)
	}
}

func TestValidateWeaverTargets_AugurMinimalOK(t *testing.T) {
	// The minimal block: one trigger, no overrides (op/adapter/replyOp default at
	// dispatch). No pattern is required anymore (Option F).
	if err := augurTargetDef(&AugurSpec{Escalate: []string{"unplannable"}}).validateWeaverTargets(); err != nil {
		t.Fatalf("a minimal augur block (no overrides) must pass: %v", err)
	}
}

func TestValidateWeaverTargets_AugurRejections(t *testing.T) {
	cases := []struct {
		name    string
		spec    *AugurSpec
		wantSub string
	}{
		{"empty escalate", &AugurSpec{}, "escalate is empty"},
		{"unknown trigger", &AugurSpec{Escalate: []string{"someday"}}, "not a known trigger"},
		{"bad op token", &AugurSpec{Escalate: []string{"unplannable"}, Op: "bad.op"}, "single token"},
		{"bad adapter token", &AugurSpec{Escalate: []string{"unplannable"}, Adapter: "a b"}, "single token"},
		{"bad autoApply action", &AugurSpec{Escalate: []string{"unplannable"},
			AutoApply: &AugurAutoApplySpec{Actions: []string{"DropTable"}}}, "not a known action"},
		{"minConfidence too high", &AugurSpec{Escalate: []string{"unplannable"},
			AutoApply: &AugurAutoApplySpec{MinConfidence: 1.5}}, "out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := augurTargetDef(tc.spec).validateWeaverTargets()
			if err == nil {
				t.Fatalf("%s: must be rejected", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("%s: unexpected reason: %v", tc.name, err)
			}
		})
	}
}
