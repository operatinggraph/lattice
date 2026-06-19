//go:build leaseshortwindow

package leasesigning

// bgcheckFreshnessWindow under the `leaseshortwindow` build tag is the short
// window the test-lease-convergence e2e gate uses to watch a bgcheck lapse in
// bounded wall-clock. ONE compile-time window governs every phase of the e2e
// binary, so it must satisfy two opposing constraints (H1):
//
//   - the steady-state test (drain → hold) must NOT lapse mid-run, else its
//     "missing_bgcheck stays false" assertion flakes. The bgcheck completes
//     early in converge and validUntil = its completedAt + window, so the window
//     must comfortably exceed (worst-case drain deadline + settle hold). The
//     convergence tests cap their drain at 45s and settle at 5s, so 75s leaves a
//     ~25s margin at that ceiling (and a far larger one in practice — converge
//     runs in single-digit seconds in-process).
//   - the eager-freshness test must still WATCH two lapses within bounded waits,
//     so the window cannot be arbitrarily large; each cycle's @at fires one
//     window after the prior converge, and the per-cycle wait budget is the
//     window plus a generous margin (well under the 10m gate timeout).
//
// The production default (5m) lives in freshness_window.go; this value is never
// compiled into a shipped binary.
const bgcheckFreshnessWindow = "75s"
