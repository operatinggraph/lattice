package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time check that PostgresAdapter satisfies Adapter, Truncater,
// KeyLister, SeqGuarded, OutcomeUpserter and OutcomeDeleter.
var _ Adapter = (*PostgresAdapter)(nil)
var _ Truncater = (*PostgresAdapter)(nil)
var _ KeyLister = (*PostgresAdapter)(nil)
var _ SeqGuarded = (*PostgresAdapter)(nil)
var _ OutcomeUpserter = (*PostgresAdapter)(nil)
var _ OutcomeDeleter = (*PostgresAdapter)(nil)

// PostgresAdapter writes materialized rows to a Postgres table.
// It uses a shared pgxpool.Pool (owned by PoolManager) so connection count
// stays bounded across many rules targeting the same DSN (ADR-9).
type PostgresAdapter struct {
	pool         *pgxpool.Pool
	table        string
	keyOrder     []string
	queryTimeout time.Duration // applied per operation via context.WithTimeout
	deleteMode   DeleteMode    // hard (default): DELETE FROM; soft: UPDATE … SET is_deleted=true

	// guarded selects the monotonic projection_seq write guard (Contract #6
	// §6.14), the Postgres analogue of PostgresGrantWriter.UpsertGrant. Unset
	// by default — an ordinary Postgres lens stays unconditional last-writer-
	// wins (§6.2). NewProtectedAdapter enables it via SetGuarded: a protected
	// read-model table always carries a projection_seq column (rls.go), so a
	// stale (lower-seq) replay must not overwrite a fresher projected row.
	guarded bool
}

// NewPostgresAdapter creates a PostgresAdapter.
// pool must be non-nil (obtained from PoolManager.Acquire).
// table is the target Postgres table name.
// keyOrder lists key field names in the order used for ON CONFLICT / WHERE clauses.
// queryTimeout is applied to each write operation; 0 means no timeout.
// deleteMode selects hard (DELETE FROM) vs soft (UPDATE … SET is_deleted=true)
// delete projection; it is fixed for the life of the adapter.
func NewPostgresAdapter(pool *pgxpool.Pool, table string, keyOrder []string, queryTimeout time.Duration, deleteMode DeleteMode) (*PostgresAdapter, error) {
	if pool == nil {
		return nil, fmt.Errorf("postgres: pool must not be nil")
	}
	if table == "" {
		return nil, fmt.Errorf("postgres: table must not be empty")
	}
	if strings.ContainsRune(table, '"') {
		return nil, fmt.Errorf("postgres: table name must not contain double-quote characters: %q", table)
	}
	if len(keyOrder) == 0 {
		return nil, fmt.Errorf("postgres: keyOrder must not be empty")
	}
	seen := make(map[string]struct{}, len(keyOrder))
	for _, k := range keyOrder {
		if _, dup := seen[k]; dup {
			return nil, fmt.Errorf("postgres: keyOrder contains duplicate field %q", k)
		}
		seen[k] = struct{}{}
	}
	return &PostgresAdapter{
		pool:         pool,
		table:        table,
		keyOrder:     keyOrder,
		queryTimeout: queryTimeout,
		deleteMode:   deleteMode,
	}, nil
}

// SetGuarded enables or disables the monotonic projection_seq write guard.
// NewProtectedAdapter calls this on the inner adapter it wraps; an ordinary
// (non-protected) PostgresAdapter is never guarded.
func (a *PostgresAdapter) SetGuarded(guarded bool) { a.guarded = guarded }

// Guarded reports whether the projection_seq write guard is enabled. The
// pipeline's rebuild path (pipeline.go) checks this via the `interface{
// Guarded() bool }` assertion to force a truncate before rescanning a
// guarded target, since its monotonic watermark would otherwise reject a
// lower-seq historical replay — mirroring NatsKVAdapter.Guarded.
func (a *PostgresAdapter) Guarded() bool { return a.guarded }

// quoteIdent wraps a Postgres identifier in double-quotes and escapes any
// embedded double-quotes per the SQL standard (replace " with "").
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Probe checks whether the Postgres pool can reach the server by calling Ping.
// Returns nil if reachable; returns an error classifiable by failure.Classify otherwise.
func (a *PostgresAdapter) Probe(ctx context.Context) error {
	return a.pool.Ping(ctx)
}

