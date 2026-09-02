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

// provisionProtected creates a correctly-locked-down protected table from the
// spec DDL (the out-of-band operator step a real deployment performs by hand).
func provisionProtected(t *testing.T, pool *pgxpool.Pool, table string, keys []string, body []ColumnDef) {
	t.Helper()
	stmts, err := BuildProtectedTableDDL(table, keys, body)
	require.NoError(t, err)
	for _, s := range stmts {
		_, err = pool.Exec(context.Background(), s)
		require.NoError(t, err, "exec: %s", s)
	}
}

// TestVerifyProtectedTable_Pass proves a correctly-provisioned out-of-band table
// (FORCE ROW LEVEL SECURITY + the §6.14 columns + a SELECT policy) passes the
// posture verify, so the protected lens activates.
func TestVerifyProtectedTable_Pass(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_verify_ok_" + sanitize(t.Name())
	body := []ColumnDef{{Name: "status", Type: "text"}}
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	provisionProtected(t, pool, tbl, []string{"id"}, body)
	require.NoError(t, VerifyProtectedTable(ctx, pool, tbl, []string{"id"}, body, 10*time.Second))
}

// TestVerifyProtectedTable_ForceRLSOff_Fails is the SECURITY-critical case: a
// table whose FORCE ROW LEVEL SECURITY was never enabled (or was turned off via
// drift) is world-readable, so the verify MUST fail and keep the lens dark.
func TestVerifyProtectedTable_ForceRLSOff_Fails(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_verify_noforce_" + sanitize(t.Name())
	body := []ColumnDef{{Name: "status", Type: "text"}}
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	// Provision correctly, then simulate the drift / forgotten-FORCE posture.
	provisionProtected(t, pool, tbl, []string{"id"}, body)
	_, err = pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE "%s" NO FORCE ROW LEVEL SECURITY`, tbl))
	require.NoError(t, err)

	err = VerifyProtectedTable(ctx, pool, tbl, []string{"id"}, body, 10*time.Second)
	require.Error(t, err, "FORCE-RLS-off must fail closed")
	assert.Contains(t, err.Error(), "FORCE ROW LEVEL SECURITY")
}

// TestVerifyProtectedTable_EnableOff_Fails is the other half of the SECURITY
// case: FORCE ROW LEVEL SECURITY without ROW LEVEL SECURITY *enabled* leaves the
// table world-readable (Postgres applies no policy when relrowsecurity is off),
// so the verify MUST fail closed even though FORCE is set.
func TestVerifyProtectedTable_EnableOff_Fails(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_verify_noenable_" + sanitize(t.Name())
	body := []ColumnDef{{Name: "status", Type: "text"}}
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	provisionProtected(t, pool, tbl, []string{"id"}, body)
	// DISABLE clears relrowsecurity while relforcerowsecurity stays set — the
	// FORCE-without-ENABLE posture that silently exposes every row.
	_, err = pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE "%s" DISABLE ROW LEVEL SECURITY`, tbl))
	require.NoError(t, err)

	err = VerifyProtectedTable(ctx, pool, tbl, []string{"id"}, body, 10*time.Second)
	require.Error(t, err, "RLS-not-enabled must fail closed")
	assert.Contains(t, err.Error(), "not ENABLED")
}

