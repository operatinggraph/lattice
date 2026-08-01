package orchestrationbase

// maxOperationRetries and maxClaimRetries cap how many times Weaver
// auto-dispatches a convergence target's remediation for one gap before it
// stops and leaves the row violating for operator attention (Contract #10
// §10.3): each lens projects its own constant as the maxretries_<gap> column
// on every convergence row, and Weaver bounds its per-(target, entity, gap)
// dispatch-count in weaver-state against that cap (lease-signing's
// retry_budget.go — the shipped precedent). The count is deleted when the
// gap closes, so a later re-open starts a fresh budget.
//
// orphanedTaskGrants dispatches directOp(CancelTask) against maxretries_operation.
// unroutedTasks' one gap is Action:"surface" (never dispatched, so its
// weaver-state dispatch-count never advances past zero), but maxretries_claim
// is declared here too — every §10.2 target that carries a gap column is
// capped uniformly, ready the moment any target's playbook action changes to
// a real dispatch.
const (
	maxOperationRetries = 3
	maxClaimRetries     = 3
)
