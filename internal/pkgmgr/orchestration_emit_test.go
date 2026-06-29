package pkgmgr_test

import (
	"encoding/json"
	"testing"

	"github.com/asolgan/lattice/internal/loom"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/weaver"
)

// findDoc returns the document the install batch emits under key, or nil.
func findDoc(ops []pkgmgr.InstallMutationForTest, key string) map[string]any {
	for _, op := range ops {
		if op.Key == key {
			return op.Document
		}
	}
	return nil
}

// orchestrationDef is a Definition declaring one lens (the lensRef target) and
// one of each orchestration spec kind.
func orchestrationDef() pkgmgr.Definition {
	return pkgmgr.Definition{
		Name:    "lease-signing",
		Version: "0.1.0",
		Lenses: []pkgmgr.LensSpec{{
			CanonicalName: "leaseSigningCandidates",
			Adapter:       "nats-kv",
			Bucket:        "lease-signing-candidates",
			Engine:        "full",
			Spec:          "MATCH (n) RETURN n.key AS key",
		}},
		WeaverTargets: []pkgmgr.WeaverTargetSpec{{
			TargetID: "leaseSigning",
			LensRef:  "leaseSigningCandidates",
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_signature": {
					Action:    "assignTask",
					Operation: "SignLease",
					Assignee:  "row.tenant",
					Target:    "row.lease",
				},
				"missing_onboarding": {
					Action:  "triggerLoom",
					Pattern: "onboarding",
					Subject: "row.lease",
				},
			},
		}},
		LoomPatterns: []pkgmgr.LoomPatternSpec{{
			PatternID:         "leaseSigning",
			SubjectType:       "lease",
			CompletionDomains: []string{"lease", "orchestration"},
			Steps: []pkgmgr.StepSpec{
				{Kind: "userTask", Operation: "SignLease", Guard: map[string]any{"absent": "signature"}},
				{Kind: "systemOp", Operation: "RecordLease"},
			},
		}},
		OpMetas: []pkgmgr.OpMetaSpec{{OperationType: "SignLease"}},
	}
}

// TestEmit_WeaverTarget_RoundTripsThroughEngineParse proves the emitted target
// vertex + spec aspect deserialize into weaver.Target exactly — the
// no-engine-change regression.
func TestEmit_WeaverTarget_RoundTripsThroughEngineParse(t *testing.T) {
	def := orchestrationDef()
	ops, _, err := pkgmgr.BuildInstallBatchForTest(def)
	if err != nil {
		t.Fatalf("BuildInstallBatchForTest: %v", err)
	}

	targetID := pkgmgr.EntityNanoIDForTest(def.Name, "weaverTarget:leaseSigning")
	vtxKey := "vtx.meta." + targetID
	vtx := findDoc(ops, vtxKey)
	if vtx == nil {
		t.Fatalf("no weaver-target vertex emitted at %s", vtxKey)
	}
	if vtx["class"] != "meta.weaverTarget" {
		t.Errorf("vertex class = %v, want meta.weaverTarget", vtx["class"])
	}
	if data, _ := vtx["data"].(map[string]any); len(data) != 0 {
		t.Errorf("vertex data should be empty, got %v", data)
	}

	specDoc := findDoc(ops, vtxKey+".spec")
	if specDoc == nil {
		t.Fatalf("no weaver-target spec aspect emitted at %s.spec", vtxKey)
	}
	if specDoc["class"] != "weaverTargetSpec" {
		t.Errorf("spec aspect class = %v, want weaverTargetSpec", specDoc["class"])
	}
	body, ok := specDoc["data"].(map[string]any)
	if !ok {
		t.Fatalf("spec aspect data not a map: %T", specDoc["data"])
	}

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal target body: %v", err)
	}
	var target weaver.Target
	if err := json.Unmarshal(raw, &target); err != nil {
		t.Fatalf("emitted target body does not deserialize into weaver.Target: %v", err)
	}
	if target.TargetID != "leaseSigning" {
		t.Errorf("target.TargetID = %q, want leaseSigning", target.TargetID)
	}
	if len(target.Gaps) != 2 {
		t.Fatalf("target.Gaps len = %d, want 2", len(target.Gaps))
	}
	if ga := target.Gaps["missing_signature"]; ga.Action != "assignTask" || ga.Operation != "SignLease" || ga.Assignee != "row.tenant" {
		t.Errorf("missing_signature gap action wrong: %+v", ga)
	}
	if ga := target.Gaps["missing_onboarding"]; ga.Action != "triggerLoom" || ga.Pattern != "onboarding" {
		t.Errorf("missing_onboarding gap action wrong: %+v", ga)
	}
	// Optional fields not set on missing_onboarding must be absent in the
	// emitted body (minimal-shape contract), so they parse to zero values.
	if ga := target.Gaps["missing_onboarding"]; ga.Operation != "" || ga.Assignee != "" {
		t.Errorf("missing_onboarding should omit unset optionals, got %+v", ga)
	}
}

