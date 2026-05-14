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

	if cp.deps.Events != nil {
		if err := cp.deps.Events.Publish(ctx, env, result); err != nil {
			// Events failed but commit succeeded — tracker exists; on
			// redelivery, step 2 will short-circuit. Log and proceed to
			// ack so we don't reprocess. (Story 1.8 hardens this.)
			cp.deps.Logger.Warn("step 9: event publish failed (commit already durable)",
				"requestId", env.RequestID, "error", err)
		}
	}

	cp.deps.Metrics.OpsCommitted.Add(1)
	cp.replyTo(msg, BuildAcceptedReply(env.RequestID, now))
	if ackErr := msg.Ack(); ackErr != nil {
		cp.deps.Logger.Warn("step 10: ack failed", "requestId", env.RequestID, "error", ackErr)
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

// MakeStubPipeline assembles a complete Story-1.5 commit path with stub
// downstream steps. It exists so cmd/processor/main.go and the
// integration tests share identical wiring.
func MakeStubPipeline(conn *substrate.Conn, coreBucket, healthBucket string, authMode AuthMode, logger *slog.Logger, instance string) (*CommitPath, *HealthHeartbeater, error) {
	authz, err := SelectAuthorizer(authMode, logger)
	if err != nil {
		return nil, nil, err
	}
	metrics := &Metrics{}
	hb := NewHealthHeartbeater(conn, healthBucket, instance, 10*time.Second, metrics, logger)
	committer := NewStubCommitter(conn, coreBucket, logger, time.Now)
	cp := NewCommitPath(Deps{
		Conn:        conn,
		CoreBucket:  coreBucket,
		HealthKV:    healthBucket,
		Authorizer:  authz,
		Hydrator:    NewHydrator(conn, coreBucket, logger),
		Executor:    NewExecutor(NewStarlarkRunner(0, 0), logger),
		Validator:   &StubValidator{logger: logger},
		Committer:   committer,
		Events:      &StubEventPublisher{logger: logger},
		Metrics:     metrics,
		Heartbeater: hb,
		Logger:      logger,
	})
	return cp, hb, nil
}

