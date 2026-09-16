package loftspaceledger

// maxArrearsEvaluationRetries caps how many times Weaver auto-dispatches
// EvaluateLoftspaceArrears for one account's open missing_evaluation gap
// before it stops and leaves the gap violating for operator attention
// (Contract #10 §10.3): the lens projects it as the constant
// maxretries_evaluation column on every loftspaceArrearsReminders row, and
// Weaver bounds its per-(target, entity, gap) dispatch-count in weaver-state
// against this cap (the budget-per-target idiom lease-signing's
// retry_budget.go establishes). The count is deleted when the gap closes (the
// evaluation stamps remindedFor = dueAt, or the recomputation clears stale),
// so a later episode — or a later stale mark — starts a fresh budget; an
// evaluation that cannot succeed at all records historyTooLong, which closes
// the gap by suppression rather than spending the budget on a doomed loop.
const maxArrearsEvaluationRetries = 3
