package lens

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the LensSpec → Rule conversion (translateSpec) delete-mode
// handling for Story 1.5.12: absent → "hard", explicit "soft" preserved, and
// invalid values rejected — for both postgres and nats_kv target shapes.

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestTranslateSpec_Postgres_DeleteMode(t *testing.T) {
	base := func(mode any) *LensSpec {
		cfg := map[string]any{
			"dsn":    "postgres://localhost/test",
			"table":  "agreements",
			"key":    []string{"agreement_id"},
			"public": true,
		}
		if mode != nil {
			cfg["deleteMode"] = mode
		}
		return &LensSpec{
			ID:           "pg-lens",
			TargetType:   "postgres",
			CypherRule:   "MATCH (a:agreement) RETURN a.id AS agreement_id",
			TargetConfig: mustJSON(t, cfg),
		}
	}

	t.Run("absent defaults hard", func(t *testing.T) {
		r, err := translateSpec(base(nil))
		require.NoError(t, err)
		assert.Equal(t, "hard", r.Into.DeleteMode)
	})
	t.Run("soft preserved", func(t *testing.T) {
		r, err := translateSpec(base("soft"))
		require.NoError(t, err)
		assert.Equal(t, "soft", r.Into.DeleteMode)
	})
	t.Run("invalid rejected", func(t *testing.T) {
		_, err := translateSpec(base("bogus"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "deleteMode")
	})
}

func TestTranslateSpec_NatsKV_DeleteMode(t *testing.T) {
	base := func(mode any) *LensSpec {
		cfg := map[string]any{
			"bucket": "agreements",
			"key":    []string{"agreement_id"},
		}
		if mode != nil {
			cfg["deleteMode"] = mode
		}
		return &LensSpec{
			ID:           "kv-lens",
			TargetType:   "nats_kv",
			CypherRule:   "MATCH (a:agreement) RETURN a.id AS agreement_id",
			TargetConfig: mustJSON(t, cfg),
		}
	}

	t.Run("absent defaults hard", func(t *testing.T) {
		r, err := translateSpec(base(nil))
		require.NoError(t, err)
		assert.Equal(t, "hard", r.Into.DeleteMode)
	})
	t.Run("soft preserved", func(t *testing.T) {
		r, err := translateSpec(base("soft"))
		require.NoError(t, err)
		assert.Equal(t, "soft", r.Into.DeleteMode)
	})
	t.Run("invalid rejected", func(t *testing.T) {
		_, err := translateSpec(base("bogus"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "deleteMode")
	})
}
