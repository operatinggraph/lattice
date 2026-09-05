package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// writeResults writes a slice of evaluation results through the active adapter,
// applying the same failure-classification, terminal-DLQ, retry-enqueue, and
// ack discipline as the inline vertex write loop. Returns the Decision the
// supervisor applies (plus a non-nil error on an infra/structural write failure,
// which leaves the message pending and pauses the pump).
//
// Retry enqueues and terminal DLQ publishes are buffered and flushed only when
// the whole batch is known free of infra/structural failures. Any path that
// leaves the message pending (Nak) makes redelivery re-run every result, so
// flushing eagerly would enqueue/publish a duplicate for the already-disposed
// results on every redelivery (e.g. each pause/resume cycle).
//
// enumeratedActors (personal-lens-retraction-design.md §3.2, R1) is nil for
// a plain lens or a non-personal actor-aware pipeline; otherwise a keyset
// frame is published per enumerated actor once every result has cleanly
// applied (no retry-enqueue, no terminal DLQ) — a partially-disposed batch
// emits no frame, so a would-be-retracting frame can never race ahead of
// the write it is supposed to describe.
//
// scope decides which non-delete results are WRITTEN on a personal target
// (personal-lens-delta-publication-design.md §4.2). A declined result is
// neither written, audited, counted toward the freshness clock, nor entered
// into the retry queue or the DLQ: it is unchanged on the device, and the
// frame naming it is what keeps the copy the device holds. EVERY non-delete
// result is still framed, and a Delete is never scoped — a retraction is not a
// content change. On any other target the scope is ScopeAll by construction
// and this loop is what it has always been.
//
// ScopeSilent is the one scope that also withholds the frame, and the one that
// withholds Deletes: it is a personal event handled while this lens's rebuild is
// replaying, where every message would land below the connected device's frame
// high-water mark and be dropped there (§4.5). Nothing else changes — the
// message is acked, so the ordering token advances and the rescan drains — and
// the rebuilt shape reaches the device from the content cycle the completion
// requests.
func (p *Pipeline) writeResults(ctx context.Context, rs ruleState, msg substrate.Message, key string, results []ruleengine.EvalResult, enumeratedActors []string, scope PublishScope) (substrate.Decision, error) {
	if p.supersededRule(rs) {
		slog.Info("pipeline: rule swapped mid-event — naking so redelivery evaluates the new rule",
			"ruleId", p.ruleID, "entityId", key, "seq", msg.Sequence)
		return substrate.Nak, nil
	}
	adpt := p.currentAdapter()
	// Resolved once per call — adpt itself is captured once above, so whether
	// it reports write outcomes cannot change across this call's loop.
	outcomeAdpt, reportsOutcome := adpt.(adapter.OutcomeUpserter)
	// The retraction direction needs its own outcome channel, and needs it for
	// two independent reasons. The audit skip below reads `committed`, and a
	// plain Delete discards whether the guard actually committed — so a
	// retraction the ordering guard DECLINED was still audited as a write that
	// happened.
	// And the D1 grant-change edge's revocation half is derived from the
	// delete's transition, which the plain call throws away with everything
	// else: without this the edge would ship a working grant-lands trigger and
	// a dead grant-revoked one.
	deleteOutcomeAdpt, reportsDeleteOutcome := adpt.(adapter.OutcomeDeleter)
	// The publication scope governs a personal target and nothing else. It is
	// read off the adapter captured above, so it cannot flip mid-call, and the
	// classification is the one reprojectActors makes for the same reason: a
	// HotReloadInto only swaps between adapters of one target type.
	_, scoped := adpt.(adapter.KeySetPublisher)
	// Guard 1 of §4.6: an actor whose publish slot a cold Hydrate holds is
	// published WHOLE whatever the scope says. The hydrate is republishing that
	// actor's every row at a LOWER revision, so a scoped publish here would
	// advance the device's frame high-water past the hydrate's and cost it
	// every row it does not already hold. Asked once per enumerated actor
	// rather than per row: the answer is a property of the actor, the row loop
	// below is thousands of iterations wide, and a hydrate that begins after
	// this read is closed by guard 2 on its own side.
	//
	// Not asked under ScopeSilent, which withholds the frame as well as the
	// rows. The hazard this guard closes is a scoped publish advancing the
	// device's frame high-water past a running hydrate's, and a pass that
	// publishes no frame advances nothing — so the exemption would only put
	// replayed rows back on the wire for the one actor least able to use them.
	var hydrating map[string]struct{}
	if scoped && scope.Kind() != ScopeKindAll && scope.Kind() != ScopeKindSilent {
		for _, actorVtxKey := range enumeratedActors {
			_, actorID, ok := substrate.ParseVertexKey(actorVtxKey)
			if !ok || !p.hydrateInFlight(actorID) {
				continue
			}
			if hydrating == nil {
				hydrating = map[string]struct{}{}
			}
			hydrating[actorID] = struct{}{}
		}
	}
	// admits answers the one question the loop asks per row: does this result
	// get written. A non-personal target and a Delete both answer yes without
	// consulting anything.
	admits := func(result ruleengine.EvalResult) bool {
		if !scoped {
			return true
		}
		if scope.Kind() == ScopeKindSilent {
			// Ahead of the Delete exemption, which every other scope grants: a
			// silent pass puts nothing on the wire at all. A retraction it
			// withholds is carried by the authoritative frame of the content
			// cycle the rebuild's completion requests — a key that frame omits
			// is what actually prunes the row on the device — where a replayed
			// Delete below the device's high-water mark prunes nothing.
			return false
		}
		if result.Delete {
			return true
		}
		if scope.Admits(result) {
			return true
		}
		actorID, _ := result.Keys[adapter.PersonalActorKeyField].(string)
		if actorID == "" {
			// A personal result whose keys carry no actor is malformed, and the
			// hydrate exemption below cannot be asked about it. Handed to the
			// adapter anyway, so the adapter is what answers: a personal target
			// keys on this field and refuses the write LOUDLY — a Nak, a health
			// fault, a line naming the lens. Reading the missing actor as "not
			// being hydrated" would withhold the row here in silence instead,
			// turning a reported defect into a row that simply never appears on
			// a device.
			return true
		}
		_, beingHydrated := hydrating[actorID]
		return beingHydrated
	}

	// Two publish pipelines, deliberately separate, and neither one reaches
	// anything but its own loop.
	//
	// writeCtx carries the ROW pipeline, so the loop below pays one ack round
	// trip for all of its writes instead of one per row — the cost that makes a
	// wide personal actor's evaluation take tens of seconds. ctx itself is left
	// alone: the keyset frame, the terminal DLQ and the retry enqueues all run
	// under it and must not join a pipeline nobody flushes for them. The frame
	// in particular is published only after flushRowPipeline has returned
	// cleanly, which is what keeps its "the rows I describe have applied"
	// contract true.
	//
	// The AUDIT pipeline is the writer's own and is flushed on the way out,
	// whatever the disposition — a lost audit entry is logged, never a reason
	// to redeliver, so folding it into the row pipeline would let forensics
	// decide the fate of correctly written rows.
	writeCtx := ctx
	var rowPipeline *substrate.PublishPipeline
	if opener, ok := adpt.(adapter.PublishPipelineOpener); ok {
		rowPipeline = opener.NewPublishPipeline()
		writeCtx = adapter.WithPublishPipeline(ctx, rowPipeline)
	}
	var auditPipeline *substrate.PublishPipeline
	if p.auditWriter != nil {
		auditPipeline = p.auditWriter.NewPublishPipeline()
		defer p.flushAuditPipeline(ctx, auditPipeline, key)
	}

	var retryResults []ruleengine.EvalResult
	var terminalErrs []error
	// Entries the evaluation proved the target already holds verbatim
	// (EvalResult.Unchanged). Counted so the lens's processed line and its
	// health entry report the writes this pass did NOT make — without it the
	// mechanism is invisible, and a lens that has stopped writing because the
	// grants stopped changing looks exactly like one that has stopped working.
	withheld := 0
	// committedResults are the rows the adapter reported as landed, held back
	// until the flush below proves it. Emitting the freshness mark and the
	// audit entry from inside the loop would record rows a failed flush is
	// about to redeliver — see the buffering point in the loop.
	var committedResults []ruleengine.EvalResult
	transientActorRetry := false
	for i := range results {
		// Stamp the triggering CDC message's stream sequence as the monotonic
		// ordering token before any write. The retry-queue capture copies the
		// stamped result, so a replay carries this same (original, lower) seq,
		// which is exactly what must lose to a later real reprojection.
		results[i].ProjectionSeq = msg.Sequence
	}
	for _, result := range results {
		if !admits(result) {
			// Withheld, not failed. The row is unchanged on the device, so
			// nothing is written, audited, counted or disposed for it — and it
			// still reaches emitPersonalFrames below, which is what stops the
			// client pruning the copy it holds.
			continue
		}
		if p.honoursUnchanged(result) {
			// The target already holds this exact body, so there is no write
			// to make and nothing to say about one: not written, not audited,
			// not counted by recordProjectionWrite, and never entered into the
			// retry or terminal lists. The stored row's projectionSeq stays
			// where the write that last CHANGED this entry left it, which is
			// what makes a withheld write safe to skip: that watermark already
			// carries the entry's last presence change.
			//
			// The freshness clock is the one thing a withheld entry DOES move,
			// and it moves once for the pass rather than once per entry (see
			// the mark after the flush below). lastProjectedAt answers "is this
			// lens still projecting", and a lens that evaluated the event and
			// confirmed every entry current is projecting — leaving the clock
			// frozen through a converged steady state would make a healthy
			// perEntry lens indistinguishable from a stalled one.
			//
			// Only a non-delete, non-FailClosed result can be marked, and
			// honoursUnchanged refuses both shapes again on the way in, so
			// §6.14's tombstones-first ordering and the FailClosed abort are
			// untouched by this branch. The verdict is also refused outright
			// if a hot reload has swapped the adapter since the comparison —
			// it was a statement about the target that is no longer there.
			// Counted at exactly the point recordProjectionWrite counts a
			// write, and for the same reason: the tally is of work this pass
			// decided on, so a redelivery that decides again counts again,
			// just as a rewritten row does.
			p.entriesWithheld.Add(1)
			withheld++
			continue
		}
		var writeErr error
		committed := true
		writtenKey := ""
		transition := adapter.TransitionNone
		if result.Delete {
			if reportsDeleteOutcome {
				var outcome adapter.DeleteOutcome
				outcome, writeErr = deleteOutcomeAdpt.DeleteWithOutcome(writeCtx, result.Keys, result.ProjectionSeq)
				committed, writtenKey, transition = outcome.Wrote, outcome.Key, outcome.Transition
			} else {
				writeErr = adpt.Delete(writeCtx, result.Keys, result.ProjectionSeq)
			}
		} else if reportsOutcome {
			var outcome adapter.UpsertOutcome
			outcome, writeErr = outcomeAdpt.UpsertWithOutcome(writeCtx, result.Keys, result.Row, result.ProjectionSeq)
			// Committed, not Wrote. A guarded adapter reports Wrote on every call
			// because maintaining the projectionSeq watermark is its job whatever
			// the row says, and it has more than one way to end up storing nothing
			// — a stale-or-equal watermark, and a write with no ordering token at
			// all. Committed states positively that a row landed, which is the
			// only claim an audit entry's outputRowHash can honestly stand behind
			// and the only event the read-model's freshness clock marks.
			committed, writtenKey, transition = outcome.Committed, outcome.Key, outcome.Transition
		} else {
			writeErr = adpt.Upsert(writeCtx, result.Keys, result.Row, result.ProjectionSeq)
		}
		p.recordProjectionWrite()

		if writeErr != nil {
			cat := failure.Classify(writeErr)
			op := "upsert"
			if result.Delete {
				op = "delete"
			}
			slog.Error("pipeline: "+op,
				"ruleId", p.ruleID, "entityId", key,
				"stage", "write", "adapter", p.adapterName, "err", writeErr)

			if errors.Is(writeErr, adapter.ErrUnsanctionedReadGrantKey) {
				// Checked BEFORE FailClosed, and it is the only error that
				// jumps that queue.
				//
				// FailClosed exists so a retraction that did not take effect
				// cannot be masked by a sibling's fresh upsert landing: the
				// whole batch is redelivered instead. That reasoning assumes
				// the failure might not recur. This one always does — the
				// lens's own declaration is what makes the key unsanctioned,
				// so every redelivery renders the same key and is refused
				// again, and a perEntry retraction carrying FailClosed would
				// Nak the lens into a permanent redelivery loop against a
				// misconfiguration no retry can fix.
				//
				// Nothing is masked by acking instead: the guard refuses the
				// lens's writes into that namespace in BOTH directions, so
				// there is no sibling upsert to land ahead of the retraction
				// this refused. The lens is dark for the namespace, loudly, on
				// its own health entry — which is the state a misdeclared
				// grant writer should be in.
				terminalErrs = append(terminalErrs, writeErr)
				continue
			}
			if result.FailClosed {
				// A FailClosed result's own failure must never be masked by
				// continuing to write its batch siblings (ruleengine.EvalResult's
				// doc) — abort for a full redelivery regardless of category,
				// rather than let e.g. CatTransient's per-actor-continue land a
				// sibling's fresh upsert while this retraction never took effect.
				return substrate.Nak, writeErr
			}
			if cat == failure.CatInfra || cat == failure.CatStructural {
				// Buffered dispositions are dropped — redelivery re-evaluates
				// every result after the pause resolves.
				return substrate.Nak, writeErr
			}
			if cat == failure.CatTerminal {
				terminalErrs = append(terminalErrs, writeErr)
				continue
			}
			if cat == failure.CatTransient && p.retryQueue != nil && p.retryMaxAttempts > 0 {
				if p.multiEnvelopeFn != nil {
					// §4.3: a perEntry lens's grants live under N per-anchor
					// keys, not the single parent row a raw WriteFn replay
					// (enqueueRetry) assumes. Replaying this failed write's
					// captured Keys/Row/Seq later could resurrect a
					// since-revoked anchor through the absent-key Create
					// door — a grant-era write that failed here leaves no
					// key and no watermark, so a later revocation's prefix
					// diff can never tombstone it, and the stale replay then
					// lands unopposed. Refuse the raw replay and re-evaluate
					// the actor instead (enqueueActorReprojectRetry) — the
					// same unit the sweep already repairs at, so a revoked
					// anchor is simply absent from the fresh set rather than
					// resurrected.
					transientActorRetry = true
					continue
				}
				retryResults = append(retryResults, result)
				continue
			}
			return substrate.Nak, nil
		}

		p.notifyGrantChange(writtenKey, transition)
		if committed {
			// Buffered, not emitted. The read model's freshness clock and its
			// forensic trail both mark the same event — a row landing in the
			// target — and under a pipelined write a row is only KNOWN to have
			// landed once the flush below comes back clean. Marking here would
			// append an audit entry whose outputRowHash describes a row the
			// stream never stored, and advance lastProjectedAt over a target
			// that has not changed, on exactly the path that then Naks. (The
			// same reasoning as the guard-declined case: a write that stored
			// nothing marks neither clock nor trail.)
			committedResults = append(committedResults, result)
		}
	}

	// The rows are on the wire but not yet known stored. Await them here —
	// ahead of every disposition below, including the ack the terminal/retry
	// path takes: acking a unit whose rows never landed would lose them with
	// no DLQ and no retry entry behind them.
	if err := p.flushRowPipeline(ctx, rowPipeline, key); err != nil {
		if cat := failure.Classify(err); cat == failure.CatInfra || cat == failure.CatStructural {
			// Same disposition the per-row branch takes for these: pending
			// message, paused pump, no frame.
			return substrate.Nak, err
		}
		// Everything else redelivers plainly. A flush error names no single
		// result, so neither of the per-row dispositions that need one — the
		// DLQ publish and the retry-queue capture — can be reached from here;
		// redelivery re-runs the whole batch instead, which the idempotent
		// upserts behind it are built for. Nothing is masked: no ack, and no
		// frame, so a retraction this batch was carrying cannot be reported as
		// applied. CatTerminal never arrives here in practice — it exists only
		// where an adapter wraps an error in failure.Terminal, and a publish
		// path has no such wrap — so this is not a DLQ route left unbuilt.
		return substrate.Nak, nil
	}

	// Only now is a committed row a stored row. The freshness clock and the
	// audit trail advance together, once, for exactly the rows the flush stands
	// behind — a failed flush returned above without emitting either.
	for i := range committedResults {
		p.recordProjected()
		p.writeAudit(ctx, auditPipeline, key, committedResults[i])
	}
	if withheld > 0 && len(committedResults) == 0 {
		// A pass that wrote nothing because everything was already current is
		// still a pass that projected: it evaluated the event and confirmed the
		// target correct, which is exactly what lastProjectedAt is asked about.
		// ONCE for the pass, not once per entry — the clock is a timestamp, not
		// a tally — and skipped when a committed row already marked it, so the
		// two paths cannot double-stamp the same instant.
		//
		// The audit trail and recordProjectionWrite stay silent: both describe
		// a row landing in the target, and no row landed. Only the liveness
		// clock has a true thing to say about a withheld pass.
		p.recordProjected()
	}

	for _, terr := range terminalErrs {
		p.publishTerminalDLQ(ctx, msg.Body, key, "write", terr)
	}
	for _, r := range retryResults {
		p.enqueueRetry(key, msg.Body, r)
	}
	if transientActorRetry {
		// enumeratedActors is nil for the actor's own vertex re-evaluating
		// itself (key IS the actor key there); a fan-out call already names
		// every affected actor — but only because InstallActorAggregate
		// (projection/driver.go) always pairs multiEnvelopeFn with an
		// ActorEnumerator, so this fallback is safe ONLY under that pairing.
		// Nothing here structurally enforces it (a hand-built pipeline could
		// set multiEnvelopeFn alone), and getting it wrong is worse than the
		// bug this mechanism replaces: Reproject on a non-actor key (an
		// aspect/link key) evaluates to zero rows and returns a clean
		// "wrote nothing", which reads as success — the failed write
		// vanishes with no DLQ, no trace. Refuse closed instead of guessing.
		if p.actorEnumerator == nil {
			err := fmt.Errorf("pipeline: writeResults: rule %q: a perEntry lens (multiEnvelopeFn) has no ActorEnumerator installed — refusing to guess an actor key for retry rather than risk reprojecting the wrong entity or silently losing the write", p.ruleID)
			slog.Error("pipeline: transient write refused — perEntry lens missing its ActorEnumerator pairing", "ruleId", p.ruleID, "entityId", key, "err", err)
			return substrate.Nak, err
		}
		// Reprojects every actor this batch touched, not just the one whose
		// write failed — a zero-write no-op for any actor whose row already
		// converged (Reproject's own comparison). This is a known cost, not
		// a free one: a large fan-out (ActorEnumerator's own documented cap
		// is in the thousands) turns one transient blip into that many full
		// cypher re-evaluations queued on the pipeline's single shared
		// RetryQueue, head-of-line-blocking every other lens's retries for
		// the duration. Narrowing this to only the actor(s) that actually
		// own a failed result needs the same key→actor inversion §4.4's
		// AnchorFromKey builds for the sweep — deliberately not duplicated
		// here; the retry stays this coarser, safe-but-costlier unit, with
		// the cost named rather than silently absorbed.
		//
		// enumeratedActors is nil for the actor's own vertex re-evaluating
		// itself — key IS the actor key there, safe now that the check
		// above has confirmed p.actorEnumerator != nil (the only condition
		// that fallback ever relies on).
		actors := enumeratedActors
		if len(actors) == 0 {
			actors = []string{key}
		}
		for _, actor := range actors {
			p.enqueueActorReprojectRetry(msg.Body, actor)
		}
	}

	if len(retryResults) > 0 || len(terminalErrs) > 0 || transientActorRetry {
		// Transient enqueue / terminal DLQ: the message is fully disposed —
		// ack to prevent redelivery (the retry queue owns the eventual write).
		// No frame here — a retry-enqueued or DLQ'd result did not (yet, or
		// ever) apply, so a frame built from `results` would describe state
		// that isn't true; the next live event or hydrate heals (§3.5).
		return substrate.Ack, nil
	}

	if scope.Frames() {
		p.emitPersonalFrames(ctx, adpt, enumeratedActors, results, msg.Sequence)
	}

	attrs := []any{"ruleId", p.ruleID, "entityId", key, "stage", "pipeline", "adapter", p.adapterName}
	if withheld > 0 {
		// On the line that already reports the event, because this is where an
		// operator asking "why did this event write so little" is looking.
		attrs = append(attrs, "entriesWithheld", withheld)
	}
	if scoped {
		// The scope this event published under, on the line that already
		// reports the event — an operator asking why a device did not receive a
		// row it expected is asking exactly this, and nothing else records it.
		// Carried as the value rather than its rendering, so slog formats the
		// set only when the line is actually emitted.
		attrs = append(attrs, "publishScope", scope)
	}
	slog.Info("pipeline: processed", attrs...)
	return substrate.Ack, nil
}

