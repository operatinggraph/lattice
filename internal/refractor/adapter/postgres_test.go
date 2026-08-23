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
		0,
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
		0,
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

	sql1, args1, err := a.buildUpsertSQL(keys, row, 0)
	require.NoError(t, err)
	sql2, args2, err := a.buildUpsertSQL(keys, row, 0)
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
		0,
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
		0,
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
		0,
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
		0,
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

// ── guarded-mode buildUpsertSQL unit tests ────────────────────────────────

func TestBuildUpsertSQL_Guarded_AppendsProjectionSeqGuard(t *testing.T) {
	a := newTestAdapter(t, "read_lease_applications", []string{"lease_id"})
	a.SetGuarded(true)
	require.True(t, a.Guarded())

	sql, args, err := a.buildUpsertSQL(
		map[string]any{"lease_id": "abc"},
		map[string]any{"status": "submitted"},
		42,
	)
	require.NoError(t, err)
	assert.Equal(t,
		`INSERT INTO "read_lease_applications" ("lease_id", "status", "projection_seq") VALUES ($1, $2, $3) ON CONFLICT ("lease_id") DO UPDATE SET "status" = EXCLUDED."status", "projection_seq" = EXCLUDED."projection_seq", "is_deleted" = false WHERE EXCLUDED."projection_seq" > "read_lease_applications"."projection_seq"`,
		sql,
	)
	assert.Equal(t, []any{"abc", "submitted", int64(42)}, args)
}

func TestBuildUpsertSQL_Guarded_KeyOnlyRow_StillGuardsNotDoNothing(t *testing.T) {
	// Unguarded key-only rows use DO NOTHING (no non-key columns to guard);
	// a guarded adapter must still attach the projection_seq guard even with
	// no business columns, since DO NOTHING would silently drop the seq write.
	a := newTestAdapter(t, "t", []string{"id"})
	a.SetGuarded(true)

	sql, args, err := a.buildUpsertSQL(
		map[string]any{"id": "e1"},
		map[string]any{},
		7,
	)
	require.NoError(t, err)
	assert.NotContains(t, sql, "DO NOTHING")
	assert.Contains(t, sql, `DO UPDATE SET "projection_seq" = EXCLUDED."projection_seq", "is_deleted" = false WHERE`)
	assert.Equal(t, []any{"e1", int64(7)}, args)
}

func TestBuildUpsertSQL_Guarded_RowSuppliedProjectionSeqIgnored(t *testing.T) {
	// projection_seq is platform-owned; a lens-declared "projection_seq" key in
	// row must never collide with (or override) the guard's own column.
	a := newTestAdapter(t, "t", []string{"id"})
	a.SetGuarded(true)

	sql, args, err := a.buildUpsertSQL(
		map[string]any{"id": "e1"},
		map[string]any{"projection_seq": int64(999), "name": "x"},
		7,
	)
	require.NoError(t, err)
	assert.Equal(t,
		`INSERT INTO "t" ("id", "name", "projection_seq") VALUES ($1, $2, $3) ON CONFLICT ("id") DO UPDATE SET "name" = EXCLUDED."name", "projection_seq" = EXCLUDED."projection_seq", "is_deleted" = false WHERE EXCLUDED."projection_seq" > "t"."projection_seq"`,
		sql,
	)
	assert.Equal(t, []any{"e1", "x", int64(7)}, args)
}

// TestBuildUpsertSQL_Guarded_RevivesTombstone proves a guarded Upsert always
// resets IsDeletedColumn to false: a live re-projection at a higher seq must
// revive a row a prior guarded Delete tombstoned (mirrors
// PostgresGrantWriter.UpsertGrant's is_deleted=false on conflict) — otherwise
// the row would stay invisible under the RLS policy's "NOT is_deleted" clause
// forever, even after a legitimate re-create.
func TestBuildUpsertSQL_Guarded_RevivesTombstone(t *testing.T) {
	a := newTestAdapter(t, "t", []string{"id"})
	a.SetGuarded(true)

	sql, _, err := a.buildUpsertSQL(map[string]any{"id": "e1"}, map[string]any{"name": "x"}, 7)
	require.NoError(t, err)
	assert.Contains(t, sql, `"is_deleted" = false`)
}

