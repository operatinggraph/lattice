package adjacency

import "context"

// readObserverKey is the unexported context key WithReadObserver stores its
// callback under. The type is unexported so no other package can forge or read
// the same context entry.
type readObserverKey struct{}

// ReadObservation describes one adjacency read this package served: which node
// it was for, at what scope, and what the node's own shape turned out to be.
type ReadObservation struct {
	// NodeID is the adjacency node the read was for.
	NodeID string
	// Relations is the set of relation names the read was scoped to, or nil
	// for an unscoped read of the whole node. It is the caller's own set and
	// must not be retained or mutated by an observer.
	Relations map[string]struct{}
	// Marked reports whether the node carries the overflow latch, and so
	// whether its edges came from Core KV's link keyspace rather than from an
	// adjacency document.
	Marked bool
	// Whole reports whether the answer covers the node's whole edge list. It
	// is false only for a scoped read that a marked node served, since an
	// unmarked node answers whole however narrow the request was.
	Whole bool
}

// WithReadObserver returns a context that makes every Neighbors and
// NeighborsScoped call under it invoke fn once, as soon as the node's own
// state is known and before the edges are assembled. It exists so a test can
// pin WHICH read a caller took — the hop-scoped read of an overflow-marked
// hub against the whole-node read — rather than inferring it from the shape of
// a footprint, which several different read paths can produce.
//
// Production code never installs one; the reader below is a single
// ctx.Value lookup per read, which is all an un-observed read pays.
func WithReadObserver(ctx context.Context, fn func(ReadObservation)) context.Context {
	return context.WithValue(ctx, readObserverKey{}, fn)
}

// readObserver reads back the callback WithReadObserver installed on ctx, or
// nil when none was installed.
func readObserver(ctx context.Context) func(ReadObservation) {
	fn, _ := ctx.Value(readObserverKey{}).(func(ReadObservation))
	return fn
}

// observeRead reports one read to ctx's observer when it carries one.
func observeRead(ctx context.Context, nodeID string, rels map[string]struct{}, marked, whole bool) {
	if fn := readObserver(ctx); fn != nil {
		fn(ReadObservation{NodeID: nodeID, Relations: rels, Marked: marked, Whole: whole})
	}
}