// TestEmit_WeaverTarget_LensRefResolvesToDeclaredLensNanoID asserts a LensRef
// authored as a lens canonicalName emits the lens's in-batch NanoID.
func TestEmit_WeaverTarget_LensRefResolvesToDeclaredLensNanoID(t *testing.T) {
	def := orchestrationDef()
	ops, _, err := pkgmgr.BuildInstallBatchForTest(def)
	if err != nil {
		t.Fatalf("BuildInstallBatchForTest: %v", err)
	}
	wantLensID := pkgmgr.EntityNanoIDForTest(def.Name, "lens:leaseSigningCandidates")

	targetID := pkgmgr.EntityNanoIDForTest(def.Name, "weaverTarget:leaseSigning")
	specDoc := findDoc(ops, "vtx.meta."+targetID+".spec")
	body := specDoc["data"].(map[string]any)
	if body["lensRef"] != wantLensID {
		t.Errorf("lensRef = %v, want resolved lens NanoID %q", body["lensRef"], wantLensID)
	}
}

// TestEmit_WeaverTarget_LensRefLiteralNanoIDPassesThrough asserts a LensRef
// already shaped as a valid NanoID (a lens in an already-installed package) is
// shipped verbatim.
func TestEmit_WeaverTarget_LensRefLiteralNanoIDPassesThrough(t *testing.T) {
	literal := pkgmgr.EntityNanoIDForTest("other-pkg", "lens:someLens")
	def := pkgmgr.Definition{
		Name:    "lease-signing",
		Version: "0.1.0",
		WeaverTargets: []pkgmgr.WeaverTargetSpec{{
			TargetID: "leaseSigning",
			LensRef:  literal,
		}},
	}
	ops, _, err := pkgmgr.BuildInstallBatchForTest(def)
	if err != nil {
		t.Fatalf("BuildInstallBatchForTest: %v", err)
	}
	targetID := pkgmgr.EntityNanoIDForTest(def.Name, "weaverTarget:leaseSigning")
	body := findDoc(ops, "vtx.meta."+targetID+".spec")["data"].(map[string]any)
	if body["lensRef"] != literal {
		t.Errorf("lensRef = %v, want literal NanoID %q passed through", body["lensRef"], literal)
	}
}

// TestEmit_WeaverTarget_DanglingLensRefFailsInstall asserts an unresolvable
// LensRef is a fail-closed install error.
func TestEmit_WeaverTarget_DanglingLensRefFailsInstall(t *testing.T) {
	def := pkgmgr.Definition{
		Name:    "lease-signing",
		Version: "0.1.0",
		WeaverTargets: []pkgmgr.WeaverTargetSpec{{
			TargetID: "leaseSigning",
			LensRef:  "noSuchLens",
		}},
	}
	if _, _, err := pkgmgr.BuildInstallBatchForTest(def); err == nil {
		t.Fatal("expected dangling LensRef to fail the install, got nil error")
	}
}

