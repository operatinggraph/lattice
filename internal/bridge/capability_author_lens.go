package bridge

import (
	"encoding/json"
	"fmt"

	"github.com/operatinggraph/lattice/internal/lenscolumns"
)

// InstalledLensResolver answers "which keys does a row of the lens this
// lensRef names carry" for the capability validator the composition root
// injects. Its method set matches the installer's own resolver interface, so a
// value of this type satisfies that one without internal/bridge importing the
// installer (the bridge stays a substrate-only leaf; internal/lenscolumns is a
// leaf of encoding/json and strings).
//
// found is true only for a live lens. err wrapping lenscolumns.ErrUnreadable is
// a lens whose columns are not derivable — a verdict, not a failure.
type InstalledLensResolver interface {
	ResolveLensColumns(lensRef string) (cols lenscolumns.Result, found bool, err error)
}

// LensReturnColumns is the static openCypher parse a PLAIN lens's row columns
// come from: the RETURN items' effective names, in declaration order. Injected
// at the composition root (the same seam the artifact validator itself is
// injected through), because the bridge does not link the rule engine on its
// own account.
//
// nil makes every plain lens unreadable rather than column-less — "not
// derivable" and "no columns" are different verdicts and only the first is safe
// to fail closed on.
type LensReturnColumns func(rule string) ([]string, error)

// catalogLensResolver answers a lensRef from the capabilityAuthorContext
// catalog the adapter already reads — NOT from Core KV, which the bridge's own
// NKey is denied (internal/natsperm/matrix.go denies the bridge
// $JS.API.DIRECT.GET.KV_core-kv, and that denial closed a decrypt-RPC side
// channel; it is not to be widened for a validation read). The catalog lens
// projects `m.class AS class` and `m.spec.data AS spec` for every meta-vertex,
// which is exactly the class test and the body lenscolumns.Spec decodes.
//
// The catalog is a read model, so "live" here means "the projection carries a
// row": the engine filters tombstoned documents out of every projection
// (internal/refractor/ruleengine/full's executor), and this lens projects no
// isDeleted column of its own for a resolver to re-test. A row that outlives
// its tombstoned vertex in a lens without diff retraction is therefore the one
// residual this answer carries, and it is bounded by where the answer is used:
// this resolver feeds the RECORD-time verdict, while the apply-time bound is
// the installer's own resolver, which reads the document and its isDeleted flag
// directly and refuses a tombstoned lens outright.
type catalogLensResolver struct {
	// specs maps a lens meta-vertex's bare NanoID to the raw `spec` aspect
	// body the catalog row carries. Only rows whose class is meta.lens are in
	// it, so membership IS the class test: a weaverTarget, loomPattern, DDL or
	// op-meta id is absent and answers found=false.
	specs map[string]json.RawMessage

	// returnColumns is the plain-lens parse. nil leaves every plain lens
	// unreadable.
	returnColumns LensReturnColumns
}

var _ InstalledLensResolver = catalogLensResolver{}

// ResolveLensColumns implements InstalledLensResolver over the catalog rows.
//
// A ref naming no meta.lens row answers found=false with no error — the same
// answer the installer gives for an absent or wrong-class id, so the two
// verdicts agree. A row whose spec will not decode, or a plain lens with no
// parser, answers found=true with an ErrUnreadable-wrapped reason: the lens
// exists and what cannot be established is its column set.
func (r catalogLensResolver) ResolveLensColumns(lensRef string) (lenscolumns.Result, bool, error) {
	raw, ok := r.specs[lensRef]
	if !ok || len(raw) == 0 {
		return lenscolumns.Result{}, false, nil
	}
	var spec lenscolumns.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return lenscolumns.Result{}, true, fmt.Errorf("%w: the catalog row for %s carries a spec that does not decode: %v",
			lenscolumns.ErrUnreadable, lensRef, err)
	}
	var columns func(string) ([]string, error)
	if r.returnColumns != nil {
		columns = r.returnColumns
	}
	cols, err := lenscolumns.Projected(spec, columns)
	return cols, true, err
}

// lensResolver builds the resolver over this catalog read. It costs no
// additional read: the rows are the ones the caller already listed for the
// prompt and the lens index.
func (r catalogRead) lensResolver(returnColumns LensReturnColumns) catalogLensResolver {
	return catalogLensResolver{specs: r.lensSpecs, returnColumns: returnColumns}
}
