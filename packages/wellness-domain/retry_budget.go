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
