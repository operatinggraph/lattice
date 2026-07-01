package adapter

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── GrantWriterAdapter unit (no database) ────────────────────────────────────

func TestNewGrantWriterAdapter_NilWriter(t *testing.T) {
	_, err := NewGrantWriterAdapter(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "writer must not be nil")
}

func TestGrantKeyFields(t *testing.T) {
	ok := map[string]any{"actor_id": "A", "anchor_id": "X", "grant_source": "cap-read.test"}
	a, x, s, err := grantKeyFields(ok)
	require.NoError(t, err)
	assert.Equal(t, "A", a)
	assert.Equal(t, "X", x)
	assert.Equal(t, "cap-read.test", s)

	cases := []struct {
		name string
		keys map[string]any
		sub  string
	}{
		{"missing actor", map[string]any{"anchor_id": "X", "grant_source": "s"}, `key "actor_id" absent`},
		{"missing anchor", map[string]any{"actor_id": "A", "grant_source": "s"}, `key "anchor_id" absent`},
		{"missing source", map[string]any{"actor_id": "A", "anchor_id": "X"}, `key "grant_source" absent`},
		{"non-string", map[string]any{"actor_id": 7, "anchor_id": "X", "grant_source": "s"}, "must be a string"},
		{"empty", map[string]any{"actor_id": "", "anchor_id": "X", "grant_source": "s"}, "must not be empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, err := grantKeyFields(c.keys)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.sub)
		})
	}
}

// ── ProtectedAdapter array-encoding unit (no database) ───────────────────────

