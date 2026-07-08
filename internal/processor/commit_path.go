package processor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/vault"
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
	// ConflictEmitter surfaces same-key commit conflicts (the §3.2 OCC
	// lane-misassignment signal) to Health KV. Nil → no-op (defaulted in
	// NewCommitPath).
	ConflictEmitter CommitConflictEmitter
	// MaxCommitAttempts bounds the Processor-internal retry on a retryable
	// revision conflict (re-hydrate → re-execute → re-commit). 0 → default (3).
	MaxCommitAttempts int
	// CommitRetryBackoff returns the pause before retry attempt n (1-based: n=1 is
	// the first retry). Capped well under the lane deadline. Nil → a small
	// linear default; tests set it to a zero function for determinism.
	CommitRetryBackoff func(attempt int) time.Duration
	// Vault performs per-identity crypto for sensitive aspects at commit-path
	// step 6.5 (Contract #3 §3.10). Nil disables the stage: sensitive
	// mutations pass through unencrypted — the safe default for a pipeline
	// that never wires PII (most test harnesses). Production wiring
	// (MakePipeline) always sets it. Requires DDLs to also be set.
	Vault vault.Vault
	// DDLs backs step 6.5's sensitivity lookup — the same cache Hydrator and
	// Validator use. Nil disables step 6.5 alongside a nil Vault.
	DDLs *DDLCache
}

// CommitPath drives the full commit path (steps 1-9 + the 6.5 encryption pass) for a single envelope.
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
	if deps.ConflictEmitter == nil {
		deps.ConflictEmitter = noopCommitConflictEmitter{}
	}
	if deps.MaxCommitAttempts <= 0 {
		deps.MaxCommitAttempts = defaultMaxCommitAttempts
	}
	if deps.CommitRetryBackoff == nil {
		deps.CommitRetryBackoff = defaultCommitRetryBackoff
	}
	return &CommitPath{deps: deps}
}

// defaultMaxCommitAttempts bounds the §3.2-OCC internal retry: the first attempt
// plus up to (default-1) re-hydrate/re-execute/re-commit retries. On exhaustion
// the conflict surfaces honestly as RevisionConflict (a genuinely hot key).
const defaultMaxCommitAttempts = 3

// defaultCommitRetryBackoff is a small linear pause (5ms, 10ms, …) before each
// retry — bounded well under the lane SLA the commit context already carries, so
// the worst-case retry budget never approaches the lane deadline. A small
// wall-clock-derived jitter (0–2ms) spreads successive retries; it is not a
// strong decorrelator of two racers that sample within the same microsecond, but
// the linear `base` already separates attempts and conflicts are rare.
func defaultCommitRetryBackoff(attempt int) time.Duration {
	base := time.Duration(attempt) * 5 * time.Millisecond
	jitter := time.Duration(time.Now().UnixNano()%2_000_000) * time.Nanosecond
	return base + jitter
}

// MessageOutcome is the branch the commit path took for a delivered operation.
// It is purely informational — the caller (the ConsumerSupervisor in production,
// or the jetstream adapter in the in-process test harness) applies the returned
// substrate.Decision; returning the outcome lets tests assert which branch fired.
type MessageOutcome string

const (
	OutcomeAccepted  MessageOutcome = "accepted"
	OutcomeDuplicate MessageOutcome = "duplicate"
	OutcomeRejected  MessageOutcome = "rejected"
	OutcomeMalformed MessageOutcome = "malformed"
	OutcomeRetryable MessageOutcome = "retryable" // transient failure → redeliver
)

// SupervisedHandler adapts the commit path to a substrate ConsumerSupervisor
// pump: it runs the commit path for one delivered operation and returns the ack
// Decision the supervisor applies. The client reply is published by the commit
// path itself (application output), separate from ack disposition.
//
// The error channel is always nil in this delivery model: every commit-path
// outcome maps to a self-contained Decision (Ack on success/duplicate, Term on a
// permanent rejection, NakWithDelay on a transient KV failure so a still-failing
// dependency is retried on a bounded floor, never a hot-loop). This mirrors
// Loom/Weaver, whose supervised handlers likewise carry their verdict on the
// Decision and leave Classify/Probe unset; per-lane infra-pause classification
// is a later increment of the per-lane-consumers adoption.
func (cp *CommitPath) SupervisedHandler() substrate.SupervisedHandler {
	return func(ctx context.Context, msg substrate.Message) (substrate.Decision, error) {
		_, decision := cp.dispatch(ctx, msg)
		return decision, nil
	}
}

