package semanticcontracts

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares the op-meta vertices that make ops forOperation-resolvable.
//
//   - CreateClause, SupersedeClause — declared for uniform discoverability
//     (functionally optional; neither is ever an assignTask target itself).
//   - InspectPremises — REQUIRED: the assignTask operation the §10.8
//     playbook's missing_inspection gap binds; the Weaver Actuator resolves
//     forOperation to its op-meta when it creates the remediation Task
//     (the SignLease precedent). Its absence would break the gap.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{OperationType: "CreateClause"},
		{OperationType: "InspectPremises"},
		{OperationType: "SupersedeClause"},
	}
}

// Permissions returns the package's permission vertices + grants. The
// trusted-tool app submits CreateClause when a landlord/operator installs a
// bespoke provision on a signed lease — the same operator-grant idiom
// loftspace-ledger's CreateAccount uses. SupersedeClause (Fire V4
// self-amendment) is the same operator-grant idiom.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateClause",
			Scope:         "any",
			Note:          "Grants the operator the right to submit CreateClause (installs a semantic-contract clause governing a lease and charging a ledger account, or a judgment clause assigning an inspector).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "InspectPremises",
			Scope:         "any",
			Note:          "Grants the operator the right to submit InspectPremises; the assigned inspector performs it via the ephemeral task grant (§10.7) — same operator model as SignLease.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "SupersedeClause",
			Scope:         "any",
			Note:          "Grants the operator the right to submit SupersedeClause (Fire V4 self-amendment: replaces a clause with a new one, tombstoning the amended clause and linking the replacement).",
			GrantsTo:      []string{"operator"},
		},
	}
}
