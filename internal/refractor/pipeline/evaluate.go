package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// ErrSkipProjection signals that an EnvelopeFn declined a row — the
// pipeline drops it without writing or erroring. Used by the Capability
// envelope to suppress projections the cypher produced over zero
// MATCH-bindings (no real actor).
var ErrSkipProjection = errors.New("pipeline: envelope: skip projection")

// ErrDeleteProjection signals that an EnvelopeFn declined to project a row
// AND that the row's target key must be deleted (not merely skipped). The
// pipeline synthesizes a Delete against the keys the envelope returned. Used
// by the ephemeral-grant envelope: a live actor whose grants have all
// expired/been removed produces no real grant, so its `cap.ephemeral.<actor>`
// key must be hard-deleted (absence = denial, Contract #6 §6.8). Unlike
// ErrSkipProjection (drop silently, leave any existing key untouched), this
// actively removes the target.
var ErrDeleteProjection = errors.New("pipeline: envelope: delete projection")

// ErrNoProvenanceTimestamp signals that an anchor vertex body carried no
// usable commit-provenance timestamp (neither lastModifiedAt nor createdAt),
// so a deterministic projectedAt cannot be derived. The pipeline surfaces
// this rather than substituting a wall-clock value.
var ErrNoProvenanceTimestamp = errors.New("pipeline: anchor vertex carries no commit-provenance timestamp")

// projectedAtFromProvenance derives the deterministic projectedAt value for a
// capability projection from the anchor vertex body's commit provenance. The
// universal Core KV envelope (Contract #1 §1.3) records the committing op's
// timestamp as lastModifiedAt (updated on every commit; equal to createdAt on
// creation). Using it makes projectedAt a pure function of the input state, so
// replay/rebuild over the same vertex yields an identical value — it is
// provenance ("as-of input state"), never a wall-clock read.
func projectedAtFromProvenance(nodeProps map[string]any) (string, error) {
	if nodeProps != nil {
		if v, ok := nodeProps["lastModifiedAt"].(string); ok && v != "" {
			return v, nil
		}
		if v, ok := nodeProps["createdAt"].(string); ok && v != "" {
			return v, nil
		}
	}
	return "", ErrNoProvenanceTimestamp
}

// evaluateForEntry runs the full-engine evaluate path against entry and
// returns the normalised []ruleengine.EvalResult shape the write loop expects.
// It binds `$actorKey`, `$now`, `$projectedAt` from the event/provenance and
// calls full.Engine.ExecuteWithFootprint. When an EnvelopeFn is installed, each row
// is rewritten before being handed to the adapter. When a SecureDecryptor is
// installed (a Secure Lens), each row's declared secure columns are decrypted
// before the results reach any write path — this wrapper is the single choke
// point the stream consumer (handle) flows through, so no plain-lens
// evaluation path can bypass it.
// The second return value is the enumerated-actor list (full vertex keys) —
// non-nil only for an actor-aware pipeline (personal-lens-retraction-
// design.md §3.2, R1: the frame-emission caller needs the complete
// enumerated set, not just the actors results happened to name, because an
// actor whose evaluation surfaced zero surviving rows must still get an
// empty retraction frame). Nil for a plain lens.
func (p *Pipeline) evaluateForEntry(ctx context.Context, rs ruleState, entry ruleengine.NodeEntry) ([]ruleengine.EvalResult, []string, error) {
	results, enumeratedActors, err := p.evaluateForEntryRaw(ctx, rs, entry)
	if err != nil {
		return nil, nil, err
	}
	if err := p.applySecureDecrypt(ctx, results); err != nil {
		return nil, nil, err
	}
	return results, enumeratedActors, nil
}

// applySecureDecrypt runs the installed SecureDecryptor over results; a no-op
// when none is installed. Every evaluation path that can reach a write must
// call this (evaluateForEntry covers the stream consumer; the actor fan-out
// handlers call it explicitly) — a validated Secure Lens is always a plain
// projection lens, so the fan-out coverage is defense in depth against a
// future wiring that combines an enumerator with a decryptor.
func (p *Pipeline) applySecureDecrypt(ctx context.Context, results []ruleengine.EvalResult) error {
	if p.secureDecryptor == nil {
		return nil
	}
	redactions, err := p.secureDecryptor.Apply(ctx, results)
	// The COUNTER measures nulls that reached the read model, so it is advanced
	// only when the result set survives to be written. All three callers discard
	// the results on error, and the message is then redelivered — so counting on
	// the error path would re-add the same redactions on every retry and inflate
	// the number an operator sizes the exposure from. The log is unconditional:
	// a refusal that happened is worth seeing whether or not its row landed.
	p.logSecureRedactions(redactions)
	if err != nil {
		return err
	}
	p.alarmSecureRedactions(ctx, redactions)
	return nil
}

// logSecureRedactions records each refusal individually, at the tier that says
// this is a privacy fault rather than a lawful erasure.
func (p *Pipeline) logSecureRedactions(redactions []SecureRedaction) {
	for _, r := range redactions {
		slog.Error("pipeline: secure column redacted to null; its value could not be resolved (privacy-critical, not a shred)",
			"ruleId", p.ruleID, "column", r.Column, "err", failure.PrivacyCritical(r.Reason))
	}
}

// alarmSecureRedactions raises the privacy-critical tier for every secure column
// an evaluation projected as null because it could not resolve it
// (retention-class-key-custody-design.md §6.2, fork F2).
//
// It deliberately does NOT pause the lens, which is where this tier differs from
// the one keyshredded raises. There, a failed nullification leaves a shredded
// identity's row standing, so halting the lens IS the containment. Here the
// containment already happened — the value is null in the row about to be
// written — and pausing would reinstate exactly the whole-lens stall F2 was
// ratified to remove: one unresolvable row blocking every other row of the lens
// from ever updating, a later erasure scrub included.
func (p *Pipeline) alarmSecureRedactions(ctx context.Context, redactions []SecureRedaction) {
	if len(redactions) == 0 || p.reporter == nil {
		return
	}
	if err := p.reporter.RecordSecureRedactions(ctx, uint64(len(redactions))); err != nil {
		slog.Error("pipeline: record secure-redaction health signal",
			"ruleId", p.ruleID, "err", err)
	}
}