// TestEmit_LoomPattern_RoundTripsThroughEngineParse proves the emitted pattern
// vertex + spec aspect deserialize into loom.Pattern exactly.
func TestEmit_LoomPattern_RoundTripsThroughEngineParse(t *testing.T) {
	def := orchestrationDef()
	ops, _, err := pkgmgr.BuildInstallBatchForTest(def)
	if err != nil {
		t.Fatalf("BuildInstallBatchForTest: %v", err)
	}

	patternID := pkgmgr.EntityNanoIDForTest(def.Name, "loomPattern:leaseSigning")
	vtxKey := "vtx.meta." + patternID
	vtx := findDoc(ops, vtxKey)
	if vtx == nil {
		t.Fatalf("no loom-pattern vertex emitted at %s", vtxKey)
	}
	if vtx["class"] != "meta.loomPattern" {
		t.Errorf("vertex class = %v, want meta.loomPattern", vtx["class"])
	}

	specDoc := findDoc(ops, vtxKey+".spec")
	if specDoc == nil {
		t.Fatalf("no loom-pattern spec aspect emitted at %s.spec", vtxKey)
	}
	if specDoc["class"] != "loomPatternSpec" {
		t.Errorf("spec aspect class = %v, want loomPatternSpec", specDoc["class"])
	}
	body := specDoc["data"].(map[string]any)

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal pattern body: %v", err)
	}
	var pattern loom.Pattern
	if err := json.Unmarshal(raw, &pattern); err != nil {
		t.Fatalf("emitted pattern body does not deserialize into loom.Pattern: %v", err)
	}
	if pattern.PatternID != "leaseSigning" || pattern.SubjectType != "lease" {
		t.Errorf("pattern identity wrong: %+v", pattern)
	}
	if len(pattern.CompletionDomains) != 2 {
		t.Errorf("completionDomains = %v, want 2", pattern.CompletionDomains)
	}
	if len(pattern.Steps) != 2 {
		t.Fatalf("steps len = %d, want 2", len(pattern.Steps))
	}
	if pattern.Steps[0].Kind != "userTask" || pattern.Steps[0].Operation != "SignLease" {
		t.Errorf("step[0] wrong: %+v", pattern.Steps[0])
	}
	if len(pattern.Steps[0].Guard) == 0 {
		t.Error("step[0] guard should round-trip into loom.Step.Guard json.RawMessage")
	}
	// step[1] has no guard — it must be omitted, parsing to an empty RawMessage.
	if len(pattern.Steps[1].Guard) != 0 {
		t.Errorf("step[1] guard should be omitted, got %s", pattern.Steps[1].Guard)
	}
}

