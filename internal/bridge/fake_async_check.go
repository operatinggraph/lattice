package bridge

import (
	"context"
	"sync"
)

// asyncRefPrefix tags the vendor reference FakeAsyncCheck mints, so the value the
// bridge records as the pending marker is visibly a vendor ref (and distinct from
// the bare instanceKey it derives from).
const asyncRefPrefix = "async-ref-"

// FakeAsyncCheck is a reference Adapter that models a real submit-then-resolve-
// later vendor (a background check that returns a pending reference and resolves
// hours later). It exercises the async path end-to-end with no infrastructure:
// Execute always returns a Pending Dispatch carrying a deterministic vendor Ref
// (derived from the idempotencyKey), and Poll(ref) returns Pending for the first
// PollsUntilResolved calls on that ref, then a Resolved Dispatch with a terminal
// Result.
//
// It is idempotent like the synchronous fakes: a repeat Execute on the same
// idempotencyKey returns the SAME Ref with NO new side-effect (so a redelivered
// Pending event re-posts the same create-only dispatch op and collapses on the
// Contract #4 tracker). Poll is the resolution half of the SPI — re-checking a
// pending call by its vendor Ref — and is covered directly by the unit test.
type FakeAsyncCheck struct {
	// PollsUntilResolved is how many Poll calls on a given ref return Pending
	// before it resolves. 0 → the first Poll already resolves. Set before use.
	PollsUntilResolved int

	mu sync.Mutex
	// submitted counts the side-effects actually performed per idempotencyKey —
	// the idempotency assertion: a repeat key must leave its count at 1.
	submitted map[string]int
	// refs memoizes the vendor Ref returned for each idempotencyKey, so a repeat
	// key returns the first call's ref verbatim.
	refs map[string]string
	// polls counts Poll calls per ref, so the adapter resolves only after
	// PollsUntilResolved pending answers.
	polls map[string]int
}

// NewFakeAsyncCheck returns a fresh in-memory async reference adapter that
// resolves after pollsUntilResolved pending polls (0 → resolves on the first
// Poll).
func NewFakeAsyncCheck(pollsUntilResolved int) *FakeAsyncCheck {
	return &FakeAsyncCheck{
		PollsUntilResolved: pollsUntilResolved,
		submitted:          make(map[string]int),
		refs:               make(map[string]string),
		polls:              make(map[string]int),
	}
}

// Execute submits the (mocked) external action exactly once per idempotencyKey
// and always returns a Pending Dispatch — the vendor accepted the request and
// will resolve it later. The Ref is deterministic in the idempotencyKey, so a
// redelivery returns the same Ref with NO new side-effect (the per-key submit
// counter does not increment). No network, no real I/O.
func (f *FakeAsyncCheck) Execute(_ context.Context, req Request) (Dispatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref, seen := f.refs[req.IdempotencyKey]
	if !seen {
		f.submitted[req.IdempotencyKey]++
		ref = asyncRefPrefix + req.IdempotencyKey
		f.refs[req.IdempotencyKey] = ref
	}
	return Dispatch{Disposition: Pending, Ref: ref}, nil
}

// Poll reports whether the submitted call has resolved. The first
// PollsUntilResolved calls on a ref return a Pending Dispatch (still in flight);
// the next returns a Resolved Dispatch with a terminal OutcomeCompleted Result.
// It is idempotent once resolved: every later Poll on the same ref returns the
// same Resolved Result.
func (f *FakeAsyncCheck) Poll(_ context.Context, ref string) (Dispatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.polls[ref] < f.PollsUntilResolved {
		f.polls[ref]++
		return Dispatch{Disposition: Pending, Ref: ref}, nil
	}
	return Dispatch{
		Disposition: Resolved,
		Result:      Result{Status: OutcomeCompleted, Detail: "async-check cleared for " + ref},
	}, nil
}

// SideEffects reports how many times the real submit was performed for
// idempotencyKey — 0 before the first Execute, and exactly 1 no matter how many
// repeat Executes follow (the idempotency proof asserts this).
func (f *FakeAsyncCheck) SideEffects(idempotencyKey string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.submitted[idempotencyKey]
}
