package full

import "context"

// footprintHookKey is the unexported context key WithFootprintCapturedHook
// stores its callback under. The type is unexported so no other package can
// forge or read the same context entry.
type footprintHookKey struct{}

// WithFootprintCapturedHook returns a context that makes ExecuteWith invoke fn
// exactly once, immediately after it finishes building this evaluation's
// read-surface footprint and before ExecuteWith returns. It exists so a test
// can commit a mutation to a footprinted key or adjacency entry at exactly
// that instant and observe the footprint-validation seam (executeFullForActor
// in package pipeline) detect it on its post-evaluation re-read — the
// scripted-interleave vector refractor-evaluation-consistency-design.md §9
// describes for the capabilityEphemeral role-queue tear. Production code
// never calls this; it is a test-only seam.
func WithFootprintCapturedHook(ctx context.Context, fn func()) context.Context {
	return context.WithValue(ctx, footprintHookKey{}, fn)
}

// footprintCapturedHook reads back the callback WithFootprintCapturedHook
// installed on ctx, or nil when none was installed.
func footprintCapturedHook(ctx context.Context) func() {
	fn, _ := ctx.Value(footprintHookKey{}).(func())
	return fn
}
