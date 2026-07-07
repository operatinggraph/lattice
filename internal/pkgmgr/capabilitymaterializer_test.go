package pkgmgr

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
)

// fullCypherParser adapts ruleengine/full.Engine to CypherParser. Living in a
// _test.go file (not pkgmgr's production code) is what avoids the import
// cycle CypherParser's doc explains — full's own test binary transitively
// imports pkgmgr, but pkgmgr's *test* binary importing full (prod) has no such
// path back, so this is safe here (and would be safe in any other package's
// production code too — just not pkgmgr's).
type fullCypherParser struct{}

func (fullCypherParser) Parse(ruleBody string) error {
	_, err := full.New().Parse(ruleBody)
	return err
}

func lensContent(t *testing.T, lc LensArtifactContent) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(lc)
	if err != nil {
		t.Fatalf("marshal lens content: %v", err)
	}
	return b
}

func grantContent(t *testing.T, gc GrantArtifactContent) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(gc)
	if err != nil {
		t.Fatalf("marshal grant content: %v", err)
	}
	return b
}

func TestValidateCapabilityArtifact_DisabledKind(t *testing.T) {
	// vertexTypeDDL is gated behind Fire 4 (Starlark) — lens/grant/weaverTarget/
	// loomPattern are the four kinds this increment enables, so a still-disabled
	// kind is needed here.
	report, err := ValidateCapabilityArtifact("vertexTypeDDL", json.RawMessage(`{}`), fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected invalid report for a disabled kind, got valid")
	}
	if len(report.Errors) != 1 {
		t.Fatalf("expected exactly one error, got %v", report.Errors)
	}
}

func TestValidateCapabilityArtifact_MalformedContent(t *testing.T) {
	_, err := ValidateCapabilityArtifact("lens", json.RawMessage(`not-json`), fullCypherParser{}, nil)
	if err == nil {
		t.Fatalf("expected a caller-contract error for malformed content")
	}
}

func TestValidateCapabilityArtifact_ValidLens(t *testing.T) {
	content := lensContent(t, LensArtifactContent{
		CanonicalName: "activeProvidersBySpecialty",
		Adapter:       "nats-kv",
		Bucket:        "active-providers",
		Spec:          "MATCH (p:provider) RETURN p.key AS key",
	})
	report, err := ValidateCapabilityArtifact("lens", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected a valid report, got errors: %v", report.Errors)
	}
}

func TestValidateCapabilityArtifact_UnparseableCypher(t *testing.T) {
	content := lensContent(t, LensArtifactContent{
		CanonicalName: "brokenLens",
		Adapter:       "nats-kv",
		Bucket:        "broken-lens",
		Spec:          "MATCH (p:provider RETURN p.key AS key", // missing close paren
	})
	report, err := ValidateCapabilityArtifact("lens", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for unparseable cypher")
	}
}

func TestValidateCapabilityArtifact_MissingCanonicalName(t *testing.T) {
	content := lensContent(t, LensArtifactContent{
		Adapter: "nats-kv",
		Bucket:  "no-name",
		Spec:    "MATCH (p:provider) RETURN p.key AS key",
	})
	report, err := ValidateCapabilityArtifact("lens", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a missing canonicalName")
	}
}

func TestValidateCapabilityArtifact_CoreKVAdapterRejected(t *testing.T) {
	// P5: a lens may never target Core KV directly — validateLensAdapters
	// already rejects any Adapter other than "" / "nats-kv" / "postgres", so an
	// AI-authored artifact cannot smuggle a core-kv-shaped adapter through.
	content := lensContent(t, LensArtifactContent{
		CanonicalName: "sneakyLens",
		Adapter:       "core-kv",
		Spec:          "MATCH (p:provider) RETURN p.key AS key",
	})
	report, err := ValidateCapabilityArtifact("lens", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a core-kv-shaped adapter")
	}
}

