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

// IsDeletedColumn and DeletedAtColumn are the platform-added tombstone columns
// every protected read-model table carries (Contract #6 §6.14), mirroring
// actor_read_grants' own is_deleted column. A guarded PostgresAdapter's Delete
// retains the row as a seq-guarded soft tombstone instead of a hard DELETE, so
// ProjectionSeqColumn's watermark survives the delete the same way it survives
// on actor_read_grants — a hard delete would discard the watermark, letting a
// later stale replay's ON CONFLICT guard find no row to compare against and
// resurrect it unconditionally. The generated RLS policy denies a tombstoned
// row to every reader regardless of anchor membership.
const (
	IsDeletedColumn = "is_deleted"
	DeletedAtColumn = "deleted_at"
)

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
// caller's body columns (verbatim type), and four platform columns:
// authz_anchors (text[], the §6.14 set of bare-NanoID match tokens),
// projection_seq (bigint), and is_deleted/deleted_at (the guarded Delete's
// seq-guarded soft tombstone, mirroring actor_read_grants). The set-membership
// policy makes a row visible iff it is not tombstoned AND the current actor
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
		"is_deleted boolean NOT NULL DEFAULT false",
		"deleted_at timestamptz",
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
	// The leading "NOT is_deleted" denies a tombstoned row (a guarded Delete's
	// soft tombstone, above) to every reader regardless of anchor membership —
	// a hard DELETE would instead have discarded the row's projection_seq
	// watermark, letting a stale replay resurrect it (see IsDeletedColumn).
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
			"  NOT is_deleted\n"+
			"  AND (\n"+
			"    EXISTS (SELECT 1 FROM %s\n"+
			"            WHERE actor_id = current_setting('lattice.actor_id', true)\n"+
			"              AND anchor_id = '*'\n"+
			"              AND NOT is_deleted)\n"+
			"    OR\n"+
			"    EXISTS (SELECT 1 FROM unnest(authz_anchors) a\n"+
			"            WHERE a IN (SELECT anchor_id FROM %s\n"+
			"                        WHERE actor_id = current_setting('lattice.actor_id', true)\n"+
			"                          AND NOT is_deleted))\n"+
			"  )\n"+
			")",
		pol, qt, quoteIdent(GrantTable), quoteIdent(GrantTable),
	)

	// CREATE TABLE IF NOT EXISTS is a no-op on a table that already exists —
	// INCLUDING when the lens has since declared a new column. So a lens that
	// gains one is "provisioned" onto a table that will never have it, and the
	// first projection after the change fails its upsert with SQLSTATE 42703.
	// That is not a slow read model: the adapter reports a STRUCTURAL failure,
	// the consumer pauses until an explicit resume, and the pause survives a
	// process cycle. The read model ends up frozen with no operable recovery,
	// from a package edit whose author had every reason to believe the emitted
	// DDL covered them.
	//
	// So the emitted DDL CONVERGES the table rather than only creating it: one
	// idempotent ADD COLUMN IF NOT EXISTS per declared column, after the
	// create. On a fresh table each is a no-op; on an existing one they are the
	// difference between a working lens and a paused one.
	//
	// Additive only. A column the lens no longer declares is left in place, and
	// a changed type is left alone — dropping or rewriting a column is data
	// loss this path must never perform unasked. Both remaining cases stay
	// loud: an undeclared column is simply never written, and a genuinely
	// incompatible type still fails the upsert, which is exactly the case that
	// should reach a human.
	// Key columns are NOT converged here, and the omission is deliberate: they
	// are the PRIMARY KEY, so a table missing one is not an older version of
	// this table, it is a different table — and ADD COLUMN cannot make an
	// existing row's new key column NOT NULL anyway. A changed key set is a
	// migration a human owns.
	alters := make([]string, 0, len(body)+4)
	addColumn := func(name, typ string) {
		alters = append(alters, fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s",
			qt, quoteIdent(name), typ))
	}
	for _, c := range body {
		addColumn(c.Name, c.Type)
	}
	// The platform's own four, converged by the same pass so a table
	// provisioned before any one of them was introduced picks it up too.
	addColumn("authz_anchors", "text[] NOT NULL DEFAULT '{}'")
	addColumn("projection_seq", "bigint NOT NULL DEFAULT 0")
	addColumn("is_deleted", "boolean NOT NULL DEFAULT false")
	addColumn("deleted_at", "timestamptz")

	stmts := make([]string, 0, 5+len(alters))
	stmts = append(stmts, createTable)
	stmts = append(stmts, alters...)
	// RLS and the policy come after the columns exist, so a policy that
	// references a newly added one compiles.
	return append(stmts, enableRLS, forceRLS, dropPolicy, createPolicy), nil
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
	_, err := w.UpsertGrantWithOutcome(ctx, actorID, anchorID, grantSource, projectionSeq)
	return err
}

