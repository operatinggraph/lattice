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

// ErrRuleSuperseded reports that the rule an actor's re-evaluation ran under
// was replaced before its results could be written, so those results derive
// from a rule no longer in force and must not land. It mirrors writeResults'
// supersededRule refusal on the CDC path — the two are one policy over the
// target's two write paths.
//
// The condition is per-pipeline, not per-actor: no actor is at fault, and the
// results in hand are equally stale for every one of them.
var ErrRuleSuperseded = errors.New("pipeline: reproject: the rule this evaluation ran under was replaced before the write, so its results are stale")

// Verdict is Reproject's conclusion for one actor. Every successful call
// reaches exactly one; the zero value is VerdictUnverified, so a branch added
// later that forgets to conclude reports "I do not know" rather than
// "converged".
//
// The verdict is explicit rather than inferred from the write outcome because
// an outcome the repair path has no transport for produces no write — and a
// signal derived from writes reads that as convergence, making a lens look
// healthier the more thoroughly it is broken.
type Verdict uint8

const (
	// VerdictUnverified means no comparison was reached, so the actor's
	// correctness is unknown. See Reprojection.VerdictReason.
	VerdictUnverified Verdict = iota
	// VerdictConverged means the stored row already equalled the recomputed
	// one, or was correctly absent.
	VerdictConverged
	// VerdictHealed means a divergence was found and the repair landed.
	VerdictHealed
	// VerdictBlocked means a divergence was found and the repair provably did
	// NOT land: the ordering guard declined it against an equal-or-fresher
	// stored watermark (Contract #6 §6.2). The row stays wrong until a real
	// CDC event above that watermark reprojects it.
	VerdictBlocked
)

// String renders a Verdict for logs and health fields.
func (v Verdict) String() string {
	switch v {
	case VerdictConverged:
		return "converged"
	case VerdictHealed:
		return "healed"
	case VerdictBlocked:
		return "blocked"
	default:
		return "unverified"
	}
}

// severity orders verdicts by how much attention they deserve:
// blocked > unverified > healed > converged. A confirmed unrepairable row
// outranks "I do not know" (the sweep knows exactly what is wrong and cannot
// fix it), which outranks a heal (a divergence that was repaired).
func (v Verdict) severity() int {
	switch v {
	case VerdictBlocked:
		return 3
	case VerdictUnverified:
		return 2
	case VerdictHealed:
		return 1
	default: // VerdictConverged
		return 0
	}
}

// BlockedClass names WHICH condition a VerdictBlocked result failed to repair.
// It is carried explicitly from the point of classification to the operator: the
// class decides how loudly the condition is reported, and recovering it by
// matching on a reason string would be one clause standing in for a set of
// shapes nobody enumerated.
//
// The zero value is BlockedUnknown, so a blocked result that reaches a consumer
// without being classified reports "I cannot prove which kind" rather than being
// demoted to the benign class. That is the same fail-closed default
// VerdictUnverified takes for Verdict.
type BlockedClass uint8

const (
	// BlockedUnknown means the class could not be proven — the write path had
	// no read-back to classify with, or none was consulted.
	BlockedUnknown BlockedClass = iota
	// BlockedProvenance means the stored and recomputed rows differ ONLY in
	// projectedFromRevisions: the row's meaning is identical. Contract #6 §6.3
	// classifies that record as coherence/debug provenance, and it drifts under
	// ordinary operation — a lens-definition write that leaves the MATCH
	// unchanged reprojects nothing yet diverges every row.
	BlockedProvenance
	// BlockedContent means the rows differ in the row's actual content. At a
	// resting watermark that has no observed producer, so it is a real finding
	// on sight.
	BlockedContent
	// BlockedRetraction means a declined Delete: a retraction the guard refused,
	// so a revoked grant stays live and honoured. It is the over-grant
	// direction, and the only class that describes a row the graph says should
	// not exist at all.
	BlockedRetraction
)

// String renders a BlockedClass for health fields and logs.
func (c BlockedClass) String() string {
	switch c {
	case BlockedRetraction:
		return "retraction"
	case BlockedContent:
		return "content"
	case BlockedProvenance:
		return "provenance"
	default:
		return "unknown"
	}
}

