package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// ErrNotActorAggregate is returned by Reproject on a pipeline that has
// neither envelope wrapper installed (envelopeFn for a one-document-per-actor
// lens, multiEnvelopeFn for a perEntry one — cap-read-per-anchor-grant-keys-design.md
// §3.3). Per-actor reconciliation is defined only for actor-aggregate lenses:
// a plain lens retracts through filter-diff retraction and the Personal Lens
// has its own Hydrate, so neither needs — nor can answer — a per-actor
// reprojection request.
var ErrNotActorAggregate = errors.New("pipeline: reproject: lens is not actor-aggregate")

// ErrNoOrderingToken is returned when reconciliation would have to write
// through the KV projection guard while the pipeline has no usable ordering
// token — its last-applied sequence is still zero because this process has not
// applied anything yet.
//
// The §6.2 KV guard drops a token-less write outright, BEFORE it compares
// against any stored watermark, so that it can neither create a clobberable
// seq-0 key nor no-op a real update (adapter.NatsKVAdapter.guardedWrite). Such
// a write cannot land whether the target row is present or absent, and the nil
// the adapter returns is a silent no-op every caller would read as a heal.
// Refusing is strictly better than that silence — otherwise the sweep
// recomputes, rewrites, and is dropped again on every tick forever, scoring
// phantom hits into the prefilter hints' earned share and counting repairs that
// never happened.
//
// Nothing durable is lost by refusing: the write was not landing, and
// seedAppliedSeqFromAckFloor gives a pipeline with any acked history a non-zero
// token at startup, so a genuinely cold pipeline heals on its first applied
// event.
var ErrNoOrderingToken = errors.New("pipeline: reproject: pipeline has applied no events, so it holds no ordering token and the guard would drop the write")

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
//
// Three callers reach this method: the sweep's deep-verify, the operator
// control-plane "reproject" RPC (control/service.go), and — since §4.3 —
// the pipeline's own retry queue re-evaluating a perEntry actor whose write
// failed transiently (enqueueActorReprojectRetry). Widening the gate below
// to accept multiEnvelopeFn makes a perEntry lens reachable through all
// three, not just the retry path.
func (p *Pipeline) Reproject(ctx context.Context, actorKey string) (Reprojection, error) {
	if p.envelopeFn == nil && p.multiEnvelopeFn == nil {
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
	if _, _, ok := substrate.ParseVertexKey(actorKey); !ok {
		// A non-vertex actorKey (an aspect or link key handed in by mistake)
		// would evaluate the anchor MATCH against it, resolve to zero rows,
		// and return a clean Reprojection{Wrote: false} — every caller reads
		// that as "converged, nothing to do", so the request this call was
		// actually meant to satisfy silently vanishes with no error and no
		// trace. Refuse it here rather than let each of the three callers
		// (sweep, control-plane RPC, retry-queue actor-reproject) re-derive
		// the same guard independently.
		return Reprojection{}, fmt.Errorf("pipeline: reproject: actorKey %q is not a Contract #1 vertex key", actorKey)
	}

	seq := p.Progress().LastAppliedSeq

	// Sweep, control-plane RPC and the retry queue all reach here off the
	// consumer goroutine, so this entry point takes its own rule snapshot.
	rs := p.ruleState()

	results, err := p.reprojectActors(ctx, rs, []string{actorKey})
	if err != nil {
		return Reprojection{}, fmt.Errorf("pipeline: reproject %q: %w", actorKey, err)
	}

	out := Reprojection{Actor: actorKey, ProjectionSeq: seq}
	adpt := p.currentAdapter()
	reader, canRead := adpt.(adapter.RowReader)

	// Every result is attempted even after one fails: a doc-mode lens's
	// single result made "abort on first error" indistinguishable from
	// "attempt all", but a perEntry lens's actor-reproject (§4.3 of
	// cap-read-per-anchor-grant-keys-design.md) can carry many results for
	// one actor, and this call is never retried per-result — the whole
	// actor is the retry unit (writeResults' enqueueActorReprojectRetry). A
	// deterministic failure on one anchor must not permanently block a
	// transiently-failing sibling's heal; errors are joined and reported
	// together so the caller still sees (and can retry) the failure.
	var errs []error
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
				errs = append(errs, ErrNoOrderingToken)
				continue
			}
			if derr := adpt.Delete(ctx, result.Keys, seq); derr != nil {
				errs = append(errs, fmt.Errorf("delete %v: %w", result.Keys, derr))
				continue
			}
			out.Deleted = true
			out.Wrote = true
			continue
		}

		if canRead {
			stored, present, rerr := reader.GetRow(ctx, result.Keys)
			if rerr != nil {
				errs = append(errs, fmt.Errorf("read stored row %v: %w", result.Keys, rerr))
				continue
			}
			if present && rowsEquivalent(stored, result.Row) {
				out.Converged = true
				continue
			}
			// The KV guard drops a token-less write outright, BEFORE it looks
			// for a stored watermark, so under it an absent row is no more
			// writable than a present one — both come back nil having written
			// nothing. Two conditions narrow this to exactly that guard. The
			// block is entered only by an adapter that reads its own rows back
			// (the NATS-KV family), which excludes a SQL-guarded target: that
			// one conditions only its UPDATE branch, so its absent-row insert
			// really does land at token zero. And the adapter must actually
			// have the guard enabled — an unguarded target ignores the token
			// entirely, so refusing would decline a create that would have
			// succeeded, which is the lost-first-projection heal.
			if guard, guarded := adpt.(adapter.SeqGuarded); seq == 0 && guarded && guard.Guarded() {
				errs = append(errs, ErrNoOrderingToken)
				continue
			}
		}

		if uerr := adpt.Upsert(ctx, result.Keys, result.Row, seq); uerr != nil {
			errs = append(errs, fmt.Errorf("upsert %v: %w", result.Keys, uerr))
			continue
		}
		out.Wrote = true
	}

	if len(errs) > 0 {
		return Reprojection{}, fmt.Errorf("pipeline: reproject %q: %d/%d results failed: %w",
			actorKey, len(errs), len(results), errors.Join(errs...))
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