// evaluateForEntryRaw is evaluateForEntry's core, pre-decrypt.
func (p *Pipeline) evaluateForEntryRaw(ctx context.Context, rs ruleState, entry ruleengine.NodeEntry) ([]ruleengine.EvalResult, []string, error) {
	if rs.engine == nil || rs.cr == nil {
		return nil, nil, fmt.Errorf("pipeline: full engine/compiled rule unset for rule %q", p.ruleID)
	}

	// Cross-vertex fan-out: on a non-actor event with an ActorEnumerator
	// installed, expand the event into the set of affected actors and
	// re-execute the cypher per actor so their capability set is
	// re-projected with the updated topology.
	//
	// An event on a vertex of the ACTOR type is an anchor's own event, and the
	// arms below own that anchor's disposition. But a pattern may bind the actor
	// type at a NON-anchor position too — `capabilityEphemeral`'s
	// (identity)<-[:reportsTo]-(report:identity) — and then this vertex sits
	// inside OTHER anchors' rows as well. Those peers are reprojected here.
	//
	// The two dispositions are computed apart and stay apart: the event's own
	// anchor may be RETRACTED (the tombstone arm), while a peer only ever goes
	// through reprojectActors. Nothing here deletes a row because the walk named
	// its anchor; a shape that did would convert a missed retraction into a mass
	// retraction, which is the worse failure.
	//
	// reprojectActors CAN still emit a Delete for a peer, by four paths, and each
	// is that peer's own genuine retraction rather than this event's: the peer's
	// vertex is gone (reprojectActors' missing-actor branch); its envelope
	// declines with ErrDeleteProjection (executeFullForActorOnce — and
	// `capabilityEphemeral`'s envelope really does decline, for a manager whose
	// every grant was delegated through the report that just vanished, which is
	// the correct answer rather than an artifact of this arm); a doc-mode
	// EmptyBehavior:delete lens evaluates to zero rows (zeroRowRetraction, whose
	// own presence check refuses to manufacture a tombstone for an anchor that
	// never held a row); or a perEntry lens's prefix diff drops a child the actor
	// no longer holds (multiEntryRetractions). All four are computed from the
	// peer's OWN freshly re-executed row set, and footprintValid re-reads that
	// read surface and retries on any revision move, so a torn read cannot
	// manufacture one either.
	var peers []string
	var peerResults []ruleengine.EvalResult
	if p.actorEnumerator != nil {
		eventType, _, _ := substrate.ParseVertexKey(entry.CoreKVKey)
		if eventType != p.actorEnumerator.actorType {
			return p.evaluateFanOut(ctx, rs, entry)
		}
		var err error
		if p.peerAnchorsEnabled() {
			if peers, err = p.peerAnchorsOf(ctx, rs, entry.CoreKVKey, eventType); err != nil {
				return nil, nil, err
			}
		}
		if len(peers) > 0 {
			if peerResults, err = p.reprojectActors(ctx, rs, peers); err != nil {
				return nil, nil, err
			}
		}
	}

	// Actor tombstone: the tombstoned actor's own projection is RETRACTED, in
	// one of three shapes. Any peer anchor rides alongside as peerResults — a
	// reprojection, never a retraction; only the vertex named by this event went
	// away.
	//
	// A personal (KeySetPublisher) target has no cap-shaped delete key that fits
	// its wire shape (natssubject.Delete requires __actor in Keys, which a bare
	// "key" map lacks) — publishing one is the identity-tombstone defect this
	// design closes structurally (§3.4): return no result of its own, just the
	// enumerated actor, so emitPersonalFrames's empty frame retracts every key
	// for this identity instead. A perEntry lens has no single cap-shaped parent
	// key to delete either — its live rows are the actor's per-anchor children —
	// so it reuses multiEntryRetractions with an empty fresh set: every currently
	// live child under the actor's prefix is tombstoned
	// (cap-read-per-anchor-grant-keys-design.md §4.2). A doc-mode capability
	// (or other actor-aware) target keeps the existing Delete-against-cap-key
	// shortcut.
	if entry.IsDeleted && p.actorEnumerator != nil {
		if _, isPersonal := p.currentAdapter().(adapter.KeySetPublisher); isPersonal {
			// The event actor leads the list with no rows of its own, so its
			// frame is empty and retracts; each peer's frame carries the keys
			// its reprojection just produced.
			return peerResults, append([]string{entry.CoreKVKey}, peers...), nil
		}
		if p.multiEnvelopeFn != nil {
			tombstones, rerr := p.multiEntryRetractions(ctx, entry.CoreKVKey, nil)
			if rerr != nil {
				return nil, nil, rerr
			}
			return append(tombstones, peerResults...), actorsTouchedWithPeers(entry.CoreKVKey, peers), nil
		}
		delKey := p.actorDeleteKeyFor(entry.CoreKVKey)
		return append([]ruleengine.EvalResult{{
			Delete: true,
			Keys:   map[string]any{"key": delKey},
			Row:    nil,
		}}, peerResults...), actorsTouchedWithPeers(entry.CoreKVKey, peers), nil
	}

	// Plain-projection anchor tombstone: retract the row the deleted anchor
	// projected. The non-actor twin of the actor-aware shortcut above. The
	// upsert-only re-scan path returns zero rows for a tombstoned anchor but
	// never a Delete, so the prior row would linger forever. A secondary-node
	// tombstone (event type != the anchor label) returns ok=false and falls
	// through to a normal re-execute so dependent rows refresh (e.g. a
	// deleted patient nulls an appointment's patientName without deleting
	// the appointment row).
	if entry.IsDeleted && p.actorEnumerator == nil {
		eventType, _, _ := substrate.ParseVertexKey(entry.CoreKVKey)
		if keys, ok := rs.engine.AnchorDeleteResult(
			rs.cr, entry.CoreKVKey, eventType, entry.Properties); ok {
			return []ruleengine.EvalResult{{Delete: true, Keys: keys, Row: nil}}, nil, nil
		}
	}

	// An event on the anchor's own type narrows the evaluation to that one
	// anchor (seedAnchorFor's contract); every other event — and every lens
	// that is not seed-eligible — recomputes the lens's whole row set. This
	// is the single seeding decision for all three plain arms:
	// the vertex arm's entry IS the event vertex, and the aspect/link arms both
	// reach here through evaluatePlainFromVertex with the owner/endpoint vertex
	// as the entry, so each arm seeds precisely when its own vertex is an
	// anchor.
	seed := p.seedAnchorFor(rs, entry.NodeLabel, entry.CoreKVKey)
	plain := p.actorEnumerator == nil && p.envelopeFn == nil && p.multiEnvelopeFn == nil
	// reentrant is true only while evaluatePlainDerivedAnchors is re-entering
	// this dispatch for one of ITS OWN derived anchors (anchor_derivation_
	// plain.go's "THE REENTRANCY SEAM"). Both derivation-eligible cases below
	// are excluded for it: a derived anchor is always of the anchor-labelled
	// type either case exists to answer, so evaluating it in HALF instead of
	// as the narrow single-seed case below would re-derive the identical set
	// and never terminate.
	reentrant := isPlainDerivedAnchorReentry(ctx)
	var results []ruleengine.EvalResult
	var err error
	switch {
	case seed == "" && plain && !reentrant:
		// A plain lens's neighbour event: seedAnchorFor found no seed because
		// entry's own type is not the lens's anchor pattern, so the shipped
		// answer is an unseeded whole-corpus re-scan — the same call the
		// default case below makes for every other (already-seeded, or
		// non-plain) event. evaluatePlainNeighbourEvent
		// (anchor_derivation_plain.go) is this branch's producer into the
		// derivation-mode switch: it returns exactly that re-scan except in
		// `act` mode on a lens the narrowing licence admits, where it
		// substitutes one seeded evaluation per derived anchor.
		results, err = p.evaluatePlainNeighbourEvent(ctx, rs, entry)
	case seed != "" && plain && !reentrant && p.seedMultiPosition(rs, entry.NodeLabel):
		// Increment 4b (§4.4): entry's own type IS the lens's anchor pattern,
		// but that same label ALSO binds a second pattern position — the
		// engine-level seed (seedAnchorFor's contract) only ever narrows the
		// ANCHOR position, so it silently misses every row where this vertex
		// sits at the OTHER position. seedMultiPosition proves the second
		// position exists; evaluateSeededMultiPosition's own derive() seeds
		// BOTH (the anchor position as a zero-hop terminus, the other by
		// walking from it). Its declined answer is the SAME narrow seeded
		// call the default case below makes — never the neighbour case's
		// whole-corpus rescan above — so `off` mode or an unlicensed lens
		// pays exactly today's cost, and only a licensed `act` lens gets the
		// correction.
		results, err = p.evaluateSeededMultiPosition(ctx, rs, entry)
	default:
		results, err = p.executeFullForActor(ctx, rs, entry.CoreKVKey, entry.Properties, seed)
	}
	if err != nil {
		return nil, nil, err
	}
	// Filter-retraction presence check (plain projection lenses): when a
	// live event anchor no longer appears in the re-derived row set — a
	// WHERE predicate flipped, a keyed aspect was deleted, a required
	// link was removed — its previously-projected row must be retracted,
	// which the upsert-only re-scan never does. The anchor's projection
	// key is derived read-free (AnchorProjectionKey succeeds only for a
	// one-row-per-anchor, anchor-keyed lens — see its ok contract), so a
	// multi-row or neighbor-keyed lens falls through to today's behaviour
	// and never risks a wrong Delete. A never-matched anchor emits an
	// idempotent Delete against an absent key — a harmless no-op, pinned
	// by test. The tombstoned-anchor shortcut above returns before this
	// check; a tombstone it could not derive keys for cannot derive them
	// here either (same derivation).
	if p.actorEnumerator == nil && p.envelopeFn == nil && p.multiEnvelopeFn == nil {
		if keys, ok := rs.engine.AnchorProjectionKey(
			rs.cr, entry.CoreKVKey, entry.NodeLabel, entry.Properties); ok &&
			!resultsContainKeys(results, keys) {
			results = append(results, ruleengine.EvalResult{Delete: true, Keys: keys})
		} else if !ok && p.diffRetraction {
			// Fire 3 (build-deferred in the design until a real consumer
			// arrived): AnchorProjectionKey could not derive a single
			// anchor-keyed row, so this lens's own opt-in target-diff picks
			// up what Fire 2 structurally cannot reach.
			var derr error
			results, derr = p.applyDiffRetraction(ctx, results)
			if derr != nil {
				return nil, nil, derr
			}
		}
	}
	// No frame when the walk reached no peer, which is the overwhelmingly common
	// case and the one the original cost argument was about: frame emission is
	// scoped to the reprojectActors code path (fan-out + Hydrate,
	// personal-lens-retraction-design.md's "For Andrew" summary), and with no
	// peer this branch is the actor's OWN vertex re-evaluating itself outside
	// that path (e.g. an identity property edit), which fires on every such
	// mutation and would make frames far chattier than the design costs for. A
	// later fan-out event or hydrate still converges any drift.
	//
	// Once a peer IS reached the branch has become a reprojectActors caller and
	// the frames are re-priced deliberately, not inherited. A peer's key set
	// really moved, and a personal client prunes only on a frame it receives
	// (emitPersonalFrames' doc) — withholding one is the retraction miss this
	// whole design exists to close, so peers must be framed. The event's own
	// actor rides along: dropping it would break actorsTouchedWithPeers' retry
	// contract below, and its marginal cost is one frame on an event that is
	// already publishing a frame per peer.
	return append(results, peerResults...), actorsTouchedWithPeers(entry.CoreKVKey, peers), nil
}

