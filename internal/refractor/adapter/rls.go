package adapter

import (
	"context"
	"fmt"
	"strings"
	"time"

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

// GrantKeyColumns is the composite key a grant-projecting lens RETURNs, in
// order — the primary key of actor_read_grants. A lens targeting the grant
// table projects exactly these three columns.
var GrantKeyColumns = []string{"actor_id", "anchor_id", "grant_source"}

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
	dropPolicy := fmt.Sprintf("DROP POLICY IF EXISTS %s ON %s", pol, qt)
	createPolicy := fmt.Sprintf(
		"CREATE POLICY %s ON %s FOR SELECT USING (\n"+
			"  EXISTS (SELECT 1 FROM unnest(authz_anchors) a\n"+
			"          WHERE a IN (SELECT anchor_id FROM %s\n"+
			"                      WHERE actor_id = current_setting('lattice.actor_id', true)\n"+
			"                        AND NOT is_deleted))\n"+
			")",
		pol, qt, quoteIdent(GrantTable),
	)

	return []string{createTable, enableRLS, forceRLS, dropPolicy, createPolicy}, nil
}

// BuildGrantTableDDL generates the DDL for the shared actor_read_grants table
// (Contract #6 §6.14 — the read-auth source of truth). One row per
// (actor_id, anchor_id, grant_source): grant_source (the contributing lens's
// canonical name) keeps producers disjoint so a revoke from one package never
// wipes another's coexisting grant.
//
// Unlike the contract's illustrative four-column shape, the table carries an
// is_deleted tombstone column: §6.14 mandates that a delete "applies only when
// its incoming projectionSeq exceeds the stored one" and that "a stale CDC
// replay cannot resurrect a revoked grant" — both require the revoked row's
// projection_seq to be RETAINED, which a hard DELETE discards (a later stale
// re-insert would then resurrect the grant). Revocation is therefore a
// seq-guarded soft tombstone; the RLS policy and the membership lookup filter
// NOT is_deleted. This reuses the existing Postgres soft-delete convention
// (DeleteModeSoft). (Staged §6.14 schema clarification — flagged for Andrew.)
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