// TestVerifyProtectedTable_PermissivePolicy_Fails proves the verify checks policy
// POSTURE, not mere presence: a SELECT policy under the expected name but with a
// USING(true) body (world-readable under FORCE RLS) is rejected.
func TestVerifyProtectedTable_PermissivePolicy_Fails(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_verify_permissive_" + sanitize(t.Name())
	body := []ColumnDef{{Name: "status", Type: "text"}}
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	// Everything but the policy pair, then a permissive policy under the
	// deterministic name instead of the §6.14 membership policy.
	stmts, err := BuildProtectedTableDDL(tbl, []string{"id"}, body)
	require.NoError(t, err)
	for _, s := range ddlWithoutPolicy(stmts) {
		_, err = pool.Exec(ctx, s)
		require.NoError(t, err, "exec: %s", s)
	}
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE POLICY %q ON %q FOR SELECT USING (true)`, policyName(tbl), tbl))
	require.NoError(t, err)

	err = VerifyProtectedTable(ctx, pool, tbl, []string{"id"}, body, 10*time.Second)
	require.Error(t, err, "a permissive (USING true) policy must fail posture")
	assert.Contains(t, err.Error(), "set-membership")
}

// TestVerifyProtectedTable_Absent_Fails proves an unprovisioned table surfaces as
// a clean "absent" error (via to_regclass NULL), not a structural pg 42P01 that
// would escalate the pump to an operator-Resume pause.
func TestVerifyProtectedTable_Absent_Fails(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	tbl := "rls_verify_absent_" + sanitize(t.Name())
	_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS "`+tbl+`"`)

	err = VerifyProtectedTable(ctx, pool, tbl, []string{"id"}, nil, 10*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absent")
}

// TestVerifyProtectedTable_MissingColumn_Fails proves a table missing an expected
// column fails the functional check with a named column.
func TestVerifyProtectedTable_MissingColumn_Fails(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_verify_missingcol_" + sanitize(t.Name())
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	// Provision with only a "status" body column, then verify expecting an
	// additional "amount" column the operator forgot.
	provisionProtected(t, pool, tbl, []string{"id"}, []ColumnDef{{Name: "status", Type: "text"}})
	err = VerifyProtectedTable(ctx, pool, tbl, []string{"id"},
		[]ColumnDef{{Name: "status", Type: "text"}, {Name: "amount", Type: "bigint"}}, 10*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "amount")
}

// TestVerifyProtectedTable_NoPolicy_Fails proves a FORCE-RLS table with no SELECT
// policy (the H3 deny-all "dark" case) is reported so the operator learns the
// read model serves nothing.
func TestVerifyProtectedTable_NoPolicy_Fails(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_verify_nopolicy_" + sanitize(t.Name())
	body := []ColumnDef{{Name: "status", Type: "text"}}
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	// Run everything but the policy pair, so the table has RLS forced and no
	// policy at all — the H3 fail-closed case.
	stmts, err := BuildProtectedTableDDL(tbl, []string{"id"}, body)
	require.NoError(t, err)
	for _, s := range ddlWithoutPolicy(stmts) {
		_, err = pool.Exec(ctx, s)
		require.NoError(t, err, "exec: %s", s)
	}

	err = VerifyProtectedTable(ctx, pool, tbl, []string{"id"}, body, 10*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "policy")
}

// TestVerifyGrantTable_Pass proves the shared actor_read_grants table passes the
// shape verify once provisioned out-of-band (the grant writer's Probe).
func TestVerifyGrantTable_Pass(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	// Provision is idempotent + the table is shared across parallel tests, so
	// this is safe under -p (never dropped here).
	w := provisionGrantWriter(t, pool)
	require.NoError(t, w.VerifyGrantTable(ctx))
}

// pkeyConstraintName looks up a table's PRIMARY KEY constraint name live
// rather than assuming Postgres's default "<table>_pkey" pattern literally: a
// long-enough table name (e.g. one built from a full Go test name) is
// truncated at NAMEDATALEN (63 bytes) before Postgres derives the default
// constraint name, so a Go-side "<tbl>_pkey" string can name an object that
// was never created.
func pkeyConstraintName(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) string {
	t.Helper()
	var name string
	err := pool.QueryRow(ctx,
		`SELECT conname FROM pg_constraint WHERE conrelid = to_regclass($1) AND contype = 'p'`,
		table,
	).Scan(&name)
	require.NoError(t, err, "table %q must have a PRIMARY KEY constraint to look up", table)
	return name
}

