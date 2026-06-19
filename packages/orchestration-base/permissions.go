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
	perms := []pkgmgr.PermissionSpec{
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
	perms = append(perms, LoomLifecyclePermissions()...)
	return append(perms, MarkExpiredPermissions()...)
}

// MarkExpiredPermissions returns the grant for the temporal-lane MarkExpired op.
//
// MarkExpired is posted by Weaver's identity:weaver service actor (Contract #10
// §10.4), which is operator-equivalent (holdsRole → operator, exactly like the
// Loom/Bridge service actors), so it is granted to operator at scope:any — the
// same operator-grant idiom the Loom lifecycle ops use. The op is target-less
// (no authContext.target — the directOp posture); auth keys on operationType +
// actor, so the operator grant authorizes Weaver's submit.
func MarkExpiredPermissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "MarkExpired",
			Scope:         "any",
			Note:          "Authorizes Weaver (identity:weaver, operator-equivalent) to submit the temporal-lane MarkExpired freshness op (§10.4).",
			GrantsTo:      []string{"operator"},
		},
	}
}
