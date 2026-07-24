package servicedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the package's permission vertices + grants.
//
// The lifecycle ops are operator-driven: the vertical's installer /
// orchestrator submits CreateServiceTemplate (provisioning an offering),
// CreateServiceInstance (starting a run of an offering for an applicant),
// and RecordServiceOutcome (recording the external-call result). No
// end-consumer submits a service-instance create in the demo, so most
// grants go to `operator` only (scope: any) — the same operator-grant idiom
// orchestration-base uses for its task lifecycle ops. Tightening to
// additional staff roles later is purely additive.
//
// RecordServiceOutcome additionally grants `provider` at scope=any (widening
// the EXISTING scope=any row's GrantsTo, never a second row — a permission's
// identity is its (operationType, scope) pair, Contract #8 §8.1, mirroring
// clinic-domain's SetProviderHours widening in wave 1): a bound
// serviceprovider records outcomes only for instances of templates THEY
// provide. Scope stays `any` (there is no scope=self equivalent for a
// non-identity target vertex like a service instance), so the Starlark
// script itself confines a provider-role, non-operator caller to the
// instance-template-serviceprovider-identity chain it is bound to
// (persona-worlds-design.md Fire W0).
//
// CreateServiceProvider / BindServiceProviderIdentity additionally grant
// `frontOfHouse` at scope=any — the staff-run provisioning + bind ceremony
// that establishes a service provider's login, mirroring clinic-domain's
// CreateProvider/BindProviderIdentity grants (the bind's own CreateOnly
// guards on both sides already make it mutually exclusive regardless of who
// submits it).
//
// WireProvidedBy is operator-only: wiring a template to its provider is a
// trusted-tool provisioning ceremony, exactly like service-location's own
// Wire* ops.
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
			Note:          "Grants the operator the right to submit RecordServiceOutcome operations, and a bound service provider the right to record outcomes for THEIR OWN provided templates' instances (the script's standing guard confines a non-operator caller to the instance-template-serviceprovider-identity chain it is bound to).",
			GrantsTo:      []string{"operator", "provider"},
		},
		{
			OperationType: "RetireServiceTemplate",
			Scope:         "any",
			Note:          "Grants the operator the right to submit RetireServiceTemplate operations (§7.3 admin-only cleanup).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "CreateServiceProvider",
			Scope:         "any",
			Note:          "Grants the operator alone the right to submit CreateServiceProvider operations (provisions a template-attached vendor entity), matching clinic-domain CreateProvider — provider-entity creation is an administrative act, and its paired bind (BindServiceProviderIdentity) is likewise operator-only.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "BindServiceProviderIdentity",
			Scope:         "any",
			Note:          "Grants the operator alone the right to bind an existing service provider to its login identity. The bind mints the identity-domain `provider` role; it is operator-only to match its precondition CreateServiceProvider and to keep the role-minting ceremony off the front-desk grant, mirroring clinic-domain BindProviderIdentity.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "WireProvidedBy",
			Scope:         "any",
			Note:          "Grants the operator the right to submit WireProvidedBy operations (wires an existing template to its provider entity).",
			GrantsTo:      []string{"operator"},
		},
	}
}
