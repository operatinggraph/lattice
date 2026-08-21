package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// handleTracked wraps handle to advance the projection-liveness forward
// cursor (lastAppliedSeq) on every Ack — including ack-and-skip — but never on
// Nak (redelivery means the message has not actually been consumed yet).
func (p *Pipeline) handleTracked(ctx context.Context, msg substrate.Message) (substrate.Decision, error) {
	decision, err := p.handle(ctx, msg)
	if decision == substrate.Ack {
		p.recordAppliedSeq(msg.Sequence)
	}
	return decision, err
}

// handle is the supervised message handler (substrate.SupervisedHandler). It
// runs Refractor's full per-message policy — decode → classify key shape →
// evaluate → write, with terminal-DLQ and retry-queue disposition — and returns
// the substrate Decision the supervisor applies. A non-nil returned error is an
// Infra/Structural failure: the message is left UN-acked so JetStream redelivers
// it when the supervised pump resumes (the supervisor classifies the error and
// pauses). Transient and Terminal outcomes are disposed here and reported via the
// Decision (Nak for transient redelivery, Ack after DLQ/retry-enqueue).
func (p *Pipeline) handle(ctx context.Context, msg substrate.Message) (substrate.Decision, error) {
	// Extract Core KV key from subject: "$KV.<bucket>.<key>" → "<key>".
	// Done before the empty-body short-circuit so a link TOMBSTONE (which has
	// an empty body) can still be classified and fanned out on the actor-aware
	// pipeline (revocation must shrink capability docs).
	prefix := "$KV." + p.coreKVBucket + "."
	key := strings.TrimPrefix(msg.Subject, prefix)
	tombstone := len(msg.Body) == 0

	// One snapshot of the compiled rule for this whole message (see ruleMu). A
	// MATCH hot-reload runs on CoreKVSource's dispatch goroutine and can land at
	// any point below, so taking the rule once here is what makes this event
	// judged AND evaluated by a single rule — the next event picks up the new
	// one. Every gate and every evaluate arm reached from here is threaded this
	// value rather than re-reading the pipeline.
	rs := p.ruleState()

	// Classify the key by Lattice Contract #1 §1.5 shape.
	switch substrate.ClassifyKey(key) {
	case substrate.KindAspect:
		// An aspect-only mutation (e.g. identity .state, unit .listing, a
		// piiKey shred) changes a vertex's projected state with no
		// vertex-root event. On the actor-aware (capability) pipeline it
		// drives a fan-out reprojection seeded from the parent vertex. On a
		// plain lens it re-executes seeded from the owner vertex — refreshing
		// the row's aspect-derived fields (a Secure Lens's piiKey shred
		// scrubs projected plaintext to null this way) and, when the
		// mutation drops the owner out of the matched set (a WHERE flip /
		// keyed-aspect deletion), retracting its row via the evaluate path's
		// filter-retraction presence check.
		if p.actorEnumerator != nil {
			// An eligible actor-aware lens skips an aspect whose parent vertex
			// type its patterns cannot bind. A key that fails to parse falls
			// through so the fan-out raises the real error rather than being
			// silently dropped here.
			if _, parentType, _, _, pok := substrate.ParseAspectKey(key); pok &&
				!p.actorAwareFanOutRelevant(rs, parentType) {
				return substrate.Ack, nil
			}
			return p.evalAspectFanOut(ctx, rs, msg, key)
		}
		return p.evalPlainAspectReprojection(ctx, rs, msg, key)
	case substrate.KindLink:
		// A pure link mutation (holdsRole/manages/appliesToUnit/...) changes
		// graph topology with no vertex event. On the actor-aware pipeline it
		// drives a fan-out reprojection from both endpoints; on a plain lens
		// it re-executes seeded from each endpoint vertex — refreshing
		// link-derived rows and retracting an endpoint anchor that a
		// required-link removal drops from the matched set.
		if p.actorEnumerator != nil {
			// Skipped when the patterns never traverse this relation, or when
			// NEITHER endpoint type can bind — the two conjuncts a
			// relation-narrowed consumer filter pins server-side, judged here
			// off the same published sets. A key that fails to parse falls
			// through so the fan-out raises the real error rather than being
			// silently dropped here.
			if t1, _, relation, t2, _, pok := substrate.ParseLinkKey(key); pok &&
				!p.linkEventRelevant(rs, relation, t1, t2) {
				return substrate.Ack, nil
			}
			return p.evalLinkFanOut(ctx, rs, msg, key, tombstone)
		}
		return p.evalPlainLinkReprojection(ctx, rs, msg, key)
	case substrate.KindUnknown:
		slog.Warn("pipeline: unknown key shape — defect signal",
			"ruleId", p.ruleID, "key", key)
		return substrate.Ack, nil
	}

	// KindVertex. An empty body carries no props for any lens shape, so it is
	// acked and skipped unconditionally — the actor-aware pipeline included.
	// The event that actually retracts an actor is the SOFT delete
	// (isDeleted: true on a non-empty vertex root), which reaches the evaluate
	// path below and emits the cap Delete from there.
	if tombstone {
		return substrate.Ack, nil
	}

	// KindVertex: parse type and id.
	label, _, ok := substrate.ParseVertexKey(key)
	if !ok {
		// Should not occur after ClassifyKey == KindVertex, but guard defensively.
		return substrate.Ack, nil
	}

	// Unmarshal payload.
	var props map[string]any
	if err := json.Unmarshal(msg.Body, &props); err != nil {
		slog.Error("pipeline: unmarshal payload",
			"ruleId", p.ruleID, "entityId", key,
			"stage", "pipeline", "adapter", p.adapterName, "err", err)
		return substrate.Nak, nil
	}

	// Edge events carry a non-empty "nodeId" field — adjacency builder handles these.
	if nodeID, _ := props["nodeId"].(string); nodeID != "" {
		return substrate.Ack, nil
	}

	// A lens whose compiled patterns provably cannot bind this vertex type
	// skips the re-execute outright — the KindVertex counterpart of the
	// aspect/link arms' relevance skip. Unlike those arms, KindVertex has no
	// per-type dispatch of its own (every vertex-root event runs the same full
	// evaluate below), so every vertex write in the system would otherwise fan
	// into a full evaluation regardless of type. Both pipeline shapes consult
	// the one gate: a plain lens through plainVertexRelevant, an actor-aware one
	// through §4.2's conjunction.
	if !p.vertexEventRelevant(rs, label) {
		return substrate.Ack, nil
	}

	isDeleted, _ := props["isDeleted"].(bool)
	entry := ruleengine.NodeEntry{
		CoreKVKey:  key,
		NodeLabel:  label,
		IsDeleted:  isDeleted,
		Properties: props,
	}

	// Evaluate against the full engine ([]ProjectionResult{Key,Values,Delete}).
	// evaluateForEntry normalises and applies the envelope so the downstream
	// write path sees a single []ruleengine.EvalResult shape.
	results, enumeratedActors, err := p.evaluateForEntry(ctx, rs, entry)
	if err != nil {
		slog.Error("pipeline: evaluate",
			"ruleId", p.ruleID, "entityId", key,
			"stage", "traversal", "adapter", p.adapterName, "err", err)
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, enumeratedActors)
	}

	// Write each result through the shared write path (failure classification,
	// terminal DLQ, retry enqueue, ack discipline). The adapter is captured
	// once inside writeResults so all results in this message use a consistent
	// instance even if HotReloadInto swaps it between messages.
	return p.writeResults(ctx, rs, msg, key, results, enumeratedActors)
}

