package lens

import (
	"testing"

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These cover the LensSpec → Rule conversion (translateSpec) for the D1.3
// read-path-authorization postgres fields: protected/public/grantTable/columns.

func protectedSpec(t *testing.T, cfg map[string]any) *LensSpec {
	base := map[string]any{
		"dsn":   "postgres://localhost/test",
		"table": "read_lease_applications",
		"key":   []string{"application_id"},
	}
	for k, v := range cfg {
		base[k] = v
	}
	return &LensSpec{
		ID:           "pg-protected",
		TargetType:   "postgres",
		CypherRule:   "MATCH (a:application) RETURN a.id AS application_id",
		TargetConfig: mustJSON(t, base),
	}
}

func TestTranslateSpec_Protected(t *testing.T) {
	r, err := translateSpec(protectedSpec(t, map[string]any{
		"protected": true,
		"columns": []map[string]any{
			{"name": "status", "type": "text"},
			{"name": "tags", "type": "text[]"},
		},
	}))
	require.NoError(t, err)
	assert.True(t, r.Into.Protected)
	assert.False(t, r.Into.Public)
	assert.False(t, r.Into.GrantTable)
	require.Len(t, r.Into.Columns, 2)
	assert.Equal(t, adapter.ColumnDef{Name: "status", Type: "text"}, r.Into.Columns[0])
	// authz_anchors is always an array column; a declared text[] body column joins it.
	assert.Equal(t, []string{adapter.AuthzAnchorsColumn, "tags"}, r.Into.ArrayColumns)
}

func TestTranslateSpec_Public(t *testing.T) {
	r, err := translateSpec(protectedSpec(t, map[string]any{"public": true}))
	require.NoError(t, err)
	assert.True(t, r.Into.Public)
	assert.False(t, r.Into.Protected)
	assert.Nil(t, r.Into.ArrayColumns, "a public (non-protected) lens declares no array columns")
}

func TestTranslateSpec_ProtectedAndPublic_Rejected(t *testing.T) {
	_, err := translateSpec(protectedSpec(t, map[string]any{"protected": true, "public": true}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both protected and public")
}

func TestTranslateSpec_ProtectedSoftDelete_Rejected(t *testing.T) {
	// A protected RLS table has no is_deleted/deleted_at column and the §6.14
	// policy does not filter it, so deleteMode "soft" would loop on every delete.
	_, err := translateSpec(protectedSpec(t, map[string]any{"protected": true, "deleteMode": "soft"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deleteMode")
	assert.Contains(t, err.Error(), "soft")
}

func TestTranslateSpec_ProtectedHardDelete_OK(t *testing.T) {
	// The default (hard) delete is the supported posture for a protected model.
	r, err := translateSpec(protectedSpec(t, map[string]any{"protected": true, "deleteMode": "hard"}))
	require.NoError(t, err)
	assert.True(t, r.Into.Protected)
	assert.Equal(t, string(adapter.DeleteModeHard), r.Into.DeleteMode)
}

func TestTranslateSpec_PublicSoftDelete_OK(t *testing.T) {
	// A non-protected (public) postgres lens may still use soft delete — only the
	// protected RLS table lacks the is_deleted column.
	r, err := translateSpec(protectedSpec(t, map[string]any{"public": true, "deleteMode": "soft"}))
	require.NoError(t, err)
	assert.False(t, r.Into.Protected)
	assert.Equal(t, string(adapter.DeleteModeSoft), r.Into.DeleteMode)
}

func TestTranslateSpec_GrantTable_Defaults(t *testing.T) {
	// A grant lens may omit table + key — both default from the platform.
	spec := &LensSpec{
		ID:         "cap-read-residence",
		TargetType: "postgres",
		CypherRule: "MATCH (i:identity) RETURN i.id AS actor_id, i.id AS anchor_id, 'cap-read.residence' AS grant_source",
		TargetConfig: mustJSON(t, map[string]any{
			"dsn":        "postgres://localhost/test",
			"grantTable": true,
		}),
	}
	r, err := translateSpec(spec)
	require.NoError(t, err)
	assert.True(t, r.Into.GrantTable)
	assert.Equal(t, adapter.GrantTable, r.Into.Table)
	assert.Equal(t, KeyField(adapter.GrantKeyColumns), r.Into.Key)
	assert.False(t, r.Into.Protected)
}

func TestTranslateSpec_GrantTable_AndProtected_Rejected(t *testing.T) {
	_, err := translateSpec(protectedSpec(t, map[string]any{"protected": true, "grantTable": true}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "grant-table lens is not a protected")
}

func TestTranslateSpec_GrantTable_AndPublic_Rejected(t *testing.T) {
	_, err := translateSpec(protectedSpec(t, map[string]any{"public": true, "grantTable": true}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "grant-table lens is not a public")
}

func TestTranslateSpec_Protected_BadColumn_Rejected(t *testing.T) {
	_, err := translateSpec(protectedSpec(t, map[string]any{
		"protected": true,
		"columns":   []map[string]any{{"name": "status"}}, // missing type
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name and type")
}

func TestTranslateSpec_Protected_OnNATSKV_Rejected(t *testing.T) {
	// A protected read model must target postgres — RLS is the enforcement
	// boundary and NATS-KV has no row-level guard. Honoring protected:true on a
	// NATS-KV target (or silently dropping it) would world-publish a model the
	// author believed was access-controlled, so it fails closed at activation.
	spec := &LensSpec{
		ID:         "kv-protected",
		TargetType: "nats_kv",
		CypherRule: "MATCH (a:application) RETURN a.id AS application_id",
		TargetConfig: mustJSON(t, map[string]any{
			"bucket":    "read-lease-applications",
			"key":       []string{"application_id"},
			"protected": true,
		}),
	}
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must target postgres")
}

func TestTranslateSpec_GrantTable_OnNATSKV_Rejected(t *testing.T) {
	// A grant lens projects to the shared Postgres actor_read_grants table; on a
	// NATS-KV target the grant flag is meaningless and would scatter the
	// read-auth source of truth onto a regular bucket. Fails closed.
	spec := &LensSpec{
		ID:         "kv-grant",
		TargetType: "nats_kv",
		CypherRule: "MATCH (i:identity) RETURN i.id AS actor_id, i.id AS anchor_id",
		TargetConfig: mustJSON(t, map[string]any{
			"bucket":     "actor-read-grants",
			"key":        []string{"actor_id"},
			"grantTable": true,
		}),
	}
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must target postgres")
}

func TestTranslateSpec_PlainNATSKV_Unaffected(t *testing.T) {
	// The existing path: a plain (public/operational) NATS-KV read model that
	// sets none of the read-path-authz flags is untouched by the guard.
	spec := &LensSpec{
		ID:         "kv-plain",
		TargetType: "nats_kv",
		CypherRule: "MATCH (l:listing) RETURN l.id AS listing_id",
		TargetConfig: mustJSON(t, map[string]any{
			"bucket": "listings-index",
			"key":    []string{"listing_id"},
		}),
	}
	r, err := translateSpec(spec)
	require.NoError(t, err)
	assert.Equal(t, "nats_kv", r.Into.Target)
	assert.Equal(t, "listings-index", r.Into.Bucket)
}

func TestTranslateSpec_NeitherProtectedPublicNorGrantTable_Rejected(t *testing.T) {
	// A postgres lens declaring none of the read-path flags is protected by
	// default (Contract #6 §6.14) — undeclared posture fails closed rather than
	// silently activating as a plain unguarded table.
	_, err := translateSpec(protectedSpec(t, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must declare protected, public, or grantTable")
}

func TestTranslateSpec_EmptyDSN_ResolvesFromEnv(t *testing.T) {
	// A package declares posture, not a connection string: an empty DSN resolves
	// from REFRACTOR_PG_DSN at activation (mirroring the bootstrap contract_view
	// lens), so the package manifest never carries a deployment DSN.
	t.Setenv("REFRACTOR_PG_DSN", "postgres://resolved-host/db")
	spec := &LensSpec{
		ID:         "pg-no-dsn",
		TargetType: "postgres",
		CypherRule: "MATCH (a:application) RETURN a.id AS application_id",
		TargetConfig: mustJSON(t, map[string]any{
			"dsn":    "",
			"table":  "read_lease_applications",
			"key":    []string{"application_id"},
			"public": true,
		}),
	}
	r, err := translateSpec(spec)
	require.NoError(t, err)
	assert.Equal(t, "postgres://resolved-host/db", r.Into.DSN)
}

func TestTranslateSpec_DeclaredDSN_OverridesEnv(t *testing.T) {
	// An explicitly declared DSN is honored verbatim — env is only the fallback.
	t.Setenv("REFRACTOR_PG_DSN", "postgres://env-host/db")
	r, err := translateSpec(protectedSpec(t, map[string]any{"public": true}))
	require.NoError(t, err)
	assert.Equal(t, "postgres://localhost/test", r.Into.DSN)
}

func TestTranslateSpec_EmptyDSN_NoEnv_Rejected(t *testing.T) {
	// Fail closed: an empty DSN with REFRACTOR_PG_DSN unset is a hard error, not a
	// silent fallback to a dev default.
	t.Setenv("REFRACTOR_PG_DSN", "")
	spec := &LensSpec{
		ID:         "pg-no-dsn-no-env",
		TargetType: "postgres",
		CypherRule: "MATCH (a:application) RETURN a.id AS application_id",
		TargetConfig: mustJSON(t, map[string]any{
			"dsn":   "",
			"table": "read_lease_applications",
			"key":   []string{"application_id"},
		}),
	}
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dsn")
}
