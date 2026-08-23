package adapter

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// grantOutcomeTestSource is the grant_source the outcome fixtures write under,
// kept distinct from every other test's so a shared dev/demo DSN cannot make one
// test's rows another's.
const grantOutcomeTestSource = "grantOutcomeTest"

// TestGrantOutcome_BothDirectionsFollowTheRowCount pins the mapping from a
// grant statement's "did a row change" answer onto the outcome the pipeline
// reads, in both directions at once.
//
// The retraction direction is the one that matters most and the one that was
// silent: a declined revocation reporting Wrote would tell the platform a
// capability was withdrawn while the RLS policy keeps honouring it. The grant
// direction's declined case only under-delivers access.
func TestGrantOutcome_BothDirectionsFollowTheRowCount(t *testing.T) {
	t.Run("a statement that changed a row committed", func(t *testing.T) {
		up := grantUpsertOutcome(true)
		assert.True(t, up.Committed)
		assert.True(t, up.Wrote)
		assert.False(t, up.DeclinedByWatermark)

		del := grantDeleteOutcome(true)
		assert.True(t, del.Wrote)
		assert.False(t, del.DeclinedByWatermark)
	})

	t.Run("a statement that changed no row was declined, and claims nothing", func(t *testing.T) {
		up := grantUpsertOutcome(false)
		assert.False(t, up.Committed, "no row changed, so no grant landed")
		assert.False(t, up.Wrote,
			"this target writes nothing when it declines, unlike the NATS-KV guard's watermark rewrite")
		assert.True(t, up.DeclinedByWatermark)

		del := grantDeleteOutcome(false)
		assert.False(t, del.Wrote,
			"a declined revocation left the grant live — reporting a retraction here is the over-grant direction")
		assert.True(t, del.DeclinedByWatermark)
	})
}

// TestGrantWriterAdapter_RefusedWriteClaimsNothing covers the outcome forms'
// refusal paths, which need no database because they never reach one: a
// malformed key map, and a row whose grant_source belongs to a different
// producer.
//
// Both must surface a zero outcome beside the error. An outcome struct is read
// by callers that check the error first, but the retraction direction is where
// a stray Wrote:true is a claim that a capability was withdrawn — so the safe
// value is the one that claims nothing, on every exit that wrote nothing.
func TestGrantWriterAdapter_RefusedWriteClaimsNothing(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "host=fake user=test")
	require.NoError(t, err)
	defer pool.Close()
	w, err := NewPostgresGrantWriter(pool, time.Second)
	require.NoError(t, err)
	g, err := NewGrantWriterAdapter(w, grantOutcomeTestSource)
	require.NoError(t, err)

	ctx := context.Background()
	foreign := map[string]any{
		GrantKeyColumns[0]: "Kx3TmZpq7RvwNsY2Hc9L",
		GrantKeyColumns[1]: "Ry7NpTbc2MkwXsZ4Jd8P",
		GrantKeyColumns[2]: "someOtherProducer",
	}

	t.Run("a foreign grant_source is refused, on both directions", func(t *testing.T) {
		up, uerr := g.UpsertWithOutcome(ctx, foreign, nil, 10)
		require.Error(t, uerr)
		assert.False(t, up.Committed)
		assert.False(t, up.Wrote)

		del, derr := g.DeleteWithOutcome(ctx, foreign, 10)
		require.Error(t, derr)
		assert.False(t, del.Wrote, "a refused revocation withdrew nothing")
		assert.False(t, del.DeclinedByWatermark,
			"no watermark was consulted, so naming one would misreport the cause")
	})

	t.Run("a malformed key map is refused, on both directions", func(t *testing.T) {
		up, uerr := g.UpsertWithOutcome(ctx, map[string]any{}, nil, 10)
		require.Error(t, uerr)
		assert.False(t, up.Committed)

		del, derr := g.DeleteWithOutcome(ctx, map[string]any{}, 10)
		require.Error(t, derr)
		assert.False(t, del.Wrote)
	})
}

