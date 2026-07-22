package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/gateway/revocation"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// revocationConsumerName is the durable name of the Gateway's own
// events.gateway.> materializer (gateway-token-revocation-activation-
// design.md §2.3).
const revocationConsumerName = "gateway-revocation"

// revocationFilterSubject covers both gateway.actorRevoked and
// gateway.actorUnrevoked.
const revocationFilterSubject = "events.gateway.>"

// revocationCatchUpTimeout bounds how long StartRevocationMaterializer waits
// for the cold-start drain (§2.3) before proceeding anyway — a slow drain is
// an infra hiccup the durable self-heals from, not a wiring failure, so it
// logs a warning rather than refusing to start.
const revocationCatchUpTimeout = 15 * time.Second

// revocationIssueKey is the Contract #5 issue key the materializer's pause
// state is surfaced under (health.go's issueCache).
const revocationIssueKey = "revocation-consumer"

// revocationPoisonIssueKey is the Contract #5 issue key any dropped,
// never-redelivered revocation event is surfaced under — an unputtable actor
// key, an unparseable event body, or an event missing its actor. Single fixed
// key (last-offender reporting, mirroring the Weaver planner's last-N-
// divergence heartbeat convention) — a dropped message is gone, not an
// ongoing state, so there is no natural per-event clear trigger; the issue
// stays until an operator investigates and clears it.
const revocationPoisonIssueKey = "revocation-poison-key"

// revocationEventBody is the shape the outbox publishes for
// gateway.actorRevoked / gateway.actorUnrevoked (internal/processor's Event
// envelope — step7_events.go); the business fields ride `payload`.
type revocationEventBody struct {
	EventType string `json:"eventType"`
	Payload   struct {
		Actor  string `json:"actor"`
		At     string `json:"at"`
		By     string `json:"by"`
		Reason string `json:"reason"`
	} `json:"payload"`
}

