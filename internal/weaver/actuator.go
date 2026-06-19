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
	// Class is the DDL canonical name. Optional (omitempty): Weaver leaves it
	// empty and relies on the Processor's operationType→class reverse index
	// (Contract #2 §2.1) — every op Weaver dispatches is admitted by exactly one
	// vertexType DDL, so inference is unambiguous. The field exists so a future
	// caller MAY pin a class explicitly when needed.
	Class string `json:"class,omitempty"`
	// ContextHint carries the OCC reads the dispatched op's DDL hydrates. Weaver
	// sets it from the plan's declared read-set (the bare vertex keys the op's
	// script validates); omitted for read-free ops.
	ContextHint *contextHint `json:"contextHint,omitempty"`
	AuthContext *authContext `json:"authContext,omitempty"`
}

type contextHint struct {
	Reads []string `json:"reads,omitempty"`
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
// reads is the dispatched op's ContextHint.Reads (the bare vertex keys its DDL
// hydrates); empty for read-free ops.
func (a *actuator) submit(ctx context.Context, requestID, operationType string, payload map[string]any, authTarget string, reads []string) error {
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
	if len(reads) > 0 {
		env.ContextHint = &contextHint{Reads: reads}
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

// deriveEpisodeTaskID returns the deterministic task NanoID an assignTask
// episode supplies to CreateTask (the verbatim taskId seam, Contract #10
// §10.6): a re-fire of the same episode re-supplies the same taskId, so the
// duplicate CreateTask collapses on the Contract #4 tracker — no duplicate
// task. It is namespaced disjoint from deriveEpisodeRequestID so the op's
// requestId and the task id never collide for the same episode.
func deriveEpisodeTaskID(targetID, entityID, gapColumn string, markRevision uint64) string {
	return deriveID("task:", targetID+"\x00"+entityID+"\x00"+gapColumn, markRevision)
}

// deriveResolveRequestID returns the deterministic requestId for one nudge
// resolve op, derived from the claimId (NOT the mark revision). The resolve must
// collapse to the SAME op across a recovery that re-submits it under a DIFFERENT
// (reclaimed) mark revision — so it is keyed on the claimId, the one identifier
// that survives a reclaim (§10.3 carries it forward). A redelivery or recovery
// re-derives the same requestId and the duplicate resolve collapses on the
// Contract #4 vtx.op.<requestId> tracker → exactly one resolve mutation in Core
// KV. Namespaced disjoint from the episode/task/timer derivations so a claimId
// and a mark-revision seed can never collide.
func deriveResolveRequestID(claimID string) string {
	return deriveID("resolve:", claimID, 0)
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