// HandleMessage runs the commit path against a single JetStream-delivered
// message and applies the resulting disposition to that message. It drives the
// in-process test harnesses; production wiring drives the same logic through
// SupervisedHandler under a ConsumerSupervisor. Both call dispatch — the single
// source of commit-path truth — and differ only in HOW they dispose: the
// supervisor applies the Decision; this adapter applies it to the jetstream.Msg,
// routing Ack through the explicit step-9 Acker boundary so the NFR-R1
// crash-at-ack fault-injection seam is preserved.
func (cp *CommitPath) HandleMessage(ctx context.Context, msg jetstream.Msg) MessageOutcome {
	outcome, decision := cp.dispatch(ctx, messageFromJetstream(msg))
	cp.disposeJetstream(ctx, decision, msg)
	return outcome
}

// dispatch executes the full commit path (steps 1-9 + the 6.5 encryption pass) for one delivered operation,
// publishes any client reply, and returns the branch outcome plus the ack
// Decision the caller must apply. It performs NO ack/nak/term itself — that
// disposition belongs to the caller (the supervisor, or HandleMessage's
// jetstream adapter).
func (cp *CommitPath) dispatch(ctx context.Context, msg substrate.Message) (MessageOutcome, substrate.Decision) {
	cp.deps.Metrics.OpsConsumed.Add(1)

	// --- Step 1: consume + parse envelope. ---
	env, err := parseEnvelopeFromBody(msg.Body)
	if err != nil {
		cp.deps.Metrics.OpsMalformed.Add(1)
		reason := err.Error()
		// Best-effort requestId recovery for health-marker keying.
		rid := extractRequestIDBestEffort(msg.Body)
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
		return OutcomeMalformed, substrate.Term
	}
	cp.deps.Logger.Info("step 1: envelope parsed",
		"requestId", env.RequestID,
		"lane", string(env.Lane),
		"operationType", env.OperationType,
		"actor", env.Actor)

	// --- Step 2: dedup. ---
	dedup, err := CheckDedup(ctx, cp.deps.Conn, cp.deps.CoreBucket, env.RequestID)
	if err != nil {
		cp.deps.Logger.Warn("step 2: dedup lookup failed; redelivering on the backoff floor",
			"requestId", env.RequestID, "error", err)
		return OutcomeRetryable, substrate.NakWithDelay
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
		return OutcomeDuplicate, substrate.Ack
	}

	// --- Step 3: auth. ---
	decision, err := cp.deps.Authorizer.Authorize(ctx, env)
	if err != nil {
		cp.deps.Metrics.OpsRejected.Add(1)
		cp.deps.Logger.Warn("step 3: authorizer error; rejecting",
			"requestId", env.RequestID, "error", err)
		cp.replyTo(msg, BuildRejectedReply(env.RequestID, ErrCodeAuthInfrastructureFailure,
			"authorizer error", map[string]any{"underlying": err.Error()}))
		return OutcomeRejected, substrate.Term
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
		return OutcomeRejected, substrate.Term
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

	// --- Steps 4-8: hydrate → execute → validate → commit, with a bounded
	// internal retry on a §3.2-OCC revision conflict. ---
	return cp.commitPipeline(ctx, msg, env, resolvedPermission)
}

// commitPipeline runs steps 4-8 for one authorized operation and publishes the
// client reply, returning the (outcome, decision) the caller disposes. It wraps
// hydrate → execute → validate → commit in a bounded retry loop: a same-key
// revision conflict on a mutation the Processor conditioned by default (Contract
// #3 §3.2 — "if omitted, use the revision read at step 4") is absorbed by
// re-hydrating against the new state and re-executing the script (exactly what a
// client resubmit does, minus the round-trip + re-auth), instead of bouncing
// RevisionConflict to the client. Re-execution is safe because a failed atomic
// batch commits NOTHING (no tracker, no outbox, no mutations) — Contract #4 §4.4.
//
// Auth (step 3) is NOT re-run on retry: it ran once in dispatch and is invariant
// for a fixed envelope (it keys on operationType + actor + authContext, none of
// which a retry changes), so re-running it would only double-emit traces. The
// "retry cannot bypass auth" property holds because the retry re-executes the
// SAME already-authorized op and step 6 re-validates permittedCommands each pass.
//
// Retry correctness rests on the Executor re-deriving a FRESH mutation set each
// pass (no expectedRevision carried over from a prior attempt's defaulting), so
// applyHydratedRevisions re-conditions against the re-hydrated revision. The real
// Starlark executor re-runs the script from scratch, satisfying this.
func (cp *CommitPath) commitPipeline(ctx context.Context, msg substrate.Message, env *OperationEnvelope, resolvedPermission *ResolvedPermission) (MessageOutcome, substrate.Decision) {
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			// A lane deadline already hit before we even re-enter — surface
			// rather than do wasted hydrate/execute work on a dead context.
			if ctx.Err() != nil {
				cp.deps.Logger.Warn("step 8: commit-retry abandoned; context done before retry",
					"requestId", env.RequestID, "attempt", attempt)
				return OutcomeRetryable, substrate.NakWithDelay
			}
			if d := cp.deps.CommitRetryBackoff(attempt); d > 0 {
				select {
				case <-time.After(d):
				case <-ctx.Done():
					// Lane deadline hit mid-backoff — surface the last conflict
					// honestly rather than spinning past the SLA.
					cp.deps.Logger.Warn("step 8: commit-retry backoff aborted by context; redelivering",
						"requestId", env.RequestID, "attempt", attempt)
					return OutcomeRetryable, substrate.NakWithDelay
				}
			}
		}

		// --- Step 4: hydrate. ---
		var state HydratedState
		var err error
		if cp.deps.Hydrator != nil {
			state, err = cp.deps.Hydrator.Hydrate(ctx, env)
			if err != nil {
				return cp.handleStubFailure(ctx, msg, env, "hydrate", err)
			}
		}
		// --- Step 5: execute. ---
		var result ScriptResult
		if cp.deps.Executor != nil {
			result, err = cp.deps.Executor.Execute(ctx, env, state)
			if err != nil {
				return cp.handleStubFailure(ctx, msg, env, "execute", err)
			}
		}
		// --- Step 6: validate. ---
		if cp.deps.Validator != nil {
			if err := cp.deps.Validator.Validate(ctx, env, result, state); err != nil {
				// DDL violations terminate the commit path (no redelivery, no retry).
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
					return OutcomeRejected, substrate.Term
				}
				return cp.handleStubFailure(ctx, msg, env, "validate", err)
			}
		}

		// --- Step 6.5: encrypt sensitive mutations (Contract #3 §3.10). ---
		// Runs after step 6 validated the plaintext shape/anchoring and before
		// the atomic batch — Core KV never observes plaintext for a sensitive
		// aspect. Both Vault and DDLs must be wired (production always sets
		// both); either nil skips the stage.
		var mintedPiiKey bool
		if cp.deps.Vault != nil && cp.deps.DDLs != nil {
			encrypted, minted, err := cp.encryptSensitiveMutations(ctx, result.Mutations)
			if err != nil {
				return cp.handleStubFailure(ctx, msg, env, "encrypt", err)
			}
			result.Mutations = encrypted
			mintedPiiKey = minted
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
			return OutcomeRejected, substrate.Term
		}

		// (A) Contract #3 §3.2: an update/tombstone with no explicit
		// expectedRevision is conditioned on the revision read at step 4 (Hydrate),
		// so two writers of the same key serialise instead of silently
		// last-write-wins. `defaulted` is the set of keys whose condition WE
		// supplied — only those are eligible for the (B) retry (an explicit-CAS op
		// and a create-once uniqueness collision keep surfacing as today).
		defaulted := applyHydratedRevisions(result.Mutations, state.Context.Hydrated)

		// (A′) Contract #2 §2.5 optionalReads: a `create` on a key step 4
		// observed as known-absent is conditioned on that absence (CreateOnly is
		// the assertion), which makes a lost create race on it retry-eligible —
		// unlike an undeclared create-once collision, re-hydration resolves it
		// (the key lands in Hydrated, the script re-branches, typically no-op).
		absentCreates := absentConditionedCreates(result.Mutations, state.Context.KnownAbsent)

		// --- Step 8: commit (with §10.7 task auto-completion injection). ---
		now := cp.deps.Clock()
		tracker := NewTracker(env, now)
		commitAck, err := cp.commitWithTaskAutoComplete(ctx, env, result, tracker, resolvedPermission)
		if err == nil {
			// Event publication is outbox-only: the faithful EventList was persisted
			// in the step-8 atomic batch (vtx.op.<id>.events) and the durable outbox
			// consumer publishes it to `core-events`. There is no in-commit publish.
			cp.deps.Metrics.OpsCommitted.Add(1)
			if env.OperationType == "ClaimIdentity" && cp.deps.ClaimEmitter != nil {
				cp.deps.ClaimEmitter.RecordClaimAttempt(ctx, "success")
			}
			cp.replyTo(msg, BuildAcceptedReplyWithRevisions(env.RequestID, now, result.PrimaryKey, commitAck.Revisions))
			if attempt > 0 {
				cp.deps.Logger.Info("step 8: committed after internal retry",
					"requestId", env.RequestID, "attempts", attempt+1)
			} else {
				cp.deps.Logger.Info("step 8: committed", "requestId", env.RequestID)
			}
			return OutcomeAccepted, substrate.Ack
		}

		// Authoritative protected-key guard (Story 1.5.5 P1): an update or tombstone
		// targeting a data.protected root is rejected before the atomic batch.
		// Terminate (no redelivery, no retry) — a redelivery cannot succeed since
		// the world is unchanged.
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
			return OutcomeRejected, substrate.Term
		}

		// Batch-size guard (Contract #3 §3.9.1): a deterministic op that exceeds
		// the message-count or per-value payload ceiling. Terminate — a
		// redelivery reproduces the identical over-limit batch and can never
		// succeed (the anti-hot-loop guarantee this design closes).
		var btlErr *BatchTooLargeError
		if errors.As(err, &btlErr) {
			cp.deps.Metrics.OpsRejected.Add(1)
			cp.deps.Logger.Info("step 8: batch-too-large rejection",
				"requestId", env.RequestID,
				"reason", btlErr.Reason,
				"limit", btlErr.Limit,
				"actual", btlErr.Actual,
				"key", btlErr.Key)
			cp.replyTo(msg, BuildRejectedReply(env.RequestID, ErrCodeBatchTooLarge,
				btlErr.Error(), map[string]any{
					"reason": btlErr.Reason,
					"limit":  btlErr.Limit,
					"actual": btlErr.Actual,
					"key":    btlErr.Key,
				}))
			return OutcomeRejected, substrate.Term
		}

		if errors.Is(err, substrate.ErrAtomicBatchRejected) {
			// If the commit failed because the tracker already exists, a previous
			// redelivery committed and we're racing with our own idempotency: ack
			// and emit a duplicate reply.
			if probe, perr := CheckDedup(ctx, cp.deps.Conn, cp.deps.CoreBucket, env.RequestID); perr == nil && probe.Outcome == DedupDuplicate {
				cp.deps.Metrics.OpsDuplicates.Add(1)
				cp.deps.Logger.Info("commit: tracker already exists (concurrent redelivery); ack + duplicate reply",
					"requestId", env.RequestID)
				cp.replyTo(msg, BuildDuplicateReply(env.RequestID, probe.Tracker))
				return OutcomeDuplicate, substrate.Ack
			}
			// Tracker not present — a genuine revision conflict on one of the
			// business mutations. Attribute it structurally (the key is not in the
			// error): a non-empty `moved` means a key WE conditioned by default
			// (§3.2) actually raced → the retry can fix it. An empty `moved` means a
			// create-once uniqueness collision or an explicit-CAS — surface it.
			//
			// mintedPiiKey extends this to step 6.5's own create-once key: a piiKey
			// mint is a "create" (never in `defaulted`, since applyHydratedRevisions
			// only conditions update/tombstone), so two concurrent first-sensitive
			// -writes for the same identity racing to mint it would otherwise
			// surface as a hard, non-retried rejection — the one create-once
			// collision this commit path doesn't already treat as benign. Retrying
			// is always safe here even if the real conflict was on an unrelated key
			// (re-hydrate/re-execute/re-commit re-derives everything fresh;
			// ensureIdentityKey's KVGet-first path simply reuses the now-existing
			// piiKey on the next attempt instead of re-minting it).
			var confErr *ConflictError
			if errors.As(err, &confErr) {
				moved := cp.movedDefaultedKeys(ctx, defaulted)
				// A known-absent-conditioned create whose key now EXISTS lost the
				// step-4-absence race — the declared-dedup analogue of a moved
				// defaulted key: re-hydration sees it present and the script
				// re-branches (Contract #2 §2.5 optionalReads).
				moved = append(moved, cp.materializedAbsentKeys(ctx, absentCreates)...)
				conflictKey := conflictKeyForSignal(confErr.ConflictingKey, moved)
				if (len(moved) > 0 || mintedPiiKey) && attempt+1 < cp.deps.MaxCommitAttempts {
					// (B) Absorb the benign same-key race: re-hydrate (fresh
					// revision) + re-execute against the new state, then re-commit.
					cp.deps.Metrics.CommitRetries.Add(1)
					cp.recordCommitConflict(ctx, env, conflictKey, false)
					cp.deps.Logger.Info("step 8: revision conflict; re-hydrating + retrying in-process",
						"requestId", env.RequestID,
						"conflictingKey", conflictKey,
						"mintedPiiKey", mintedPiiKey,
						"attempt", attempt+1)
					continue
				}
				if len(moved) > 0 {
					// Retryable but the budget is exhausted — a genuinely hot key.
					cp.deps.Metrics.CommitRetryExhausted.Add(1)
					cp.recordCommitConflict(ctx, env, conflictKey, true)
				}
				cp.deps.Metrics.OpsRejected.Add(1)
				cp.deps.Logger.Info("step 8: revision conflict; rejecting",
					"requestId", env.RequestID,
					"conflictingKey", conflictKey)
				cp.replyTo(msg, BuildRejectedReply(env.RequestID, ErrCodeRevisionConflict,
					confErr.Error(), map[string]any{
						"conflictingKey": conflictKey,
					}))
				return OutcomeRejected, substrate.Term
			}
		}

		// Genuine commit failure → redeliver on the backoff floor so JetStream
		// retries without hot-looping a still-failing dependency. Because the
		// tracker is only created on a successful atomic batch, a failed commit
		// leaves the world in the pre-commit state and redelivery is safe
		// (Contract #4 §4.4).
		cp.deps.Logger.Warn("step 8: commit failed; redelivering on the backoff floor",
			"requestId", env.RequestID, "error", err)
		return OutcomeRetryable, substrate.NakWithDelay
	}
}

