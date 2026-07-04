package pkgmgr

import (
	"fmt"

	"github.com/asolgan/lattice/internal/guardgrammar"
)

// validateEffects runs the Contract #10 §10.8 Planner-extension install-time
// validations on every DDL's optional Effects map, fail-closed and pure (no
// I/O): each key must name one of the DDL's own PermittedCommands (an effect
// for an operationType the DDL does not handle can never be entailed by any
// commit), and each declared guard must parse under the shared §10.5 grammar
// (internal/guardgrammar — the same grammar a Loom step Guard uses). A
// malformed guard rejects the whole install, same doctrine as every other
// package-data validator in this file.
func (def Definition) validateEffects() error {
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
