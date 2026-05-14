package processor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/substrate"
)

// Deps bundles the dependencies the commit path needs. The fields are
// public so test code can substitute fakes (e.g., a controlled Clock or
// an in-process Committer that fails the first call to exercise the
// retry-safe assertion).
type Deps struct {
	Conn        *substrate.Conn
	CoreBucket  string
	HealthKV    string
	Authorizer  Authorizer
	Hydrator    Hydrator
	Executor    Executor
	Validator   Validator
	Committer   Committer
	Events      EventPublisher
	AckerFactory AckerFactory
	Metrics     *Metrics
	Heartbeater *HealthHeartbeater
	Logger      *slog.Logger
	// Clock is the wall clock the commit path uses for tracker timestamps
	// and reply CommittedAt. Tests override it.
	Clock func() time.Time
}

// CommitPath drives steps 1-3 (and stubbed 4-10) for a single envelope.
type CommitPath struct {
	deps Deps
}

// NewCommitPath constructs the driver. Required fields must be non-nil;
// missing fields panic to surface programmer errors early.
func NewCommitPath(deps Deps) *CommitPath {
	if deps.Conn == nil {
		panic("processor: CommitPath requires Conn")
	}
	if deps.CoreBucket == "" {
		panic("processor: CommitPath requires CoreBucket")
	}
	if deps.Authorizer == nil {
		panic("processor: CommitPath requires Authorizer")
	}
	if deps.Committer == nil {
		panic("processor: CommitPath requires Committer (StubCommitter is fine for Story 1.5)")
	}
	if deps.Metrics == nil {
		deps.Metrics = &Metrics{}
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Clock == nil {
		deps.Clock = time.Now
	}
	if deps.AckerFactory == nil {
		deps.AckerFactory = DefaultAckerFactory
	}
	return &CommitPath{deps: deps}
}

// HandleMessage runs the commit path against a single JetStream-delivered
// message. The returned MessageOutcome is purely informational — the
// commit path itself decides ack/nack/term against the message internally.
// Returning the outcome lets tests assert on which branch fired.
type MessageOutcome string

const (
	OutcomeAccepted   MessageOutcome = "accepted"
	OutcomeDuplicate  MessageOutcome = "duplicate"
	OutcomeRejected   MessageOutcome = "rejected"
	OutcomeMalformed  MessageOutcome = "malformed"
	OutcomeRetryable  MessageOutcome = "retryable" // transient failure → nak
)

// HandleMessage executes steps 1-3 then the stubbed 4-10. It is the
// single function tests + Run both call.
func (cp *CommitPath) HandleMessage(ctx context.Context, msg jetstream.Msg) MessageOutcome {
	cp.deps.Metrics.OpsConsumed.Add(1)

	// --- Step 1: consume + parse envelope. ---
	env, err := parseEnvelopeFromMessage(msg)
	if err != nil {
		cp.deps.Metrics.OpsMalformed.Add(1)
		reason := err.Error()
		// Best-effort requestId recovery for health-marker keying.
		rid := extractRequestIDBestEffort(msg.Data())
		cp.deps.Logger.Warn("MalformedOperation: terminating with term=true",
			"reason", reason, "recoveredRequestId", rid)
		if cp.deps.Heartbeater != nil {
			cp.deps.Heartbeater.EmitMalformedOperation(ctx, rid, reason)
		}
		// Reply with EnvelopeMalformed if the message carried a reply
		// inbox — clients that submitted via request-reply get
		// notified; fire-and-forget publishers get nothing (the
		// MalformedOperation health marker is the trail).
		cp.maybeReplyMalformed(msg, rid, reason)
		if termErr := msg.TermWithReason("malformed envelope: " + reason); termErr != nil {
			cp.deps.Logger.Warn("term-with-reason failed; falling back to term",
				"error", termErr)
			_ = msg.Term()
		}
		return OutcomeMalformed
	}
	cp.deps.Logger.Info("step 1: envelope parsed",
		"requestId", env.RequestID,
		"lane", string(env.Lane),
		"operationType", env.OperationType,
		"actor", env.Actor)

	// --- Step 2: dedup. ---
	dedup, err := CheckDedup(ctx, cp.deps.Conn, cp.deps.CoreBucket, env.RequestID)
	if err != nil {
		cp.deps.Logger.Warn("step 2: dedup lookup failed; nak for redelivery",
			"requestId", env.RequestID, "error", err)
		_ = msg.Nak()
		return OutcomeRetryable
	}
	if dedup.Outcome == DedupDuplicate {
		cp.deps.Metrics.OpsDuplicates.Add(1)
		cp.deps.Logger.Info("DuplicateDetected: short-circuit at step 2",
			"requestId", env.RequestID,
			"trackerKey", TrackerKey(env.RequestID))
		cp.replyTo(msg, BuildDuplicateReply(env.RequestID, dedup.Tracker))
		if ackErr := msg.Ack(); ackErr != nil {
			cp.deps.Logger.Warn("ack on duplicate failed", "error", ackErr)
		}
		return OutcomeDuplicate
	}

	// --- Step 3: auth (stub). ---
	decision, err := cp.deps.Authorizer.Authorize(ctx, env)
	if err != nil {
		cp.deps.Metrics.OpsRejected.Add(1)
		cp.deps.Logger.Warn("step 3: authorizer error; rejecting",
			"requestId", env.RequestID, "error", err)
		cp.replyTo(msg, BuildRejectedReply(env.RequestID, ErrCodeInternalError,
			"authorizer error", map[string]any{"underlying": err.Error()}))
		_ = msg.TermWithReason("authorizer error: " + err.Error())
		return OutcomeRejected
	}
	if !decision.Authorized {
		cp.deps.Metrics.OpsRejected.Add(1)
		code := decision.Code
		if code == "" {
			code = ErrCodeAuthDenied
		}
		cp.deps.Logger.Info("step 3: authorization denied; rejecting",
			"requestId", env.RequestID, "code", string(code), "reason", decision.Reason)
		cp.replyTo(msg, BuildRejectedReply(env.RequestID, code, decision.Reason, nil))
		_ = msg.TermWithReason("auth denied: " + decision.Reason)
		return OutcomeRejected
	}
	cp.deps.Logger.Info("step 3: authorized",
		"requestId", env.RequestID, "stub", decision.Stub)

	// --- Steps 4-10: stubbed pipeline. ---
	var state HydratedState
	if cp.deps.Hydrator != nil {
		state, err = cp.deps.Hydrator.Hydrate(ctx, env)
		if err != nil {
			return cp.handleStubFailure(ctx, msg, env, "hydrate", err)
		}
	}
	var result ScriptResult
	if cp.deps.Executor != nil {
		result, err = cp.deps.Executor.Execute(ctx, env, state)
		if err != nil {
			return cp.handleStubFailure(ctx, msg, env, "execute", err)
		}
	}
	if cp.deps.Validator != nil {
		if err := cp.deps.Validator.Validate(ctx, env, result); err != nil {
			// DDL violations terminate the commit path (no redelivery).
			var ddlErr *DDLViolation
			if errors.As(err, &ddlErr) {
				cp.deps.Metrics.OpsRejected.Add(1)
				cp.deps.Logger.Info("step 6: DDL violation; rejecting",
					"requestId", env.RequestID,
					"constraint", ddlErr.ViolatedConstraint,
					"mutationKey", ddlErr.MutationKey,
					"detail", ddlErr.Detail)
				cp.replyTo(msg, BuildRejectedReply(env.RequestID, ErrCodeDDLViolation,
					ddlErr.Error(), map[string]any{
						"constraint":  ddlErr.ViolatedConstraint,
						"mutationKey": ddlErr.MutationKey,
					}))
				_ = msg.TermWithReason("DDLViolation: " + ddlErr.Detail)
				return OutcomeRejected
			}
			return cp.handleStubFailure(ctx, msg, env, "validate", err)
		}
	}

	now := cp.deps.Clock()
	tracker := NewTracker(env, now)
	if _, err := cp.deps.Committer.Commit(ctx, env, result, tracker); err != nil {
		// If the commit failed because the tracker already exists, a
		// previous redelivery committed and we're racing with our own
		// idempotency: ack and emit a duplicate reply.
		if errors.Is(err, substrate.ErrAtomicBatchRejected) {
			// Re-probe the tracker to confirm it was the cause.
			if probe, perr := CheckDedup(ctx, cp.deps.Conn, cp.deps.CoreBucket, env.RequestID); perr == nil && probe.Outcome == DedupDuplicate {
				cp.deps.Metrics.OpsDuplicates.Add(1)
				cp.deps.Logger.Info("commit: tracker already exists (concurrent redelivery); ack + duplicate reply",
					"requestId", env.RequestID)
				cp.replyTo(msg, BuildDuplicateReply(env.RequestID, probe.Tracker))
				_ = msg.Ack()
				return OutcomeDuplicate
			}
			// Tracker not present — genuine revision conflict on one of
			// the business mutations. Surface as RevisionConflict
			// (Contract #2 §2.6) and terminate (script's assertion of
			// the world disagrees with reality).
			var confErr *ConflictError
			if errors.As(err, &confErr) {
				cp.deps.Metrics.OpsRejected.Add(1)
				cp.deps.Logger.Info("step 8: revision conflict; rejecting",
					"requestId", env.RequestID,
					"conflictingKey", confErr.ConflictingKey)
				cp.replyTo(msg, BuildRejectedReply(env.RequestID, ErrCodeRevisionConflict,
					confErr.Error(), map[string]any{
						"conflictingKey": confErr.ConflictingKey,
					}))
				_ = msg.TermWithReason("RevisionConflict")
				return OutcomeRejected
			}
		}
		// Genuine commit failure → nak so JetStream redelivers. Because
		// the tracker is only created on a successful atomic batch, a
		// failed commit leaves the world in the pre-commit state and
		// redelivery is safe (Contract #4 §4.4).
		cp.deps.Logger.Warn("step 8: commit failed; nak for redelivery",
			"requestId", env.RequestID, "error", err)
		_ = msg.Nak()
		return OutcomeRetryable
	}

	// --- Step 9: event publication. ---
	// Commit (step 8) is durable. If step 9 fails after retries, we
	// nak so JetStream redelivers; step 2's tracker short-circuit makes
	// the redelivered attempt a no-op-from-the-mutation-perspective and
	// step 9 will run again from a clean state. The reply for the
	// caller is deferred until step 10 succeeds — Contract #2 §2.4
	// anchors durability at step 8, but Story 1.8 confirms reply is
	// still emitted after step 9 completes successfully (the architect's
	// decision was: reply after commit but observable success of the
	// whole 10-step path requires step 9 too).
	if cp.deps.Events != nil {
		if err := cp.deps.Events.Publish(ctx, env, result); err != nil {
			cp.deps.Logger.Warn("step 9: event publish failed (commit already durable); nak for redelivery",
				"requestId", env.RequestID, "error", err)
			_ = msg.Nak()
			return OutcomeRetryable
		}
	}

	cp.deps.Metrics.OpsCommitted.Add(1)
	cp.replyTo(msg, BuildAcceptedReply(env.RequestID, now))

	// --- Step 10: explicit Acker boundary. ---
	acker := cp.deps.AckerFactory(msg, cp.deps.Logger)
	if ackErr := acker.Ack(ctx); ackErr != nil {
		cp.deps.Logger.Warn("step 10: ack failed", "requestId", env.RequestID, "error", ackErr)
		// Ack failure: JetStream will redeliver; tracker short-circuits.
		// We still consider the operation accepted from the caller's
		// perspective because step 8 was durable + reply was sent.
		return OutcomeAccepted
	}
	cp.deps.Logger.Info("step 10: ack", "requestId", env.RequestID)
	return OutcomeAccepted
}

func (cp *CommitPath) handleStubFailure(_ context.Context, msg jetstream.Msg, env *OperationEnvelope, step string, err error) MessageOutcome {
	cp.deps.Metrics.OpsRejected.Add(1)
	cp.deps.Logger.Warn("step returned error",
		"step", step, "requestId", env.RequestID, "error", err)

	code, details := classifyStepError(err)
	cp.replyTo(msg, BuildRejectedReply(env.RequestID, code,
		fmt.Sprintf("step %s failed: %s", step, err.Error()), details))
	_ = msg.TermWithReason(string(code) + ": " + err.Error())
	return OutcomeRejected
}

// classifyStepError maps a typed step-4/5 error onto the wire-shape
// ErrorCode plus a details map. Falls back to InternalError for
// untyped failures.
func classifyStepError(err error) (ErrorCode, map[string]any) {
	var hErr *HydrationError
	if errors.As(err, &hErr) {
		return ErrCodeHydrationFailed, map[string]any{
			"code":       hErr.Code,
			"missingKey": hErr.MissingKey,
		}
	}
	var sErr *ScriptError
	if errors.As(err, &sErr) {
		d := map[string]any{
			"code":    sErr.Code,
			"message": sErr.Message,
		}
		if sErr.Line > 0 {
			d["line"] = sErr.Line
			d["column"] = sErr.Column
		}
		return ErrCodeScriptFailed, d
	}
	return ErrCodeInternalError, nil
}

// replyTo publishes a reply envelope on msg.Reply() if a reply subject
// was provided. Errors are logged (the commit is already durable, so
// failure to reply is observability-only).
func (cp *CommitPath) replyTo(msg jetstream.Msg, reply OperationReply) {
	if msg.Reply() == "" {
		return
	}
	b, err := MarshalReply(reply)
	if err != nil {
		cp.deps.Logger.Warn("reply marshal failed", "error", err)
		return
	}
	if err := cp.deps.Conn.NATS().Publish(msg.Reply(), b); err != nil {
		cp.deps.Logger.Warn("reply publish failed", "subject", msg.Reply(), "error", err)
	}
}

func (cp *CommitPath) maybeReplyMalformed(msg jetstream.Msg, requestID, reason string) {
	if msg.Reply() == "" {
		return
	}
	rid := requestID
	r := OperationReply{
		RequestID: rid,
		Status:    ReplyStatusRejected,
		Error: &ReplyError{
			Code:    ErrCodeEnvelopeMalformed,
			Message: reason,
		},
	}
	b, err := MarshalReply(r)
	if err != nil {
		return
	}
	_ = cp.deps.Conn.NATS().Publish(msg.Reply(), b)
}

// Run drives a Consume loop until ctx is cancelled. The callback wires
// each delivered message through HandleMessage. Errors from Consume
// itself are logged; the caller decides when to stop the consumer.
func (cp *CommitPath) Run(ctx context.Context, cons jetstream.Consumer) error {
	cc, err := cons.Consume(func(m jetstream.Msg) {
		cp.HandleMessage(ctx, m)
	})
	if err != nil {
		return fmt.Errorf("processor: start Consume: %w", err)
	}
	defer cc.Stop()

	<-ctx.Done()
	return nil
}

// MakeStubPipeline assembles a complete Story-1.7 commit path with the
// real Hydrator, Executor, Validator, and Committer wired against a
// DDL cache built at startup. The event publisher remains stubbed
// (Story 1.8). It exists so cmd/processor/main.go and the integration
// tests share identical wiring.
//
// The function name is retained for backwards compatibility with Story
// 1.5's call sites; "stub" now refers only to the EventPublisher.
func MakeStubPipeline(conn *substrate.Conn, coreBucket, healthBucket string, authMode AuthMode, logger *slog.Logger, instance string) (*CommitPath, *HealthHeartbeater, error) {
	authz, err := SelectAuthorizer(authMode, logger)
	if err != nil {
		return nil, nil, err
	}
	metrics := &Metrics{}
	hb := NewHealthHeartbeater(conn, healthBucket, instance, 10*time.Second, metrics, logger)

	// Build the DDL cache from a full scan of Core KV's `vtx.meta.>`.
	ddls := NewDDLCache(conn, coreBucket, logger)
	if err := ddls.Refresh(context.Background()); err != nil {
		return nil, nil, fmt.Errorf("ddl cache refresh: %w", err)
	}

	committer := NewCommitter(conn, coreBucket, ddls, logger, time.Now)
	cp := NewCommitPath(Deps{
		Conn:        conn,
		CoreBucket:  coreBucket,
		HealthKV:    healthBucket,
		Authorizer:  authz,
		Hydrator:    NewHydratorWithCache(conn, coreBucket, ddls, logger),
		Executor:    NewExecutor(NewStarlarkRunner(0, 0), logger),
		Validator:   NewValidator(ddls, logger),
		Committer:   committer,
		Events:      NewEventPublisher(conn, logger),
		Metrics:     metrics,
		Heartbeater: hb,
		Logger:      logger,
	})
	return cp, hb, nil
}

