package weaver

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// opEnvelope is the wire format published to ops.<lane> (Contract #2 §2.1) —
// the envelope fields Weaver populates, serialized in the shape the
// Processor's consume path reads; weaver carries its own copy to keep the
// module boundary clean (no internal/processor import).
type opEnvelope struct {
	RequestID     string          `json:"requestId"`
	Lane          string          `json:"lane"`
	OperationType string          `json:"operationType"`
	Actor         string          `json:"actor"`
	SubmittedAt   string          `json:"submittedAt"`
	Payload       json.RawMessage `json:"payload"`
	// Class is the DDL canonical name. Optional (omitempty): left empty for an
	// operationType admitted by exactly one installed vertexType DDL, which the
	// Processor's operationType→class reverse index (Contract #2 §2.1) infers
	// for free. A directOp's playbook entry sets it (GapActionSpec.Class →
	// plan.class) only when Operation is genuinely ambiguous across installed
	// DDLs — the reverse index deliberately excludes an ambiguous operationType
	// rather than guess, so an unpinned dispatch against it would fail closed
	// (MissingClass) forever.
	Class string `json:"class,omitempty"`
	// ContextHint carries the OCC reads the dispatched op's DDL hydrates. Weaver
	// sets it from the plan's declared read-set (the bare vertex keys the op's
	// script validates); omitted for read-free ops.
	ContextHint *contextHint `json:"contextHint,omitempty"`
	AuthContext *authContext `json:"authContext,omitempty"`
}

type contextHint struct {
	Reads         []string `json:"reads,omitempty"`
	OptionalReads []string `json:"optionalReads,omitempty"`
}

type authContext struct {
	Target string `json:"target,omitempty"`
}

// actuator submits remediation ops. The submit is ONE fire-and-forget publish
// to ops.<lane> — no request-reply (a synchronous reply wait blocks the
// consumer and forces a raw NATS handle into the engine) and no command
// outbox: Weaver, unlike Loom, has no cursor advance to keep atomic with the
// submit. Its crash-recovery story is the §10.3 weaver-state mark +
// level-reconcile: a failed publish Naks the CDC message, the redelivery
// re-reads the existing mark, and the retry re-publishes the SAME
// deterministic requestId, which collapses on the Contract #4
// vtx.op.<requestId> tracker.
type actuator struct {
	conn   *substrate.Conn
	lane   string
	actor  string
	logger *slog.Logger
}

func newActuator(conn *substrate.Conn, lane, actor string, logger *slog.Logger) *actuator {
	if logger == nil {
		logger = slog.Default()
	}
	return &actuator{conn: conn, lane: lane, actor: actor, logger: logger}
}

// submit publishes one remediation op under Weaver's service-actor authority.
// class pins the op's DDL canonical name (opEnvelope.Class) for an
// operationType admitted by more than one installed vertexType DDL; empty for
// every unambiguous op (the Processor's reverse index resolves it for free).
// reads is the dispatched op's ContextHint.Reads (the bare vertex keys its DDL
// hydrates); empty for read-free ops. optionalReads is its
// ContextHint.OptionalReads (Contract #2 §2.5 — declared absence-tolerant
// reads, e.g. assignTask's stable task dedup key); empty when the op reads none.
func (a *actuator) submit(ctx context.Context, requestID, operationType, class string, payload map[string]any, authTarget string, reads, optionalReads []string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("weaver: marshal op payload: %w", err)
	}
	env := opEnvelope{
		RequestID:     requestID,
		Lane:          a.lane,
		OperationType: operationType,
		Actor:         a.actor,
		SubmittedAt:   substrate.FormatTimestamp(time.Now()),
		Payload:       body,
	}
	if class != "" {
		env.Class = class
	}
	if len(reads) > 0 || len(optionalReads) > 0 {
		env.ContextHint = &contextHint{Reads: reads, OptionalReads: optionalReads}
	}
	if authTarget != "" {
		env.AuthContext = &authContext{Target: authTarget}
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("weaver: marshal op envelope: %w", err)
	}
	if err := a.conn.Publish(ctx, "ops."+a.lane, data, nil); err != nil {
		return fmt.Errorf("weaver: publish op %s: %w", requestID, err)
	}
	a.logger.Info("weaver op submitted",
		"requestId", requestID, "operation", operationType, "lane", a.lane, "authTarget", authTarget)
	return nil
}

