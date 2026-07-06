package adapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GrantTable is the shared Postgres table that merges every read-grant lens's
// rows (Contract #6 §6.14). RLS policies on protected business tables match a
// row's authz_anchors against the anchors granted to the current actor here.
const GrantTable = "actor_read_grants"

// AuthzAnchorsColumn is the platform-added array column every protected
// read-model table carries (Contract #6 §6.14): the set of bare-NanoID match
// tokens the RLS policy unnests. It is always a text[] array column.
const AuthzAnchorsColumn = "authz_anchors"

// ProjectionSeqColumn is the platform-added monotonic-guard column every
// protected read-model table carries (Contract #6 §6.14), mirroring
// actor_read_grants.projection_seq. PostgresAdapter's guarded mode conditions
// its upsert on this column so a stale (lower-seq) replay cannot overwrite a
// fresher projected row.
const ProjectionSeqColumn = "projection_seq"

// GrantKeyColumns is the composite key a grant-projecting lens RETURNs, in
// order — the primary key of actor_read_grants. A lens targeting the grant
// table projects exactly these three columns.
var GrantKeyColumns = []string{"actor_id", "anchor_id", "grant_source"}

// WildcardAnchor is the reserved all-access anchor_id (Contract #6 §6.14, D1
// design M5): a grant row `(actor_id, '*', grant_source)` makes actor_id able
// to read EVERY row of EVERY protected table, regardless of that row's
// authz_anchors. It is never a resource anchor itself — the platform NanoID
// alphabet (internal/substrate.Alphabet) excludes '*', so no real anchor_id
// can ever collide with it. This is the read-side mirror of the write path's
// scope:"any" root grant: a wildcard row still flows through the SAME §6.14
// set-membership policy (never an RLS bypass), so an all-access read stays
// attributable via actor_read_grants — revocable and traceable to a grant
// row exactly like any other read (no separate audit log exists today; this
// is the same posture every other RLS-scoped read already has).
const WildcardAnchor = "*"

// ColumnDef declares one column of a generated protected read-model table.
// Type is the verbatim Postgres column type (e.g. "text", "bigint", "jsonb");
// the caller (the protected lens) owns the type because the lens RETURN — not
// the lens spec — knows each projected field's shape.
type ColumnDef struct {
	Name string
	Type string
}

// validateIdent rejects an empty identifier or one carrying a double-quote, so a
// quoted identifier cannot break out of its quoting. Mirrors the guard in
// NewPostgresAdapter / NewPostgresGrantWriter.
func validateIdent(kind, name string) error {
	if name == "" {
		return fmt.Errorf("rls: %s must not be empty", kind)
	}
	if strings.ContainsRune(name, '"') {
		return fmt.Errorf("rls: %s %q must not contain double-quote characters", kind, name)
	}
	return nil
}

// policyName derives the deterministic RLS policy name for a table:
// rls_<table>. The table identifier is validated by the caller.
func policyName(table string) string {
	return "rls_" + table
}