// TestVerifyProtectedTable_NoUniqueConstraint_Fails proves the §4.2(e) gate: a
// table that is otherwise correctly provisioned (RLS forced, columns, policy)
// but whose unique constraint on the ON CONFLICT key columns is missing must
// FAIL closed, not pass. This is the design's own motivating scenario — a
// table dropped and re-provisioned without its PRIMARY KEY — which would
// otherwise write-fail every upsert with SQLSTATE 42P10 while every check
// above this one still reports healthy. Proven only after
// TestVerifyProtectedTable_Pass is green (the positive vector), so the
// refusal below is known to be about the CONSTRAINT, not some other posture
// defect this fixture also happens to trip.
func TestVerifyProtectedTable_NoUniqueConstraint_Fails(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_verify_nouniq_" + sanitize(t.Name())
	body := []ColumnDef{{Name: "status", Type: "text"}}
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	provisionProtected(t, pool, tbl, []string{"id"}, body)
	// BuildProtectedTableDDL's inline "PRIMARY KEY (id)" — drop it to simulate
	// a re-provisioned table that lost the constraint the write path's
	// ON CONFLICT (id) depends on, leaving every other posture check intact.
	pkey := pkeyConstraintName(t, ctx, pool, tbl)
	_, err = pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE %q DROP CONSTRAINT %q`, tbl, pkey))
	require.NoError(t, err)

	err = VerifyProtectedTable(ctx, pool, tbl, []string{"id"}, body, 10*time.Second)
	require.Error(t, err, "a table missing its ON CONFLICT arbiter index must fail closed")
	assert.Contains(t, err.Error(), "unique index/constraint")
}

// TestVerifyProtectedTable_SupersetUniqueConstraint_Fails proves "exactly
// covering" is not "covers": a unique index on keyCols PLUS an extra column is
// not an arbiter index for ON CONFLICT (keyCols) either (PostgreSQL 16 docs,
// INSERT "Conflict Target" — inference requires an EXACT set match), so it
// must not satisfy this gate.
func TestVerifyProtectedTable_SupersetUniqueConstraint_Fails(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_verify_superset_" + sanitize(t.Name())
	body := []ColumnDef{{Name: "status", Type: "text"}}
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	provisionProtected(t, pool, tbl, []string{"id"}, body)
	pkey := pkeyConstraintName(t, ctx, pool, tbl)
	_, err = pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE %q DROP CONSTRAINT %q`, tbl, pkey))
	require.NoError(t, err)
	// A unique index on (id, status) — a superset of the write path's
	// ON CONFLICT (id) target — cannot arbitrate it either.
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE UNIQUE INDEX ON %q (id, status)`, tbl))
	require.NoError(t, err)

	err = VerifyProtectedTable(ctx, pool, tbl, []string{"id"}, body, 10*time.Second)
	require.Error(t, err, "a unique index on a SUPERSET of the ON CONFLICT columns must not satisfy the gate")
	assert.Contains(t, err.Error(), "unique index/constraint")
}

// ── hasExactUniqueConstraint — the shape matrix its own doc comment claims ────
//
// These exercise hasExactUniqueConstraint directly (this file is package
// adapter, not adapter_test) against small standalone tables, rather than
// dragging in VerifyProtectedTable's full RLS ceremony — the predicate under
// test has nothing to do with row-level security. Every case is verified live
// against postgres:16-alpine (docker-compose.yml, our pin) in the function's
// own doc comment.

// TestHasExactUniqueConstraint_Subset_Fails proves a unique index on a SUBSET
// of cols (fewer columns than the ON CONFLICT target) does not satisfy the
// gate — the other half of "exactly", alongside the superset case above.
func TestHasExactUniqueConstraint_Subset_Fails(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_uc_subset_" + sanitize(t.Name())
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %q (a text, b text, c text)`, tbl))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE UNIQUE INDEX ON %q (a)`, tbl))
	require.NoError(t, err)

	ok, err := hasExactUniqueConstraint(ctx, pool, tbl, []string{"a", "b"})
	require.NoError(t, err)
	assert.False(t, ok, "a unique index on a SUBSET of the target columns must not satisfy the gate")
}

// TestHasExactUniqueConstraint_Include_Passes proves a covering index's
// INCLUDE columns (stored, not part of the key) do not affect the match —
// indnkeyatts bounds the comparison to the index's own key columns.
func TestHasExactUniqueConstraint_Include_Passes(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_uc_include_" + sanitize(t.Name())
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %q (a text, b text, c text)`, tbl))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE UNIQUE INDEX ON %q (a, b) INCLUDE (c)`, tbl))
	require.NoError(t, err)

	ok, err := hasExactUniqueConstraint(ctx, pool, tbl, []string{"a", "b"})
	require.NoError(t, err)
	assert.True(t, ok, "a covering index's INCLUDE columns must not affect the key-column match")
}

// TestHasExactUniqueConstraint_ColumnOrderPermutation_Passes proves the match
// is a SET comparison, not positional — PostgreSQL 16 docs, INSERT "Conflict
// Target": inference is "without regard to order".
func TestHasExactUniqueConstraint_ColumnOrderPermutation_Passes(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_uc_order_" + sanitize(t.Name())
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %q (a text, b text)`, tbl))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE UNIQUE INDEX ON %q (a, b)`, tbl))
	require.NoError(t, err)

	ok, err := hasExactUniqueConstraint(ctx, pool, tbl, []string{"b", "a"})
	require.NoError(t, err)
	assert.True(t, ok, "column declaration order must not affect the match")
}

// TestHasExactUniqueConstraint_Deferrable_Fails proves a DEFERRABLE unique
// constraint (indimmediate = false) does not satisfy the gate. PostgreSQL 16
// docs, INSERT "Conflict Target": "In all cases, only NOT DEFERRABLE
// constraints and unique indexes are supported as arbiters." Confirmed live: a
// write attempting ON CONFLICT against exactly these columns on a
// DEFERRABLE-only constraint raises "ON CONFLICT does not support deferrable
// unique constraints/exclusion constraints as arbiters" (SQLSTATE 55000).
func TestHasExactUniqueConstraint_Deferrable_Fails(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_uc_defer_" + sanitize(t.Name())
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %q (a text, b text)`, tbl))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE UNIQUE INDEX %s ON %q (a, b)`, quoteIdent(tbl+"_idx"), tbl))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, fmt.Sprintf(
		`ALTER TABLE %q ADD CONSTRAINT %s UNIQUE USING INDEX %s DEFERRABLE INITIALLY DEFERRED`,
		tbl, quoteIdent(tbl+"_uniq"), quoteIdent(tbl+"_idx")))
	require.NoError(t, err)

	var indimmediate bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT indimmediate FROM pg_index WHERE indrelid = to_regclass($1)`, tbl,
	).Scan(&indimmediate))
	require.False(t, indimmediate, "sanity: the constraint must actually be non-immediate (deferrable)")

	ok, err := hasExactUniqueConstraint(ctx, pool, tbl, []string{"a", "b"})
	require.NoError(t, err)
	assert.False(t, ok, "a DEFERRABLE unique constraint must not satisfy the gate — Postgres refuses it as an ON CONFLICT arbiter")
}