// actorsTouchedWithPeers names every actor an actor-type vertex event's write
// touched. With no peer reached it reports nil, which is the "the actor's own
// vertex re-evaluating itself" signal writeResults' transient-retry fallback
// reads (it substitutes the CDC key, which IS that actor). With peers it names
// them AND the event's own actor — dropping the latter would leave a failed
// write for the actor the event is about unqueued, since the fallback no longer
// fires once the list is non-empty.
func actorsTouchedWithPeers(self string, peers []string) []string {
	if len(peers) == 0 {
		return nil
	}
	return append([]string{self}, peers...)
}

// peerAnchorsOf returns the anchors OTHER than vertexKey whose rows an event on
// vertexKey — itself a vertex of the actor type — can move. It asks the same
// affected-anchor question the fan-out arms ask, through the same seam, so the
// pattern-directed derivation gets first refusal here too: for a lens binding
// the actor type only at the anchor it answers {vertexKey} with no adjacency
// read at all, and the caller does no extra work.
func (p *Pipeline) peerAnchorsOf(ctx context.Context, rs ruleState, vertexKey, vertexType string) ([]string, error) {
	anchors, err := p.affectedAnchors(ctx, rs, vertexKey,
		func() ([]string, bool, error) {
			return p.deriveAnchorsForVertex(ctx, rs, vertexKey, vertexType)
		},
		func(scoped bool) ([]string, error) {
			return p.enumerateAnchorsWalk(ctx, rs, vertexKey, vertexType, scoped)
		})
	if err != nil {
		return nil, fmt.Errorf("pipeline: peer-anchor enumerate from %q: %w", vertexKey, err)
	}
	peers := make([]string, 0, len(anchors))
	for _, a := range anchors {
		if a != vertexKey {
			peers = append(peers, a)
		}
	}
	return peers, nil
}

// maxFootprintRetries bounds footprint validation's inline re-execution to
// exactly one extra attempt (evaluation-consistency-design.md §4.3): drift is
// ms-scale, so a single re-execution against post-drift state converges in
// the common case, and sustained churn requeues rather than retrying inline
// without bound.
const maxFootprintRetries = 1

// executeFullForActor runs the full-engine cypher against a single actor key,
// wraps each row through envelopeFn/multiEnvelopeFn (when installed), and —
// for an auth-plane actor-aggregate lens — validates the evaluation's read
// surface against current KV state before trusting the result (see
// executeFullForActorAttempt). nodeProps is the actor vertex's stored Core KV
// body; it's used only to derive projectedAt (the engine itself always reads
// the graph fresh, memoized per evaluation).
//
// seedAnchor, when non-empty, narrows the evaluation's anchor pattern to that
// one vertex instead of scanning its whole type (refractor-footprint-
// reduction-design.md §D2). Only a caller that has proved the triggering event
// is a mutation of that anchor may pass it — pipeline.seedAnchorFor is that
// proof; "" is always the safe, whole-row-set evaluation.
func (p *Pipeline) executeFullForActor(ctx context.Context, rs ruleState, actorKey string, nodeProps map[string]any, seedAnchor string) ([]ruleengine.EvalResult, error) {
	return p.executeFullForActorCosted(ctx, rs, actorKey, nodeProps, seedAnchor, true)
}

// executeFullForAudit is executeFullForActor for the background divergence
// audit: the same evaluation, deliberately kept OUT of the cost observability
// the projection path feeds.
//
// The gauge it stays out of is peakBindingRows — a rolling MAX over the last
// DefaultPeakRowsBufferSize evaluations, published as the early warning that a
// lens is approaching the engine's binding-set cap (peakrows.go). The audit's
// samples are systematically the SMALLEST a lens ever produces (one seeded
// anchor, never the whole type) and, on a low-traffic lens, far more numerous
// than the projection's own — so recording them would refill the whole window
// with the cheapest evaluations the lens can make and drive the published peak
// DOWN, masking exactly the approach-to-cap the gauge exists to catch. A
// background observer must not move a gauge about production load, least of all
// in the reassuring direction.
//
// Latency is excluded for the same reason and already is by construction: a
// plain pipeline carries no latency ring (SetLatencyBuffer is called only on the
// actor-aggregate and operation-aggregate paths), so the wrapper below skips it
// explicitly rather than relying on that remaining true.
func (p *Pipeline) executeFullForAudit(ctx context.Context, rs ruleState, actorKey string, nodeProps map[string]any, seedAnchor string) ([]ruleengine.EvalResult, error) {
	return p.executeFullForActorCosted(ctx, rs, actorKey, nodeProps, seedAnchor, false)
}

func (p *Pipeline) executeFullForActorCosted(ctx context.Context, rs ruleState, actorKey string, nodeProps map[string]any, seedAnchor string, recordCost bool) ([]ruleengine.EvalResult, error) {
	start := time.Now()
	results, err := p.executeFullForActorAttempt(ctx, rs, actorKey, nodeProps, seedAnchor, 0, recordCost)
	if err != nil {
		return nil, err
	}
	// Record per-event projection latency for the heartbeat aggregator.
	// The buffer is cheap (single atomic-protected ring slot per insert)
	// so calling it on every fan-out actor is fine. Measured across every
	// attempt (including a drift retry), so a validated lens's latency
	// honestly reflects the doubled cost the design's perf gate measures.
	if p.latencyBuf != nil && recordCost {
		p.latencyBuf.Record(time.Since(start))
	}
	return results, nil
}

// executeFullForActorAttempt is executeFullForActor's core, parameterized by
// attempt (0 = first execution, up to maxFootprintRetries = the allowed
// inline retries) so the footprint-validation recursion can never run deeper
// than the design's stated bound.
//
// Scope (needsFootprintValidation): only an actor-aggregate lens
// (envelopeFn/multiEnvelopeFn installed) projecting an auth-plane surface
// (p.authPlane) pays for validation — every other lens returns its first
// execution's results unchanged, zero extra reads.
//
// In scope, the read surface ExecuteWith reports (every vertex/aspect key and
// adjacency node it read, with the revision observed) is re-read fresh
// immediately after evaluation. A moved revision means the row could blend
// two different instants — never a real one — so it is never trusted as-is.
// One immediate re-execution against the now-current state is attempted;
// if the retry's OWN footprint still diverges (sustained churn), this
// returns failure.ErrEvalDrift instead of the possibly-torn result — never a
// silently empty set, which four downstream consumers would misread as "zero
// rows" (design §4.3). The caller's existing transient-failure handling
// (dispositionEvalErr's retry-enqueue, the sweep's repair-failure accounting)
// takes it from there.
func (p *Pipeline) executeFullForActorAttempt(ctx context.Context, rs ruleState, actorKey string, nodeProps map[string]any, seedAnchor string, attempt int, recordCost bool) ([]ruleengine.EvalResult, error) {
	results, footprint, err := p.executeFullForActorOnce(ctx, rs, actorKey, nodeProps, seedAnchor, recordCost)
	if err != nil {
		return nil, err
	}
	if !p.needsFootprintValidation() {
		return results, nil
	}
	valid, verr := p.footprintValid(ctx, footprint)
	if verr != nil {
		return nil, fmt.Errorf("pipeline: footprint validation for %q: %w", actorKey, verr)
	}
	if valid {
		return results, nil
	}
	if attempt >= maxFootprintRetries {
		if p.reporter != nil {
			if rerr := p.reporter.RecordEvalDriftRequeue(ctx); rerr != nil {
				slog.Error("pipeline: record eval-drift requeue", "ruleId", p.ruleID, "actorKey", actorKey, "err", rerr)
			}
		}
		return nil, fmt.Errorf("pipeline: %q: %w", actorKey, failure.ErrEvalDrift)
	}
	if p.reporter != nil {
		if rerr := p.reporter.RecordEvalDriftRetry(ctx); rerr != nil {
			slog.Error("pipeline: record eval-drift retry", "ruleId", p.ruleID, "actorKey", actorKey, "err", rerr)
		}
	}
	// Re-fetch the actor's own vertex body for the retry: nodeProps only
	// drives projectedAt derivation (the engine re-reads the graph itself
	// regardless), but the drift that triggered this retry could be exactly
	// a commit to the actor's own vertex, and a retry computed against a
	// provenance timestamp older than the state it re-reads would be
	// internally inconsistent.
	freshProps, ferr := p.fetchVertexProps(ctx, actorKey)
	if ferr != nil {
		return nil, fmt.Errorf("pipeline: re-fetch %q after drift: %w", actorKey, ferr)
	}
	return p.executeFullForActorAttempt(ctx, rs, actorKey, freshProps, seedAnchor, attempt+1, recordCost)
}