// BuildProtectedTableDDL generates the ordered DDL statements that provision a
// protected read-model table under row-level security (Contract #6 §6.14,
// brainstorm #38's RLS-policy generator). Deriving the DDL+policy from the lens
// spec keeps schema and projection from drifting and makes FORCE RLS structural
// rather than a checklist item.
//
// The generated table carries the caller's key columns (text, NOT NULL), the
// caller's body columns (verbatim type), and two platform columns: authz_anchors
// (text[], the §6.14 set of bare-NanoID match tokens) and projection_seq
// (bigint). The set-membership policy makes a row visible iff the current actor
// holds a grant for ANY of its authz_anchors.
//
// Every protected table is created with ENABLE + FORCE ROW LEVEL SECURITY, so a
// table whose policy was never generated denies all rows (a fail-closed outage,
// never a silent leak — §6.14 H3). current_setting('lattice.actor_id', true)
// returns NULL when the boundary never set the actor, so the IN matches nothing
// and the read is denied.
//
// Statements use IF NOT EXISTS / idempotent forms so re-running at every lens
// activation is safe. keyCols must be non-empty and free of duplicates; all
// identifiers are validated and double-quoted.
func BuildProtectedTableDDL(table string, keyCols []string, body []ColumnDef) ([]string, error) {
	if err := validateIdent("table", table); err != nil {
		return nil, err
	}
	if len(keyCols) == 0 {
		return nil, fmt.Errorf("rls: keyCols must not be empty")
	}
	seen := make(map[string]struct{}, len(keyCols))
	for _, k := range keyCols {
		if err := validateIdent("key column", k); err != nil {
			return nil, err
		}
		if _, dup := seen[k]; dup {
			return nil, fmt.Errorf("rls: keyCols contains duplicate field %q", k)
		}
		seen[k] = struct{}{}
	}
	for _, c := range body {
		if err := validateIdent("body column", c.Name); err != nil {
			return nil, err
		}
		if _, isKey := seen[c.Name]; isKey {
			return nil, fmt.Errorf("rls: body column %q duplicates a key column", c.Name)
		}
		if strings.TrimSpace(c.Type) == "" {
			return nil, fmt.Errorf("rls: body column %q has empty type", c.Name)
		}
		if strings.ContainsRune(c.Type, ';') {
			return nil, fmt.Errorf("rls: body column %q type %q must not contain ';'", c.Name, c.Type)
		}
	}

	colDefs := make([]string, 0, len(keyCols)+len(body)+2)
	for _, k := range keyCols {
		colDefs = append(colDefs, fmt.Sprintf("%s text NOT NULL", quoteIdent(k)))
	}
	for _, c := range body {
		colDefs = append(colDefs, fmt.Sprintf("%s %s", quoteIdent(c.Name), c.Type))
	}
	colDefs = append(colDefs,
		"authz_anchors text[] NOT NULL DEFAULT '{}'",
		"projection_seq bigint NOT NULL DEFAULT 0",
	)
	quotedKeys := make([]string, len(keyCols))
	for i, k := range keyCols {
		quotedKeys[i] = quoteIdent(k)
	}
	colDefs = append(colDefs, fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(quotedKeys, ", ")))

	qt := quoteIdent(table)
	pol := quoteIdent(policyName(table))

	createTable := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n  %s\n)", qt, strings.Join(colDefs, ",\n  "))
	enableRLS := fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY", qt)
	forceRLS := fmt.Sprintf("ALTER TABLE %s FORCE ROW LEVEL SECURITY", qt)
	// CREATE POLICY has no IF NOT EXISTS; DROP-then-CREATE makes activation
	// idempotent and lets a policy revision (e.g. a new column) take effect on
	// the next activation without manual intervention.
	//
	// FOR SELECT scopes the filter to the read path. A FOR ALL policy's USING
	// clause is also applied as the INSERT/UPDATE WITH CHECK, which would reject
	// a non-superuser writer that sets no lattice.actor_id (the trusted Refractor
	// projector) — so the policy governs reads only; writes stay governed by
	// table GRANTs + the trusted projector posture (P2). FORCE RLS still
	// deny-alls reads on a table whose policy was never generated (H3).
	//
	// The second EXISTS is the WildcardAnchor escape hatch (§6.14 M5): a grant
	// row anchored '*' matches every row of THIS table regardless of its
	// authz_anchors — the read-side mirror of the write path's scope:"any" root
	// grant. It is still a §6.14 set-membership lookup against actor_read_grants
	// (seq-guarded, revocable, NOT is_deleted-filtered) — never a bypass of the
	// policy itself.
	dropPolicy := fmt.Sprintf("DROP POLICY IF EXISTS %s ON %s", pol, qt)
	createPolicy := fmt.Sprintf(
		"CREATE POLICY %s ON %s FOR SELECT USING (\n"+
			"  EXISTS (SELECT 1 FROM %s\n"+
			"          WHERE actor_id = current_setting('lattice.actor_id', true)\n"+
			"            AND anchor_id = '*'\n"+
			"            AND NOT is_deleted)\n"+
			"  OR\n"+
			"  EXISTS (SELECT 1 FROM unnest(authz_anchors) a\n"+
			"          WHERE a IN (SELECT anchor_id FROM %s\n"+
			"                      WHERE actor_id = current_setting('lattice.actor_id', true)\n"+
			"                        AND NOT is_deleted))\n"+
			")",
		pol, qt, quoteIdent(GrantTable), quoteIdent(GrantTable),
	)

	return []string{createTable, enableRLS, forceRLS, dropPolicy, createPolicy}, nil
}

