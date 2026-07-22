package projection

import (
	"fmt"
	"log/slog"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// EnvelopeFn builds the pipeline envelope wrapper that turns each per-actor
// RETURN row into the on-wire document the descriptor describes. It is the
// single data-driven replacement for the per-canonical-name capability
// envelope wrappers: one path, parameterized by the compiled OutputDescriptor.
//
// lensDefKey is the meta-lens vertex key (vtx.meta.<id>); revisionOf returns the
// current Core KV revision of a key (0 = unknown/absent). Both feed
// projectedFromRevisions via ContributingSources (§6.3, freshness: auto).
//
// Behavior, reproducing the built-in wrappers exactly:
//   - A row whose anchor actorKey is empty is declined (ErrSkipProjection) — it
//     is the degenerate aggregation row a cypher produces over zero anchor
//     bindings. The my-tasks wrapper additionally falls back to the bound
//     params["actorKey"] before declining, so a last-task-closed actor deletes
//     its key rather than leaving it stale; this driver does the same.
//   - A row whose anchor is not the descriptor's AnchorType is declined.
//   - Each body column projects by the SHAPE of its RETURN value: a list
//     (collect) value is realness-filtered (the roster path — drop degenerate
//     null-key collect entries); a scalar value (bool, string, number, or nil)
//     projects VERBATIM (the convergence path — a scalar Weaver reads as a bool
//     or a string param). A nil scalar projects as a genuine null so a downstream
//     bool reads false and a string param reads absent, never as `[]`.
//   - When the empty behavior is delete/softDelete and the realness check finds
//     no real value, the row is declined with ErrDeleteProjection keyed at
//     BuildKey(actorKey). Realness for a list column is "any real entry after the
//     filter"; for a designated scalar realness column it is "the scalar is
//     present and real" (a convergence lens marks the anchor alive that way).
//   - Otherwise the envelope is {key, <actorField>: actorKey, version,
//     projectedAt, projectedFromRevisions, [lanes], <bodyColumns...>,
//     <staticEmptyColumns...: []>}.
func (d OutputDescriptor) EnvelopeFn(lensDefKey string, revisionOf func(string) uint64) pipeline.EnvelopeFn {
	return func(row map[string]any, keys map[string]any, params map[string]any) (map[string]any, map[string]any, error) {
		actorKey, _ := row["actorKey"].(string)
		if actorKey == "" {
			actorKey, _ = params["actorKey"].(string)
		}
		if actorKey == "" {
			return nil, nil, pipeline.ErrSkipProjection
		}
		vtxType, _, ok := substrate.ParseVertexKey(actorKey)
		if !ok {
			return nil, nil, fmt.Errorf("projection: actorKey %q is not a Contract #1 vertex key", actorKey)
		}
		if vtxType != d.AnchorType {
			return nil, nil, pipeline.ErrSkipProjection
		}

		outKey := d.BuildKey(actorKey)

		// Project each body column by the SHAPE of its RETURN value and decide
		// the empty-result action. A list column is realness-filtered (the roster
		// path); a scalar column projects verbatim (the convergence path) — a
		// scalar RETURN value (bool, string, number, nil) is never coerced to []
		// so Weaver's boolColumn / string-param resolution reads it directly.
		projected := make(map[string]any, len(d.BodyColumns))
		anyReal := false
		for _, col := range d.BodyColumns {
			if list, isList := row[col].([]any); isList {
				vals := d.RealnessFiltered(list)
				if vals == nil {
					vals = []any{}
				}
				projected[col] = vals
				if len(vals) > 0 {
					anyReal = true
				}
				continue
			}
			// Scalar passthrough: the raw value as-is (a nil scalar stays nil, so
			// the envelope carries a genuine null, not an empty list).
			projected[col] = row[col]
		}

		// A designated scalar realness column (e.g. a convergence lens's
		// entityKey) marks the anchor alive when present and real. This is
		// distinct from the roster realness (a field inside each list entry); a
		// roster lens names a field that lives inside its collect entries, never a
		// top-level scalar column, so this check is dormant for the roster lenses.
		if d.RealnessFilter != "" {
			if v, isCol := row[d.RealnessFilter]; isCol {
				if _, isList := v.([]any); !isList && isRealField(v) {
					anyReal = true
				}
			}
		}

		if !anyReal && d.RealnessFilter != "" {
			switch d.EmptyAction() {
			case ActionDelete, ActionSoftDelete:
				return nil, map[string]any{"key": outKey}, pipeline.ErrDeleteProjection
			case ActionSkip:
				return nil, nil, pipeline.ErrSkipProjection
			case ActionWriteEmptyDoc:
				// Fall through to build the envelope with every body column
				// already empty-after-realness — the key stays present with an
				// empty body, which is exactly the empty-doc behavior.
			}
		}

		envelope := map[string]any{
			"key":                    outKey,
			d.ActorField:             actorKey,
			"version":                Version,
			"projectedAt":            params["projectedAt"],
			"projectedFromRevisions": ContributingSources(actorKey, lensDefKey, []map[string]any{row}, revisionOf),
		}
		if len(d.Lanes) > 0 {
			envelope["lanes"] = append([]string(nil), d.Lanes...)
		}
		for _, col := range d.BodyColumns {
			envelope[col] = projected[col]
		}
		for _, col := range d.StaticEmptyColumns {
			envelope[col] = []any{}
		}

		return envelope, map[string]any{"key": outKey}, nil
	}
}

// Version is the Capability KV envelope schema version (Contract #6 §6.3),
// pinned to "1.0" for Phase 1. Every actor-aggregate document carries it.
const Version = "1.0"

// InstallActorAggregate wires an actor-aggregate lens through the compiled
// ProjectionPlan: the §6.13 Output descriptor drives the on-wire envelope, the
// per-actor cross-vertex fan-out, the empty/delete-key behavior, and the §6.2
// guard predicate — all from lens-definition data, with no canonical-name
// knowledge. Returns false when the lens must NOT be registered (a fail-closed
// descriptor error), true once the components are installed.
//
// Fan-out uses the broad adjacency ActorEnumerator — the sound superset that
// can never miss an affected anchor, so it over-reprojects rather than under-
// reprojecting a security-plane lens.
func InstallActorAggregate(
	p *pipeline.Pipeline,
	adpt adapter.Adapter,
	r *lens.Rule,
	projectionRevision func(string) uint64,
	adjKV, coreKV *substrate.KV,
	logger *slog.Logger,
) bool {
	desc, err := ParseOutputDescriptor(r.Output)
	if err != nil {
		logger.Error("actor-aggregate output descriptor invalid — refusing registration",
			"lensId", r.ID, "err", err)
		return false
	}

	authPlane := IsAuthPlane(r)
	if _, err := Compile(r); err != nil {
		logger.Error("actor-aggregate plan compile failed — refusing registration",
			"lensId", r.ID, "err", err)
		return false
	}

	lensDefKey := "vtx.meta." + r.ID
	p.SetEnvelopeFn(desc.EnvelopeFn(lensDefKey, projectionRevision))
	p.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, desc.AnchorType))
	p.SetActorDeleteKey(desc.BuildKey)
	p.SetLatencyBuffer(pipeline.NewLatencyRingBuffer(pipeline.DefaultLatencyBufferSize))

	// The auth-plane convergence sweep (capability-projection-reconciliation-
	// design.md §3.2). Installing a plan is what opts a lens in, and only an
	// auth-plane actor-aggregate lens receives one — a plain lens retracts
	// through filter/diff retraction and the Personal Lens has its own Hydrate,
	// so neither is excluded by a name list; it simply never gets a plan.
	if authPlane {
		p.SetSweepPlan(pipeline.SweepPlan{
			AnchorType:    desc.AnchorType,
			BuildKey:      desc.BuildKey,
			AnchorFromKey: desc.AnchorFromKey,
		})
	}

	guarded := authPlane || desc.RequiresGuardedTombstone()
	if guarded {
		if gErr := EnableProjectionGuard(adpt, r.ID); gErr != nil {
			logger.Error("actor-aggregate guard", "lensId", r.ID, "err", gErr)
			return false
		}
	}

	logger.Info("actor-aggregate envelope + fan-out + delete-key + latency installed",
		"lensId", r.ID, "lensDefKey", lensDefKey,
		"anchorType", desc.AnchorType, "guarded", guarded, "authPlane", authPlane)
	return true
}

// EnableProjectionGuard turns on the monotonic projection-write guard for a
// NATS-KV-backed lens. The caller decides which lenses are guarded from the
// compiled plan predicate (auth-plane or empty-delete tombstone) and flips the
// flag here. The guarded lenses are security/correctness-plane, so an adapter
// that cannot enforce the guard (e.g. a Postgres target) is a fail-closed error,
// not a silent downgrade: a guarded lens running unguarded re-opens the
// resurrection window the guard exists to close.
func EnableProjectionGuard(adpt adapter.Adapter, lensID string) error {
	nkv, ok := adpt.(*adapter.NatsKVAdapter)
	if !ok {
		return fmt.Errorf("projection-write guard required for lens %s but target adapter cannot enforce it (not NATS-KV)", lensID)
	}
	nkv.SetGuarded(true)
	return nil
}
