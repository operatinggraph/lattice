package control

// TargetIDFromSubject exposes the unexported targetIDFromSubject for the
// control_test package. The disable/enable/revoke endpoints register on the
// wildcard subject lattice.ctrl.weaver.*.<op>, so NATS subject routing can
// only ever deliver a conforming 5-token subject to dispatchEndpoint — the
// parser's deviation branches are an unreachable-via-NATS defensive boundary.
// Exposing the helper lets those branches be table-tested directly, guarding
// against a future direct caller or a refactor that loosens the wildcard.
var TargetIDFromSubject = targetIDFromSubject
