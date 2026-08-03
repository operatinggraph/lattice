package adapter

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Pure DDL-generation tests (no database) ──────────────────────────────────

// ddlWithoutPolicy is everything BuildProtectedTableDDL emits except the
// DROP/CREATE POLICY pair it always ends with — the fixture shape the
// fail-closed tests need (a table with RLS enabled and forced, and no policy).
//
// Counted from the END, never the front: the emitter also converges the
// table's columns, so the number of statements before the policy depends on
// how many columns the lens declares. A `stmts[:3]` slice silently stopped
// including ENABLE/FORCE ROW LEVEL SECURITY when those were added, which made
// a fail-closed test pass for the wrong reason.
func ddlWithoutPolicy(stmts []string) []string { return stmts[:len(stmts)-2] }

func TestBuildProtectedTableDDL_Shape(t *testing.T) {
	stmts, err := BuildProtectedTableDDL("read_lease_applications",
		[]string{"application_id"},
		[]ColumnDef{{Name: "unit", Type: "text"}, {Name: "status", Type: "text"}},
	)
	require.NoError(t, err)
	// create table, one converging ADD COLUMN per body column, the platform's
	// own four, then enable rls, force rls, drop policy, create policy.
	require.Len(t, stmts, 1+2+4+4)

	create := stmts[0]
	alters := stmts[1 : len(stmts)-4]
	tail := stmts[len(stmts)-4:]
	enable, force, dropPol, createPol := tail[0], tail[1], tail[2], tail[3]

	// The converging half: CREATE TABLE IF NOT EXISTS is a no-op on a table
	// that already exists, so a lens that GAINS a column would otherwise be
	// provisioned onto a table that never gets it — and the next projection
	// pauses the consumer structurally on SQLSTATE 42703. Every declared body
	// column is re-asserted idempotently.
	assert.Equal(t, []string{
		`ALTER TABLE "read_lease_applications" ADD COLUMN IF NOT EXISTS "unit" text`,
		`ALTER TABLE "read_lease_applications" ADD COLUMN IF NOT EXISTS "status" text`,
		`ALTER TABLE "read_lease_applications" ADD COLUMN IF NOT EXISTS "authz_anchors" text[] NOT NULL DEFAULT '{}'`,
		`ALTER TABLE "read_lease_applications" ADD COLUMN IF NOT EXISTS "projection_seq" bigint NOT NULL DEFAULT 0`,
		`ALTER TABLE "read_lease_applications" ADD COLUMN IF NOT EXISTS "is_deleted" boolean NOT NULL DEFAULT false`,
		`ALTER TABLE "read_lease_applications" ADD COLUMN IF NOT EXISTS "deleted_at" timestamptz`,
	}, alters)
	// The KEY column is deliberately absent: it is the PRIMARY KEY, so a table
	// lacking it is a different table, not an older revision of this one, and
	// ADD COLUMN could not make it NOT NULL over existing rows regardless.
	for _, a := range alters {
		assert.NotContains(t, a, `"application_id"`)
	}
	// Ordering is load-bearing: the policy is dropped and recreated AFTER the
	// columns exist, so a policy referencing a newly added one compiles.
	assert.Less(t, len(alters), len(stmts)-1)

	assert.Contains(t, create, `CREATE TABLE IF NOT EXISTS "read_lease_applications"`)
	assert.Contains(t, create, `"application_id" text NOT NULL`)
	assert.Contains(t, create, `"unit" text`)
	assert.Contains(t, create, `"status" text`)
	assert.Contains(t, create, `authz_anchors text[] NOT NULL DEFAULT '{}'`)
	assert.Contains(t, create, `projection_seq bigint NOT NULL DEFAULT 0`)
	assert.Contains(t, create, `PRIMARY KEY ("application_id")`)

	assert.Equal(t, `ALTER TABLE "read_lease_applications" ENABLE ROW LEVEL SECURITY`, enable)
	// FORCE RLS is the structural fail-closed guard (§6.14 H3): present, always.
	assert.Equal(t, `ALTER TABLE "read_lease_applications" FORCE ROW LEVEL SECURITY`, force)

	assert.Equal(t, `DROP POLICY IF EXISTS "rls_read_lease_applications" ON "read_lease_applications"`, dropPol)
	// Set-membership policy (§6.14): a row is visible iff the actor holds a grant
	// for ANY of its authz_anchors; live grants only (NOT is_deleted); the actor
	// comes from the session GUC, NULL-safe (deny when unset).
	assert.Contains(t, createPol, `CREATE POLICY "rls_read_lease_applications" ON "read_lease_applications"`)
	// FOR SELECT — the policy governs reads only, so it never acts as a WITH CHECK
	// that would block the trusted (no-actor) projector's writes.
	assert.Contains(t, createPol, `FOR SELECT USING (`)
	assert.Contains(t, createPol, `unnest(authz_anchors)`)
	assert.Contains(t, createPol, `anchor_id FROM "actor_read_grants"`)
	assert.Contains(t, createPol, `current_setting('lattice.actor_id', true)`)
	assert.Contains(t, createPol, `NOT is_deleted`)
	// The WildcardAnchor escape hatch (§6.14 M5): an OR'd EXISTS matching a
	// literal '*' anchor_id grant, independent of the row's own authz_anchors.
	assert.Contains(t, createPol, `anchor_id = '*'`)
	assert.Contains(t, createPol, " OR\n")
}

