package adapter_test

// PartitionKeyLister's two implementations, held to the one contract they share:
// the listing is the partition INSIDE the lens's ownership scope, and every way
// of widening it is refused rather than answered.
//
// Both arms authorise a Delete. The Postgres arm's rows sit on RLS-protected
// tables, and the NATS-KV arm's sit in buckets several lenses share — so a
// listing that returned one key too many is a row tombstoned for an anchor that
// still owns it, or a sibling lens's row deleted by this lens's event.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// TestPostgresAdapter_ListKeysWhere_Integration is the Postgres arm against a
// real table: the leading key column and a non-leading one both scope correctly,
// a soft tombstone is excluded on exactly the condition ListKeys excludes it on,
// and every widening is refused.
func TestPostgresAdapter_ListKeysWhere_Integration(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if testing.Short() || dsn == "" {
		t.Skip("skipping: POSTGRES_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	// The landlord lens's shape: (app_id, landlord_id) with app_id leading, plus
	// the soft-tombstone column a protected table always carries.
	_, err = pool.Exec(ctx, `CREATE TEMP TABLE partition_listing (
		app_id TEXT, landlord_id TEXT, unit_key TEXT,
		projection_seq BIGINT NOT NULL DEFAULT 0,
		is_deleted BOOLEAN NOT NULL DEFAULT FALSE,
		deleted_at TIMESTAMPTZ,
		PRIMARY KEY (app_id, landlord_id))`)
	require.NoError(t, err)

	a, err := adapter.NewPostgresAdapter(pool, "partition_listing", []string{"app_id", "landlord_id"}, 5*time.Second, adapter.DeleteModeSoft)
	require.NoError(t, err)
	a.SetGuarded(true)

	const (
		appA      = "Hj4kPmRtw9nbCxz5vQ2y"
		appB      = "Lk2Pn6mQrtwzKbcXvP3T"
		landlordX = "Rt7vQwYzKm3bNpXc9dfg"
		landlordY = "Zx8mNpQrTvWyKbc4dhj2"
	)
	rows := []struct{ app, landlord string }{
		{appA, landlordX},
		{appA, landlordY},
		{appB, landlordX},
	}
	for _, r := range rows {
		require.NoError(t, a.Upsert(ctx,
			map[string]any{"app_id": r.app, "landlord_id": r.landlord},
			map[string]any{"unit_key": "vtx.unit." + r.app}, 10))
	}

	keySet := func(got []map[string]any) []string {
		out := make([]string, 0, len(got))
		for _, m := range got {
			out = append(out, m["app_id"].(string)+"|"+m["landlord_id"].(string))
		}
		return out
	}

	t.Run("the leading key column scopes to one anchor's partition", func(t *testing.T) {
		got, err := a.ListKeysWhere(ctx, map[string]any{"app_id": appA}, "")
		require.NoError(t, err)
		require.ElementsMatch(t, []string{appA + "|" + landlordX, appA + "|" + landlordY}, keySet(got),
			"the partition is one anchor's rows — the other application's row is never listed")
	})

	t.Run("a non-leading key column scopes just as exactly", func(t *testing.T) {
		got, err := a.ListKeysWhere(ctx, map[string]any{"landlord_id": landlordX}, "")
		require.NoError(t, err)
		require.ElementsMatch(t, []string{appA + "|" + landlordX, appB + "|" + landlordX}, keySet(got),
			"a full-PK scan is correct, not merely permitted — the index shape decides cost, never the answer")
	})

	t.Run("a soft tombstone is excluded", func(t *testing.T) {
		require.NoError(t, a.Delete(ctx, map[string]any{"app_id": appA, "landlord_id": landlordY}, 20))

		got, err := a.ListKeysWhere(ctx, map[string]any{"app_id": appA}, "")
		require.NoError(t, err)
		require.Equal(t, []string{appA + "|" + landlordX}, keySet(got),
			"a tombstoned row is not live, and a diff that saw one would re-tombstone it on every event")

		whole, err := a.ListKeys(ctx)
		require.NoError(t, err)
		require.ElementsMatch(t, []string{appA + "|" + landlordX, appB + "|" + landlordX}, keySet(whole),
			"the whole listing excludes it on the same condition, which is the agreement the two must keep")
	})

	t.Run("an empty fixed is refused", func(t *testing.T) {
		_, err := a.ListKeysWhere(ctx, nil, "")
		require.Error(t, err, "a caller that asked to scope and was quietly handed the table is the failure worth being loud about")
		require.Contains(t, err.Error(), "at least one key column")
	})

	t.Run("an unknown column is refused", func(t *testing.T) {
		_, err := a.ListKeysWhere(ctx, map[string]any{"tenant_id": appA}, "")
		require.Error(t, err, "a column this table does not key on names no partition, and dropping it would widen the scope silently")
		require.Contains(t, err.Error(), "not a key column")
	})

	t.Run("a prefix is refused", func(t *testing.T) {
		_, err := a.ListKeysWhere(ctx, map[string]any{"app_id": appA}, "cap.")
		require.Error(t, err, "a postgres table carries no key prefix, so honouring the request is impossible and ignoring it is a wider listing")
	})
}

// TestProtectedAdapter_ListKeysWhere_ForwardsToTheBase pins the wrapper's own
// reachability: every lens the partition transport arms is Protected, so a
// wrapper that did not re-declare the method would type-assert as a
// non-PartitionKeyLister and the activation gate would refuse to arm exactly the
// lenses the mechanism exists for.
func TestProtectedAdapter_ListKeysWhere_ForwardsToTheBase(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if testing.Short() || dsn == "" {
		t.Skip("skipping: POSTGRES_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	_, err = pool.Exec(ctx, `CREATE TEMP TABLE protected_partition_listing (
		app_id TEXT, landlord_id TEXT, authz_anchors TEXT[],
		projection_seq BIGINT NOT NULL DEFAULT 0,
		is_deleted BOOLEAN NOT NULL DEFAULT FALSE,
		deleted_at TIMESTAMPTZ,
		PRIMARY KEY (app_id, landlord_id))`)
	require.NoError(t, err)

	base, err := adapter.NewPostgresAdapter(pool, "protected_partition_listing", []string{"app_id", "landlord_id"}, 5*time.Second, adapter.DeleteModeSoft)
	require.NoError(t, err)
	p, err := adapter.NewProtectedAdapter(base, []string{"authz_anchors"}, nil)
	require.NoError(t, err)

	lister, ok := any(p).(adapter.PartitionKeyLister)
	require.True(t, ok, "a Protected target must satisfy PartitionKeyLister, or no protected lens can ever be partition-armed")

	const appA = "Hj4kPmRtw9nbCxz5vQ2y"
	require.NoError(t, p.Upsert(ctx,
		map[string]any{"app_id": appA, "landlord_id": "Rt7vQwYzKm3bNpXc9dfg"},
		map[string]any{"authz_anchors": []any{"Rt7vQwYzKm3bNpXc9dfg"}}, 10))
	require.NoError(t, p.Upsert(ctx,
		map[string]any{"app_id": "Lk2Pn6mQrtwzKbcXvP3T", "landlord_id": "Rt7vQwYzKm3bNpXc9dfg"},
		map[string]any{"authz_anchors": []any{"Rt7vQwYzKm3bNpXc9dfg"}}, 10))

	got, err := lister.ListKeysWhere(ctx, map[string]any{"app_id": appA}, "")
	require.NoError(t, err)
	require.Len(t, got, 1, "the wrapper's listing is the base's, scoped to one anchor's partition")
	require.Equal(t, appA, got[0]["app_id"])
}

// TestNatsKVAdapter_ListKeysWhere runs the NATS-KV arm against a real bucket,
// because the segment semantics it filters on are the substrate's rather than a
// HasPrefix approximation.
func TestNatsKVAdapter_ListKeysWhere(t *testing.T) {
	ctx := context.Background()
	const (
		appA      = "Hj4kPmRtw9nbCxz5vQ2y"
		appB      = "Lk2Pn6mQrtwzKbcXvP3T"
		landlordX = "Rt7vQwYzKm3bNpXc9dfg"
		landlordY = "Zx8mNpQrTvWyKbc4dhj2"
	)

	t.Run("a composite key is filtered out of the whole listing", func(t *testing.T) {
		kv := startKV(t)
		a, err := adapter.New(kv, []string{"app_id", "landlord_id"}, adapter.DeleteModeHard)
		require.NoError(t, err)
		for _, k := range [][2]string{{appA, landlordX}, {appA, landlordY}, {appB, landlordX}} {
			require.NoError(t, a.Upsert(ctx, map[string]any{"app_id": k[0], "landlord_id": k[1]}, map[string]any{"v": 1}, 0))
		}

		got, err := a.ListKeysWhere(ctx, map[string]any{"app_id": appA}, "")
		require.NoError(t, err)
		require.ElementsMatch(t,
			[]map[string]any{
				{"app_id": appA, "landlord_id": landlordX},
				{"app_id": appA, "landlord_id": landlordY},
			}, got, "the other application's row is in the same bucket and is never returned")
	})

	t.Run("the prefix listing on a shared bucket never reaches a sibling", func(t *testing.T) {
		kv := startKV(t)
		// The lens owns the two-segment `mine.` space; a sibling writes keys
		// with the SAME segment count under its own prefix, so mapKeys renders
		// them into the same field shape and only the prefix separates them.
		mine, err := adapter.New(kv, []string{"lens", "app_id"}, adapter.DeleteModeHard)
		require.NoError(t, err)
		for _, k := range []string{appA, appB} {
			require.NoError(t, mine.Upsert(ctx, map[string]any{"lens": "mine", "app_id": k}, map[string]any{"v": 1}, 0))
		}
		sibling, err := adapter.New(kv, []string{"lens", "app_id"}, adapter.DeleteModeHard)
		require.NoError(t, err)
		require.NoError(t, sibling.Upsert(ctx, map[string]any{"lens": "theirs", "app_id": appA}, map[string]any{"v": 1}, 0))

		got, err := mine.ListKeysWhere(ctx, map[string]any{"app_id": appA}, "mine.")
		require.NoError(t, err)
		require.Equal(t, []map[string]any{{"lens": "mine", "app_id": appA}}, got,
			"the sibling's key carries the same segment count AND the same app_id, so only the prefix listing keeps it out")

		// The negative direction, stated as its own fact: the unscoped listing
		// would have returned it, which is why the prefix has to be threaded.
		unscoped, err := mine.ListKeysWhere(ctx, map[string]any{"app_id": appA}, "")
		require.NoError(t, err)
		require.Len(t, unscoped, 2,
			"without the prefix the sibling's row is in the diff's scope — the ownership proof is the listing's, not the filter's")
	})

	t.Run("a value carrying a subject wildcard widens nothing", func(t *testing.T) {
		kv := startKV(t)
		a, err := adapter.New(kv, []string{"app_id", "landlord_id"}, adapter.DeleteModeHard)
		require.NoError(t, err)
		for _, k := range [][2]string{{appA, landlordX}, {appB, landlordY}} {
			require.NoError(t, a.Upsert(ctx, map[string]any{"app_id": k[0], "landlord_id": k[1]}, map[string]any{"v": 1}, 0))
		}

		for _, wildcard := range []string{"*", ">", "*.*"} {
			got, err := a.ListKeysWhere(ctx, map[string]any{"app_id": wildcard}, "")
			require.NoError(t, err)
			require.Emptyf(t, got,
				"%q is compared, never rendered into a subject token — a filter built from it would have matched every row", wildcard)
		}
	})

	t.Run("an empty fixed is refused", func(t *testing.T) {
		kv := startKV(t)
		a, err := adapter.New(kv, []string{"app_id", "landlord_id"}, adapter.DeleteModeHard)
		require.NoError(t, err)
		_, err = a.ListKeysWhere(ctx, nil, "")
		require.Error(t, err)
		require.Contains(t, err.Error(), "at least one key column")
	})

	t.Run("an unknown column is refused", func(t *testing.T) {
		kv := startKV(t)
		a, err := adapter.New(kv, []string{"app_id", "landlord_id"}, adapter.DeleteModeHard)
		require.NoError(t, err)
		_, err = a.ListKeysWhere(ctx, map[string]any{"tenant_id": appA}, "")
		require.Error(t, err)
		require.Contains(t, err.Error(), "not a key column")
	})
}