// needsFootprintValidation reports whether this pipeline's evaluations must
// pass footprint validation before their results are trusted — the scope
// predicate (evaluation-consistency-design.md §13.3): actorAggregate (an
// envelope wrapper installed) AND auth-plane (projects an authorization
// surface, projection.IsAuthPlane / p.authPlane) AND
// requiresFootprintValidation (the lens's compiled cypher emits a
// multi-binding conjunct unit, projection.hasMultiBindingConjunctUnit /
// p.requiresFootprintValidation). A lens matching the first two conjuncts
// but whose every tuple is single-binding (e.g. capabilityRoles) is exempt
// by construction — validating it buys nothing and costs everything under
// write pressure on a shared hub node (§13.1's defect 1). Every other lens
// pays zero validation cost.
func (p *Pipeline) needsFootprintValidation() bool {
	return (p.envelopeFn != nil || p.multiEnvelopeFn != nil) && p.authPlane && p.requiresFootprintValidation
}

// footprintValid re-reads every entry in fp against CURRENT KV state and
// reports whether every revision still matches what the evaluation observed
// — including a present-then-absent or absent-then-present flip (design
// §4.1: absence is itself part of the footprint, closing the NOT-predicate
// conjunct: 0 is a recorded revision, not a missing map entry). There is no
// header-only read in substrate (design §5), so each entry costs a full
// re-read, paid honestly rather than approximated.
//
// The adjacency half re-reads each node at the scope the footprint names: a
// node the evaluation read whole is re-read whole and compared by
// fingerprint, a node it read only through typed relations is re-read at
// exactly those relations and compared by matched edge identities. A node
// carrying both records is compared both ways.
func (p *Pipeline) footprintValid(ctx context.Context, fp ruleengine.EvalFootprint) (bool, error) {
	// A torn footprint is rejected without reading anything. The evaluation
	// itself observed two values for one key — a multi-walk lens's branches
	// disagreeing across their separate memos (mergeFootprints) — so the row
	// already blends two instants and the merged maps hold only one value per
	// key. Any re-read would compare current state against a value that was
	// never the whole truth, and would pass whenever the graph has since gone
	// quiet: reading is not just useless here, it is the fail-OPEN direction.
	// Rejecting costs nothing and lands in the ordinary drift path — one
	// immediate re-execution, and failure.ErrEvalDrift on sustained churn.
	if fp.Torn {
		return false, nil
	}
	for key, wantRev := range fp.NodeRevisions {
		gotRev, err := p.currentNodeRevision(ctx, key)
		if err != nil {
			return false, err
		}
		if gotRev != wantRev {
			return false, nil
		}
	}
	// The two adjacency maps do not share a key set: a node read WHOLE has an
	// EdgeRevisions fingerprint, a node read only at a relation scope (an
	// overflow-marked hub every hop crossed by a typed relation) has none and
	// is carried by its EdgeSelectors record alone. Validation therefore walks
	// their union — order is immaterial, since any single node reporting drift
	// invalidates the whole footprint.
	for nodeID := range edgeFootprintNodes(fp) {
		wantRev, hasWhole := fp.EdgeRevisions[nodeID]
		sel, hasSelectors := fp.EdgeSelectors[nodeID]

		if !hasSelectors || sel.Fallback {
			// The coarse path: an untyped hop consumed every edge on this
			// node regardless of type (Fallback), or no selector was recorded
			// at all (defensive only — fetchEdges' one caller, traverseRel,
			// always records). Either way the sound comparison is the node's
			// whole-edge-set fingerprint. An untyped hop always reads the node
			// whole, so a fingerprint MUST be here; a node on this path
			// without one is a malformed footprint with nothing to compare,
			// and reports drift rather than validating unchecked.
			if !hasWhole {
				return false, nil
			}
			edges, gotRev, err := adjacency.Neighbors(ctx, p.adjKV, p.coreKV, nodeID)
			if err != nil {
				return false, err
			}
			if gotRev != wantRev {
				return false, nil
			}
			// A Fallback node may also carry Matched sets, and they are not
			// redundant with the fingerprint: recordEdgeSelector stops
			// recording once Fallback is set, so the sets present are exactly
			// the TYPED hops that preceded the untyped one on this node. Each
			// observed its relation at an earlier instant than the whole read
			// the fingerprint pins, and only re-deriving the set can catch a
			// write that landed between those two instants.
			if hasSelectors && !edgeSelectorsUnchanged(sel, edges) {
				return false, nil
			}
			continue
		}

		// §13.4, the selector path: every hop on this node was typed, so it
		// is validated by re-applying each recorded selector to a fresh read
		// and comparing the matched edge-identity set. A sibling write to an
		// UNRELATED relation on a shared hub node (a role, an op-meta, a
		// location) does not read as drift — which is why the node's whole
		// fingerprint is deliberately NOT compared here even when one was
		// recorded.
		//
		// The re-read takes the same scope the footprint names: one
		// NeighborsByRelation over every relation the node's selectors name,
		// so a marked hub is enumerated at those relations instead of drained
		// whole, and an unmarked node is the one document read it always was.
		rels := make(map[string]struct{}, len(sel.Matched))
		for selector := range sel.Matched {
			rels[selector.RelType] = struct{}{}
		}
		// A selector entry naming no selector at all is malformed the same way
		// a coarse node with no fingerprint is, and fails closed for the same
		// reason: there is nothing to compare, the whole fingerprint is
		// deliberately not compared on this path, and a scoped re-read of an
		// empty relation set reads nothing — so validating here would confirm
		// a node this pass never looked at.
		//
		// No engine-built footprint carries the shape. Both producers of a
		// selector entry populate it — recordEdgeSelector because a typed hop
		// always records its selector, recordComposedPins because it only runs
		// for a non-empty set of substituted relations — and mergeFootprints
		// copies whatever its branches held, so it can only pass the shape
		// through, never mint it. This is defence in depth, and the direction
		// it fails in is the one that costs a re-execution rather than the one
		// that confirms an unchecked row.
		if len(rels) == 0 {
			return false, nil
		}
		edges, _, err := adjacency.NeighborsByRelation(ctx, p.adjKV, p.coreKV, nodeID, rels)
		if err != nil {
			return false, err
		}
		if !edgeSelectorsUnchanged(sel, edges) {
			return false, nil
		}
	}
	return true, nil
}

// edgeFootprintNodes returns every adjacency NodeID fp carries in either
// adjacency map — the set footprintValid validates, since a node may appear in
// EdgeRevisions (read whole), in EdgeSelectors (read at a relation scope), or
// in both.
func edgeFootprintNodes(fp ruleengine.EvalFootprint) map[string]struct{} {
	nodes := make(map[string]struct{}, len(fp.EdgeRevisions)+len(fp.EdgeSelectors))
	for nodeID := range fp.EdgeRevisions {
		nodes[nodeID] = struct{}{}
	}
	for nodeID := range fp.EdgeSelectors {
		nodes[nodeID] = struct{}{}
	}
	return nodes
}

// edgeSelectorsUnchanged re-applies every selector sel recorded to edges and
// reports whether each one still matches exactly the edge identities the
// footprint recorded for it. edges must cover at least the relations sel names;
// a selector naming a relation edges does not cover would re-derive as empty
// and read as drift, which is the safe direction but not the intended
// comparison, so callers read at a scope wide enough for sel.
func edgeSelectorsUnchanged(sel ruleengine.EdgeSelectorFootprint, edges []adjacency.EdgeEntry) bool {
	for selector, wantIDs := range sel.Matched {
		gotIDs := make(map[string]struct{}, len(wantIDs))
		for _, e := range edges {
			if e.Name != selector.RelType {
				continue
			}
			if !adjacency.DirectionMatches(e.Direction, selector.Direction) {
				continue
			}
			gotIDs[e.EdgeID] = struct{}{}
		}
		if !edgeIDSetsEqual(gotIDs, wantIDs) {
			return false
		}
	}
	return true
}

// edgeIDSetsEqual compares two edge-identity sets for equality — same size
// AND same members. A swap of one edge for a different same-count edge
// (e.g. a revoke-and-grant landing between footprint capture and
// validation) must still count as drift, so size equality alone is not
// sufficient.
func edgeIDSetsEqual(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}