// replayWrite applies one captured EvalResult through the current adapter and
// reports whether a row actually landed, using the outcome-reporting call
// whenever the adapter offers one — the same preference, and the same
// treatment of an adapter that offers none, as writeResults' own write step.
//
// A retry replays the ORIGINAL projectionSeq, so a guarded target legitimately
// declines it once a later real reprojection has moved the watermark past it.
// That is the retry doing its job (the fresher row is already there), not a
// failure, so it returns cleanly and the entry retires — but nothing landed, so
// there is nothing to audit either.
func (p *Pipeline) replayWrite(ctx context.Context, result ruleengine.EvalResult) (committed bool, err error) {
	a := p.currentAdapter()
	p.recordProjectionWrite()
	if result.Delete {
		if od, ok := a.(adapter.OutcomeDeleter); ok {
			outcome, derr := od.DeleteWithOutcome(ctx, result.Keys, result.ProjectionSeq)
			return outcome.Wrote, derr
		}
		if derr := a.Delete(ctx, result.Keys, result.ProjectionSeq); derr != nil {
			return false, derr
		}
		return true, nil
	}
	if ou, ok := a.(adapter.OutcomeUpserter); ok {
		outcome, uerr := ou.UpsertWithOutcome(ctx, result.Keys, result.Row, result.ProjectionSeq)
		return outcome.Committed, uerr
	}
	if uerr := a.Upsert(ctx, result.Keys, result.Row, result.ProjectionSeq); uerr != nil {
		return false, uerr
	}
	return true, nil
}

