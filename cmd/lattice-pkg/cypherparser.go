package main

import (
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// fullCypherParser adapts ruleengine/full to pkgmgr.CypherParser. Living here
// (not in internal/pkgmgr) avoids the import cycle pkgmgr.CypherParser's doc
// explains — full's own test binary transitively imports pkgmgr, so pkgmgr
// itself cannot import full directly. The CLI is an independent leaf binary, so
// it can wire the two together.
type fullCypherParser struct{}

func (fullCypherParser) Parse(ruleBody string) (pkgmgr.SpecLabels, error) {
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

var _ pkgmgr.CypherParser = fullCypherParser{}

// newInstaller is the CLI's only installer constructor, so every subcommand
// gets the same wiring — including the spec parser the install-time
// narrowed-filter budget gate needs (pkgmgr.Installer.SpecParser,
// dynamic-type-taxonomy-design.md §10.2). Calling pkgmgr.NewInstaller directly
// from a subcommand would silently drop that gate for whichever path forgot,
// which is exactly the kind of per-call-site divergence a single constructor
// removes.
func newInstaller(conn *substrate.Conn, adminActor string) *pkgmgr.Installer {
	inst := pkgmgr.NewInstaller(conn, adminActor)
	inst.SpecParser = fullCypherParser{}
	return inst
}
