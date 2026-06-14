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

// Compile-time check that PostgresAdapter satisfies Adapter.
var _ Adapter = (*PostgresAdapter)(nil)

// PostgresAdapter writes materialized rows to a Postgres table.
// It uses a shared pgxpool.Pool (owned by PoolManager) so connection count
// stays bounded across many rules targeting the same DSN (ADR-9).
type PostgresAdapter struct {
	pool         *pgxpool.Pool
	table        string
	keyOrder     []string
	queryTimeout time.Duration // applied per operation via context.WithTimeout
	deleteMode   DeleteMode    // hard (default): DELETE FROM; soft: UPDATE … SET is_deleted=true
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
func (a *PostgresAdapter) buildUpsertSQL(keys map[string]any, row map[string]any) (string, []any, error) {
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
	nonKeyCols := make([]string, 0, len(row))
	for col := range row {
		if _, isKey := keySet[col]; !isKey {
			nonKeyCols = append(nonKeyCols, col)
		}
	}
	sort.Strings(nonKeyCols)

	// 4. Full column list: key columns first, then non-key columns.
	allCols := make([]string, 0, len(a.keyOrder)+len(nonKeyCols))
	allCols = append(allCols, a.keyOrder...)
	allCols = append(allCols, nonKeyCols...)

	// 5. Argument slice: key values then non-key values in matching order.
	args := make([]any, 0, len(allCols))
	args = append(args, keyArgs...)
	for _, col := range nonKeyCols {
		args = append(args, row[col])
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

	// 8. DO UPDATE SET for non-key columns; DO NOTHING for key-only rows.
	var onConflict string
	if len(nonKeyCols) == 0 {
		onConflict = "DO NOTHING"
	} else {
		setParts := make([]string, len(nonKeyCols))
		for i, col := range nonKeyCols {
			q := quoteIdent(col)
			setParts[i] = fmt.Sprintf("%s = EXCLUDED.%s", q, q)
		}
		onConflict = "DO UPDATE SET " + strings.Join(setParts, ", ")
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
// projectionSeq is accepted to satisfy the Adapter interface but ignored: the
// monotonic projection-write guard is not enforced on Postgres targets
// (Contract #6 §6.2). Postgres writes remain unconditional last-writer-wins.
func (a *PostgresAdapter) Upsert(ctx context.Context, keys map[string]any, row map[string]any, _ uint64) error {
	ctx, cancel := a.withTimeout(ctx)
	defer cancel()

	sqlStr, args, err := a.buildUpsertSQL(keys, row)
	if err != nil {
		return err
	}
	for i, v := range args {
		args[i] = coerceForPgx(v)
	}

	_, err = a.pool.Exec(ctx, sqlStr, args...)
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
