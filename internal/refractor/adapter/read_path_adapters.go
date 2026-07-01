package adapter

import (
	"context"
	"fmt"
)

// GrantWriterAdapter adapts the seq-guarded PostgresGrantWriter to the Adapter
// interface so a cap-read.* grant lens projects through the standard pipeline.
// It maps a projected row keyed by (actor_id, anchor_id, grant_source) onto the
// guarded UpsertGrant/RevokeGrant calls, forwarding the pipeline's projectionSeq
// as the §6.14 monotonic guard token.
//
// It deliberately does NOT implement Truncater: actor_read_grants is shared
// across every grant_source, so a single grant lens's rebuild must not
// TRUNCATE the whole table (that would wipe other sources' coexisting grants).
// Per-source retraction flows through Delete → RevokeGrant instead.
type GrantWriterAdapter struct {
	w *PostgresGrantWriter
}

var _ Adapter = (*GrantWriterAdapter)(nil)

// NewGrantWriterAdapter wraps a non-nil PostgresGrantWriter.
func NewGrantWriterAdapter(w *PostgresGrantWriter) (*GrantWriterAdapter, error) {
	if w == nil {
		return nil, fmt.Errorf("grant writer adapter: writer must not be nil")
	}
	return &GrantWriterAdapter{w: w}, nil
}

// grantKeyFields extracts the three grant key columns as strings, erroring if
// any is absent or not a string (grant keys are always projected NanoID/name
// strings — a non-string is a lens-shape bug, fail-closed).
func grantKeyFields(keys map[string]any) (actor, anchor, source string, err error) {
	get := func(col string) (string, error) {
		v, ok := keys[col]
		if !ok {
			return "", fmt.Errorf("grant writer adapter: key %q absent", col)
		}
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("grant writer adapter: key %q must be a string, got %T", col, v)
		}
		if s == "" {
			return "", fmt.Errorf("grant writer adapter: key %q must not be empty", col)
		}
		return s, nil
	}
	if actor, err = get(GrantKeyColumns[0]); err != nil {
		return
	}
	if anchor, err = get(GrantKeyColumns[1]); err != nil {
		return
	}
	source, err = get(GrantKeyColumns[2])
	return
}

// Upsert records a live grant under the monotonic-seq guard.
func (g *GrantWriterAdapter) Upsert(ctx context.Context, keys map[string]any, _ map[string]any, projectionSeq uint64) error {
	actor, anchor, source, err := grantKeyFields(keys)
	if err != nil {
		return err
	}
	return g.w.UpsertGrant(ctx, actor, anchor, source, projectionSeq)
}

// Delete tombstones a grant (seq-guarded), so a stale replay cannot resurrect it.
func (g *GrantWriterAdapter) Delete(ctx context.Context, keys map[string]any, projectionSeq uint64) error {
	actor, anchor, source, err := grantKeyFields(keys)
	if err != nil {
		return err
	}
	return g.w.RevokeGrant(ctx, actor, anchor, source, projectionSeq)
}

// Probe verifies the actor_read_grants table's out-of-band posture (it exists
// with the §6.14 shape) before the grant lens projects, and re-verifies on the
// infra-pause probe loop so the lens auto-resumes once the operator provisions
// the table. Refractor issues no DDL.
func (g *GrantWriterAdapter) Probe(ctx context.Context) error { return g.w.VerifyGrantTable(ctx) }

// Close is a no-op — the pool lifecycle is owned by PoolManager.
func (g *GrantWriterAdapter) Close() error { return nil }

// ProtectedAdapter wraps a PostgresAdapter for a protected read-model table,
// encoding the declared array columns (authz_anchors + any text[] body column)
// so they land as Postgres arrays rather than JSONB.
//
// The full engine emits a list value as []any, which the base adapter coerces
// to json.RawMessage (correct for a JSONB column). A text[] column needs a Go
// string slice instead, so this wrapper converts the declared array columns
// []any → []string BEFORE delegating; the base adapter's coercion then passes
// the []string through unchanged and pgx encodes it to text[]. The base adapter
// is left untouched, so every existing (non-protected) Postgres lens is
// byte-identical.
type ProtectedAdapter struct {
	inner     *PostgresAdapter
	arrayCols map[string]struct{}
	// body is the lens-declared body columns, retained so Probe can verify the
	// out-of-band table carries them (the key columns come from inner.keyOrder).
	body []ColumnDef
}

