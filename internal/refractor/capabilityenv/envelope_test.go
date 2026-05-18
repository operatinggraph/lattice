// Unit tests for the capabilityenv wrapper (Story 4.4 — pendingReview injection).
//
// Tests:
//  1. TestWrapper_PendingReview_FlaggedForReview  — stateReader returns "flagged-for-review"
//     → envelope must contain pendingReview: true.
//  2. TestWrapper_PendingReview_Absent_WhenUnclaimed — stateReader returns "unclaimed"
//     → envelope must NOT contain a pendingReview key.
//  3. TestWrapper_PendingReview_NilStateReader — stateReader is nil
//     → envelope must NOT contain pendingReview (backward-compatible path).
package capabilityenv

import (
	"testing"

	"github.com/asolgan/lattice/internal/refractor/pipeline"
)

// makeRow builds a minimal RETURN row as produced by the capability lens cypher.
func makeRow(actorKey string) map[string]any {
	return map[string]any{
		"actorKey":            actorKey,
		"platformPermissions": []any{},
		"serviceAccess":       []any{},
		"ephemeralGrants":     []any{},
		"roles":               []any{},
	}
}

func makeParams() map[string]any {
	return map[string]any{"projectedAt": "2026-05-17T00:00:00Z"}
}

// Valid 20-char NanoID-alphabet keys used in tests.
// Alphabet: ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789
const (
	testFlaggedActorID  = "ABCDEFGHJKLMNPQRSTUa" // 20 valid chars
	testUnclaimedActorID = "ABCDEFGHJKLMNPQRSTUb"
	testNilReaderActorID = "ABCDEFGHJKLMNPQRSTUc"
	testRoleActorID      = "ABCDEFGHJKLMNPQRSTUd"
)

// TestWrapper_PendingReview_FlaggedForReview: when stateReader returns
// "flagged-for-review", the envelope must carry pendingReview: true.
func TestWrapper_PendingReview_FlaggedForReview(t *testing.T) {
	actorKey := "vtx.identity." + testFlaggedActorID

	stateReader := func(k string) string {
		if k == actorKey {
			return "flagged-for-review"
		}
		return ""
	}

	fn := NewWrapper("vtx.meta.test-lens", func(string) uint64 { return 0 }, stateReader)
	env, _, err := fn(makeRow(actorKey), map[string]any{}, makeParams())
	if err != nil {
		t.Fatalf("wrapper returned error: %v", err)
	}
	if env == nil {
		t.Fatal("wrapper returned nil envelope")
	}
	pending, hasPending := env["pendingReview"]
	if !hasPending {
		t.Fatal("envelope missing pendingReview field for flagged-for-review identity")
	}
	if pending != true {
		t.Fatalf("pendingReview = %v, want true", pending)
	}
}

// TestWrapper_PendingReview_Absent_WhenUnclaimed: when stateReader returns
// "unclaimed", pendingReview must be absent (not false, not zero — absent).
func TestWrapper_PendingReview_Absent_WhenUnclaimed(t *testing.T) {
	actorKey := "vtx.identity." + testUnclaimedActorID

	stateReader := func(k string) string { return "unclaimed" }

	fn := NewWrapper("vtx.meta.test-lens", func(string) uint64 { return 0 }, stateReader)
	env, _, err := fn(makeRow(actorKey), map[string]any{}, makeParams())
	if err != nil {
		t.Fatalf("wrapper returned error: %v", err)
	}
	if _, hasPending := env["pendingReview"]; hasPending {
		t.Fatalf("pendingReview must be absent for unclaimed identity, got %v", env["pendingReview"])
	}
}

// TestWrapper_PendingReview_NilStateReader: when stateReader is nil (pre-4.4
// call sites), pendingReview must be absent — backward compatibility.
func TestWrapper_PendingReview_NilStateReader(t *testing.T) {
	actorKey := "vtx.identity." + testNilReaderActorID

	fn := NewWrapper("vtx.meta.test-lens", func(string) uint64 { return 0 }, nil)
	env, _, err := fn(makeRow(actorKey), map[string]any{}, makeParams())
	if err != nil {
		t.Fatalf("wrapper returned error: %v", err)
	}
	if _, hasPending := env["pendingReview"]; hasPending {
		t.Fatalf("pendingReview must be absent when stateReader is nil, got %v", env["pendingReview"])
	}
}

// TestWrapper_SkipsNonIdentityActorKey: a non-identity actorKey must be dropped
// (ErrSkipProjection), not processed.
func TestWrapper_SkipsNonIdentityActorKey(t *testing.T) {
	actorKey := "vtx.role." + testRoleActorID

	fn := NewWrapper("vtx.meta.test-lens", func(string) uint64 { return 0 }, nil)
	row := map[string]any{
		"actorKey":            actorKey,
		"platformPermissions": []any{},
	}
	_, _, err := fn(row, map[string]any{}, makeParams())
	if err != pipeline.ErrSkipProjection {
		t.Fatalf("expected ErrSkipProjection for non-identity actorKey, got %v", err)
	}
}
