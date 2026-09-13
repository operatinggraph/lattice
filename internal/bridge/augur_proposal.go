package bridge

import (
	"encoding/json"
	"fmt"
)

// AugurProposal is the model's STRUCTURED OUTPUT for one Augur reasoning call —
// the remediation Claude proposes for a stuck Weaver convergence gap. It is the
// payload an `augur` adapter returns, carried verbatim in the bridge
// Dispatch's Result.Detail string (the bridge treats Detail as opaque, design
// §augur). It is deliberately ONLY the model's answer: the gap context
// (targetId / entityId / gapColumn / trigger) is NOT part of it — that context is
// reconstructed by the RecordProposal replyOp from the externalTask claim vertex
// (the same `{externalRef, status, result}` reply contract every externalTask
// replyOp consumes), never invented by the model. This split is the load-bearing
// safety property: the model can only PROPOSE an action + params; it never
// supplies the identity of the entity it is acting on.
//
// Confidence is the model's self-reported 0..1 score; the RecordProposal §5
// validator stores the proposal `invalid` if it falls outside [0,1]. Params is
// the proposed action's free-form params object (validated for scope-escape at
// record time). Provenance fields record exactly what was reasoned over for the
// audit trail + stale-proposal detection.
//
// A proposal is either single-step or PLAN-SHAPED. A single-step proposal names
// its remediation in Action/Params and leaves Steps empty. A plan-shaped
// proposal lists its ordered remediation in Steps, and the record op mirrors
// steps[0] into the stored action/params — so when Steps is non-empty the
// top-level Action/Params carry no meaning for the recorded proposal and are
// ignored. Each step is validated against the §5 boundary independently, and
// Weaver dispatches the plan one leg per episode.
type AugurProposal struct {
	// Action is the proposed remediation action. The RecordProposal §5 validator
	// rejects (stores invalid) any action outside {triggerLoom, assignTask,
	// directOp} — the model gains no new authority, only the ability to propose
	// arranging the actions Weaver already holds.
	Action string `json:"action"`
	// Params is the proposed action's params. A param naming an entity other than
	// the escalated candidate is a scope escape → the proposal is stored invalid.
	Params map[string]any `json:"params,omitempty"`
	// Steps is the ordered plan of a plan-shaped proposal. Empty for a
	// single-step proposal, in which case Action/Params carry the remediation.
	// When it is non-empty the record op mirrors steps[0] into the stored
	// action/params and IGNORES the top-level Action/Params; every step is
	// validated against the same §5 boundary as a single-step proposal, and a
	// step that fails it invalidates the whole proposal.
	Steps []AugurStep `json:"steps,omitempty"`
	// Rationale is the model's free-form reasoning, stored for the audit trail.
	Rationale string `json:"rationale,omitempty"`
	// Confidence is the model's 0..1 self-reported confidence. Out of range →
	// the proposal is stored invalid.
	Confidence float64 `json:"confidence"`
	// Model is the model id that produced the proposal (e.g. claude-opus-4-8).
	Model string `json:"model,omitempty"`
	// PromptHash is a hash of the exact prompt reasoned over (audit + stale detect).
	PromptHash string `json:"promptHash,omitempty"`
	// CatalogHash is a hash of the action catalog reasoned over; lets a reviewer
	// detect a proposal that reasoned over a since-changed catalog.
	CatalogHash string `json:"catalogHash,omitempty"`
	// ReasonedAt is the RFC3339 timestamp of the reasoning call.
	ReasonedAt string `json:"reasonedAt,omitempty"`
}

// AugurStep is one leg of a plan-shaped proposal: the same {action, params}
// pair a single-step proposal carries at the top level, validated against the
// same §5 boundary and dispatched as its own Weaver episode.
type AugurStep struct {
	// Action is this leg's proposed remediation action, from the same allowed
	// escalation vocabulary {triggerLoom, assignTask, directOp}.
	Action string `json:"action"`
	// Params is this leg's params. A param naming an entity other than the
	// escalated candidate is a scope escape → the whole proposal is invalid.
	Params map[string]any `json:"params,omitempty"`
}

// Encode marshals the proposal to the JSON string the bridge carries in
// Result.Detail. It never fails for a well-formed AugurProposal (the field types
// are all JSON-encodable); a marshal error is surfaced rather than silently
// producing a blank Detail, so a wiring bug is loud.
func (p AugurProposal) Encode() (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("bridge: encode augur proposal: %w", err)
	}
	return string(b), nil
}

// DecodeAugurProposal parses the Result.Detail JSON the augur adapter produced
// back into a typed proposal — the consumer side of the codec (the RecordProposal
// reply leg, and tests). A blank detail or malformed JSON is a loud error: a
// reasoning reply that cannot be parsed must never be silently recorded as an
// empty proposal (it would validate as invalid-action and mask the real failure).
func DecodeAugurProposal(detail string) (AugurProposal, error) {
	if detail == "" {
		return AugurProposal{}, fmt.Errorf("bridge: decode augur proposal: empty detail")
	}
	var p AugurProposal
	if err := json.Unmarshal([]byte(detail), &p); err != nil {
		return AugurProposal{}, fmt.Errorf("bridge: decode augur proposal: %w", err)
	}
	return p, nil
}
