package substrate

import "context"

// kvResolveThenGetHookKey is the unexported context key WithKVResolveThenGetHook
// stores its callback under. The type is unexported so no other package can
// forge or read the same context entry.
type kvResolveThenGetHookKey struct{}

// WithKVResolveThenGetHook returns a context that makes the NoSnapshot
// past-the-cap read report, once per call, how it was served: how many
// stream-info requests it made to RESOLVE its filters (one per distinct
// wildcard) and how many multi-get REQUESTS it issued for the resolved keys.
//
// It exists because the shape of that answer is invisible in the answer
// itself: a complete, correct map comes back whether it was assembled from
// two atomic requests or from a per-key drain, so only the request profile
// distinguishes the strategy this read exists to take from the one it
// replaced.
//
// Production code never calls this; it is a test-only seam, mirroring
// WithKVDrainRoundHook.
func WithKVResolveThenGetHook(ctx context.Context, fn func(infos, requests int)) context.Context {
	return context.WithValue(ctx, kvResolveThenGetHookKey{}, fn)
}

// kvResolveThenGetHook reads back the callback WithKVResolveThenGetHook installed,
// returning nil when none was — the production case.
func kvResolveThenGetHook(ctx context.Context) func(infos, requests int) {
	fn, _ := ctx.Value(kvResolveThenGetHookKey{}).(func(infos, requests int))
	return fn
}

// kvResolvedKeysHookKey is the unexported context key WithKVResolvedKeysHook
// stores its callback under.
type kvResolvedKeysHookKey struct{}

// WithKVResolvedKeysHook returns a context that makes the NoSnapshot
// past-the-cap read invoke fn once, with the exact keys its filters resolved
// to, AFTER the resolution and BEFORE any of those keys is read.
//
// It exists so a test can commit a mutation in that window — the interleaving
// that decides whether a key hard-deleted while the read is in flight comes
// back as live. The window is unreachable from outside: resolution and reading
// are one call, and a delete landing either side of the pair is an ordinary
// before-or-after read that proves nothing about the middle.
//
// Production code never calls this; it is a test-only seam, mirroring
// WithKVDrainRoundHook.
func WithKVResolvedKeysHook(ctx context.Context, fn func(keys []string)) context.Context {
	return context.WithValue(ctx, kvResolvedKeysHookKey{}, fn)
}

// kvResolvedKeysHook reads back the callback WithKVResolvedKeysHook installed,
// returning nil when none was — the production case.
func kvResolvedKeysHook(ctx context.Context) func(keys []string) {
	fn, _ := ctx.Value(kvResolvedKeysHookKey{}).(func(keys []string))
	return fn
}

// kvFastPathRequestHookKey is the unexported context key
// WithKVFastPathRequestHook stores its callback under.
type kvFastPathRequestHookKey struct{}

// WithKVFastPathRequestHook returns a context that makes every FAST-PATH
// multi-get request issued under it report the number of subjects it carried,
// once per request — retries included, since each is its own round trip.
//
// It exists for the same reason WithKVResolveThenGetHook does, one layer down:
// a complete map comes back whether it was assembled from one request or from
// twenty, so only a per-request count can pin a caller's stated bound. A caller
// that chunks its own request set to stay on the fast path (see
// KVDirectGetSubjectCap) has no other way to show its ceil(N/cap) claim is
// true, and a bound nothing measures is prose.
//
// Production code never calls this; it is a test-only seam, mirroring
// WithKVDrainRoundHook.
func WithKVFastPathRequestHook(ctx context.Context, fn func(subjects int)) context.Context {
	return context.WithValue(ctx, kvFastPathRequestHookKey{}, fn)
}

// kvFastPathRequestHook reads back the callback WithKVFastPathRequestHook
// installed, returning nil when none was — the production case.
func kvFastPathRequestHook(ctx context.Context) func(subjects int) {
	fn, _ := ctx.Value(kvFastPathRequestHookKey{}).(func(subjects int))
	return fn
}