// evalLinkFanOut handles a KindLink CDC event on the actor-aware pipeline.
// It determines whether the link is a create or a tombstone, drives the
// endpoint-seeded fan-out reprojection (evaluateLinkFanOut), and writes the
// resulting capability projections through the normal write path.
//
// A link tombstone arrives with an empty body (NATS DEL/PURGE). A link create
// or update arrives with a body whose `isDeleted` field distinguishes a
// soft-delete (revocation) from an active link.
func (p *Pipeline) evalLinkFanOut(ctx context.Context, rs ruleState, msg substrate.Message, key string, tombstone bool) (substrate.Decision, error) {
	isDeleted := tombstone
	if !tombstone {
		var props map[string]any
		if err := json.Unmarshal(msg.Body, &props); err != nil {
			slog.Error("pipeline: link fan-out: unmarshal payload",
				"ruleId", p.ruleID, "entityId", key, "err", err)
			return substrate.Nak, nil
		}
		isDeleted, _ = props["isDeleted"].(bool)
	}

	results, enumeratedActors, err := p.evaluateLinkFanOut(ctx, rs, key, isDeleted)
	if err != nil {
		slog.Error("pipeline: link fan-out: evaluate",
			"ruleId", p.ruleID, "entityId", key, "stage", "traversal", "err", err)
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, enumeratedActors)
	}
	if err := p.applySecureDecrypt(ctx, results); err != nil {
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, enumeratedActors)
	}

	return p.writeResults(ctx, rs, msg, key, results, enumeratedActors)
}