// applyHydratedRevisions implements Contract #3 §3.2's default update-revision
// condition: for every update/tombstone mutation that carries NO explicit
// expectedRevision, set it to the revision the key was read at during step 4
// (Hydrate). Keys not in the hydrated set (e.g. a lazy kv.Read() not declared in
// contextHint.reads, or a write to a never-read key) have no step-4 revision and
// stay unconditioned — the honest limit of "the revision read at step 4". Returns
// key → the conditioned revision for each key whose condition this defaulting
// supplied, so the retry path can attribute a conflict structurally (NATS does
// not surface the failing key) and scope itself to exactly the conflicts §3.2
// introduces (leaving explicit-CAS and create-once conflicts to surface).
func applyHydratedRevisions(mutations []MutationOp, hydrated map[string]VertexDoc) map[string]uint64 {
	if len(hydrated) == 0 {
		return nil
	}
	var defaulted map[string]uint64
	for i := range mutations {
		m := &mutations[i]
		if m.Op != "update" && m.Op != "tombstone" {
			continue
		}
		if m.ExpectedRevision != nil {
			continue // explicit caller assertion (compensating op) — never overridden
		}
		doc, ok := hydrated[m.Key]
		if !ok {
			continue // not read at step 4 → no revision to condition on
		}
		rev := doc.Revision
		m.ExpectedRevision = &rev
		if defaulted == nil {
			defaulted = map[string]uint64{}
		}
		defaulted[m.Key] = rev
	}
	return defaulted
}