// enqueueRetry constructs and enqueues a RetryEntry for a transient write
// failure, mirroring the inline retry-enqueue path in processMsg.
//
// A retried write that lands is a committed target write whose audit entry and
// freshness stamp writeResults never got to make, so this path makes them —
// under the same committed-only gate, since both describe a row that is
// actually stored, whichever path stored it.
func (p *Pipeline) enqueueRetry(key string, rawPayload []byte, result ruleengine.EvalResult) {
	capturedResult := result
	capturedReporter := p.reporter
	capturedSeq := ""
	if p.reporter != nil {
		if seq := p.reporter.ActiveSequence(); seq != 0 {
			capturedSeq = fmt.Sprintf("%d", seq)
		}
	}
	e := &failure.RetryEntry{
		RuleID:       p.ruleID,
		EntityID:     key,
		Stage:        "write",
		RawPayload:   rawPayload,
		RuleSequence: capturedSeq,
		WriteFn: func(rctx context.Context) error {
			committed, err := p.replayWrite(rctx, capturedResult)
			if err != nil {
				return err
			}
			if committed {
				p.recordProjected()
				// No pipeline: a replay writes one captured result, so there
				// is no loop to amortise an ack over.
				p.writeAudit(rctx, nil, key, capturedResult)
			}
			return nil
		},
		Attempt:     0,
		MaxAttempts: p.retryMaxAttempts,
		BaseBackoff: p.retryBaseBackoff,
		Conn:        p.retryConn,
		OnDLQPublished: func(rctx context.Context, errMsg string) {
			if capturedReporter != nil {
				if recErr := capturedReporter.RecordError(rctx, errMsg); recErr != nil {
					slog.Error("pipeline: update health errorCount after retry DLQ",
						"ruleId", p.ruleID, "err", recErr)
				}
			}
		},
	}
	p.retryQueue.Enqueue(e)
}

