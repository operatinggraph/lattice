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
//
// source is the lens's declared grant_source, and it is the confinement
// boundary for every operation this adapter performs on the shared table:
// ListKeys enumerates only rows carrying it, and Upsert/Delete reject a row
// whose projected grant_source column disagrees with it. Empty means the lens
// declared none — permitted (the shipped anchor-derived producers need no
// enumeration), but then ListKeys is refused rather than falling back to an
// unscoped one, which would diff this producer's rows against every other
// producer's and retract theirs.
type GrantWriterAdapter struct {
	w      *PostgresGrantWriter
	source string
}

var (
	_ Adapter         = (*GrantWriterAdapter)(nil)
	_ KeyLister       = (*GrantWriterAdapter)(nil)
	_ SeqGuarded      = (*GrantWriterAdapter)(nil)
	_ OutcomeUpserter = (*GrantWriterAdapter)(nil)
	_ OutcomeDeleter  = (*GrantWriterAdapter)(nil)
)

// Guarded reports the §6.14 monotonic guard, which this adapter carries
// unconditionally: UpsertGrant and RevokeGrant each end
// `WHERE EXCLUDED.projection_seq > actor_read_grants.projection_seq`, so there
// is no unguarded GrantWriterAdapter to build and nothing to switch off. That
// is why it is a constant rather than a SetGuarded flag like the NATS-KV and
// Postgres adapters, whose guard is opted into per lens.
//
// This report is the pipeline's only route to that fact. Every other target
// has its guard turned on by projection.RequiresGuard, which answers false for
// any lens that is not an actorAggregate — which is every grant lens — so the
// guard-aware paths would otherwise treat writes the storage layer is ordering
// as last-writer-wins. A non-stream-sequenced write is what this guards
// against: it would carry the sentinel seq 0, and against an absent row that
// INSERT lands — an unordered live read grant in the table every protected
// table's RLS policy consults.
func (g *GrantWriterAdapter) Guarded() bool { return true }

