package bridge

import (
	"context"
	"fmt"
	"sync"
)

// FakeCapabilityAuthor is the deterministic reference Adapter for the
// AI-authored-capabilities escalation dispatch (design
// ai-authored-capabilities-design.md §3.4) — the CI / e2e workhorse that
// exercises the request → dispatch → reason → record loop with NO real model
// call (no network, no spend, fully deterministic). It mirrors FakeAugur: it
// is synchronous (always returns a Resolved Dispatch), in-memory, and records
// every idempotencyKey it has reasoned for so a repeat key returns the SAME
// proposal WITHOUT a second side-effect (the per-key reasoning-call counter is
// the cost-control proof — at most one billed reasoning call per authoring
// episode, even under redelivery).
//
// The real claude-opus-4-8-backed adapter is a follow-on increment — the same
// posture Augur's own adapter is still in (FakeAugur is what CI runs there
// too). Every non-trigger request yields a benign, VALID lens proposal built
// from Request.Params["intent"] (the subject-templated field
// CreateAuthoringClaim resolved from the subject's own .request aspect); the
// §5 malicious-artifact DEFENDED classes are already proven directly against
// RecordCapabilityProposal (proposal_test.go) — this adapter's job is only to
// exercise the dispatch mechanism, not re-prove the validation boundary.
type FakeCapabilityAuthor struct {
	mu sync.Mutex
	// results memoizes the Result returned per idempotencyKey, so a redelivery
	// on the same key replays the first reasoning verbatim with no second call.
	results map[string]Result
	// calls counts the reasoning side-effects actually performed per
	// idempotencyKey — the cost-control assertion: a repeat key stays at 1.
	calls map[string]int
	// override, when set, is the proposal returned for any NON-refusal request
	// instead of the default benign lens — the seam a test uses to inject an
	// arbitrary proposal (valid or crafted-invalid) without a dedicated trigger.
	override *CapabilityAuthorProposal
}

// FakeCapabilityAuthorRefusalIntent makes FakeCapabilityAuthor return a
// terminal OutcomeFailed (a modeled stop_reason "refusal": the model declined
// to propose; err == nil, a definitive verdict the bridge must NOT retry),
// carrying no proposal — matches on Request.Params["intent"].
const FakeCapabilityAuthorRefusalIntent = "capability-author-refusal"

// fakeCapabilityAuthorModel is the provenance model id FakeCapabilityAuthor
// stamps (the design's default reasoning model).
const fakeCapabilityAuthorModel = "claude-opus-4-8"

// NewFakeCapabilityAuthor returns a fresh in-memory reference reasoning
// adapter.
func NewFakeCapabilityAuthor() *FakeCapabilityAuthor {
	return &FakeCapabilityAuthor{
		results: make(map[string]Result),
		calls:   make(map[string]int),
	}
}

// SetProposal overrides the proposal FakeCapabilityAuthor returns for any
// non-refusal request — the injection seam for a test that needs a specific
// (valid or invalid-shaped) artifact. Set once before the adapter is
// exercised.
func (f *FakeCapabilityAuthor) SetProposal(p CapabilityAuthorProposal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.override = &p
}

// Execute performs the (mocked) reasoning call exactly once per
// idempotencyKey. It is synchronous: it always returns a Resolved Dispatch (a
// terminal Result inline, never Pending). No network, no real model call.
func (f *FakeCapabilityAuthor) Execute(_ context.Context, req Request) (Dispatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if res, seen := f.results[req.IdempotencyKey]; seen {
		return Dispatch{Disposition: Resolved, Result: res}, nil
	}

	intent := req.Params["intent"]

	// A modeled refusal is a terminal business verdict (no reasoning
	// side-effect to bill, no proposal), memoized so a redelivery replays the
	// same verdict.
	if intent == FakeCapabilityAuthorRefusalIntent {
		res := Result{Status: OutcomeFailed, Detail: "capabilityAuthor: model declined to propose (refusal) for intent " + intent}
		f.results[req.IdempotencyKey] = res
		return Dispatch{Disposition: Resolved, Result: res}, nil
	}

	f.calls[req.IdempotencyKey]++
	proposal := f.proposalFor(intent)
	detail, err := proposal.Encode()
	if err != nil {
		// A well-formed CapabilityAuthorProposal always encodes; surface a
		// wiring bug loudly (a transient-looking error the bridge re-drives,
		// never a blank Detail).
		return Dispatch{}, fmt.Errorf("bridge: FakeCapabilityAuthor encode proposal for key %s: %w", req.IdempotencyKey, err)
	}
	res := Result{Status: OutcomeCompleted, Detail: detail}
	f.results[req.IdempotencyKey] = res
	return Dispatch{Disposition: Resolved, Result: res}, nil
}

// proposalFor builds the deterministic proposal for a request: an override
// (if set) wins; otherwise a benign, in-scope, VALID lens artifact whose
// rationale echoes the requested intent. Caller holds f.mu.
func (f *FakeCapabilityAuthor) proposalFor(intent string) CapabilityAuthorProposal {
	if f.override != nil {
		return *f.override
	}
	return CapabilityAuthorProposal{
		Kind: "lens",
		Content: `{"canonicalName":"activeProvidersBySpecialty","adapter":"nats-kv",` +
			`"bucket":"active-providers","spec":"MATCH (p:provider) RETURN p.key AS key"}`,
		Target:     CapabilityAuthorTarget{Mode: "newPackage"},
		Rationale:  "reasoned proposal for: " + intent,
		Confidence: 0.86,
		Validation: CapabilityAuthorValidation{State: "valid"},
		Provenance: CapabilityAuthorProvenance{
			Model:       fakeCapabilityAuthorModel,
			PromptHash:  "fake-prompt-hash",
			CatalogHash: "fake-catalog-hash",
			ReasonedAt:  "2026-07-04T00:00:00Z",
		},
	}
}

// Poll is unreachable for this synchronous adapter (Execute never returns
// Pending, so the bridge never holds a Ref to poll). It returns a clear error
// so a wiring mistake surfaces rather than silently resolving.
func (f *FakeCapabilityAuthor) Poll(_ context.Context, ref string) (Dispatch, error) {
	return Dispatch{}, fmt.Errorf("bridge: FakeCapabilityAuthor is synchronous: Poll unsupported (ref %q)", ref)
}

// SideEffects reports how many reasoning calls were actually performed for
// idempotencyKey — 0 before the first Execute, and exactly 1 no matter how
// many repeat Executes follow on the same key (the cost-control proof asserts
// at most 1). A refusal performs no reasoning side-effect, so its count stays
// 0.
func (f *FakeCapabilityAuthor) SideEffects(idempotencyKey string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[idempotencyKey]
}
