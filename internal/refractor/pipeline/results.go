package pipeline

import (
	"context"
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
func (p *Pipeline) writeResults(ctx context.Context, rs ruleState, msg substrate.Message, key string, results []ruleengine.EvalResult, enumeratedActors []string) (substrate.Decision, error) {
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
	var retryResults []ruleengine.EvalResult
	var terminalErrs []error
	transientActorRetry := false
	for i := range results {
		// Stamp the triggering CDC message's stream sequence as the monotonic
		// ordering token before any write. The retry-queue capture copies the
		// stamped result, so a replay carries this same (original, lower) seq,
		// which is exactly what must lose to a later real reprojection.
		results[i].ProjectionSeq = msg.Sequence
	}
	for _, result := range results {
		var writeErr error
		committed := true
		writtenKey := ""
		transition := adapter.TransitionNone
		if result.Delete {
			if reportsDeleteOutcome {
				var outcome adapter.DeleteOutcome
				outcome, writeErr = deleteOutcomeAdpt.DeleteWithOutcome(ctx, result.Keys, result.ProjectionSeq)
				committed, writtenKey, transition = outcome.Wrote, outcome.Key, outcome.Transition
			} else {
				writeErr = adpt.Delete(ctx, result.Keys, result.ProjectionSeq)
			}
		} else if reportsOutcome {
			var outcome adapter.UpsertOutcome
			outcome, writeErr = outcomeAdpt.UpsertWithOutcome(ctx, result.Keys, result.Row, result.ProjectionSeq)
			// Committed, not Wrote. A guarded adapter reports Wrote on every call
			// because maintaining the projectionSeq watermark is its job whatever
			// the row says, and it has more than one way to end up storing nothing
			// — a stale-or-equal watermark, and a write with no ordering token at
			// all. Committed states positively that a row landed, which is the
			// only claim an audit entry's outputRowHash can honestly stand behind
			// and the only event the read-model's freshness clock marks.
			committed, writtenKey, transition = outcome.Committed, outcome.Key, outcome.Transition
		} else {
			writeErr = adpt.Upsert(ctx, result.Keys, result.Row, result.ProjectionSeq)
		}

		if writeErr != nil {
			cat := failure.Classify(writeErr)
			op := "upsert"
			if result.Delete {
				op = "delete"
			}
			slog.Error("pipeline: "+op,
				"ruleId", p.ruleID, "entityId", key,
				"stage", "write", "adapter", p.adapterName, "err", writeErr)

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
			// The read model's freshness clock and its forensic trail both mark
			// the same event — a row landing in the target — so a write that
			// stored nothing marks neither. An operator reading lastProjectedAt
			// off a lens whose every write the guard is declining would otherwise
			// see a clock ticking over a target that has not changed in hours.
			p.recordProjected()
			p.writeAudit(ctx, key, result)
		}
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
		// here; this increment accepts the coarser, safe-but-costlier retry
		// unit and names the cost rather than silently absorbing it.
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

	p.emitPersonalFrames(ctx, adpt, enumeratedActors, results, msg.Sequence)

	slog.Info("pipeline: processed",
		"ruleId", p.ruleID, "entityId", key,
		"stage", "pipeline", "adapter", p.adapterName)
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
				p.writeAudit(rctx, key, capturedResult)
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

// writeAudit appends an audit entry after a successful write. It is a no-op when
// auditWriter is nil (optional feature, AC6). Errors are logged as Warn — a failed
// audit entry must never interrupt message processing (the write already succeeded).
func (p *Pipeline) writeAudit(ctx context.Context, entityID string, result ruleengine.EvalResult) {
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
	if err := p.auditWriter.WriteAudit(ctx, entityID, op, row); err != nil {
		if ctx.Err() == nil {
			slog.Warn("pipeline: audit write failed",
				"ruleId", p.ruleID, "entityId", entityID, "op", op, "err", err)
		}
	}
}
