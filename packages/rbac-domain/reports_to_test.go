// Story 4.7 cleanup — relocated reportsTo Starlark test.
//
// The pre-4.7 RBAC bootstrap surface included a `reportsTo` DDL
// (Story 3.6) handling AssignReportingChain / RemoveReportingChain.
// Story 4.7 retired the bootstrap rbac DDLs in favour of the
// rbac-domain Capability Package, which is intentionally scoped to
// roles + permissions + grants — it does NOT include reportsTo.
//
// `reportsTo` therefore has no current home. The DDL script itself
// is deleted alongside internal/bootstrap/role_mgmt_ddl.go in this
// cleanup. The TestStarlark_ReportsTo_AssignReportingChain coverage
// is preserved here as a skip-with-TODO so the regression is visible
// in the test suite once reportsTo lands in its own package
// (org-hierarchy or similar).
//
// TODO: when reportsTo ships as a Capability Package (post-Phase-1
// org-hierarchy work), relocate this test into that package, restore
// the assertions, and delete this file.
package rbacdomain_test

import "testing"

// TestStarlark_ReportsTo_AssignReportingChain — DEFERRED.
//
// Originally (Story 3.6) this test asserted that the reportsTo DDL
// script:
//   - parses + compiles
//   - returns Contract #3 shape (mutations + events)
//   - first mutation key starts with "lnk.identity."
//   - first mutation key contains ".reportsTo."
//   - first event class is "ReportingChainAssigned"
//
// The reportsTo DDL was removed from the kernel in Story 4.7 and not
// included in the rbac-domain package. When reportsTo ships in its
// own Capability Package, port these assertions into that package's
// test suite.
func TestStarlark_ReportsTo_AssignReportingChain(t *testing.T) {
	t.Skip("reportsTo retired from kernel in Story 4.7; awaiting org-hierarchy Capability Package — TODO: relocate this test.")
}
