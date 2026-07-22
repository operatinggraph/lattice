package capability

import (
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// fullCypherParser adapts ruleengine/full.Engine to pkgmgr.CypherParser.
// Living here (not in internal/pkgmgr) avoids the import cycle
// pkgmgr.CypherParser's doc explains — full's own test binary transitively
// imports pkgmgr, so pkgmgr itself cannot import full directly. The CLI is
// an independent leaf package, so it can wire the two together.
type fullCypherParser struct{}

func (fullCypherParser) Parse(ruleBody string) error {
	_, err := full.New().Parse(ruleBody)
	return err
}

var _ pkgmgr.CypherParser = fullCypherParser{}