func TestValidateCapabilityArtifact_ReservedBucketAliasRejected(t *testing.T) {
	// The reserved short alias guard (bucketguard.go) must apply identically to
	// an AI-authored lens — reused validateAll, not a weaker copy.
	content := lensContent(t, LensArtifactContent{
		CanonicalName: "phantomLens",
		Adapter:       "nats-kv",
		Bucket:        "capability",
		Spec:          "MATCH (p:provider) RETURN p.key AS key",
	})
	report, err := ValidateCapabilityArtifact("lens", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for the reserved 'capability' bucket alias")
	}
}

func TestValidateCapabilityArtifact_OutOfScopeFieldRejected(t *testing.T) {
	// A raw content payload that smuggles a field this increment doesn't expose
	// (e.g. "protected") must be caught, not silently dropped by json.Unmarshal
	// and downgraded to a plain lens.
	content := json.RawMessage(`{"canonicalName":"sneakyProtected","adapter":"postgres","table":"sneaky","spec":"MATCH (p:provider) RETURN p.key AS key","protected":true}`)
	report, err := ValidateCapabilityArtifact("lens", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for an out-of-scope 'protected' field")
	}
}

func TestValidateCapabilityArtifact_MissingBucketRejected(t *testing.T) {
	content := lensContent(t, LensArtifactContent{
		CanonicalName: "noBucketLens",
		Adapter:       "nats-kv",
		Spec:          "MATCH (p:provider) RETURN p.key AS key",
	})
	report, err := ValidateCapabilityArtifact("lens", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a nats-kv lens with no Bucket")
	}
}

func TestValidateCapabilityArtifact_ValidGrant(t *testing.T) {
	content := grantContent(t, GrantArtifactContent{
		OperationType: "RescheduleAppointment",
		Scope:         "self",
		GrantsTo:      []string{"front-desk"},
	})
	held := []HeldPermission{{OperationType: "RescheduleAppointment", Scope: "any"}}
	report, err := ValidateCapabilityArtifact("grant", content, fullCypherParser{}, held)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected a valid report, got errors: %v", report.Errors)
	}
}

func TestValidateCapabilityArtifact_GrantExactScopeMatch(t *testing.T) {
	content := grantContent(t, GrantArtifactContent{
		OperationType: "RescheduleAppointment",
		Scope:         "any",
		GrantsTo:      []string{"front-desk"},
	})
	held := []HeldPermission{{OperationType: "RescheduleAppointment", Scope: "any"}}
	report, err := ValidateCapabilityArtifact("grant", content, fullCypherParser{}, held)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected a valid report, got errors: %v", report.Errors)
	}
}

func TestValidateCapabilityArtifact_GrantExceedsRequesterScope_Rejected(t *testing.T) {
	// The privilege-escalation case the scope check exists to close: the
	// requester holds ONLY "self" for this operationType, but the artifact
	// requests granting "any" — broader than the requester's own authority.
	content := grantContent(t, GrantArtifactContent{
		OperationType: "RescheduleAppointment",
		Scope:         "any",
		GrantsTo:      []string{"front-desk"},
	})
	held := []HeldPermission{{OperationType: "RescheduleAppointment", Scope: "self"}}
	report, err := ValidateCapabilityArtifact("grant", content, fullCypherParser{}, held)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a grant exceeding the requester's held scope")
	}
}

func TestValidateCapabilityArtifact_GrantRequesterHoldsNothing_Rejected(t *testing.T) {
	// An operator routing an AI request for an operationType they don't hold at
	// all can never mint that grant, at any scope.
	content := grantContent(t, GrantArtifactContent{
		OperationType: "DeleteEverything",
		Scope:         "self",
		GrantsTo:      []string{"operator"},
	})
	report, err := ValidateCapabilityArtifact("grant", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report when the requester holds no matching permission")
	}
}

func TestValidateCapabilityArtifact_GrantDifferentOperationType_Rejected(t *testing.T) {
	// Holding broad authority over ONE operationType must never cover a grant
	// naming a DIFFERENT operationType.
	content := grantContent(t, GrantArtifactContent{
		OperationType: "DeleteEverything",
		Scope:         "self",
		GrantsTo:      []string{"operator"},
	})
	held := []HeldPermission{{OperationType: "RescheduleAppointment", Scope: "any"}}
	report, err := ValidateCapabilityArtifact("grant", content, fullCypherParser{}, held)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a grant naming an operationType the requester doesn't hold")
	}
}