// BuildGrantTableDDL generates the DDL for the shared actor_read_grants table
// (Contract #6 §6.14 — the read-auth source of truth). One row per
// (actor_id, anchor_id, grant_source): grant_source (the contributing lens's
// canonical name) keeps producers disjoint so a revoke from one package never
// wipes another's coexisting grant.
//
// The table carries an is_deleted tombstone column (§6.14's five-column grant
// schema): §6.14 mandates that a delete "applies only when its incoming
// projectionSeq exceeds the stored one" and that "a stale CDC replay cannot
// resurrect a revoked grant" — both require the revoked row's projection_seq to
// be RETAINED, which a hard DELETE discards (a later stale re-insert would then
// resurrect the grant). Revocation is therefore a seq-guarded soft tombstone;
// the RLS policy and the membership lookup filter NOT is_deleted. This reuses
// the existing Postgres soft-delete convention (DeleteModeSoft).
func BuildGrantTableDDL() []string {
	return []string{
		fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n"+
			"  actor_id text NOT NULL,\n"+
			"  anchor_id text NOT NULL,\n"+
			"  grant_source text NOT NULL,\n"+
			"  projection_seq bigint NOT NULL,\n"+
			"  is_deleted boolean NOT NULL DEFAULT false,\n"+
			"  PRIMARY KEY (actor_id, anchor_id, grant_source)\n"+
			")", quoteIdent(GrantTable)),
	}
}

// PostgresGrantWriter provisions and maintains the actor_read_grants table with
// the §6.14 monotonic-seq guard. It is the Postgres write seam the cap-read.*
// lenses project through; it is NOT a generic Adapter (the grant table needs the
// seq-guard the business-table adapter is deliberately exempt from, §6.2).
type PostgresGrantWriter struct {
	pool         *pgxpool.Pool
	queryTimeout time.Duration
}

// NewPostgresGrantWriter creates a grant writer over a shared pool (from
// PoolManager.Acquire). pool must be non-nil. queryTimeout is applied per
// operation; 0 means no timeout.
func NewPostgresGrantWriter(pool *pgxpool.Pool, queryTimeout time.Duration) (*PostgresGrantWriter, error) {
	if pool == nil {
		return nil, fmt.Errorf("grant writer: pool must not be nil")
	}
	return &PostgresGrantWriter{pool: pool, queryTimeout: queryTimeout}, nil
}

func (w *PostgresGrantWriter) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if w.queryTimeout > 0 {
		return context.WithTimeout(ctx, w.queryTimeout)
	}
	return ctx, func() {}
}

// Probe checks the pool can reach the server (mirrors PostgresAdapter.Probe),
// so a grant-writer-backed pipeline participates in the infrastructure-pause
// probe loop like any other adapter.
func (w *PostgresGrantWriter) Probe(ctx context.Context) error {
	return w.pool.Ping(ctx)
}

// Provision creates the actor_read_grants table if it does not exist.
// Idempotent — safe to call at every grant-lens activation.
func (w *PostgresGrantWriter) Provision(ctx context.Context) error {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	for _, stmt := range BuildGrantTableDDL() {
		if _, err := w.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("grant writer: provision: %w", err)
		}
	}
	return nil
}

