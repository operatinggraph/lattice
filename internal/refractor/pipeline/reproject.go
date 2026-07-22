package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// ErrNotActorAggregate is returned by Reproject on a pipeline that has no
// envelope wrapper installed. Per-actor reconciliation is defined only for
// actor-aggregate lenses: a plain lens retracts through filter-diff
// retraction and the Personal Lens has its own Hydrate, so neither needs —
// nor can answer — a per-actor reprojection request.
var ErrNotActorAggregate = errors.New("pipeline: reproject: lens is not actor-aggregate")

// ErrNoOrderingToken is returned when reconciliation would have to OVERWRITE or
// RETRACT an existing row while the pipeline has no usable ordering token — its
// last-applied sequence is still zero because this process has not applied
// anything yet.
//
// The §6.2 guard rejects any write whose projectionSeq is `<=` the stored one,
// so such a write cannot land: it would be dropped by the guard while every
// caller read it as a successful heal. Refusing is strictly better than that
// silence — otherwise the sweep recomputes, rewrites, and is rejected again on
// every tick forever, churning the auth plane while holding
// CapabilityCoverageDivergence open on a divergence it never actually repaired.
//
// Creating an ABSENT row is unaffected: the guard's absent-key branch takes
// Create, which has no stored watermark to lose to, so the lost-first-projection
// case this whole design exists for still heals from a cold pipeline.
var ErrNoOrderingToken = errors.New("pipeline: reproject: pipeline has applied no events, so it holds no ordering token and the guard would reject the write")

// Reprojection reports what one Reproject call did to one actor's row.
type Reprojection struct {
	// Actor is the vertex key that was re-evaluated.
	Actor string
	// Converged is true when the stored row already equalled the recomputed
	// one and nothing was written.
	Converged bool
	// Deleted is true when reconciliation removed the row (actor absent, or
	// the envelope's empty semantics retract it).
	Deleted bool
	// Wrote is true when a divergence was healed by an upsert or a delete.
	Wrote bool
	// ProjectionSeq is the ordering token the write carried — the pipeline's
	// last-applied stream sequence captured before re-evaluation.
	ProjectionSeq uint64
}

// volatileEnvelopeFields are stamped fresh on every evaluation and therefore
// carry no divergence signal: comparing them would make every reconciliation
// look divergent and defeat the zero-write steady state. projectionSeq is
// already stripped by adapter.RowReader.GetRow; projectedAt is the wall-clock
// stamp the envelope applies per evaluation (projection.OutputDescriptor's
// EnvelopeFn). Everything else — including projectedFromRevisions — is
// compared, so a genuine source-revision change still reads as divergence.
var volatileEnvelopeFields = []string{"projectedAt"}

