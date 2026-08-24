package wellnessledger

// maxChargeRetries caps how many times Weaver auto-dispatches WellnessDebitAccount
// for one no-show booking before it stops and leaves the gap violating for
// operator attention (Contract #10 §10.3): the lens projects it as the
// constant maxretries_charge column on every wellnessNoShowSettlement row,
// and Weaver bounds its per-(target, entity, gap) dispatch-count in
// weaver-state against this cap (lease-signing's retry_budget.go — the
// shipped precedent). The count is deleted when the gap closes (the charge
// posts), so a later no-show starts a fresh budget.
const maxChargeRetries = 3

// maxPriceChargeRetries caps how many times Weaver auto-dispatches
// WellnessDebitAccount for one priced booking's class-price charge before it stops
// and leaves the gap violating for operator attention (Contract #10 §10.3):
// the lens projects it as the constant maxretries_price_charge column on every
// wellnessClassPriceSettlement row, and Weaver bounds its per-(target,
// entity, gap) dispatch-count in weaver-state against this cap (the same
// budget-per-target idiom maxChargeRetries above establishes). The count is
// deleted when the gap closes (the charge posts), so a later booking on a
// re-priced session starts a fresh budget.
const maxPriceChargeRetries = 3

// maxRefundRetries caps how many times Weaver auto-dispatches
// WellnessCreditAccount for one wellnessrefund marker before it stops and
// leaves the gap violating for operator attention (Contract #10 §10.3): the
// lens projects it as the constant maxretries_refund column on every
// wellnessRefundSettlement row, the same budget-per-target idiom
// maxChargeRetries/maxPriceChargeRetries above establish. The count is
// deleted when the gap closes (the credit posts) — a wellnessrefund is
// minted at most once per cancelled booking (CancelBooking, wellness-domain),
// so there is no "later" refund to reuse the budget.
const maxRefundRetries = 3