// Close is a no-op — the pool lifecycle is owned by PoolManager, not the adapter.
func (a *PostgresAdapter) Close() error { return nil }

// withTimeout wraps ctx with a.queryTimeout deadline if the timeout is positive.
// Callers must always invoke the returned cancel function.
func (a *PostgresAdapter) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if a.queryTimeout > 0 {
		return context.WithTimeout(ctx, a.queryTimeout)
	}
	return ctx, func() {}
}

// buildUpsertSQL constructs the INSERT ... ON CONFLICT ... DO UPDATE SQL and its argument slice.
//
// Column ordering is deterministic: key columns appear in a.keyOrder order; non-key columns from
// row appear in alphabetical order. All identifiers (table and column names) are double-quoted.
// Key columns that appear in the row map are silently ignored (the keys map is authoritative).
// When row yields no non-key columns, DO NOTHING is used instead of DO UPDATE SET.
//
// When a.guarded, projectionSeq is written to ProjectionSeqColumn as an explicit
// column (never sourced from row — the platform, not the lens, owns it) and the
// ON CONFLICT clause is conditioned `WHERE EXCLUDED.projection_seq >
// "<table>".projection_seq`, mirroring PostgresGrantWriter.UpsertGrant: a
// stale (lower-seq) replay leaves the fresher row untouched instead of
// overwriting it. DO NOTHING never applies to a guarded write (the guard needs
// a DO UPDATE to attach its WHERE clause), so a guarded row with no non-key
// business columns still gets a DO UPDATE that touches only projection_seq.
func (a *PostgresAdapter) buildUpsertSQL(keys map[string]any, row map[string]any, projectionSeq uint64) (string, []any, error) {
	// 1. Validate key fields and collect values in keyOrder.
	keyArgs := make([]any, len(a.keyOrder))
	for i, k := range a.keyOrder {
		v, ok := keys[k]
		if !ok {
			return "", nil, fmt.Errorf("postgres upsert: key field %q absent from keys map", k)
		}
		keyArgs[i] = v
	}

	// 2. Build set of key column names for overlap filtering.
	keySet := make(map[string]struct{}, len(a.keyOrder))
	for _, k := range a.keyOrder {
		keySet[k] = struct{}{}
	}

	// 3. Sort non-key columns alphabetically for deterministic SQL.
	// Key columns that appear in row are filtered out to prevent duplicate-column errors.
	// ProjectionSeqColumn/IsDeletedColumn/DeletedAtColumn are platform-owned
	// (guarded mode sets them explicitly below), so a same-named lens-declared
	// column would collide — filter them too.
	nonKeyCols := make([]string, 0, len(row))
	for col := range row {
		if _, isKey := keySet[col]; isKey {
			continue
		}
		if a.guarded && (col == ProjectionSeqColumn || col == IsDeletedColumn || col == DeletedAtColumn) {
			continue
		}
		nonKeyCols = append(nonKeyCols, col)
	}
	sort.Strings(nonKeyCols)

	// 4. Full column list: key columns first, then non-key columns, then the
	// guard column (guarded mode only — always last so its placeholder index
	// is easy to reason about).
	allCols := make([]string, 0, len(a.keyOrder)+len(nonKeyCols)+1)
	allCols = append(allCols, a.keyOrder...)
	allCols = append(allCols, nonKeyCols...)
	if a.guarded {
		allCols = append(allCols, ProjectionSeqColumn)
	}

	// 5. Argument slice: key values then non-key values then the guard value,
	// in matching order.
	args := make([]any, 0, len(allCols))
	args = append(args, keyArgs...)
	for _, col := range nonKeyCols {
		args = append(args, row[col])
	}
	if a.guarded {
		args = append(args, int64(projectionSeq))
	}

	// 6. Positional placeholders $1, $2, ...
	placeholders := make([]string, len(allCols))
	for i := range allCols {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	// 7. ON CONFLICT clause — key columns quoted.
	quotedKeyCols := make([]string, len(a.keyOrder))
	for i, k := range a.keyOrder {
		quotedKeyCols[i] = quoteIdent(k)
	}
	conflictCols := strings.Join(quotedKeyCols, ", ")

	// 8. DO UPDATE SET for non-key (+ guard) columns; DO NOTHING only when
	// unguarded and there are no non-key columns. A guarded write also always
	// resets IsDeletedColumn to false: a fresh Upsert at a higher seq is a live
	// re-projection, so a row a prior guarded Delete tombstoned must be revived
	// (mirrors PostgresGrantWriter.UpsertGrant's `is_deleted = false`). This is a
	// literal, not EXCLUDED-sourced, since no lens ever projects IsDeletedColumn.
	var onConflict string
	if len(nonKeyCols) == 0 && !a.guarded {
		onConflict = "DO NOTHING"
	} else {
		setCols := nonKeyCols
		if a.guarded {
			setCols = append(append([]string(nil), nonKeyCols...), ProjectionSeqColumn)
		}
		setParts := make([]string, len(setCols))
		for i, col := range setCols {
			q := quoteIdent(col)
			setParts[i] = fmt.Sprintf("%s = EXCLUDED.%s", q, q)
		}
		if a.guarded {
			setParts = append(setParts, fmt.Sprintf("%s = false", quoteIdent(IsDeletedColumn)))
		}
		onConflict = "DO UPDATE SET " + strings.Join(setParts, ", ")
		if a.guarded {
			onConflict += fmt.Sprintf(" WHERE EXCLUDED.%s > %s.%s",
				quoteIdent(ProjectionSeqColumn), quoteIdent(a.table), quoteIdent(ProjectionSeqColumn))
		}
	}

	// 9. Quoted column list for INSERT.
	quotedAllCols := make([]string, len(allCols))
	for i, col := range allCols {
		quotedAllCols[i] = quoteIdent(col)
	}

	sql := fmt.Sprintf(
		`INSERT INTO "%s" (%s) VALUES (%s) ON CONFLICT (%s) %s`,
		a.table,
		strings.Join(quotedAllCols, ", "),
		strings.Join(placeholders, ", "),
		conflictCols,
		onConflict,
	)
	return sql, args, nil
}

// coerceForPgx converts Go types that pgx cannot natively encode for JSONB columns.
// []any and map[string]any are marshaled to json.RawMessage so pgx's JSONB codec
// can handle them. All other types (string, float64, bool, nil, etc.) pass through
// unchanged — pgx and Postgres handle their type compatibility directly.
// If marshaling fails, the original value is returned so pgx will surface an error.
func coerceForPgx(v any) any {
	switch val := v.(type) {
	case []any:
		b, err := json.Marshal(val)
		if err != nil {
			return v
		}
		return json.RawMessage(b)
	case map[string]any:
		b, err := json.Marshal(val)
		if err != nil {
			return v
		}
		return json.RawMessage(b)
	default:
		return v
	}
}

// Upsert writes a materialized row to the Postgres table using INSERT ... ON CONFLICT DO UPDATE.
// keys and row together form the complete row; keys drives the ON CONFLICT clause.
// []any and map[string]any values are coerced to json.RawMessage for JSONB columns.
// The per-rule queryTimeout is applied via withTimeout. Returns nil on success.
//
// projectionSeq is ignored unless the adapter is guarded (SetGuarded): an
// ordinary Postgres lens keeps the unconditional last-writer-wins behavior
// Contract #6 §6.2 documents. A guarded adapter (NewProtectedAdapter) instead
// conditions the write on projectionSeq per buildUpsertSQL.
func (a *PostgresAdapter) Upsert(ctx context.Context, keys map[string]any, row map[string]any, projectionSeq uint64) error {
	_, err := a.upsert(ctx, keys, row, projectionSeq)
	return err
}

// UpsertWithOutcome is Upsert plus a report of whether a row actually landed
// (adapter.OutcomeUpserter), which on this target is the difference between a
// guarded write that took effect and one the monotonic guard declined.
//
// A guarded upsert here is `INSERT … ON CONFLICT DO UPDATE … WHERE
// EXCLUDED.projection_seq > <table>.projection_seq` (buildUpsertSQL). When the
// stored seq is equal or higher the statement matches no row, updates nothing,
// and returns no error — indistinguishable, to a caller reading only the error,
// from a write that landed. The pipeline's audit gate and the reconciler both
// have to tell those apart, so the row count the statement already reports is
// surfaced instead of discarded.
func (a *PostgresAdapter) UpsertWithOutcome(ctx context.Context, keys map[string]any, row map[string]any, projectionSeq uint64) (UpsertOutcome, error) {
	return a.upsert(ctx, keys, row, projectionSeq)
}

// upsert is the shared implementation behind Upsert and UpsertWithOutcome.
func (a *PostgresAdapter) upsert(ctx context.Context, keys map[string]any, row map[string]any, projectionSeq uint64) (UpsertOutcome, error) {
	ctx, cancel := a.withTimeout(ctx)
	defer cancel()

	sqlStr, args, err := a.buildUpsertSQL(keys, row, projectionSeq)
	if err != nil {
		return UpsertOutcome{}, err
	}
	for i, v := range args {
		args[i] = coerceForPgx(v)
	}

	tag, err := a.pool.Exec(ctx, sqlStr, args...)
	if err != nil {
		return UpsertOutcome{}, err
	}
	return a.upsertOutcome(tag.RowsAffected()), nil
}

// upsertOutcome maps the row count one upsert statement reported onto the
// outcome the pipeline reads.
//
// Wrote coincides with Committed on this target, unlike NatsKVAdapter's guarded
// path: there the guard's decline still rewrites the watermark, so a write
// genuinely happens on every call; here the decline is the ON CONFLICT WHERE
// clause matching no row, so nothing is written at all and claiming otherwise
// would be a fabrication.
//
// Zero rows under the guard means the watermark declined, and only that: the
// statement inserts when the key is absent and updates when it is present, so
// the sole way to touch no row is the WHERE clause — which is the watermark
// comparison itself. Unguarded, zero rows is the `DO NOTHING` arm buildUpsertSQL
// emits for a key-columns-only table, where the row is already there and no
// column exists to change.
//
// Key is left empty: a Postgres row has no single rendered key string for a
// caller to route on, and UpsertOutcome.Key exists to hand back exactly the
// string the adapter wrote. Transition stays TransitionNone for the same reason
// the unguarded NATS-KV path leaves it there — this path never reads the stored
// row's liveness back, so it has no evidence either way.
func (a *PostgresAdapter) upsertOutcome(rowsAffected int64) UpsertOutcome {
	committed := rowsAffected > 0
	return UpsertOutcome{
		Wrote:               committed,
		Committed:           committed,
		DeclinedByWatermark: a.guarded && !committed,
	}
}

// buildListKeysSQL constructs the key-only SELECT for ListKeys: the key
// columns in a.keyOrder order, filtered to live rows only when a soft
// tombstone can appear in the table — unguarded DeleteModeSoft, or a.guarded
// (which always soft-tombstones on Delete regardless of deleteMode, see
// buildDeleteSQL) — since a tombstone is an UPDATE, not a row removal, and
// must be excluded from the "currently live" set the diff compares against.
func (a *PostgresAdapter) buildListKeysSQL() string {
	quotedKeyCols := make([]string, len(a.keyOrder))
	for i, k := range a.keyOrder {
		quotedKeyCols[i] = quoteIdent(k)
	}
	sqlStr := fmt.Sprintf(`SELECT %s FROM "%s"`, strings.Join(quotedKeyCols, ", "), a.table)
	if a.deleteMode == DeleteModeSoft || a.guarded {
		sqlStr += ` WHERE NOT "is_deleted"`
	}
	return sqlStr
}

// ListKeys returns every live row's key fields (a.keyOrder), one map per row.
// Used by the pipeline's Fire-3 diff retraction (DiffRetraction) to derive
// Deletes for rows a fresh re-projection no longer produces but no single CDC
// event names directly — the neighbor-driven / multi-row gap Fire 2's
// anchor-self presence check cannot reach.
func (a *PostgresAdapter) ListKeys(ctx context.Context) ([]map[string]any, error) {
	ctx, cancel := a.withTimeout(ctx)
	defer cancel()

	rows, err := a.pool.Query(ctx, a.buildListKeysSQL())
	if err != nil {
		return nil, fmt.Errorf("postgres list keys: %w", err)
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(a.keyOrder))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("postgres list keys: scan: %w", err)
		}
		m := make(map[string]any, len(a.keyOrder))
		for i, k := range a.keyOrder {
			m[k] = vals[i]
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres list keys: %w", err)
	}
	return out, nil
}