// severity orders blocked classes by how much attention they deserve:
// retraction > content > unknown > provenance. It is one order, defined once,
// and it is what breaks the fold's tie between two blocked results, what picks
// the sweep's governing reason, and what drives the health severity.
//
// Retraction and content outrank the rest because neither has an observed
// ordinary producer, so the first sighting is the finding. Unknown sits above
// provenance rather than below it because a row whose class cannot be proven
// must not be treated as the benign one; provenance sits at the bottom because
// it is reachable by an ordinary operation and the row's meaning is unchanged.
func (c BlockedClass) severity() int {
	switch c {
	case BlockedRetraction:
		return 3
	case BlockedContent:
		return 2
	case BlockedProvenance:
		return 0
	default: // BlockedUnknown
		return 1
	}
}

// blockedClassFor maps the comparator's divergence classification to the class
// an operator acts on. divergenceNone reaches this only from a write path that
// held no read-back evidence, so it takes the unknown class — never a benign
// one, which would read as a claim the row was fine.
func blockedClassFor(d divergence) BlockedClass {
	switch d {
	case divergenceProvenance:
		return BlockedProvenance
	case divergenceContent:
		return BlockedContent
	default:
		return BlockedUnknown
	}
}

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
	// Wrote is true when a divergence was healed by an upsert or a delete
	// that actually landed. A write the ordering guard declined does not set
	// it — that is VerdictBlocked.
	Wrote bool
	// Verdict is this call's conclusion for the actor. One call can carry
	// many results for one actor (a perEntry lens), so it is the WORST
	// verdict any result reached: an actor with one blocked row and nine
	// converged ones is not converged.
	Verdict Verdict
	// VerdictReason names the cause behind a VerdictUnverified or
	// VerdictBlocked, so the issue an operator reads names a condition rather
	// than a count. Empty for Converged and Healed.
	VerdictReason string
	// BlockedClass is which condition a VerdictBlocked conclusion failed to
	// repair, and is meaningful only for that verdict — every other verdict
	// leaves it at BlockedUnknown. It travels beside VerdictReason rather than
	// inside it, and the two always describe the same result: a consumer
	// deciding severity reads this field, never the text.
	BlockedClass BlockedClass
	// ProjectionSeq is the ordering token the write carried — the pipeline's
	// last-applied stream sequence captured before re-evaluation.
	ProjectionSeq uint64
}

// verdictFold accumulates the per-result verdicts of one Reproject call into
// the actor's single conclusion.
//
// It tracks whether ANY result concluded, separately from what they concluded,
// because the two must not share an encoding. VerdictUnverified is both the
// fail-closed default (nothing concluded ⇒ "I do not know") and a verdict a
// result can actively reach, and it outranks Healed — so folding straight onto
// the zero value would let the default swallow every real conclusion after it,
// reporting a healed actor as unverified forever. The flag is what keeps the
// default and the verdict distinguishable.
type verdictFold struct {
	verdict   Verdict
	class     BlockedClass
	reason    string
	concluded bool
}

// add folds one non-blocked result's verdict in, keeping the worst and the
// reason that produced it. A blocked result goes through addBlocked, which is
// the only way a class reaches the fold; anything that arrives here blocked
// carries BlockedUnknown, the fail-closed class.
func (f *verdictFold) add(v Verdict, reason string) {
	f.addBlocked(v, BlockedUnknown, reason)
}

// addBlocked folds one result's verdict in together with the class of condition
// it failed to repair.
//
// Two blocked results TIE on verdict severity, so without the class the actor
// would report whichever the loop reached first: a perEntry actor holding one
// provenance-only blocked row and one content-divergence blocked row would name
// either, depending on iteration order. The class is that tie's order, and the
// reason travels with whichever class wins so the text and the class can never
// describe different results.
func (f *verdictFold) addBlocked(v Verdict, c BlockedClass, reason string) {
	if !f.concluded || v.severity() > f.verdict.severity() {
		f.verdict, f.class, f.reason, f.concluded = v, c, reason, true
		return
	}
	if v != f.verdict {
		// A strictly quieter verdict never displaces the held one.
		return
	}
	if v == VerdictBlocked {
		if c.severity() > f.class.severity() {
			f.class, f.reason = c, reason
		}
		return
	}
	if f.reason == "" {
		f.reason = reason
	}
}

