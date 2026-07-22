// Package capabilityenv holds the role-by-operation index envelope — the
// operation-aggregate Capability KV projection (Contract #6 §6.1). It is the
// one remaining target-specific envelope: the actor-aggregate projections
// (cap.<actor>, cap.ephemeral.<actor>, my-tasks.<actor>) are driven
// declaratively by the projection-plan compiler's Output descriptor.
package capabilityenv

import (
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

// NewRoleIndexWrapper returns the EnvelopeFn for the role-by-operation index
// lens (Contract #6 §6.1).
//
// Input row (produced by the cypher RETURN):
//
//	{operationType: "read", roles: [...], projectedAt: "..."}
//
// Output (Contract #6 §6.1 secondary-key shape):
//
//	{key: "cap.role-by-operation.<operationType>",
//	 projectedAt: <projectedAt>,
//	 roles: [...]}
//
// Rows whose operationType is null/empty are dropped (ErrSkipProjection) —
// the executor's `collect` over zero MATCH bindings produces such rows
// when the CDC event doesn't touch a role/permission grant.
func NewRoleIndexWrapper() pipeline.EnvelopeFn {
	return func(row map[string]any, keys map[string]any, params map[string]any) (map[string]any, map[string]any, error) {
		op, _ := row["operationType"].(string)
		if op == "" {
			return nil, nil, pipeline.ErrSkipProjection
		}
		projectedAt, _ := row["projectedAt"].(string)
		if projectedAt == "" {
			projectedAt, _ = params["projectedAt"].(string)
		}
		envKey := "cap.role-by-operation." + op
		envelope := map[string]any{
			"key":         envKey,
			"projectedAt": projectedAt,
			"roles":       emptyArrayIfNil(row["roles"]),
		}
		// The natskv adapter constructs the bucket key from the seeded
		// Into.Key list, which for the role-index lens is ["operationType"].
		// Set that field to the full Contract #6 §6.1 key so the bucket
		// entry lands at `cap.role-by-operation.<op>`.
		return envelope, map[string]any{"operationType": envKey}, nil
	}
}

func emptyArrayIfNil(v any) any {
	if v == nil {
		return []any{}
	}
	return v
}
