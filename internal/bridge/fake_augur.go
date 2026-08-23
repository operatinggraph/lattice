package bridge

import (
	"context"
	"fmt"
	"sync"
)

// FakeAugur is the deterministic reference Adapter for the Augur reasoning tier
// (Weaver's L3 evaluator) — the CI / e2e workhorse that exercises the whole
// escalate → reason → record → review loop with NO real model call (no network,
// no spend, fully deterministic). It mirrors the other reference adapters
// (FakeStripe / FakeBackgroundCheck): it is synchronous (always returns a
// Resolved Dispatch), in-memory, and records every idempotencyKey it has reasoned
// for so a repeat key returns the SAME proposal WITHOUT a second side-effect (the
// per-key reasoning-call counter does not increment). That counter is the
// cost-control proof the design calls out: at most one billed reasoning call per
// escalation episode, even under redelivery / reclaim.
//
// The proposal it returns is the model's structured output only (an AugurProposal
// carried in Result.Detail); the gap context (targetId / entityId / …) is the
// RecordProposal replyOp's job to reconstruct from the claim vertex, never the
// adapter's. The real `claude-opus-4-8`-backed adapter is a follow-on increment;
// FakeAugur is what CI runs.
//
// Designated trigger Subjects select the adversarial paths the §5 deterministic
// validator must DEFEND against (the e2e mints the externalTask instance with one
// of these bare handles, which the bridge passes through as Request.Subject):
//
//   - AugurScopeEscapeSubject   → a proposal whose action targets a DIFFERENT
//     entity than the escalated candidate (the §5 scope-escape class).
//   - AugurUnknownActionSubject → a proposal naming an action outside the allowed
//     {triggerLoom, assignTask, directOp} vocabulary.
//   - AugurBadConfidenceSubject → a structurally-valid proposal with a confidence
//     outside [0,1].
//   - AugurRefusalSubject       → a terminal OutcomeFailed (a modeled stop_reason
//     "refusal": the model declined to propose; err == nil, a definitive verdict
//     the bridge must NOT retry), carrying NO proposal.
//
// Any other Subject yields a benign, in-scope, VALID assignTask proposal scoped
// to the escalated candidate (read from Request.Params["entityId"], falling back
// to the Subject handle) — the happy path: a stuck gap becomes a `pending`,
// human-reviewable proposal.
type FakeAugur struct {
	mu sync.Mutex
	// results memoizes the Result returned per idempotencyKey, so a redelivery on
	// the same key replays the first reasoning verbatim with no second call.
	results map[string]Result
	// calls counts the reasoning side-effects actually performed per
	// idempotencyKey — the cost-control assertion: a repeat key stays at 1.
	calls map[string]int
	// override, when set, is the proposal returned for any NON-trigger Subject
	// instead of the default benign assignTask — the seam a test (or a live demo)
	// uses to inject an arbitrary proposal (valid or crafted-malicious) without a
	// dedicated trigger subject.
	override *AugurProposal
}

// FakeAugur trigger Subjects (the bare instanceKey handle the bridge passes as
// Request.Subject). Each selects one adversarial / edge path the §5 validator and
// the bridge must handle.
const (
	// AugurScopeEscapeSubject makes FakeAugur propose acting on a DIFFERENT entity
	// than the escalated candidate (a directOp scopedTo a foreign vtx key) — the
	// §5 scope-escape class the validator must store `invalid`.
	AugurScopeEscapeSubject = "augur-scope-escape"
	// AugurUnknownActionSubject makes FakeAugur propose an action outside the
	// allowed escalation vocabulary — the §5 unknown-action class.
	AugurUnknownActionSubject = "augur-unknown-action"
	// AugurBadConfidenceSubject makes FakeAugur return a structurally-valid
	// proposal with a confidence outside [0,1] — the §5 confidence-range class.
	AugurBadConfidenceSubject = "augur-bad-confidence"
	// AugurRefusalSubject makes FakeAugur return a terminal OutcomeFailed modeling
	// a model refusal (stop_reason "refusal"): a definitive verdict (err == nil),
	// no proposal, the bridge must not retry it.
	AugurRefusalSubject = "augur-refusal"
	// fakeAugurForeignEntity is the foreign entity key the scope-escape proposal
	// targets — deliberately not the escalated candidate. A type-neutral kernel
	// key (the bridge is type-agnostic platform code — no vertical type leaks in).
	fakeAugurForeignEntity = "vtx.identity.someForeignActor"
	// fakeAugurModel is the provenance model id FakeAugur stamps when the
	// request carries no model override. It names the fake itself rather than a
	// real vendor model: the value travels on the proposal, through the
	// augur-proposals lens, to the operator deciding whether to approve, so a
	// vendor model id here would attribute a canned proposal to a model that
	// never ran. A reviewer must be able to tell reasoning from fixture at a
	// glance, and the provenance field is where they look.
	fakeAugurModel = "fake-augur: reference adapter, no model call"
)

// NewFakeAugur returns a fresh in-memory reference reasoning adapter.
func NewFakeAugur() *FakeAugur {
	return &FakeAugur{
		results: make(map[string]Result),
		calls:   make(map[string]int),
	}
}