// TestPostgresGrantWriter_RevokeGrantWithOutcome_Integration is the real-table
// proof of the zero-row reading: on this statement a zero row count means the
// monotonic guard declined, and cannot mean "the grant was already gone",
// because the INSERT arm plants a tombstone whenever the row is absent.
//
// The two cases below are exactly that pair — an absent row, then a tied token —
// so a reading that confused them would have to disagree with one of them.
func TestPostgresGrantWriter_RevokeGrantWithOutcome_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	w := provisionGrantWriter(t, pool)
	const actor = "Kx3TmZpq7RvwNsY2Hc9L"
	const anchor = "Ry7NpTbc2MkwXsZ4Jd8P"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM "actor_read_grants" WHERE actor_id=$1 AND grant_source=$2`, actor, grantOutcomeTestSource)
	})

	// A grant this actor never held: the tombstone is INSERTed, so a row IS
	// touched. This is the case a hard delete would report as "nothing to do".
	committed, err := w.RevokeGrantWithOutcome(ctx, actor, anchor, grantOutcomeTestSource, 10)
	require.NoError(t, err)
	assert.True(t, committed,
		"revoking an absent grant plants its guarding tombstone, which is a row written")

	// A tied token against that stored tombstone: the ON CONFLICT WHERE clause
	// fails and nothing is touched.
	committed, err = w.RevokeGrantWithOutcome(ctx, actor, anchor, grantOutcomeTestSource, 10)
	require.NoError(t, err, "the guard declines by writing nothing, not by erroring")
	assert.False(t, committed, "a tied watermark withdrew nothing")

	// And a stale one.
	committed, err = w.RevokeGrantWithOutcome(ctx, actor, anchor, grantOutcomeTestSource, 9)
	require.NoError(t, err)
	assert.False(t, committed, "a stale replay withdrew nothing")

	// A strictly higher token lands.
	committed, err = w.RevokeGrantWithOutcome(ctx, actor, anchor, grantOutcomeTestSource, 11)
	require.NoError(t, err)
	assert.True(t, committed, "a token above the stored watermark withdraws")

	seq, deleted, found := grantState(t, pool, actor, anchor, grantOutcomeTestSource)
	require.True(t, found, "the row is retained so a stale upsert cannot resurrect the grant")
	assert.Equal(t, int64(11), seq, "only the committed revoke moved the watermark")
	assert.True(t, deleted)
}

// TestGrantWriterAdapter_DeleteWithOutcome_Integration drives the same
// distinction through the adapter the pipeline actually holds, since a correct
// writer reached through a wrapper that drops its report is no better than an
// incorrect one.
func TestGrantWriterAdapter_DeleteWithOutcome_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	w := provisionGrantWriter(t, pool)
	g, err := NewGrantWriterAdapter(w, grantOutcomeTestSource)
	require.NoError(t, err)

	const actor = "Vb5KrQzn8HjtWpX3Lm6Y"
	const anchor = "Zc9WdMfp4TgqNrY7Bk2H"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM "actor_read_grants" WHERE actor_id=$1 AND grant_source=$2`, actor, grantOutcomeTestSource)
	})
	keys := map[string]any{
		GrantKeyColumns[0]: actor,
		GrantKeyColumns[1]: anchor,
		GrantKeyColumns[2]: grantOutcomeTestSource,
	}

	upserted, err := g.UpsertWithOutcome(ctx, keys, nil, 20)
	require.NoError(t, err)
	require.True(t, upserted.Committed, "the grant must be live before revoking it means anything")

	// A tied token: the revocation does NOT take, and the grant stays live.
	outcome, err := g.DeleteWithOutcome(ctx, keys, 20)
	require.NoError(t, err)
	assert.False(t, outcome.Wrote,
		"a revocation the guard declined must never report a retraction — that is the over-grant direction")
	assert.True(t, outcome.DeclinedByWatermark)

	_, deleted, found := grantState(t, pool, actor, anchor, grantOutcomeTestSource)
	require.True(t, found)
	require.False(t, deleted, "the grant is still live, which is exactly what the outcome must not hide")

	// A strictly higher token withdraws it.
	outcome, err = g.DeleteWithOutcome(ctx, keys, 21)
	require.NoError(t, err)
	assert.True(t, outcome.Wrote)
	assert.False(t, outcome.DeclinedByWatermark)

	_, deleted, found = grantState(t, pool, actor, anchor, grantOutcomeTestSource)
	require.True(t, found)
	assert.True(t, deleted, "the committed revocation tombstoned the row")
}

// TestPostgresGrantWriter_RevokeAllGrantsForActorWithOutcome_Integration pins
// the count, and pins the limit of what the count can say. A plain UPDATE's zero
// rows is ambiguous between "the actor held nothing" and "every row is already
// at or above the token" — so the outcome reports a number and claims no verdict.
//
// Under the shred token the ambiguity does not arise, which is the case that
// matters: math.MaxInt64 exceeds every stored projection_seq, so nothing can
// decline and zero means the actor held no rows.
func TestPostgresGrantWriter_RevokeAllGrantsForActorWithOutcome_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	w := provisionGrantWriter(t, pool)
	const actor = "Nq2XvHkd6PbsRtZ8Wj4M"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM "actor_read_grants" WHERE actor_id=$1`, actor)
	})

	// An actor with no rows at all.
	revoked, err := w.RevokeAllGrantsForActorWithOutcome(ctx, actor, math.MaxInt64)
	require.NoError(t, err)
	assert.Equal(t, int64(0), revoked, "no rows held, so no rows withdrawn")

	require.NoError(t, w.UpsertGrant(ctx, actor, "Hs4YtLbn7QmwErX2Kd9V", grantOutcomeTestSource, 5))
	require.NoError(t, w.UpsertGrant(ctx, actor, "Jd6ZuMcp8RnxFsY3Lf7W", grantOutcomeTestSource, 6))

	revoked, err = w.RevokeAllGrantsForActorWithOutcome(ctx, actor, math.MaxInt64)
	require.NoError(t, err)
	assert.Equal(t, int64(2), revoked, "both of the actor's grants were withdrawn")

	// Re-running the shred touches nothing: every row now sits at the token.
	revoked, err = w.RevokeAllGrantsForActorWithOutcome(ctx, actor, math.MaxInt64)
	require.NoError(t, err)
	assert.Equal(t, int64(0), revoked, "a second shred has nothing left below the token")
}

// TestPostgresGrantWriter_RevokeAllGrantsForActorWithOutcome_EmptyActor keeps
// the guard the plain form already had: an empty actor would match no row
// column and is refused rather than silently revoking nothing.
func TestPostgresGrantWriter_RevokeAllGrantsForActorWithOutcome_EmptyActor(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "host=fake user=test")
	require.NoError(t, err)
	defer pool.Close()
	w, err := NewPostgresGrantWriter(pool, time.Second)
	require.NoError(t, err)

	revoked, err := w.RevokeAllGrantsForActorWithOutcome(context.Background(), "", math.MaxInt64)

	require.Error(t, err)
	assert.Zero(t, revoked)
}
