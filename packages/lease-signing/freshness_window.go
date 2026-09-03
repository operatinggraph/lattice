//go:build !leaseshortwindow

package leasesigning

// bgcheckFreshnessWindow is the validity span the replyOp stamps onto every
// service outcome as `validUntil = completedAt + window` (a Go duration string,
// time.ParseDuration form). The lens applies the freshness policy to the BGCHECK
// family only: a completed bgcheck counts toward convergence solely while no
// timer has recorded a lapse reaching its `validUntil`; once one has,
// missing_bgcheck re-opens (a stale background check IS a missing background
// check). The backgroundCheckFreshness lens projects that validUntil as the
// instance's own freshUntil, so Weaver's temporal lane arms the @at on the
// check itself and its fired MarkExpired records the lapse there, eagerly, at
// the lapse instant. Payment ignores validUntil (ever-completed), so the value stamped on
// a payment outcome is harmless and unused — the freshness rule lives in the
// lens cypher, per Contract #10 §10.2.
//
// The window is baked into leaseServiceReplyDDLScript at package-init time, so
// it is a compile-time constant: the value is selected by build tag, never a
// runtime mutation. This file carries the production default: the span a
// completed check stays valid, set by the vendor's own validity policy for the
// screening — a vendor-validity policy, not a demo cadence. A window of
// minutes turns the missing_bgcheck convergence loop into a metronome: every
// lapse re-opens missing_bgcheck, Weaver's triggerLoom mints a fresh service
// instance to close it, and that instance's own reply stamps a validUntil that
// lapses one window later, repeating for as long as the application stays
// open. The e2e convergence gate compiles with `-tags leaseshortwindow` to
// substitute a short window it can watch lapse in bounded wall-clock
// (freshness_window_short.go).
const bgcheckFreshnessWindow = "720h"