// TestHasExactUniqueConstraint_InvalidIndex_Fails proves an INVALID unique
// index (the state a `CREATE INDEX CONCURRENTLY` left broken by duplicate
// data produces) does not satisfy the gate. Source: postgres/postgres
// REL_16_STABLE src/backend/optimizer/util/plancat.c, infer_arbiter_indexes:
// "if (!idxForm->indisvalid) goto next;". Confirmed live: the same ON CONFLICT
// against an invalid index's columns raises SQLSTATE 42P10, "there is no
// unique or exclusion constraint matching the ON CONFLICT specification" — the
// same error a wholly absent constraint produces.
func TestHasExactUniqueConstraint_InvalidIndex_Fails(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_uc_invalid_" + sanitize(t.Name())
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %q (a text, b text)`, tbl))
	require.NoError(t, err)
	// Duplicate data makes the CONCURRENTLY build fail partway through,
	// leaving the index behind as indisvalid = false rather than rolling it
	// back — CREATE INDEX CONCURRENTLY does not run in a transaction block.
	_, err = pool.Exec(ctx, fmt.Sprintf(`INSERT INTO %q (a, b) VALUES ('x','y'), ('x','y')`, tbl))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE UNIQUE INDEX CONCURRENTLY %s ON %q (a, b)`, quoteIdent(tbl+"_idx"), tbl))
	require.Error(t, err, "sanity: the concurrent build must fail on the duplicate row")

	var indisvalid bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT indisvalid FROM pg_index WHERE indrelid = to_regclass($1)`, tbl,
	).Scan(&indisvalid))
	require.False(t, indisvalid, "sanity: the failed concurrent build must leave an INVALID index, not none at all")

	ok, err := hasExactUniqueConstraint(ctx, pool, tbl, []string{"a", "b"})
	require.NoError(t, err)
	assert.False(t, ok, "an INVALID unique index must not satisfy the gate — Postgres treats it as not present for arbitration")
}

// TestHasExactUniqueConstraint_ExpressionKeyColumn_Fails proves a unique index
// whose key includes an expression (not a plain column reference) does not
// satisfy the gate, even when its PLAIN key columns happen to equal cols as a
// set. unnest(indkey) yields attnum = 0 at an expression's key position;
// silently dropping that position via the pg_attribute join would produce a
// false match. Source: postgres/postgres REL_16_STABLE
// src/backend/optimizer/util/plancat.c, infer_arbiter_indexes — column
// matching is a bitmap-set comparison AND (via RelationGetIndexExpressions +
// list_difference) an expression-list comparison, so a plain-column target can
// never match an index carrying an expression key. Confirmed live: a unique
// index on (actor_id, anchor_id, grant_source, lower(x)) — mirroring
// actor_read_grants's own ON CONFLICT columns plus one expression — and an
// ON CONFLICT (actor_id, anchor_id, grant_source) both raise SQLSTATE 42P10.
func TestHasExactUniqueConstraint_ExpressionKeyColumn_Fails(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_uc_expr_" + sanitize(t.Name())
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	_, err = pool.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE %q (actor_id text, anchor_id text, grant_source text, x text)`, tbl))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, fmt.Sprintf(
		`CREATE UNIQUE INDEX ON %q (actor_id, anchor_id, grant_source, lower(x))`, tbl))
	require.NoError(t, err)

	ok, err := hasExactUniqueConstraint(ctx, pool, tbl, []string{"actor_id", "anchor_id", "grant_source"})
	require.NoError(t, err)
	assert.False(t, ok, "an index whose key includes an expression must not satisfy a plain-column target match, even when the plain columns coincide")
}

