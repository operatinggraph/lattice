// Unit tests for the capabilityenv wrapper.
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
	testIdentityActorID = "ABCDEFGHJKLMNPQRSTUa"
	testRoleActorID     = "ABCDEFGHJKLMNPQRSTUd"
)

// TestWrapper_BuildsEnvelopeForIdentityActor: happy path — the wrapper
// produces a Contract #6 §6.2 envelope with the expected fields populated
// from the row + lens-def key.
func TestWrapper_BuildsEnvelopeForIdentityActor(t *testing.T) {
	actorKey := "vtx.identity." + testIdentityActorID

	fn := NewWrapper("vtx.meta.test-lens", func(string) uint64 { return 0 })
	env, keys, err := fn(makeRow(actorKey), map[string]any{}, makeParams())
	if err != nil {
		t.Fatalf("wrapper returned error: %v", err)
	}
	if env == nil {
		t.Fatal("wrapper returned nil envelope")
	}
	if got, want := env["actor"], actorKey; got != want {
		t.Errorf("actor = %v, want %v", got, want)
	}
	if got, want := env["version"], Version; got != want {
		t.Errorf("version = %v, want %v", got, want)
	}
	if _, has := env["pendingReview"]; has {
		t.Error("envelope must NOT carry pendingReview after Story 4.6 walk-back")
	}
	if keys["key"] == nil {
		t.Error("keys[\"key\"] is unset")
	}
}

// TestWrapper_SkipsNonIdentityActorKey: a non-identity actorKey must be dropped
// (ErrSkipProjection), not processed.
func TestWrapper_SkipsNonIdentityActorKey(t *testing.T) {
	actorKey := "vtx.role." + testRoleActorID

	fn := NewWrapper("vtx.meta.test-lens", func(string) uint64 { return 0 })
	row := map[string]any{
		"actorKey":            actorKey,
		"platformPermissions": []any{},
	}
	_, _, err := fn(row, map[string]any{}, makeParams())
	if err != pipeline.ErrSkipProjection {
		t.Fatalf("expected ErrSkipProjection for non-identity actorKey, got %v", err)
	}
}