// scheduleTimer publishes one §10.4 @at scheduled message on the
// core-schedules stream: subject schedule.weaver.timer.<targetId>.<entityId>,
// republish target schedule.weaver.timer.fired.<targetId>.<entityId> (within
// the stream's own subject space, as the server requires). Fire-and-forget
// like submit — one publish, no reply; re-publishing to the same subject
// replaces the prior schedule (one schedule per subject).
func (a *actuator) scheduleTimer(ctx context.Context, targetID, entityID string, payload []byte, fireAt string) error {
	subject := scheduleSubjectPrefix + targetID + "." + entityID
	header := map[string]string{
		substrate.ScheduleHeader:       "@at " + fireAt,
		substrate.ScheduleTargetHeader: firedSubjectPrefix + targetID + "." + entityID,
	}
	if err := a.conn.Publish(ctx, subject, payload, header); err != nil {
		return fmt.Errorf("weaver: schedule timer %s: %w", subject, err)
	}
	a.logger.Info("weaver timer scheduled", "targetId", targetID, "entityId", entityID, "fireAt", fireAt)
	return nil
}

// deriveEpisodeRequestID returns a deterministic 20-char NanoID (over the
// canonical Lattice alphabet, Contract #1) for one dispatch episode. The
// episode tag is the mark's KV create revision: a re-fire of the SAME episode
// (publish-failure retry, CDC redelivery) reuses the same requestId and
// collapses on the Contract #4 vtx.op.<requestId> tracker; a legitimately
// re-opened gap (mark deleted, new CAS-create) gets a new revision → a new
// requestId → a real new dispatch.
func deriveEpisodeRequestID(targetID, entityID, gapColumn string, markRevision uint64) string {
	return deriveID("episode:", targetID+"\x00"+entityID+"\x00"+gapColumn, markRevision)
}

// deriveStableTaskID returns the deterministic task NanoID an assignTask
// dispatch supplies to CreateTask (the verbatim taskId seam, Contract #10 §10.6
// / §10.3), keyed on the mark's per-OPEN-EPISODE claimId rather than the
// per-reclaim markRevision. The claimId is minted once at the mark's CAS-create
// and preserved across every reclaim, so EVERY re-dispatch of the same open gap
// re-supplies the SAME taskId — the duplicate CreateTask collapses on the
// existing task (the script's kv.Read no-op + the CreateOnly backstop) instead
// of minting a fresh one per mark-lease expiry. A legitimate close→reopen mints
// a new mark ⇒ new claimId ⇒ a fresh taskId (a real new task). It is namespaced
// disjoint from deriveEpisodeRequestID so the op's requestId and the task id
// never collide for the same episode.
func deriveStableTaskID(targetID, entityID, gapColumn, claimID string) string {
	return deriveID("task:", targetID+"\x00"+entityID+"\x00"+gapColumn+"\x00"+claimID, 0)
}

// deriveStableInstanceID returns the deterministic Loom instanceId a triggerLoom
// dispatch supplies to StartLoomPattern, keyed on the open-episode claimId (see
// deriveStableTaskID). A re-dispatch re-supplies the same instanceId, so the
// re-emitted loom.patternStarted collapses on Loom's existing instance.<id>
// (getInstance presence + the createInstance CreateOnly race guard) — no new
// Loom instance, hence no new task. This dedups the whole pattern, not just its
// task — the correct altitude for triggerLoom. Namespaced disjoint from the task
// and requestId derivations.
func deriveStableInstanceID(targetID, entityID, gapColumn, claimID string) string {
	return deriveID("instance:", targetID+"\x00"+entityID+"\x00"+gapColumn+"\x00"+claimID, 0)
}

