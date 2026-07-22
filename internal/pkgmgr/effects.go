package pkgmgr

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
)

// validateEffects runs the Contract #10 §10.8 Planner-extension install-time
// validations on every DDL's optional Effects map, fail-closed and pure (no
// I/O): each key must name one of the DDL's own PermittedCommands (an effect
// for an operationType the DDL does not handle can never be entailed by any
// commit), each declared guard must parse under the shared §10.5 grammar
// (internal/guardgrammar — the same grammar a Loom step Guard uses), and the
// operationType must have a matching OpMetaSpec in the same Definition — the
// op-meta vertex is where buildInstallBatch materializes the `.effects`
// aspect (the Weaver planner's runtime catalog, Fire 6), so an Effects entry
// with nowhere to land would silently never reach any goal-regression search.
// A malformed guard or an unresolvable op-meta rejects the whole install, same
// doctrine as every other package-data validator in this file.
func (def Definition) validateEffects() error {
	opMetas := make(map[string]bool, len(def.OpMetas))
	for _, o := range def.OpMetas {
		opMetas[o.OperationType] = true
	}
	for dIdx, d := range def.DDLs {
		if len(d.Effects) == 0 {
			continue
		}
		permitted := make(map[string]bool, len(d.PermittedCommands))
		for _, c := range d.PermittedCommands {
			permitted[c] = true
		}
		for op, guards := range d.Effects {
			if !permitted[op] {
				return fmt.Errorf("pkgmgr: DDL[%d] %q: Effects declares operationType %q, which is not in PermittedCommands (%v)",
					dIdx, d.CanonicalName, op, d.PermittedCommands)
			}
			if !opMetas[op] {
				return fmt.Errorf("pkgmgr: DDL[%d] %q: Effects declares operationType %q, which has no matching OpMetaSpec — "+
					"add OpMetaSpec{OperationType: %q} to OpMetas so the effect has an op-meta vertex to materialize onto",
					dIdx, d.CanonicalName, op, op)
			}
			for gIdx, raw := range guards {
				if _, err := guardgrammar.Parse(raw); err != nil {
					return fmt.Errorf("pkgmgr: DDL[%d] %q: Effects[%q][%d]: %w",
						dIdx, d.CanonicalName, op, gIdx, err)
				}
			}
		}
	}
	return nil
}
