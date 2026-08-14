package rbacdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the 9 permission vertices + their grants. Every
// operation is granted to the `operator` role. The role canonical name
// `operator` is resolved by cmd/lattice-pkg to the seeded NanoID from
// lattice.bootstrap.json.
//
// The rbac DDL's tenth command, `UpdatePermission`, is deliberately
// ungranted: it is the one operation that can rewrite an existing permission
// vertex's body, so granting it would make a permission's operationType/scope
// — and the `data.origin` provenance stamp that Contract #6 §6.1 keys the
// reserved-operation refusal on — re-targetable after authoring. Nothing in
// the repo invokes it. No granted operation can therefore rewrite a permission
// vertex's body: it is authored by `CreatePermission` or by the installer, and
// narrowed only by `TombstonePermission` / `RevokePermission`. An ungranted op
// is denied at step 3 by absence; `scripts/lint-package-standard.go`'s
// grant-authoring gate default-denies any package that grants it back.
//
// That closes the operation channel, not every channel — `UpgradePackage`'s
// bootstrap DDL still accepts client-supplied mutations against any `vtx.*`
// key, so Contract #6 §6.1's write-once rule is not yet whole; that gap is
// filed separately and is not rbac-domain's to close.
func Permissions() []pkgmgr.PermissionSpec {
	mk := func(op string) pkgmgr.PermissionSpec {
		return pkgmgr.PermissionSpec{
			OperationType: op,
			Scope:         "any",
			Note:          "Grants the operator the right to submit " + op + " operations.",
			GrantsTo:      []string{"operator"},
		}
	}
	return []pkgmgr.PermissionSpec{
		mk("CreateRole"),
		mk("UpdateRole"),
		mk("TombstoneRole"),
		mk("CreatePermission"),
		mk("TombstonePermission"),
		mk("AssignRole"),
		mk("RevokeRole"),
		mk("GrantPermission"),
		mk("RevokePermission"),
	}
}