// scopedGrantPool opens a pool whose connections carry a private, freshly
// created schema as their sole search_path, so VerifyGrantTable's unqualified
// to_regclass(GrantTable) lookup resolves against a throwaway table this test
// owns exclusively — never the real actor_read_grants (shared with every
// other test in this package AND, on a dev/demo DSN, a live Refractor grant
// lens actually writing through it). pg_catalog stays implicitly searchable
// regardless of search_path, so to_regclass/pg_index/pg_attribute/pg_constraint
// all still resolve correctly. The schema (and everything in it) is dropped
// CASCADE in t.Cleanup, registered before the pool-close cleanup so it runs
// first (LIFO) while the pool is still live.
func scopedGrantPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	schema := fmt.Sprintf("t_grants_%s_%d", sanitize(t.Name()), time.Now().UnixNano())
	if len(schema) > 63 {
		schema = schema[:63] // NAMEDATALEN; the trailing nanosecond digits keep it unique.
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA %q`, schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cerr := pool.Exec(context.Background(), fmt.Sprintf(`DROP SCHEMA %q CASCADE`, schema))
		require.NoError(t, cerr, "drop this test's own scratch schema")
	})
	return pool
}

// TestVerifyGrantTable_NoUniqueConstraint_Fails is TestVerifyProtectedTable_
// NoUniqueConstraint_Fails's grant-table counterpart, isolated to a scratch
// schema (scopedGrantPool) rather than mutating the shared actor_read_grants:
// under `go test -p 4` other packages (e.g. ruleengine/full's
// capability_read_grants_lens_contract_test.go) hit the same DSN concurrently,
// and on a dev/demo DSN a live Refractor grant lens writes through the real
// table — either would take a 42P10 (a structural collision with §4.2(e)'s
// own check) during the window the constraint is dropped.
//
// The table is built WITH its PRIMARY KEY first and proven to pass — the
// positive vector for this fixture's own plumbing — before the constraint is
// dropped and the refusal asserted, so the refusal is known to be about the
// constraint and not some defect in the scratch-schema setup.
func TestVerifyGrantTable_NoUniqueConstraint_Fails(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool := scopedGrantPool(t, dsn)

	_, err := pool.Exec(ctx, `CREATE TABLE "`+GrantTable+`" (
		actor_id text NOT NULL,
		anchor_id text NOT NULL,
		grant_source text NOT NULL,
		projection_seq bigint NOT NULL,
		is_deleted boolean NOT NULL DEFAULT false,
		PRIMARY KEY (actor_id, anchor_id, grant_source)
	)`)
	require.NoError(t, err)

	w, err := NewPostgresGrantWriter(pool, 10*time.Second)
	require.NoError(t, err)
	require.NoError(t, w.VerifyGrantTable(ctx), "sanity: the scratch-schema table with its PK passes")

	pkey := pkeyConstraintName(t, ctx, pool, GrantTable)
	_, err = pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE %q DROP CONSTRAINT %q`, GrantTable, pkey))
	require.NoError(t, err)

	err = w.VerifyGrantTable(ctx)
	require.Error(t, err, "a grant table missing its ON CONFLICT arbiter index must fail closed")
	assert.Contains(t, err.Error(), "unique index/constraint")
}