// TestBuildProtectedTableDDL_WildcardIsSeparateFromRowAnchorClause is a
// string-shape proof that the wildcard EXISTS is OR'd alongside, not folded
// into, the per-row authz_anchors match — so a wildcard grant is evaluated
// independently of a row's own anchors. The real Postgres RLS semantics (a
// wildcard-granted actor actually seeing every row) are proven by the
// POSTGRES_TEST_DSN-gated integration test in cmd/clinic-app.
func TestBuildProtectedTableDDL_WildcardIsSeparateFromRowAnchorClause(t *testing.T) {
	stmts, err := BuildProtectedTableDDL("read_x", []string{"id"}, nil)
	require.NoError(t, err)
	createPol := stmts[len(stmts)-1]
	// The wildcard EXISTS must be self-contained (actor_id + anchor_id='*' +
	// NOT is_deleted) and OR'd against — not folded into — the per-row
	// unnest(authz_anchors) match, so a wildcard grant is evaluated even for a
	// row whose authz_anchors is empty/unrelated.
	wildcardIdx := strings.Index(createPol, "anchor_id = '*'")
	rowMatchIdx := strings.Index(createPol, "unnest(authz_anchors)")
	require.True(t, wildcardIdx >= 0 && rowMatchIdx >= 0, "policy must contain both clauses: %s", createPol)
	require.Less(t, wildcardIdx, rowMatchIdx, "the wildcard EXISTS must precede the OR'd row-anchor EXISTS")
}

func TestBuildProtectedTableDDL_CompositeKey(t *testing.T) {
	stmts, err := BuildProtectedTableDDL("t", []string{"a", "b"}, nil)
	require.NoError(t, err)
	assert.Contains(t, stmts[0], `PRIMARY KEY ("a", "b")`)
}

