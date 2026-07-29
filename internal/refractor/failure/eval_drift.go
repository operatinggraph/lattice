package failure

import "errors"

// ErrEvalDrift signals that an auth-plane evaluation's read-surface footprint
// still diverged from current KV state after one inline re-execution — the
// evaluation never lands a torn (fabricated-combination) row, but its output
// cannot be trusted at read-now semantics either, so the caller requeues the
// actor rather than writing anything
// (refractor-evaluation-consistency-design.md §4.3). Classify routes it to
// CatTransient: the pump's retry-queue and the sweep's repair-failure
// accounting both already know how to retry a transient failure, and drift is
// ms-scale, so retrying is the correct response rather than a permanent
// failure.
var ErrEvalDrift = errors.New("failure: evaluation read-surface drifted and did not converge after re-execution")
