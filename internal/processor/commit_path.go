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
	Conn         *substrate.Conn
	CoreBucket   string
	HealthKV     string
	Authorizer   Authorizer
	Hydrator     Hydrator
	Executor     Executor
	Validator    Validator
	Committer    Committer
	AckerFactory AckerFactory
	Metrics      *Metrics
	Heartbeater  *HealthHeartbeater
	Logger       *slog.Logger
	// Clock is the wall clock the commit path uses for tracker timestamps
	// and reply CommittedAt. Tests override it.
	Clock func() time.Time
	// DenialBuilder constructs FR22-structured denial details for auth-denied
	// rejections. Nil when capability mode is not active — denials fall back
	// to the minimal reply.
	DenialBuilder *DenialResponseBuilder
	// TraceEmitter writes three-plane auth trace records to Health KV per FR23.
	// Nil when not wired (stub mode). Fire-and-forget: the emitter launches a
	// goroutine so step 3 latency is unaffected.
	TraceEmitter *AuthTraceEmitter
	// ClaimEmitter records ClaimIdentity attempt outcomes to Health KV at
	// health.processor.<instance>.claim-attempts.<outcome>. Nil safe: a nil
	// ClaimEmitter silently skips emission.
	ClaimEmitter ClaimAttemptEmitter
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
		panic("processor: CommitPath requires Committer")
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
	OutcomeAccepted  MessageOutcome = "accepted"
	OutcomeDuplicate MessageOutcome = "duplicate"
	OutcomeRejected  MessageOutcome = "rejected"
	OutcomeMalformed MessageOutcome = "malformed"
	OutcomeRetryable MessageOutcome = "retryable" // transient failure → nak
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
		// Events were persisted atomically with the original commit (the outbox
		// aspect) and the durable outbox consumer owns publishing. A redelivery
		// therefore simply acks — there is nothing to re-derive or re-publish here.
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
		cp.replyTo(msg, BuildRejectedReply(env.RequestID, ErrCodeAuthInfrastructureFailure,
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
		// Enrich denial reply with FR22-structured fields when the DenialBuilder
		// is wired (capability mode). Stub mode leaves it nil → minimal reply.
		var denialDetails map[string]any
		if cp.deps.DenialBuilder != nil {
			dd := cp.deps.DenialBuilder.BuildDenialDetails(ctx, env, decision, decision.Doc)
			denialDetails = DenialDetailsAsMap(dd)
		}
		cp.replyTo(msg, BuildRejectedReply(env.RequestID, code, decision.Reason, denialDetails))
		// Emit FR23 three-plane auth trace record asynchronously.
		// Fire-and-forget — does not block step 3 path.
		cp.deps.TraceEmitter.Emit(env, decision)
		_ = msg.TermWithReason("auth denied: " + decision.Reason)
		return OutcomeRejected
	}
	// Capture the resolved permission so downstream steps receive auth provenance
	// without re-reading Capability KV. Allocated by CapabilityAuthorizer; nil
	// on the StubAuthorizer path.
	resolvedPermission := decision.Resolved
	cp.deps.Logger.Info("step 3: authorized",
		"requestId", env.RequestID, "stub", decision.Stub,
		"authPath", resolvedPermissionPath(resolvedPermission),
		"projectedAt", resolvedPermissionProjectedAt(resolvedPermission))
	// Emit FR23 auth trace for allowed decisions when flag is ON. The emitter
	// guards internally on traceAllowDecisions; nil emitter is a no-op.
	cp.deps.TraceEmitter.Emit(env, decision)

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

	// Reply-constraint, enforced BEFORE commit: a script-named primaryKey must
	// lie within the operation's write footprint (a mutation key, or the
	// 3-segment vertex root of one). The write path is not a read channel — a
	// script cannot surface a key it did not write. Rejecting here, ahead of the
	// atomic batch, guarantees a contract violation never mutates Core KV or
	// publishes events.
	if result.PrimaryKey != "" && !primaryKeyInCommit(result.PrimaryKey, result.Mutations) {
		cp.deps.Metrics.OpsRejected.Add(1)
		cp.deps.Logger.Warn("reply-constraint violation: response.primaryKey is not within the write footprint; rejecting before commit",
			"requestId", env.RequestID, "primaryKey", result.PrimaryKey)
		cp.replyTo(msg, BuildRejectedReply(env.RequestID, ErrCodeDDLViolation,
			"response.primaryKey is not within the committed write footprint",
			map[string]any{"primaryKey": result.PrimaryKey}))
		_ = msg.TermWithReason("DDLViolation: primaryKey not within write footprint")
		return OutcomeRejected
	}

	now := cp.deps.Clock()
	tracker := NewTracker(env, now)
	commitAck, err := cp.commitWithTaskAutoComplete(ctx, env, result, tracker, resolvedPermission)
	if err != nil {
		// Authoritative protected-key guard (Story 1.5.5 P1): an update or
		// tombstone targeting a data.protected root is rejected before the
		// atomic batch. Terminate (no redelivery) — a redelivery cannot
		// succeed since the world is unchanged.
		var protErr *ProtectedKeyError
		if errors.As(err, &protErr) {
			cp.deps.Metrics.OpsRejected.Add(1)
			cp.deps.Logger.Info("step 8: protected-key rejection",
				"requestId", env.RequestID,
				"key", protErr.Key,
				"root", protErr.Root,
				"op", protErr.Op)
			cp.replyTo(msg, BuildRejectedReply(env.RequestID, ErrCodeProtectedKey,
				protErr.Error(), map[string]any{
					"key":  protErr.Key,
					"root": protErr.Root,
					"op":   protErr.Op,
				}))
			_ = msg.TermWithReason("ProtectedKey: " + protErr.Root)
			return OutcomeRejected
		}
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

	// Event publication is outbox-only: the faithful EventList was persisted in
	// the step-8 atomic batch (vtx.op.<id>.events) and the durable outbox
	// consumer publishes it to `core-events`, acking only after a confirmed
	// publish. There is no in-commit publish — this eliminates the double-publish
	// race and guarantees redelivery republishes the REAL events, never a
	// reconstruction.

	cp.deps.Metrics.OpsCommitted.Add(1)
	// Emit claim-attempt success for ClaimIdentity ops only.
	if env.OperationType == "ClaimIdentity" && cp.deps.ClaimEmitter != nil {
		cp.deps.ClaimEmitter.RecordClaimAttempt(ctx, "success")
	}
	// Surface the validated principal primaryKey + per-key revisions in the
	// success reply. PrimaryKey is empty for multi-key ops (clients read the
	// committed key set from Revisions).
	cp.replyTo(msg, BuildAcceptedReplyWithRevisions(env.RequestID, now, result.PrimaryKey, commitAck.Revisions))

	// --- Step 9: explicit Acker boundary. ---
	acker := cp.deps.AckerFactory(msg, cp.deps.Logger)
	if ackErr := acker.Ack(ctx); ackErr != nil {
		cp.deps.Logger.Warn("step 9: ack failed", "requestId", env.RequestID, "error", ackErr)
		// Ack failure: JetStream will redeliver; tracker short-circuits.
		// We still consider the operation accepted from the caller's
		// perspective because step 8 was durable + reply was sent.
		return OutcomeAccepted
	}
	cp.deps.Logger.Info("step 9: ack", "requestId", env.RequestID)
	return OutcomeAccepted
}

// commitWithTaskAutoComplete commits the operation, injecting the §10.6
// task auto-completion into the SAME atomic batch when step-3 resolved on the
// task path (Contract #10 §10.7) and the named task is currently open.
//
// Seam (a) (Adjudication #1): the conditional status→complete mutation + the
// TaskCompleted event are appended to a copy of the ScriptResult before
// Committer.Commit, so the existing batch builder, BuildEventList, and the
// transactional outbox carry them unchanged — one atomic batch holds both the
// op's own effect and the task closure, or neither.
//
// CAS-on-open + best-effort (Adjudication #2): the completion mutation carries
// the task root's read revision. If the commit loses an OCC race on that
// injected update (a concurrent admin CompleteTask/CancelTask moved the root),
// re-read the task: if it is no longer open, drop the injection and commit the
// user's op ALONE. A task-side race the user did not cause MUST NOT fail the
// user's op — this never surfaces as RevisionConflict for the auto-complete.
// A conflict on one of the user's OWN mutations is returned unchanged.
func (cp *CommitPath) commitWithTaskAutoComplete(
	ctx context.Context,
	env *OperationEnvelope,
	result ScriptResult,
	tracker Tracker,
	rp *ResolvedPermission,
) (CommitAck, error) {
	taskKey := taskKeyFromTaskPathDecision(rp)
	if taskKey == "" {
		// Not a task-path op (role/service/platform auth, or stub) — nothing
		// to auto-complete. Commit the op as-is.
		return cp.deps.Committer.Commit(ctx, env, result, tracker)
	}

	ac, err := readTaskAutoCompletion(ctx, cp.deps.Conn, cp.deps.CoreBucket, taskKey)
	if err != nil {
		// A read failure on the task root must not fail the user's op on a
		// best-effort closure. Log and commit the op alone; a redelivery (or a
		// later op on the same grant) re-attempts the closure, and the
		// CAS-on-open keeps that idempotent.
		cp.deps.Logger.Warn("auto-complete: task root read failed; committing op without closure",
			"requestId", env.RequestID, "taskKey", taskKey, "error", err)
		return cp.deps.Committer.Commit(ctx, env, result, tracker)
	}
	if !ac.open {
		// Task absent / cancelled / already complete → inject nothing (no
		// double-complete, no cancelled-resurrection; the stale-grant window is
		// a harmless no-op).
		return cp.deps.Committer.Commit(ctx, env, result, tracker)
	}

	// The completion of a task whose granted op just ran is a platform behaviour
	// (Contract #10 §10.6), injected on the commit path — not script-authored.
	// The seam is auditable here in the log, NOT via a marker on the persisted
	// root data or event payload (those stay byte-for-byte identical to the
	// explicit CompleteTask shape so Loom consumes one shape).
	cp.deps.Logger.Info("auto-complete: injecting task closure into the atomic batch",
		"requestId", env.RequestID, "taskKey", taskKey, "autoComplete", true)
	augmented := injectTaskAutoCompletion(result, ac)
	commitAck, err := cp.deps.Committer.Commit(ctx, env, augmented, tracker)
	if err == nil {
		return commitAck, nil
	}
	if !errors.Is(err, substrate.ErrAtomicBatchRejected) {
		// Non-conflict commit failure (e.g. protected-key, infra) — propagate
		// unchanged; the injection did not introduce it.
		return commitAck, err
	}

	// An atomic-batch conflict occurred. Re-read the task to attribute it. The
	// only guarded mutation the injection adds is the task update at
	// ac.revision; if the root has since moved (or closed), the injected CAS is
	// a cause of the conflict — re-resolve the closure so the user's op is
	// never bounced on a task-side race (Adjudication #2).
	recheck, rerr := readTaskAutoCompletion(ctx, cp.deps.Conn, cp.deps.CoreBucket, taskKey)
	if rerr != nil {
		return commitAck, err
	}
	if recheck.open && recheck.revision == ac.revision {
		// The task root is untouched at the revision we asserted — the conflict
		// is on one of the USER's own mutations, not the auto-complete. Surface
		// it unchanged (the existing RevisionConflict branch handles it).
		return commitAck, err
	}
	if recheck.open {
		// Still open but at a newer revision — retry the injection once with the
		// fresh CAS handle.
		retry := injectTaskAutoCompletion(result, recheck)
		retryAck, retryErr := cp.deps.Committer.Commit(ctx, env, retry, tracker)
		if retryErr == nil {
			return retryAck, nil
		}
		// Lost the race again (or a user-mutation conflict surfaced under the
		// retry) — fall through and commit the op alone so the user's op is
		// never bounced on a task-side race.
	}
	cp.deps.Logger.Info("auto-complete: task closed or moved under a concurrent transition; committing op without closure",
		"requestId", env.RequestID, "taskKey", taskKey)
	return cp.deps.Committer.Commit(ctx, env, result, tracker)
}

// primaryKeyInCommit reports whether a script-named primaryKey lies within the
// operation's write footprint: it must be a mutation key, or the 3-segment
// vertex root of a mutation key (the vertex an op attached an aspect to). This
// keeps primaryKey from surfacing any entity the op did not write — the write
// path is not a read channel — while letting aspect-only updates name their
// principal vertex rather than an internal aspect. It operates on the mutation
// set (known pre-commit), so the check can reject ahead of the atomic batch.
func primaryKeyInCommit(primaryKey string, mutations []MutationOp) bool {
	if primaryKey == "" {
		return false
	}
	for _, m := range mutations {
		// protectedRootKey returns "" for non-vertex keys (e.g. links); the
		// non-empty primaryKey above guarantees that never spuriously matches.
		if m.Key == primaryKey || protectedRootKey(m.Key) == primaryKey {
			return true
		}
	}
	return false
}

// resolvedPermissionPath returns rp.Path or "stub" / "none" for log fields.
func resolvedPermissionPath(rp *ResolvedPermission) string {
	if rp == nil {
		return "stub-or-none"
	}
	return rp.Path
}

// resolvedPermissionProjectedAt returns rp.ProjectedAt or "" for log fields.
func resolvedPermissionProjectedAt(rp *ResolvedPermission) string {
	if rp == nil {
		return ""
	}
	return rp.ProjectedAt
}

func (cp *CommitPath) handleStubFailure(ctx context.Context, msg jetstream.Msg, env *OperationEnvelope, step string, err error) MessageOutcome {
	cp.deps.Metrics.OpsRejected.Add(1)
	cp.deps.Logger.Warn("step returned error",
		"step", step, "requestId", env.RequestID, "error", err)

	// Emit specific ClaimKeyInvalid outcome to Health KV (NFR-S6 anti-enumeration:
	// the reply carries only the generic code with no detail).
	var sErr *ScriptError
	if errors.As(err, &sErr) && sErr.Code == "ClaimKeyInvalid" {
		if cp.deps.ClaimEmitter != nil {
			cp.deps.ClaimEmitter.RecordClaimAttempt(ctx, sErr.Detail)
		}
	}

	code, details := classifyStepError(err)
	cp.replyTo(msg, BuildRejectedReply(env.RequestID, code,
		fmt.Sprintf("step %s failed: %s", step, err.Error()), details))
	_ = msg.TermWithReason(string(code) + ": " + err.Error())
	return OutcomeRejected
}

// classifyStepError maps a typed step-4/5 error onto the wire-shape
// ErrorCode plus a details map. Falls back to InternalError for
// untyped failures.
//
// ClaimKeyInvalid (NFR-S6): returns ErrCodeClaimKeyInvalid with no detail so
// callers cannot enumerate specific failure reasons. The Detail field on
// *ScriptError is the internal side-channel for Health KV emission only; it
// must NOT appear in the response details map.
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
		// ClaimKeyInvalid: generic rejection with no detail for anti-enumeration.
		if sErr.Code == "ClaimKeyInvalid" {
			return ErrCodeClaimKeyInvalid, nil
		}
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

// replySubject returns the subject to publish replies to for a delivered
// JetStream message. JetStream pull consumers rewrite msg.Reply() to the
// stream's ACK subject; callers that want a direct reply carry their inbox
// in the Lattice-Reply-Inbox header, which takes precedence.
func replySubject(msg jetstream.Msg) string {
	if hdr := msg.Headers(); hdr != nil {
		if inbox := hdr.Get("Lattice-Reply-Inbox"); inbox != "" {
			return inbox
		}
	}
	return msg.Reply()
}

// replyTo publishes a reply envelope to the caller's reply subject.
// Errors are logged (the commit is already durable, so failure to reply
// is observability-only).
func (cp *CommitPath) replyTo(msg jetstream.Msg, reply OperationReply) {
	subject := replySubject(msg)
	if subject == "" {
		return
	}
	b, err := MarshalReply(reply)
	if err != nil {
		cp.deps.Logger.Warn("reply marshal failed", "error", err)
		return
	}
	if err := cp.deps.Conn.NATS().Publish(subject, b); err != nil {
		cp.deps.Logger.Warn("reply publish failed", "subject", subject, "error", err)
	}
}

func (cp *CommitPath) maybeReplyMalformed(msg jetstream.Msg, requestID, reason string) {
	subject := replySubject(msg)
	if subject == "" {
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
	_ = cp.deps.Conn.NATS().Publish(subject, b)
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

// MakeStubPipeline is a convenience wrapper over MakePipeline for callers that
// do not need capability-auth (capabilityBucket="", traceAllowDecisions=false).
// The "stub" in the name refers to the absent Capability KV integration, not
// to stub implementations — all other components (Hydrator, Executor, Validator,
// Committer, EventPublisher) are production-identical.
func MakeStubPipeline(conn *substrate.Conn, coreBucket, healthBucket string, authMode AuthMode, logger *slog.Logger, instance string) (*CommitPath, *HealthHeartbeater, error) {
	return MakePipeline(conn, coreBucket, healthBucket, "", authMode, false, logger, instance)
}

// MakePipeline is the production wiring entry point. capabilityBucket is the
// Capability KV bucket name; empty falls back to AuthModeStub regardless of
// the requested mode (test-friendly default). authMode is applied via
// SelectAuthorizerArgs so the Capability KV reader + alert emitter are wired
// in one place.
//
// traceAllowDecisions (FR23): when true, auth trace records are also written
// for ALLOWED decisions. Defaults off — volume implications for busy deployments.
func MakePipeline(conn *substrate.Conn, coreBucket, healthBucket, capabilityBucket string, authMode AuthMode, traceAllowDecisions bool, logger *slog.Logger, instance string) (*CommitPath, *HealthHeartbeater, error) {
	metrics := &Metrics{}
	hb := NewHealthHeartbeater(conn, healthBucket, instance, 10*time.Second, metrics, logger)
	alertEmitter := NewHealthAlertEmitter(conn, healthBucket, logger)

	// Capability mode requires the bucket name. When capabilityBucket="" we fall
	// back to stub so test fixtures work without a Capability KV seed.
	effectiveMode := authMode
	if (authMode == AuthModeCapability || authMode == "") && capabilityBucket == "" {
		effectiveMode = AuthModeStub
	}

	authz, err := SelectAuthorizerArgs(SelectAuthorizerOpts{
		Mode:             effectiveMode,
		Logger:           logger,
		Reader:           conn,
		CapabilityBucket: capabilityBucket,
		Emitter:          alertEmitter,
	})
	if err != nil {
		return nil, nil, err
	}

	// Wire the heartbeat's per-tick capability-auth signals when the real
	// authorizer is active.
	if ca, ok := authz.(*CapabilityAuthorizer); ok {
		hb.AttachCapabilityAuthorizer(ca)
	}

	// Build the DDL cache from a full scan of Core KV's `vtx.meta.>`.
	ddls := NewDDLCache(conn, coreBucket, logger)
	if err := ddls.Refresh(context.Background()); err != nil {
		return nil, nil, fmt.Errorf("ddl cache refresh: %w", err)
	}

	// Wire DenialResponseBuilder (FR22) when capability mode is active.
	var denialBuilder *DenialResponseBuilder
	if capabilityBucket != "" {
		denialBuilder = NewDenialResponseBuilder(conn, capabilityBucket, logger)
	}

	// Wire AuthTraceEmitter (FR23) when health bucket is available. Nil emitter
	// is a no-op (safe to call Emit on nil *AuthTraceEmitter per the guard in Emit).
	var traceEmitter *AuthTraceEmitter
	if healthBucket != "" && instance != "" {
		traceEmitter = NewAuthTraceEmitter(conn, healthBucket, instance, traceAllowDecisions, logger)
	}

	// Wire claim attempt emitter for ClaimIdentity Health KV signals.
	claimEmitter := NewClaimAttemptEmitter(conn, healthBucket, instance, logger)

	committer := NewCommitter(conn, coreBucket, ddls, logger, time.Now)
	cp := NewCommitPath(Deps{
		Conn:          conn,
		CoreBucket:    coreBucket,
		HealthKV:      healthBucket,
		Authorizer:    authz,
		Hydrator:      NewHydratorWithCache(conn, coreBucket, ddls, logger),
		Executor:      NewExecutor(NewStarlarkRunner(0, 0), logger),
		Validator:     NewValidator(ddls, logger),
		Committer:     committer,
		Metrics:       metrics,
		Heartbeater:   hb,
		Logger:        logger,
		DenialBuilder: denialBuilder,
		TraceEmitter:  traceEmitter,
		ClaimEmitter:  claimEmitter,
	})
	return cp, hb, nil
}