func TestBuildProtectedTableDDL_Validation(t *testing.T) {
	cases := []struct {
		name   string
		table  string
		keys   []string
		body   []ColumnDef
		errSub string
	}{
		{"empty table", "", []string{"id"}, nil, "table must not be empty"},
		{"table with quote", `t"x`, []string{"id"}, nil, "double-quote"},
		{"no keys", "t", nil, nil, "keyCols must not be empty"},
		{"empty key", "t", []string{""}, nil, "key column must not be empty"},
		{"key with quote", "t", []string{`i"d`}, nil, "double-quote"},
		{"duplicate key", "t", []string{"id", "id"}, nil, "duplicate field"},
		{"body dup of key", "t", []string{"id"}, []ColumnDef{{Name: "id", Type: "text"}}, "duplicates a key column"},
		{"body empty name", "t", []string{"id"}, []ColumnDef{{Name: "", Type: "text"}}, "body column must not be empty"},
		{"body empty type", "t", []string{"id"}, []ColumnDef{{Name: "x", Type: " "}}, "empty type"},
		{"body type semicolon", "t", []string{"id"}, []ColumnDef{{Name: "x", Type: "text; DROP TABLE"}}, "must not contain ';'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := BuildProtectedTableDDL(c.table, c.keys, c.body)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.errSub)
		})
	}
}

func TestBuildGrantTableDDL_Shape(t *testing.T) {
	stmts := BuildGrantTableDDL()
	require.Len(t, stmts, 1)
	ddl := stmts[0]
	assert.Contains(t, ddl, `CREATE TABLE IF NOT EXISTS "actor_read_grants"`)
	assert.Contains(t, ddl, `actor_id text NOT NULL`)
	assert.Contains(t, ddl, `anchor_id text NOT NULL`)
	assert.Contains(t, ddl, `grant_source text NOT NULL`)
	assert.Contains(t, ddl, `projection_seq bigint NOT NULL`)
	// The retained tombstone that makes the §6.14 no-resurrect guarantee possible.
	assert.Contains(t, ddl, `is_deleted boolean NOT NULL DEFAULT false`)
	assert.Contains(t, ddl, `PRIMARY KEY (actor_id, anchor_id, grant_source)`)
}

func TestNewPostgresGrantWriter_NilPool(t *testing.T) {
	_, err := NewPostgresGrantWriter(nil, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pool")
}

// ── Integration tests (require real Postgres; superuser DSN) ─────────────────

func provisionGrantWriter(t *testing.T, pool *pgxpool.Pool) *PostgresGrantWriter {
	t.Helper()
	w, err := NewPostgresGrantWriter(pool, 10*time.Second)
	require.NoError(t, err)
	require.NoError(t, w.Provision(context.Background()))
	return w
}

// grantState reads the stored (projection_seq, is_deleted) for a grant row, or
// found=false if the row is absent.
func grantState(t *testing.T, pool *pgxpool.Pool, actor, anchor, source string) (seq int64, deleted, found bool) {
	t.Helper()
	row := pool.QueryRow(context.Background(),
		`SELECT projection_seq, is_deleted FROM "actor_read_grants" WHERE actor_id=$1 AND anchor_id=$2 AND grant_source=$3`,
		actor, anchor, source)
	err := row.Scan(&seq, &deleted)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return 0, false, false
		}
		require.NoError(t, err)
	}
	return seq, deleted, true
}

