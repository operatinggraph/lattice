package servicedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// The three lifecycle ops are operator-driven: the vertical's installer /
// orchestrator submits CreateServiceTemplate (provisioning an offering),
// CreateServiceInstance (starting a run of an offering for an applicant),
// and RecordServiceOutcome (recording the external-call result). No
// end-consumer submits a service-instance create in the demo, so the grants
// go to `operator` only (scope: any) — the same operator-grant idiom
// orchestration-base uses for its task lifecycle ops. Tightening to
// additional staff roles later is purely additive.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateServiceTemplate",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreateServiceTemplate operations.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "CreateServiceInstance",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreateServiceInstance operations.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "RecordServiceOutcome",
			Scope:         "any",
			Note:          "Grants the operator the right to submit RecordServiceOutcome operations.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "RetireServiceTemplate",
			Scope:         "any",
			Note:          "Grants the operator the right to submit RetireServiceTemplate operations (§7.3 admin-only cleanup).",
			GrantsTo:      []string{"operator"},
		},
	}
}
