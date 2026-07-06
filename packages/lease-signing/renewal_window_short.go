//go:build leaseshortwindow

package leasesigning

// renewalWindow under the `leaseshortwindow` build tag is the short window the
// test-lease-convergence e2e gate uses to watch a renewal cycle open within
// bounded wall-clock, mirroring bgcheckFreshnessWindow's short variant
// (freshness_window_short.go). Expressed in whole hours (like the production
// value) so renewalWindowHours below stays an exact, not rounded, conversion —
// 1 hour is short enough for a bounded e2e wait (the leaseTermMonths floor it
// drives collapses to the 1-month minimum either way, see
// renewal_window.go's renewalWindowHours doc). The production default
// (1440h / 60 days) lives in renewal_window.go; this value is never compiled
// into a shipped binary.
const renewalWindow = "1h"

// renewalWindowHours mirrors renewalWindow's hour count — see renewal_window.go.
const renewalWindowHours = 1
