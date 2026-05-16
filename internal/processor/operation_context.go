// Story 3.3 — per-operation context threading for the resolved
// Capability KV permission entry.
//
// AC #3: "the resolved permission entry is attached to the operation
// context for downstream observability." Story 3.4 (denial response) and
// 3.5 (auth failure traceability) consume this. Decision #8: keep
// strictly internal — do NOT bleed into OperationEnvelope or
// OperationReply.
package processor

import "context"

// ResolvedPermission is the auth path + matched entry pointers chosen at
// step 3. The pointers are into the parsed CapabilityDoc; lifecycle is
// the commit-path goroutine handling one envelope, so escape concerns
// are scoped to a single operation.
type ResolvedPermission struct {
	// CapKey is the Capability KV key that backed this decision.
	CapKey string
	// ProjectedAt echoes the doc's projection timestamp — observability /
	// 3.4 denial response can include this without re-reading the doc.
	ProjectedAt string
	// Path is one of "platform" / "service" / "task" — the dispatch
	// branch that matched. Empty when no match (denial).
	Path string
	// Exactly one of the three is non-nil on success.
	PlatformPermission *PlatformPermission
	ServiceAccess      *ServiceAccessEntry
	AllowedOperation   *AllowedOperation
	EphemeralGrant     *EphemeralGrant
}

// Story 3.3 surfaces ResolvedPermission on the Decision struct directly
// (see step3_auth.go). The commit path captures it from the Authorize
// return value and threads it through steps 4-10 as a local variable on
// the HandleMessage stack frame — no context.Value indirection needed
// for a value that's scoped to a single goroutine + single envelope.
//
// Story 3.4 / 3.5 will widen the consumer surface (denial response +
// event-publication metadata). For now the producer side is correct and
// the value flows.
var _ = context.Background // keep the import live for downstream stories
