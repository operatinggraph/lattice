package bridge

import (
	"context"
	"fmt"
	"sync"
)

// FakeNotification is a reference Adapter modeling an idempotent notification
// send (email/SMS). Like FakeStripe/FakeBackgroundCheck/FakeDocGen it is
// deterministic, in-memory, and performs NO real vendor I/O; it records every
// idempotencyKey it has "sent" and, on a repeat key, returns the SAME Result
// with NO second side-effect. A real vendor integration (SendGrid/Twilio) is a
// later increment (docs/components/bridge.md; the clinic-reminders-
// notification-adapter-design.md follow-on).
type FakeNotification struct {
	mu sync.Mutex
	// results memoizes the Result returned for each successfully-sent
	// idempotencyKey, so a repeat key returns the first call's Result verbatim.
	results map[string]Result
	// calls counts the side-effects actually performed per idempotencyKey — the
	// idempotency assertion: a repeat key must leave its count at 1.
	calls map[string]int
}

// NewFakeNotification returns a fresh in-memory reference notification adapter.
func NewFakeNotification() *FakeNotification {
	return &FakeNotification{
		results: make(map[string]Result),
		calls:   make(map[string]int),
	}
}

// Execute performs the (mocked) send exactly once per idempotencyKey. It is
// synchronous: a call always returns a Resolved Dispatch (a terminal Result
// inline, never Pending) — notification delivery here never fails (there is no
// declined-charge analog for a reminder send). The first call for a key
// records the side-effect and a deterministic OutcomeCompleted Result; any
// later call with the same key returns that Result and performs NO further
// side-effect. No network, no real I/O.
func (f *FakeNotification) Execute(_ context.Context, req Request) (Dispatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if res, seen := f.results[req.IdempotencyKey]; seen {
		return Dispatch{Disposition: Resolved, Result: res}, nil
	}
	f.calls[req.IdempotencyKey]++
	res := Result{Status: OutcomeCompleted, Detail: fmt.Sprintf("notification sent for %s", req.IdempotencyKey)}
	f.results[req.IdempotencyKey] = res
	return Dispatch{Disposition: Resolved, Result: res}, nil
}

// Poll is unreachable for this synchronous adapter (Execute never returns
// Pending, so the bridge never holds a Ref to poll). It returns a clear error
// so a wiring mistake surfaces rather than silently resolving.
func (f *FakeNotification) Poll(_ context.Context, ref string) (Dispatch, error) {
	return Dispatch{}, fmt.Errorf("bridge: FakeNotification is synchronous: Poll unsupported (ref %q)", ref)
}

// SideEffects reports how many times the send was performed for
// idempotencyKey — 0 before the first Execute, and exactly 1 no matter how
// many repeat Executes follow on the same key.
func (f *FakeNotification) SideEffects(idempotencyKey string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[idempotencyKey]
}
