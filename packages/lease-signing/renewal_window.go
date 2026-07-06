//go:build !leaseshortwindow

package leasesigning

// renewalWindow is how far ahead of a lease's leaseEnd its renewal cycle opens:
// renewalOpensAt = leaseEnd - renewalWindow (DecideLeaseApplication's
// .tenancy stamping, Starlark date-math — never cypher). SetRenewalTerms floors
// termMonths at ceil(renewalWindow in months) so a new term can never expire
// before the NEXT cycle's own renewalOpensAt would have already arrived
// (monthly rollover is explicitly out of scope, design §4.4).
//
// Baked into the DecideLeaseApplication script + the renewal DDL script at
// package-init time (compile-time constant), the same build-tag posture as
// bgcheckFreshnessWindow / bgcheckFreshnessWindow_short. This file carries the
// production default; the e2e convergence gate compiles with
// `-tags leaseshortwindow` to substitute a short window it can watch a cycle
// open within bounded wall-clock (renewal_window_short.go).
const renewalWindow = "1440h" // 60 days

// renewalWindowHours is renewalWindow's hour count as a bare integer — spliced
// into the renewal DDL script as an int literal (renewalWindow itself is
// spliced in as the duration-string operand to time.rfc3339_add; Starlark has
// no duration-string parser, so SetRenewalTerms's termMonths floor needs this
// separate integer form to compute ceil(renewalWindow in months)). The two
// constants MUST stay in lockstep — pinned by
// TestRenewalWindowHours_MatchesRenewalWindow.
const renewalWindowHours = 1440