// absentConditionedCreates returns the keys of `create` mutations targeting a
// key step 4 recorded as known-absent (a declared `optionalReads` key that was
// not found — Contract #2 §2.5). Such a create is conditioned on the step-4
// observed ABSENCE (CreateOnly carries the assertion), so on a conflict the
// commit path probes exactly these keys: one that now exists proves the benign
// declared-dedup race and licenses the in-process retry. Creates on keys never
// declared (or declared but present) are excluded — their collisions surface
// as today (uniqueness/domain rejects the retry cannot fix).
func absentConditionedCreates(mutations []MutationOp, knownAbsent map[string]struct{}) []string {
	if len(knownAbsent) == 0 {
		return nil
	}
	var keys []string
	for _, m := range mutations {
		if m.Op != "create" {
			continue
		}
		if _, ok := knownAbsent[m.Key]; ok {
			keys = append(keys, m.Key)
		}
	}
	return keys
}

// materializedAbsentKeys reports which known-absent-conditioned create keys now
// EXIST in Core KV — the step-4 absence they were conditioned on has been
// invalidated by a concurrent winner. The mirror of movedDefaultedKeys for the
// create side: bounded (one KVGet per absent-conditioned create, typically one),
// probed only on a conflict. A transient read error or a still-absent key is
// treated as "did not materialize" (conservative — surface rather than spin).
func (cp *CommitPath) materializedAbsentKeys(ctx context.Context, absentCreates []string) []string {
	if len(absentCreates) == 0 {
		return nil
	}
	var materialized []string
	for _, key := range absentCreates {
		if _, err := cp.deps.Conn.KVGet(ctx, cp.deps.CoreBucket, key); err == nil {
			materialized = append(materialized, key)
		}
	}
	return materialized
}