// resolve returns the actor's conclusion. Nothing concluded means nothing was
// verified — never that everything converged.
func (f *verdictFold) resolve(defaultReason string) (Verdict, BlockedClass, string) {
	if !f.concluded {
		return VerdictUnverified, BlockedUnknown, defaultReason
	}
	return f.verdict, f.class, f.reason
}

// volatileEnvelopeFields are stamped fresh on every evaluation and therefore
// carry no divergence signal: comparing them would make every reconciliation
// look divergent and defeat the zero-write steady state. projectionSeq is
// already stripped by adapter.RowReader.GetRow; projectedAt is the wall-clock
// stamp the envelope applies per evaluation (projection.OutputDescriptor's
// EnvelopeFn). Everything else — including projectedFromRevisions — is
// compared, so a genuine source-revision change still reads as divergence.
var volatileEnvelopeFields = []string{"projectedAt"}

// provenanceEnvelopeFields carry the freshness record rather than the row's
// meaning: projectedFromRevisions maps each contributing graph key to the Core
// KV revision the projection read (projection.ContributingSources), which
// Contract #6 §6.3 classifies as coherence/debug provenance.
//
// They stay INSIDE the divergence comparison — a source-revision change is a
// real divergence and is repaired like any other. They are enumerated
// separately only so a reconciliation that provably could not write can say
// WHICH KIND of divergence it failed to repair. That distinction is the whole
// reason the Contract #6 §6.2 tie-rule amendment was held rather than
// shipped: provenance drift at a resting watermark is reachable by an ordinary
// operation (a lens-definition write that leaves the MATCH unchanged
// reprojects nothing yet diverges every row), while a CONTENT divergence at a
// tied token has no observed producer. Reporting both identically would either
// bury a real finding in provenance noise or raise an alarm on every benign
// tick.
var provenanceEnvelopeFields = []string{"projectedFromRevisions"}

// divergence classifies how a stored row differs from a freshly computed one.
type divergence uint8

const (
	// divergenceNone means the rows are equivalent modulo volatile fields.
	divergenceNone divergence = iota
	// divergenceProvenance means they differ ONLY in the freshness record —
	// the row's meaning is identical.
	divergenceProvenance
	// divergenceContent means they differ in the row's actual content.
	divergenceContent
)

func (d divergence) String() string {
	switch d {
	case divergenceProvenance:
		return "provenance-only divergence (projectedFromRevisions)"
	case divergenceContent:
		return "content divergence"
	default:
		// Reachable inside a blocked reason only when the write path had no
		// read-back to classify with, so the honest rendering is that the kind
		// is unknown — never "no divergence", which reads as a claim the row
		// was fine.
		return "divergence of unknown kind (no read-back)"
	}
}

