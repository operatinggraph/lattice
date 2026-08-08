package processor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
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
	// *ddlResolver carries DDLs + linkReader and the shared governing-DDL /
	// instanceOf-chain resolution (step6_resolve_ddl.go) — the same
	// resolution step 6.5's encrypt and decrypt-on-read reuse via their own
	// ddlResolver, so a sensitive class resolvable only via the chain is
	// treated identically by every path that needs to know it's sensitive.
	*ddlResolver
	Logger *slog.Logger
}

// NewValidator wires a real Validator backed by the DDL cache. conn/coreBucket
// back the on-demand instanceOf reader used by the step-6 governing-DDL walk; a
// nil conn (test affordance) leaves on-demand discovery disabled.
func NewValidator(cache *DDLCache, conn *substrate.Conn, coreBucket string, logger *slog.Logger) *ValidatorImpl {
	if cache == nil {
		panic("processor: NewValidator requires DDLCache")
	}
	if logger == nil {
		logger = slog.Default()
	}
	v := &ValidatorImpl{ddlResolver: &ddlResolver{DDLs: cache, Logger: logger}, Logger: logger}
	if conn != nil && coreBucket != "" {
		v.linkReader = &connInstanceOfReader{conn: conn, coreBucket: coreBucket}
		v.classReader = &connVertexClassReader{conn: conn, coreBucket: coreBucket}
	}
	return v
}

// Validate implements Validator. Walks each mutation in result and
// returns the first DDLViolation encountered (commit path semantics:
// "any DDL violation terminates the commit path"). state carries the hydrated
// working set + on-demand KV reader so the step-6 governing-DDL walk
// (Contract #1 §1.5) can resolve a fine-grained-class vertex's type authority
// via its instanceOf chain.
func (v *ValidatorImpl) Validate(ctx context.Context, env *OperationEnvelope, result ScriptResult, state HydratedState) error {
	rid := env.RequestID
	if err := validateExternalEgressGuard(result, state, rid); err != nil {
		return err
	}
	for _, m := range result.Mutations {
		if err := v.validateOne(ctx, env, m, result, state, rid); err != nil {
			return err
		}
	}
	v.Logger.Info("step 6: validated",
		"requestId", rid,
		"mutations", len(result.Mutations))
	return nil
}

// validateExternalEgressGuard enforces the design sensitive-param-egress
// §3.6 commit-path guard: an op that emits an `external.*`-domain event AND
// decrypted any sensitive aspect as plaintext this execution (via `reads`,
// `optionalReads`, or a lazy kv.Read not under `egressReads`) is rejected —
// sensitive data may reach an external event only as a ref via
// `contextHint.egressReads`. Scope is deliberately the external-egress plane
// only: an op emitting no external.* event may still decrypt and derive a
// value into an ordinary domain event, today's DDL-trust surface, unchanged.
func validateExternalEgressGuard(result ScriptResult, state HydratedState, rid string) error {
	tracker := state.Context.SensitiveReads
	if tracker == nil || !tracker.plaintextRead {
		return nil
	}
	for _, ev := range result.Events {
		if eventDomain(ev.Class) != "external" {
			continue
		}
		return &DDLViolation{
			ViolatedConstraint: "externalEgressSensitivePlaintext",
			OperationRequestID: rid,
			Detail: fmt.Sprintf(
				"event class %q (external-egress domain) rejected: this execution decrypted a sensitive aspect as plaintext; sensitive data may reach an external event only as a contextHint.egressReads ref",
				ev.Class),
		}
	}
	return nil
}

// validateOne enforces the per-mutation rules. Public-shape returned
// error is always *DDLViolation when violation; nil on success.
func (v *ValidatorImpl) validateOne(ctx context.Context, env *OperationEnvelope, m MutationOp, result ScriptResult, state HydratedState, rid string) error {
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

	// 4. DDL-driven checks (only when a governing DDL resolves — permissive
	// default per Contract #1 §1.5/§1.6). Resolution is exact class→DDL first
	// (today's fast path), then the bounded instanceOf-chain walk to the type
	// authority for a fine-grained discriminator class that has no direct DDL.
	if class != "" {
		if ref, ok := v.resolveGoverningDDL(ctx, class, m.Key, kind, result, state); ok {
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

			// Sensitive aspect write-scope (NFR-S3), CONDITIONAL ON THE
			// DECLARED CUSTODY KIND (retention-class-key-custody-design.md
			// §4.1). Only meaningful for aspect mutations.
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
				if err := validateSensitiveCustody(ref, m.Key, parentType, rid); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// validateSensitiveCustody enforces the sensitive-aspect anchoring rule for
// the DDL's declared custody kind (retention-class-key-custody-design.md
// §4.1). The fail-closed default is the whole point of the shape: a package
// that flips Sensitive:true on an appointment-anchored aspect and forgets to
// declare custody gets today's rejection, NOT plaintext at rest.
func validateSensitiveCustody(ref MetaVertexRef, mutationKey, parentType, rid string) error {
	switch ref.CustodyKind {
	case "", CustodyKindIdentity:
		// Byte-identical to the pre-custody rule: the DEK is the anchoring
		// identity's, so the anchor must BE an identity or there is no holder.
		if parentType != "identity" {
			return &DDLViolation{
				ViolatedConstraint: "sensitiveAspectScope",
				MutationKey:        mutationKey,
				OperationRequestID: rid,
				Detail: fmt.Sprintf("sensitive aspect %q may only attach to identity vertices; parent type is %q",
					ref.CanonicalName, parentType),
			}
		}
		return nil

	case CustodyKindRetentionClass:
		// Any anchor is permitted — the holder is the declared class, not the
		// parent. What IS checked is that the install actually resolved a
		// holder of the right type. This validates a value the INSTALL
		// produced, never caller input, so it can only fail if a DDL was
		// written or migrated wrong; failing closed keeps a malformed custody
		// declaration from committing plaintext-adjacent state under a key
		// step 6.5 could not mint.
		holder := ref.CustodyHolderKey
		holderType, _, ok := substrate.ParseVertexKey(holder)
		if !ok || holderType != RetentionClassVertexType {
			return &DDLViolation{
				ViolatedConstraint: "sensitiveAspectScope",
				MutationKey:        mutationKey,
				OperationRequestID: rid,
				Detail: fmt.Sprintf("sensitive aspect %q declares custody kind %q but its holder key %q is not a vtx.%s.<NanoID> vertex",
					ref.CanonicalName, CustodyKindRetentionClass, holder, RetentionClassVertexType),
			}
		}
		return nil

	default:
		return &DDLViolation{
			ViolatedConstraint: "sensitiveAspectScope",
			MutationKey:        mutationKey,
			OperationRequestID: rid,
			Detail: fmt.Sprintf("sensitive aspect %q declares unknown custody kind %q",
				ref.CanonicalName, ref.CustodyKind),
		}
	}
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
