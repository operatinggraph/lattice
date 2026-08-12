package health

import (
	"sort"
	"sync"
)

// SyncStreamWitness records every distinct SYNC stream name a process has
// seen a Personal Lens target, and answers the two questions that follow from
// it: "is this the first time?" and "is the fleet still unambiguous?".
//
// LIFETIME: one per process, created empty at boot, never persisted, never
// pruned. Nothing here is a cache of server state — it is a record of what
// THIS process has been told to run, so it cannot go stale; the only way an
// entry becomes wrong is a process restart, which discards it wholesale and
// re-learns from the lens rules on the way back up.
//
// It exists because personal-lens-interest is ONE bucket keyed
// "<identityId>.<deviceId>" with no stream dimension, while a reconciler
// probes durables on exactly one stream. Two Personal Lenses targeting
// different streams would therefore have each reconciler see every OTHER
// stream's devices as durable-less and delete them — the whole bucket, every
// sweep, in both directions. Nothing outside packages/edge-manifest pins the
// stream name to "SYNC", and a hot reload can move Into.Stream under a
// running reconciler, so the ambiguity is not merely hypothetical.
//
// Loupe already refuses to render a fleet-wide verdict on this exact input
// (cmd/loupe/edge.go's syncStreamForFleet). An inspector that will not
// DISPLAY an answer and a reconciler that proceeds to DELETE on the same
// input must not disagree about whether the input is interpretable.
type SyncStreamWitness struct {
	mu      sync.Mutex
	seen    map[string]struct{}
	ordered []string
}

// NewSyncStreamWitness returns an empty witness.
func NewSyncStreamWitness() *SyncStreamWitness {
	return &SyncStreamWitness{seen: map[string]struct{}{}}
}

// Observe records stream and reports whether this process had not seen it
// before — the caller's cue to run the once-per-stream boot work. Safe for
// concurrent callers: lens activation runs from the CDC load callback, the
// taxonomy retry and the bootstrap-lens goroutine.
func (w *SyncStreamWitness) Observe(stream string) (first bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.seen[stream]; ok {
		return false
	}
	w.seen[stream] = struct{}{}
	w.ordered = append(w.ordered, stream)
	return true
}

// Ambiguous returns every stream seen, sorted, once more than one has been —
// and nil while the fleet is still interpretable. A caller that DELETES on
// per-stream evidence must treat a non-nil answer as "stop".
func (w *SyncStreamWitness) Ambiguous() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.ordered) < 2 {
		return nil
	}
	out := append([]string(nil), w.ordered...)
	sort.Strings(out)
	return out
}