// SetProposal overrides the proposal FakeAugur returns for any non-trigger
// Subject — the injection seam for a test (or a live demo) that needs a specific
// proposal. The trigger Subjects above still take precedence. Set once before the
// adapter is exercised.
func (f *FakeAugur) SetProposal(p AugurProposal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.override = &p
}

// Execute performs the (mocked) reasoning call exactly once per idempotencyKey.
// It is synchronous: it always returns a Resolved Dispatch (a terminal Result
// inline, never Pending). The first call for a key records the side-effect and a
// deterministic Result; any later call with the same key returns that Result and
// performs NO further reasoning call. The trigger Subjects select the adversarial
// / refusal paths; every other Subject yields a benign, in-scope, valid
// assignTask proposal. No network, no real model call.
func (f *FakeAugur) Execute(_ context.Context, req Request) (Dispatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if res, seen := f.results[req.IdempotencyKey]; seen {
		return Dispatch{Disposition: Resolved, Result: res}, nil
	}

	// A modeled refusal is a terminal business verdict (no reasoning side-effect
	// to bill, no proposal), memoized so a redelivery replays the same verdict.
	if req.Subject == AugurRefusalSubject {
		res := Result{Status: OutcomeFailed, Detail: "augur: model declined to propose (refusal) for " + req.Subject}
		f.results[req.IdempotencyKey] = res
		return Dispatch{Disposition: Resolved, Result: res}, nil
	}

	f.calls[req.IdempotencyKey]++
	proposal := f.proposalFor(req)
	detail, err := proposal.Encode()
	if err != nil {
		// A well-formed AugurProposal always encodes; surface a wiring bug loudly
		// (a transient-looking error the bridge re-drives, never a blank Detail).
		return Dispatch{}, fmt.Errorf("bridge: FakeAugur encode proposal for key %s: %w", req.IdempotencyKey, err)
	}
	res := Result{Status: OutcomeCompleted, Detail: detail}
	f.results[req.IdempotencyKey] = res
	return Dispatch{Disposition: Resolved, Result: res}, nil
}

// proposalFor builds the deterministic proposal for a Request: a trigger Subject
// selects its adversarial shape; an override (if set) wins for non-trigger
// Subjects; otherwise the benign in-scope assignTask. Caller holds f.mu.
func (f *FakeAugur) proposalFor(req Request) AugurProposal {
	entity := req.Params["entityId"]
	if entity == "" {
		entity = req.Subject
	}
	// model honors the request's optional override (Weaver's augur.model,
	// threaded through as Params["model"]) exactly as a real model-backed
	// adapter would, falling back to the fixed fake default when the target
	// set none — this is what makes the override genuinely observable
	// end-to-end rather than a value that's threaded through and dropped.
	model := req.Params["model"]
	if model == "" {
		model = fakeAugurModel
	}
	base := AugurProposal{
		Model:       model,
		PromptHash:  "fake-prompt-hash",
		CatalogHash: "fake-catalog-hash",
		ReasonedAt:  "2026-06-29T00:00:00Z",
	}
	switch req.Subject {
	case AugurScopeEscapeSubject:
		base.Action = "directOp"
		base.Params = map[string]any{"scopedTo": fakeAugurForeignEntity}
		base.Rationale = "crafted scope-escape: targets a different entity than the escalated candidate"
		base.Confidence = 0.95
		return base
	case AugurUnknownActionSubject:
		base.Action = "deleteEverything"
		base.Params = map[string]any{"scopedTo": entity}
		base.Rationale = "crafted unknown-action: not in the allowed escalation vocabulary"
		base.Confidence = 0.9
		return base
	case AugurBadConfidenceSubject:
		base.Action = "assignTask"
		base.Params = map[string]any{"scopedTo": entity, "forOperation": "ApproveLeaseApplication"}
		base.Rationale = "crafted out-of-range confidence"
		base.Confidence = 1.5
		return base
	}
	if f.override != nil {
		return *f.override
	}
	base.Action = "assignTask"
	base.Params = map[string]any{"scopedTo": entity, "forOperation": "ApproveLeaseApplication"}
	base.Rationale = "no playbook entry; the closest catalog action is a human approval task scoped to the escalated candidate"
	base.Confidence = 0.82
	return base
}

// Poll is unreachable for this synchronous adapter (Execute never returns
// Pending, so the bridge never holds a Ref to poll). It returns a clear error so
// a wiring mistake surfaces rather than silently resolving.
func (f *FakeAugur) Poll(_ context.Context, ref string) (Dispatch, error) {
	return Dispatch{}, fmt.Errorf("bridge: FakeAugur is synchronous: Poll unsupported (ref %q)", ref)
}

// SideEffects reports how many reasoning calls were actually performed for
// idempotencyKey — 0 before the first Execute, and exactly 1 no matter how many
// repeat Executes follow on the same key (the cost-control proof asserts at most
// 1). A refusal performs no reasoning side-effect, so its count stays 0.
func (f *FakeAugur) SideEffects(idempotencyKey string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[idempotencyKey]
}
