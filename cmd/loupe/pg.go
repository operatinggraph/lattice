package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The Postgres read seam (loupe-2-ux-design.md §6.4, fire F9): a read-only
// connector lighting up the lens CONTENTS panel for postgres-target lenses —
// the protected read models and the shared grant table. Loupe connects with
// its OWN DSN (LOUPE_PG_DSN), never the lens spec's writer DSN; the read-only
// guarantee is the DSN role's Postgres grants (SELECT-only), not code
// discipline. Every query is bounded three ways: the row limit, the
// pool-level statement timeout, and the per-request context.
//
// Loupe's all-access read posture is a WILDCARD actor_read_grants GRANT, never
// an RLS bypass (Andrew's ratified M5 decision, read-path-authorization-d1-
// design.md §8: "even admin reads pass through RLS and remain
// attributable/loggable — an un-instrumented superuser read-actor is one
// compromise from total exposure"). Every query below runs inside a
// transaction that sets lattice.actor_id to Loupe's configured operator
// (s.operatorActorKey's bare NanoID, mirroring cmd/loftspace-app/applications.go's
// queryApplications). Two disjoint producers grant that identity the wildcard
// anchor '*', covering both mechanism-B postures: the kernel's
// capabilityReadWildcardGrants lens (internal/bootstrap/lenses.go) for a
// holdsRole->operator (root) identity, and packages/console-operator's own
// consoleOperatorReadGrants lens for a holdsRole->consoleOperator (scoped,
// the standing default per loupe-operator-auth-lift-design.md mechanism B)
// identity — so RLS itself resolves "sees everything" for whichever posture
// is configured, exactly like every other protected read, never a
// role-level bypass. Verified live against real (non-empty) protected-table
// data, not just an empty-table coincidence.

// pgStatementTimeoutMS is applied as the pool's statement_timeout runtime
// parameter so a pathological query dies server-side even if the client-side
// context somehow survives it.
const pgStatementTimeoutMS = "5000"

// newLoupePGPool builds the lazy read pool for the seam. pgxpool does not
// dial eagerly, so a down Postgres surfaces per-request as a JSON error (the
// console-wide degraded-mode contract), never as a startup failure.
func newLoupePGPool(dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse LOUPE_PG_DSN: %w", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = pgStatementTimeoutMS
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("build pg pool: %w", err)
	}
	return pool, nil
}

// pgResolveTarget maps a postgres lens spec to the table and key columns the
// CONTENTS panel browses. A grant-table lens may omit both: it projects to the
// shared actor_read_grants table under the platform composite key, so the
// adapter package's constants fill the gaps. Every other postgres lens
// defaults its key to ["key"] (the same default the package builder emits).
func pgResolveTarget(spec lensFullSpec) (table string, keyCols []string, err error) {
	table, _ = spec.Target["table"].(string)
	keyCols = keyColumns(spec.Target)
	grantTable, _ := spec.Target["grantTable"].(bool)
	if grantTable {
		if table == "" {
			table = adapter.GrantTable
		}
		if len(keyCols) == 0 {
			keyCols = adapter.GrantKeyColumns
		}
	}
	if table == "" {
		return "", nil, fmt.Errorf("declares no target table")
	}
	if len(keyCols) == 0 {
		keyCols = []string{"key"}
	}
	return table, keyCols, nil
}

// pgIdent validates and double-quotes a SQL identifier sourced from a lens
// spec. Rejecting embedded double-quotes means the quoted form cannot break
// out of its quoting (the same guard the Refractor adapters apply on the
// write side).
func pgIdent(kind, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%s must not be empty", kind)
	}
	if strings.ContainsRune(name, '"') {
		return "", fmt.Errorf("%s %q must not contain double-quote characters", kind, name)
	}
	return `"` + name + `"`, nil
}

// escapeILIKE escapes the LIKE metacharacters in a user-supplied substring so
// it matches literally inside the %…% pattern (backslash is the default
// ESCAPE character).
func escapeILIKE(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(q)
}

// buildLensRowsSQL assembles the bounded count + select statements for a lens
// contents browse. The row key is the key columns joined with '.'
// (concat_ws), mirroring how a nats_kv target's key reads; the q substring
// filters that joined key case-insensitively, matching the KV path's
// semantics. Identifiers are validated+quoted; q travels as a bind parameter;
// limit is a server-clamped int.
func buildLensRowsSQL(table string, keyCols []string, q string, limit int) (countSQL, rowsSQL string, args []any, err error) {
	qt, err := pgIdent("table", table)
	if err != nil {
		return "", "", nil, err
	}
	if len(keyCols) == 0 {
		return "", "", nil, fmt.Errorf("key columns must not be empty")
	}
	quoted := make([]string, len(keyCols))
	for i, k := range keyCols {
		if quoted[i], err = pgIdent("key column", k); err != nil {
			return "", "", nil, err
		}
	}
	keyExpr := "concat_ws('.', " + strings.Join(quoted, ", ") + ")"

	where := ""
	if q != "" {
		where = " WHERE " + keyExpr + " ILIKE $1"
		args = []any{"%" + escapeILIKE(q) + "%"}
	}
	countSQL = "SELECT count(*) FROM " + qt + where
	rowsSQL = "SELECT * FROM " + qt + where +
		" ORDER BY " + strings.Join(quoted, ", ") +
		fmt.Sprintf(" LIMIT %d", limit)
	return countSQL, rowsSQL, args, nil
}

