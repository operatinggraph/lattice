package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ensurePostgresTable creates the target table for a lens if it does not
// already exist. Schema follows AC #4: every row carries `is_deleted boolean`
// and `deleted_at timestamptz` columns so soft-delete is the only delete
// path. Key columns are TEXT NOT NULL; non-key projection columns are
// TEXT NULLABLE — the lens output schema is intentionally loose here.
// Story 2.2 may move this to a proper migration tool.
func ensurePostgresTable(ctx context.Context, pool *pgxpool.Pool, table string, keys []string) error {
	if table == "" || len(keys) == 0 {
		return fmt.Errorf("ensurePostgresTable: table and keys required")
	}
	cols := make([]string, 0, len(keys)+3)
	for _, k := range keys {
		cols = append(cols, fmt.Sprintf(`%q TEXT NOT NULL`, k))
	}
	cols = append(cols,
		`is_deleted BOOLEAN NOT NULL DEFAULT FALSE`,
		`deleted_at TIMESTAMPTZ`,
		`row_data JSONB`,
	)
	quotedKeys := make([]string, len(keys))
	for i, k := range keys {
		quotedKeys[i] = fmt.Sprintf("%q", k)
	}
	sql := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %q (%s, PRIMARY KEY (%s))`,
		table,
		strings.Join(cols, ", "),
		strings.Join(quotedKeys, ", "),
	)
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("ensure table %q: %w", table, err)
	}
	return nil
}
