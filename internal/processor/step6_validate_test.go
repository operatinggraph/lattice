package processor

import (
	"context"
	"errors"
	"testing"
)

// helper: cache pre-seeded with "identity" (vertexType DDL,
// permittedCommands=["CreateIdentity"]) and "email" (aspectType DDL,
// sensitive=true, permittedCommands=["CreateIdentity"]).
func seedSensitiveAspectDDL(t *testing.T, ctx context.Context, conn substrateConn, class string) {
	t.Helper()
	root := "vtx.meta." + class
	doc := []byte(`{"class":"meta.ddl.aspectType","isDeleted":false,"data":{"canonicalName":"` + class + `","sensitive":true,"permittedCommands":["CreateIdentity"]}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root, doc); err != nil {
		t.Fatalf("seed sensitive DDL %s: %v", root, err)
	}
}

// substrateConn is a tiny interface so the helper above is testable
// against either *substrate.Conn or a fake; in practice only the real
// conn is used.
type substrateConn interface {
	KVPut(ctx context.Context, bucket, key string, value []byte) (uint64, error)
}

func buildValidatorWithCache(t *testing.T) (*ValidatorImpl, *DDLCache, context.Context) {
	t.Helper()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedSensitiveAspectDDL(t, ctx, conn, "email")
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return NewValidator(cache, conn, testCoreBucket, testLogger()), cache, ctx
}

func TestValidate_CleanPass(t *testing.T) {
	t.Parallel()
	v, _, ctx := buildValidatorWithCache(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.identity." + testNanoID2,
			Document: map[string]interface{}{
				"class":     "identity",
				"isDeleted": false,
				"data":      map[string]interface{}{"name": "Andrew"},
			},
		}},
	}
	if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidate_PermittedCommandsViolation(t *testing.T) {
	t.Parallel()
	v, _, ctx := buildValidatorWithCache(t)
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "DeleteIdentity" // not in identity DDL's list
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.identity." + testNanoID2,
			Document: map[string]interface{}{
				"class": "identity",
				"data":  map[string]interface{}{},
			},
		}},
	}
	err := v.Validate(ctx, env, result, HydratedState{})
	var ddlErr *DDLViolation
	if !errors.As(err, &ddlErr) {
		t.Fatalf("expected *DDLViolation, got %T: %v", err, err)
	}
	if ddlErr.ViolatedConstraint != "permittedCommands" {
		t.Fatalf("ViolatedConstraint = %q", ddlErr.ViolatedConstraint)
	}
}

func TestValidate_SensitiveAspectOnNonIdentityRejected(t *testing.T) {
	t.Parallel()
	v, _, ctx := buildValidatorWithCache(t)
	env := newTestEnvelope(testNanoID1)
	// Aspect attached to a "lease" vertex — should fail (email is sensitive).
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.lease." + testNanoID2 + ".workEmail",
			Document: map[string]interface{}{
				"class":     "email",
				"vertexKey": "vtx.lease." + testNanoID2,
				"localName": "workEmail",
				"data":      map[string]interface{}{"value": "x@y"},
			},
		}},
	}
	err := v.Validate(ctx, env, result, HydratedState{})
	var ddlErr *DDLViolation
	if !errors.As(err, &ddlErr) {
		t.Fatalf("expected *DDLViolation, got %T: %v", err, err)
	}
	if ddlErr.ViolatedConstraint != "sensitiveAspectScope" {
		t.Fatalf("ViolatedConstraint = %q", ddlErr.ViolatedConstraint)
	}
}

func TestValidate_SensitiveAspectOnIdentityAllowed(t *testing.T) {
	t.Parallel()
	v, _, ctx := buildValidatorWithCache(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.identity." + testNanoID2 + ".workEmail",
			Document: map[string]interface{}{
				"class":     "email",
				"vertexKey": "vtx.identity." + testNanoID2,
				"localName": "workEmail",
				"data":      map[string]interface{}{"value": "x@y"},
			},
		}},
	}
	if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("Validate (allowed): %v", err)
	}
}

func TestValidate_KeyPatternViolation(t *testing.T) {
	t.Parallel()
	v, _, ctx := buildValidatorWithCache(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "not-a-valid-key",
			Document: map[string]interface{}{
				"class": "identity",
			},
		}},
	}
	err := v.Validate(ctx, env, result, HydratedState{})
	var ddlErr *DDLViolation
	if !errors.As(err, &ddlErr) {
		t.Fatalf("expected *DDLViolation, got %T: %v", err, err)
	}
	if ddlErr.ViolatedConstraint != "keyPattern" {
		t.Fatalf("ViolatedConstraint = %q", ddlErr.ViolatedConstraint)
	}
}

func TestValidate_UnknownOpRejected(t *testing.T) {
	t.Parallel()
	v, _, ctx := buildValidatorWithCache(t)
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "merge",
			Key: "vtx.identity." + testNanoID2,
			Document: map[string]interface{}{
				"class": "identity",
			},
		}},
	}
	err := v.Validate(ctx, env, result, HydratedState{})
	var ddlErr *DDLViolation
	if !errors.As(err, &ddlErr) {
		t.Fatalf("expected *DDLViolation, got %T: %v", err, err)
	}
	if ddlErr.ViolatedConstraint != "opEnum" {
		t.Fatalf("ViolatedConstraint = %q", ddlErr.ViolatedConstraint)
	}
}

func TestValidate_UndeclaredClassIsPermissive(t *testing.T) {
	t.Parallel()
	v, _, ctx := buildValidatorWithCache(t)
	env := newTestEnvelope(testNanoID1)
	// "anomalyFlag" is not in the DDL cache. The validator should
	// allow it through (permissive default per Contract #1 §1.6).
	result := ScriptResult{
		Mutations: []MutationOp{{
			Op:  "create",
			Key: "vtx.identity." + testNanoID2 + ".anomalyFlag",
			Document: map[string]interface{}{
				"class":     "anomalyFlag",
				"vertexKey": "vtx.identity." + testNanoID2,
				"localName": "anomalyFlag",
				"data":      map[string]interface{}{"reason": "test"},
			},
		}},
	}
	if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("Validate (permissive): %v", err)
	}
}