// TestRLS_GrantSeqGuard_Integration proves the §6.14 monotonic-seq guard on
// actor_read_grants: a stale CDC replay cannot resurrect a revoked grant (H4),
// and each grant_source owns its rows independently.
func TestRLS_GrantSeqGuard_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	w := provisionGrantWriter(t, pool)

	// Unique per-test actor so the shared table is safe under -p parallel CI.
	actor := "actor_" + sanitize(t.Name())
	anchor := "anchorX"
	srcA := "cap-read.residence"
	srcB := "cap-read.roles"
	clearActor := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM "actor_read_grants" WHERE actor_id=$1`, actor)
	}
	clearActor() // self-heal against leftover from an interrupted prior run
	t.Cleanup(clearActor)

	// Grant at seq 5 → live.
	require.NoError(t, w.UpsertGrant(ctx, actor, anchor, srcA, 5))
	seq, deleted, found := grantState(t, pool, actor, anchor, srcA)
	require.True(t, found)
	assert.EqualValues(t, 5, seq)
	assert.False(t, deleted, "fresh grant is live")

	// Revoke at seq 10 → tombstoned, seq retained.
	require.NoError(t, w.RevokeGrant(ctx, actor, anchor, srcA, 10))
	seq, deleted, _ = grantState(t, pool, actor, anchor, srcA)
	assert.EqualValues(t, 10, seq)
	assert.True(t, deleted, "revoked grant is tombstoned")

	// H4 — stale replay of the original grant (seq 5) MUST NOT resurrect it.
	require.NoError(t, w.UpsertGrant(ctx, actor, anchor, srcA, 5))
	seq, deleted, _ = grantState(t, pool, actor, anchor, srcA)
	assert.EqualValues(t, 10, seq, "stale upsert is a no-op; seq unchanged")
	assert.True(t, deleted, "stale upsert cannot resurrect a revoked grant")

	// A strictly-newer grant DOES revive it (monotonic forward progress).
	require.NoError(t, w.UpsertGrant(ctx, actor, anchor, srcA, 12))
	_, deleted, _ = grantState(t, pool, actor, anchor, srcA)
	assert.False(t, deleted, "newer-seq grant revives the tombstone")

	// Per-source isolation: srcB granted independently; revoking srcA leaves srcB.
	require.NoError(t, w.UpsertGrant(ctx, actor, anchor, srcB, 3))
	require.NoError(t, w.RevokeGrant(ctx, actor, anchor, srcA, 20))
	_, deletedA, _ := grantState(t, pool, actor, anchor, srcA)
	_, deletedB, foundB := grantState(t, pool, actor, anchor, srcB)
	assert.True(t, deletedA, "srcA revoked")
	require.True(t, foundB)
	assert.False(t, deletedB, "revoking srcA must not touch srcB's coexisting grant")

	// A stale revoke (lower seq) on srcB is a no-op.
	require.NoError(t, w.RevokeGrant(ctx, actor, anchor, srcB, 1))
	_, deletedB, _ = grantState(t, pool, actor, anchor, srcB)
	assert.False(t, deletedB, "stale revoke cannot tombstone a fresher grant")
}

// TestRLS_RevokeAllGrantsForActor_Integration proves the crypto-shredding
// entry point: a bulk revoke tombstones EVERY grant_source row for the
// shredded actor (unlike RevokeGrant, which is scoped to one source), leaves
// another actor's coexisting rows alone, and honors the same monotonic-seq
// guard (a stale call cannot un-revoke a row a fresher revoke already
// tombstoned at a higher seq).
func TestRLS_RevokeAllGrantsForActor_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	w := provisionGrantWriter(t, pool)

	actor := "actor_" + sanitize(t.Name())
	other := "other_" + sanitize(t.Name())
	srcA := "cap-read.clinic.patient"
	srcB := "cap-read.provider.clinic"
	clear := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM "actor_read_grants" WHERE actor_id IN ($1, $2)`, actor, other)
	}
	clear()
	t.Cleanup(clear)

	// The shredded actor holds grants from two distinct producers, at two
	// distinct anchors — a bulk revoke must reach both regardless of source.
	require.NoError(t, w.UpsertGrant(ctx, actor, "anchorA", srcA, 5))
	require.NoError(t, w.UpsertGrant(ctx, actor, "anchorB", srcB, 7))
	// A different actor's row, same anchor/source shape, must survive untouched.
	require.NoError(t, w.UpsertGrant(ctx, other, "anchorA", srcA, 5))

	require.NoError(t, w.RevokeAllGrantsForActor(ctx, actor, 100))

	seqA, deletedA, foundA := grantState(t, pool, actor, "anchorA", srcA)
	require.True(t, foundA)
	assert.True(t, deletedA, "every grant_source row for the shredded actor must be tombstoned")
	assert.EqualValues(t, 100, seqA)
	_, deletedB, foundB := grantState(t, pool, actor, "anchorB", srcB)
	require.True(t, foundB)
	assert.True(t, deletedB, "a different grant_source row for the same actor must also be tombstoned")
	_, deletedOther, foundOther := grantState(t, pool, other, "anchorA", srcA)
	require.True(t, foundOther)
	assert.False(t, deletedOther, "another actor's row must not be touched")

	// A stale (lower-seq) revoke-all is a no-op under the monotonic guard.
	require.NoError(t, w.RevokeAllGrantsForActor(ctx, actor, 50))
	seqAfter, _, _ := grantState(t, pool, actor, "anchorA", srcA)
	assert.EqualValues(t, 100, seqAfter, "a stale revoke-all must not regress the stored seq")
}

