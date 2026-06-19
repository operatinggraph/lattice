//go:build !leaseshortwindow

package leasesigning

// bgcheckFreshnessWindow is the validity span the replyOp stamps onto every
// service outcome as `validUntil = completedAt + window` (a Go duration string,
// time.ParseDuration form). The lens applies the freshness policy to the BGCHECK
// family only: a completed bgcheck counts toward convergence solely while
// `validUntil > $now`; once it lapses missing_bgcheck re-opens (a stale
// background check IS a missing background check) and, via the projected
// freshUntil column, Weaver's temporal lane re-opens it eagerly at the lapse
// instant. Payment ignores validUntil (ever-completed), so the value stamped on
// a payment outcome is harmless and unused — the freshness rule lives in the
// lens cypher, per Contract #10 §10.2.
//
// The window is baked into leaseServiceReplyDDLScript at package-init time, so
// it is a compile-time constant: the value is selected by build tag, never a
// runtime mutation. This file carries the production default; the e2e
// convergence gate compiles with `-tags leaseshortwindow` to substitute a short
// window it can watch lapse in bounded wall-clock (freshness_window_short.go).
const bgcheckFreshnessWindow = "5m"
