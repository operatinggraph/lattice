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

// Compile-time check that PostgresAdapter satisfies Adapter and Truncater.
var _ Adapter = (*PostgresAdapter)(nil)
var _ Truncater = (*PostgresAdapter)(nil)

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
// pipeline's adjacency-watch path (pipeline.go) checks this via the
// `interface{ Guarded() bool }` assertion to skip a sentinel-seq (0) write
// that would otherwise be unconditionally dropped by the guard clause below —
// mirroring NatsKVAdapter.Guarded.
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
	// ProjectionSeqColumn is platform-owned (guarded mode appends it explicitly
	// below), so a same-named lens-declared column would collide — filter it too.
	nonKeyCols := make([]string, 0, len(row))
	for col := range row {
		if _, isKey := keySet[col]; isKey {
			continue
		}
		if a.guarded && col == ProjectionSeqColumn {
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
	// unguarded and there are no non-key columns.
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
	ctx, cancel := a.withTimeout(ctx)
	defer cancel()

	sqlStr, args, err := a.buildUpsertSQL(keys, row, projectionSeq)
	if err != nil {
		return err
	}
	for i, v := range args {
		args[i] = coerceForPgx(v)
	}

	_, err = a.pool.Exec(ctx, sqlStr, args...)
	return err
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

// buildDeleteSQL constructs the delete SQL and its argument slice, branching on
// the adapter's construction-time deleteMode.
//
//   - DeleteModeHard (default): `DELETE FROM "<table>" WHERE <clauses>` — the row
//     is physically removed. Lineage already lives in Core KV.
//   - DeleteModeSoft: `UPDATE "<table>" SET is_deleted=true, deleted_at=NOW()
//     WHERE <clauses>` — a tombstone is retained for audit/forensic targets. The
//     target table must then have `is_deleted boolean` and `deleted_at
//     timestamptz` columns.
//
// Key columns appear in a.keyOrder order with positional placeholders $1, $2, ...
// All identifiers are double-quoted via quoteIdent.
func (a *PostgresAdapter) buildDeleteSQL(keys map[string]any) (string, []any, error) {
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
	if a.deleteMode == DeleteModeSoft {
		sql = fmt.Sprintf(`UPDATE "%s" SET is_deleted=true, deleted_at=NOW() WHERE %s`, a.table, where)
	} else {
		sql = fmt.Sprintf(`DELETE FROM "%s" WHERE %s`, a.table, where)
	}
	return sql, args, nil
}

// Delete removes a row from the Postgres table by its key fields.
// Zero rows affected is not an error — deletion of a non-existent row is idempotent (NFR2).
// []any and map[string]any key values are coerced to json.RawMessage for JSONB columns.
// The per-rule queryTimeout is applied via withTimeout. Returns nil on success.
//
// projectionSeq is accepted to satisfy the Adapter interface but ignored: the
// monotonic projection-write guard is not enforced on Postgres targets
// (Contract #6 §6.2).
func (a *PostgresAdapter) Delete(ctx context.Context, keys map[string]any, _ uint64) error {
	ctx, cancel := a.withTimeout(ctx)
	defer cancel()

	sqlStr, args, err := a.buildDeleteSQL(keys)
	if err != nil {
		return err
	}
	for i, v := range args {
		args[i] = coerceForPgx(v)
	}

	_, err = a.pool.Exec(ctx, sqlStr, args...)
	return err
}
