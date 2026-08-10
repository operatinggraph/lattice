package substrate

import "context"

// kvDrainRoundHookKey is the unexported context key WithKVDrainRoundHook
// stores its callback under. The type is unexported so no other package can
// forge or read the same context entry.
type kvDrainRoundHookKey struct{}

// WithKVDrainRoundHook returns a context that makes the multi-subject fallback
// drain invoke fn after each completed fetch round, with the zero-based round
// index. It exists so a test can commit a mutation BETWEEN rounds of one
// drain — the interleaving that decides whether a key hard-deleted while the
// drain is in flight is retracted from the accumulating result or returned as
// live. That window is unreachable from outside: a single-round drain always
// observes a tombstone as its subject's last message, so only a mutation
// landing after round N and before round N+1 exercises the retraction.
//
// Production code never calls this; it is a test-only seam, mirroring
// full.WithFootprintCapturedHook.
func WithKVDrainRoundHook(ctx context.Context, fn func(round int)) context.Context {
	return context.WithValue(ctx, kvDrainRoundHookKey{}, fn)
}

// kvDrainRoundHook reads back the callback WithKVDrainRoundHook installed,
// returning nil when none was — the production case.
func kvDrainRoundHook(ctx context.Context) func(round int) {
	fn, _ := ctx.Value(kvDrainRoundHookKey{}).(func(round int))
	return fn
}