// TestEmit_LoomPattern_ExternalTaskRoundTripsThroughEngineParse proves an
// externalTask step's four fields (adapter/params/replyOp/instanceOp) are
// emitted by the body builder and round-trip into a loom.Step the engine
// accepts — the validation-parity + body-builder agreement the second site
// requires. The emitted body is unmarshaled into loom.Pattern (the exact CDC
// path) and the resulting step must carry every externalTask field.
func TestEmit_LoomPattern_ExternalTaskRoundTripsThroughEngineParse(t *testing.T) {
	def := pkgmgr.Definition{
		Name:    "lease-signing",
		Version: "0.1.0",
		LoomPatterns: []pkgmgr.LoomPatternSpec{{
			PatternID:   "leaseSigning",
			SubjectType: "lease",
			Steps: []pkgmgr.StepSpec{
				{
					Kind:       "externalTask",
					Adapter:    "docusign",
					InstanceOp: "CreateSigningInstance",
					ReplyOp:    "ResolveSigning",
					Params:     map[string]any{"template": "lease", "ttlDays": 7},
				},
				{Kind: "systemOp", Operation: "RecordLease"},
			},
		}},
	}
	ops, _, err := pkgmgr.BuildInstallBatchForTest(def)
	if err != nil {
		t.Fatalf("BuildInstallBatchForTest: %v", err)
	}

	patternID := pkgmgr.EntityNanoIDForTest(def.Name, "loomPattern:leaseSigning")
	specDoc := findDoc(ops, "vtx.meta."+patternID+".spec")
	if specDoc == nil {
		t.Fatalf("no loom-pattern spec aspect emitted for externalTask pattern")
	}
	body := specDoc["data"].(map[string]any)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal pattern body: %v", err)
	}
	var pattern loom.Pattern
	if err := json.Unmarshal(raw, &pattern); err != nil {
		t.Fatalf("emitted externalTask pattern body does not deserialize into loom.Pattern: %v", err)
	}
	if len(pattern.Steps) != 2 {
		t.Fatalf("steps len = %d, want 2", len(pattern.Steps))
	}
	s := pattern.Steps[0]
	if s.Kind != "externalTask" {
		t.Fatalf("step[0] kind = %q, want externalTask", s.Kind)
	}
	if s.Adapter != "docusign" {
		t.Errorf("step[0] adapter = %q, want docusign", s.Adapter)
	}
	if s.InstanceOp != "CreateSigningInstance" {
		t.Errorf("step[0] instanceOp = %q, want CreateSigningInstance", s.InstanceOp)
	}
	if s.ReplyOp != "ResolveSigning" {
		t.Errorf("step[0] replyOp = %q, want ResolveSigning", s.ReplyOp)
	}
	if len(s.Params) == 0 {
		t.Error("step[0] params should round-trip into loom.Step.Params json.RawMessage")
	}
	// externalTask must NOT carry an operation; the systemOp step must.
	if s.Operation != "" {
		t.Errorf("step[0] (externalTask) operation should be empty, got %q", s.Operation)
	}
	if pattern.Steps[1].Operation != "RecordLease" {
		t.Errorf("step[1] (systemOp) operation = %q, want RecordLease", pattern.Steps[1].Operation)
	}
}

// TestEmit_OpMeta_ShapeMatchesEngineIndex asserts the op-meta vertex carries
// operationType on its data and emits NO spec aspect (the installOpMeta shape
// both engines' indexOpMeta read).
func TestEmit_OpMeta_ShapeMatchesEngineIndex(t *testing.T) {
	def := orchestrationDef()
	ops, _, err := pkgmgr.BuildInstallBatchForTest(def)
	if err != nil {
		t.Fatalf("BuildInstallBatchForTest: %v", err)
	}

	opMetaID := pkgmgr.EntityNanoIDForTest(def.Name, "opMeta:SignLease")
	vtxKey := "vtx.meta." + opMetaID
	vtx := findDoc(ops, vtxKey)
	if vtx == nil {
		t.Fatalf("no op-meta vertex emitted at %s", vtxKey)
	}
	if vtx["class"] != "meta.ddl.vertexType" {
		t.Errorf("op-meta vertex class = %v, want meta.ddl.vertexType", vtx["class"])
	}
	data, ok := vtx["data"].(map[string]any)
	if !ok || data["operationType"] != "SignLease" {
		t.Errorf("op-meta vertex data.operationType = %v, want SignLease", vtx["data"])
	}
	if findDoc(ops, vtxKey+".spec") != nil {
		t.Error("op-meta vertex must NOT emit a spec aspect")
	}
}

// TestEmit_DeclaredKeysIncludeAllOrchestrationKeys asserts every emitted
// orchestration key (vertex + spec aspect) is in declaredKeys so uninstall
// reclaims it.
func TestEmit_DeclaredKeysIncludeAllOrchestrationKeys(t *testing.T) {
	def := orchestrationDef()
	_, declared, err := pkgmgr.BuildInstallBatchForTest(def)
	if err != nil {
		t.Fatalf("BuildInstallBatchForTest: %v", err)
	}
	declaredSet := make(map[string]struct{}, len(declared))
	for _, k := range declared {
		declaredSet[k] = struct{}{}
	}

	targetID := pkgmgr.EntityNanoIDForTest(def.Name, "weaverTarget:leaseSigning")
	patternID := pkgmgr.EntityNanoIDForTest(def.Name, "loomPattern:leaseSigning")
	opMetaID := pkgmgr.EntityNanoIDForTest(def.Name, "opMeta:SignLease")
	want := []string{
		"vtx.meta." + targetID,
		"vtx.meta." + targetID + ".spec",
		"vtx.meta." + patternID,
		"vtx.meta." + patternID + ".spec",
		"vtx.meta." + opMetaID,
	}
	for _, k := range want {
		if _, ok := declaredSet[k]; !ok {
			t.Errorf("declaredKeys missing %q (uninstall would orphan it)", k)
		}
	}
}