// UpsertGrant records a live grant of anchorID to actorID from grantSource,
// applying the §6.14 monotonic-seq guard: the write takes effect only when
// projectionSeq strictly exceeds the stored projection_seq for this
// (actor_id, anchor_id, grant_source). A stale CDC replay (lower-or-equal seq)
// is a no-op, so it can neither downgrade a fresh grant nor resurrect a revoked
// one. A previously-tombstoned row is revived (is_deleted ← false) only by a
// strictly-newer seq.
func (w *PostgresGrantWriter) UpsertGrant(ctx context.Context, actorID, anchorID, grantSource string, projectionSeq uint64) error {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	const q = `INSERT INTO ` + `"` + GrantTable + `"` + ` (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
VALUES ($1, $2, $3, $4, false)
ON CONFLICT (actor_id, anchor_id, grant_source)
DO UPDATE SET projection_seq = EXCLUDED.projection_seq, is_deleted = false
WHERE EXCLUDED.projection_seq > ` + `"` + GrantTable + `"` + `.projection_seq`
	_, err := w.pool.Exec(ctx, q, actorID, anchorID, grantSource, int64(projectionSeq))
	if err != nil {
		return fmt.Errorf("grant writer: upsert: %w", err)
	}
	return nil
}

// RevokeGrant tombstones a grant (is_deleted ← true) under the same monotonic
// guard: the revoke takes effect only when projectionSeq strictly exceeds the
// stored projection_seq. The row and its seq are RETAINED so a later stale
// upsert at a lower seq cannot resurrect the grant (§6.14). A revoke for a row
// that was never granted inserts a tombstone at the revoke seq (so an
// out-of-order stale upsert that arrives afterward is still guarded).
func (w *PostgresGrantWriter) RevokeGrant(ctx context.Context, actorID, anchorID, grantSource string, projectionSeq uint64) error {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	const q = `INSERT INTO ` + `"` + GrantTable + `"` + ` (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
VALUES ($1, $2, $3, $4, true)
ON CONFLICT (actor_id, anchor_id, grant_source)
DO UPDATE SET projection_seq = EXCLUDED.projection_seq, is_deleted = true
WHERE EXCLUDED.projection_seq > ` + `"` + GrantTable + `"` + `.projection_seq`
	_, err := w.pool.Exec(ctx, q, actorID, anchorID, grantSource, int64(projectionSeq))
	if err != nil {
		return fmt.Errorf("grant writer: revoke: %w", err)
	}
	return nil
}

// ProvisionProtectedTable runs BuildProtectedTableDDL against the pool, creating
// (idempotently) the protected read-model table with FORCE ROW LEVEL SECURITY
// and the §6.14 set-membership policy. Called at protected-lens activation,
// AFTER the actor_read_grants table exists (the policy references it). timeout,
// when positive, bounds the DDL batch.
func ProvisionProtectedTable(ctx context.Context, pool *pgxpool.Pool, table string, keyCols []string, body []ColumnDef, timeout time.Duration) error {
	if pool == nil {
		return fmt.Errorf("rls: provision: pool must not be nil")
	}
	stmts, err := BuildProtectedTableDDL(table, keyCols, body)
	if err != nil {
		return err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	for _, stmt := range stmts {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("rls: provision %q: %w", table, err)
		}
	}
	return nil
}

