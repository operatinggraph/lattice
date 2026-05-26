// Per-operation context threading for the resolved Capability KV permission
// entry. The resolved permission is carried on the Decision struct and threaded
// through steps 4-10 as a local variable on the HandleMessage stack frame —
// no context.Value indirection needed for a value scoped to a single goroutine.
// Strictly internal: never bleed into OperationEnvelope or OperationReply.
package processor

// ResolvedPermission is the auth path + matched entry pointers chosen at
// step 3. The pointers are into the parsed CapabilityDoc; lifecycle is
// the commit-path goroutine handling one envelope, so escape concerns
// are scoped to a single operation.
type ResolvedPermission struct {
	// CapKey is the Capability KV key that backed this decision.
	CapKey string
	// ProjectedAt echoes the doc's projection timestamp — observability /
	// denial response can include this without re-reading the doc.
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
