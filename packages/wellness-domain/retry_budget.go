package wellnessdomain

// maxReleaseRetries caps how many times Weaver auto-dispatches
// ReleaseOrphanedBooking for one booking before it stops and leaves the gap
// violating for operator attention (Contract #10 §10.3): the lens projects it
// as the constant maxretries_release column on every
// wellnessOrphanedBookingSettlement row, and Weaver bounds its
// per-(target, entity, gap) dispatch-count in weaver-state against this cap
// (lease-signing's retry_budget.go — the shipped precedent). The count is
// deleted when the gap closes (the booking is released), so a later orphaning
// starts a fresh budget.
const maxReleaseRetries = 3

// maxPromotionRetries caps how many times Weaver auto-dispatches
// PromoteWaitlistedBookings for one session before it stops and leaves the
// gap violating for operator attention (Contract #10 §10.3) — the same budget
// shape maxReleaseRetries carries, projected as the constant
// maxretries_promotion column on every wellnessWaitlistPromotion row. One
// dispatch seats every candidate the session has room for, so a second
// attempt only ever follows a failed first one; the count is deleted when the
// gap closes (no waitlist left, or no seat left free), so a later capacity
// raise starts a fresh budget.
const maxPromotionRetries = 3