func TestValidateCapabilityArtifact_GrantMissingOperationType_Rejected(t *testing.T) {
	content := grantContent(t, GrantArtifactContent{
		Scope:    "self",
		GrantsTo: []string{"front-desk"},
	})
	report, err := ValidateCapabilityArtifact("grant", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a missing operationType")
	}
}

func TestValidateCapabilityArtifact_GrantInvalidScope_Rejected(t *testing.T) {
	content := grantContent(t, GrantArtifactContent{
		OperationType: "RescheduleAppointment",
		Scope:         "everything",
		GrantsTo:      []string{"front-desk"},
	})
	held := []HeldPermission{{OperationType: "RescheduleAppointment", Scope: "any"}}
	report, err := ValidateCapabilityArtifact("grant", content, fullCypherParser{}, held)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a scope outside {any, self}")
	}
}

func TestValidateCapabilityArtifact_GrantEmptyGrantsTo_Rejected(t *testing.T) {
	content := grantContent(t, GrantArtifactContent{
		OperationType: "RescheduleAppointment",
		Scope:         "self",
	})
	held := []HeldPermission{{OperationType: "RescheduleAppointment", Scope: "any"}}
	report, err := ValidateCapabilityArtifact("grant", content, fullCypherParser{}, held)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for an empty grantsTo")
	}
}

func TestValidateCapabilityArtifact_GrantWhitespaceRole_Rejected(t *testing.T) {
	content := grantContent(t, GrantArtifactContent{
		OperationType: "RescheduleAppointment",
		Scope:         "self",
		GrantsTo:      []string{"  "},
	})
	held := []HeldPermission{{OperationType: "RescheduleAppointment", Scope: "any"}}
	report, err := ValidateCapabilityArtifact("grant", content, fullCypherParser{}, held)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a whitespace-only role name")
	}
}

func TestValidateCapabilityArtifact_GrantDuplicateRole_Rejected(t *testing.T) {
	content := grantContent(t, GrantArtifactContent{
		OperationType: "RescheduleAppointment",
		Scope:         "self",
		GrantsTo:      []string{"front-desk", "front-desk"},
	})
	held := []HeldPermission{{OperationType: "RescheduleAppointment", Scope: "any"}}
	report, err := ValidateCapabilityArtifact("grant", content, fullCypherParser{}, held)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a duplicate role in grantsTo")
	}
}

func TestValidateCapabilityArtifact_KindCaseSensitive_Rejected(t *testing.T) {
	// The enabled-kind check is exact-string, case-sensitive — "Grant" must
	// never be silently treated as the enabled "grant" kind, on either this Go
	// allow-list or the independent Starlark ENABLED_KINDS gate it mirrors.
	report, err := ValidateCapabilityArtifact("Grant", json.RawMessage(`{}`), fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected invalid report for a case-mismatched kind, got valid")
	}
}

func TestValidateCapabilityArtifact_GrantDuplicatePermission_Rejected(t *testing.T) {
	// validatePermissionIdentityUniqueness only fires within a Definition's own
	// Permissions slice — a single-grant artifact can never self-collide, so
	// this proves the shared validateAll pre-flight is genuinely wired in
	// (grantArtifactDefinition), not merely present as an unreachable import.
	def := grantArtifactDefinition(GrantArtifactContent{
		OperationType: "RescheduleAppointment",
		Scope:         "self",
		GrantsTo:      []string{"front-desk"},
	}, "", "")
	def.Permissions = append(def.Permissions, def.Permissions[0])
	if err := def.validateAll(); err == nil {
		t.Fatalf("expected validateAll to reject a duplicate (operationType, scope) permission pair")
	}
}

