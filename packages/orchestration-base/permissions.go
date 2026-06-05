package orchestrationbase

import "github.com/asolgan/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// The task lifecycle ops are staff/operator-grantable management ops (a
// leasing manager / operator assigns task-scoped ephemeral grants and manages
// their lifecycle). They follow the same operator-grant idiom rbac-domain uses
// for its management create ops (CreateRole/CreatePermission → operator);
// tightening to additional staff roles later is purely additive.
//
// CreateTask mints a task; ReAssignTask re-points its assignee; CompleteTask
// and CancelTask close it out-of-band (the §10.6 auto-complete path needs no
// permission — it is platform-injected on the commit path, not a submitted op).
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateTask",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreateTask operations.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "ReAssignTask",
			Scope:         "any",
			Note:          "Grants the operator the right to submit ReAssignTask operations.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "CompleteTask",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CompleteTask operations.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "CancelTask",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CancelTask operations.",
			GrantsTo:      []string{"operator"},
		},
	}
}
