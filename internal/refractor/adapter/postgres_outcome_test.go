package adapter

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPostgresAdapter_UpsertOutcome_FromRowCount pins the mapping from the row
// count one upsert statement reported onto the outcome the pipeline's audit
// gate reads.
//
// The guarded statement declines by matching no row — `ON CONFLICT DO UPDATE …
// WHERE EXCLUDED.projection_seq > <table>.projection_seq` — and returns no
// error, so the row count is the only evidence a caller has that the write took
// effect. Reading it wrong means auditing a row the table does not hold.
func TestPostgresAdapter_UpsertOutcome_FromRowCount(t *testing.T) {
	cases := []struct {
		name         string
		guarded      bool
		rowsAffected int64
		wantCommit   bool
		wantDeclined bool
	}{
		{
			name:         "guarded, one row: the watermark let the write through",
			guarded:      true,
			rowsAffected: 1,
			wantCommit:   true,
		},
		{
			name:         "guarded, no rows: the watermark declined, and only the watermark can",
			guarded:      true,
			rowsAffected: 0,
			wantCommit:   false,
			wantDeclined: true,
		},
		{
			name:         "unguarded, one row: an ordinary last-writer-wins upsert",
			rowsAffected: 1,
			wantCommit:   true,
		},
		{
			// The DO NOTHING arm buildUpsertSQL emits for a key-columns-only
			// table: the row is already there and no column exists to change.
			// Nothing was stored, but no guard was consulted either.
			name:         "unguarded, no rows: nothing stored, and no watermark to blame",
			rowsAffected: 0,
			wantCommit:   false,
			wantDeclined: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &PostgresAdapter{guarded: tc.guarded}

			outcome := a.upsertOutcome(tc.rowsAffected)

			assert.Equal(t, tc.wantCommit, outcome.Committed, "Committed")
			assert.Equal(t, tc.wantDeclined, outcome.DeclinedByWatermark, "DeclinedByWatermark")
			assert.Equal(t, tc.wantCommit, outcome.Wrote,
				"this target performs no write at all when it declines, so Wrote cannot outrun Committed")
		})
	}
}

// TestPostgresAdapter_DeleteOutcome_FromRowCount pins the retraction direction,
// where a zero row count means something different under each of the three
// statements buildDeleteSQL emits — so the upsert direction's reading cannot be
// carried over, and neither can the grant table's.
//
// The hard-delete row is the one that must not be copied from the grant writer:
// there an absent row still costs a row (the ON CONFLICT form inserts a
// tombstone), so zero unambiguously means declined. A DELETE FROM on an absent
// key is idempotent success and consults no watermark at all.
func TestPostgresAdapter_DeleteOutcome_FromRowCount(t *testing.T) {
	cases := []struct {
		name         string
		guarded      bool
		mode         DeleteMode
		rowsAffected int64
		wantWrote    bool
		wantDeclined bool
	}{
		{
			name:         "guarded, one row: the tombstone landed",
			guarded:      true,
			rowsAffected: 1,
			wantWrote:    true,
		},
		{
			// Ambiguous at the statement level — an absent row, or a stored
			// watermark at or above the token — and resolved toward the decline:
			// a false alarm an operator can see beats a silent over-grant.
			name:         "guarded, no rows: reported declined, the fail-closed side of the ambiguity",
			guarded:      true,
			rowsAffected: 0,
			wantWrote:    false,
			wantDeclined: true,
		},
		{
			name:         "unguarded soft, one row: the tombstone landed",
			mode:         DeleteModeSoft,
			rowsAffected: 1,
			wantWrote:    true,
		},
		{
			name:         "unguarded soft, no rows: the key matched nothing, and no watermark was consulted",
			mode:         DeleteModeSoft,
			rowsAffected: 0,
			wantWrote:    false,
			wantDeclined: false,
		},
		{
			name:         "unguarded hard, one row: the row was removed",
			mode:         DeleteModeHard,
			rowsAffected: 1,
			wantWrote:    true,
		},
		{
			name:         "unguarded hard, no rows: already absent is idempotent success, NOT a watermark decline",
			mode:         DeleteModeHard,
			rowsAffected: 0,
			wantWrote:    false,
			wantDeclined: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &PostgresAdapter{guarded: tc.guarded, deleteMode: tc.mode}

			outcome := a.deleteOutcome(tc.rowsAffected)

			assert.Equal(t, tc.wantWrote, outcome.Wrote, "Wrote")
			assert.Equal(t, tc.wantDeclined, outcome.DeclinedByWatermark, "DeclinedByWatermark")
		})
	}
}