// movedDefaultedKeys structurally attributes an atomic-batch revision conflict.
// NATS does NOT surface the failing subject in the rejection (it carries only
// "wrong last sequence: N" — see substrate/batch.go), so the failing key cannot
// be read off the error. Instead, re-read each key the Processor conditioned by
// default (§3.2) and report those whose CURRENT Core-KV revision has moved off
// the value we asserted — NATS revisions are monotonic, so a key that raced now
// sits at a strictly higher sequence (or is gone). A non-empty result means a
// benign same-key update race occurred → the retry can fix it by re-hydrating
// against the new revision. An empty result means no defaulted key moved, so the
// conflict was a create-once uniqueness collision or an explicit-CAS — retrying
// cannot help, surface it. Bounded: one KVGet per defaulted key (typically one),
// only on a conflict (rare). A transient read error is treated as "did not move"
// (conservative — surface rather than spin); the lane-deadline / NakWithDelay
// path covers a genuinely unreachable KV.
func (cp *CommitPath) movedDefaultedKeys(ctx context.Context, defaulted map[string]uint64) []string {
	if len(defaulted) == 0 {
		return nil
	}
	var moved []string
	for key, conditionedRev := range defaulted {
		entry, err := cp.deps.Conn.KVGet(ctx, cp.deps.CoreBucket, key)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				// Hard-deleted under us → the revision we conditioned on is no
				// longer valid; re-execution must re-derive against its absence.
				moved = append(moved, key)
			}
			continue
		}
		if entry.Revision != conditionedRev {
			moved = append(moved, key)
		}
	}
	return moved
}

