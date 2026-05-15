package adapter

import "context"

// Adapter is the common write interface implemented by all target store adapters.
//
// keys holds the composite key fields and values (from EvalResult.Keys).
// row holds all projected non-key column values (from EvalResult.Row).
type Adapter interface {
	Upsert(ctx context.Context, keys map[string]any, row map[string]any) error
	Delete(ctx context.Context, keys map[string]any) error
	// Probe performs a lightweight liveness check against the target store.
	// Returns nil if the store is reachable and the target bucket/table exists;
	// returns an error (classified by failure.Classify) otherwise.
	// Used by the pipeline's infrastructure-pause probe loop (FR17).
	Probe(ctx context.Context) error
	Close() error
}

// Truncater is an optional interface for adapters that support clearing all
// rows/entries from the target store. Adapters may implement this to support
// the truncate-before-rebuild operation (FR29).
// Truncate is called by pipeline.Pipeline.Rebuild when truncate=true is
// requested via the "rebuild" control operation.
type Truncater interface {
	Truncate(ctx context.Context) error
}