// classifyDivergence compares a stored row against a freshly computed one and
// reports whether they differ, and if so whether the difference is confined to
// the provenance fields. Both sides are copied before any key is dropped, so
// neither the caller's computed row nor the adapter's returned map is mutated.
//
// Comparison is by canonical JSON rendering — the same identity basis
// resultsContainKeys uses — because the stored row has been through a JSON
// round-trip (numbers decode as float64, lists as []any) while the computed
// row still carries the engine's in-memory Go types. A structural comparison
// would read those as divergent for byte-identical documents and turn every
// reconciliation into a write.
//
// keys names the row's own key columns, which are excluded on both sides: the
// computed row carries every RETURN alias, key columns included, while a
// RowReader's GetRow may omit them (a Postgres GetRow scopes its SELECT by key
// and returns content columns only), and a row fetched BY those keys cannot
// differ in them.
func classifyDivergence(stored, computed, keys map[string]any) divergence {
	ignore := keyColumnNames(keys)
	if equal, _ := rowsComparableMasked(stored, computed, ignore); equal {
		return divergenceNone
	}
	ignore = append(ignore, provenanceEnvelopeFields...)
	a, aerr := canonicalJSON(stored, ignore...)
	b, berr := canonicalJSON(computed, ignore...)
	if aerr != nil || berr != nil {
		// A row that cannot be rendered cannot be proven provenance-only, so
		// it takes the louder classification rather than the quieter one.
		return divergenceContent
	}
	if bytes.Equal(a, b) {
		return divergenceProvenance
	}
	return divergenceContent
}

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

	// Refuse results derived from a rule that has since been replaced — the
	// same policy writeResults applies on the CDC path, at the same point in
	// the sequence (after evaluation, before any write). Checking earlier
	// would leave the window open for a swap landing DURING evaluation, which
	// is the longer interval.
	//
	// The check SHRINKS the window; it does not eliminate it. A MATCH
	// hot-reload increments the rule generation SYNCHRONOUSLY and only then
	// starts the goroutine that truncates the lens's rows (cmd/refractor's
	// reload path), so a swap that lands during evaluation — the long
	// interval, which is why the check sits here and not earlier — is caught.
	// A swap landing between this check and a write below is not, and for a
	// perEntry actor with many results that residual is the whole write loop.
	// Closing it entirely would need the check to be atomic with each write,
	// i.e. a lock held across the target I/O; the CDC path accepts the same
	// residual for the same reason. Without any check at all, a
	// still-running sweep pass lands an old-rule row into the emptied target
	// — where the absent-key branch takes it unconditionally — stamped at the
	// consumer HEAD, which is >= every sequence the rebuild is about to
	// replay. The rebuild is then locked out of that key by the very write it
	// was racing, and if the MATCH edit was a narrowing or a revocation the
	// frozen row is the pre-edit permission set.
	if p.supersededRule(rs) {
		return Reprojection{}, ErrRuleSuperseded
	}

	out := Reprojection{Actor: actorKey, ProjectionSeq: seq}
	var fold verdictFold
	adpt := p.currentAdapter()
	reader, canRead := adpt.(adapter.RowReader)
	outcomeUpserter, reportsUpsert := adpt.(adapter.OutcomeUpserter)
	outcomeDeleter, reportsDelete := adpt.(adapter.OutcomeDeleter)

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
			//
			// retractionEvidenced tracks whether this branch actually LOOKED.
			// Without a read-back — or after a read that errored — a delete
			// returning nil proves nothing: an idempotent retraction of an
			// absent key is indistinguishable from one that removed a live
			// row. The upsert branch below refuses to call that a heal, and
			// this branch must apply the same rule to the same fact.
			retractionEvidenced := false
			if canRead {
				_, present, rerr := reader.GetRow(ctx, result.Keys)
				switch {
				case rerr != nil:
					// A read that failed is not a read that found the row.
				case !present:
					out.Converged = true
					fold.add(VerdictConverged, "")
					continue
				default:
					retractionEvidenced = true
				}
			}
			if seq == 0 {
				errs = append(errs, ErrNoOrderingToken)
				continue
			}
			if reportsDelete {
				outcome, derr := outcomeDeleter.DeleteWithOutcome(ctx, result.Keys, seq)
				p.recordProjectionWrite()
				if derr != nil {
					errs = append(errs, fmt.Errorf("delete %v: %w", result.Keys, derr))
					continue
				}
				if outcome.DeclinedByWatermark {
					// The over-grant direction: a retraction the guard
					// declined leaves the row live. Reporting it as a heal is
					// how a revoking MATCH edit gets silently defeated while
					// the sweep logs a repair once a minute.
					fold.addBlocked(VerdictBlocked, BlockedRetraction,
						"stored watermark >= reconciliation token; retraction unrepairable")
					continue
				}
				out.Deleted = true
				out.Wrote = true
				// The convergence sweep's deep verify and the operator
				// reproject RPC both land here, and a retraction either of
				// them heals is as real a grant withdrawal as one the CDC
				// path writes. A consumer of the read-grant projection that
				// heard only about CDC-path flips would keep honouring a
				// grant the healer just took away.
				p.notifyGrantChange(outcome.Key, outcome.Transition)
				fold.add(retractionVerdict(retractionEvidenced))
				continue
			}
			derr := adpt.Delete(ctx, result.Keys, seq)
			p.recordProjectionWrite()
			if derr != nil {
				errs = append(errs, fmt.Errorf("delete %v: %w", result.Keys, derr))
				continue
			}
			out.Deleted = true
			out.Wrote = true
			fold.add(retractionVerdict(retractionEvidenced))
			continue
		}

		// divergedAs carries the read-back evidence from the comparison below
		// to the write outcome, so a declined write can name WHICH KIND of
		// divergence it failed to repair. Without a reader there is no
		// evidence at all, which is its own verdict.
		divergedAs := divergenceNone
		if canRead {
			stored, present, rerr := reader.GetRow(ctx, result.Keys)
			if rerr != nil {
				errs = append(errs, fmt.Errorf("read stored row %v: %w", result.Keys, rerr))
				continue
			}
			if present {
				divergedAs = classifyDivergence(stored, result.Row, result.Keys)
			} else {
				divergedAs = divergenceContent
			}
			if divergedAs == divergenceNone {
				out.Converged = true
				fold.add(VerdictConverged, "")
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

		if reportsUpsert {
			outcome, uerr := outcomeUpserter.UpsertWithOutcome(ctx, result.Keys, result.Row, seq)
			p.recordProjectionWrite()
			if uerr != nil {
				errs = append(errs, fmt.Errorf("upsert %v: %w", result.Keys, uerr))
				continue
			}
			if outcome.DeclinedByWatermark {
				// A divergence this path provably cannot repair. Which kind it
				// is decides whether an operator should act: provenance drift
				// at a resting watermark is reachable by ordinary operation,
				// whereas content divergence here has no known producer and is
				// a real finding on sight.
				fold.addBlocked(VerdictBlocked, blockedClassFor(divergedAs),
					"stored watermark >= reconciliation token; "+divergedAs.String()+" unrepairable")
				continue
			}
			if !outcome.Committed {
				// The guard's other way of storing nothing: a write carrying no
				// ordering token at all, dropped fail-closed before any stored
				// watermark is consulted. It earns its own verdict reason
				// because it is a separate fault with a separate fix — a
				// watermark conflict clears itself once this pipeline's token
				// advances past the stored one, whereas a missing token means
				// the caller had none to offer and every pass will keep failing
				// identically until it does.
				//
				// Reading Wrote here instead would book a repair that stored
				// nothing, and on this path that is a phantom heal on an
				// authorization surface: the sweep tells the capability plane's
				// health a divergent grant row was fixed while it sits exactly
				// as it was.
				// The class stays UNKNOWN whatever the read-back said: no stored
				// watermark was ever consulted, so this path has not established
				// that a guard conflict is what stands between the row and its
				// repair. The reason names the block cause it DID observe — a
				// missing token — and never the comparator's divergence kind,
				// so the text agrees with the class an operator reads for
				// severity.
				fold.addBlocked(VerdictBlocked, BlockedUnknown,
					"reconciliation write carried no ordering token; unrepairable")
				continue
			}
			// Every non-committing outcome has already continued out above, so
			// what reaches here landed.
			out.Wrote = true
			p.notifyGrantChange(outcome.Key, outcome.Transition)
			fold.add(writeVerdict(canRead))
			continue
		}

		uerr := adpt.Upsert(ctx, result.Keys, result.Row, seq)
		p.recordProjectionWrite()
		if uerr != nil {
			errs = append(errs, fmt.Errorf("upsert %v: %w", result.Keys, uerr))
			continue
		}
		out.Wrote = true
		fold.add(writeVerdict(canRead))
	}

	if len(errs) > 0 {
		return Reprojection{}, fmt.Errorf("pipeline: reproject %q: %d/%d results failed: %w",
			actorKey, len(errs), len(results), errors.Join(errs...))
	}

	if len(results) == 0 {
		// The evaluation produced nothing to reconcile against. When doc-mode
		// zero-row retraction is armed, a zero-row actor arrives here as a
		// DELETE result instead, so its absence was proven; a perEntry lens
		// runs multiEntryRetractions unconditionally, so silence means the
		// prefix diff found nothing to retract. Neither of those reaches this
		// branch. What does is a doc-mode lens whose empty behaviour has no
		// retraction transport — the exact shape that let twelve stale rows
		// render green for twelve days, reported as convergence because the
		// write loop simply never iterated.
		if p.zeroRowRetraction || p.multiEnvelopeFn != nil {
			fold.add(VerdictConverged, "")
		} else {
			fold.add(VerdictUnverified, "zero rows and no retraction transport for emptyBehavior")
		}
	}

	out.Verdict, out.BlockedClass, out.VerdictReason = fold.resolve("no result reached a comparison")
	// Converged is derived from the verdict rather than latched by any single
	// result, so the boolean and the verdict can never disagree on the wire. A
	// perEntry actor with one converged entry and one blocked entry is not a
	// converged actor, and a consumer reading only the boolean must not be told
	// that it is.
	out.Converged = out.Verdict == VerdictConverged
	return out, nil
}

