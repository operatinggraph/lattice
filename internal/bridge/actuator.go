package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// opEnvelope is the wire format published to ops.<lane> (Contract #2 §2.1) — the
// same shape internal/processor.OperationEnvelope serializes to; the bridge
// carries its own copy to keep the module boundary clean (it imports no
// internal/processor — substrate-only, like Loom's relay).
type opEnvelope struct {
	RequestID     string          `json:"requestId"`
	Lane          string          `json:"lane"`
	OperationType string          `json:"operationType"`
	Actor         string          `json:"actor"`
	SubmittedAt   string          `json:"submittedAt"`
	Payload       json.RawMessage `json:"payload"`
	ContextHint   *contextHint    `json:"contextHint,omitempty"`
	AuthContext   *authContext    `json:"authContext,omitempty"`
}

// contextHint mirrors internal/processor.ContextHint (Contract #2 §2.5) — the
// same local-copy convention Weaver's and Loom's actuators use, since the
// bridge is substrate-only and imports no internal/processor.
type contextHint struct {
	Reads []string `json:"reads,omitempty"`
}

type authContext struct {
	Target string `json:"target,omitempty"`
}

// actuator submits the result op (replyOp) for a completed external call. It is
// a direct fire-and-forget publish to ops.<lane>: the bridge holds no command
// outbox (it persists no cursor to keep atomic with the publish — there is no
// dual write). Crash-safety holds via at-least-once event redelivery plus the
// deterministic requestId: a re-published replyOp collapses on the Contract #4
// vtx.op.<requestId> tracker. The actuator uses ONLY substrate primitives — no
// raw nats.go/jetstream handle in internal/bridge.
type actuator struct {
	conn  *substrate.Conn
	lane  string
	actor string
}

func newActuator(conn *substrate.Conn, lane, actor string) *actuator {
	return &actuator{conn: conn, lane: lane, actor: actor}
}

// submit publishes one replyOp envelope to ops.<lane>. requestId is the
// deterministic result-op id (deriveReplyRequestID) so a redelivered external
// event re-submits the same id and collapses on the Contract #4 tracker.
// payload carries payload.externalRef = the opaque instanceKey plus the outcome
// fields. authContext is omitted: the bridge service actor is root-equivalent
// (operator scope:"any") and authorizes regardless of target, and the bridge is
// type-agnostic so it never synthesizes a typed claim-vertex target (the real
// replyOp DDL supplies any narrow target). reads is the read-posture class-(a)
// key set the replyOp DDL requires (Contract #2 §2.5, script-read-posture-design
// §13 dispatcher map) — derivable from externalRef alone since that is all the
// bridge ever knows about the reply; nil for a replyOp with no declarable read
// (the DDL's own kv.Read stays lazy on-demand, unchanged behavior).
func (a *actuator) submit(ctx context.Context, requestID, operation string, payload map[string]any, reads []string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("bridge: marshal replyOp payload: %w", err)
	}
	env := opEnvelope{
		RequestID:     requestID,
		Lane:          a.lane,
		OperationType: operation,
		Actor:         a.actor,
		SubmittedAt:   substrate.FormatTimestamp(time.Now()),
		Payload:       body,
	}
	if len(reads) > 0 {
		env.ContextHint = &contextHint{Reads: reads}
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("bridge: marshal replyOp envelope: %w", err)
	}
	if err := a.conn.Publish(ctx, "ops."+a.lane, data, nil); err != nil {
		return fmt.Errorf("bridge: publish replyOp %q to ops.%s: %w", requestID, a.lane, err)
	}
	return nil
}

// scheduleAt publishes one §10.4 @at scheduled message on the core-schedules
// stream: subject scheduleSubject, republish target firedTarget (which MUST lie
// within the stream's own subject space, as the server requires). It is
// fire-and-forget like submit — one publish, no reply; re-publishing to the same
// subject REPLACES the prior schedule (one schedule per subject), so a redelivered
// Pending re-arms idempotently. fireAt is truncated to whole seconds so the header
// instant matches the .dispatch marker's canonical-UTC instants; a past instant is
// published verbatim and the server fires it immediately (correct level
// semantics).
func (a *actuator) scheduleAt(ctx context.Context, scheduleSubject, firedTarget string, fireAt time.Time, payload []byte) error {
	fireAtStr := fireAt.UTC().Truncate(time.Second).Format(time.RFC3339)
	header := map[string]string{
		substrate.ScheduleHeader:       "@at " + fireAtStr,
		substrate.ScheduleTargetHeader: firedTarget,
	}
	if err := a.conn.Publish(ctx, scheduleSubject, payload, header); err != nil {
		return fmt.Errorf("bridge: schedule %q (target %q): %w", scheduleSubject, firedTarget, err)
	}
	return nil
}
