package pkgmgr

import (
	"encoding/json"

	"github.com/operatinggraph/lattice/internal/lenscolumns"
)

// lensColumnsSpec converts l's column-deciding fields into lenscolumns.Spec.
// Output and Source are converted by marshalling through their own JSON tags
// rather than copying fields by hand — the same "field shape, not Go type"
// mirror LensSpec.Output/Source already are of their Refractor-side
// counterparts (see LensSpec.Source's doc comment) — so there is exactly one
// mapping between the on-wire shape and lenscolumns.Spec, shared by every
// caller instead of one hand-copied conversion per call site. Marshalling a
// plain data struct of strings, slices and maps cannot fail; an error here
// would mean l's own type stopped being that, which the zero value surfaces
// as a Spec lenscolumns.Projected reports unreadable rather than a panic.
func lensColumnsSpec(l LensSpec) lenscolumns.Spec {
	s := lenscolumns.Spec{
		ProjectionKind: l.ProjectionKind,
		CypherRule:     l.Spec,
		CypherBranches: l.SpecBranches,
	}
	if l.Output != nil {
		if b, err := json.Marshal(l.Output); err == nil {
			_ = json.Unmarshal(b, &s.Output)
		}
	}
	if l.Source != nil {
		if b, err := json.Marshal(l.Source); err == nil {
			_ = json.Unmarshal(b, &s.Source)
		}
	}
	return s
}