// TestBuildUpsertSQL_Guarded_RowSuppliedTombstoneColumnsIgnored proves a
// lens-declared row value under IsDeletedColumn/DeletedAtColumn never collides
// with (or overrides) the platform-owned tombstone columns — mirroring the
// ProjectionSeqColumn collision guard above.
func TestBuildUpsertSQL_Guarded_RowSuppliedTombstoneColumnsIgnored(t *testing.T) {
	a := newTestAdapter(t, "t", []string{"id"})
	a.SetGuarded(true)

	sql, args, err := a.buildUpsertSQL(
		map[string]any{"id": "e1"},
		map[string]any{"is_deleted": true, "deleted_at": "bogus", "name": "x"},
		7,
	)
	require.NoError(t, err)
	assert.Equal(t,
		`INSERT INTO "t" ("id", "name", "projection_seq") VALUES ($1, $2, $3) ON CONFLICT ("id") DO UPDATE SET "name" = EXCLUDED."name", "projection_seq" = EXCLUDED."projection_seq", "is_deleted" = false WHERE EXCLUDED."projection_seq" > "t"."projection_seq"`,
		sql,
	)
	assert.Equal(t, []any{"e1", "x", int64(7)}, args)
}

func TestPostgresAdapter_Upsert_UnguardedIgnoresProjectionSeq(t *testing.T) {
	// Regression: an ordinary (unguarded) adapter must keep Contract #6 §6.2's
	// unconditional last-writer-wins behavior — no guard clause, no matter what
	// projectionSeq the pipeline passes.
	a := newTestAdapter(t, "t", []string{"id"})
	require.False(t, a.Guarded())

	sql, _, err := a.buildUpsertSQL(map[string]any{"id": "e1"}, map[string]any{"name": "x"}, 999)
	require.NoError(t, err)
	assert.NotContains(t, sql, "projection_seq")
	assert.NotContains(t, sql, "WHERE")
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

	sql, args, err := a.buildDeleteSQL(map[string]any{"agreement_id": "abc"}, 0)
	require.NoError(t, err)
	assert.Equal(t,
		`DELETE FROM "occupancy_view" WHERE "agreement_id" = $1`,
		sql,
	)
	assert.Equal(t, []any{"abc"}, args)
}

func TestBuildDeleteSQL_SingleKey_Soft(t *testing.T) {
	a := newTestAdapterMode(t, "occupancy_view", []string{"agreement_id"}, DeleteModeSoft)

	sql, args, err := a.buildDeleteSQL(map[string]any{"agreement_id": "abc"}, 0)
	require.NoError(t, err)
	assert.Equal(t,
		`UPDATE "occupancy_view" SET is_deleted=true, deleted_at=NOW() WHERE "agreement_id" = $1`,
		sql,
	)
	assert.Equal(t, []any{"abc"}, args)
}

func TestBuildDeleteSQL_CompositeKey_Hard(t *testing.T) {
	a := newTestAdapterMode(t, "occupancy_view", []string{"team_id", "agreement_id"}, DeleteModeHard)

	sql, args, err := a.buildDeleteSQL(map[string]any{"team_id": "t1", "agreement_id": "abc"}, 0)
	require.NoError(t, err)
	assert.Equal(t,
		`DELETE FROM "occupancy_view" WHERE "team_id" = $1 AND "agreement_id" = $2`,
		sql,
	)
	assert.Equal(t, []any{"t1", "abc"}, args)
}

func TestBuildDeleteSQL_CompositeKey_Soft(t *testing.T) {
	a := newTestAdapterMode(t, "occupancy_view", []string{"team_id", "agreement_id"}, DeleteModeSoft)

	sql, args, err := a.buildDeleteSQL(map[string]any{"team_id": "t1", "agreement_id": "abc"}, 0)
	require.NoError(t, err)
	assert.Equal(t,
		`UPDATE "occupancy_view" SET is_deleted=true, deleted_at=NOW() WHERE "team_id" = $1 AND "agreement_id" = $2`,
		sql,
	)
	assert.Equal(t, []any{"t1", "abc"}, args)
}