func TestPostgresGrantWriter_RevokeAllGrantsForActor_EmptyActorID(t *testing.T) {
	w, err := NewPostgresGrantWriter(&pgxpool.Pool{}, 0)
	require.NoError(t, err)
	err = w.RevokeAllGrantsForActor(context.Background(), "", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "actorID must not be empty")
}

// TestRLS_RebuildReplayRederivesRowsLostOutOfBand_Integration pins the repair
// path for a diverged actor_read_grants.
//
// A grant lens cannot truncate on rebuild — actor_read_grants is shared, so
// GrantWriterAdapter deliberately implements no Truncater — which reads as
// leaving an operator who restores or partially wipes the table with no way to
// re-derive the missing rows. It does not: the §6.14 guard lives entirely in the
// ON CONFLICT arm, so an ABSENT row takes the plain INSERT at whatever seq the
// write carries, while a row still present replays against its own stored seq
// and is correctly a no-op (the comparison is strictly-greater).
//
// So `Rebuild(truncate=false)` IS the repair path: resetting the durable replays
// every Core KV entry carrying its real stream sequence
// (pipeline.go, results[i].ProjectionSeq = msg.Sequence), which re-creates what
// is missing and leaves what is not alone. This test pins the property the
// repair rests on, at the layer that owns it.
func TestRLS_RebuildReplayRederivesRowsLostOutOfBand_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	w := provisionGrantWriter(t, pool)

	actor := "actor_" + sanitize(t.Name())
	src := "cap-read.residence"
	clearActor := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM "actor_read_grants" WHERE actor_id=$1`, actor)
	}
	clearActor()
	t.Cleanup(clearActor)

	// Steady state: two grants projected at their stream sequences.
	const seqKept, seqLost = 40, 41
	require.NoError(t, w.UpsertGrant(ctx, actor, "anchorKept", src, seqKept))
	require.NoError(t, w.UpsertGrant(ctx, actor, "anchorLost", src, seqLost))

	// An out-of-band restore/wipe removes one row entirely — not a tombstone,
	// which is what makes it invisible to every retraction mechanism.
	_, err = pool.Exec(ctx, `DELETE FROM "actor_read_grants" WHERE actor_id=$1 AND anchor_id=$2`, actor, "anchorLost")
	require.NoError(t, err)
	_, _, found := grantState(t, pool, actor, "anchorLost", src)
	require.False(t, found, "precondition: the row is gone, not tombstoned")

	// The rebuild replay: every entry re-projected at its own stream sequence,
	// the same seqs the steady state was written at.
	require.NoError(t, w.UpsertGrant(ctx, actor, "anchorKept", src, seqKept))
	require.NoError(t, w.UpsertGrant(ctx, actor, "anchorLost", src, seqLost))

	seq, deleted, found := grantState(t, pool, actor, "anchorLost", src)
	require.True(t, found, "a replay at the row's own stream seq must re-derive a row lost out-of-band")
	assert.EqualValues(t, seqLost, seq)
	assert.False(t, deleted, "the re-derived grant is live")

	seq, deleted, found = grantState(t, pool, actor, "anchorKept", src)
	require.True(t, found)
	assert.EqualValues(t, seqKept, seq, "a row that survived replays against its own seq and is unchanged")
	assert.False(t, deleted)

	// The repair must not resurrect a grant that was legitimately revoked at a
	// higher seq — otherwise the rebuild would be an over-grant, and the reason
	// a diverged table is safe to rebuild is precisely that it is not.
	require.NoError(t, w.RevokeGrant(ctx, actor, "anchorKept", src, 99))
	require.NoError(t, w.UpsertGrant(ctx, actor, "anchorKept", src, seqKept))
	_, deleted, _ = grantState(t, pool, actor, "anchorKept", src)
	assert.True(t, deleted, "a replay at a stale seq must not resurrect a revoked grant")
}

// TestRLS_ForceRLS_DenyAll_Integration proves §6.14 H3: a protected table is
// fail-closed. A table created WITHOUT a policy denies all rows under FORCE RLS;
// a table WITH the set-membership policy returns only rows whose authz_anchors
// the actor holds a grant for, and nothing at all when the actor is unset.
//
// The dev/CI superuser bypasses RLS, so the read is performed under a dedicated
// non-superuser role (SET LOCAL ROLE) — the posture a real read boundary uses.
func TestRLS_ForceRLS_DenyAll_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	w := provisionGrantWriter(t, pool)

	const role = "rls_test_reader"
	suffix := sanitize(t.Name())
	protectedTbl := "rls_protected_" + suffix
	nopolicyTbl := "rls_nopolicy_" + suffix
	actor := "actor_" + suffix
	anchorGranted := "anchor_granted_" + suffix
	anchorOther := "anchor_other_" + suffix

	// Non-superuser reader role (RLS is bypassed by superusers).
	_, err = pool.Exec(ctx, `DO $$ BEGIN
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='`+role+`') THEN
			CREATE ROLE `+role+` NOLOGIN;
		END IF; END $$;`)
	require.NoError(t, err)

	clean := func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DROP TABLE IF EXISTS "`+protectedTbl+`"`)
		_, _ = pool.Exec(bg, `DROP TABLE IF EXISTS "`+nopolicyTbl+`"`)
		_, _ = pool.Exec(bg, `DELETE FROM "actor_read_grants" WHERE actor_id=$1`, actor)
	}
	clean() // self-heal against leftover from an interrupted prior run
	t.Cleanup(clean)

	// 1. Protected table WITH the generated policy.
	stmts, err := BuildProtectedTableDDL(protectedTbl, []string{"id"}, []ColumnDef{{Name: "body", Type: "text"}})
	require.NoError(t, err)
	for _, s := range stmts {
		_, err = pool.Exec(ctx, s)
		require.NoError(t, err, "exec: %s", s)
	}
	// 2. Table created WITHOUT a policy (ENABLE+FORCE only) — the H3 fail-closed case.
	nopolicy, err := BuildProtectedTableDDL(nopolicyTbl, []string{"id"}, []ColumnDef{{Name: "body", Type: "text"}})
	require.NoError(t, err)
	for _, s := range ddlWithoutPolicy(nopolicy) {
		_, err = pool.Exec(ctx, s)
		require.NoError(t, err, "exec: %s", s)
	}

	_, err = pool.Exec(ctx, fmt.Sprintf(`GRANT SELECT ON "%s","%s","actor_read_grants" TO %s`, protectedTbl, nopolicyTbl, role))
	require.NoError(t, err)

	// Seed one row in each table whose authz_anchors include the granted anchor
	// (insert as superuser — bypasses RLS).
	for _, tbl := range []string{protectedTbl, nopolicyTbl} {
		_, err = pool.Exec(ctx,
			fmt.Sprintf(`INSERT INTO "%s" (id, body, authz_anchors, projection_seq) VALUES ($1,$2,$3,$4)`, tbl),
			"row1", "secret", []string{anchorGranted, anchorOther}, 1)
		require.NoError(t, err)
	}

	// Read helper: count visible rows as the reader role, with an optional actor.
	visibleAs := func(tbl, actorID string) int {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = tx.Exec(ctx, "SET LOCAL ROLE "+role)
		require.NoError(t, err)
		if actorID != "" {
			// set_config(..., true) = transaction-local, like SET LOCAL, but
			// accepts a bind parameter (plain SET does not).
			_, err = tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID)
			require.NoError(t, err)
		}
		var n int
		require.NoError(t, tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM "%s"`, tbl)).Scan(&n))
		return n
	}

	// No actor set → current_setting returns NULL → deny all (fail-closed).
	assert.Equal(t, 0, visibleAs(protectedTbl, ""), "unset actor sees nothing")
	// Actor set but holds NO grant → deny.
	assert.Equal(t, 0, visibleAs(protectedTbl, actor), "ungranted actor sees nothing")

	// Grant the actor one of the row's anchors → the row becomes visible.
	require.NoError(t, w.UpsertGrant(ctx, actor, anchorGranted, "cap-read.test", 1))
	assert.Equal(t, 1, visibleAs(protectedTbl, actor), "granted actor sees the row")

	// H3 headline: the table with NO policy denies all rows even for the same
	// granted actor (FORCE RLS + missing policy ⇒ deny-all, never serve-all).
	assert.Equal(t, 0, visibleAs(nopolicyTbl, actor), "policy-less protected table denies all rows")

	// Revoke the grant → the row disappears again (live-grant filter).
	require.NoError(t, w.RevokeGrant(ctx, actor, anchorGranted, "cap-read.test", 2))
	assert.Equal(t, 0, visibleAs(protectedTbl, actor), "revoked grant hides the row")
}

// TestRLS_CrossActorAnchor_Filtered_Integration proves §6.14 read-path
// isolation: two actors with disjoint grants on the same protected table each
// see only the row anchored to a grant they hold, and an ungranted actor sees
// nothing.
func TestRLS_CrossActorAnchor_Filtered_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	role, visibleAs := rlsReaderHarness(t, pool)

	suffix := sanitize(t.Name())
	tbl := "rbv2_" + suffix
	stmts, err := BuildProtectedTableDDL(tbl, []string{"id"}, []ColumnDef{{Name: "body", Type: "text"}})
	require.NoError(t, err)
	for _, s := range stmts {
		_, err = pool.Exec(ctx, s)
		require.NoError(t, err, "exec: %s", s)
	}
	_, err = pool.Exec(ctx, fmt.Sprintf(`GRANT SELECT ON "%s","actor_read_grants" TO %s`, tbl, role))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) })

	actorA := "actorA_" + suffix
	actorB := "actorB_" + suffix
	anchorA := "anchorA_" + suffix
	anchorB := "anchorB_" + suffix

	w := provisionGrantWriter(t, pool)
	require.NoError(t, w.UpsertGrant(ctx, actorA, anchorA, "cap-read.test", 1))
	require.NoError(t, w.UpsertGrant(ctx, actorB, anchorB, "cap-read.test", 1))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM "actor_read_grants" WHERE actor_id=$1 OR actor_id=$2`, actorA, actorB)
	})

	_, err = pool.Exec(ctx, fmt.Sprintf(`INSERT INTO "%s" (id, body, authz_anchors, projection_seq) VALUES ($1,$2,$3,$4)`, tbl),
		"rowA", "secret-rowA", []string{anchorA}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, fmt.Sprintf(`INSERT INTO "%s" (id, body, authz_anchors, projection_seq) VALUES ($1,$2,$3,$4)`, tbl),
		"rowB", "secret-rowB", []string{anchorB}, 1)
	require.NoError(t, err)

	assert.Equal(t, 1, visibleAs(tbl, actorA), "A sees only A's row")
	assert.Equal(t, 1, visibleAs(tbl, actorB), "B sees only B's row")
	assert.Equal(t, 0, visibleAs(tbl, "stranger_"+suffix), "an ungranted actor sees nothing")
	assert.Equal(t, 0, visibleAs(tbl, ""), "an unauthenticated (NULL) actor sees nothing")
}

