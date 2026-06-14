package adapter

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPostgresAdapter_NilPool(t *testing.T) {
	_, err := NewPostgresAdapter(nil, "my_table", []string{"id"}, 0, DeleteModeHard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pool")
}

func TestNewPostgresAdapter_EmptyTable(t *testing.T) {
	// We need a non-nil pool; use pgxpool.New with a fake DSN (lazy connection).
	pool, err := pgxpool.New(context.Background(), "host=fake user=test")
	require.NoError(t, err)
	defer pool.Close()

	_, err = NewPostgresAdapter(pool, "", []string{"id"}, 0, DeleteModeHard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "table")
}

func TestNewPostgresAdapter_EmptyKeyOrder(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "host=fake user=test")
	require.NoError(t, err)
	defer pool.Close()

	_, err = NewPostgresAdapter(pool, "my_table", nil, 0, DeleteModeHard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keyOrder")
}

func TestNewPostgresAdapter_Valid(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "host=fake user=test")
	require.NoError(t, err)
	defer pool.Close()

	a, err := NewPostgresAdapter(pool, "my_table", []string{"id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)
	assert.NotNil(t, a)
}

func TestPostgresAdapter_Close_IsNoOp(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "host=fake user=test")
	require.NoError(t, err)
	defer pool.Close()

	a, err := NewPostgresAdapter(pool, "my_table", []string{"id"}, 0, DeleteModeHard)
	require.NoError(t, err)

	assert.NoError(t, a.Close(), "Close must be a no-op and return nil")
}

func TestPostgresAdapter_WithTimeout_AppliesDeadline(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "host=fake user=test")
	require.NoError(t, err)
	defer pool.Close()

	timeout := 5 * time.Second
	a, err := NewPostgresAdapter(pool, "my_table", []string{"id"}, timeout, DeleteModeHard)
	require.NoError(t, err)

	ctx := context.Background()
	wrapped, cancel := a.withTimeout(ctx)
	defer cancel()

	deadline, ok := wrapped.Deadline()
	assert.True(t, ok, "wrapped context must have a deadline when queryTimeout > 0")
	assert.WithinDuration(t, time.Now().Add(timeout), deadline, time.Second)
}

func TestPostgresAdapter_WithTimeout_NoDeadlineWhenZero(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "host=fake user=test")
	require.NoError(t, err)
	defer pool.Close()

	a, err := NewPostgresAdapter(pool, "my_table", []string{"id"}, 0, DeleteModeHard)
	require.NoError(t, err)

	ctx := context.Background()
	wrapped, cancel := a.withTimeout(ctx)
	defer cancel()

	_, ok := wrapped.Deadline()
	assert.False(t, ok, "context must have no deadline when queryTimeout is 0")
}

// ── buildUpsertSQL unit tests (no real Postgres needed) ──────────────────────

func newTestAdapter(t *testing.T, table string, keyOrder []string) *PostgresAdapter {
	return newTestAdapterMode(t, table, keyOrder, DeleteModeHard)
}

func newTestAdapterMode(t *testing.T, table string, keyOrder []string, mode DeleteMode) *PostgresAdapter {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "host=fake user=test")
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	a, err := NewPostgresAdapter(pool, table, keyOrder, 0, mode)
	require.NoError(t, err)
	return a
}

func TestBuildUpsertSQL_SingleKey(t *testing.T) {
	a := newTestAdapter(t, "occupancy_view", []string{"agreement_id"})

	sql, args, err := a.buildUpsertSQL(
		map[string]any{"agreement_id": "abc"},
		map[string]any{"party_name": "Acme"},
	)
	require.NoError(t, err)
	assert.Equal(t,
		`INSERT INTO "occupancy_view" ("agreement_id", "party_name") VALUES ($1, $2) ON CONFLICT ("agreement_id") DO UPDATE SET "party_name" = EXCLUDED."party_name"`,
		sql,
	)
	assert.Equal(t, []any{"abc", "Acme"}, args)
}