// currentNodeRevision returns key's current Core KV revision, or 0 when the
// key is absent by the same test the executor's readNode uses: a genuinely
// missing key, a null JSON body, or a soft-deleted (isDeleted: true) vertex
// all read as absent there, so a footprinted key that was absent for THAT
// reason and still is must compare equal (0 == 0), not spuriously drift
// because its KV entry technically still exists.
func (p *Pipeline) currentNodeRevision(ctx context.Context, key string) (uint64, error) {
	entry, err := p.coreKV.Get(ctx, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("pipeline: footprint re-read %q: %w", key, err)
	}
	var props map[string]any
	if uerr := json.Unmarshal(entry.Value, &props); uerr != nil {
		return 0, fmt.Errorf("pipeline: footprint re-read %q: unmarshal: %w", key, uerr)
	}
	if props == nil {
		return 0, nil
	}
	if deleted, _ := props["isDeleted"].(bool); deleted {
		return 0, nil
	}
	return entry.Revision, nil
}

// executeFullForActorOnce runs ONE evaluation of the full-engine cypher
// against actorKey and wraps each row through envelopeFn/multiEnvelopeFn
// (when installed), returning the evaluation's read-surface footprint
// alongside the results so a caller can validate it. Factored out of
// executeFullForActor so the footprint-validation retry
// (executeFullForActorAttempt) can re-run exactly this step against fresh
// state without duplicating the envelope/collision-guard/retraction logic.
func (p *Pipeline) executeFullForActorOnce(ctx context.Context, rs ruleState, actorKey string, nodeProps map[string]any, seedAnchor string, recordCost bool) ([]ruleengine.EvalResult, ruleengine.EvalFootprint, error) {
	now := time.Now().UTC()
	projectedAt, perr := projectedAtFromProvenance(nodeProps)
	if perr != nil {
		return nil, ruleengine.EvalFootprint{}, fmt.Errorf("pipeline: projectedAt for %q: %w", actorKey, perr)
	}
	params := map[string]any{
		"actorKey":    actorKey,
		"now":         now.Format(time.RFC3339),
		"projectedAt": projectedAt,
	}
	out, footprint, stats, err := p.executeBranches(ctx, rs, actorKey, nodeProps, params, seedAnchor)
	// The evaluation's peak binding rows lands before the error is inspected.
	// A cap refusal is the case the gauge exists for, and this is the one
	// attempt boundary that sees every evaluation exactly once — a
	// footprint-drift re-execution re-enters here and contributes its own
	// sample, as does each retry-queue redelivery.
	//
	// recordCost is false for the background divergence audit alone: its
	// samples describe a one-anchor observation, not the lens's production
	// load, and folding them in would drive the gauge DOWN (executeFullForAudit).
	if recordCost {
		p.recordPeakBindingRows(stats)
	}
	// Per-evaluation detail for an operator who has turned Debug on; the health
	// entry's rolling gauge is the always-on surface, and a cap refusal logs its
	// own Warn from the engine. The Enabled check is what keeps this off the hot
	// path: a variadic slog call builds its argument slice at the call site,
	// before the handler ever gets to drop it by level, and this runs once per
	// event per actor.
	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		slog.Debug("pipeline: evaluation cost",
			"ruleId", p.ruleID, "actorKey", actorKey, "peakBindingRows", stats.PeakBindingRows)
	}
	if err != nil {
		return nil, ruleengine.EvalFootprint{}, err
	}
	results := make([]ruleengine.EvalResult, 0, len(out))
	for _, r := range out {
		row := r.Values
		keys := r.Key
		if p.multiEnvelopeFn != nil {
			entries, envErr := p.multiEnvelopeFn(row, keys, params)
			if errors.Is(envErr, ErrSkipProjection) {
				continue
			}
			if envErr != nil {
				return nil, ruleengine.EvalFootprint{}, fmt.Errorf("pipeline: multi-envelope: %w", envErr)
			}
			for _, e := range entries {
				results = append(results, ruleengine.EvalResult{
					Keys: e.Keys,
					Row:  e.Row,
				})
			}
			continue
		}
		if p.envelopeFn != nil {
			newRow, newKeys, envErr := p.envelopeFn(row, keys, params)
			if errors.Is(envErr, ErrSkipProjection) {
				continue
			}
			if errors.Is(envErr, ErrDeleteProjection) {
				results = append(results, ruleengine.EvalResult{
					Delete: true,
					Keys:   newKeys,
					Row:    nil,
				})
				continue
			}
			if envErr != nil {
				return nil, ruleengine.EvalFootprint{}, fmt.Errorf("pipeline: envelope: %w", envErr)
			}
			row = newRow
			keys = newKeys
		}
		results = append(results, ruleengine.EvalResult{
			Delete: r.Delete,
			Keys:   keys,
			Row:    row,
		})
	}
	// Zero-row retraction (doc-mode EmptyBehavior delete/softDelete): a
	// filtering WHERE on the anchor match itself — as opposed to an OPTIONAL
	// MATCH secondary pattern — makes the cypher above return no row at all
	// once the anchor stops matching, so `out` (and therefore `results`) is
	// empty and the per-row envelope callback above never runs to decline
	// anything. p.zeroRowRetraction is armed only for a doc-mode descriptor
	// whose empty behavior actually tombstones
	// (projection.InstallActorAggregate), so this is inert for every other
	// lens shape — a perEntry lens retracts through multiEntryRetractions
	// below instead, and a skip/emptyDoc doc-mode lens must never have its
	// existing key touched by this. zeroRowDeleteKey's own presence check is
	// what keeps this from manufacturing a tombstone for an anchor that never
	// held a row (e.g. activation replay walking every anchor subject).
	if p.zeroRowRetraction && len(results) == 0 {
		if key, ok := p.zeroRowDeleteKey(ctx, actorKey); ok {
			results = append(results, ruleengine.EvalResult{
				Delete: true,
				Keys:   map[string]any{"key": key},
				Row:    nil,
			})
		}
	}
	// An actor-aggregate lens (envelope installed) derives its output key from the
	// anchor, not the row, so every non-delete row for one actor carries the same
	// key. If the cypher returns 2+ such rows, the write loop would overwrite them
	// in turn (last-writer-wins) and silently drop the rest — an FR29 violation.
	// The aggregation belongs in the cypher (collect → one row per anchor); when it
	// is missing, surface the authoring defect and fail the actor's projection
	// closed rather than write a half-result. A perEntry lens's own within-row
	// dedup (EntryEnvelopeFn's byID map) only closes this for entries collected
	// inside ONE row — a cypher missing aggregation still returns 2+ rows per
	// actor, and the same anchor could be keyed by each, so the guard applies to
	// multiEnvelopeFn's results too, not only envelopeFn's.
	if p.envelopeFn != nil || p.multiEnvelopeFn != nil {
		if err := p.guardOutputKeyCollision(ctx, actorKey, results); err != nil {
			return nil, ruleengine.EvalFootprint{}, err
		}
	}
	// A perEntry lens's fresh entry set is only ever additive on its own
	// (EntryEnvelopeFn never sees the actor's previously-projected children,
	// only this evaluation's rows) — an anchor the actor no longer holds
	// would otherwise linger as a stale key forever (an over-grant on the
	// security plane, cap-read-per-anchor-grant-keys-design.md §4.2). The
	// prefix diff below is the retraction transport that closes it.
	if p.multiEnvelopeFn != nil {
		withRetractions, rerr := p.multiEntryRetractions(ctx, actorKey, results)
		if rerr != nil {
			return nil, ruleengine.EvalFootprint{}, rerr
		}
		results = withRetractions
	}
	return results, footprint, nil
}