// VerifyProtectedTable performs the read-only posture verification a protected
// read-model table must pass before its lens projects (Contract #6 §6.14,
// out-of-band provisioning verify-and-pause). It issues NO DDL and NO writes —
// only system-catalog reads — so the operator owns provisioning while Refractor
// refuses to project into a table that is not locked down. It is the Probe a
// protected lens runs while infra-paused at activation, so a failure keeps the
// lens dark and the probe loop re-verifies until the operator provisions the
// table out-of-band.
//
// It gates, in priority order:
//
//   - The table exists, is an ordinary table (relkind 'r'), and has row-level
//     security BOTH enabled (relrowsecurity) AND forced (relforcerowsecurity) —
//     the SECURITY-critical bit. RLS is inactive unless ENABLE is set (FORCE
//     alone, without ENABLE, leaves the table world-readable), and FORCE is what
//     also subjects the table owner. With both on, a missing or wrong policy
//     denies all rows (a fail-closed outage, never a leak — §6.14 H3); with
//     either off, the table is readable. An absent table fails here.
//   - the expected columns present with the platform types (authz_anchors is
//     exactly text[], projection_seq is bigint, every key + body column present) —
//     a missing/mistyped column would fail the write anyway; verifying up front
//     turns a per-row write error into a clean activation pause with a named
//     column.
//   - the §6.14 set-membership SELECT policy present and intact — not merely that
//     SOME SELECT policy exists (a permissive USING(true) policy would over-share
//     under FORCE RLS), but that the deterministically-named policy exists and its
//     USING expression references the authz-anchors column and the grant table. A
//     trusted operator adding a SECOND permissive policy alongside is outside the
//     threat model (same class as deliberately disabling FORCE); this gate catches
//     the realistic mistake of a missing or hand-wrong membership policy.
//
// Every failure is a plain (untagged) error so failure.Classify treats it as
// recoverable (the default transient tier), not a structural pg error that would
// escalate the pump to an operator-Resume pause. The lookups use to_regclass
// (NULL when absent) for the same reason — an absent table surfaces as this
// descriptive error, not a structural 42P01.
func VerifyProtectedTable(ctx context.Context, pool *pgxpool.Pool, table string, keyCols []string, body []ColumnDef, timeout time.Duration) error {
	if pool == nil {
		return fmt.Errorf("rls: verify: pool must not be nil")
	}
	if err := validateIdent("table", table); err != nil {
		return err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// 1. SECURITY-critical: the table exists, is an ordinary table, and RLS is
	// both ENABLED and FORCED. ENABLE (relrowsecurity) is what makes policies
	// apply at all — FORCE without ENABLE leaves the table world-readable — and
	// FORCE (relforcerowsecurity) extends enforcement to the table owner.
	var relkind string
	var rowSec, forceRowSec bool
	err := pool.QueryRow(ctx,
		`SELECT relkind::text, relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass($1)`,
		table,
	).Scan(&relkind, &rowSec, &forceRowSec)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("rls: verify %q: table is absent — provision it out-of-band", table)
	}
	if err != nil {
		return fmt.Errorf("rls: verify %q: read pg_class: %w", table, err)
	}
	if relkind != "r" {
		return fmt.Errorf("rls: verify %q: not an ordinary table (relkind %q) — row-level security does not apply", table, relkind)
	}
	if !rowSec {
		return fmt.Errorf("rls: verify %q: ROW LEVEL SECURITY is not ENABLED — refusing to project (the table would be world-readable)", table)
	}
	if !forceRowSec {
		return fmt.Errorf("rls: verify %q: FORCE ROW LEVEL SECURITY is not enabled — refusing to project (the table owner would bypass RLS)", table)
	}

	// 2. Functional: the expected columns are present with the platform types
	// (exact Postgres type names via pg_attribute/format_type, resolved against
	// the same relation to_regclass found).
	cols, err := tableColumns(ctx, pool, table)
	if err != nil {
		return fmt.Errorf("rls: verify %q: %w", table, err)
	}
	for _, k := range keyCols {
		if _, ok := cols[k]; !ok {
			return fmt.Errorf("rls: verify %q: missing key column %q", table, k)
		}
	}
	for _, c := range body {
		if _, ok := cols[c.Name]; !ok {
			return fmt.Errorf("rls: verify %q: missing body column %q", table, c.Name)
		}
	}
	if dt, ok := cols[AuthzAnchorsColumn]; !ok {
		return fmt.Errorf("rls: verify %q: missing %s column", table, AuthzAnchorsColumn)
	} else if dt != "text[]" {
		return fmt.Errorf("rls: verify %q: %s must be text[], found %q", table, AuthzAnchorsColumn, dt)
	}
	if dt, ok := cols["projection_seq"]; !ok {
		return fmt.Errorf("rls: verify %q: missing projection_seq column", table)
	} else if dt != "bigint" {
		return fmt.Errorf("rls: verify %q: projection_seq must be bigint, found %q", table, dt)
	}

	// 3. The §6.14 membership policy is present and intact: the deterministically
	// named SELECT policy exists (polcmd 'r' = SELECT, '*' = ALL) and its USING
	// expression filters on the authz-anchors column against the grant table — so
	// a missing, mis-named, or USING(true) policy is rejected, not silently served.
	var qual string
	err = pool.QueryRow(ctx,
		`SELECT pg_get_expr(polqual, polrelid) FROM pg_policy
		 WHERE polrelid = to_regclass($1) AND polname = $2 AND polcmd IN ('r', '*')`,
		table, policyName(table),
	).Scan(&qual)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("rls: verify %q: no SELECT policy %q — the read model is dark or mis-provisioned", table, policyName(table))
	}
	if err != nil {
		return fmt.Errorf("rls: verify %q: read pg_policy: %w", table, err)
	}
	if !strings.Contains(qual, AuthzAnchorsColumn) || !strings.Contains(qual, GrantTable) {
		return fmt.Errorf("rls: verify %q: SELECT policy %q does not enforce §6.14 set-membership (USING must filter %s against %s) — refusing to project", table, policyName(table), AuthzAnchorsColumn, GrantTable)
	}
	return nil
}

