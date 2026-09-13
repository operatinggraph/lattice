package pkgmgr

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/operatinggraph/lattice/internal/lenscolumns"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// MetaLensClass is the Contract #1 root class every installed lens
// meta-vertex carries. Both holders of the weaver-target binding rule — the
// installer's live preflight and the artifact validator's resolver — decide
// "is this id a lens" on exactly this string, so it is spelled once.
const MetaLensClass = "meta.lens"

// CoreKVLensResolver is the one live InstalledLensResolver: it answers a
// lensRef from the installed kernel, reading the lens meta-vertex's root (for
// its class and tombstone state) and its `spec` aspect (for the declaration
// lenscolumns reads the row columns from) in ONE batched round trip.
//
// It carries its own context because InstalledLensResolver's method does not:
// the interface is consumed inside ValidateCapabilityArtifact, which is pure
// and takes no context of its own, so the caller that builds the resolver —
// the one that knows the request's deadline — is the one that binds it.
//
// Cost: one KVGetMulti of at most 2 keys per resolved ref.
type CoreKVLensResolver struct {
	ctx    context.Context
	conn   *substrate.Conn
	parser CypherParser
}

// NewCoreKVLensResolver builds a resolver reading Core KV through conn, with
// ctx bounding every read it makes.
//
// parser supplies the static openCypher parse a PLAIN lens's row columns come
// from (its RETURN items' names). A nil parser leaves every plain lens
// UNREADABLE — lenscolumns.ErrUnreadable naming the absent parser — never
// column-less: "not derivable" and "no columns" are different verdicts, and
// only the first is safe to fail closed on.
func NewCoreKVLensResolver(ctx context.Context, conn *substrate.Conn, parser CypherParser) *CoreKVLensResolver {
	return &CoreKVLensResolver{ctx: ctx, conn: conn, parser: parser}
}

var _ InstalledLensResolver = (*CoreKVLensResolver)(nil)

// ResolveLensColumns implements InstalledLensResolver against the installed
// kernel.
//
// found is true only for a root that is a live `meta.lens`: a ref that is not
// NanoID-shaped names no installed meta-vertex at all (answered without a
// read), and an id whose root is absent, tombstoned, or of any other class — a
// weaver target, a Loom pattern, a DDL, an op-meta — answers false. That is
// the same class test the installer's live preflight applies, so the two
// holders never disagree about what a lensRef binds.
//
// A live `meta.lens` whose `spec` aspect is absent or will not decode answers
// found=true with an ErrUnreadable-wrapped error: the lens exists, and what
// cannot be established is its column set. A FAILED read is neither — it
// returns a plain error, which every caller treats as "no verdict" rather than
// as an empty kernel.
func (r *CoreKVLensResolver) ResolveLensColumns(lensRef string) (lenscolumns.Result, bool, error) {
	if !substrate.IsValidNanoID(lensRef) {
		return lenscolumns.Result{}, false, nil
	}
	rootKey := metaVertexPrefix + lensRef
	specKey := rootKey + ".spec"
	entries, err := r.conn.KVGetMulti(r.ctx, CoreBucket, []string{rootKey, specKey})
	if err != nil {
		return lenscolumns.Result{}, false, fmt.Errorf("pkgmgr: read lens meta %s: %w", rootKey, err)
	}

	root, ok := entries[rootKey]
	if !ok || root == nil {
		return lenscolumns.Result{}, false, nil
	}
	var rootEnv substrate.DocumentEnvelope
	if uerr := json.Unmarshal(root.Value, &rootEnv); uerr != nil {
		// A root nothing can characterize is not a lens this binding may rely
		// on. Reported as "not a lens" rather than as a read failure: the read
		// succeeded, and what it returned decides the class test.
		return lenscolumns.Result{}, false, nil
	}
	if rootEnv.Class != MetaLensClass || rootEnv.IsDeleted {
		return lenscolumns.Result{}, false, nil
	}

	specEntry, ok := entries[specKey]
	if !ok || specEntry == nil {
		return lenscolumns.Result{}, true, fmt.Errorf("%w: %s carries no spec aspect, so nothing declares what its rows hold",
			lenscolumns.ErrUnreadable, rootKey)
	}
	var specEnv substrate.AspectEnvelope
	if uerr := json.Unmarshal(specEntry.Value, &specEnv); uerr != nil {
		return lenscolumns.Result{}, true, fmt.Errorf("%w: %s does not decode as an aspect envelope: %v",
			lenscolumns.ErrUnreadable, specKey, uerr)
	}
	if specEnv.IsDeleted {
		return lenscolumns.Result{}, true, fmt.Errorf("%w: %s is tombstoned, so nothing declares what its rows hold",
			lenscolumns.ErrUnreadable, specKey)
	}
	spec, err := lensSpecFromAspectData(specEnv.Data)
	if err != nil {
		return lenscolumns.Result{}, true, fmt.Errorf("%w: %s: %v", lenscolumns.ErrUnreadable, specKey, err)
	}
	cols, err := lenscolumns.Projected(spec, lensColumnsFromParser(r.parser))
	return cols, true, err
}

// lensSpecFromAspectData converts a stored lens `spec` aspect body (build.go's
// lensSpecBody, decoded by the envelope into a map) into lenscolumns.Spec.
//
// The conversion goes back through JSON rather than reading map keys by hand:
// lenscolumns.Spec's tags ARE the stored body's field names, so re-marshalling
// the map and decoding it is the one mapping — a hand-read would be a second
// spelling of the same wire shape, free to drift from the one build.go writes.
func lensSpecFromAspectData(data map[string]any) (lenscolumns.Spec, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return lenscolumns.Spec{}, fmt.Errorf("re-encode spec body: %w", err)
	}
	var spec lenscolumns.Spec
	if err := json.Unmarshal(b, &spec); err != nil {
		return lenscolumns.Spec{}, fmt.Errorf("decode spec body: %w", err)
	}
	return spec, nil
}

// lensColumnsFromParser adapts a CypherParser to the returnColumns function
// lenscolumns.Projected reads a plain lens's row keys through — one spelling
// of "the RETURN names come from the SAME parse as the error", shared by the
// live resolver, the installer's preflight and the artifact validator.
//
// A nil parser yields a nil function, which is exactly what makes a plain lens
// unreadable rather than column-less (lenscolumns.Projected's contract).
func lensColumnsFromParser(p CypherParser) func(string) ([]string, error) {
	if p == nil {
		return nil
	}
	return func(rule string) ([]string, error) {
		labels, err := p.Parse(rule)
		if err != nil {
			return nil, err
		}
		return labels.Columns, nil
	}
}