// UpsertGrantWithOutcome is UpsertGrant plus whether the grant row actually
// changed: false when the monotonic guard declined, which the statement signals
// by matching no row and returning no error. A caller reading only the error
// cannot tell that apart from a grant that landed, and on this table the
// difference is a live read grant existing or not.
func (w *PostgresGrantWriter) UpsertGrantWithOutcome(ctx context.Context, actorID, anchorID, grantSource string, projectionSeq uint64) (committed bool, err error) {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	const q = `INSERT INTO ` + `"` + GrantTable + `"` + ` (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
VALUES ($1, $2, $3, $4, false)
ON CONFLICT (actor_id, anchor_id, grant_source)
DO UPDATE SET projection_seq = EXCLUDED.projection_seq, is_deleted = false
WHERE EXCLUDED.projection_seq > ` + `"` + GrantTable + `"` + `.projection_seq`
	tag, err := w.pool.Exec(ctx, q, actorID, anchorID, grantSource, int64(projectionSeq))
	if err != nil {
		return false, fmt.Errorf("grant writer: upsert: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RevokeGrant tombstones a grant (is_deleted ← true) under the same monotonic
// guard: the revoke takes effect only when projectionSeq strictly exceeds the
// stored projection_seq. The row and its seq are RETAINED so a later stale
// upsert at a lower seq cannot resurrect the grant (§6.14). A revoke for a row
// that was never granted inserts a tombstone at the revoke seq (so an
// out-of-order stale upsert that arrives afterward is still guarded).
func (w *PostgresGrantWriter) RevokeGrant(ctx context.Context, actorID, anchorID, grantSource string, projectionSeq uint64) error {
	_, err := w.RevokeGrantWithOutcome(ctx, actorID, anchorID, grantSource, projectionSeq)
	return err
}

// RevokeGrantWithOutcome is RevokeGrant plus whether the tombstone actually
// landed. This is the direction where a silent no-op OVER-GRANTS: a revocation
// reported as done, on a row that is still live, tells the platform a capability
// was withdrawn when the RLS policy will keep honouring it.
//
// A zero row count means the guard declined, and can mean nothing else. The
// statement's INSERT arm fires whenever the (actor_id, anchor_id, grant_source)
// row is absent — that is what plants the guarding tombstone RevokeGrant's own
// contract promises — so "already gone" costs a row too, and never lands here.
// The only path that touches no row is the ON CONFLICT WHERE clause failing,
// which is the monotonic comparison itself. The absent-key ambiguity a hard
// DELETE would carry does not arise, because nothing here deletes.
func (w *PostgresGrantWriter) RevokeGrantWithOutcome(ctx context.Context, actorID, anchorID, grantSource string, projectionSeq uint64) (committed bool, err error) {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	const q = `INSERT INTO ` + `"` + GrantTable + `"` + ` (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
VALUES ($1, $2, $3, $4, true)
ON CONFLICT (actor_id, anchor_id, grant_source)
DO UPDATE SET projection_seq = EXCLUDED.projection_seq, is_deleted = true
WHERE EXCLUDED.projection_seq > ` + `"` + GrantTable + `"` + `.projection_seq`
	tag, err := w.pool.Exec(ctx, q, actorID, anchorID, grantSource, int64(projectionSeq))
	if err != nil {
		return false, fmt.Errorf("grant writer: revoke: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RevokeAllGrantsForActor tombstones every actor_read_grants row for
// actorID, across every grant_source, under the same monotonic-seq guard as
// RevokeGrant. Unlike RevokeGrant and every other write path here it is NOT
// scoped to one lens's declared grant_source: this is the crypto-shredding
// entry point (keyshredded.GrantTableRevoker), and a shred revokes the
// actor's OWN read grants regardless of which producer wrote them.
// actor_id is the row's own key column, so scoping by it alone cannot touch
// another actor's rows — the cross-producer isolation every other method
// enforces via grant_source has nothing to protect here.
//
// A row this actor never had is left absent: unlike RevokeGrant, there is no
// fixed (anchor_id, grant_source) pair to insert a guarding placeholder
// tombstone for ahead of time — the actor's full row set is not known without
// reading it first, and a shred is meant to be one write, not a read-then-
// write. A grant projected for this actor at a HIGHER seq after this call
// (a race, not the expected shred ordering) is therefore not preemptively
// guarded — best-effort, consistent with keyshredded's own package-level
// framing (belt-and-suspenders in Phase A, not a permanent guarantee).
func (w *PostgresGrantWriter) RevokeAllGrantsForActor(ctx context.Context, actorID string, projectionSeq uint64) error {
	_, err := w.RevokeAllGrantsForActorWithOutcome(ctx, actorID, projectionSeq)
	return err
}

// RevokeAllGrantsForActorWithOutcome is RevokeAllGrantsForActor plus the number
// of rows it tombstoned.
//
// The count is reported, not a committed/declined verdict, because a plain
// UPDATE cannot separate the two: zero rows means either the actor held no
// grants (nothing to withdraw) or every row it held is already at or above the
// token (all declined). Claiming one reading would be a guess, and on this table
// the wrong guess is silence about grants that survived a shred.
//
// The distinction is not load-bearing for the caller this exists for.
// keyshredded's Manager passes math.MaxInt64 as the token, and no stored
// projection_seq can equal or exceed that, so under the shred the guard cannot
// decline and a zero count means the actor genuinely held no rows.
func (w *PostgresGrantWriter) RevokeAllGrantsForActorWithOutcome(ctx context.Context, actorID string, projectionSeq uint64) (revoked int64, err error) {
	if actorID == "" {
		return 0, fmt.Errorf("grant writer: revoke all: actorID must not be empty")
	}
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	const q = `UPDATE ` + `"` + GrantTable + `"` + ` SET projection_seq = $2, is_deleted = true
WHERE actor_id = $1 AND projection_seq < $2`
	tag, err := w.pool.Exec(ctx, q, actorID, int64(projectionSeq))
	if err != nil {
		return 0, fmt.Errorf("grant writer: revoke all: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListGrantsBySource returns the live (non-tombstoned) grants carrying
// grantSource, each as an (actor_id, anchor_id, grant_source) key map. It is
// the read half of a grant lens's DiffRetraction: the pipeline diffs this
// against a fresh re-projection and revokes what the projection no longer
// produces. The grant_source predicate confines that diff to the calling
// lens's own rows — actor_read_grants is shared, so an unfiltered enumeration
// would hand one producer every other producer's grants to retract. A tombstone
// is an UPDATE, not a row removal, so is_deleted rows are excluded from the
// "currently live" set (mirroring PostgresAdapter.buildListKeysSQL).
func (w *PostgresGrantWriter) ListGrantsBySource(ctx context.Context, grantSource string) ([]map[string]any, error) {
	if grantSource == "" {
		return nil, fmt.Errorf("grant writer: list grants: grantSource must not be empty")
	}
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	const q = `SELECT actor_id, anchor_id, grant_source FROM ` + `"` + GrantTable + `"` +
		` WHERE grant_source = $1 AND NOT is_deleted`
	rows, err := w.pool.Query(ctx, q, grantSource)
	if err != nil {
		return nil, fmt.Errorf("grant writer: list grants: %w", err)
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var actor, anchor, source string
		if err := rows.Scan(&actor, &anchor, &source); err != nil {
			return nil, fmt.Errorf("grant writer: list grants: scan: %w", err)
		}
		out = append(out, map[string]any{
			GrantKeyColumns[0]: actor,
			GrantKeyColumns[1]: anchor,
			GrantKeyColumns[2]: source,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("grant writer: list grants: %w", err)
	}
	return out, nil
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
//     exactly text[], projection_seq is bigint, is_deleted is boolean,
//     deleted_at is timestamptz, every key + body column present) — a
//     missing/mistyped column would fail the write anyway; verifying up front
//     turns a per-row write error into a clean activation pause with a named
//     column.
//   - the §6.14 set-membership SELECT policy present and intact — not merely that
//     SOME SELECT policy exists (a permissive USING(true) policy would over-share
//     under FORCE RLS), but that the deterministically-named policy exists and its
//     USING expression references the authz-anchors column and the grant table. A
//     trusted operator adding a SECOND permissive policy alongside is outside the
//     threat model (same class as deliberately disabling FORCE); this gate catches
//     the realistic mistake of a missing or hand-wrong membership policy.
//   - a unique index/constraint exactly covering keyCols — the same columns the
//     write path's ON CONFLICT (postgres.go) targets. Postgres's arbiter-index
//     inference requires an exact set match (see hasExactUniqueConstraint), so a
//     table re-provisioned without it would otherwise pass every check above and
//     then fail every write with SQLSTATE 42P10 — a table-shape defect indexes
//     and columns don't catch. This check must FAIL when the constraint is
//     missing, never pass: the lens stays dark and re-verifies until the operator
//     restores it, which is what keeps the re-provisioned-table scenario this
//     design exists for from becoming an unbounded transient Nak loop under a
//     health entry that reads active (§4.2e).
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
	if dt, ok := cols[IsDeletedColumn]; !ok {
		return fmt.Errorf("rls: verify %q: missing %s column", table, IsDeletedColumn)
	} else if dt != "boolean" {
		return fmt.Errorf("rls: verify %q: %s must be boolean, found %q", table, IsDeletedColumn, dt)
	}
	if dt, ok := cols[DeletedAtColumn]; !ok {
		return fmt.Errorf("rls: verify %q: missing %s column", table, DeletedAtColumn)
	} else if dt != "timestamp with time zone" {
		return fmt.Errorf("rls: verify %q: %s must be timestamptz, found %q", table, DeletedAtColumn, dt)
	}

	// 2b. The write path's postgres.go builds `ON CONFLICT (<keyCols>)`; Postgres
	// can only honor that against a unique index/constraint whose columns are
	// EXACTLY keyCols, so a table missing one — most concretely the design's own
	// motivating scenario, dropped and re-provisioned without it — fails every
	// write at SQLSTATE 42P10, which is not visible from the columns/types check
	// above. Checked here, before the write ever happens, so it keeps the lens
	// dark instead of live-but-broken.
	hasUnique, err := hasExactUniqueConstraint(ctx, pool, table, keyCols)
	if err != nil {
		return fmt.Errorf("rls: verify %q: %w", table, err)
	}
	if !hasUnique {
		return fmt.Errorf("rls: verify %q: no unique index/constraint exactly covering key columns %v — the write path's ON CONFLICT target has no arbiter index", table, keyCols)
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
// table exists with the expected columns and types, and a unique index/
// constraint exactly covering (actor_id, anchor_id, grant_source) — the
// UpsertGrant/RevokeGrant ON CONFLICT target — so the seq-guarded
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
	// UpsertGrant and RevokeGrant both write
	// `ON CONFLICT (actor_id, anchor_id, grant_source)`; Postgres can only honor
	// that against a unique index/constraint whose columns are exactly this set
	// (hasExactUniqueConstraint), so a table re-provisioned without it passes
	// every check above and then fails every write with SQLSTATE 42P10. This
	// check must fail (never pass) when that constraint is absent, so the grant
	// lens stays dark instead of live-but-broken.
	grantConflictCols := []string{"actor_id", "anchor_id", "grant_source"}
	hasUnique, err := hasExactUniqueConstraint(ctx, w.pool, GrantTable, grantConflictCols)
	if err != nil {
		return fmt.Errorf("grant writer: verify: %w", err)
	}
	if !hasUnique {
		return fmt.Errorf("grant writer: verify: %q has no unique index/constraint exactly covering %v — the write path's ON CONFLICT target has no arbiter index", GrantTable, grantConflictCols)
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

// hasExactUniqueConstraint reports whether table carries a unique index that
// Postgres will actually accept as an ON CONFLICT(cols) arbiter — not merely
// one whose key columns happen to match cols as a set.
//
// "Exactly" is not a stricter-than-needed check: it is what Postgres itself
// requires to honor an ON CONFLICT(cols) target. Per the PostgreSQL 16 docs
// (INSERT, "Conflict Target" — our pin is postgres:16-alpine,
// docker-compose.yml): "All table_name unique indexes that, without regard to
// order, contain exactly the conflict_target-specified columns/expressions are
// inferred (chosen) as arbiter indexes." A unique index on a superset (e.g. one
// extra column) or a subset of cols does not arbitrate that target and must not
// satisfy this check either — a looser "covers" check would pass a table whose
// write path still fails at SQLSTATE 42P10. Verified live against a pinned
// Postgres 16 instance: a PRIMARY KEY on exactly cols passes; the same
// constraint dropped, narrowed to a subset, or widened to a superset all
// correctly fail (each pinned by a test in rls_verify_test.go); a covering
// index's INCLUDE columns and the indexed columns' declaration order are both
// correctly ignored (Postgres's own "without regard to order" rule, also
// pinned by a test); a partial unique index (indpred IS NOT NULL) is excluded
// — it arbitrates only an ON CONFLICT target that names a matching
// index_predicate, which neither write path here (postgres.go, rls.go
// UpsertGrant/RevokeGrant) does.
//
// Three further conditions gate real Postgres arbiter eligibility beyond the
// plain column-set match, each verified live against postgres:16-alpine
// (docker-compose.yml) and each pinned by its own test:
//
//   - indimmediate: a UNIQUE constraint declared DEFERRABLE backs an index
//     with indimmediate = false, and Postgres refuses it as an arbiter — live:
//     `ALTER TABLE ... ADD CONSTRAINT ... UNIQUE ... DEFERRABLE INITIALLY
//     DEFERRED` then an ON CONFLICT against those columns raises "ON CONFLICT
//     does not support deferrable unique constraints/exclusion constraints as
//     arbiters" (SQLSTATE 55000 — object_not_in_prerequisite_state, distinct
//     from the 42P10 "no matching arbiter" case below; failure/classify.go
//     does not need a 55000 case, because this gate now refuses the table at
//     probe time before any write is attempted). Doc citation: PostgreSQL 16
//     docs, INSERT, "Conflict Target" — "In all cases, only NOT DEFERRABLE
//     constraints and unique indexes are supported as arbiters."
//   - indisvalid: an index left INVALID by an interrupted `CREATE INDEX
//     CONCURRENTLY` is not inferable — live: creating one over duplicate data
//     (indisvalid = false, indisunique/indimmediate still true) and then
//     issuing the same ON CONFLICT raises SQLSTATE 42P10, "there is no unique
//     or exclusion constraint matching the ON CONFLICT specification" — the
//     identical error a wholly absent constraint produces, confirming Postgres
//     treats an invalid index as not present for arbitration. Source:
//     postgres/postgres REL_16_STABLE src/backend/optimizer/util/plancat.c,
//     infer_arbiter_indexes: "if (!idxForm->indisvalid) goto next;" (skips the
//     candidate before any column comparison).
//   - Expression key columns: unnest(indkey) yields attnum = 0 for an
//     expression (e.g. lower(x)), and a plain JOIN to pg_attribute silently
//     drops that position — so a 4-key unique index on
//     (actor_id, anchor_id, grant_source, lower(x)) previously matched a
//     3-column plain target after the join dropped the expression key,
//     though Postgres sees 4 key columns and refuses to arbitrate a
//     3-column target (live: SQLSTATE 42P10). Source: same function,
//     infer_arbiter_indexes compares BOTH a per-column attribute bitmap AND
//     (via RelationGetIndexExpressions + list_difference) the expression
//     list — a target naming only plain columns can never satisfy an index
//     that includes an expression key. This check refuses outright whenever
//     any key position (ord <= indnkeyatts) is attnum 0, rather than
//     matching a narrowed column set silently.
func hasExactUniqueConstraint(ctx context.Context, pool *pgxpool.Pool, table string, cols []string) (bool, error) {
	var ok bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_index i
			WHERE i.indrelid = to_regclass($1)
				AND i.indisunique
				AND i.indimmediate
				AND i.indisvalid
				AND i.indpred IS NULL
				AND NOT EXISTS (
					SELECT 1
					FROM unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
					WHERE k.ord <= i.indnkeyatts AND k.attnum = 0
				)
				AND (
					SELECT array_agg(a.attname::text ORDER BY a.attname::text)
					FROM unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
					JOIN pg_attribute a
						ON a.attrelid = i.indrelid AND a.attnum = k.attnum
					WHERE k.ord <= i.indnkeyatts
				) = (
					SELECT array_agg(c ORDER BY c) FROM unnest($2::text[]) AS c
				)
		)`,
		table, cols,
	).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("read unique constraint: %w", err)
	}
	return ok, nil
}