// evalPlainAspectReprojection handles a KindAspect CDC event on a plain
// (non-actor-aware) lens: re-evaluate the owner (parent) vertex so its
// projected row reflects the aspect's current state — a changed field
// refreshes, a Secure Lens's piiKey shred scrubs plaintext to null
// (decrypt fails ErrKeyShredded → secure columns project null), and an
// aspect mutation that drops the owner out of the matched set retracts its
// row through the evaluate path's filter-retraction presence check. The
// aspect body is irrelevant (the re-execute reads current Core KV state),
// so a tombstone and a value change take the same path. An owner whose type
// doesn't bind the lens's MATCH evaluates to zero rows with no derivable
// anchor key (harmless no-op); a missing/tombstoned owner is skipped — row
// deletion belongs to the anchor-tombstone path.
func (p *Pipeline) evalPlainAspectReprojection(ctx context.Context, rs ruleState, msg substrate.Message, key string) (substrate.Decision, error) {
	parentVtx, parentType, _, _, ok := substrate.ParseAspectKey(key)
	if !ok {
		return substrate.Ack, nil
	}
	if !rs.plainReactsTo(parentType) {
		// The lens's patterns cannot bind this vertex type — the mutation
		// cannot change its rows; skip the re-execute.
		return substrate.Ack, nil
	}
	results, err := p.evaluatePlainFromVertex(ctx, rs, parentVtx, parentType)
	if err != nil {
		slog.Error("pipeline: plain aspect reprojection: evaluate",
			"ruleId", p.ruleID, "entityId", key, "stage", "traversal", "err", err)
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, nil)
	}
	return p.writeResults(ctx, rs, msg, key, results, nil)
}

// evalPlainLinkReprojection handles a KindLink CDC event on a plain
// (non-actor-aware) lens: re-evaluate both endpoint vertices so rows derived
// through the link refresh, and an endpoint anchor that a required-link
// removal drops from the matched set retracts through the evaluate path's
// filter-retraction presence check. A link tombstone (empty body, or a body
// with isDeleted) and a link create take the same evaluate path — the
// re-execute reads current adjacency either way. Results are deduplicated
// across the two endpoint evaluations (a whole-type-scan lens re-derives the
// same row set from each seed).
//
// Like the actor-aware link fan-out, the link is first idempotently applied
// to adjKV (both directional entries, the link key as EdgeID — matching the
// dedicated adjacency consumer's events exactly): the two consumers observe
// the same CDC event with no cross-consumer ordering guarantee, so applying
// it here first is what guarantees the re-execute always reads a consistent
// edge set — a tombstone's re-execute can never still see the removed edge.
func (p *Pipeline) evalPlainLinkReprojection(ctx context.Context, rs ruleState, msg substrate.Message, key string) (substrate.Decision, error) {
	type1, id1, linkName, type2, id2, ok := substrate.ParseLinkKey(key)
	if !ok {
		return substrate.Ack, nil
	}
	if !p.linkEventRelevant(rs, linkName, type1, type2) {
		// Either the lens's patterns never traverse this relation, or neither
		// endpoint type is bindable by them; either way the link cannot appear
		// in its traversals. Skip — including the adjacency self-apply: the
		// dedicated consumer owns the index, this lens just doesn't need it
		// applied-before-read.
		return substrate.Ack, nil
	}
	reacts1, reacts2 := rs.plainReactsTo(type1), rs.plainReactsTo(type2)
	isDeleted := len(msg.Body) == 0
	if !isDeleted {
		var linkProps map[string]any
		if err := json.Unmarshal(msg.Body, &linkProps); err != nil {
			// A malformed link body can never parse on redelivery — Terminal
			// (DLQ), matching the dedicated adjacency consumer's disposition
			// for the identical message. A bare Nak here would poison-loop
			// every plain pipeline on one bad body.
			slog.Error("pipeline: plain link reprojection: unmarshal payload",
				"ruleId", p.ruleID, "entityId", key, "err", err)
			return p.dispositionEvalErr(ctx, msg, key, "decode",
				failure.Terminal(fmt.Errorf("pipeline: plain link reprojection: unmarshal %q: %w", key, err)), nil)
		}
		isDeleted, _ = linkProps["isDeleted"].(bool)
	}
	for _, evt := range adjacency.EventsForLink(key, type1, id1, linkName, type2, id2, isDeleted) {
		if err := adjacency.Build(ctx, p.adjKV, evt); err != nil {
			slog.Error("pipeline: plain link reprojection: adjacency build",
				"ruleId", p.ruleID, "entityId", key, "err", err)
			return p.dispositionEvalErr(ctx, msg, key, "traversal", err, nil)
		}
	}
	endpoints := []struct {
		vtx, label string
		reacts     bool
	}{
		{"vtx." + type1 + "." + id1, type1, reacts1},
		{"vtx." + type2 + "." + id2, type2, reacts2},
	}
	var combined []ruleengine.EvalResult
	seen := make(map[string]bool)
	for _, ep := range endpoints {
		if !ep.reacts {
			continue
		}
		results, err := p.evaluatePlainFromVertex(ctx, rs, ep.vtx, ep.label)
		if err != nil {
			slog.Error("pipeline: plain link reprojection: evaluate",
				"ruleId", p.ruleID, "entityId", key, "endpoint", ep.vtx,
				"stage", "traversal", "err", err)
			return p.dispositionEvalErr(ctx, msg, key, "traversal", err, nil)
		}
		for _, r := range results {
			id := dedupeKeyFor(r)
			if seen[id] {
				continue
			}
			seen[id] = true
			combined = append(combined, r)
		}
	}
	return p.writeResults(ctx, rs, msg, key, combined, nil)
}

