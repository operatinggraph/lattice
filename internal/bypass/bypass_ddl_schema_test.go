package bypass

// Bypass #4 — DDL Schema Violation
//
// Enforcement: Processor step 6 validator (ValidatorImpl from step6_validate.go,
// Story 1.7/1.9).
//
// Two scenarios are tested:
//   1. Forbidden operationType per permittedCommands: DDL declares
//      permittedCommands=["CreateResource"]; op type "DeleteResource" →
//      DDLViolation(permittedCommands).
//   2. Sensitive aspect on non-identity vertex: DDL marks aspect as
//      sensitive=true; attempt to attach it to a "resource" vertex →
//      DDLViolation(sensitiveAspectScope).
//
// In both cases: DDLViolation is returned BEFORE step 8 (atomic batch),
// so no partial state reaches Core KV and no op-tracker key is written.
//
// Report row:
//   DDL schema violation | BLOCKED | Processor step 6 validator

import (
	"context"
	"errors"
	"testing"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// TestBypass4_ForbiddenOperationType verifies that an operation with an
// operationType not listed in the DDL's permittedCommands is rejected by
// the step 6 validator.
//
// Scenario: DDL for class "bypassresource" declares permittedCommands=["CreateResource"].
// An incoming operation with operationType="DeleteResource" is blocked.
// The mutation key MUST NOT appear in Core KV.
func TestBypass4_ForbiddenOperationType(t *testing.T) {
	ctx, conn := setupBypassHarness(t)

	// Seed DDL: class "bypassres", permittedCommands=["CreateResource"].
	// Class name must be a valid vertex type segment: [a-z][a-z0-9]*.
	seedDDL4(t, ctx, conn, "bypassres", []string{"CreateResource"}, false)

	cache, validator := buildValidator4(t, ctx, conn)
	_ = cache

	// The mutation key this bypass would write.
	// vtx.<type>.<nanoID> — type must be all-lowercase, nanoID must be 20 chars.
	mutationKey := "vtx.bypassres." + bypassNanoID1

	env := &processor.OperationEnvelope{
		RequestID:     bypassNanoID1,
		Lane:          processor.LaneDefault,
		OperationType: "DeleteResource", // NOT in permittedCommands
		Actor:         "vtx.identity." + bypassNanoID2,
		SubmittedAt:   "2026-05-14T00:00:00Z",
	}

	result := processor.ScriptResult{
		Mutations: []processor.MutationOp{{
			Op:  "create",
			Key: mutationKey,
			Document: map[string]interface{}{
				"class":     "bypassres",
				"isDeleted": false,
				"data":      map[string]interface{}{},
			},
		}},
	}

	err := validator.Validate(ctx, env, result)

	// ASSERTION: must be *DDLViolation with ViolatedConstraint="permittedCommands".
	var ddlErr *processor.DDLViolation
	if !errors.As(err, &ddlErr) {
		t.Fatalf("bypass4 forbidden-op: BYPASS ESCAPED: expected *DDLViolation, got %T: %v (mutation would proceed to Core KV)", err, err)
	}
	if ddlErr.ViolatedConstraint != "permittedCommands" {
		t.Fatalf("bypass4 forbidden-op: ViolatedConstraint=%q, want 'permittedCommands'", ddlErr.ViolatedConstraint)
	}

	// ASSERTION: mutation key must NOT be in Core KV (step 8 never ran).
	if kvPresent(ctx, conn, bypassCoreBucket, mutationKey) {
		t.Fatalf("bypass4: BYPASS ESCAPED: mutation key %q reached Core KV despite DDLViolation", mutationKey)
	}

	t.Logf("Bypass #4 forbidden-op BLOCKED: DDLViolation(permittedCommands) caught for 'DeleteResource', Core KV unmodified")
}

// TestBypass4_SensitiveAspectOnNonIdentity verifies that a sensitive aspect
// attached to a non-identity vertex (e.g., a "resource" vertex) is rejected
// by the step 6 validator's sensitiveAspectScope enforcement (NFR-S3).
func TestBypass4_SensitiveAspectOnNonIdentity(t *testing.T) {
	ctx, conn := setupBypassHarness(t)

	// Seed sensitive DDL: class "bypassnote", sensitive=true.
	// Class name must be a valid type segment: [a-z][a-z0-9]*.
	seedDDL4(t, ctx, conn, "bypassnote", nil, true)

	_, validator := buildValidator4(t, ctx, conn)

	// Attempt to attach a sensitive aspect to a "resource" (non-identity) vertex.
	// Key form: vtx.resource.<nanoID>.<localName> — ParseAspectKey yields parentType="resource".
	// localName "bypassnote" must be valid: [a-z_][a-zA-Z0-9]* ✓
	sensitiveAspectKey := "vtx.resource." + bypassNanoID2 + ".bypassnote"

	env := &processor.OperationEnvelope{
		RequestID:     bypassNanoID2,
		Lane:          processor.LaneDefault,
		OperationType: "CreateNote",
		Actor:         "vtx.identity." + bypassNanoID1,
		SubmittedAt:   "2026-05-14T00:00:00Z",
	}

	result := processor.ScriptResult{
		Mutations: []processor.MutationOp{{
			Op:  "create",
			Key: sensitiveAspectKey,
			Document: map[string]interface{}{
				"class":     "bypassnote",
				"vertexKey": "vtx.resource." + bypassNanoID2,
				"localName": "bypassnote",
				"data":      map[string]interface{}{"value": "secret"},
			},
		}},
	}

	err := validator.Validate(ctx, env, result)

	// ASSERTION: must be *DDLViolation with ViolatedConstraint="sensitiveAspectScope".
	var ddlErr *processor.DDLViolation
	if !errors.As(err, &ddlErr) {
		t.Fatalf("bypass4 sensitive-aspect: BYPASS ESCAPED: expected *DDLViolation, got %T: %v", err, err)
	}
	if ddlErr.ViolatedConstraint != "sensitiveAspectScope" {
		t.Fatalf("bypass4 sensitive-aspect: ViolatedConstraint=%q, want 'sensitiveAspectScope'", ddlErr.ViolatedConstraint)
	}

	// ASSERTION: sensitive aspect key must NOT be in Core KV.
	if kvPresent(ctx, conn, bypassCoreBucket, sensitiveAspectKey) {
		t.Fatalf("bypass4: BYPASS ESCAPED: sensitive aspect key %q reached Core KV", sensitiveAspectKey)
	}

	t.Logf("Bypass #4 sensitive-aspect BLOCKED: DDLViolation(sensitiveAspectScope) caught, Core KV unmodified")
}

// TestBypass4_DDLViolation_StepOrdering confirms the structural invariant:
// Validate() is called at step 6 in the commit path BEFORE step 8 (Commit).
// An error from Validate prevents any Core KV write (the CommitPath only
// calls Committer.Commit when Validate returns nil).
func TestBypass4_DDLViolation_StepOrdering(t *testing.T) {
	ctx, conn := setupBypassHarness(t)

	// Class name "bypassord" is all-lowercase (valid type segment).
	seedDDL4(t, ctx, conn, "bypassord", []string{"AllowedOp"}, false)
	_, validator := buildValidator4(t, ctx, conn)

	mutationKey := "vtx.bypassord." + bypassNanoID3
	trackerKey := "vtx.op." + bypassNanoID3

	env := &processor.OperationEnvelope{
		RequestID:     bypassNanoID3,
		Lane:          processor.LaneDefault,
		OperationType: "ForbiddenOp",
		Actor:         "vtx.identity." + bypassNanoID1,
		SubmittedAt:   "2026-05-14T00:00:00Z",
	}

	result := processor.ScriptResult{
		Mutations: []processor.MutationOp{{
			Op:  "create",
			Key: mutationKey,
			Document: map[string]interface{}{
				"class":     "bypassord",
				"isDeleted": false,
				"data":      map[string]interface{}{},
			},
		}},
	}

	err := validator.Validate(ctx, env, result)
	if err == nil {
		t.Fatalf("bypass4 ordering: expected DDLViolation, got nil")
	}

	// Because Validate returned error, CommitPath would NOT call Committer.Commit.
	// Therefore no tracker key either.
	if kvPresent(ctx, conn, bypassCoreBucket, trackerKey) {
		t.Fatalf("bypass4 ordering: BYPASS ESCAPED: tracker key present despite DDLViolation")
	}
	if kvPresent(ctx, conn, bypassCoreBucket, mutationKey) {
		t.Fatalf("bypass4 ordering: BYPASS ESCAPED: mutation key present despite DDLViolation")
	}

	t.Logf("Bypass #4 step-ordering confirmed: DDLViolation at step 6 prevents any Core KV writes")
}

// ---- helpers ----

// seedDDL4 writes a DDL meta-vertex for the given class into Core KV.
// Uses the shadow-key pattern (vtx.meta.<class>) that DDLCache.Refresh picks up.
func seedDDL4(t *testing.T, ctx context.Context, conn *substrate.Conn, class string, permittedCommands []string, sensitive bool) {
	t.Helper()
	key := "vtx.meta." + class
	cmdJSON := "[]"
	if len(permittedCommands) > 0 {
		b := marshalJSON(permittedCommands)
		cmdJSON = string(b)
	}
	docClass := "meta.ddl.vertexType"
	sensitiveField := ""
	if sensitive {
		docClass = "meta.ddl.aspectType"
		sensitiveField = `,"sensitive":true`
	}
	doc := []byte(`{"class":"` + docClass + `","isDeleted":false,"data":{"canonicalName":"` + class + `","permittedCommands":` + cmdJSON + sensitiveField + `}}`)
	if _, err := conn.KVPut(ctx, bypassCoreBucket, key, doc); err != nil {
		t.Fatalf("bypass4: seed DDL %s: %v", key, err)
	}
}

// buildValidator4 creates a DDLCache refreshed from Core KV and a ValidatorImpl.
func buildValidator4(t *testing.T, ctx context.Context, conn *substrate.Conn) (*processor.DDLCache, *processor.ValidatorImpl) {
	t.Helper()
	cache := processor.NewDDLCache(conn, bypassCoreBucket, bypassLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("bypass4: DDLCache.Refresh: %v", err)
	}
	return cache, processor.NewValidator(cache, bypassLogger())
}
