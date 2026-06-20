package bridge

import (
	"context"
	"fmt"
	"sync"
)

// Request is one external-call dispatch handed to an Adapter. IdempotencyKey is
// the instanceKey (Contract #10 §10.3): the adapter MUST treat two Requests
// bearing the same key as the same action and produce at most one external
// side-effect. Operation/Subject/Params carry the resolved call fields so an
// adapter can shape the real external call (which external endpoint, on whose
// behalf, with what arguments).
type Request struct {
	IdempotencyKey string
	Operation      string
	Subject        string
	Params         map[string]string
}

// Outcome is an adapter's TERMINAL business verdict on an Execute that ran to
// completion (err == nil). It is opaque to the bridge: the bridge copies it into
// the result op's payload verbatim and never branches on it — the {Completed,
// Failed} vocabulary is a contract between an adapter and ITS paired replyOp
// (the same posture as the free-form Detail), not bridge knowledge. It is
// distinct from a returned error: an error is a (possibly transient) failure the
// bridge re-drives on a bounded cadence, whereas a Failed Outcome is a definitive
// business rejection (a declined charge, a failed background check) that must NOT
// be retried.
type Outcome string

const (
	// OutcomeCompleted is the terminal success verdict: the external call
	// succeeded with a satisfying result.
	OutcomeCompleted Outcome = "completed"
	// OutcomeFailed is the terminal business-failure verdict: the external call
	// completed but returned a definitive rejection (e.g. a declined payment, a
	// failed background check). It is returned with err == nil — errors remain
	// reserved for transient retry.
	OutcomeFailed Outcome = "failed"
)

// Result is an Adapter's response to an Execute that ran to completion
// (err == nil). Status is the terminal business verdict (completed | failed —
// Failed is a definitive rejection, not an error); it is opaque to the bridge, copied
// verbatim into the result op's payload for the adapter's paired replyOp to act
// on. Detail is an adapter-defined opaque outcome string (a confirmation
// reference, a decision) carried into the result op's payload for the audit join;
// like Status it is never interpreted by the bridge.
type Result struct {
	Status Outcome
	Detail string
}

// Disposition is the adapter's verdict on whether an Execute (or a Poll) reached
// a terminal Result inline, or merely submitted the call to a vendor that will
// resolve it later. It is orthogonal to the {Completed, Failed} Outcome: a
// Resolved dispatch carries a terminal Outcome, a Pending dispatch carries none
// yet. It is distinct from a returned error: an error is a (possibly transient)
// failure the bridge re-drives; a Pending Disposition is an orderly "submitted,
// not yet answered."
type Disposition int

const (
	// Resolved is the terminal disposition: the call ran to a final Result inline
	// (the Dispatch's Result field is valid). A synchronous adapter always returns
	// Resolved.
	Resolved Disposition = iota
	// Pending is the in-flight disposition: the adapter submitted the call to the
	// vendor and it will resolve later (via a Poll or a webhook). The Dispatch's
	// Ref field carries the vendor's opaque reference; no Result is determined yet.
	Pending
)

// Dispatch is an Adapter's response to an Execute (or a Poll) that ran without a
// returned error. Disposition selects which other field is load-bearing:
//
//   - Resolved → Result is the terminal verdict (carries Status / Detail); the
//     bridge posts the replyOp exactly as a synchronous call does.
//   - Pending → Ref is the vendor's opaque pending reference (the poll/webhook
//     key); Result is unset and the bridge records a pending marker instead of a
//     terminal outcome.
//
// It is named Dispatch (not Outcome) to avoid colliding with the terminal-verdict
// Outcome enum the Result carries: a Dispatch wraps that verdict together with the
// pending/resolved decision, so the two cannot share a name.
type Dispatch struct {
	Disposition Disposition
	Result      Result
	Ref         string
}

// Adapter is the unit of "call one external system idempotently" — the external
// integration a dispatched external call resolves to. The bridge calls Execute
// after a visible claim already exists (the claim vertex the instanceOp minted
// write-ahead, before the external.* event was publishable); the adapter owns
// the real external action.
//
// The idempotencyKey on the Request (= the instanceKey) is the contract: the
// adapter is the de-dup boundary, NOT the bridge. Two Execute calls with the
// same idempotencyKey MUST yield exactly one external side-effect and the same
// Dispatch — this is what makes a redelivery/recovery re-call on the same
// instanceKey safe (a re-submitted Pending must return the same Ref, no new
// side-effect). A returned error is a (possibly transient) failure: the bridge
// surfaces it and re-drives the event on a bounded cadence; it does not retry
// inline.
//
// Execute returns a Dispatch: a synchronous adapter returns Resolved with a
// terminal Result (today's path); a vendor that submits-then-resolves-later
// returns Pending with a vendor Ref. Poll resolves a previously-Pending Ref — it
// returns Resolved once the vendor answers, Pending while still in flight, and an
// error for a transient probe failure. Poll lands now so the SPI is stable; the
// driver that calls it on a schedule is a later increment.
type Adapter interface {
	Execute(ctx context.Context, req Request) (Dispatch, error)
	Poll(ctx context.Context, ref string) (Dispatch, error)
}

// AdapterFunc adapts a plain SYNCHRONOUS Execute function to the Adapter
// interface — the usual convenience for the common case (a one-method synchronous
// adapter), and a clean seam for tests and small inline adapters. A function-only
// adapter is synchronous by construction, so its Poll is unreachable: it never
// returns Pending, so the bridge never has a Ref to poll.
type AdapterFunc func(ctx context.Context, req Request) (Dispatch, error)

// Execute calls the underlying function.
func (f AdapterFunc) Execute(ctx context.Context, req Request) (Dispatch, error) {
	return f(ctx, req)
}

// Poll is unreachable for a synchronous AdapterFunc (it never returns Pending, so
// no Ref is ever handed back to poll). It returns a clear error rather than a
// silent zero Dispatch, so a wiring mistake that routes a poll here surfaces.
func (f AdapterFunc) Poll(_ context.Context, ref string) (Dispatch, error) {
	return Dispatch{}, fmt.Errorf("bridge: synchronous adapter: Poll unsupported (ref %q)", ref)
}

// Registry resolves an adapter name (the external event's adapter field) to a
// concrete Adapter at dispatch time. An event naming an unregistered adapter is
// a config error, surfaced by Lookup's ok=false (never a silent no-op) — the
// bridge acks the event and raises a Health issue, never a hot Nak loop.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

// NewRegistry returns an empty adapter registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter)}
}

// Register binds name to adapter. A blank name or nil adapter is rejected, and
// re-registering an already-bound name is rejected — an adapter set is built
// once at engine construction, so a duplicate name is a wiring bug, surfaced
// rather than silently shadowing the prior binding.
func (r *Registry) Register(name string, adapter Adapter) error {
	if name == "" {
		return fmt.Errorf("bridge: adapter name is required")
	}
	if adapter == nil {
		return fmt.Errorf("bridge: adapter %q is nil", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[name]; exists {
		return fmt.Errorf("bridge: adapter %q already registered", name)
	}
	r.adapters[name] = adapter
	return nil
}

// Lookup resolves an adapter name to its registered Adapter. ok=false means no
// adapter is bound to that name — a config error the caller must surface, never
// treat as a silent skip.
func (r *Registry) Lookup(name string) (adapter Adapter, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	return a, ok
}