// jsonSafeValue makes a decoded column value marshalable: a non-finite float
// (Postgres float4/float8 accept NaN/±Infinity) would abort json.Marshal
// after the 200 header is already written, silently blanking the panel — so
// it renders as its Postgres text form instead.
func jsonSafeValue(v any) any {
	switch f := v.(type) {
	case float64:
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return strconv.FormatFloat(f, 'g', -1, 64)
		}
	case float32:
		f64 := float64(f)
		if math.IsNaN(f64) || math.IsInf(f64, 0) {
			return strconv.FormatFloat(f64, 'g', -1, 32)
		}
	}
	return v
}

// pgRowDoc shapes one fetched row into the panel's {key, doc} form: key is
// the key-column values joined with '.', doc is the full column→value map
// (authz_anchors and projection_seq included — the read-path story the
// inspector exists to show). A key column that decodes to a non-string is
// rendered via fmt.Sprint so a surprising schema still yields a usable key.
func pgRowDoc(cols []string, vals []any, keyCols []string) (string, map[string]any) {
	doc := make(map[string]any, len(cols))
	for i, c := range cols {
		if i < len(vals) {
			doc[c] = jsonSafeValue(vals[i])
		}
	}
	keyParts := make([]string, 0, len(keyCols))
	for _, k := range keyCols {
		v, ok := doc[k]
		if !ok || v == nil {
			continue
		}
		if s, isStr := v.(string); isStr {
			keyParts = append(keyParts, s)
		} else {
			keyParts = append(keyParts, fmt.Sprint(v))
		}
	}
	return strings.Join(keyParts, "."), doc
}

// queryLensRows runs the bounded browse inside a per-request transaction with
// a txn-local actor session variable — the same pooling-safety shape
// cmd/loftspace-app/applications.go's queryApplications uses: set_config(...,
// is_local=true) is discarded at COMMIT/ROLLBACK, so the pooled connection
// returns clean and the next request inherits no actor (deny) until it sets
// its own. The query itself carries no auth filter — RLS is the scope, and
// actorID's wildcard grant (or lack of one) is what determines whether it
// sees every row or none. Returns the shaped rows plus the unfiltered-
// total/truncation facts the panel's status line renders.
//
// actorID must be the bare identity NanoID (VerifiedActor.Subject shape),
// matching the actor_id column in actor_read_grants and the §6.14 anchor
// representation — never the prefixed vtx.identity.<id> key.
func queryLensRows(ctx context.Context, pool *pgxpool.Pool, actorID, table string, keyCols []string, q string, limit int) (rows []map[string]any, total int, err error) {
	countSQL, rowsSQL, args, err := buildLensRowsSQL(table, keyCols, q, limit)
	if err != nil {
		return nil, 0, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('lattice.actor_id', $1, true)", actorID); err != nil {
		return nil, 0, fmt.Errorf("set actor: %w", err)
	}
	if err := tx.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count %s: %w", table, err)
	}
	pgRows, err := tx.Query(ctx, rowsSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("select %s: %w", table, err)
	}
	defer pgRows.Close()

	fds := pgRows.FieldDescriptions()
	cols := make([]string, len(fds))
	for i, fd := range fds {
		cols[i] = fd.Name
	}
	rows = make([]map[string]any, 0, limit)
	for pgRows.Next() {
		vals, err := pgRows.Values()
		if err != nil {
			return nil, 0, fmt.Errorf("decode %s row: %w", table, err)
		}
		key, doc := pgRowDoc(cols, vals, keyCols)
		rows = append(rows, map[string]any{"key": key, "doc": doc})
	}
	if err := pgRows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate %s: %w", table, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, fmt.Errorf("commit: %w", err)
	}
	return rows, total, nil
}

// pgActorID resolves Loupe's configured operator to the bare identity NanoID
// the RLS session variable expects (substrate.ParseVertexKey, the same
// validated parse readauth.go's handleOperatorDevToken uses on this same
// field) — never a naive prefix trim, so a malformed key errors instead of
// silently setting lattice.actor_id to garbage that happens to deny everything.
func (s *server) pgActorID() (string, error) {
	if s.operatorActorKey == "" {
		return "", fmt.Errorf("no operator actor configured (LOUPE_OPERATOR_ACTOR_KEY unset and no bootstrap admin actor loaded)")
	}
	vertexType, subject, ok := substrate.ParseVertexKey(s.operatorActorKey)
	if !ok || vertexType != "identity" {
		return "", fmt.Errorf("operator actor key is malformed (must be a vtx.identity.<id> key)")
	}
	return subject, nil
}

// lensRowsPG serves the CONTENTS panel for a postgres-target lens. Without a
// configured read pool it answers the designed pg-pending shape (the panel's
// honest empty state); with one it browses the target table under the same
// limit/q semantics as the nats_kv path. A malformed spec and a set-but-
// unparseable DSN both error rather than masquerading as the pending state.
func (s *server) lensRowsPG(ctx context.Context, w http.ResponseWriter, id string, spec lensFullSpec, limit int, q string) {
	table, keyCols, err := pgResolveTarget(spec)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "lens "+id+": "+err.Error())
		return
	}
	if s.pg == nil {
		if s.pgDSNInvalid {
			s.writeError(w, http.StatusBadGateway,
				"LOUPE_PG_DSN is set but unparseable; postgres lens contents unavailable (see the startup log)")
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{
			"targetType": spec.TargetType,
			"pgPending":  true,
			"rows":       []any{},
			"count":      0,
			"total":      0,
		})
		return
	}
	actorID, err := s.pgActorID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "lens "+id+": "+err.Error())
		return
	}
	rows, total, err := queryLensRows(ctx, s.pg, actorID, table, keyCols, q, limit)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "lens "+id+": "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"targetType": "postgres",
		"table":      table,
		"rows":       rows,
		"count":      len(rows),
		"total":      total,
		"truncated":  total > len(rows),
		"limit":      limit,
	})
}