// enqueueActorReprojectRetry constructs and enqueues a RetryEntry for a
// perEntry lens's transient write failure, re-evaluating actorKey (via
// Reproject) on each attempt instead of replaying a captured raw write
// (§4.3 of cap-read-per-anchor-grant-keys-design.md — see the writeResults
// comment at the call site for why a raw replay is unsafe for per-anchor
// keys). Reuses the same RetryEntry queue/backoff/DLQ-escalation machinery
// as enqueueRetry; only the WriteFn's unit of work changes, from "the write"
// to "the actor".
func (p *Pipeline) enqueueActorReprojectRetry(rawPayload []byte, actorKey string) {
	capturedReporter := p.reporter
	capturedSeq := ""
	if p.reporter != nil {
		if seq := p.reporter.ActiveSequence(); seq != 0 {
			capturedSeq = fmt.Sprintf("%d", seq)
		}
	}
	e := &failure.RetryEntry{
		RuleID: p.ruleID,
		// actorKey, not key: key is the triggering entity (an actor's own
		// vertex, or a fan-out's aspect/link key) — actorKey is the thing
		// actually being retried, and the only identity worth logging or
		// escalating to the DLQ on exhaustion. A fan-out batch enqueues one
		// entry per actor, so each must name its OWN actor here or every
		// entry (and every DLQ message on exhaustion) reads identically and
		// names none of them.
		EntityID:     actorKey,
		Stage:        "write",
		RawPayload:   rawPayload,
		RuleSequence: capturedSeq,
		WriteFn: func(rctx context.Context) error {
			res, err := p.Reproject(rctx, actorKey)
			if err != nil {
				return err
			}
			if res.Verdict == VerdictBlocked {
				// The reconciliation ran and the ordering guard declined its
				// write, so the repair this entry owes has NOT been made —
				// retiring the entry here would report the owed repair as
				// delivered and leave nothing else looking at the row.
				//
				// Retrying is productive rather than a spin: the token is the
				// pipeline's last-applied sequence, which advances on every
				// acked event, so a later attempt carries a token that can
				// outrank the stored watermark. If the backoff is exhausted
				// first, the entry reaches the DLQ and records an error, which
				// is an honest terminal signal — and strictly better than the
				// silence it replaces.
				return fmt.Errorf("pipeline: actor-reproject %q: %s", actorKey, res.VerdictReason)
			}
			return nil
		},
		Attempt:     0,
		MaxAttempts: p.retryMaxAttempts,
		BaseBackoff: p.retryBaseBackoff,
		Conn:        p.retryConn,
		OnDLQPublished: func(rctx context.Context, errMsg string) {
			if capturedReporter != nil {
				if recErr := capturedReporter.RecordError(rctx, errMsg); recErr != nil {
					slog.Error("pipeline: update health errorCount after retry DLQ",
						"ruleId", p.ruleID, "err", recErr)
				}
			}
		},
	}
	p.retryQueue.Enqueue(e)
}