// writeVerdict is the verdict a landed write earns. Without read-back the write
// is not evidence of anything: the adapter cannot distinguish a repaired
// divergence from an unconditional rewrite of an already-correct row.
func writeVerdict(canRead bool) (Verdict, string) {
	if canRead {
		return VerdictHealed, ""
	}
	return VerdictUnverified, "target cannot read rows back, so a write is not evidence of divergence"
}

// retractionVerdict is writeVerdict's delete-side twin: a retraction counts as a
// heal only when the row was READ as present first, since deleting an absent key
// succeeds just as quietly as removing a live one.
func retractionVerdict(evidenced bool) (Verdict, string) {
	if evidenced {
		return VerdictHealed, ""
	}
	return VerdictUnverified, "row was not read back before retraction, so the delete is not evidence of divergence"
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
	equal, _ := rowsComparable(stored, computed)
	return equal
}

// rowsComparable is rowsEquivalent with its one failure mode separated out:
// comparable reports whether both sides could be rendered at all, and equal is
// meaningful only when it is true.
//
// rowsEquivalent folds "these differ" and "I could not render one of them"
// (a value JSON cannot express — NaN, +Inf, a cycle) into a single false,
// which is the right answer for a REPAIR path: a row it cannot compare is a
// row it should rewrite. It is the wrong answer for the divergence audit,
// which writes nothing and must report an unrenderable row as unverified
// rather than as a divergence it has proven — the same rule §4.3 states for
// evaluation and read errors.
func rowsComparable(stored, computed map[string]any) (equal, comparable bool) {
	a, aerr := canonicalJSON(stored)
	b, berr := canonicalJSON(computed)
	if aerr != nil || berr != nil {
		return false, false
	}
	return bytes.Equal(a, b), true
}

