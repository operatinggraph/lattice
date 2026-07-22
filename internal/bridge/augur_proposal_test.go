package bridge_test

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/bridge"
)

// TestAugurProposal_CodecRoundTrip: a proposal encoded to the Result.Detail JSON
// string decodes back to an equal proposal — the contract between the augur
// adapter (producer) and the RecordProposal reply leg (consumer).
func TestAugurProposal_CodecRoundTrip(t *testing.T) {
	t.Parallel()
	in := bridge.AugurProposal{
		Action:      "assignTask",
		Params:      map[string]any{"scopedTo": "vtx.leaseapp.abc", "forOperation": "ApproveLeaseApplication"},
		Rationale:   "no playbook entry; propose a human approval task",
		Confidence:  0.82,
		Model:       "claude-opus-4-8",
		PromptHash:  "ph",
		CatalogHash: "ch",
		ReasonedAt:  "2026-06-29T00:00:00Z",
	}
	detail, err := in.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out, err := bridge.DecodeAugurProposal(detail)
	if err != nil {
		t.Fatalf("DecodeAugurProposal: %v", err)
	}
	if out.Action != in.Action || out.Confidence != in.Confidence || out.Model != in.Model ||
		out.Rationale != in.Rationale || out.PromptHash != in.PromptHash ||
		out.CatalogHash != in.CatalogHash || out.ReasonedAt != in.ReasonedAt {
		t.Fatalf("round-trip mismatch:\n in = %+v\nout = %+v", in, out)
	}
	if got, ok := out.Params["scopedTo"].(string); !ok || got != "vtx.leaseapp.abc" {
		t.Fatalf("round-trip params lost scopedTo: %#v", out.Params)
	}
	if got, ok := out.Params["forOperation"].(string); !ok || got != "ApproveLeaseApplication" {
		t.Fatalf("round-trip params lost forOperation: %#v", out.Params)
	}
}

// TestDecodeAugurProposal_RejectsBlankAndMalformed: a blank or unparseable Detail
// is a LOUD error, never a silently-empty proposal (which would mask a real
// reasoning failure as a benign invalid-action proposal).
func TestDecodeAugurProposal_RejectsBlankAndMalformed(t *testing.T) {
	t.Parallel()
	if _, err := bridge.DecodeAugurProposal(""); err == nil {
		t.Fatalf("blank detail must be a loud error")
	}
	if _, err := bridge.DecodeAugurProposal("{not json"); err == nil {
		t.Fatalf("malformed JSON must be a loud error")
	}
}
