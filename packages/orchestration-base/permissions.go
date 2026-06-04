package orchestrationbase

import "github.com/asolgan/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// CreateTask is a staff/operator-grantable management op (a leasing
// manager / operator assigns task-scoped ephemeral grants). It follows the
// same operator-grant idiom rbac-domain uses for its management create ops
// (CreateRole/CreatePermission → operator); tightening to additional staff
// roles later is purely additive.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateTask",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreateTask operations.",
			GrantsTo:      []string{"operator"},
		},
	}
}