// TestBuildDeleteSQL_Guarded_SeqGuardedSoftTombstone proves a guarded (protected
// read-model) adapter's Delete is ALWAYS a seq-guarded soft tombstone —
// regardless of deleteMode — so ProjectionSeqColumn's watermark survives a
// delete the way it survives an Upsert: a later stale replay's ON CONFLICT
// guard still has a row to compare its projectionSeq against, instead of
// hard-DELETE discarding the watermark and letting the replay resurrect the
// row unconditionally (Contract #6 §6.14).
func TestBuildDeleteSQL_Guarded_SeqGuardedSoftTombstone(t *testing.T) {
	a := newTestAdapter(t, "read_lease_applications", []string{"lease_id"})
	a.SetGuarded(true)

	sql, args, err := a.buildDeleteSQL(map[string]any{"lease_id": "abc"}, 42)
	require.NoError(t, err)
	assert.Equal(t,
		`UPDATE "read_lease_applications" SET is_deleted=true, deleted_at=NOW(), projection_seq=$2 WHERE "lease_id" = $1 AND projection_seq < $2`,
		sql,
	)
	assert.Equal(t, []any{"abc", int64(42)}, args)
}

// TestBuildDeleteSQL_Guarded_IgnoresDeclaredDeleteMode proves the guarded path
// overrides deleteMode entirely — even an adapter constructed with
// DeleteModeSoft still emits the seq-guarded tombstone (with projection_seq
// bumped), never the unguarded plain soft-delete UPDATE.
func TestBuildDeleteSQL_Guarded_IgnoresDeclaredDeleteMode(t *testing.T) {
	a := newTestAdapterMode(t, "t", []string{"id"}, DeleteModeSoft)
	a.SetGuarded(true)

	sql, _, err := a.buildDeleteSQL(map[string]any{"id": "x"}, 5)
	require.NoError(t, err)
	assert.Contains(t, sql, "projection_seq=$2")
	assert.Contains(t, sql, "AND projection_seq < $2")
}

// ── buildListKeysSQL / ListKeys unit tests (no real Postgres needed) ────────

func TestBuildListKeysSQL_SingleKey_Hard(t *testing.T) {
	a := newTestAdapterMode(t, "occupancy_view", []string{"agreement_id"}, DeleteModeHard)
	assert.Equal(t, `SELECT "agreement_id" FROM "occupancy_view"`, a.buildListKeysSQL())
}

func TestBuildListKeysSQL_CompositeKey_Hard(t *testing.T) {
	a := newTestAdapterMode(t, "read_landlord_lease_applications", []string{"app_id", "landlord_id"}, DeleteModeHard)
	assert.Equal(t,
		`SELECT "app_id", "landlord_id" FROM "read_landlord_lease_applications"`,
		a.buildListKeysSQL(),
	)
}

func TestBuildListKeysSQL_Soft_ExcludesDeleted(t *testing.T) {
	a := newTestAdapterMode(t, "occupancy_view", []string{"agreement_id"}, DeleteModeSoft)
	assert.Equal(t,
		`SELECT "agreement_id" FROM "occupancy_view" WHERE NOT "is_deleted"`,
		a.buildListKeysSQL(),
	)
}

// TestBuildListKeysSQL_Guarded_ExcludesDeleted proves a guarded (protected)
// adapter excludes tombstoned rows from ListKeys even though its declared
// deleteMode is the default DeleteModeHard — the guarded Delete path always
// soft-tombstones (buildDeleteSQL), so "live" must be derived from a.guarded,
// not from deleteMode alone.
func TestBuildListKeysSQL_Guarded_ExcludesDeleted(t *testing.T) {
	a := newTestAdapter(t, "read_lease_applications", []string{"lease_id"})
	a.SetGuarded(true)
	assert.Equal(t,
		`SELECT "lease_id" FROM "read_lease_applications" WHERE NOT "is_deleted"`,
		a.buildListKeysSQL(),
	)
}