// conflictKeyForSignal picks the key to report for a conflict. The substrate
// rarely names the failing key (the NATS rejection omits the subject), so prefer
// the structurally-attributed `moved` set — the keys we proved raced — and fall
// back to the substrate's best-effort guess only when attribution found nothing.
func conflictKeyForSignal(guessed string, moved []string) string {
	if len(moved) > 0 {
		return strings.Join(moved, ",")
	}
	return guessed
}

// recordCommitConflict surfaces a same-key conflict to Health KV as the
// lane-misassignment signal (Andrew, 2026-06-29): two writers raced the same
// key, normally indicating one was published on the wrong lane. Best-effort.
func (cp *CommitPath) recordCommitConflict(ctx context.Context, env *OperationEnvelope, conflictingKey string, exhausted bool) {
	cp.deps.ConflictEmitter.RecordCommitConflict(ctx, CommitConflictInfo{
		ConflictingKey: conflictingKey,
		Lane:           string(env.Lane),
		OperationType:  env.OperationType,
		Exhausted:      exhausted,
	})
}

// messageFromJetstream builds the substrate.Message view dispatch consumes from a
// raw jetstream.Msg, for the in-process adapter path. It mirrors the supervisor's
// own message construction (Subject/Body/ReplySubject + a header accessor) so the
// adapter and the supervised path feed dispatch identical inputs.
func messageFromJetstream(msg jetstream.Msg) substrate.Message {
	hdr := msg.Headers()
	return substrate.Message{
		Subject:      msg.Subject(),
		Body:         msg.Data(),
		ReplySubject: msg.Reply(),
		Header: func(key string) string {
			if hdr == nil {
				return ""
			}
			return hdr.Get(key)
		},
	}
}

