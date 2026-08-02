package cafedomain

// maxSettleRetries caps how many times Weaver auto-dispatches SettleStaleTab
// for one tab before it stops and leaves the gap violating for operator
// attention (Contract #10 §10.3): the lens projects it as the constant
// maxretries_settle column on every cafeStaleTabSettlement row, and Weaver
// bounds its per-(target, entity, gap) dispatch-count in weaver-state against
// this cap (wellness-domain's retry_budget.go — the shipped precedent). The
// count is deleted when the gap closes (the tab is settled), so a later
// reopen-and-go-stale starts a fresh budget.
const maxSettleRetries = 3