// buildTruncateSQL constructs the truncate statement. The target table name is
// double-quoted via quoteIdent so a reserved word or mixed-case identifier is
// honored exactly; the constructor already rejects an embedded double-quote, so
// the name cannot break out of the quoting.
func (a *PostgresAdapter) buildTruncateSQL() string {
	return fmt.Sprintf(`TRUNCATE TABLE %s`, quoteIdent(a.table))
}

// Truncate clears every row from the target table so a rebuild's stream replay
// re-populates it from empty (Pipeline.Rebuild with truncate=true, FR29).
// TRUNCATE drops all rows in one statement (no per-row tombstone scan) and
// leaves the table schema intact, mirroring the NATS-KV adapter's purge-every-key
// Truncate. Postgres targets carry no projection-write guard — writes are
// unconditional last-writer-wins (Contract #6 §6.2) — so unlike the guarded
// KV path there is no watermark to reset; the replay simply re-inserts from a
// clean table. The per-rule queryTimeout is applied via withTimeout.
func (a *PostgresAdapter) Truncate(ctx context.Context) error {
	ctx, cancel := a.withTimeout(ctx)
	defer cancel()

	_, err := a.pool.Exec(ctx, a.buildTruncateSQL())
	return err
}

// buildDeleteSQL constructs the delete SQL and its argument slice.
//
//   - a.guarded (a protected read-model table, Contract #6 §6.14): ALWAYS a
//     seq-guarded soft tombstone, regardless of deleteMode — `UPDATE "<table>"
//     SET is_deleted=true, deleted_at=NOW(), projection_seq=$N WHERE <clauses>
//     AND projection_seq < $N`. Retaining the row and bumping its watermark
//     (rather than discarding it via a hard DELETE) means a later stale CDC
//     replay's Upsert still finds a row to compare its projectionSeq against —
//     a hard delete would leave no row, so the ON CONFLICT guard in
//     buildUpsertSQL never fires and the plain INSERT resurrects the row
//     unconditionally. deleteMode is not lens-configurable for a guarded
//     table: this is a platform invariant, not a per-lens choice.
//   - DeleteModeSoft (unguarded): `UPDATE "<table>" SET is_deleted=true,
//     deleted_at=NOW() WHERE <clauses>` — an unguarded tombstone for
//     audit/forensic targets that opt in; not seq-guarded (§6.2 last-writer-
//     wins).
//   - DeleteModeHard (default, unguarded): `DELETE FROM "<table>" WHERE
//     <clauses>` — the row is physically removed. Lineage already lives in
//     Core KV.
//
// Key columns appear in a.keyOrder order with positional placeholders $1, $2, ...
// All identifiers are double-quoted via quoteIdent.
func (a *PostgresAdapter) buildDeleteSQL(keys map[string]any, projectionSeq uint64) (string, []any, error) {
	args := make([]any, len(a.keyOrder))
	clauses := make([]string, len(a.keyOrder))
	for i, k := range a.keyOrder {
		v, ok := keys[k]
		if !ok {
			return "", nil, fmt.Errorf("postgres delete: key field %q absent from keys map", k)
		}
		args[i] = v
		clauses[i] = fmt.Sprintf("%s = $%d", quoteIdent(k), i+1)
	}
	where := strings.Join(clauses, " AND ")
	var sql string
	switch {
	case a.guarded:
		args = append(args, int64(projectionSeq))
		seqParam := fmt.Sprintf("$%d", len(args))
		sql = fmt.Sprintf(
			`UPDATE "%s" SET is_deleted=true, deleted_at=NOW(), projection_seq=%s WHERE %s AND projection_seq < %s`,
			a.table, seqParam, where, seqParam,
		)
	case a.deleteMode == DeleteModeSoft:
		sql = fmt.Sprintf(`UPDATE "%s" SET is_deleted=true, deleted_at=NOW() WHERE %s`, a.table, where)
	default:
		sql = fmt.Sprintf(`DELETE FROM "%s" WHERE %s`, a.table, where)
	}
	return sql, args, nil
}