// multiEntryRetractions computes cap-read-per-anchor-grant-keys-design.md
// §4.2's per-actor prefix diff: it lists actorKey's existing child keys under
// its perEntry lens prefix and tombstones every one the fresh set no longer
// carries, returned ahead of fresh so writeResults' sequential dispatch lands
// every tombstone before any upsert (deny-closed ordering — a pass that dies
// mid-way only ever under-grants, never over-grants). A listed candidate
// already carrying isDeleted is skipped without a rewrite: its stored
// projectionSeq watermark already outranks any grant-era replay (the §6.2
// guard), so re-stamping it would be pure churn, not a correctness
// requirement. The prefix is actorKey's own parent doc key (the same
// derivation actorDeleteKeyFor uses for a whole-actor tombstone) — never a
// bucket-wide listing, so a sibling lens sharing the target never sees its
// keys named here.
//
// It also carries the §6 migration transport: an actor's first evaluation
// under a perEntry lens may still find a live pre-flip legacy parent document
// at the exact parent key (no child suffix) — that document is guard-
// tombstoned in the same batch, tombstones-first, so the dual-read window
// (capabilityread.IsReadable) never serves it past this evaluation. This is
// unconditional (not gated on the child listing being non-empty): the very
// first post-flip evaluation of an actor has no children yet either.
//
// Every tombstone this function returns carries ruleengine.EvalResult's
// FailClosed — writeResults aborts the whole batch (full redelivery) if a
// tombstone write fails, rather than continuing to write the fresh entries
// that follow it in the same list. Without that, a transient failure on
// exactly the tombstone write (while sibling fresh upserts still succeed)
// would leave a revoked anchor's stale grant (legacy parent doc or a dropped
// child key) live alongside the correctly-updated rest of the set — the
// exact fail-OPEN shape this design exists to close, reopened one batch at a
// time.
func (p *Pipeline) multiEntryRetractions(ctx context.Context, actorKey string, fresh []ruleengine.EvalResult) ([]ruleengine.EvalResult, error) {
	adpt := p.currentAdapter()
	lister, ok := adpt.(adapter.PrefixKeyLister)
	if !ok {
		return nil, fmt.Errorf("pipeline: multi-entry retraction: adapter %T cannot enumerate keys by prefix — a perEntry lens cannot retract a dropped anchor", adpt)
	}
	reader, ok := adpt.(adapter.RowReader)
	if !ok {
		return nil, fmt.Errorf("pipeline: multi-entry retraction: adapter %T cannot read back a row — tombstone-skip cannot be decided", adpt)
	}

	parentKey := p.actorDeleteKeyFor(actorKey)
	var tombstones []ruleengine.EvalResult
	if _, live, err := reader.GetRow(ctx, map[string]any{"key": parentKey}); err != nil {
		return nil, fmt.Errorf("pipeline: multi-entry retraction: get legacy parent %q: %w", parentKey, err)
	} else if live {
		tombstones = append(tombstones, ruleengine.EvalResult{Delete: true, Keys: map[string]any{"key": parentKey}, FailClosed: true})
	}

	childPrefix := parentKey + "."
	existing, err := lister.ListKeysPrefix(ctx, childPrefix)
	if err != nil {
		return nil, fmt.Errorf("pipeline: multi-entry retraction: list %q: %w", childPrefix, err)
	}
	if len(existing) == 0 {
		if len(tombstones) == 0 {
			return fresh, nil
		}
		return append(tombstones, fresh...), nil
	}

	freshKeys := make(map[string]struct{}, len(fresh))
	for _, f := range fresh {
		k, ok := f.Keys["key"].(string)
		if !ok {
			// A fresh entry this pass just built carrying no usable "key"
			// would otherwise silently vanish from freshKeys — its still-live
			// existing key then reads as dropped and gets tombstoned below,
			// while this malformed entry's own upsert (same key, same
			// incoming seq) is rejected by the adapter's §6.2 guard as a
			// same-seq no-op. Net effect: a still-granted anchor is durably
			// revoked with no error. Fail the whole evaluation instead,
			// mirroring EntryEnvelopeFn's fail-closed posture for a
			// malformed key field (never silently drop a grant).
			return nil, fmt.Errorf("pipeline: multi-entry retraction: fresh result carries no usable \"key\" field (%v)", f.Keys)
		}
		freshKeys[k] = struct{}{}
	}

	for _, keys := range existing {
		k, ok := keys["key"].(string)
		if !ok {
			continue
		}
		if _, stillFresh := freshKeys[k]; stillFresh {
			continue
		}
		_, live, err := reader.GetRow(ctx, keys)
		if err != nil {
			return nil, fmt.Errorf("pipeline: multi-entry retraction: get %q: %w", k, err)
		}
		if !live {
			continue
		}
		tombstones = append(tombstones, ruleengine.EvalResult{Delete: true, Keys: keys, FailClosed: true})
	}
	if len(tombstones) == 0 {
		return fresh, nil
	}
	return append(tombstones, fresh...), nil
}

// guardOutputKeyCollision enforces the one-row-per-anchor invariant of an
// actor-aggregate projection. When 2+ non-delete results for a single actor map
// to the same anchor-derived output key, writing them in turn would overwrite
// last-writer-wins and silently drop the earlier rows (FR29 — Refractor must
// never silently drop). It records the defect on the Health-KV surface
// (errorCount + lastError, the same surface a terminal write failure uses) and
// logs a WARN, then returns a Terminal-classified error so the actor's
// projection fails closed: the colliding rows are never written, and the
// disposition path routes the event to the DLQ + Health rather than wedging the
// rule. The correct authoring fix is to aggregate in the cypher
// (collect(DISTINCT …) → one row per anchor); this guard catches the case where
// that aggregation is missing. A delete result paired with a write, or rows for
// different actors, are not collisions and pass through untouched.
func (p *Pipeline) guardOutputKeyCollision(ctx context.Context, actorKey string, results []ruleengine.EvalResult) error {
	collidingKey, count, found := detectOutputKeyCollision(results)
	if !found {
		return nil
	}
	msg := fmt.Sprintf(
		"actor-aggregate projection produced %d non-delete rows for actor %q sharing output key %q; "+
			"the cypher must aggregate to one row per anchor (collect)",
		count, actorKey, collidingKey)
	slog.Warn("pipeline: actor-aggregate output-key collision — defect signal",
		"ruleId", p.ruleID, "actorKey", actorKey,
		"outputKey", collidingKey, "rowCount", count)
	if p.reporter != nil {
		if recErr := p.reporter.RecordError(ctx, msg); recErr != nil {
			slog.Error("pipeline: record output-key collision on health KV",
				"ruleId", p.ruleID, "err", recErr)
		}
	}
	return failure.Terminal(fmt.Errorf("pipeline: %s", msg))
}

// detectOutputKeyCollision reports the first output key carried by 2+ non-delete
// results in a single actor's result set, along with the total number of results
// that share it. Delete results are excluded: a delete + a write for the same key
// is the normal retract-then-write shape, not a collision. found is false when
// every non-delete result has a distinct output key (the overwhelmingly common
// one-row-per-anchor path).
func detectOutputKeyCollision(results []ruleengine.EvalResult) (collidingKey string, count int, found bool) {
	counts := make(map[string]int, len(results))
	var firstRepeated string
	for i := range results {
		if results[i].Delete {
			continue
		}
		key, _ := results[i].Keys["key"].(string)
		if key == "" {
			continue
		}
		counts[key]++
		if counts[key] == 2 && firstRepeated == "" {
			firstRepeated = key
		}
	}
	if firstRepeated == "" {
		return "", 0, false
	}
	return firstRepeated, counts[firstRepeated], true
}

// evaluateFanOut handles the cross-vertex fan-out path: the CDC event arrived
// on a non-actor vertex; enumerate affected actors and re-execute the cypher
// per actor. Each actor's result set is appended to the returned []EvalResult
// — the pipeline write loop handles each result row independently.
func (p *Pipeline) evaluateFanOut(ctx context.Context, rs ruleState, entry ruleengine.NodeEntry) ([]ruleengine.EvalResult, []string, error) {
	eventType, _, _ := substrate.ParseVertexKey(entry.CoreKVKey)
	actorKeys, err := p.affectedAnchors(ctx, rs, entry.CoreKVKey,
		func() ([]string, bool, error) {
			return p.deriveAnchorsForVertex(ctx, rs, entry.CoreKVKey, eventType)
		},
		func(scoped bool) ([]string, error) {
			return p.enumerateAnchorsWalk(ctx, rs, entry.CoreKVKey, eventType, scoped)
		})
	if err != nil {
		return nil, nil, fmt.Errorf("pipeline: fan-out enumerate: %w", err)
	}
	// No affected actors → no projection to write. This is a valid
	// outcome (e.g. a role with no assignments yet, or a service in a
	// location no actor sits inside).
	if len(actorKeys) == 0 {
		return nil, nil, nil
	}
	results, err := p.reprojectActors(ctx, rs, actorKeys)
	return results, actorKeys, err
}