// deriveAugurHandle returns the stable bare-handle instanceKey for an Augur
// reasoning episode, keyed on (targetID, entityID, gapColumn). Unlike the
// triggerLoom/assignTask stable ids it is NOT claimId-seeded — Weaver dispatches
// the reasoning op as a generic directOp, whose payload carries no claimId — so
// the handle is stable across redeliveries AND the reconciler reclaim: the
// CreateAugurReasoningClaim claim vertex collapses create-only and the bridge
// dedups on idempotencyKey == this handle, giving ≤1 claim / ≤1 billed model
// call per stuck gap. A genuine close→reopen of the same gap therefore reuses
// the existing proposal rather than re-reasoning — the cost-bounded choice for
// the human-gated reasoning tier. Namespaced disjoint from the task/instance
// and requestId derivations.
func deriveAugurHandle(targetID, entityID, gapColumn string) string {
	return deriveID("augur:", targetID+"\x00"+entityID+"\x00"+gapColumn, 0)
}

// deriveProposalDispatchRequestID returns the deterministic requestId for the
// Fire 2b augurDispatch target's proposed-remediation op — PROPOSAL-scoped
// (keyed on the handle alone, no mark revision / claimId), so a sweep reclaim
// of the same open dispatch re-derives the SAME requestId and collapses on the
// Contract #4 tracker (at-most-one remediation effect) regardless of whether
// the prior attempt's RecordProposalDispatch flip ever landed (design
// augur-dispatch-pickup §3.3/§3.4). Namespaced disjoint from every other
// derivation.
func deriveProposalDispatchRequestID(proposalHandle string) string {
	return deriveID("proposalDispatch:", proposalHandle, 0)
}

// deriveProposalDispatchFlipRequestID returns the deterministic requestId for
// the RecordProposalDispatch flip that follows a Fire 2b dispatch — scoped to
// the proposal handle AND the outcome, so a redelivery/reclaim's repeat flip
// attempt (dispatched or invalid) collapses on the Contract #4 tracker too;
// the DDL's approved-only guard is the independent second backstop.
func deriveProposalDispatchFlipRequestID(proposalHandle, outcome string) string {
	return deriveID("proposalDispatchFlip:", proposalHandle+"\x00"+outcome, 0)
}

// deriveTimerRequestID returns the deterministic requestId for one fired-timer
// conversion (Contract #10 §10.4): derived from the schedule subject + the
// fire instant, so an at-least-once redelivery of the SAME firing reuses the
// same requestId and collapses on the Contract #4 vtx.op.<requestId> tracker,
// while a new firing of a re-armed timer (a new fireAt) is a genuinely new op.
func deriveTimerRequestID(scheduleSubject, fireAt string) string {
	return deriveID("timer:", scheduleSubject+"\x00"+fireAt, 0)
}

// deriveID is the shared deterministic NanoID derivation: sha256 over the
// namespaced seed, expanded across the canonical alphabet by re-hashing.
func deriveID(namespace, seed string, revision uint64) string {
	var rev [8]byte
	binary.BigEndian.PutUint64(rev[:], revision)
	sum := sha256.Sum256(append([]byte(namespace+seed+":"), rev[:]...))
	id := make([]byte, substrate.NanoIDLength)
	digest := sum[:]
	di := 0
	for i := 0; i < substrate.NanoIDLength; i++ {
		if di >= len(digest) {
			next := sha256.Sum256(digest)
			digest = next[:]
			di = 0
		}
		id[i] = substrate.Alphabet[int(digest[di])%len(substrate.Alphabet)]
		di++
	}
	return string(id)
}