func TestBuildUpsertSQL_CompositeKey(t *testing.T) {
	a := newTestAdapter(t, "occupancy_view", []string{"team_id", "agreement_id"})

	sql, args, err := a.buildUpsertSQL(
		map[string]any{"team_id": "t1", "agreement_id": "abc"},
		map[string]any{"party_name": "Acme"},
	)
	require.NoError(t, err)
	assert.Equal(t,
		`INSERT INTO "occupancy_view" ("team_id", "agreement_id", "party_name") VALUES ($1, $2, $3) ON CONFLICT ("team_id", "agreement_id") DO UPDATE SET "party_name" = EXCLUDED."party_name"`,
		sql,
	)
	assert.Equal(t, []any{"t1", "abc", "Acme"}, args)
}

func TestBuildUpsertSQL_MultipleNonKeyColumns_Deterministic(t *testing.T) {
	a := newTestAdapter(t, "t", []string{"id"})

	// Call twice with the same map contents — map iteration is random.
	// Both calls must produce identical SQL (alphabetical non-key col order).
	keys := map[string]any{"id": 1}
	row := map[string]any{"zzz": "last", "aaa": "first", "mmm": "middle"}

	sql1, args1, err := a.buildUpsertSQL(keys, row)
	require.NoError(t, err)
	sql2, args2, err := a.buildUpsertSQL(keys, row)
	require.NoError(t, err)

	assert.Equal(t, sql1, sql2, "SQL must be identical on repeated calls")
	assert.Equal(t, args1, args2, "args must be identical on repeated calls")

	// Non-key columns must appear alphabetically (quoted): "aaa", "mmm", "zzz".
	assert.Contains(t, sql1, `"aaa", "mmm", "zzz"`)
	assert.Equal(t, []any{1, "first", "middle", "last"}, args1)
}

func TestBuildUpsertSQL_KeyOnlyRow_DoNothing(t *testing.T) {
	a := newTestAdapter(t, "events", []string{"event_id"})

	sql, args, err := a.buildUpsertSQL(
		map[string]any{"event_id": "e1"},
		map[string]any{}, // no non-key columns
	)
	require.NoError(t, err)
	assert.Contains(t, sql, "DO NOTHING")
	assert.NotContains(t, sql, "DO UPDATE")
	assert.Equal(t, []any{"e1"}, args)
}

func TestBuildUpsertSQL_MissingKeyField(t *testing.T) {
	a := newTestAdapter(t, "t", []string{"id", "tenant"})

	_, _, err := a.buildUpsertSQL(
		map[string]any{"id": 1}, // "tenant" absent
		map[string]any{"name": "x"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant")
}

func TestBuildUpsertSQL_TableNameDoubleQuoted(t *testing.T) {
	// Reserved-word table name "order" must be double-quoted in generated SQL.
	a := newTestAdapter(t, "order", []string{"id"})

	sql, _, err := a.buildUpsertSQL(
		map[string]any{"id": 1},
		map[string]any{"qty": 5},
	)
	require.NoError(t, err)
	assert.Contains(t, sql, `"order"`)
}

func TestBuildUpsertSQL_KeyRowOverlap_KeyColumnsFilteredFromRow(t *testing.T) {
	// If a key column also appears in row, it must be silently ignored in the
	// non-key section — no duplicate column, no error.
	a := newTestAdapter(t, "t", []string{"id"})

	sql, args, err := a.buildUpsertSQL(
		map[string]any{"id": 42},
		map[string]any{"id": 99, "name": "Alice"}, // "id" overlaps with keyOrder
	)
	require.NoError(t, err)
	// "id" must appear exactly once in the column list.
	assert.Equal(t,
		`INSERT INTO "t" ("id", "name") VALUES ($1, $2) ON CONFLICT ("id") DO UPDATE SET "name" = EXCLUDED."name"`,
		sql,
	)
	// args must use the key-map value (42), not the row-map value (99).
	assert.Equal(t, []any{42, "Alice"}, args)
}

func TestNewPostgresAdapter_DuplicateKeyOrder(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "host=fake user=test")
	require.NoError(t, err)
	defer pool.Close()

	_, err = NewPostgresAdapter(pool, "t", []string{"id", "id"}, 0, DeleteModeHard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestNewPostgresAdapter_TableNameWithDoubleQuote(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "host=fake user=test")
	require.NoError(t, err)
	defer pool.Close()

	_, err = NewPostgresAdapter(pool, `bad"name`, []string{"id"}, 0, DeleteModeHard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "double-quote")
}

// TestPostgresAdapter_Probe_RequiresPostgres tests a real Probe call.
// Skipped unless POSTGRES_TEST_DSN is set and -short is not active.
func TestPostgresAdapter_Probe_RequiresPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Postgres integration test in short mode")
	}
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("skipping: POSTGRES_TEST_DSN not set")
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	a, err := NewPostgresAdapter(pool, "any_table", []string{"id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)

	err = a.Probe(context.Background())
	assert.NoError(t, err, "Probe against real Postgres must succeed")
}

// ── Upsert integration tests (require real Postgres) ─────────────────────────

func skipIfNoPostgres(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping Postgres integration test in short mode")
	}
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("skipping: POSTGRES_TEST_DSN not set")
	}
	return dsn
}

