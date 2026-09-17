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

// maxOccurrenceRetries caps how many times Weaver auto-dispatches
// ExtendSessionSeries for one rolling series before it stops and leaves the
// gap violating for operator attention (Contract #10 §10.3) — the same budget
// shape maxReleaseRetries carries, projected as the constant
// maxretries_occurrence AND maxretries_led_occurrence columns on every
// wellnessSeriesHorizon row (Weaver reads the budget under each gap's own
// suffix, so both gaps carry it). One dispatch mints (or skips) the one
// occurrence the window moves onto and advances the horizon past the
// recorded lapse, so a second attempt only ever follows a refused first one
// — a stale row, or a studio retired between the projection and the
// dispatch (a retired instructor is not a refusal: the class is minted unled
// and the horizon drops them); the count is deleted when the gap closes (the
// horizon moved, or the desk stopped or moved the run), so the next window
// starts a fresh budget.
const maxOccurrenceRetries = 3