// TestRLS_CrossAnchorBleed_Filtered_Integration proves §6.14 H5 hierarchical
// set-membership: an actor holding a fine-grained grant (unit.X) never sees a
// row anchored only to a different unit, while an actor holding a coarser
// grant (building.B) sees every row tagged with that coarse anchor but not an
// orphan row tagged with an unrelated fine anchor only.
func TestRLS_CrossAnchorBleed_Filtered_Integration(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	role, visibleAs := rlsReaderHarness(t, pool)

	suffix := sanitize(t.Name())
	tbl := "rbv4_" + suffix
	stmts, err := BuildProtectedTableDDL(tbl, []string{"id"}, []ColumnDef{{Name: "body", Type: "text"}})
	require.NoError(t, err)
	for _, s := range stmts {
		_, err = pool.Exec(ctx, s)
		require.NoError(t, err, "exec: %s", s)
	}
	_, err = pool.Exec(ctx, fmt.Sprintf(`GRANT SELECT ON "%s","actor_read_grants" TO %s`, tbl, role))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) })

	unitX := "unitx_" + suffix
	unitY := "unity_" + suffix
	buildingB := "buildingb_" + suffix
	fineActor := "fine_" + suffix
	coarseActor := "coarse_" + suffix

	w := provisionGrantWriter(t, pool)
	require.NoError(t, w.UpsertGrant(ctx, fineActor, unitX, "cap-read.residence", 1))
	require.NoError(t, w.UpsertGrant(ctx, coarseActor, buildingB, "cap-read.residence", 1))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM "actor_read_grants" WHERE actor_id=$1 OR actor_id=$2`, fineActor, coarseActor)
	})

	insertRow := func(id string, anchors []string) {
		_, err := pool.Exec(ctx, fmt.Sprintf(`INSERT INTO "%s" (id, body, authz_anchors, projection_seq) VALUES ($1,$2,$3,$4)`, tbl),
			id, "secret-"+id, anchors, 1)
		require.NoError(t, err)
	}
	insertRow("rowX", []string{unitX, buildingB})      // a unit in building B
	insertRow("rowY", []string{unitY, buildingB})      // a different unit in building B
	insertRow("rowOrphan", []string{unitY})            // a unit neither actor has a grant for

	// fineActor (unit.X) sees rowX only — no cross-anchor bleed to rowY/rowOrphan.
	assert.Equal(t, 1, visibleAs(tbl, fineActor), "unit.X holder sees only the unit.X row")
	// coarseActor (building.B) sees BOTH rowX and rowY (both tagged building.B),
	// but NOT rowOrphan (tagged unit.Y only) — the H5 hierarchical grant.
	assert.Equal(t, 2, visibleAs(tbl, coarseActor), "building.B holder sees every unit in the building, not the orphan")
}

// rlsReaderHarness creates a per-run non-superuser reader role (RLS is
// bypassed by superusers) and returns it with a visibleAs closure that
// counts rows the given actor can SELECT from table under that role, with
// lattice.actor_id set transaction-locally — the read boundary's posture.
func rlsReaderHarness(t *testing.T, pool *pgxpool.Pool) (role string, visibleAs func(table, actor string) int) {
	t.Helper()
	role = "rls_read_bypass_reader_" + sanitize(t.Name())
	ctx := context.Background()
	_, err := pool.Exec(ctx, `DO $$ BEGIN
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='`+role+`') THEN
			CREATE ROLE `+role+` NOLOGIN;
		END IF; END $$;`)
	require.NoError(t, err)

	visibleAs = func(table, actor string) int {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = tx.Exec(ctx, "SET LOCAL ROLE "+role)
		require.NoError(t, err)
		if actor != "" {
			_, err = tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actor)
			require.NoError(t, err)
		}
		var n int
		require.NoError(t, tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM "%s"`, table)).Scan(&n))
		return n
	}
	return role, visibleAs
}

// sanitize maps a Go test name to a lowercase identifier-safe suffix.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
