package control

// TargetIDFromSubject exposes the unexported targetIDFromSubject for the
// control_test package. The disable/enable/revoke endpoints register on the
// wildcard subject lattice.ctrl.weaver.*.<op>, so NATS subject routing can
// only ever deliver a conforming 5-token subject to dispatchEndpoint — the
// parser's deviation branches are an unreachable-via-NATS defensive boundary.
// Exposing the helper lets those branches be table-tested directly, guarding
// against a future direct caller or a refactor that loosens the wildcard.
var TargetIDFromSubject = targetIDFromSubject

// ResetBudgetScopeFromSubject exposes the unexported
// resetBudgetScopeFromSubject for the control_test package, for the same reason
// TargetIDFromSubject is exposed: the resetBudget endpoint registers on the
// wildcard lattice.ctrl.weaver.*.resetBudget.*.*, so NATS routing can only
// deliver a conforming 7-token subject with non-empty tokens, and the parser's
// deviation branches are a defensive boundary reachable only by a direct
// caller.
var ResetBudgetScopeFromSubject = resetBudgetScopeFromSubject