// Delete removes (or, for a guarded protected table, seq-guarded soft-
// tombstones — see buildDeleteSQL) a row from the Postgres table by its key
// fields. Zero rows affected is not an error — deletion of a non-existent row,
// or a stale-seq no-op on a guarded table, is idempotent (NFR2). []any and
// map[string]any key values are coerced to json.RawMessage for JSONB columns.
// The per-rule queryTimeout is applied via withTimeout. Returns nil on success.
//
// projectionSeq is ignored unless the adapter is guarded: an ordinary Postgres
// lens keeps the unconditional last-writer-wins behavior Contract #6 §6.2
// documents.
func (a *PostgresAdapter) Delete(ctx context.Context, keys map[string]any, projectionSeq uint64) error {
	_, err := a.deleteRow(ctx, keys, projectionSeq)
	return err
}

// DeleteWithOutcome is Delete plus a report of whether the retraction actually
// landed (adapter.OutcomeDeleter) — the retraction sibling of
// UpsertWithOutcome, and the more dangerous direction of the pair. A guarded
// delete declines by matching no row and returning nil, so a caller reading
// only the error records a withdrawal that left the row exactly where it was.
func (a *PostgresAdapter) DeleteWithOutcome(ctx context.Context, keys map[string]any, projectionSeq uint64) (DeleteOutcome, error) {
	return a.deleteRow(ctx, keys, projectionSeq)
}