func TestPostgresAdapter_Upsert_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()

	// Create a temp table for this test.
	_, err = pool.Exec(ctx, `CREATE TEMP TABLE upsert_test (id TEXT PRIMARY KEY, name TEXT)`)
	require.NoError(t, err)

	a, err := NewPostgresAdapter(pool, "upsert_test", []string{"id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)

	// First upsert — inserts the row.
	err = a.Upsert(ctx, map[string]any{"id": "abc"}, map[string]any{"name": "Acme"}, 0)
	require.NoError(t, err)

	// Second upsert with same key — updates, not duplicates.
	err = a.Upsert(ctx, map[string]any{"id": "abc"}, map[string]any{"name": "Acme Corp"}, 0)
	require.NoError(t, err)

	// Exactly one row must exist with the latest value.
	var count int
	var name string
	err = pool.QueryRow(ctx, `SELECT COUNT(*), MAX(name) FROM upsert_test WHERE id = 'abc'`).Scan(&count, &name)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "exactly one row must exist after two upserts")
	assert.Equal(t, "Acme Corp", name, "row must reflect the latest upserted value")
}

func TestPostgresAdapter_Upsert_CompositeKey_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()

	_, err = pool.Exec(ctx, `CREATE TEMP TABLE upsert_composite (team_id TEXT, agreement_id TEXT, party_name TEXT, PRIMARY KEY (team_id, agreement_id))`)
	require.NoError(t, err)

	a, err := NewPostgresAdapter(pool, "upsert_composite", []string{"team_id", "agreement_id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)

	// Insert.
	err = a.Upsert(ctx,
		map[string]any{"team_id": "t1", "agreement_id": "a1"},
		map[string]any{"party_name": "Acme"},
		0,
	)
	require.NoError(t, err)

	// Update same composite key.
	err = a.Upsert(ctx,
		map[string]any{"team_id": "t1", "agreement_id": "a1"},
		map[string]any{"party_name": "Acme Corp"},
		0,
	)
	require.NoError(t, err)

	var count int
	var name string
	err = pool.QueryRow(ctx, `SELECT COUNT(*), MAX(party_name) FROM upsert_composite WHERE team_id='t1' AND agreement_id='a1'`).Scan(&count, &name)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "exactly one row after composite-key upsert")
	assert.Equal(t, "Acme Corp", name)
}

