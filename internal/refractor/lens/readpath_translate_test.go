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

func TestTranslateSpec_NonProtected_NoProvisioning(t *testing.T) {
	// A plain postgres lens (the existing path) carries none of the read-path flags.
	r, err := translateSpec(protectedSpec(t, nil))
	require.NoError(t, err)
	assert.False(t, r.Into.Protected)
	assert.False(t, r.Into.Public)
	assert.False(t, r.Into.GrantTable)
	assert.Nil(t, r.Into.Columns)
	assert.Nil(t, r.Into.ArrayColumns)
}
