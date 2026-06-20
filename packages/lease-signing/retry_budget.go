package leasesigning

// maxBgcheckRetries and maxPaymentRetries cap how many external-call attempts a
// gap tolerates before Weaver stops auto-dispatching it. The lens projects each
// as the constant maxretries_<gap> column on every convergence row; Weaver bounds
// its per-(target, entity, gap) dispatch-count in weaver-state against that cap.
// Once the count reaches the cap the gap stays violating but is no longer
// auto-dispatched — the terminal is "stop and escalate," not a silent reject. A
// human (or a control-plane action) resolves it; a check that completes closes the
// gap, which deletes the dispatch-count so a later renewal starts a fresh budget.
//
// The two families are capped independently. The values are baked into
// leaseApplicationCompleteSpec at package-init time (compile-time constants), the
// §10.2 "the policy lives in the cypher" convention — the same posture as
// bgcheckFreshnessWindow.
const (
	maxBgcheckRetries = 3
	maxPaymentRetries = 3
)