// evaluateLinkFanOut handles a link CDC event (create or tombstone) on the
// actor-aware pipeline. A pure link mutation (e.g. holdsRole, grantedBy)
// carries no vertex change, so the only way affected actors are reprojected
// is to seed the fan-out from BOTH link endpoints.
//
// Adjacency consistency: the dedicated adjacency consumer
// (internal/refractor/consumer/bootstrap.go) and this pipeline both react to
// the same link event with no cross-consumer ordering guarantee. Before
// enumerating, we idempotently apply the link to adjKV ourselves (mirroring
// processLinkEnvelope) so the reprojection cypher sees a consistent edge set
// regardless of which consumer reached the link first. adjacency.Build upserts
// (create) / removes (tombstone) by EdgeID, so the dedicated consumer's later
// Build for the same edge is a no-op. This guarantees the reprojection never
// races ahead of the edge that triggered it.
func (p *Pipeline) evaluateLinkFanOut(ctx context.Context, rs ruleState, linkKey string, isDeleted bool) ([]ruleengine.EvalResult, []string, error) {
	srcType, srcID, linkName, dstType, dstID, ok := substrate.ParseLinkKey(linkKey)
	if !ok {
		// ClassifyKey already gated KindLink; unreachable in practice.
		return nil, nil, fmt.Errorf("pipeline: link fan-out: not a Contract #1 link key: %q", linkKey)
	}

	// Idempotently reflect this link in adjKV before enumerating. The link key
	// is its own EdgeID (Contract #1 link keys are globally unique), so a
	// create upserts and a tombstone removes by that EdgeID — matching the
	// dedicated consumer's directional events exactly.
	for _, evt := range adjacency.EventsForLink(linkKey, srcType, srcID, linkName, dstType, dstID, isDeleted) {
		if err := adjacency.Build(ctx, p.adjKV, evt); err != nil {
			return nil, nil, fmt.Errorf("pipeline: link fan-out: adjacency build for %q: %w", linkKey, err)
		}
	}

	srcVtx := substrate.VertexKey(srcType, srcID)
	dstVtx := substrate.VertexKey(dstType, dstID)

	// Chosen before the empty-set return, not after: "this event reached nobody"
	// is a real answer either arm can give, and skipping the choice on the quiet
	// events would both bias the tally and leave the derivation's own empty
	// answer unreachable — which for a link binding no hop is the common case.
	actorKeys, err := p.affectedAnchors(ctx, rs, linkKey,
		func() ([]string, bool, error) {
			return p.deriveAnchorsForLink(ctx, rs, linkKey)
		},
		func(scoped bool) ([]string, error) {
			// Seed the actor enumeration from BOTH endpoint vertices and union
			// the results. Either endpoint may be (or reach) an actor.
			actorSet := map[string]struct{}{}
			for _, ep := range []struct{ key, typ string }{{srcVtx, srcType}, {dstVtx, dstType}} {
				actors, err := p.enumerateAnchorsWalk(ctx, rs, ep.key, ep.typ, scoped)
				if err != nil {
					return nil, fmt.Errorf("pipeline: link fan-out enumerate from %q: %w", ep.key, err)
				}
				for _, a := range actors {
					actorSet[a] = struct{}{}
				}
			}
			out := make([]string, 0, len(actorSet))
			for a := range actorSet {
				out = append(out, a)
			}
			return out, nil
		})
	if err != nil {
		return nil, nil, err
	}
	if len(actorKeys) == 0 {
		// A link whose endpoints reach no actors (e.g. a book→author link)
		// is a correct no-op.
		return nil, nil, nil
	}
	results, err := p.reprojectActors(ctx, rs, actorKeys)
	return results, actorKeys, err
}

// evaluateAspectFanOut handles an aspect CDC event (mutation or tombstone) on
// the actor-aware pipeline. An aspect-only mutation (e.g. identity .state,
// role .description) carries no vertex-root change, so affected actors are
// reprojected by seeding the fan-out from the aspect's parent vertex.
//
// When the parent is a non-actor vertex (e.g. a role .description), the
// enumerator walks adjacency to the actors that reach it. When the parent is
// itself an actor (e.g. an identity .state flip), that actor is reprojected —
// alone only where the compiled pattern proves no other anchor binds it
// (enumerateAnchors), and otherwise alongside every anchor the walk from it
// reaches, since a pattern binding the actor type at a non-anchor position
// makes one identity's aspect part of another identity's row.
//
// Adjacency is untouched — an aspect change never alters graph topology — so,
// unlike the link fan-out, no adjacency.Build is performed here.
func (p *Pipeline) evaluateAspectFanOut(ctx context.Context, rs ruleState, aspectKey string) ([]ruleengine.EvalResult, []string, error) {
	parentVtx, parentType, _, _, ok := substrate.ParseAspectKey(aspectKey)
	if !ok {
		// ClassifyKey already gated KindAspect; unreachable in practice.
		return nil, nil, fmt.Errorf("pipeline: aspect fan-out: not a Contract #1 aspect key: %q", aspectKey)
	}

	actorKeys, err := p.affectedAnchors(ctx, rs, aspectKey,
		func() ([]string, bool, error) {
			return p.deriveAnchorsForAspect(ctx, rs, aspectKey)
		},
		func(scoped bool) ([]string, error) {
			return p.enumerateAnchorsWalk(ctx, rs, parentVtx, parentType, scoped)
		})
	if err != nil {
		return nil, nil, fmt.Errorf("pipeline: aspect fan-out enumerate from %q: %w", parentVtx, err)
	}
	// No affected actors → no projection to write (e.g. a meta-vertex aspect,
	// or a vertex no actor reaches). A correct no-op.
	if len(actorKeys) == 0 {
		return nil, nil, nil
	}
	results, err := p.reprojectActors(ctx, rs, actorKeys)
	return results, actorKeys, err
}

// reprojectActors re-executes the capability cypher for each actor key and
// returns the concatenated result set. A missing (tombstoned) actor yields a
// Delete against its Capability KV key. Shared by the vertex fan-out
// (evaluateFanOut) and the link fan-out (evaluateLinkFanOut).
func (p *Pipeline) reprojectActors(ctx context.Context, rs ruleState, actorKeys []string) ([]ruleengine.EvalResult, error) {
	// This currentAdapter() call is independent of the one writeResults/
	// Hydrate later capture for the actual write — safe because a
	// HotReloadInto only ever swaps between two adapters of the SAME
	// target type for a given lens (INTO-only config fields like
	// subjectPrefix/stream change; Into.Target does not), so the
	// KeySetPublisher classification cannot flip mid-event even if the
	// concrete instance does.
	_, isPersonal := p.currentAdapter().(adapter.KeySetPublisher)
	var all []ruleengine.EvalResult
	for _, actorKey := range actorKeys {
		// Fetch the actor's properties via Core KV so the engine can
		// resolve the anchor `MATCH (identity {key: $actorKey})`
		// without scanning. Missing actors are skipped — they may have
		// been tombstoned out from under a stale adjacency edge.
		entryProps, err := p.fetchVertexProps(ctx, actorKey)
		if err != nil {
			return nil, fmt.Errorf("pipeline: fan-out fetch %q: %w", actorKey, err)
		}
		if entryProps == nil {
			if isPersonal {
				// A personal target has no cap-shaped delete key that fits
				// its wire shape (personal-lens-retraction-design.md §3.4) —
				// the caller's empty keyset frame for this enumerated actor
				// is what retracts every key, so emit no result here.
				continue
			}
			// Actor missing → retract its projection so the Capability KV
			// reflects the disappearance. This case can occur if the actor
			// was tombstoned but its adjacency hasn't been pruned yet. Reuse
			// multiEntryRetractions with an empty fresh set so every live
			// child under the actor's prefix is tombstoned, along with a
			// still-live legacy parent document if one exists
			// (cap-read-per-anchor-grant-keys-design.md §4.2/§6). Reproject's
			// own gate accepts a perEntry lens too (§4.3, widened to
			// `p.envelopeFn == nil && p.multiEnvelopeFn == nil`), so this
			// branch is reachable via the retry path's actor-reproject
			// (enqueueActorReprojectRetry) and the sweep's deep-verify alike:
			// `InstallActorAggregate` wires a real `entryKeyColumn` lens's
			// `SetMultiEnvelopeFn` at registration — the bootstrap
			// `capabilityRead` base lens is the first live one.
			if p.multiEnvelopeFn != nil {
				tombstones, rerr := p.multiEntryRetractions(ctx, actorKey, nil)
				if rerr != nil {
					return nil, rerr
				}
				all = append(all, tombstones...)
				continue
			}
			delKey := p.actorDeleteKeyFor(actorKey)
			all = append(all, ruleengine.EvalResult{
				Delete: true,
				Keys:   map[string]any{"key": delKey},
				Row:    nil,
			})
			continue
		}
		// Never seeded: an actor reprojection re-derives that actor's whole
		// capability set from its own vertex, which is not an anchor-labeled
		// CDC event and carries no proof that only one anchor's rows moved.
		res, err := p.executeFullForActor(ctx, rs, actorKey, entryProps, "")
		if err != nil {
			return nil, err
		}
		all = append(all, res...)
	}
	return all, nil
}