func TestNewProtectedAdapter_NilInner(t *testing.T) {
	_, err := NewProtectedAdapter(nil, []string{"authz_anchors"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inner must not be nil")
}

func TestProtectedAdapter_EncodeArrays(t *testing.T) {
	pa := &ProtectedAdapter{arrayCols: map[string]struct{}{"authz_anchors": {}}}

	// []any of strings → []string; non-array column untouched.
	out, err := pa.encodeArrays(map[string]any{
		"authz_anchors": []any{"n1", "n2"},
		"status":        "submitted",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"n1", "n2"}, out["authz_anchors"])
	assert.Equal(t, "submitted", out["status"])

	// nil array value → empty slice (a row with no anchors → RLS denies it).
	out, err = pa.encodeArrays(map[string]any{"authz_anchors": nil})
	require.NoError(t, err)
	assert.Equal(t, []string{}, out["authz_anchors"])

	// already []string → passthrough.
	out, err = pa.encodeArrays(map[string]any{"authz_anchors": []string{"a"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, out["authz_anchors"])

	// non-string element → fail-closed error.
	_, err = pa.encodeArrays(map[string]any{"authz_anchors": []any{"ok", 7}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "element 1 must be a string")

	// non-list value for an array column → error.
	_, err = pa.encodeArrays(map[string]any{"authz_anchors": "not-a-list"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a list")
}

func TestProtectedAdapter_NoArrayCols_Passthrough(t *testing.T) {
	pa := &ProtectedAdapter{arrayCols: map[string]struct{}{}}
	in := map[string]any{"authz_anchors": []any{"n1"}}
	out, err := pa.encodeArrays(in)
	require.NoError(t, err)
	// With no declared array columns the row is returned as-is (same map).
	assert.Equal(t, in["authz_anchors"], out["authz_anchors"])
}

// ── Integration: the full read-path seam (require real Postgres) ─────────────

// TestReadPathSeam_Integration proves the Fire-1b platform seam end-to-end with
// the REAL adapters: the GrantWriterAdapter populates actor_read_grants under
// the seq-guard, the ProtectedAdapter writes a protected row's authz_anchors as
// text[], and RLS then shows the row only to a granted actor.
func TestReadPathSeam_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	const role = "rls_test_reader"
	suffix := sanitize(t.Name())
	tbl := "rls_seam_" + suffix
	actor := "actor_" + suffix
	other := "actor_other_" + suffix
	anchor := "anchor_" + suffix

	_, err = pool.Exec(ctx, `DO $$ BEGIN
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='`+role+`') THEN CREATE ROLE `+role+` NOLOGIN; END IF;
	END $$;`)
	require.NoError(t, err)

	clean := func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DROP TABLE IF EXISTS "`+tbl+`"`)
		_, _ = pool.Exec(bg, `DELETE FROM "actor_read_grants" WHERE actor_id = ANY($1)`, []string{actor, other})
	}
	clean()
	t.Cleanup(clean)

	// Provision the grant table + the protected business table from the spec.
	gw, err := NewPostgresGrantWriter(pool, 10*time.Second)
	require.NoError(t, err)
	require.NoError(t, gw.Provision(ctx))
	require.NoError(t, ProvisionProtectedTable(ctx, pool, tbl,
		[]string{"id"}, []ColumnDef{{Name: "status", Type: "text"}}, 10*time.Second))

	_, err = pool.Exec(ctx, fmt.Sprintf(`GRANT SELECT ON "%s","actor_read_grants" TO %s`, tbl, role))
	require.NoError(t, err)

	// Write a protected row via the ProtectedAdapter with authz_anchors as the
	// engine's native []any list — it must land as text[].
	base, err := NewPostgresAdapter(pool, tbl, []string{"id"}, 10*time.Second, DeleteModeHard)
	require.NoError(t, err)
	pa, err := NewProtectedAdapter(base, []string{AuthzAnchorsColumn}, []ColumnDef{{Name: "status", Type: "text"}})
	require.NoError(t, err)
	require.NoError(t, pa.Upsert(ctx,
		map[string]any{"id": "row1"},
		map[string]any{"status": "submitted", "authz_anchors": []any{anchor}, "projection_seq": int64(1)},
		1))

	// Confirm the column really is text[] (unnest works) — not jsonb.
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT count(*) FROM "%s", unnest(authz_anchors) a WHERE a=$1`, tbl), anchor).Scan(&n))
	assert.Equal(t, 1, n, "authz_anchors stored as a text[] array")

	visibleAs := func(actorID string) int {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = tx.Exec(ctx, "SET LOCAL ROLE "+role)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID)
		require.NoError(t, err)
		var c int
		require.NoError(t, tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM "%s"`, tbl)).Scan(&c))
		return c
	}

	// No grant yet → denied for both.
	assert.Equal(t, 0, visibleAs(actor))

	// Grant the actor the row's anchor via the GrantWriterAdapter (the pipeline
	// path: Upsert keyed by actor_id/anchor_id/grant_source).
	ga, err := NewGrantWriterAdapter(gw)
	require.NoError(t, err)
	require.NoError(t, ga.Upsert(ctx,
		map[string]any{"actor_id": actor, "anchor_id": anchor, "grant_source": "cap-read.test"},
		nil, 5))

	assert.Equal(t, 1, visibleAs(actor), "granted actor sees the row")
	assert.Equal(t, 0, visibleAs(other), "a different actor still sees nothing")

	// Revoke via the adapter's Delete → tombstoned, seq-guarded.
	require.NoError(t, ga.Delete(ctx,
		map[string]any{"actor_id": actor, "anchor_id": anchor, "grant_source": "cap-read.test"}, 10))
	assert.Equal(t, 0, visibleAs(actor), "revoked actor sees nothing")

	// A stale re-grant through the adapter cannot resurrect (seq 5 < 10).
	require.NoError(t, ga.Upsert(ctx,
		map[string]any{"actor_id": actor, "anchor_id": anchor, "grant_source": "cap-read.test"}, nil, 5))
	assert.Equal(t, 0, visibleAs(actor), "stale re-grant via adapter does not resurrect")

	// ProvisionProtectedTable is idempotent (re-run at every activation).
	require.NoError(t, ProvisionProtectedTable(ctx, pool, tbl,
		[]string{"id"}, []ColumnDef{{Name: "status", Type: "text"}}, 10*time.Second))
}

// TestProtectedAdapter_SeqGuard_Integration proves NewProtectedAdapter enables
// the same monotonic projection_seq guard PostgresGrantWriter.UpsertGrant
// applies to actor_read_grants (design doc "Fire 2 subsumed" / seq-guard): a
// stale (lower-seq) replay must leave a fresher projected row untouched.
func TestProtectedAdapter_SeqGuard_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	tbl := "rls_seqguard_" + sanitize(t.Name())
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS "`+tbl+`"`)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) })
	require.NoError(t, ProvisionProtectedTable(ctx, pool, tbl,
		[]string{"id"}, []ColumnDef{{Name: "status", Type: "text"}}, 10*time.Second))

	base, err := NewPostgresAdapter(pool, tbl, []string{"id"}, 10*time.Second, DeleteModeHard)
	require.NoError(t, err)
	pa, err := NewProtectedAdapter(base, []string{AuthzAnchorsColumn}, []ColumnDef{{Name: "status", Type: "text"}})
	require.NoError(t, err)
	require.True(t, pa.Guarded(), "NewProtectedAdapter must enable the seq guard")

	readStatus := func() (status string, seq int64) {
		require.NoError(t, pool.QueryRow(ctx,
			fmt.Sprintf(`SELECT status, projection_seq FROM "%s" WHERE id=$1`, tbl), "row1").Scan(&status, &seq))
		return
	}

	// Fresh write at seq 5 → live.
	require.NoError(t, pa.Upsert(ctx,
		map[string]any{"id": "row1"},
		map[string]any{"status": "submitted", "authz_anchors": []any{}}, 5))
	status, seq := readStatus()
	assert.Equal(t, "submitted", status)
	assert.EqualValues(t, 5, seq)

	// A stale (lower-seq) replay must not overwrite the fresher row.
	require.NoError(t, pa.Upsert(ctx,
		map[string]any{"id": "row1"},
		map[string]any{"status": "STALE", "authz_anchors": []any{}}, 3))
	status, seq = readStatus()
	assert.Equal(t, "submitted", status, "stale upsert must not overwrite a fresher row")
	assert.EqualValues(t, 5, seq, "stale upsert must not regress projection_seq")

	// A strictly-newer write DOES apply (monotonic forward progress).
	require.NoError(t, pa.Upsert(ctx,
		map[string]any{"id": "row1"},
		map[string]any{"status": "approved", "authz_anchors": []any{}}, 9))
	status, seq = readStatus()
	assert.Equal(t, "approved", status)
	assert.EqualValues(t, 9, seq)
}