// disposeJetstream applies a commit-path Decision to the underlying jetstream.Msg
// for the adapter path. Ack routes through the explicit step-9 Acker boundary
// (the NFR-R1 fault-injection seam); the other dispositions map directly.
func (cp *CommitPath) disposeJetstream(ctx context.Context, d substrate.Decision, msg jetstream.Msg) {
	switch d {
	case substrate.Term:
		if err := msg.Term(); err != nil {
			cp.deps.Logger.Warn("term failed", "error", err)
		}
	case substrate.Nak:
		if err := msg.Nak(); err != nil {
			cp.deps.Logger.Warn("nak failed", "error", err)
		}
	case substrate.NakWithDelay:
		if err := msg.NakWithDelay(substrate.DefaultRedeliveryDelay); err != nil {
			cp.deps.Logger.Warn("nak-with-delay failed", "error", err)
		}
	default: // substrate.Ack — step-9 explicit Acker boundary.
		acker := cp.deps.AckerFactory(msg, cp.deps.Logger)
		if ackErr := acker.Ack(ctx); ackErr != nil {
			cp.deps.Logger.Warn("step 9: ack failed", "error", ackErr)
		}
	}
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

func (cp *CommitPath) handleStubFailure(ctx context.Context, msg substrate.Message, env *OperationEnvelope, step string, err error) (MessageOutcome, substrate.Decision) {
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
	return OutcomeRejected, substrate.Term
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

// replySubjectFromMessage returns the subject to publish replies to for a
// delivered message. JetStream pull consumers rewrite the reply subject to the
// stream's ACK subject; callers that want a direct reply carry their inbox in the
// Lattice-Reply-Inbox header, which takes precedence. A Message constructed
// without a header source (nil Header) falls back to ReplySubject.
func replySubjectFromMessage(msg substrate.Message) string {
	if msg.Header != nil {
		if inbox := msg.Header("Lattice-Reply-Inbox"); inbox != "" {
			return inbox
		}
	}
	return msg.ReplySubject
}

// replyTo publishes a reply envelope to the caller's reply subject.
// Errors are logged (the commit is already durable, so failure to reply
// is observability-only).
func (cp *CommitPath) replyTo(msg substrate.Message, reply OperationReply) {
	subject := replySubjectFromMessage(msg)
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

func (cp *CommitPath) maybeReplyMalformed(msg substrate.Message, requestID, reason string) {
	subject := replySubjectFromMessage(msg)
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

// MakeStubPipeline is a convenience wrapper over MakePipeline for callers that
// do not need capability-auth (capabilityBucket="", traceAllowDecisions=false).
// The "stub" in the name refers to the absent Capability KV integration, not
// to stub implementations — all other components (Hydrator, Executor, Validator,
// Committer, EventPublisher) are production-identical.
func MakeStubPipeline(conn *substrate.Conn, coreBucket, healthBucket string, authMode AuthMode, logger *slog.Logger, instance string) (*CommitPath, *HealthHeartbeater, error) {
	// Vault nil: a stub pipeline never wires PII crypto, so step 6.5 is a
	// no-op and any sensitive aspect it happens to write lands unencrypted.
	// None of the stub-pipeline callers (Processor-internal step tests,
	// package-install harnesses, unrelated e2e convergence fixtures) assert
	// sensitive-aspect plaintext today; the pipelines that do (identity-domain's
	// own PII/claim tests) go through testutil.CapabilityPipeline instead,
	// which wires a real Vault.
	return MakePipeline(conn, coreBucket, healthBucket, "", authMode, false, logger, instance, AuthWiring{}, nil)
}

// AuthWiring carries the platform-path routing inputs the step-3 authorizer
// needs but the processor cannot compute itself (they depend on bootstrap key
// constants that live above the processor import boundary). The caller
// (cmd/processor) discovers SystemActorKeys and threads them in.
type AuthWiring struct {
	// RbacRolesActive enables class-aware platform routing (system actors →
	// cap.<actor> ∪ cap.roles.<actor> union; every other actor →
	// cap.roles.<actor>). Production ALWAYS sets this true — the routing is
	// correct whether or not rbac-domain is installed, because an absent
	// cap.roles.<actor> is an empty skip in the union read. It is NOT gated on
	// an rbac-install probe: see the field doc on SelectAuthorizerOpts for why
	// that probe was a boot-latch bug. The zero value (false) is a test-only
	// posture (cap.<actor> single-key for all actors).
	RbacRolesActive bool
	// SystemActorKeys are the kernel-seeded system actor keys (vtx.identity.<id>
	// of admin + the service actors) that read the cap.<actor> ∪ cap.roles.<actor>
	// union. Primordial, so a one-time discovery at startup is stable.
	SystemActorKeys []string
}

// MakePipeline is the production wiring entry point. capabilityBucket is the
// Capability KV bucket name; empty falls back to AuthModeStub regardless of
// the requested mode (test-friendly default). authMode is applied via
// SelectAuthorizerArgs so the Capability KV reader + alert emitter are wired
// in one place.
//
// traceAllowDecisions (FR23): when true, auth trace records are also written
// for ALLOWED decisions. Defaults off — volume implications for busy deployments.
//
// authWiring carries the rbac-domain platform-path routing (see AuthWiring).
//
// v is the Vault backing step 6.5's sensitive-aspect encrypt-on-write /
// decrypt-on-read (Contract #3 §3.10). Nil disables the stage (see
// MakeStubPipeline); production wiring (cmd/processor) always supplies one.
func MakePipeline(conn *substrate.Conn, coreBucket, healthBucket, capabilityBucket string, authMode AuthMode, traceAllowDecisions bool, logger *slog.Logger, instance string, authWiring AuthWiring, v vault.Vault) (*CommitPath, *HealthHeartbeater, error) {
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
		RbacRolesActive:  authWiring.RbacRolesActive,
		SystemActorKeys:  authWiring.SystemActorKeys,
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

	// Wire the commit-conflict emitter (the §3.2-OCC lane-misassignment signal).
	conflictEmitter := NewCommitConflictEmitter(conn, healthBucket, instance, logger)

	hydrator := NewHydratorWithCache(conn, coreBucket, ddls, logger)
	hydrator.Vault = v

	committer := NewCommitter(conn, coreBucket, ddls, logger, time.Now)
	cp := NewCommitPath(Deps{
		Conn:            conn,
		CoreBucket:      coreBucket,
		HealthKV:        healthBucket,
		Authorizer:      authz,
		Hydrator:        hydrator,
		Executor:        NewExecutor(NewStarlarkRunner(0, 0), logger),
		Validator:       NewValidator(ddls, conn, coreBucket, logger),
		Committer:       committer,
		Metrics:         metrics,
		Heartbeater:     hb,
		Logger:          logger,
		DenialBuilder:   denialBuilder,
		TraceEmitter:    traceEmitter,
		ClaimEmitter:    claimEmitter,
		ConflictEmitter: conflictEmitter,
		Vault:           v,
		DDLs:            ddls,
	})
	return cp, hb, nil
}
