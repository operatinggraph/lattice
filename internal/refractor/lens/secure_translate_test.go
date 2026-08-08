package lens

// LensSpec → Rule conversion coverage for the Secure-Lens secureColumns
// declaration (Contract #3 §3.10): decrypted PII may only land in a
// protected postgres model, every secure column must be a declared column,
// and any other posture fails closed at spec-load time.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func secureSpec(t *testing.T, cfg map[string]any) *LensSpec {
	base := map[string]any{
		"dsn":       "postgres://localhost/test",
		"table":     "read_secure_roster",
		"key":       []string{"identity_id"},
		"protected": true,
		"columns": []map[string]any{
			{"name": "name", "type": "text"},
			{"name": "identity_key", "type": "text"},
		},
		"secureColumns": []map[string]any{
			{"column": "name", "holderTypes": []any{"identity"}, "field": "value"},
		},
	}
	for k, v := range cfg {
		if v == nil {
			delete(base, k)
			continue
		}
		base[k] = v
	}
	return &LensSpec{
		ID:           "pg-secure",
		TargetType:   "postgres",
		CypherRule:   "MATCH (i:identity) RETURN i.key AS identity_id, i.key AS identity_key, i.name.data AS name",
		TargetConfig: mustJSON(t, base),
	}
}

func TestTranslateSpec_SecureColumns_Threaded(t *testing.T) {
	r, err := translateSpec(secureSpec(t, nil))
	require.NoError(t, err)
	require.Len(t, r.Into.SecureColumns, 1)
	assert.Equal(t, SecureColumn{Column: "name", HolderTypes: []string{"identity"}, Field: "value"}, r.Into.SecureColumns[0])
	assert.True(t, r.Into.Protected)
}

func TestTranslateSpec_SecureColumns_RequireProtected(t *testing.T) {
	_, err := translateSpec(secureSpec(t, map[string]any{"protected": nil}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protected")
}

func TestTranslateSpec_SecureColumns_PublicRejected(t *testing.T) {
	_, err := translateSpec(secureSpec(t, map[string]any{"protected": nil, "public": true}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protected")
}

func TestTranslateSpec_SecureColumns_GrantTableRejected(t *testing.T) {
	_, err := translateSpec(secureSpec(t, map[string]any{"grantTable": true}))
	require.Error(t, err)
}

func TestTranslateSpec_SecureColumns_ActorAggregateRejected(t *testing.T) {
	spec := secureSpec(t, nil)
	spec.ProjectionKind = "actorAggregate"
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plain projection")
}

func TestTranslateSpec_SecureColumns_UndeclaredColumnRejected(t *testing.T) {
	_, err := translateSpec(secureSpec(t, map[string]any{
		"secureColumns": []map[string]any{
			{"column": "ssn", "holderTypes": []any{"identity"}},
		},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not among the declared")
}

func TestTranslateSpec_SecureColumns_MissingHolderTypesRejected(t *testing.T) {
	_, err := translateSpec(secureSpec(t, map[string]any{
		"secureColumns": []map[string]any{
			{"column": "name"},
		},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "holderTypes")
}

func TestTranslateSpec_SecureColumns_DuplicateRejected(t *testing.T) {
	_, err := translateSpec(secureSpec(t, map[string]any{
		"secureColumns": []map[string]any{
			{"column": "name", "holderTypes": []any{"identity"}},
			{"column": "name", "holderTypes": []any{"identity"}, "field": "value"},
		},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "twice")
}

func TestTranslateSpec_SecureColumns_ReservedRLSColumnRejected(t *testing.T) {
	_, err := translateSpec(secureSpec(t, map[string]any{
		"columns": []map[string]any{
			{"name": "authz_anchors", "type": "text[]"},
			{"name": "identity_key", "type": "text"},
		},
		"secureColumns": []map[string]any{
			{"column": "authz_anchors", "holderTypes": []any{"identity"}},
		},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "platform RLS column")
}

func TestTranslateSpec_SecureColumns_KeyColumnRejected(t *testing.T) {
	_, err := translateSpec(secureSpec(t, map[string]any{
		"columns": []map[string]any{
			{"name": "identity_id", "type": "text"},
			{"name": "identity_key", "type": "text"},
		},
		"secureColumns": []map[string]any{
			{"column": "identity_id", "holderTypes": []any{"identity"}},
		},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "output-key column")
}

// A secure lens needs no column carrying its holder's key. Custody comes from
// the ciphertext, so a lens projecting the ciphertext column alone is valid —
// this is the whole class of "the declared column's RETURN alias was never
// projected" runtime failure that resolving custody from keyId removed.
func TestTranslateSpec_SecureColumns_NoHolderKeyColumnNeeded(t *testing.T) {
	r, err := translateSpec(secureSpec(t, map[string]any{
		"columns": []map[string]any{
			{"name": "name", "type": "text"},
		},
		"secureColumns": []map[string]any{
			{"column": "name", "holderTypes": []any{"identity"}},
		},
	}))
	require.NoError(t, err)
	require.Len(t, r.Into.SecureColumns, 1)
}

// A holder type that is not a Contract #1 type segment can never match a key
// holder, so the column would refuse every ciphertext it was given. Refuse the
// declaration instead of shipping a lens that projects null forever.
func TestTranslateSpec_SecureColumns_InvalidHolderTypeRejected(t *testing.T) {
	_, err := translateSpec(secureSpec(t, map[string]any{
		"secureColumns": []map[string]any{
			{"column": "name", "holderTypes": []any{"retentionClass"}},
		},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vertex type segment")
}

func TestTranslateSpec_SecureColumns_DuplicateHolderTypeRejected(t *testing.T) {
	_, err := translateSpec(secureSpec(t, map[string]any{
		"secureColumns": []map[string]any{
			{"column": "name", "holderTypes": []any{"identity", "identity"}},
		},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "twice")
}

func TestTranslateSpec_SecureColumns_NATSKVRejected(t *testing.T) {
	spec := &LensSpec{
		ID:         "kv-secure",
		TargetType: "nats_kv",
		CypherRule: "MATCH (i:identity) RETURN i.key AS key, i.name.data AS name",
		TargetConfig: mustJSON(t, map[string]any{
			"bucket": "roster",
			"key":    []string{"key"},
			"secureColumns": []map[string]any{
				{"column": "name", "holderTypes": []any{"identity"}},
			},
		}),
	}
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RLS")
}