// TestPostgresAdapter_GuardedUpsertWithOutcome_Integration runs the same
// distinction against a real table, where the row count comes from Postgres
// rather than from the test.
func TestPostgresAdapter_GuardedUpsertWithOutcome_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()

	_, err = pool.Exec(ctx, `CREATE TEMP TABLE guarded_outcome_test (
		id TEXT PRIMARY KEY,
		name TEXT,
		projection_seq BIGINT NOT NULL DEFAULT 0,
		is_deleted BOOLEAN NOT NULL DEFAULT false
	)`)
	require.NoError(t, err)

	a, err := NewPostgresAdapter(pool, "guarded_outcome_test", []string{"id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)
	a.SetGuarded(true)

	keys := map[string]any{"id": "abc"}

	// An absent row: the INSERT lands.
	outcome, err := a.UpsertWithOutcome(ctx, keys, map[string]any{"name": "Acme"}, 10)
	require.NoError(t, err)
	require.True(t, outcome.Committed, "a fresh key must insert")
	require.False(t, outcome.DeclinedByWatermark)

	// A tied token: the ON CONFLICT WHERE clause matches nothing.
	outcome, err = a.UpsertWithOutcome(ctx, keys, map[string]any{"name": "Tied"}, 10)
	require.NoError(t, err, "the guard declines by writing nothing, not by erroring")
	assert.False(t, outcome.Committed, "a tied watermark stores no row")
	assert.True(t, outcome.DeclinedByWatermark)

	// A lower token: same.
	outcome, err = a.UpsertWithOutcome(ctx, keys, map[string]any{"name": "Stale"}, 9)
	require.NoError(t, err)
	assert.False(t, outcome.Committed, "a stale replay stores no row")
	assert.True(t, outcome.DeclinedByWatermark)

	// The declines must have left the stored row alone, or the outcome is
	// reporting something other than what the table did.
	var name string
	require.NoError(t, pool.QueryRow(ctx, `SELECT name FROM guarded_outcome_test WHERE id = 'abc'`).Scan(&name))
	assert.Equal(t, "Acme", name, "no declined write may reach the row")

	// A strictly higher token: the update lands.
	outcome, err = a.UpsertWithOutcome(ctx, keys, map[string]any{"name": "Fresh"}, 11)
	require.NoError(t, err)
	assert.True(t, outcome.Committed, "a token above the stored watermark commits")
	assert.False(t, outcome.DeclinedByWatermark)
	require.NoError(t, pool.QueryRow(ctx, `SELECT name FROM guarded_outcome_test WHERE id = 'abc'`).Scan(&name))
	assert.Equal(t, "Fresh", name)
}

// TestProtectedAdapter_UpsertWithOutcome_ForwardsInsteadOfDefaulting is the
// wrapper half. ProtectedAdapter holds its base adapter as a named field, so an
// optional interface it does not re-declare is simply absent — and writeResults
// then treats every write as having landed. Since NewProtectedAdapter turns the
// monotonic guard ON, that default is wrong for exactly the lenses the guard
// exists for.
func TestProtectedAdapter_UpsertWithOutcome_ForwardsInsteadOfDefaulting(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()

	_, err = pool.Exec(ctx, `CREATE TEMP TABLE protected_outcome_test (
		id TEXT PRIMARY KEY,
		authz_anchors TEXT[],
		projection_seq BIGINT NOT NULL DEFAULT 0,
		is_deleted BOOLEAN NOT NULL DEFAULT false,
		deleted_at TIMESTAMPTZ
	)`)
	require.NoError(t, err)

	inner, err := NewPostgresAdapter(pool, "protected_outcome_test", []string{"id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)
	p, err := NewProtectedAdapter(inner, []string{"authz_anchors"}, nil)
	require.NoError(t, err)
	require.True(t, p.Guarded(), "a protected adapter is guarded by construction")

	keys := map[string]any{"id": "row1"}
	row := map[string]any{"authz_anchors": []any{"Kx3TmZpq7RvwNsY2Hc9L"}}

	outcome, err := p.UpsertWithOutcome(ctx, keys, row, 10)
	require.NoError(t, err)
	require.True(t, outcome.Committed, "the array encoding must still reach the table")

	outcome, err = p.UpsertWithOutcome(ctx, keys, row, 10)
	require.NoError(t, err)
	assert.False(t, outcome.Committed,
		"a tied watermark stores no row, and the wrapper must report that rather than swallow it")
	assert.True(t, outcome.DeclinedByWatermark)

	// The retraction direction through the same wrapper. A guarded delete is a
	// soft tombstone conditioned on projection_seq, so a tied token withdraws
	// nothing — and the row must still be live afterwards, which is the fact a
	// swallowed outcome would hide.
	del, err := p.DeleteWithOutcome(ctx, keys, 10)
	require.NoError(t, err, "the guard declines by writing nothing, not by erroring")
	assert.False(t, del.Wrote, "a declined retraction withdrew nothing")
	assert.True(t, del.DeclinedByWatermark)

	var deleted bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT is_deleted FROM protected_outcome_test WHERE id = 'row1'`).Scan(&deleted))
	require.False(t, deleted, "the row is still live, which is exactly what the outcome must not hide")

	del, err = p.DeleteWithOutcome(ctx, keys, 11)
	require.NoError(t, err)
	assert.True(t, del.Wrote, "a token above the stored watermark withdraws")
	assert.False(t, del.DeclinedByWatermark)
	require.NoError(t, pool.QueryRow(ctx, `SELECT is_deleted FROM protected_outcome_test WHERE id = 'row1'`).Scan(&deleted))
	assert.True(t, deleted, "the committed retraction tombstoned the row")
}

// TestPostgresAdapter_HardDeleteOfAbsentKeyIsNotADecline is the case the grant
// table's reading would get wrong if it were copied across. An unguarded hard
// delete of a key that is not there removed nothing and consulted no watermark,
// so it reports neither a retraction nor a decline.
func TestPostgresAdapter_HardDeleteOfAbsentKeyIsNotADecline(t *testing.T) {
	dsn := skipIfNoPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	ctx := context.Background()
	_, err = pool.Exec(ctx, `CREATE TEMP TABLE hard_delete_outcome_test (id TEXT PRIMARY KEY, name TEXT)`)
	require.NoError(t, err)

	a, err := NewPostgresAdapter(pool, "hard_delete_outcome_test", []string{"id"}, 5*time.Second, DeleteModeHard)
	require.NoError(t, err)

	outcome, err := a.DeleteWithOutcome(ctx, map[string]any{"id": "never-existed"}, 0)
	require.NoError(t, err, "deleting an absent key is idempotent, not an error")
	assert.False(t, outcome.Wrote, "nothing was removed, so nothing was retracted")
	assert.False(t, outcome.DeclinedByWatermark,
		"an absent key is not a watermark conflict — this adapter is unguarded and consulted none")

	require.NoError(t, a.Upsert(ctx, map[string]any{"id": "present"}, map[string]any{"name": "Acme"}, 0))
	outcome, err = a.DeleteWithOutcome(ctx, map[string]any{"id": "present"}, 0)
	require.NoError(t, err)
	assert.True(t, outcome.Wrote, "removing a row that was there is a real retraction")
	assert.False(t, outcome.DeclinedByWatermark)
}
