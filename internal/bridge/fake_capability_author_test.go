package bridge_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/asolgan/lattice/internal/bridge"
)

// TestFakeCapabilityAuthor_HappyPath: a benign request yields a Resolved,
// OutcomeCompleted dispatch carrying a VALID lens proposal whose rationale
// echoes the requested intent. The reasoning side-effect is recorded exactly
// once.
func TestFakeCapabilityAuthor_HappyPath(t *testing.T) {
	t.Parallel()
	a := bridge.NewFakeCapabilityAuthor()
	intent := "a lens listing active providers by specialty"
	disp, err := a.Execute(context.Background(), bridge.Request{
		IdempotencyKey: "cap-1",
		Params:         map[string]string{"intent": intent},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if disp.Disposition != bridge.Resolved {
		t.Fatalf("FakeCapabilityAuthor is synchronous: Disposition = %v, want Resolved", disp.Disposition)
	}
	if disp.Result.Status != bridge.OutcomeCompleted {
		t.Fatalf("happy path Status = %q, want %q", disp.Result.Status, bridge.OutcomeCompleted)
	}
	var p bridge.CapabilityAuthorProposal
	if err := json.Unmarshal([]byte(disp.Result.Detail), &p); err != nil {
		t.Fatalf("decode proposal: %v", err)
	}
	if p.Kind != "lens" {
		t.Fatalf("happy proposal kind = %q, want lens", p.Kind)
	}
	if p.Validation.State != "valid" {
		t.Fatalf("happy proposal validation.state = %q, want valid", p.Validation.State)
	}
	if p.Confidence < 0 || p.Confidence > 1 {
		t.Fatalf("happy proposal confidence out of range: %v", p.Confidence)
	}
	if p.Rationale == "" {
		t.Fatalf("happy proposal rationale must echo the requested intent, got empty")
	}
	if got := a.SideEffects("cap-1"); got != 1 {
		t.Fatalf("one reasoning call performed: side effects = %d, want 1", got)
	}
}

// TestFakeCapabilityAuthor_IdempotentOnRepeatedKey is the cost-control proof:
// a repeat idempotencyKey returns the SAME proposal and performs NO second
// reasoning call (at most one billed model call per authoring episode, even
// under redelivery).
func TestFakeCapabilityAuthor_IdempotentOnRepeatedKey(t *testing.T) {
	t.Parallel()
	a := bridge.NewFakeCapabilityAuthor()
	req := bridge.Request{IdempotencyKey: "cap-rep", Params: map[string]string{"intent": "a lens"}}
	first, err := a.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	for i := 0; i < 3; i++ {
		again, err := a.Execute(context.Background(), req)
		if err != nil {
			t.Fatalf("repeat Execute: %v", err)
		}
		if again.Result.Detail != first.Result.Detail {
			t.Fatalf("repeat key must replay the same proposal:\n first = %q\n again = %q", first.Result.Detail, again.Result.Detail)
		}
	}
	if got := a.SideEffects("cap-rep"); got != 1 {
		t.Fatalf("repeat key must perform exactly one reasoning call: side effects = %d, want 1", got)
	}
}

// TestFakeCapabilityAuthor_Refusal: a modeled model refusal is a terminal
// OutcomeFailed (err == nil — a definitive verdict the bridge must not
// retry), carries no proposal, and performs no reasoning side-effect.
func TestFakeCapabilityAuthor_Refusal(t *testing.T) {
	t.Parallel()
	a := bridge.NewFakeCapabilityAuthor()
	disp, err := a.Execute(context.Background(), bridge.Request{
		IdempotencyKey: "ref",
		Params:         map[string]string{"intent": bridge.FakeCapabilityAuthorRefusalIntent},
	})
	if err != nil {
		t.Fatalf("a refusal is a terminal verdict, not a transient error: %v", err)
	}
	if disp.Result.Status != bridge.OutcomeFailed {
		t.Fatalf("refusal Status = %q, want %q", disp.Result.Status, bridge.OutcomeFailed)
	}
	if got := a.SideEffects("ref"); got != 0 {
		t.Fatalf("a refusal performs no reasoning side-effect: side effects = %d, want 0", got)
	}
}

// TestFakeCapabilityAuthor_SetProposalOverride: the injection seam returns an
// arbitrary proposal for a non-refusal request, while a refusal intent still
// wins.
func TestFakeCapabilityAuthor_SetProposalOverride(t *testing.T) {
	t.Parallel()
	a := bridge.NewFakeCapabilityAuthor()
	a.SetProposal(bridge.CapabilityAuthorProposal{
		Kind:       "lens",
		Content:    `{"canonicalName":"custom"}`,
		Confidence: 0.5,
		Validation: bridge.CapabilityAuthorValidation{State: "invalid"},
	})
	disp, err := a.Execute(context.Background(), bridge.Request{IdempotencyKey: "ov", Params: map[string]string{"intent": "anything"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var p bridge.CapabilityAuthorProposal
	if err := json.Unmarshal([]byte(disp.Result.Detail), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Validation.State != "invalid" {
		t.Fatalf("override validation.state = %q, want invalid", p.Validation.State)
	}

	// A refusal intent still selects the terminal-failure shape, not the override.
	disp2, err := a.Execute(context.Background(), bridge.Request{
		IdempotencyKey: "ov2",
		Params:         map[string]string{"intent": bridge.FakeCapabilityAuthorRefusalIntent},
	})
	if err != nil {
		t.Fatalf("Execute refusal: %v", err)
	}
	if disp2.Result.Status != bridge.OutcomeFailed {
		t.Fatalf("a refusal intent must win over the override: Status = %q, want %q", disp2.Result.Status, bridge.OutcomeFailed)
	}
}

// TestFakeCapabilityAuthor_PollUnsupported: the synchronous adapter never
// returns Pending, so a routed Poll is a wiring bug — a clear error, not a
// silent zero Dispatch.
func TestFakeCapabilityAuthor_PollUnsupported(t *testing.T) {
	t.Parallel()
	a := bridge.NewFakeCapabilityAuthor()
	if _, err := a.Poll(context.Background(), "ref"); err == nil {
		t.Fatalf("Poll on a synchronous adapter must error")
	}
}