func TestValidateCapabilityArtifact_GrantOutOfScopeFieldRejected(t *testing.T) {
	// A raw content payload smuggling a field GrantArtifactContent doesn't
	// expose (e.g. "roles" instead of "grantsTo") must be caught, not silently
	// dropped by json.Unmarshal.
	content := json.RawMessage(`{"operationType":"RescheduleAppointment","scope":"self","grantsTo":["front-desk"],"roles":["operator"]}`)
	held := []HeldPermission{{OperationType: "RescheduleAppointment", Scope: "any"}}
	report, err := ValidateCapabilityArtifact("grant", content, fullCypherParser{}, held)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for an out-of-scope 'roles' field")
	}
}

func TestDefinitionForCapabilityArtifact_Grant(t *testing.T) {
	content := grantContent(t, GrantArtifactContent{
		OperationType: "RescheduleAppointment",
		Scope:         "self",
		GrantsTo:      []string{"front-desk"},
		Note:          "AI-authored grant",
	})
	def, err := DefinitionForCapabilityArtifact("grant", content, "ai-grant-pkg", "1.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(def.Permissions) != 1 {
		t.Fatalf("expected exactly one Permission, got %d", len(def.Permissions))
	}
	p := def.Permissions[0]
	if p.OperationType != "RescheduleAppointment" || p.Scope != "self" || p.Note != "AI-authored grant" {
		t.Fatalf("materialized Permission = %+v, want operationType=RescheduleAppointment scope=self note=%q", p, "AI-authored grant")
	}
	if len(p.GrantsTo) != 1 || p.GrantsTo[0] != "front-desk" {
		t.Fatalf("materialized Permission.GrantsTo = %v, want [front-desk]", p.GrantsTo)
	}
}

func weaverTargetContent(t *testing.T, wc WeaverTargetArtifactContent) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(wc)
	if err != nil {
		t.Fatalf("marshal weaverTarget content: %v", err)
	}
	return b
}

func loomPatternContent(t *testing.T, lp LoomPatternArtifactContent) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(lp)
	if err != nil {
		t.Fatalf("marshal loomPattern content: %v", err)
	}
	return b
}

func TestValidateCapabilityArtifact_ValidWeaverTarget(t *testing.T) {
	content := weaverTargetContent(t, WeaverTargetArtifactContent{
		TargetID: "aiTargetDispatch",
		LensRef:  "someExistingLens",
		Gaps: map[string]GapActionArtifact{
			"missing_followUp": {Action: "directOp", Operation: "SendReminder"},
		},
	})
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected a valid report, got errors: %v", report.Errors)
	}
}

func TestValidateCapabilityArtifact_WeaverTargetMissingTargetID_Rejected(t *testing.T) {
	content := weaverTargetContent(t, WeaverTargetArtifactContent{
		LensRef: "someExistingLens",
		Gaps: map[string]GapActionArtifact{
			"missing_followUp": {Action: "directOp", Operation: "SendReminder"},
		},
	})
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a missing targetId")
	}
}

func TestValidateCapabilityArtifact_WeaverTargetBadGapColumn_Rejected(t *testing.T) {
	// The missing_<gap> column convention (validateWeaverTargets, reused, not
	// duplicated) must reject an AI-authored target exactly like a hand-authored
	// one.
	content := weaverTargetContent(t, WeaverTargetArtifactContent{
		TargetID: "aiTargetDispatch",
		LensRef:  "someExistingLens",
		Gaps: map[string]GapActionArtifact{
			"followUp": {Action: "directOp", Operation: "SendReminder"},
		},
	})
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a gaps key not matching missing_<gap>")
	}
}

func TestValidateCapabilityArtifact_WeaverTargetReservedGapParam_Rejected(t *testing.T) {
	content := weaverTargetContent(t, WeaverTargetArtifactContent{
		TargetID: "aiTargetDispatch",
		LensRef:  "someExistingLens",
		Gaps: map[string]GapActionArtifact{
			"missing_followUp": {Action: "directOp", Operation: "SendReminder", Params: map[string]string{"expectedRevision": "5"}},
		},
	})
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a gap action setting the engine-reserved expectedRevision param")
	}
}