var (
	_ Adapter   = (*ProtectedAdapter)(nil)
	_ Truncater = (*ProtectedAdapter)(nil)
)

// NewProtectedAdapter wraps a non-nil PostgresAdapter. arrayCols names the row
// columns to encode as Postgres arrays (text[]); a nil/empty set behaves like
// the base adapter. body is the lens-declared body columns, used by Probe to
// verify the out-of-band table's shape.
//
// Enables the inner adapter's monotonic projection_seq write guard (Contract
// #6 §6.14): every protected table carries projection_seq (VerifyProtectedTable
// requires it), so a stale replay must not overwrite a fresher projected row —
// the same guard PostgresGrantWriter.UpsertGrant applies to actor_read_grants.
func NewProtectedAdapter(inner *PostgresAdapter, arrayCols []string, body []ColumnDef) (*ProtectedAdapter, error) {
	if inner == nil {
		return nil, fmt.Errorf("protected adapter: inner must not be nil")
	}
	inner.SetGuarded(true)
	set := make(map[string]struct{}, len(arrayCols))
	for _, c := range arrayCols {
		set[c] = struct{}{}
	}
	return &ProtectedAdapter{inner: inner, arrayCols: set, body: body}, nil
}

// toStringSlice converts a list value to []string for a text[] column. A nil
// value becomes an empty array (a row with no anchors — RLS then denies it). A
// non-string element is a lens-shape bug (anchors are bare-NanoID strings) and
// errors fail-closed.
func toStringSlice(col string, v any) ([]string, error) {
	switch xs := v.(type) {
	case nil:
		return []string{}, nil
	case []string:
		return xs, nil
	case []any:
		out := make([]string, len(xs))
		for i, e := range xs {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("protected adapter: array column %q element %d must be a string, got %T", col, i, e)
			}
			out[i] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("protected adapter: array column %q must be a list, got %T", col, v)
	}
}

// encodeArrays returns a copy of row with the declared array columns converted
// to []string. The input map is not mutated.
func (p *ProtectedAdapter) encodeArrays(row map[string]any) (map[string]any, error) {
	if len(p.arrayCols) == 0 {
		return row, nil
	}
	out := make(map[string]any, len(row))
	for k, v := range row {
		if _, isArray := p.arrayCols[k]; isArray {
			s, err := toStringSlice(k, v)
			if err != nil {
				return nil, err
			}
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out, nil
}

// Upsert encodes the declared array columns then delegates to the base adapter.
func (p *ProtectedAdapter) Upsert(ctx context.Context, keys map[string]any, row map[string]any, projectionSeq uint64) error {
	encoded, err := p.encodeArrays(row)
	if err != nil {
		return err
	}
	return p.inner.Upsert(ctx, keys, encoded, projectionSeq)
}

// Delete delegates to the base adapter (the key columns are never arrays).
func (p *ProtectedAdapter) Delete(ctx context.Context, keys map[string]any, projectionSeq uint64) error {
	return p.inner.Delete(ctx, keys, projectionSeq)
}

// Probe verifies the protected table's out-of-band security posture (FORCE ROW
// LEVEL SECURITY on, the §6.14 columns, a SELECT policy) before the lens
// projects, and re-verifies on the infra-pause probe loop so the lens stays dark
// fail-closed until the operator provisions the table out-of-band, then
// auto-resumes. Refractor issues no DDL; this is the active replacement for the
// retired runtime ProvisionProtectedTable.
func (p *ProtectedAdapter) Probe(ctx context.Context) error {
	return VerifyProtectedTable(ctx, p.inner.pool, p.inner.table, p.inner.keyOrder, p.body, p.inner.queryTimeout)
}

// Close delegates to the base adapter (a no-op — the pool is pool-managed).
func (p *ProtectedAdapter) Close() error { return p.inner.Close() }

// Guarded reports the inner adapter's guard state (always true — see
// NewProtectedAdapter). The pipeline's adjacency-watch path checks this via
// the `interface{ Guarded() bool }` assertion to skip a sentinel-seq (0) write
// on a guarded adapter, the same protection the KV-guarded lenses already get.
func (p *ProtectedAdapter) Guarded() bool { return p.inner.Guarded() }

// Truncate delegates to the base adapter so a protected read model still
// supports truncate-before-rebuild (FR29).
func (p *ProtectedAdapter) Truncate(ctx context.Context) error { return p.inner.Truncate(ctx) }