func TestPostgresAdapter_Upsert_MissingTable_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	a, err := NewPostgresAdapter(pool, "table_does_not_exist_xyz", []string{"id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)

	err = a.Upsert(context.Background(), map[string]any{"id": "x"}, map[string]any{"val": "y"}, 0)
	assert.Error(t, err, "upsert into non-existent table must return an error")
}

// ── buildDeleteSQL unit tests (no real Postgres needed) ──────────────────────

func TestBuildDeleteSQL_SingleKey_Hard(t *testing.T) {
	a := newTestAdapterMode(t, "occupancy_view", []string{"agreement_id"}, DeleteModeHard)

	sql, args, err := a.buildDeleteSQL(map[string]any{"agreement_id": "abc"})
	require.NoError(t, err)
	assert.Equal(t,
		`DELETE FROM "occupancy_view" WHERE "agreement_id" = $1`,
		sql,
	)
	assert.Equal(t, []any{"abc"}, args)
}

func TestBuildDeleteSQL_SingleKey_Soft(t *testing.T) {
	a := newTestAdapterMode(t, "occupancy_view", []string{"agreement_id"}, DeleteModeSoft)

	sql, args, err := a.buildDeleteSQL(map[string]any{"agreement_id": "abc"})
	require.NoError(t, err)
	assert.Equal(t,
		`UPDATE "occupancy_view" SET is_deleted=true, deleted_at=NOW() WHERE "agreement_id" = $1`,
		sql,
	)
	assert.Equal(t, []any{"abc"}, args)
}

func TestBuildDeleteSQL_CompositeKey_Hard(t *testing.T) {
	a := newTestAdapterMode(t, "occupancy_view", []string{"team_id", "agreement_id"}, DeleteModeHard)

	sql, args, err := a.buildDeleteSQL(map[string]any{"team_id": "t1", "agreement_id": "abc"})
	require.NoError(t, err)
	assert.Equal(t,
		`DELETE FROM "occupancy_view" WHERE "team_id" = $1 AND "agreement_id" = $2`,
		sql,
	)
	assert.Equal(t, []any{"t1", "abc"}, args)
}

func TestBuildDeleteSQL_CompositeKey_Soft(t *testing.T) {
	a := newTestAdapterMode(t, "occupancy_view", []string{"team_id", "agreement_id"}, DeleteModeSoft)

	sql, args, err := a.buildDeleteSQL(map[string]any{"team_id": "t1", "agreement_id": "abc"})
	require.NoError(t, err)
	assert.Equal(t,
		`UPDATE "occupancy_view" SET is_deleted=true, deleted_at=NOW() WHERE "team_id" = $1 AND "agreement_id" = $2`,
		sql,
	)
	assert.Equal(t, []any{"t1", "abc"}, args)
}

func TestBuildDeleteSQL_MissingKeyField(t *testing.T) {
	a := newTestAdapter(t, "t", []string{"id", "tenant"})

	_, _, err := a.buildDeleteSQL(map[string]any{"id": 1}) // "tenant" absent
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant")
}

func TestBuildDeleteSQL_ColumnNamesQuoted(t *testing.T) {
	// Reserved-word column name must be double-quoted.
	a := newTestAdapter(t, "t", []string{"order"})

	sql, _, err := a.buildDeleteSQL(map[string]any{"order": 99})
	require.NoError(t, err)
	assert.Contains(t, sql, `"order"`)
}

// ── Delete integration tests (require real Postgres) ─────────────────────────

func TestPostgresAdapter_Delete_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()

	_, err = pool.Exec(ctx, `CREATE TEMP TABLE delete_test (id TEXT PRIMARY KEY, name TEXT)`)
	require.NoError(t, err)

	// Insert a row directly.
	_, err = pool.Exec(ctx, `INSERT INTO delete_test VALUES ('abc', 'Acme')`)
	require.NoError(t, err)

	a, err := NewPostgresAdapter(pool, "delete_test", []string{"id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)

	// Delete the row.
	err = a.Delete(ctx, map[string]any{"id": "abc"}, 0)
	require.NoError(t, err)

	// Verify it is gone.
	var count int
	err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM delete_test WHERE id = 'abc'`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "row must be gone after Delete")
}

func TestPostgresAdapter_Delete_Idempotent_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()

	_, err = pool.Exec(ctx, `CREATE TEMP TABLE delete_idempotent_test (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)

	a, err := NewPostgresAdapter(pool, "delete_idempotent_test", []string{"id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)

	// Delete a row that was never inserted — must return nil (idempotent, NFR2).
	err = a.Delete(ctx, map[string]any{"id": "nonexistent"}, 0)
	assert.NoError(t, err, "deleting a non-existent row must be a no-error no-op")
}

func TestPostgresAdapter_Delete_CompositeKey_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()

	_, err = pool.Exec(ctx, `CREATE TEMP TABLE delete_composite_test (team_id TEXT, agreement_id TEXT, PRIMARY KEY (team_id, agreement_id))`)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `INSERT INTO delete_composite_test VALUES ('t1', 'a1')`)
	require.NoError(t, err)

	a, err := NewPostgresAdapter(pool, "delete_composite_test", []string{"team_id", "agreement_id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)

	err = a.Delete(ctx, map[string]any{"team_id": "t1", "agreement_id": "a1"}, 0)
	require.NoError(t, err)

	var count int
	err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM delete_composite_test WHERE team_id='t1' AND agreement_id='a1'`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "composite-key row must be gone after Delete")
}

func TestPostgresAdapter_Delete_Soft_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()

	// Soft-delete targets must carry is_deleted + deleted_at columns.
	_, err = pool.Exec(ctx, `CREATE TEMP TABLE delete_soft_test (id TEXT PRIMARY KEY, name TEXT, is_deleted BOOLEAN DEFAULT false, deleted_at TIMESTAMPTZ)`)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `INSERT INTO delete_soft_test (id, name) VALUES ('abc', 'Acme')`)
	require.NoError(t, err)

	a, err := NewPostgresAdapter(pool, "delete_soft_test", []string{"id"}, 5*time.Second, DeleteModeSoft)
	require.NoError(t, err)

	err = a.Delete(ctx, map[string]any{"id": "abc"}, 0)
	require.NoError(t, err)

	// Row must still exist with is_deleted=true (tombstone retained).
	var isDeleted bool
	err = pool.QueryRow(ctx, `SELECT is_deleted FROM delete_soft_test WHERE id = 'abc'`).Scan(&isDeleted)
	require.NoError(t, err)
	assert.True(t, isDeleted, "soft-delete must retain the row with is_deleted=true")
}

// ── coerceForPgx unit tests (no real Postgres needed) ────────────────────────

func TestCoerceForPgx_StringPassThrough(t *testing.T) {
	input := "hello"
	result := coerceForPgx(input)
	assert.Equal(t, input, result, "string must pass through unchanged")
}

func TestCoerceForPgx_Float64PassThrough(t *testing.T) {
	input := float64(3.14)
	result := coerceForPgx(input)
	assert.Equal(t, input, result, "float64 must pass through unchanged")
}

func TestCoerceForPgx_BoolPassThrough(t *testing.T) {
	input := true
	result := coerceForPgx(input)
	assert.Equal(t, input, result, "bool must pass through unchanged")
}

func TestCoerceForPgx_NilPassThrough(t *testing.T) {
	result := coerceForPgx(nil)
	assert.Nil(t, result, "nil must return nil (pgx encodes as NULL)")
}

func TestCoerceForPgx_SliceBecomesJSONRawMessage(t *testing.T) {
	input := []any{1, "two", true}
	result := coerceForPgx(input)
	raw, ok := result.(json.RawMessage)
	require.True(t, ok, "[]any must become json.RawMessage")
	assert.True(t, json.Valid(raw), "result must be valid JSON")

	var decoded []any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Len(t, decoded, 3)
}

func TestCoerceForPgx_MapBecomesJSONRawMessage(t *testing.T) {
	input := map[string]any{"a": 1, "b": "two"}
	result := coerceForPgx(input)
	raw, ok := result.(json.RawMessage)
	require.True(t, ok, "map[string]any must become json.RawMessage")
	assert.True(t, json.Valid(raw), "result must be valid JSON")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Len(t, decoded, 2)
}

// ── JSONB integration test (requires real Postgres) ───────────────────────────

func TestPostgresAdapter_Upsert_JSONBColumn_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()

	_, err = pool.Exec(ctx, `CREATE TEMP TABLE jsonb_test (id TEXT PRIMARY KEY, tags JSONB, meta JSONB)`)
	require.NoError(t, err)

	a, err := NewPostgresAdapter(pool, "jsonb_test", []string{"id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)

	tagsInput := []any{"alpha", "beta", "gamma"}
	metaInput := map[string]any{"source": "test", "version": float64(2)}

	err = a.Upsert(ctx,
		map[string]any{"id": "row1"},
		map[string]any{"tags": tagsInput, "meta": metaInput},
		0,
	)
	require.NoError(t, err, "upsert with JSONB columns must succeed")

	// Scan back as raw JSON strings and verify round-trip integrity.
	var tagsJSON, metaJSON string
	err = pool.QueryRow(ctx, `SELECT tags::text, meta::text FROM jsonb_test WHERE id = 'row1'`).Scan(&tagsJSON, &metaJSON)
	require.NoError(t, err)

	var tagsDecoded []any
	require.NoError(t, json.Unmarshal([]byte(tagsJSON), &tagsDecoded))
	assert.Len(t, tagsDecoded, 3, "tags array must have 3 elements")

	var metaDecoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(metaJSON), &metaDecoded))
	assert.Equal(t, "test", metaDecoded["source"], "meta.source must round-trip correctly")
	assert.Equal(t, float64(2), metaDecoded["version"], "meta.version must round-trip correctly")
}