func TestValidateCapabilityArtifact_WeaverTargetUnknownActionRejected(t *testing.T) {
	content := weaverTargetContent(t, WeaverTargetArtifactContent{
		TargetID: "aiTargetDispatch",
		LensRef:  "someExistingLens",
		Gaps: map[string]GapActionArtifact{
			"missing_followUp": {Action: "deleteEverything"},
		},
	})
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a gap action outside the §10.8 action table")
	}
}

func TestValidateCapabilityArtifact_WeaverTargetAugurFieldRejected(t *testing.T) {
	// The `augur` escalation-policy block is deliberately out of scope for an
	// AI-authored weaverTarget artifact (§For-Andrew #1's autonomy posture) —
	// a raw payload smuggling it must be caught, not silently dropped by
	// json.Unmarshal and downgraded to a plain (augur-less) target.
	content := json.RawMessage(`{"targetId":"aiTargetDispatch","lensRef":"someExistingLens","gaps":{"missing_followUp":{"action":"directOp","operation":"SendReminder"}},"augur":{"escalate":["unplannable"]}}`)
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for an out-of-scope 'augur' field")
	}
}

func TestValidateCapabilityArtifact_WeaverTargetSmuggledGapFieldRejected(t *testing.T) {
	// A planner-extension surface (goal/candidates/mode/augur) buried in a GAP
	// entry — not at the top level — is out of scope for the AI weaverTarget
	// path. GapActionArtifact does not expose it, so json.Unmarshal SILENTLY
	// DROPS it and the gap would materialize as a plain directOp, bypassing §5's
	// stored-invalid audit trail. The nested unknown-field scan must catch it and
	// report it as gaps.<col>.<key>.
	content := json.RawMessage(`{"targetId":"aiTargetDispatch","lensRef":"someExistingLens","gaps":{"missing_x":{"action":"directOp","operation":"SendReminder","goal":[{"present":"row.done"}]}}}`)
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a gap smuggling an out-of-scope 'goal' key")
	}
	joined := strings.Join(report.Errors, " ")
	if !strings.Contains(joined, "gaps.missing_x.goal") {
		t.Fatalf("smuggled gap key must be reported as gaps.missing_x.goal; got %q", joined)
	}
}

func TestValidateCapabilityArtifact_LoomPatternSmuggledStepFieldRejected(t *testing.T) {
	// A key buried in a STEP entry (not at the top level) that StepArtifact does
	// not expose is silently dropped by json.Unmarshal and would bypass §5's
	// stored-invalid audit trail — the same class as a smuggled gap field. The
	// nested step scan must catch it and report it as steps[<i>].<key>.
	content := json.RawMessage(`{"patternId":"aiPattern","subjectType":"vtx.thing","steps":[{"kind":"systemOp","operation":"DoThing","escalate":["x"]}]}`)
	report, err := ValidateCapabilityArtifact("loomPattern", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a step smuggling an out-of-scope 'escalate' key")
	}
	joined := strings.Join(report.Errors, " ")
	if !strings.Contains(joined, "steps[0].escalate") {
		t.Fatalf("smuggled step key must be reported as steps[0].escalate; got %q", joined)
	}
}

func TestDefinitionForCapabilityArtifact_WeaverTarget(t *testing.T) {
	content := weaverTargetContent(t, WeaverTargetArtifactContent{
		TargetID: "aiTargetDispatch",
		LensRef:  "someExistingLens",
		Gaps: map[string]GapActionArtifact{
			"missing_followUp": {Action: "directOp", Operation: "SendReminder", Reads: []string{"row.entityKey"}},
		},
	})
	def, err := DefinitionForCapabilityArtifact("weaverTarget", content, "ai-target-pkg", "1.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(def.WeaverTargets) != 1 {
		t.Fatalf("expected exactly one WeaverTarget, got %d", len(def.WeaverTargets))
	}
	wt := def.WeaverTargets[0]
	if wt.TargetID != "aiTargetDispatch" || wt.LensRef != "someExistingLens" {
		t.Fatalf("materialized WeaverTarget = %+v, want targetId=aiTargetDispatch lensRef=someExistingLens", wt)
	}
	ga, ok := wt.Gaps["missing_followUp"]
	if !ok {
		t.Fatalf("expected gaps[missing_followUp], got %v", wt.Gaps)
	}
	if ga.Action != "directOp" || ga.Operation != "SendReminder" || len(ga.Reads) != 1 || ga.Reads[0] != "row.entityKey" {
		t.Fatalf("materialized GapAction = %+v, want action=directOp operation=SendReminder reads=[row.entityKey]", ga)
	}
}