// emitPersonalFrames publishes one keyset frame per enumerated actor
// through adpt, when it is KeySetPublisher-capable (personal-lens-
// retraction-design.md §3.1-3.2, R1). adpt must be the SAME adapter
// instance the caller already wrote results through (writeResults captures
// it once via currentAdapter() and passes it here) — re-resolving
// currentAdapter() independently at this later point would let a
// concurrent HotReloadInto swap the adapter between the write and the
// frame, so the frame could describe rows written through a different
// adapter instance than the one it's attributed to (or a capability-typed
// pre-reload adapter could see a post-reload personal instance and
// no-op incorrectly, or vice versa). enumeratedActors is nil for a plain
// lens or a non-personal actor-aware pipeline — skip entirely (a cheap
// check before any type assertion). Grouping frame keys from results —
// rather than treating an actor absent from results as having nothing to
// say — still yields an empty frame for such an actor, because the loop
// below ranges over enumeratedActors, not over results: an actor whose
// evaluation produced zero surviving rows (D1 deny, Interest Set miss, a
// missing actor, a genuinely empty match) gets a frame with no keys, which
// is exactly the last-row-retraction signal (§3.2 rule 1). A publish error
// is logged, not surfaced: the write this frame describes already
// succeeded, so losing the frame only risks staleness the next live event
// or hydrate heals (§3.5) — never a wrong delete, since the client only
// prunes on a frame it actually receives.
func (p *Pipeline) emitPersonalFrames(ctx context.Context, adpt adapter.Adapter, enumeratedActors []string, results []ruleengine.EvalResult, revision uint64) {
	if len(enumeratedActors) == 0 {
		return
	}
	publisher, ok := adpt.(adapter.KeySetPublisher)
	if !ok {
		return
	}
	byActor := make(map[string][]map[string]any, len(enumeratedActors))
	for i := range results {
		if results[i].Delete {
			continue
		}
		actorID, _ := results[i].Keys[adapter.PersonalActorKeyField].(string)
		if actorID == "" {
			continue
		}
		byActor[actorID] = append(byActor[actorID], results[i].Keys)
	}
	for _, actorVtxKey := range enumeratedActors {
		_, actorID, ok := substrate.ParseVertexKey(actorVtxKey)
		if !ok {
			continue
		}
		if err := publisher.PublishKeySet(ctx, actorID, byActor[actorID], revision); err != nil {
			slog.Error("pipeline: publish keyset frame",
				"ruleId", p.ruleID, "actorId", actorID, "err", err)
		}
	}
}

// resultsContainKeys reports whether any non-delete result carries the given
// target-key map — the filter-retraction presence test: present ⇒ the anchor
// still projects, absent ⇒ its row must be retracted. Keys compare by their
// canonical JSON rendering (the identity the adapters key on), so a
// same-valued key differing only in in-memory numeric type reads as PRESENT —
// erring toward linger (safe), never toward deleting a row the adapter would
// address identically.
func resultsContainKeys(results []ruleengine.EvalResult, keys map[string]any) bool {
	want, err := json.Marshal(keys)
	if err != nil {
		return true // unmarshalable keys: treat as present → no Delete (fail safe)
	}
	for i := range results {
		if results[i].Delete {
			continue
		}
		got, gerr := json.Marshal(results[i].Keys)
		if gerr == nil && bytes.Equal(got, want) {
			return true
		}
	}
	return false
}

// applyDiffRetraction closes the neighbor-driven / multi-row retraction gap
// Fire 2's anchor-self presence check cannot reach by construction (a
// composite output key with a column bound to a non-anchor variable, so
// AnchorProjectionKey returns ok=false for every event on the lens, not just
// some). It reads the target's full live key set via adapter.KeyLister,
// diffs it against this re-execute's freshly-derived row set, and appends a
// Delete for every key the target still carries but the fresh computation no
// longer produces.
//
// Correctness rests on the lens itself being a genuinely unanchored
// whole-scan (no `{key: $actorKey}` seed anywhere in its MATCH clauses, the
// shape every live diffRetraction-opted-in lens has): because the query
// re-derives the COMPLETE current truth on every re-execute regardless of
// which vertex seeded it, comparing that complete truth against the target's
// complete existing key set is exact — not an approximation scoped to
// "whichever vertex happened to trigger this event," which would risk
// misattributing an identity vertex's role (e.g. applicant vs. managing
// landlord) and deriving the wrong scope. Only called when p.diffRetraction
// is set (SetDiffRetraction) — a convergence (`violating`-flag) lens never
// opts in, so its deliberate never-retract contract is untouched.
//
// An adapter that doesn't implement KeyLister is a configuration defect — a
// lens opted into DiffRetraction against a target that cannot enumerate its
// keys, so no row can ever be retracted. It fails the projection rather than
// passing results through: for the retraction-bearing lenses this mechanism
// exists to serve (a grant producer above all), silence would present a
// permanently inert path as a working security control. Activation refuses the
// lens up front (cmd/refractor's DiffRetraction guard), so reaching here means
// the adapter was swapped underneath a running pipeline — loud is correct.
func (p *Pipeline) applyDiffRetraction(ctx context.Context, results []ruleengine.EvalResult) ([]ruleengine.EvalResult, error) {
	lister, ok := p.currentAdapter().(adapter.KeyLister)
	if !ok {
		return nil, fmt.Errorf("pipeline: diff retraction: adapter %T does not implement adapter.KeyLister — the lens cannot retract anything", p.currentAdapter())
	}
	existing, err := lister.ListKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("pipeline: diff retraction: list keys: %w", err)
	}
	for _, exKeys := range existing {
		if resultsContainKeys(results, exKeys) {
			continue
		}
		results = append(results, ruleengine.EvalResult{Delete: true, Keys: exKeys})
	}
	return results, nil
}

// fetchVertexProps point-reads a vertex from Core KV and returns its
// properties (or nil if missing / soft-deleted).
func (p *Pipeline) fetchVertexProps(ctx context.Context, vtxKey string) (map[string]any, error) {
	entry, err := p.coreKV.Get(ctx, vtxKey)
	if err != nil {
		// A genuinely-absent key is "missing" (nil, nil); any other error
		// surfaces so the caller can decide retry/structural handling.
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if entry == nil || len(entry.Value) == 0 {
		return nil, nil
	}
	var props map[string]any
	if jerr := json.Unmarshal(entry.Value, &props); jerr != nil {
		return nil, jerr
	}
	if isDel, _ := props["isDeleted"].(bool); isDel {
		return nil, nil
	}
	return props, nil
}

// actorDeleteKeyFor derives the Capability KV key to delete when actorKey
// disappears, using the lens-specific derivation when one is installed and
// falling back to the primary cap.<actor> shape otherwise.
func (p *Pipeline) actorDeleteKeyFor(actorKey string) string {
	if p.actorDeleteKey != nil {
		return p.actorDeleteKey(actorKey)
	}
	return capabilityKeyForActor(actorKey)
}

// zeroRowDeleteKey reports actorKey's delete key when a doc-mode zero-row-
// retraction lens's evaluation produced no rows, but ONLY when the current
// adapter positively confirms that key is presently live. executeFullForActorOnce
// reaches this on EVERY zero-row evaluation for an armed lens, including an
// anchor whose filtering WHERE has never once matched — activation replay
// walks every anchor subject (DeliverLastPerSubject), so without this check a
// never-projected anchor would manufacture a guarded tombstone key on its very
// first evaluation, durably growing the target for a row that never existed.
// Mirrors Reproject's own presence check (reproject.go, RowReader.GetRow): an
// adapter that cannot read its own rows back, a failed read, or a confirmed
// absence all decline the emit (fail-safe, the current behavior) — only a
// positively confirmed live row earns the delete.
func (p *Pipeline) zeroRowDeleteKey(ctx context.Context, actorKey string) (string, bool) {
	reader, ok := p.currentAdapter().(adapter.RowReader)
	if !ok {
		return "", false
	}
	key := p.actorDeleteKeyFor(actorKey)
	_, present, err := reader.GetRow(ctx, map[string]any{"key": key})
	if err != nil || !present {
		return "", false
	}
	return key, true
}

// capabilityKeyForActor derives the Capability KV target key
// (cap.<type>.<id>) from an actor vertex key (vtx.<type>.<id>).
// Mirrors capabilityenv.capabilityKey but lives here to avoid a
// circular import (capabilityenv imports pipeline for EnvelopeFn).
func capabilityKeyForActor(actorKey string) string {
	if rest, ok := strings.CutPrefix(actorKey, substrate.VertexPrefix+"."); ok {
		return "cap." + rest
	}
	return "cap." + actorKey
}