// TestPostgresAdapter_GetRow_BypassesRLS pins the finding
// refractor-hub-walk-and-periodic-load-design.md §5.2 depends on: GetRow reads
// through a.pool — the same connection Upsert/Delete already use, never a
// per-actor RLS-scoped session — and Contract #6 §6.14's generated policy is
// FOR SELECT only, so whether that connection sees a row it holds no grant for
// is a property of the ROLE it connects as, not of GetRow's own SQL (rls.go:
// "writes stay governed by table GRANTs + the trusted projector posture").
//
// docker-compose.yml's POSTGRES_USER creates the connecting role, and the
// Postgres image always makes POSTGRES_USER a superuser, which bypasses row
// security unconditionally regardless of FORCE ROW LEVEL SECURITY — the same
// property rls_test.go's own integration tests rely on ("the dev/CI superuser
// bypasses RLS", hence their SET LOCAL ROLE to a dedicated non-superuser
// reader). This is proven by contrast: a role-scoped, actor-less reader sees
// zero rows for a table with no grants at all, while GetRow — no role switch —
// reads the row back over the same pool.
func TestPostgresAdapter_GetRow_BypassesRLS(t *testing.T) {
	dsn := skipIfNoPostgres(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tbl := "rls_verify_getrow_" + sanitize(t.Name())
	body := []ColumnDef{{Name: "status", Type: "text"}}
	clean := func() { _, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+tbl+`"`) }
	clean()
	t.Cleanup(clean)

	provisionProtected(t, pool, tbl, []string{"id"}, body)

	role, visibleAs := rlsReaderHarness(t, pool)
	_, err = pool.Exec(ctx, fmt.Sprintf(`GRANT SELECT ON "%s","actor_read_grants" TO %s`, tbl, role))
	require.NoError(t, err)

	a, err := NewPostgresAdapter(pool, tbl, []string{"id"}, 10*time.Second, DeleteModeHard)
	require.NoError(t, err)
	a.SetGuarded(true)

	require.NoError(t, a.Upsert(ctx, map[string]any{"id": "row1"}, map[string]any{"status": "submitted"}, 1))

	// No actor_read_grants row names any anchor for this table at all — a
	// role-scoped, RLS-subject reader must see nothing.
	require.Equal(t, 0, visibleAs(tbl, "some-actor-with-no-grant"),
		"sanity: an actor holding no grant must see nothing under RLS")

	// GetRow runs on the writer's own pool — no SET LOCAL ROLE, no
	// lattice.actor_id — and must see the row anyway.
	row, ok, err := a.GetRow(ctx, map[string]any{"id": "row1"})
	require.NoError(t, err)
	require.True(t, ok, "the writer's own connection must read the row back — it is not an RLS subject")
	assert.Equal(t, "submitted", row["status"])
}