func TestPostgresAdapter_ListKeys_CompositeKey_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()
	_, err = pool.Exec(ctx, `CREATE TEMP TABLE listkeys_composite (app_id TEXT, landlord_id TEXT, name TEXT, PRIMARY KEY (app_id, landlord_id))`)
	require.NoError(t, err)

	a, err := NewPostgresAdapter(pool, "listkeys_composite", []string{"app_id", "landlord_id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)

	require.NoError(t, a.Upsert(ctx, map[string]any{"app_id": "app1", "landlord_id": "lord1"}, map[string]any{"name": "A"}, 0))
	require.NoError(t, a.Upsert(ctx, map[string]any{"app_id": "app2", "landlord_id": "lord2"}, map[string]any{"name": "B"}, 0))

	got, err := a.ListKeys(ctx)
	require.NoError(t, err)
	want := []map[string]any{
		{"app_id": "app1", "landlord_id": "lord1"},
		{"app_id": "app2", "landlord_id": "lord2"},
	}
	assert.ElementsMatch(t, want, got)

	// Deleting one row must drop it from ListKeys.
	require.NoError(t, a.Delete(ctx, map[string]any{"app_id": "app1", "landlord_id": "lord1"}, 0))
	got, err = a.ListKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, []map[string]any{{"app_id": "app2", "landlord_id": "lord2"}}, got)
}

func TestPostgresAdapter_ListKeys_Soft_ExcludesDeleted_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()
	_, err = pool.Exec(ctx, `CREATE TEMP TABLE listkeys_soft (id TEXT PRIMARY KEY, name TEXT, is_deleted BOOLEAN NOT NULL DEFAULT false, deleted_at TIMESTAMPTZ)`)
	require.NoError(t, err)

	a, err := NewPostgresAdapter(pool, "listkeys_soft", []string{"id"}, 5*time.Second, DeleteModeSoft)
	require.NoError(t, err)

	require.NoError(t, a.Upsert(ctx, map[string]any{"id": "keep"}, map[string]any{"name": "K"}, 0))
	require.NoError(t, a.Upsert(ctx, map[string]any{"id": "gone"}, map[string]any{"name": "G"}, 0))
	require.NoError(t, a.Delete(ctx, map[string]any{"id": "gone"}, 0))

	got, err := a.ListKeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, []map[string]any{{"id": "keep"}}, got)
}

func TestBuildDeleteSQL_MissingKeyField(t *testing.T) {
	a := newTestAdapter(t, "t", []string{"id", "tenant"})

	_, _, err := a.buildDeleteSQL(map[string]any{"id": 1}, 0) // "tenant" absent
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenant")
}

func TestBuildDeleteSQL_ColumnNamesQuoted(t *testing.T) {
	// Reserved-word column name must be double-quoted.
	a := newTestAdapter(t, "t", []string{"order"})

	sql, _, err := a.buildDeleteSQL(map[string]any{"order": 99}, 0)
	require.NoError(t, err)
	assert.Contains(t, sql, `"order"`)
}

// ── buildTruncateSQL unit tests (no real Postgres needed) ────────────────────

func TestBuildTruncateSQL_PlainTable(t *testing.T) {
	a := newTestAdapter(t, "occupancy_view", []string{"agreement_id"})

	assert.Equal(t, `TRUNCATE TABLE "occupancy_view"`, a.buildTruncateSQL())
}

func TestBuildTruncateSQL_TableNameDoubleQuoted(t *testing.T) {
	// Reserved-word table name "order" must be double-quoted in the statement.
	a := newTestAdapter(t, "order", []string{"id"})

	assert.Equal(t, `TRUNCATE TABLE "order"`, a.buildTruncateSQL())
}

func TestPostgresAdapter_SatisfiesTruncater(t *testing.T) {
	a := newTestAdapter(t, "t", []string{"id"})

	_, ok := interface{}(a).(Truncater)
	assert.True(t, ok, "PostgresAdapter must implement adapter.Truncater")
}

