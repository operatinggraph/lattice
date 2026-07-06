package loom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// opEnvelope is the wire format published to ops.<lane> (Contract #2 §2.1) — the
// same shape internal/processor.OperationEnvelope serializes to; loom carries
// its own copy to keep the module boundary clean (AC #8: no internal/processor
// import).
type opEnvelope struct {
	RequestID     string          `json:"requestId"`
	Lane          string          `json:"lane"`
	OperationType string          `json:"operationType"`
	Actor         string          `json:"actor"`
	SubmittedAt   string          `json:"submittedAt"`
	Payload       json.RawMessage `json:"payload"`
	// Class is left empty: Loom relies on the Processor's operationType→class
	// reverse index (Contract #2 §2.1) — each op Loom dispatches (CreateTask,
	// the externalTask instanceOp, the lifecycle ops) is admitted by exactly one
	// vertexType DDL, so inference is unambiguous. The field stays for an
	// explicit override.
	Class string `json:"class,omitempty"`
	// ContextHint carries the OCC reads the dispatched op's DDL hydrates, copied
	// from the outbox record's Reads; omitted for read-free ops.
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

// relayDurable is the command-outbox relay's durable consumer name.
const relayDurable = "loom-outbox-relay"

// relay is the Actuator: it drains loom-state outbox.<token> records and
// fire-and-forget publishes each op to ops.<lane>, deleting the record on
// publish-ack (Contract #10 §10.3 command outbox). It is a durable consumer on
// the loom-state backing stream, so a publish failure Naks → JetStream
// redelivers (at-least-once); re-publish is idempotent because Loom chose the
// requestId, so a duplicate collapses on the Contract #4 vtx.op.<requestId>
// tracker. The relay uses ONLY substrate primitives — no raw nats.go/jetstream
// handle in internal/loom (AC #8).
type relay struct {
	conn       *substrate.Conn
	bucket     string // loom-state
	subjPrefix string // "$KV.<bucket>." — to recover the key from the subject
	logger     *slog.Logger
}

func newRelay(conn *substrate.Conn, bucket string, logger *slog.Logger) *relay {
	return &relay{
		conn:       conn,
		bucket:     bucket,
		subjPrefix: "$KV." + bucket + ".",
		logger:     logger,
	}
}

// handle publishes one outbox record to ops.<lane> and deletes it on success.
// An empty body is a delete marker (the relay's own delete-on-publish, or a
// tombstone) — nothing to relay. An unparseable record is poison (Term). Both
// failure-return paths (publish and the subsequent KVDelete) return
// NakWithDelay: one consistent backoff posture so the relay never hot-loops
// against a failing dependency (with the ops stream healthy but loom-state KV
// failing, an immediate Nak would re-publish — idempotent but not free — and
// re-fail the delete at full speed for the outage's duration). Bounded cadence,
// unbounded count (at-least-once preserved; the supervisor sets no MaxDeliver).
func (r *relay) handle(ctx context.Context, msg substrate.Message) (substrate.Decision, error) {
	if len(msg.Body) == 0 {
		return substrate.Ack, nil
	}
	var rec outboxRecord
	if err := json.Unmarshal(msg.Body, &rec); err != nil {
		r.logger.Error("loom relay: outbox record unparseable; term", "err", err)
		return substrate.Term, nil
	}
	env := opEnvelope{
		RequestID:     rec.RequestID,
		Lane:          rec.Lane,
		OperationType: rec.Operation,
		Actor:         rec.Actor,
		SubmittedAt:   substrate.FormatTimestamp(time.Now()),
		Payload:       rec.Payload,
	}
	if len(rec.Reads) > 0 || len(rec.OptionalReads) > 0 {
		env.ContextHint = &contextHint{Reads: rec.Reads, OptionalReads: rec.OptionalReads}
	}
	if rec.Target != "" {
		env.AuthContext = &authContext{Target: rec.Target}
	}
	data, err := json.Marshal(env)
	if err != nil {
		r.logger.Error("loom relay: marshal envelope; term", "requestId", rec.RequestID, "err", err)
		return substrate.Term, nil
	}
	if err := r.conn.Publish(ctx, "ops."+rec.Lane, data, nil); err != nil {
		r.logger.Error("loom relay: publish failed; nak with delay", "requestId", rec.RequestID, "err", err)
		return substrate.NakWithDelay, nil
	}
	key := strings.TrimPrefix(msg.Subject, r.subjPrefix)
	if err := r.conn.KVDelete(ctx, r.bucket, key); err != nil && !errors.Is(err, substrate.ErrKeyNotFound) {
		r.logger.Error("loom relay: delete outbox record failed; nak with delay", "key", key, "err", err)
		return substrate.NakWithDelay, nil
	}
	r.logger.Info("loom op relayed", "requestId", rec.RequestID, "operation", rec.Operation, "lane", rec.Lane)
	return substrate.Ack, nil
}

// buildOutbox constructs the outbox record the engine writes into a transition
// batch (the op the relay will submit). payload is the op's payload object;
// reads is the dispatched op's ContextHint.Reads (the bare vertex keys its DDL
// hydrates), nil/empty for read-free ops; optionalReads is its
// ContextHint.OptionalReads (Contract #2 §2.5 — declared absence-tolerant
// reads, e.g. CreateTask's dedup key), nil/empty when the op reads none.
func buildOutbox(requestID, operation string, payload map[string]any, target, lane, actor string, reads, optionalReads []string) (*outboxRecord, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("loom: marshal op payload: %w", err)
	}
	return &outboxRecord{
		RequestID:     requestID,
		Operation:     operation,
		Payload:       body,
		Target:        target,
		Lane:          lane,
		Actor:         actor,
		Reads:         reads,
		OptionalReads: optionalReads,
	}, nil
}
