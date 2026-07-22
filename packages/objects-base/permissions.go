package objectsbase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// The three lifecycle ops are granted to `operator` only (scope: any) — the
// operator-grant idiom service-domain / orchestration-base use for lifecycle ops.
// Live submitters: the object-store-manager owner-tombstone cascade (TombstoneObject,
// an operator-equivalent service actor), Loupe (admin), and the vertical apps'
// browser-direct AttachObject / DetachObject through the Gateway (today under a
// shared staff credential that holds operator). A consumer scope=self grant on
// AttachObject is deferred to the real-actor write migration
// (real-actor-write-auth-e2e §3.4), where the browser-direct attach submits under
// the applicant's own identity; adding roles/scopes then is purely additive.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "AttachObject",
			Scope:         "any",
			Note:          "Grants the operator the right to submit AttachObject operations.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "DetachObject",
			Scope:         "any",
			Note:          "Grants the operator the right to submit DetachObject operations.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "TombstoneObject",
			Scope:         "any",
			Note:          "Grants the operator the right to submit TombstoneObject operations (GC-internal: the object-store-manager owner-tombstone cascade).",
			GrantsTo:      []string{"operator"},
		},
	}
}