func TestValidateCapabilityArtifact_ValidLoomPattern(t *testing.T) {
	content := loomPatternContent(t, LoomPatternArtifactContent{
		PatternID:   "aiPattern",
		SubjectType: "capabilityproposal",
		Steps: []StepArtifact{{
			Kind:      "systemOp",
			Operation: "SendReminder",
		}},
	})
	report, err := ValidateCapabilityArtifact("loomPattern", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected a valid report, got errors: %v", report.Errors)
	}
}

func TestValidateCapabilityArtifact_LoomPatternExternalTask_Valid(t *testing.T) {
	content := loomPatternContent(t, LoomPatternArtifactContent{
		PatternID:   "aiExternalPattern",
		SubjectType: "capabilityproposal",
		Steps: []StepArtifact{{
			Kind:       "externalTask",
			Adapter:    "someAdapter",
			InstanceOp: "CreateSomeClaim",
			ReplyOp:    "RecordSomeResult",
			Params:     map[string]any{"foo": "bar"},
		}},
	})
	report, err := ValidateCapabilityArtifact("loomPattern", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected a valid report, got errors: %v", report.Errors)
	}
}

func TestValidateCapabilityArtifact_LoomPatternNoSteps_Rejected(t *testing.T) {
	content := loomPatternContent(t, LoomPatternArtifactContent{
		PatternID:   "aiPattern",
		SubjectType: "capabilityproposal",
	})
	report, err := ValidateCapabilityArtifact("loomPattern", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a pattern with no steps")
	}
}

func TestValidateCapabilityArtifact_LoomPatternSystemOpForbidsAdapter_Rejected(t *testing.T) {
	// The §10.5 shape enforcement (validateLoomPatterns, reused, not
	// duplicated): a systemOp step may not carry externalTask-only fields.
	content := loomPatternContent(t, LoomPatternArtifactContent{
		PatternID:   "aiPattern",
		SubjectType: "capabilityproposal",
		Steps: []StepArtifact{{
			Kind:      "systemOp",
			Operation: "SendReminder",
			Adapter:   "someAdapter",
		}},
	})
	report, err := ValidateCapabilityArtifact("loomPattern", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a systemOp step carrying an externalTask-only field")
	}
}

func TestValidateCapabilityArtifact_LoomPatternUnknownKind_Rejected(t *testing.T) {
	content := loomPatternContent(t, LoomPatternArtifactContent{
		PatternID:   "aiPattern",
		SubjectType: "capabilityproposal",
		Steps: []StepArtifact{{
			Kind:      "arbitraryCode",
			Operation: "SendReminder",
		}},
	})
	report, err := ValidateCapabilityArtifact("loomPattern", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a step kind outside {systemOp, userTask, externalTask}")
	}
}

func TestValidateCapabilityArtifact_LoomPatternUnknownFieldRejected(t *testing.T) {
	// A raw payload smuggling a top-level field LoomPatternArtifactContent
	// doesn't expose must be caught, not silently dropped by json.Unmarshal —
	// mirrors the lens/grant/weaverTarget out-of-scope-field defense even
	// though no LoomPatternSpec field is excluded today (future-proofing
	// against schema drift, not a currently-live posture).
	content := json.RawMessage(`{"patternId":"aiPattern","subjectType":"capabilityproposal","steps":[{"kind":"systemOp","operation":"SendReminder"}],"futureField":"sneaky"}`)
	report, err := ValidateCapabilityArtifact("loomPattern", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for an out-of-scope 'futureField' field")
	}
}