// Reproject re-executes one actor's projection and reconciles the stored row
// with it (capability-projection-reconciliation-design.md §3.1). It is the
// auth plane's targeted heal for the class where a CDC event lost to a
// pipeline-availability gap leaves a doc permanently absent: the graph is the
// truth, this recomputes from it, and the §6.2 guard keeps the write
// subordinate to any real CDC event that races it.
//
// The ordering token is the pipeline's own forward progress
// (Progress().LastAppliedSeq) captured BEFORE re-evaluation — the same
// capture-then-reproject discipline Hydrate uses. Any CDC event not yet
// reflected in the read carries a strictly greater stream sequence, so its
// projection overwrites this write under the guard's `<=`-rejects rule; ties
// drop the reconciliation write because the stored doc already reflects that
// event. It is deliberately NOT the shred nullifier's MaxInt64: that stamp is
// a terminal authority, and using it here would freeze the key against all
// future CDC — the inversion of intent.
//
// A converged actor costs zero KV writes: the recomputed body is compared
// against the stored one (modulo volatileEnvelopeFields) and the write is
// dropped when they match, so the sweep in Fire 1b is churn-free at rest.
func (p *Pipeline) Reproject(ctx context.Context, actorKey string) (Reprojection, error) {
	if p.envelopeFn == nil {
		return Reprojection{}, ErrNotActorAggregate
	}
	if _, isPersonal := p.currentAdapter().(adapter.KeySetPublisher); isPersonal {
		// A Personal Lens also installs an envelopeFn (the actor-fan-out
		// injection), so the check above alone doesn't exclude it — but
		// Reproject's RowReader-diff reconciliation model was never built
		// for an append-only personal target (no GetRow, no cap-shaped
		// missing-actor Delete since personal-lens-retraction-design.md
		// §3.4 — reprojectActors now silently skips that branch for a
		// KeySetPublisher adapter, which would otherwise turn a real
		// reconciliation gap into a quiet no-op here). Personal Lens has
		// its own reconciliation path: Hydrate.
		return Reprojection{}, ErrNotActorAggregate
	}
	if actorKey == "" {
		return Reprojection{}, fmt.Errorf("pipeline: reproject: actorKey is required")
	}

	seq := p.Progress().LastAppliedSeq

	results, err := p.reprojectActors(ctx, []string{actorKey})
	if err != nil {
		return Reprojection{}, fmt.Errorf("pipeline: reproject %q: %w", actorKey, err)
	}

	out := Reprojection{Actor: actorKey, ProjectionSeq: seq}
	adpt := p.currentAdapter()
	reader, canRead := adpt.(adapter.RowReader)

	for _, result := range results {
		if result.Delete {
			// A delete is skippable only when the row is already gone;
			// GetRow reports a soft-deleted row as absent too, so an
			// already-retracted actor writes nothing.
			if canRead {
				if _, present, rerr := reader.GetRow(ctx, result.Keys); rerr == nil && !present {
					out.Converged = true
					continue
				}
			}
			if seq == 0 {
				return Reprojection{}, ErrNoOrderingToken
			}
			if derr := adpt.Delete(ctx, result.Keys, seq); derr != nil {
				return Reprojection{}, fmt.Errorf("pipeline: reproject %q: delete: %w", actorKey, derr)
			}
			out.Deleted = true
			out.Wrote = true
			continue
		}

		if canRead {
			stored, present, rerr := reader.GetRow(ctx, result.Keys)
			if rerr != nil {
				return Reprojection{}, fmt.Errorf("pipeline: reproject %q: read stored row: %w", actorKey, rerr)
			}
			if present && rowsEquivalent(stored, result.Row) {
				out.Converged = true
				continue
			}
			// A divergent row that already exists can only be corrected by a
			// write that outranks its stored watermark.
			if present && seq == 0 {
				return Reprojection{}, ErrNoOrderingToken
			}
		}

		if uerr := adpt.Upsert(ctx, result.Keys, result.Row, seq); uerr != nil {
			return Reprojection{}, fmt.Errorf("pipeline: reproject %q: upsert: %w", actorKey, uerr)
		}
		out.Wrote = true
	}

	if out.Wrote {
		out.Converged = false
	}
	return out, nil
}

// rowsEquivalent compares a stored row against a freshly computed one,
// ignoring the fields that are restamped on every evaluation. Both sides are
// copied before the volatile keys are dropped so neither the caller's computed
// row nor the adapter's returned map is mutated.
//
// Comparison is by canonical JSON rendering — the same identity basis
// resultsContainKeys uses — because the stored row has been through a JSON
// round-trip (numbers decode as float64, lists as []any) while the computed
// row still carries the engine's in-memory Go types. A structural comparison
// would read those as divergent for byte-identical documents and turn every
// reconciliation into a write.
func rowsEquivalent(stored, computed map[string]any) bool {
	a, aerr := canonicalJSON(stored)
	b, berr := canonicalJSON(computed)
	if aerr != nil || berr != nil {
		return false
	}
	return bytes.Equal(a, b)
}

// canonicalJSON renders a row without its volatile fields. encoding/json emits
// map keys in sorted order, so the rendering is stable for a given content.
func canonicalJSON(row map[string]any) ([]byte, error) {
	clean := make(map[string]any, len(row))
	maps.Copy(clean, row)
	for _, f := range volatileEnvelopeFields {
		delete(clean, f)
	}
	return json.Marshal(clean)
}