// evalAspectFanOut handles a KindAspect CDC event on the actor-aware
// pipeline. An aspect-only mutation (e.g. identity .state, role .description)
// carries no vertex-root event, so the parent vertex's projection is re-derived
// by seeding the fan-out from the parent vertex (evaluateAspectFanOut) and
// writing the resulting capability projections through the normal write path.
//
// Unlike a link, an aspect mutation does not change graph topology, so no
// adjacency update is required; the aspect body is irrelevant to the fan-out
// (the reprojection cypher re-reads current Core KV state), so a tombstone
// (empty body) and a value change take the same path.
func (p *Pipeline) evalAspectFanOut(ctx context.Context, rs ruleState, msg substrate.Message, key string) (substrate.Decision, error) {
	results, enumeratedActors, err := p.evaluateAspectFanOut(ctx, rs, key)
	if err != nil {
		slog.Error("pipeline: aspect fan-out: evaluate",
			"ruleId", p.ruleID, "entityId", key, "stage", "traversal", "err", err)
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, enumeratedActors)
	}
	if err := p.applySecureDecrypt(ctx, results); err != nil {
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, enumeratedActors)
	}

	return p.writeResults(ctx, rs, msg, key, results, enumeratedActors)
}

// dispositionEvalErr maps an evaluate-stage error to a Decision (+ error for the
// pause path), mirroring the inline ack/nak discipline the pre-supervisor
// pipeline applied: Infra/Structural leave the message pending (return the error
// so the supervisor pauses); Terminal publishes a DLQ entry and acks; Transient
// naks for redelivery.
//
// A footprint-validation drift (failure.ErrEvalDrift, evaluation-consistency-
// design.md §4.3) is Transient like any other, but a bare Nak would only
// redeliver the SAME triggering message — re-running the exact evaluation
// that just drifted, with no backoff and no health signal. When this
// pipeline is actor-aggregate (an envelope wrapper installed, the only shape
// ErrEvalDrift can ever come from — needsFootprintValidation gates on it) and
// a retry queue is configured, the failing actor(s) are enqueued through the
// same re-evaluate-and-write closure the write-path transient-failure branch
// already uses (enqueueActorReprojectRetry), so the drift gets the queue's
// backoff/DLQ/health machinery instead of an un-backed-off redelivery loop.
// enumeratedActors is nil for a plain lens (which can never produce
// ErrEvalDrift — needsFootprintValidation always false) and for the actor's
// own vertex re-evaluating itself, where key IS the actor key.
func (p *Pipeline) dispositionEvalErr(ctx context.Context, msg substrate.Message, key, stage string, err error, enumeratedActors []string) (substrate.Decision, error) {
	cat := failure.Classify(err)
	if cat == failure.CatInfra || cat == failure.CatStructural {
		// Do NOT dispose — leave pending for redelivery when the pipeline resumes.
		return substrate.Nak, err
	}
	if cat == failure.CatTerminal {
		p.publishTerminalDLQ(ctx, msg.Body, key, stage, err)
		return substrate.Ack, nil
	}
	if errors.Is(err, failure.ErrEvalDrift) && p.retryQueue != nil && p.retryMaxAttempts > 0 &&
		(p.envelopeFn != nil || p.multiEnvelopeFn != nil) && p.actorEnumerator != nil {
		actors := enumeratedActors
		if len(actors) == 0 {
			actors = []string{key}
		}
		for _, actor := range actors {
			p.enqueueActorReprojectRetry(msg.Body, actor)
		}
		return substrate.Ack, nil
	}
	return substrate.Nak, nil
}