// ── Truncate integration test (requires real Postgres) ───────────────────────

func TestPostgresAdapter_Truncate_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()

	_, err = pool.Exec(ctx, `CREATE TEMP TABLE truncate_test (id TEXT PRIMARY KEY, name TEXT)`)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `INSERT INTO truncate_test VALUES ('a', 'Acme'), ('b', 'Beta')`)
	require.NoError(t, err)

	a, err := NewPostgresAdapter(pool, "truncate_test", []string{"id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)

	require.NoError(t, a.Truncate(ctx))

	var count int
	err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM truncate_test`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "all rows must be gone after Truncate")

	// Truncate on an already-empty table is a no-op, not an error.
	assert.NoError(t, a.Truncate(ctx), "truncating an empty table must succeed")
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

// TestPostgresAdapter_GuardedDelete_PreventsStaleReplayResurrection is the
// Contract #6 §6.14 proof that a guarded (protected read-model) table's
// Delete must retain the projection_seq watermark, so a stale replay
// arriving after the delete cannot resurrect the row. A hard DELETE would
// instead discard the watermark, letting the stale INSERT's ON CONFLICT guard
// find no row to compare against and succeed unconditionally.
func TestPostgresAdapter_GuardedDelete_PreventsStaleReplayResurrection(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()

	_, err = pool.Exec(ctx, `CREATE TEMP TABLE guarded_delete_test (
		id TEXT PRIMARY KEY, name TEXT,
		projection_seq BIGINT NOT NULL DEFAULT 0,
		is_deleted BOOLEAN NOT NULL DEFAULT false,
		deleted_at TIMESTAMPTZ)`)
	require.NoError(t, err)

	a, err := NewPostgresAdapter(pool, "guarded_delete_test", []string{"id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)
	a.SetGuarded(true)

	// Live projection at seq 10.
	require.NoError(t, a.Upsert(ctx, map[string]any{"id": "abc"}, map[string]any{"name": "Acme"}, 10))

	// Delete at seq 20 — must retain the row as a tombstone, not remove it.
	require.NoError(t, a.Delete(ctx, map[string]any{"id": "abc"}, 20))

	var isDeleted bool
	var seq int64
	err = pool.QueryRow(ctx, `SELECT is_deleted, projection_seq FROM guarded_delete_test WHERE id = 'abc'`).Scan(&isDeleted, &seq)
	require.NoError(t, err, "the row must still exist after a guarded delete (tombstone, not removal)")
	assert.True(t, isDeleted)
	assert.Equal(t, int64(20), seq)

	// A stale CDC replay for the pre-delete seq (15, between the upsert and the
	// delete) arrives after the delete. Without the retained watermark, this
	// INSERT ... ON CONFLICT would find no row to guard against and resurrect
	// the entity unconditionally.
	require.NoError(t, a.Upsert(ctx, map[string]any{"id": "abc"}, map[string]any{"name": "STALE"}, 15))

	err = pool.QueryRow(ctx, `SELECT is_deleted, projection_seq FROM guarded_delete_test WHERE id = 'abc'`).Scan(&isDeleted, &seq)
	require.NoError(t, err)
	assert.True(t, isDeleted, "a stale replay (seq 15 < stored seq 20) must not revive the tombstone")
	assert.Equal(t, int64(20), seq, "a stale replay must not move the watermark backward")

	// A fresh, later projection (seq 30) legitimately revives the row.
	require.NoError(t, a.Upsert(ctx, map[string]any{"id": "abc"}, map[string]any{"name": "Acme Again"}, 30))

	var name string
	err = pool.QueryRow(ctx, `SELECT name, is_deleted, projection_seq FROM guarded_delete_test WHERE id = 'abc'`).Scan(&name, &isDeleted, &seq)
	require.NoError(t, err)
	assert.False(t, isDeleted, "a fresh higher-seq upsert must revive the tombstoned row")
	assert.Equal(t, "Acme Again", name)
	assert.Equal(t, int64(30), seq)
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
