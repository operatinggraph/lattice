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

// authTargetValidated reports whether the auth path that matched actually
// checked `authContext.target` against something the platform knows, rather
// than letting a client-supplied value through unexamined.
//
// Only two paths validate it:
//   - platform scope=self — matchPlatformPermission denies unless target == actor,
//     and denies outright when target is absent
//   - task — matchEphemeralGrant skips any grant whose target != authContext.target
//
// scope=any, the service path, and the stub authorizer (rp == nil) never read
// target, so it is an unchecked hint there and this is false. Fail-closed: an
// unrecognized or absent path is "not validated", never "trusted".
//
// The task path additionally requires the matched grant to NAME a target. Its
// comparison is an inequality-skip, so a grant projected with an empty target
// matches an authContext that carries none — agreeing about nothing. Treating
// that as validated would exempt a caller who supplied no target at all, which
// is strictly weaker than the presence test this bit replaces in the guards.
func authTargetValidated(rp *ResolvedPermission) bool {
	if rp == nil {
		return false
	}
	switch rp.Path {
	case "task":
		return rp.EphemeralGrant != nil && rp.EphemeralGrant.Target != ""
	case "platform":
		return rp.PlatformPermission != nil && rp.PlatformPermission.Scope == "self"
	default:
		return false
	}
}
