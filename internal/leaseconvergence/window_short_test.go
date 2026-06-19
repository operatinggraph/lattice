//go:build leaseshortwindow

package leaseconvergence_test

import "time"

// shortFreshnessWindow is true under -tags leaseshortwindow: the freshness window
// is seconds, so the eager-freshness leg can watch the bgcheck lapse and assert
// the @at arms + fires within the test's bounded deadline.
const shortFreshnessWindow = true

// freshnessWindow mirrors lease-signing's bgcheckFreshnessWindow under the same
// `leaseshortwindow` build tag (packages/lease-signing/freshness_window_short.go):
// the validity span the replyOp stamps as validUntil, and so the wall-clock a
// bgcheck takes to lapse after each converge. The harness derives its context
// deadline from this so the value stays in lock-step with the lens window rather
// than a drifting magic number.
const freshnessWindow = 75 * time.Second