// VerifyGrantTable performs the read-only posture verification for the shared
// actor_read_grants table (Contract #6 §6.14). It issues no DDL: it asserts the
// table exists with the expected columns and types, so the seq-guarded
// Upsert/RevokeGrant writes and every protected policy's membership subquery have
// the shape they depend on. Like VerifyProtectedTable it returns plain
// (recoverable) errors so a grant lens auto-resumes once the operator provisions
// the table out-of-band. The grant table is the read-auth source of truth, not a
// protected business table, so it is not itself RLS-locked — only its shape is
// verified.
func (w *PostgresGrantWriter) VerifyGrantTable(ctx context.Context) error {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	want := map[string]string{
		"actor_id":       "text",
		"anchor_id":      "text",
		"grant_source":   "text",
		"projection_seq": "bigint",
		"is_deleted":     "boolean",
	}
	got, err := tableColumns(ctx, w.pool, GrantTable)
	if err != nil {
		return fmt.Errorf("grant writer: verify: %w", err)
	}
	if len(got) == 0 {
		return fmt.Errorf("grant writer: verify: table %q is absent — provision it out-of-band", GrantTable)
	}
	for col, typ := range want {
		dt, ok := got[col]
		if !ok {
			return fmt.Errorf("grant writer: verify: %q missing column %q", GrantTable, col)
		}
		if dt != typ {
			return fmt.Errorf("grant writer: verify: %q column %q must be %s, found %q", GrantTable, col, typ, dt)
		}
	}
	return nil
}

// tableColumns reads a table's column-name → exact-Postgres-type map via
// pg_attribute/format_type ("text", "bigint", "text[]", "boolean", …), resolved
// against the relation to_regclass finds — so it is consistent with the pg_class
// and pg_policy lookups (no search_path divergence) and distinguishes text[] from
// any other array type. An absent table (or one with no live columns) yields an
// empty map (no error), so the caller distinguishes "absent" from "wrong shape".
func tableColumns(ctx context.Context, pool *pgxpool.Pool, table string) (map[string]string, error) {
	rows, err := pool.Query(ctx,
		`SELECT a.attname, format_type(a.atttypid, a.atttypmod)
		 FROM pg_attribute a
		 WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped`,
		table,
	)
	if err != nil {
		return nil, fmt.Errorf("read columns: %w", err)
	}
	defer rows.Close()
	cols := make(map[string]string)
	for rows.Next() {
		var name, dtype string
		if err := rows.Scan(&name, &dtype); err != nil {
			return nil, fmt.Errorf("scan column: %w", err)
		}
		cols[name] = dtype
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate columns: %w", err)
	}
	return cols, nil
}