func TestValidateCapabilityArtifact_LoomPatternDeclarativeGuard_Valid(t *testing.T) {
	// A well-formed §10.5 declarative guard (not the reserved Starlark escape
	// hatch) must validate normally — the Starlark rejection below must not
	// over-reject ordinary declarative guards.
	content := loomPatternContent(t, LoomPatternArtifactContent{
		PatternID:   "aiPattern",
		SubjectType: "capabilityproposal",
		Steps: []StepArtifact{{
			Kind:      "systemOp",
			Operation: "SendReminder",
			Guard:     map[string]any{"present": "subject.someAspect.data.someField"},
		}},
	})
	report, err := ValidateCapabilityArtifact("loomPattern", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected a valid report for a declarative guard, got errors: %v", report.Errors)
	}
}

func TestValidateCapabilityArtifact_LoomPatternStarlarkGuardRejected(t *testing.T) {
	// The reserved Starlark escape hatch ({reads, starlark}, Contract #10
	// §10.5) is well-formed JSON that validateLoomPatterns' shape checks alone
	// would accept — this is the record-time boundary that must catch it
	// instead, since AI-authored Starlark is Fire-4-gated (§3.2 "no Starlark").
	content := loomPatternContent(t, LoomPatternArtifactContent{
		PatternID:   "aiPattern",
		SubjectType: "capabilityproposal",
		Steps: []StepArtifact{{
			Kind:      "systemOp",
			Operation: "SendReminder",
			Guard:     map[string]any{"reads": []any{"subject"}, "starlark": "return True"},
		}},
	})
	report, err := ValidateCapabilityArtifact("loomPattern", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a step guard using the reserved Starlark escape hatch")
	}
}

func TestValidateCapabilityArtifact_LoomPatternMalformedGuardRejected(t *testing.T) {
	content := loomPatternContent(t, LoomPatternArtifactContent{
		PatternID:   "aiPattern",
		SubjectType: "capabilityproposal",
		Steps: []StepArtifact{{
			Kind:      "systemOp",
			Operation: "SendReminder",
			Guard:     map[string]any{"present": "subject.x", "absent": "subject.y"},
		}},
	})
	report, err := ValidateCapabilityArtifact("loomPattern", content, fullCypherParser{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a guard declaring more than one top-level grammar key")
	}
}

func TestDefinitionForCapabilityArtifact_LoomPattern(t *testing.T) {
	content := loomPatternContent(t, LoomPatternArtifactContent{
		PatternID:         "aiPattern",
		SubjectType:       "capabilityproposal",
		CompletionDomains: []string{"orchestration"},
		Steps: []StepArtifact{{
			Kind:      "systemOp",
			Operation: "SendReminder",
		}},
	})
	def, err := DefinitionForCapabilityArtifact("loomPattern", content, "ai-pattern-pkg", "1.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(def.LoomPatterns) != 1 {
		t.Fatalf("expected exactly one LoomPattern, got %d", len(def.LoomPatterns))
	}
	lp := def.LoomPatterns[0]
	if lp.PatternID != "aiPattern" || lp.SubjectType != "capabilityproposal" {
		t.Fatalf("materialized LoomPattern = %+v, want patternId=aiPattern subjectType=capabilityproposal", lp)
	}
	if len(lp.CompletionDomains) != 1 || lp.CompletionDomains[0] != "orchestration" {
		t.Fatalf("materialized LoomPattern.CompletionDomains = %v, want [orchestration]", lp.CompletionDomains)
	}
	if len(lp.Steps) != 1 || lp.Steps[0].Kind != "systemOp" || lp.Steps[0].Operation != "SendReminder" {
		t.Fatalf("materialized LoomPattern.Steps = %+v, want one systemOp/SendReminder step", lp.Steps)
	}
}