// NewGrantWriterAdapter wraps a non-nil PostgresGrantWriter. source is the
// lens's declared grant_source (see GrantWriterAdapter.source); "" is a lens
// that declares none, which forgoes ListKeys and therefore DiffRetraction,
// whose activation guard demands a usable KeyLister.
func NewGrantWriterAdapter(w *PostgresGrantWriter, source string) (*GrantWriterAdapter, error) {
	if w == nil {
		return nil, fmt.Errorf("grant writer adapter: writer must not be nil")
	}
	return &GrantWriterAdapter{w: w, source: source}, nil
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

// checkSource rejects a projected grant_source that disagrees with the lens's
// declared one. Without this the declaration and the cypher could drift, and
// the drift would be silent AND destructive: ListKeys would enumerate the
// declared source's rows while the projection produced another's, so the diff
// would find no fresh row matching any listed key and retract the declared
// source's entire live set. Skipped when the lens declared no source (nothing
// to disagree with, and ListKeys is refused anyway).
func (g *GrantWriterAdapter) checkSource(source string) error {
	if g.source == "" || source == g.source {
		return nil
	}
	return fmt.Errorf("grant writer adapter: row grant_source %q does not match the lens's declared grant_source %q", source, g.source)
}

// Upsert records a live grant under the monotonic-seq guard.
func (g *GrantWriterAdapter) Upsert(ctx context.Context, keys map[string]any, _ map[string]any, projectionSeq uint64) error {
	actor, anchor, source, err := grantKeyFields(keys)
	if err != nil {
		return err
	}
	if err := g.checkSource(source); err != nil {
		return err
	}
	return g.w.UpsertGrant(ctx, actor, anchor, source, projectionSeq)
}

// UpsertWithOutcome is Upsert plus whether the grant row actually changed
// (adapter.OutcomeUpserter). This adapter is guarded unconditionally (see
// Guarded), so without it every grant the §6.14 monotonic guard declined would
// reach the pipeline's audit gate as a write that happened — and this target is
// the read-grant table itself, where the entry would assert that an anchor
// became readable when it did not.
//
// Key stays empty and Transition TransitionNone for the same reasons the base
// Postgres adapter leaves them: a grant row has no single rendered key string,
// and this path never reads the stored row's liveness back.
func (g *GrantWriterAdapter) UpsertWithOutcome(ctx context.Context, keys map[string]any, _ map[string]any, projectionSeq uint64) (UpsertOutcome, error) {
	actor, anchor, source, err := grantKeyFields(keys)
	if err != nil {
		return UpsertOutcome{}, err
	}
	if err := g.checkSource(source); err != nil {
		return UpsertOutcome{}, err
	}
	committed, err := g.w.UpsertGrantWithOutcome(ctx, actor, anchor, source, projectionSeq)
	if err != nil {
		return UpsertOutcome{}, err
	}
	return grantUpsertOutcome(committed), nil
}

// grantUpsertOutcome and grantDeleteOutcome map a grant statement's "did a row
// change" answer onto the two outcome shapes.
//
// They are named rather than inlined at each call because the two directions
// have to stay in step and are otherwise ten lines apart: this pair IS the
// place a reader checks that a declined retraction reports exactly as little as
// a declined grant, and the place a test can check it without a database.
//
// Both statements are guarded unconditionally, so a row count of zero always
// means the monotonic guard declined — there is no unguarded arm here that
// could touch no row for some other reason.
func grantUpsertOutcome(committed bool) UpsertOutcome {
	return UpsertOutcome{
		Wrote:               committed,
		Committed:           committed,
		DeclinedByWatermark: !committed,
	}
}

func grantDeleteOutcome(committed bool) DeleteOutcome {
	return DeleteOutcome{
		Wrote:               committed,
		DeclinedByWatermark: !committed,
	}
}

// Delete tombstones a grant (seq-guarded), so a stale replay cannot resurrect it.
func (g *GrantWriterAdapter) Delete(ctx context.Context, keys map[string]any, projectionSeq uint64) error {
	actor, anchor, source, err := grantKeyFields(keys)
	if err != nil {
		return err
	}
	if err := g.checkSource(source); err != nil {
		return err
	}
	return g.w.RevokeGrant(ctx, actor, anchor, source, projectionSeq)
}

// DeleteWithOutcome is Delete plus whether the tombstone actually landed
// (adapter.OutcomeDeleter).
//
// This is the over-grant direction and the reason the pair exists at all. The
// guard declines a revocation by writing nothing and returning nil, so a caller
// reading only the error books a withdrawal that did not happen — on the table
// every protected read consults, which means the platform believes a capability
// is gone while the RLS policy keeps honouring it.
//
// Wrote follows the row count, which on this statement is unambiguous: the
// INSERT arm plants a tombstone whenever the row is absent, so only the
// monotonic WHERE clause can leave zero rows behind (see
// PostgresGrantWriter.RevokeGrantWithOutcome). Key and Transition stay empty for
// the reasons UpsertWithOutcome gives above.
func (g *GrantWriterAdapter) DeleteWithOutcome(ctx context.Context, keys map[string]any, projectionSeq uint64) (DeleteOutcome, error) {
	actor, anchor, source, err := grantKeyFields(keys)
	if err != nil {
		return DeleteOutcome{}, err
	}
	if err := g.checkSource(source); err != nil {
		return DeleteOutcome{}, err
	}
	committed, err := g.w.RevokeGrantWithOutcome(ctx, actor, anchor, source, projectionSeq)
	if err != nil {
		return DeleteOutcome{}, err
	}
	return grantDeleteOutcome(committed), nil
}

// ListKeys returns every live grant this lens owns — the rows carrying its
// declared grant_source — as (actor_id, anchor_id, grant_source) maps, the
// same shape Upsert/Delete take as keys.
//
// The source scoping is load-bearing, not a filter for efficiency:
// actor_read_grants is shared across every producer (see the type doc), so the
// pipeline's DiffRetraction, which retracts every listed key the fresh
// re-projection no longer produces, would revoke every OTHER package's grants
// on this lens's first event if this enumerated the whole table. A lens that
// declared no grant_source therefore gets an error, never an unscoped list:
// there is no safe fallback, and DiffRetraction's activation guard refuses the
// lens up front rather than letting it reach here.
func (g *GrantWriterAdapter) ListKeys(ctx context.Context) ([]map[string]any, error) {
	if g.source == "" {
		return nil, fmt.Errorf("grant writer adapter: list keys requires a declared grantSource — %s is shared across producers and an unscoped list would retract every other producer's grants", GrantTable)
	}
	return g.w.ListGrantsBySource(ctx, g.source)
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
	_ Adapter            = (*ProtectedAdapter)(nil)
	_ Truncater          = (*ProtectedAdapter)(nil)
	_ KeyLister          = (*ProtectedAdapter)(nil)
	_ PartitionKeyLister = (*ProtectedAdapter)(nil)
	_ SeqGuarded         = (*ProtectedAdapter)(nil)
	_ RowReader          = (*ProtectedAdapter)(nil)
	_ OutcomeUpserter    = (*ProtectedAdapter)(nil)
	_ OutcomeDeleter     = (*ProtectedAdapter)(nil)
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

// UpsertWithOutcome encodes the declared array columns then delegates to the
// base adapter, reporting what it reports (adapter.OutcomeUpserter).
//
// Spelling it out is load-bearing, exactly as ListKeys below is: the inner
// adapter is a NAMED FIELD, not embedded, so a wrapper satisfies only the
// optional interfaces it re-declares. writeResults prefers UpsertWithOutcome
// and falls back to treating a plain Upsert as always written — so without this
// method every protected lens, which is to say every lens carrying the §6.14
// monotonic guard, would audit each write the guard declined as one that
// landed. The encoding is the whole of this wrapper's job and it happens before
// the delegation, so the outcome describes the same single statement the base
// adapter would have run on its own.
func (p *ProtectedAdapter) UpsertWithOutcome(ctx context.Context, keys map[string]any, row map[string]any, projectionSeq uint64) (UpsertOutcome, error) {
	encoded, err := p.encodeArrays(row)
	if err != nil {
		return UpsertOutcome{}, err
	}
	return p.inner.UpsertWithOutcome(ctx, keys, encoded, projectionSeq)
}

// Delete delegates to the base adapter (the key columns are never arrays).
func (p *ProtectedAdapter) Delete(ctx context.Context, keys map[string]any, projectionSeq uint64) error {
	return p.inner.Delete(ctx, keys, projectionSeq)
}

// DeleteWithOutcome delegates to the base adapter and reports what it reports
// (adapter.OutcomeDeleter).
//
// Re-declaring it is what makes it reachable: the inner adapter is a named
// field, so a wrapper carries only the optional interfaces it names itself.
// Without this method writeResults treats every retraction here as landed, and
// a protected adapter is guarded by construction — so a delete the ordering
// guard refused would be audited as a withdrawal and would tick the read
// model's freshness clock. The lenses that retract most are the DiffRetraction
// ones, whose whole job is removing rows a fresh projection no longer produces.
func (p *ProtectedAdapter) DeleteWithOutcome(ctx context.Context, keys map[string]any, projectionSeq uint64) (DeleteOutcome, error) {
	return p.inner.DeleteWithOutcome(ctx, keys, projectionSeq)
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
// NewProtectedAdapter). The pipeline's rebuild path checks this via the
// `interface{ Guarded() bool }` assertion to force a truncate before
// rescanning a guarded adapter, the same protection the KV-guarded lenses
// already get.
func (p *ProtectedAdapter) Guarded() bool { return p.inner.Guarded() }

// Truncate delegates to the base adapter so a protected read model still
// supports truncate-before-rebuild (FR29).
func (p *ProtectedAdapter) Truncate(ctx context.Context) error { return p.inner.Truncate(ctx) }

// ListKeys delegates to the base adapter, which already lists this table's own
// key columns and excludes the guarded Delete's soft tombstones — a protected
// table is always guarded, so its tombstoned rows are correctly absent from the
// "currently live" set the diff compares against.
//
// Wrapping is why this has to be spelled out: the pipeline reaches DiffRetraction
// through an adapter.KeyLister type assertion, and a wrapper only satisfies an
// optional interface it re-declares. Without this method every protected lens
// declaring DiffRetraction — the very shape the mechanism was built for, a
// composite key with a column bound to a neighbor rather than the row's own
// anchor — silently retracted nothing, so a row kept its authz_anchors after the
// link that justified them was gone. Found live 2026-07-19 on
// landlordUnitsRead and landlordLeaseApplicationsRead.
func (p *ProtectedAdapter) ListKeys(ctx context.Context) ([]map[string]any, error) {
	return p.inner.ListKeys(ctx)
}

// ListKeysWhere delegates the partition-scoped listing (adapter.PartitionKeyLister)
// to the base adapter, which applies the same live-rows condition its whole
// listing applies.
//
// Re-declaring it is what makes it reachable, for the reason ListKeys above
// gives: the pipeline reaches the partition-scoped diff through a
// PartitionKeyLister type assertion, and a wrapper satisfies only the optional
// interfaces it re-declares. Every lens this mechanism arms is Protected —
// landlordLeaseApplicationsRead, landlordUnitsRead and
// objectIdentityAttachmentsRead all activate through this wrapper — so without
// this method the activation gate would refuse to arm the very lenses the
// partition transport exists for.
func (p *ProtectedAdapter) ListKeysWhere(ctx context.Context, fixed map[string]any, prefix string) ([]map[string]any, error) {
	return p.inner.ListKeysWhere(ctx, fixed, prefix)
}

// GetRow delegates to the base adapter's read-back (adapter.RowReader). No
// array re-encoding is needed on the way out: encodeArrays exists only to
// turn the engine's []any into the []string pgx needs to WRITE a text[]
// column, and a value GetRow reads back already comes decoded through pgx's
// own array codec — a Go value ready for canonicalJSON's comparison exactly
// as the base adapter returns it.
//
// Re-declaring it is what makes it reachable: the inner adapter is a named
// field, not embedded, so a wrapper satisfies only the optional interfaces it
// re-declares (the same reason ListKeys above and DeleteWithOutcome before it
// exist). Without this method a Protected lens's own adapter type-asserts as
// a non-RowReader regardless of what the base PostgresAdapter implements, so
// the divergence audit could never enrol it and the plain-arm neighbour
// derivation could never narrow it. leaseApplicationsRead
// (packages/lease-signing/lenses.go) is exactly this shape — Protected: true
// and plain (non-actorAggregate) — so it activates through this wrapper, not
// a bare PostgresAdapter, and its enrolment depends on this method existing.
func (p *ProtectedAdapter) GetRow(ctx context.Context, keys map[string]any) (map[string]any, bool, error) {
	return p.inner.GetRow(ctx, keys)
}