// StartRevocationMaterializer opens the token-revocation bucket, attaches the
// durable events.gateway.> consumer that folds RevokeActor/UnrevokeActor
// events into it, and blocks until the durable has drained every event
// committed before this call (cold-start correctness, design §2.3/§2.4).
//
// Returns an error — refuse to start — only if the bucket cannot open or the
// consumer cannot attach; a slow catch-up logs a warning and proceeds (the
// durable self-heals as it drains). The caller is responsible for calling
// Stop on the returned supervisor at shutdown.
func StartRevocationMaterializer(ctx context.Context, conn *substrate.Conn, hb *Heartbeater, logger *slog.Logger) (*substrate.ConsumerSupervisor, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := conn.KVStatus(ctx, revocation.BucketName); err != nil {
		return nil, fmt.Errorf("gateway: token-revocation bucket unavailable: %w", err)
	}

	sup := substrate.NewConsumerSupervisor(conn)
	spec := substrate.ConsumerSpec{
		Name:          revocationConsumerName,
		Stream:        bootstrap.CoreEventsStreamName,
		FilterSubject: revocationFilterSubject,
		DeliverPolicy: substrate.DeliverAll,
		Handler:       revocationHandler(conn, hb, logger),
		Classify:      classifyRevocationError,
		Probe:         func(ctx context.Context) error { return conn.KVStatus(ctx, revocation.BucketName) },
		Health:        &heartbeatIssueSink{hb: hb},
		Logger:        logger,
	}
	if err := sup.Add(ctx, spec); err != nil {
		return nil, fmt.Errorf("gateway: attach revocation consumer: %w", err)
	}

	deadline := time.Now().Add(revocationCatchUpTimeout)
	for {
		caughtUp, err := conn.ConsumerCaughtUp(ctx, bootstrap.CoreEventsStreamName, revocationConsumerName)
		if err == nil && caughtUp {
			break
		}
		if time.Now().After(deadline) {
			logger.Warn("gateway: revocation cold-start catch-up timed out; continuing (self-heals as the durable drains)")
			break
		}
		select {
		case <-ctx.Done():
			return sup, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return sup, nil
}

// revocationHandler folds a gateway.actorRevoked/actorUnrevoked event into the
// token-revocation bucket. Must be idempotent (at-least-once delivery): a
// redelivered revoke re-puts the same key, a redelivered unrevoke re-deletes
// an already-absent key (tolerated below).
func revocationHandler(conn *substrate.Conn, hb *Heartbeater, logger *slog.Logger) substrate.SupervisedHandler {
	return func(ctx context.Context, msg substrate.Message) (substrate.Decision, error) {
		if len(msg.Body) == 0 {
			return substrate.Ack, nil
		}
		var eb revocationEventBody
		if err := json.Unmarshal(msg.Body, &eb); err != nil {
			logger.Warn("gateway: revocation event body unparseable; dropping", "error", err)
			hb.SetIssue(revocationPoisonIssueKey, severityError, "revocation.malformedEvent",
				"revocation event body unparseable, dropped: "+err.Error())
			return substrate.Ack, nil
		}
		actor := eb.Payload.Actor
		if actor == "" {
			logger.Warn("gateway: revocation event missing actor; dropping", "eventType", eb.EventType)
			hb.SetIssue(revocationPoisonIssueKey, severityError, "revocation.missingActor",
				"revocation event missing actor, dropped: eventType="+eb.EventType)
			return substrate.Ack, nil
		}

		switch eb.EventType {
		case "gateway.actorRevoked":
			doc, err := json.Marshal(map[string]any{
				"revokedAt": eb.Payload.At,
				"by":        eb.Payload.By,
				"reason":    eb.Payload.Reason,
			})
			if err != nil {
				return substrate.Ack, nil // unreachable (map[string]any always marshals)
			}
			if _, err := conn.KVPut(ctx, revocation.BucketName, actor, doc); err != nil {
				return revocationWriteFailed(hb, logger, "revoke", actor, err)
			}
			hb.RecordRevocationSync(msg.Sequence, time.Now())
			return substrate.Ack, nil
		case "gateway.actorUnrevoked":
			if err := conn.KVDelete(ctx, revocation.BucketName, actor); err != nil && !errors.Is(err, substrate.ErrKeyNotFound) {
				return revocationWriteFailed(hb, logger, "unrevoke", actor, err)
			}
			hb.RecordRevocationSync(msg.Sequence, time.Now())
			return substrate.Ack, nil
		default:
			// FilterSubject scopes delivery to events.gateway.>, so an
			// unrecognized eventType would mean a future sibling event under
			// the same domain — ignore rather than fail closed on it.
			return substrate.Ack, nil
		}
	}
}

// revocationWriteFailed classifies a KVPut/KVDelete failure on the
// token-revocation bucket. An invalid-key error can never succeed on
// redelivery (the actor id itself is unwritable) — dropping it loudly (Term)
// with a Health issue is the fix for the poison-pill hazard that otherwise
// stalls kill-switch sync forever (the consumer has no MaxDeliver). Any other
// failure is treated as transient (Nak, at-least-once redelivery preserved).
func revocationWriteFailed(hb *Heartbeater, logger *slog.Logger, verb, actor string, err error) (substrate.Decision, error) {
	if substrate.IsInvalidKeyError(err) {
		logger.Error("gateway: revocation event dropped — unputtable actor key", "verb", verb, "actor", actor, "error", err)
		hb.SetIssue(revocationPoisonIssueKey, severityError, "revocation.unputtableKey",
			"revocation "+verb+" dropped for unputtable actor key "+actor+": "+err.Error())
		return substrate.Term, fmt.Errorf("gateway: %s %s: unputtable key, dropping: %w", verb, actor, err)
	}
	return substrate.Nak, fmt.Errorf("gateway: %s %s: %w", verb, actor, err)
}

// classifyRevocationError is the consumer spec's Classify hook. It is the
// load-bearing half of the poison-pill fix: ConsumerSupervisor.processMsg
// only calls applyDecision (which is what actually invokes msg.Term() for a
// Term decision) when the handler's error classifies as ClassTransient or
// ClassTerminal — ClassInfra/ClassStructural bypass the returned Decision
// entirely and instead pause the whole consumer with the message left
// un-acked (substrate/consumer_supervisor_spec.go's documented "the framework
// does NOT ack/nak on infra/structural" contract). An invalid-key error is
// permanently-bad data on ONE message, not a dependency outage — classifying
// it ClassInfra (the prior blanket behavior) would pause the entire
// materializer and leave the poison message pending, so on resume/probe it
// redelivers and re-triggers the same pause: an infinite pause/probe/
// redeliver spin functionally equivalent to the bug this fix closes.
// ClassTerminal lets the Term decision actually apply, disposing the one bad
// message while every other event keeps flowing. Any other error (e.g. the
// bucket unreachable) still classifies ClassInfra, preserving the existing
// pause+probe behavior for genuine infra faults.
func classifyRevocationError(err error) substrate.FailureClass {
	if substrate.IsInvalidKeyError(err) {
		return substrate.ClassTerminal
	}
	return substrate.ClassInfra
}

// heartbeatIssueSink bridges the ConsumerSupervisor's pause lifecycle to the
// Contract #5 heartbeat's issue set (§2.6's fail-safe half): a paused
// materializer surfaces revocation.consumerDisconnected until it resumes. It
// persists nothing (Load always reports active) — the Gateway is a
// single-instance-per-process materializer with no cross-restart pause state
// to restore; the rich per-consumer health-kv persistence Loom/Weaver use is
// unneeded here.
type heartbeatIssueSink struct {
	hb *Heartbeater
}

func (s *heartbeatIssueSink) SetActive(context.Context) error {
	s.hb.ClearIssue(revocationIssueKey)
	s.hb.SetRevocationConnected(true)
	return nil
}

func (s *heartbeatIssueSink) SetPaused(_ context.Context, reason substrate.PauseReason, lastErr string) error {
	s.hb.SetIssue(revocationIssueKey, severityError, "revocation.consumerDisconnected",
		"token-revocation consumer paused ("+string(reason)+"): "+lastErr)
	s.hb.SetRevocationConnected(false)
	return nil
}

func (s *heartbeatIssueSink) Load(context.Context) (substrate.HealthStatus, substrate.PauseReason, error) {
	return substrate.StatusActive, "", nil
}
