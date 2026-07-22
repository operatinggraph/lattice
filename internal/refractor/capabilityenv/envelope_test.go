package capabilityenv

import (
	"errors"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

// TestRoleIndexWrapper_Projects asserts the role-by-operation index row is
// rewritten into the Contract #6 §6.1 cap.role-by-operation.<op> shape and the
// bucket key is set on the operationType field.
func TestRoleIndexWrapper_Projects(t *testing.T) {
	fn := NewRoleIndexWrapper()
	row := map[string]any{
		"operationType": "read",
		"roles":         []any{"operator"},
		"projectedAt":   "2026-05-15T10:00:00Z",
	}
	env, keys, err := fn(row, nil, map[string]any{})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if env["key"] != "cap.role-by-operation.read" {
		t.Fatalf("key: %v", env["key"])
	}
	if keys["operationType"] != "cap.role-by-operation.read" {
		t.Fatalf("bucket key must be set on operationType: %v", keys)
	}
	if _, ok := env["roles"].([]any); !ok {
		t.Fatalf("roles must be an array: %v", env["roles"])
	}
}

// TestRoleIndexWrapper_NullOperationType_Skips asserts a row with an empty
// operationType (a collect over zero MATCH bindings) is declined.
func TestRoleIndexWrapper_NullOperationType_Skips(t *testing.T) {
	fn := NewRoleIndexWrapper()
	_, _, err := fn(map[string]any{"operationType": ""}, nil, map[string]any{})
	if !errors.Is(err, pipeline.ErrSkipProjection) {
		t.Fatalf("expected ErrSkipProjection, got %v", err)
	}
}

// TestRoleIndexWrapper_EmptyRoles_DefaultsToArray asserts a nil roles value
// materializes as an empty array rather than null.
func TestRoleIndexWrapper_EmptyRoles_DefaultsToArray(t *testing.T) {
	fn := NewRoleIndexWrapper()
	env, _, err := fn(map[string]any{"operationType": "write"}, nil, map[string]any{"projectedAt": "t"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	roles, ok := env["roles"].([]any)
	if !ok || len(roles) != 0 {
		t.Fatalf("roles must default to empty array; got %v", env["roles"])
	}
}