// rowsComparableMasked is rowsComparable with additional columns excluded from
// the comparison before it runs — the divergence audit's one comparison site
// (Auditor.auditAnchor) calls this instead of rowsComparable for every plain
// lens it audits, Secure or not. rowsComparable itself is untouched and stays
// the sweep's own comparator (rowsEquivalent / classifyDivergence): a mask is
// never threaded there, because the sweep's reconciliation writer must never
// learn to treat a masked column as agreeing when it may not.
//
// ignore always carries the row's own KEY columns: a freshly computed row
// (ruleengine.EvalResult.Row / ruleengine.ProjectionResult.Values) carries
// every RETURN alias, key columns included, because the engine has no notion
// of "this alias is also the key" — while adapter.RowReader.GetRow's contract
// excludes them (a Postgres GetRow scopes its SELECT by key and never returns
// the key columns as content; postgres.go's buildGetRowSQL / getRowPlatformColumns
// doc says so). Comparing them would report a mismatch no recomputation could
// ever resolve, which is why every audited Postgres-adapter anchor must have
// its own key columns excluded here regardless of whether the lens is Secure.
//
// For a Secure Lens, ignore additionally carries its declared secure columns
// (SecureDecryptor.Columns): the audit's recompute never decrypts them, so
// their content is unverified — never assumed equal, never assumed diverged.
func rowsComparableMasked(stored, computed map[string]any, ignore []string) (equal, comparable bool) {
	a, aerr := canonicalJSON(stored, ignore...)
	b, berr := canonicalJSON(computed, ignore...)
	if aerr != nil || berr != nil {
		return false, false
	}
	return bytes.Equal(a, b), true
}

// keyColumnNames lists a row's key column names — the columns a comparison
// between a stored row and a freshly computed one always excludes, because the
// stored row was fetched BY them and a RowReader may omit them from its result.
func keyColumnNames(keys map[string]any) []string {
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	return names
}

// canonicalJSON renders a row without its volatile fields, plus any extra
// fields the caller asks to ignore. encoding/json emits map keys in sorted
// order, so the rendering is stable for a given content.
func canonicalJSON(row map[string]any, alsoIgnore ...string) ([]byte, error) {
	clean := make(map[string]any, len(row))
	maps.Copy(clean, row)
	for _, f := range volatileEnvelopeFields {
		delete(clean, f)
	}
	for _, f := range alsoIgnore {
		delete(clean, f)
	}
	return json.Marshal(clean)
}