// deleteRow is the shared implementation behind Delete and DeleteWithOutcome.
func (a *PostgresAdapter) deleteRow(ctx context.Context, keys map[string]any, projectionSeq uint64) (DeleteOutcome, error) {
	ctx, cancel := a.withTimeout(ctx)
	defer cancel()

	sqlStr, args, err := a.buildDeleteSQL(keys, projectionSeq)
	if err != nil {
		return DeleteOutcome{}, err
	}
	for i, v := range args {
		args[i] = coerceForPgx(v)
	}

	tag, err := a.pool.Exec(ctx, sqlStr, args...)
	if err != nil {
		return DeleteOutcome{}, err
	}
	return a.deleteOutcome(tag.RowsAffected()), nil
}

// deleteOutcome maps the row count one retraction statement reported onto the
// outcome its caller reads. buildDeleteSQL emits three different statements, a
// zero row count means something different under each, and the upsert
// direction's reading does not carry over — so each is settled on its own.
//
// Wrote is the same answer under all three, and it is exact: a statement that
// touched no row retracted nothing. An absent key retracts nothing, a declined
// guard retracts nothing, and a matched row is the only way anything is
// withdrawn. NatsKVAdapter reports its own absent-key hard delete the same way,
// for the reason stated there — a caller must not book a repair for a row that
// was never present.
//
// DeclinedByWatermark is where the three diverge:
//
//   - Unguarded hard (DELETE FROM … WHERE <keys>): zero rows means the key was
//     already absent, which is idempotent success and not a watermark decline —
//     no watermark is consulted at all. False.
//   - Unguarded soft (UPDATE … SET is_deleted=true WHERE <keys>): no guard
//     either, so also false. A tombstone written over an existing tombstone
//     still matches its key and counts as a row, matching the NATS-KV soft path.
//   - Guarded (UPDATE … WHERE <keys> AND projection_seq < $N): zero rows is
//     genuinely ambiguous. The row may be absent, or present at a watermark
//     equal to or above the token. A plain UPDATE cannot separate them, and
//     there is no INSERT arm to make absence cost a row the way the grant
//     table's ON CONFLICT form does.
//
// The guarded case is resolved one-sidedly toward reporting the decline, which
// is a choice rather than a reading. Claiming a decline for an already-absent
// row raises a visible false alarm about a retraction that had nothing left to
// do; claiming no decline for a real one lets a caller record a withdrawal that
// did not happen, on tables whose rows carry read permissions. One error
// direction is noise an operator can see; the other is a silent over-grant.
// transitionFrom's unparseable-body branch in natskv.go resolves its own
// ambiguity one-sidedly for the same reason.
func (a *PostgresAdapter) deleteOutcome(rowsAffected int64) DeleteOutcome {
	wrote := rowsAffected > 0
	return DeleteOutcome{
		Wrote:               wrote,
		DeclinedByWatermark: a.guarded && !wrote,
	}
}
