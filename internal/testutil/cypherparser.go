package testutil

import (
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// FullCypherParser adapts ruleengine/full to pkgmgr.CypherParser. Living here
// (not in internal/pkgmgr) avoids the import cycle pkgmgr.CypherParser's doc
// explains — full's own test binary transitively imports pkgmgr, so pkgmgr
// itself cannot import full directly. testutil sits below neither: nothing in
// full's import graph reaches it.
type FullCypherParser struct{}

func (FullCypherParser) Parse(ruleBody string) (pkgmgr.SpecLabels, error) {
	facts, err := full.SpecLabels(ruleBody)
	if err != nil {
		return pkgmgr.SpecLabels{}, err
	}
	return pkgmgr.SpecLabels{
		Referenced: facts.Referenced,
		Exhaustive: facts.Exhaustive,
		Expansion:  facts.Expansion,
	}, nil
}

var _ pkgmgr.CypherParser = FullCypherParser{}

// NewInstaller builds the installer a fixture should use to install a real
// package, wired the way the production entry points wire theirs — in
// particular with SpecParser set, so a fixture installing a shipped package
// exercises the install-time narrowed-filter budget gate
// (dynamic-type-taxonomy-design.md §10.2) rather than skipping it.
//
// That is the point of it existing: the gate refuses a lens whose abstract
// labels cannot fit the label cap, and the only way to know it does not refuse a
// lens that WAS fine is for the suite's own package installs to run it. A
// fixture calling pkgmgr.NewInstaller directly proves nothing about the gate.
//
// RoleIDs are left to the caller — a fixture that installs a package with
// role-scoped grants sets StandardRoleIDs (or its own map) on the result.
func NewInstaller(conn *substrate.Conn, adminActor string) *pkgmgr.Installer {
	inst := pkgmgr.NewInstaller(conn, adminActor)
	inst.SpecParser = FullCypherParser{}
	return inst
}
