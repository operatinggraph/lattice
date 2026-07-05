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

	// create + enable + force (stmts[:3]), then a permissive policy under the
	// deterministic name instead of the §6.14 membership policy.
	stmts, err := BuildProtectedTableDDL(tbl, []string{"id"}, body)
	require.NoError(t, err)
	for _, s := range stmts[:3] {
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

	// Run create + enable + force, but SKIP the policy (stmts[3], stmts[4]).
	stmts, err := BuildProtectedTableDDL(tbl, []string{"id"}, body)
	require.NoError(t, err)
	for _, s := range stmts[:3] {
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