// TestEmit_DeterministicKeys asserts re-emitting the same Definition produces
// identical keys (idempotent install).
func TestEmit_DeterministicKeys(t *testing.T) {
	def := orchestrationDef()
	ops1, _, err := pkgmgr.BuildInstallBatchForTest(def)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	ops2, _, err := pkgmgr.BuildInstallBatchForTest(def)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if len(ops1) != len(ops2) {
		t.Fatalf("op count differs across builds: %d vs %d", len(ops1), len(ops2))
	}
	for i := range ops1 {
		if ops1[i].Key != ops2[i].Key {
			t.Errorf("key[%d] differs across builds: %q vs %q", i, ops1[i].Key, ops2[i].Key)
		}
	}
}

// TestEmit_WeaverTarget_AugurBlockRoundTripsThroughEngineParse proves the
// optional §10.8 augur block survives the full emit path (augurBody → spec
// aspect → weaver.Target parse). A target with no augur block round-trips to
// the frozen-contract shape (Augur == nil), asserted by the base test above.
func TestEmit_WeaverTarget_AugurBlockRoundTripsThroughEngineParse(t *testing.T) {
	def := orchestrationDef()
	def.WeaverTargets[0].Augur = &pkgmgr.AugurSpec{
		Escalate: []string{"unplannable"},
		Pattern:  "augurReasoning",
		Model:    "claude-opus-4-8",
		AutoApply: &pkgmgr.AugurAutoApplySpec{
			Actions: []string{"triggerLoom"}, MinConfidence: 0.8,
		},
	}
	def.LoomPatterns = append(def.LoomPatterns, pkgmgr.LoomPatternSpec{
		PatternID:   "augurReasoning",
		SubjectType: "lease",
		Steps: []pkgmgr.StepSpec{{
			Kind: "externalTask", Adapter: "augur",
			InstanceOp: "CreateAugurReasoningClaim", ReplyOp: "RecordProposal",
		}},
	})

	ops, _, err := pkgmgr.BuildInstallBatchForTest(def)
	if err != nil {
		t.Fatalf("BuildInstallBatchForTest: %v", err)
	}
	targetID := pkgmgr.EntityNanoIDForTest(def.Name, "weaverTarget:leaseSigning")
	specDoc := findDoc(ops, "vtx.meta."+targetID+".spec")
	if specDoc == nil {
		t.Fatalf("no weaver-target spec aspect emitted")
	}
	raw, err := json.Marshal(specDoc["data"])
	if err != nil {
		t.Fatalf("marshal target body: %v", err)
	}
	var target weaver.Target
	if err := json.Unmarshal(raw, &target); err != nil {
		t.Fatalf("emitted augur-bearing body does not deserialize into weaver.Target: %v", err)
	}
	if target.Augur == nil {
		t.Fatalf("augur block dropped on the emit path")
	}
	if len(target.Augur.Escalate) != 1 || target.Augur.Escalate[0] != "unplannable" {
		t.Errorf("escalate not round-tripped: %+v", target.Augur.Escalate)
	}
	if target.Augur.Pattern != "augurReasoning" || target.Augur.Model != "claude-opus-4-8" {
		t.Errorf("pattern/model not round-tripped: %+v", target.Augur)
	}
	if target.Augur.AutoApply == nil || target.Augur.AutoApply.MinConfidence != 0.8 ||
		len(target.Augur.AutoApply.Actions) != 1 {
		t.Errorf("autoApply not round-tripped: %+v", target.Augur.AutoApply)
	}
}