// publishTerminalDLQ publishes a DLQ message for an entity whose data is permanently
// unrecoverable (failure.CatTerminal). Uses p.retryConn — the same substrate connection set via
// SetRetryQueue. If p.retryConn == nil (no connection configured), logs and returns without
// panicking, mirroring RetryQueue.escalateToDLQ. rawBody is the message body
// stored as the DLQ rawPayload.
func (p *Pipeline) publishTerminalDLQ(ctx context.Context, rawBody []byte, entityID, stage string, origErr error) {
	if p.retryConn == nil {
		slog.Error("pipeline: terminal failure, no connection for DLQ — entity dropped",
			"ruleId", p.ruleID, "entityId", entityID,
			"stage", stage, "err", origErr)
		return
	}
	// Fill RuleSequence from the reporter's cached active sequence.
	// Only format when non-zero; zero means SetRuleSequence was never called (keeps "" sentinel).
	ruleSeq := ""
	if p.reporter != nil {
		if seq := p.reporter.ActiveSequence(); seq != 0 {
			ruleSeq = fmt.Sprintf("%d", seq)
		}
	}
	dlqMsg := failure.DLQMessage{
		RuleID:       p.ruleID,
		EntityID:     entityID,
		FailedStage:  stage,
		ErrorClass:   "TERMINAL",
		ErrorMessage: origErr.Error(),
		RetryCount:   0,
		RuleSequence: ruleSeq,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		RawPayload:   string(rawBody),
	}
	// Use WithoutCancel so a DLQ publish triggered during shutdown still completes.
	pubCtx := context.WithoutCancel(ctx)
	if err := failure.Publish(pubCtx, p.retryConn, p.ruleID, dlqMsg); err != nil {
		slog.Error("pipeline: terminal DLQ publish failed",
			"ruleId", p.ruleID, "entityId", entityID,
			"stage", stage, "err", err)
	} else if p.reporter != nil {
		// AC3: increment health KV error count after each DLQ write.
		if recErr := p.reporter.RecordError(pubCtx, origErr.Error()); recErr != nil {
			slog.Error("pipeline: update health errorCount after terminal DLQ",
				"ruleId", p.ruleID, "err", recErr)
		}
	}
}

