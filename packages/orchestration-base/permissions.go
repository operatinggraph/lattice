package orchestrationbase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// The task lifecycle ops are staff/operator-grantable management ops (a
// leasing manager / operator assigns task-scoped ephemeral grants and manages
// their lifecycle). They follow the same operator-grant idiom rbac-domain uses
// for its management create ops (CreateRole/CreatePermission → operator);
// tightening to additional staff roles later is purely additive.
//
// CreateTask mints a task (assignedTo an identity or queuedFor a role,
// FR28); ClaimTask lets a role-holder claim a queued task; ReAssignTask
// re-points its assignee; CompleteTask and CancelTask close it out-of-band
// (the §10.6 auto-complete path needs no permission — it is
// platform-injected on the commit path, not a submitted op).
//
// ClaimTask is granted to `operator` as the platform-wide baseline (operators
// may always claim any queued task) and to `frontOfHouse` + `backOfHouse`, the
// two shipped staff roles whose queued work is the whole point of the FR28
// role-queue — back-of-house is the maintenance tech who claims a queued work
// order (facet-staff-worlds-design.md §6 F5). Any package establishing a FURTHER role-queue (e.g. a
// "leasing-team" role) must likewise grant that role ClaimTask — the Epic-12
// cap.roles decomposition lets each package contribute its own role grants,
// since orchestration-base cannot know a downstream package's role names.
//
// A platform-wide ClaimTask grant is not a platform-wide claim capability: the
// DDL script resolves the task's own queuedFor link and rejects
// NotAuthorizedToClaim unless the submitting actor holds THAT role. The
// permission is the outer gate; holding the queued role is the real
// confinement. So frontOfHouse can claim front-desk work and nothing else.
func Permissions() []pkgmgr.PermissionSpec {
	perms := []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateTask",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreateTask operations.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "ClaimTask",
			Scope:         "any",
			Note:          "Grants the operator and both staff roles the right to submit ClaimTask operations (FR28 platform-wide baseline; the script further requires the claimant to hold the task's own queued role, so this grant claims nothing on its own).",
			GrantsTo:      []string{"operator", "frontOfHouse", "backOfHouse"},
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
		{
			OperationType: "SetAvailability",
			Scope:         "any",
			Note:          "Grants the operator the right to submit SetAvailability operations (Fire 2 routing input).",
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
