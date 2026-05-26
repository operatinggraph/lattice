package processor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/asolgan/lattice/internal/substrate"
)

// DDLViolation is the typed step-6 failure. Carries the violated
// constraint name, the offending mutation key, and the operation's
// requestId so the commit path can construct a rejection reply.
type DDLViolation struct {
	ViolatedConstraint string // e.g., "permittedCommands", "sensitiveAspectScope", "keyPattern"
	MutationKey        string
	OperationRequestID string
	Detail             string
}

func (e *DDLViolation) Error() string {
	return fmt.Sprintf("DDLViolation[%s]: requestId=%s mutationKey=%s: %s",
		e.ViolatedConstraint, e.OperationRequestID, e.MutationKey, e.Detail)
}

// ValidatorImpl is the step-6 DDL validator. Step 6 enforces:
//   - Key pattern validity (Contract #1 §1.1 — must parse via the
//     substrate parsers).
//   - permittedCommands when the affected DDL declares the constraint.
//   - Sensitive aspect write-scope — sensitive aspects may attach ONLY
//     to identity-typed vertices (NFR-S3).
//   - mutation.op ∈ {create, update, tombstone}.
//
// Per Contract #1 §1.5/§1.6 the permissive-by-default rule applies:
// when no DDL is found for a mutation's class, the corresponding
// schema/permittedCommands/sensitive checks are skipped (a permissive
// pass-through). Other checks (key pattern, op enum) apply regardless.
type ValidatorImpl struct {
	DDLs   *DDLCache
	Logger *slog.Logger
}

// NewValidator wires a real Validator backed by the DDL cache.
func NewValidator(cache *DDLCache, logger *slog.Logger) *ValidatorImpl {
	if cache == nil {
		panic("processor: NewValidator requires DDLCache")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ValidatorImpl{DDLs: cache, Logger: logger}
}

// Validate implements Validator. Walks each mutation in result and
// returns the first DDLViolation encountered (commit path semantics:
// "any DDL violation terminates the commit path").
func (v *ValidatorImpl) Validate(_ context.Context, env *OperationEnvelope, result ScriptResult) error {
	rid := env.RequestID
	for _, m := range result.Mutations {
		if err := v.validateOne(env, m, rid); err != nil {
			return err
		}
	}
	v.Logger.Info("step 6: validated",
		"requestId", rid,
		"mutations", len(result.Mutations))
	return nil
}

// validateOne enforces the per-mutation rules. Public-shape returned
// error is always *DDLViolation when violation; nil on success.
func (v *ValidatorImpl) validateOne(env *OperationEnvelope, m MutationOp, rid string) error {
	// 1. op enum.
	switch m.Op {
	case "create", "update", "tombstone":
	default:
		return &DDLViolation{
			ViolatedConstraint: "opEnum",
			MutationKey:        m.Key,
			OperationRequestID: rid,
			Detail:             fmt.Sprintf("op %q not in {create, update, tombstone}", m.Op),
		}
	}

	// 2. Key pattern — must parse as vertex, aspect, or link.
	kind := substrate.ClassifyKey(m.Key)
	if kind == substrate.KindUnknown {
		return &DDLViolation{
			ViolatedConstraint: "keyPattern",
			MutationKey:        m.Key,
			OperationRequestID: rid,
			Detail:             "key does not match Contract #1 vertex/aspect/link patterns",
		}
	}

	// 3. Class derivation from document. For tombstones the document is
	// optional — if absent, skip DDL lookups.
	class := ""
	if m.Document != nil {
		if v, ok := m.Document["class"].(string); ok {
			class = v
		}
	}

	// 4. DDL-driven checks (only when DDL is present — permissive
	// default per Contract #1 §1.5/§1.6).
	if class != "" {
		if ref, ok := v.DDLs.Lookup(class); ok {
			// permittedCommands enforcement: when the DDL declares a
			// non-empty list, the operation envelope's operationType
			// must appear in it.
			if len(ref.PermittedCommands) > 0 {
				if !stringInSlice(env.OperationType, ref.PermittedCommands) {
					return &DDLViolation{
						ViolatedConstraint: "permittedCommands",
						MutationKey:        m.Key,
						OperationRequestID: rid,
						Detail: fmt.Sprintf("operationType %q not permitted by DDL meta-vertex %q (permittedCommands %v)",
							env.OperationType, ref.MetaVertexKey, ref.PermittedCommands),
					}
				}
			}

			// Sensitive aspect write-scope (NFR-S3). Only meaningful for
			// aspect mutations: sensitive aspects may only attach to
			// identity-typed vertices.
			if ref.Sensitive && kind == substrate.KindAspect {
				_, parentType, _, _, ok := substrate.ParseAspectKey(m.Key)
				if !ok {
					return &DDLViolation{
						ViolatedConstraint: "keyPattern",
						MutationKey:        m.Key,
						OperationRequestID: rid,
						Detail:             "aspect key failed to parse",
					}
				}
				if parentType != "identity" {
					return &DDLViolation{
						ViolatedConstraint: "sensitiveAspectScope",
						MutationKey:        m.Key,
						OperationRequestID: rid,
						Detail: fmt.Sprintf("sensitive aspect %q may only attach to identity vertices; parent type is %q",
							ref.CanonicalName, parentType),
					}
				}
			}
		}
	}

	return nil
}

func stringInSlice(s string, list []string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// hasMetaVertexMutation returns true when any mutation in the batch
// targets a `vtx.meta.*` key. Used by the Committer to decide whether
// to invalidate the DDL cache after a successful commit. Exported for
// the Committer's use.
func hasMetaVertexMutation(muts []MutationOp) bool {
	for _, m := range muts {
		if strings.HasPrefix(m.Key, "vtx.meta.") {
			return true
		}
	}
	return false
}