// writeAudit appends an audit entry after a successful write, into pipe when
// the caller opened one. It is a no-op when auditWriter is nil (optional
// feature, AC6). Errors are logged as Warn — a failed audit entry must never
// interrupt message processing (the write already succeeded).
func (p *Pipeline) writeAudit(ctx context.Context, pipe *substrate.PublishPipeline, entityID string, result ruleengine.EvalResult) {
	if p.auditWriter == nil {
		return
	}
	op := "upsert"
	var row map[string]any
	if result.Delete {
		op = "delete"
	} else {
		row = result.Row
	}
	if err := p.auditWriter.WriteAuditPipelined(ctx, pipe, entityID, op, row); err != nil {
		if ctx.Err() == nil {
			slog.Warn("pipeline: audit write failed",
				"ruleId", p.ruleID, "entityId", entityID, "op", op, "err", err)
		}
	}
}

// flushRowPipeline awaits the store acks of a write loop's pipelined rows and
// logs a failure the way the loop logs a per-row write error, so the two read
// the same in a lens's log. A nil pipe (an adapter whose writes are not
// publishes) has nothing outstanding and returns nil.
func (p *Pipeline) flushRowPipeline(ctx context.Context, pipe *substrate.PublishPipeline, entityID string) error {
	if pipe == nil {
		return nil
	}
	if err := pipe.Flush(ctx); err != nil {
		slog.Error("pipeline: write flush",
			"ruleId", p.ruleID, "entityId", entityID,
			"stage", "write", "adapter", p.adapterName, "err", err)
		return err
	}
	return nil
}

// flushAuditPipeline awaits the audit entries of one writeResults call, whatever
// disposition it reached — it runs deferred, so an early return on a write
// failure still lands the entries for the rows that did commit before it. A
// failure is logged at Warn and goes no further: the audit trail is
// best-effort, and a forensic entry never decides a message's fate.
func (p *Pipeline) flushAuditPipeline(ctx context.Context, pipe *substrate.PublishPipeline, entityID string) {
	if pipe == nil {
		return
	}
	if err := pipe.Flush(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("pipeline: audit write failed",
			"ruleId", p.ruleID, "entityId", entityID, "err", err)
	}
}
