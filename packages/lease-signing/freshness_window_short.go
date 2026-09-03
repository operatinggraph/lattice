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
//     steady-state test caps its drain at 15s and settles for 5s, so 25s clears
//     that 20s ceiling with the same 5s margin the old 40s/35s pairing held (and
//     far more in practice — converge runs in ~1s in-process, so the steady-state
//     test completes in well under 10s). Every other test in the package also
//     finishes in under 10s wall-clock, so a 25s window cannot lapse during any
//     non-eager test.
//   - the eager-freshness test must still WATCH two lapses within bounded waits,
//     so the window cannot be arbitrarily large; each cycle's @at fires one
//     window after the prior converge, and the per-cycle wait budget is the
//     window plus a generous margin (well under the 10m gate timeout). It is the
//     dominant cost of the gate (~2*window of pure lapse), so the window is kept
//     to the steady-state floor, not above it.
//
// The production default (720h) lives in freshness_window.go; this value is never
// compiled into a shipped binary.
const bgcheckFreshnessWindow = "25s"
